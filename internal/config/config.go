// Package config finds, parses, merges and validates .github/dojo.toml, the
// file that describes a project's board, fields and process verbs.
//
// Load walks up from a directory to the config file, applies the preset it
// names (embedded in the binary) and returns a validated *Config. Every error
// is a *render.Error with exit code 1 that names the file and the key path.
// Checking names against the live board is left to the caller (see
// StatusRefs, Fields, Errorf and KeyPath).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/Trum-ok/gh-dojo/internal/render"
)

// FileName is the config path relative to the repository root.
const FileName = ".github/dojo.toml"

// DocsURL is the README section that documents the config format.
const DocsURL = "https://github.com/Trum-ok/gh-dojo#configuration"

// Version is the only config version this binary understands.
const Version = 1

// Verb arguments that require and require_one_of may name. Each is a CLI
// flag of the verb (--comment, --by) and a placeholder in its comment
// template.
const (
	ArgComment = "comment"
	ArgBy      = "by"
)

// Args lists the verb arguments in the order they are documented.
var Args = []string{ArgComment, ArgBy}

// Builtins are the command names verbs cannot take: commands that exist now
// or are planned.
var Builtins = []string{
	"agent", "block", "comment", "describe", "doctor", "help", "init",
	"list", "new", "next", "release", "show", "unblock",
}

// Config is a loaded and validated .github/dojo.toml with its preset applied.
type Config struct {
	Path    string // absolute path of the loaded file; empty when built by Parse
	Display string // the file as shown in errors, relative to the start directory

	Version int
	Preset  string // preset name, or "" when the config sets none

	Board    Board
	Fields   map[string]string // logical name → board field name; "status" is always set
	Statuses Statuses
	Labels   Labels
	Session  Session
	Verbs    []Verb // preset verbs in preset order, then the repo's own in file order

	origin map[string]string // key path → preset name, for values only the preset set
}

// Board says where the issues live.
type Board struct {
	Owner    string   // user or org login
	Project  int      // project number, 1 or greater
	Repos    []string // "owner/name"; the first is the default repo; may be empty
	Assignee string   // optional, e.g. "@me"
}

// Statuses gives meaning to some board statuses. Both are optional: without a
// preset they are empty unless the config sets them.
type Statuses struct {
	Initial string   // set by `new` and assumed for issues not on the board yet
	Final   []string // hidden by `list`, with a counter
}

// Labels maps `new` flags to label names: `--type bug` → TypePrefix+"bug",
// `--area cli` → AreaPrefix+"cli".
type Labels struct {
	TypePrefix string
	AreaPrefix string
}

// Verb is one process verb, such as `start`.
type Verb struct {
	Name         string
	When         string   // one line: when to use the verb; first line of its --help
	To           string   // target status; "" leaves the status alone
	From         []string // statuses the verb may start from; empty means any
	Require      []string // arguments (ArgComment, ArgBy) that must all be given
	RequireOneOf []string // arguments of which at least one must be given
	Comment      string   // comment template (see Render); "" posts no comment
}

// StatusRef is a status name the config refers to, with the key path that
// refers to it, e.g. {"In progress", "verbs.start.to"}.
type StatusRef struct {
	Status string
	Key    string
}

// Load finds the config by walking up from dir (see Find), parses it and
// applies its preset.
func Load(dir string) (*Config, error) {
	path, err := Find(dir)
	if err != nil {
		return nil, err
	}
	display := path
	if abs, err := filepath.Abs(dir); err == nil {
		if rel, err := filepath.Rel(abs, path); err == nil {
			display = rel
		}
	}
	display = filepath.ToSlash(display)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, render.Usage("%s: cannot read: %v", display, errors.Unwrap(err)).WithCause(err)
	}
	c, err := Parse(data, display)
	if err != nil {
		return nil, err
	}
	c.Path = path
	return c, nil
}

// Find returns the absolute path of the nearest .github/dojo.toml in dir or
// one of its parents, up to the filesystem root.
func Find(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", render.Usage("cannot resolve directory %s: %v", dir, err).WithCause(err)
	}
	for {
		path := filepath.Join(abs, filepath.FromSlash(FileName))
		info, err := os.Stat(path)
		switch {
		case err == nil && !info.IsDir():
			return path, nil
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return "", render.Usage("cannot read %s: %v", path, errors.Unwrap(err)).WithCause(err)
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", render.Usage("no %s in this directory or any parent", FileName).
				WithFix(fmt.Sprintf("create %s in the repository root, example: %s", FileName, DocsURL))
		}
		abs = parent
	}
}

// Parse parses and validates config data. display names the file in errors.
func Parse(data []byte, display string) (*Config, error) {
	repo, err := parseRaw(data, display, false)
	if err != nil {
		return nil, err
	}
	c := &Config{Display: display, origin: map[string]string{}}
	merged := repo
	if repo.Preset != nil {
		name := *repo.Preset
		preset, err := loadPreset(name)
		if errors.Is(err, errUnknownPreset) {
			return nil, c.Errorf("unknown preset %q", name).WithAllowed(Presets()...)
		}
		if err != nil {
			return nil, err
		}
		merged = merge(preset, repo, name, c.origin)
	}
	if err := c.build(merged); err != nil {
		return nil, err
	}
	return c, nil
}

// Verb returns the verb with the given name.
func (c *Config) Verb(name string) (Verb, bool) {
	for _, v := range c.Verbs {
		if v.Name == name {
			return v, true
		}
	}
	return Verb{}, false
}

// VerbNames lists the verb names in config order.
func (c *Config) VerbNames() []string {
	names := make([]string, len(c.Verbs))
	for i, v := range c.Verbs {
		names[i] = v.Name
	}
	return names
}

// StatusField returns the board name of the status field.
func (c *Config) StatusField() string { return c.Fields["status"] }

// FieldNames lists the logical field names, "status" first, then sorted.
func (c *Config) FieldNames() []string {
	names := make([]string, 0, len(c.Fields))
	for n := range c.Fields {
		if n != "status" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return append([]string{"status"}, names...)
}

// StatusRefs lists every status the config names, in a stable order:
// statuses.initial, statuses.final, then each verb's to and from. A status
// named in several places appears once per place, so a check against the
// board can point at the exact key.
func (c *Config) StatusRefs() []StatusRef {
	var refs []StatusRef
	if c.Statuses.Initial != "" {
		refs = append(refs, StatusRef{c.Statuses.Initial, "statuses.initial"})
	}
	for _, s := range c.Statuses.Final {
		refs = append(refs, StatusRef{s, "statuses.final"})
	}
	for _, v := range c.Verbs {
		if v.To != "" {
			refs = append(refs, StatusRef{v.To, "verbs." + v.Name + ".to"})
		}
		for _, s := range v.From {
			refs = append(refs, StatusRef{s, "verbs." + v.Name + ".from"})
		}
	}
	return refs
}

// KeyPath returns key annotated with the preset that set it, if the value
// comes from the preset and not from the file:
// "verbs.start.to (from preset kanban)".
func (c *Config) KeyPath(key string) string {
	if p := c.origin[key]; p != "" {
		return fmt.Sprintf("%s (from preset %s)", key, p)
	}
	return key
}

// Errorf returns a config error (exit code 1) prefixed with the file name:
// ".github/dojo.toml: <message>". Use KeyPath for the key in the message.
func (c *Config) Errorf(format string, args ...any) *render.Error {
	return render.Usage("%s: %s", c.Display, fmt.Sprintf(format, args...))
}
