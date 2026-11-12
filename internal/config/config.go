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
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/render"
)

// FileName is the config path relative to the repository root.
const FileName = ".github/dojo.toml"

// DocsURL is the README section that documents the config format.
const DocsURL = "https://github.com/Trum-ok/gh-dojo#configuration"

// ErrNoConfig is the cause of the error Find and Load return when there is
// no config file to load; test for it with errors.Is. Any other error means
// a config exists but cannot be read or is invalid.
var ErrNoConfig = errors.New("no " + FileName + " found")

// Version is the only config version this binary understands.
const Version = 1

// Verb arguments that require and require_one_of may name. Each is a CLI
// flag of the verb (--comment, --by, --assignee) and a placeholder in its
// comment template. --assignee is the login a verb with assign =
// "{assignee}" assigns (see Verb.AssignArg); only such a verb takes it.
const (
	ArgComment  = "comment"
	ArgBy       = "by"
	ArgAssignee = "assignee"
)

var (
	args     = []string{ArgComment, ArgBy, ArgAssignee}
	builtins = []string{
		"agent", "block", "comment", "describe", "doctor", "help", "init",
		"list", "new", "next", "release", "show", "unblock",
	}
)

// Args lists the verb arguments in the order they are documented.
func Args() []string { return slices.Clone(args) }

// Builtins lists the command names verbs cannot take: commands that exist
// now or are planned.
func Builtins() []string { return slices.Clone(builtins) }

// Config is a loaded and validated .github/dojo.toml with its preset applied.
type Config struct {
	Path    string // absolute path of the loaded file; empty when built by Parse
	Display string // the file as shown in errors, relative to the start directory

	Version int
	Preset  string // preset name, or "" when the config sets none

	Board    Board
	Fields   map[string]string // logical name → board field name: "status", and "sprint" when set
	Statuses Statuses
	Labels   Labels
	Session  Session
	Verbs    []Verb // preset verbs in preset order, then the repo's own in file order
	Agent    Agent

	origin map[string]string // key path → preset name, for values only the preset set
}

// Board says where the issues live.
type Board struct {
	Owner   string   // user or org login
	Project int      // project number, 1 or greater
	Repos   []string // "owner/name"; the first is the default repo; may be empty
	// Assignee is who a verb with assign = true assigns: Me or a login;
	// "" when not set.
	Assignee string
	// Timezone is board.timezone, an IANA time zone name such as
	// "Europe/Moscow"; "" when not set. Location is that zone, UTC when
	// not set: the sprint effect's "today" and the days issues entered a
	// status are dates in it.
	Timezone string
	Location *time.Location
}

// Loc returns the board's time zone (Board.Location), UTC when unset.
func (b Board) Loc() *time.Location {
	if b.Location == nil {
		return time.UTC
	}
	return b.Location
}

// Zone names the board's time zone for output: "" for UTC, which dates
// leave unsaid, else the IANA name ("Europe/Moscow").
func (b Board) Zone() string {
	if loc := b.Loc(); loc != time.UTC {
		return loc.String()
	}
	return ""
}

// Me is the assignee that stands for the authenticated user.
const Me = "@me"

// AssignFromArg is the value of a verb's assign key that takes the user
// to assign from --assignee when the verb runs (Verb.AssignArg).
const AssignFromArg = "{" + ArgAssignee + "}"

// UnassignAll is the value of a verb's unassign key that removes every
// assignee but the one the verb assigns.
const UnassignAll = "all"

// Close reasons of the close effect, as written in the config.
const (
	CloseCompleted  = "completed"
	CloseNotPlanned = "not_planned"
	CloseDuplicate  = "duplicate"
)

// ConfirmUser is the only value of confirm: the verb runs only with --yes.
const ConfirmUser = "user"

// Values of a verb's iteration key, which sets the sprint field
// (fields.sprint): the iteration of today, the iteration of the day the
// issue last entered a status ("by_status_date:Ready"), or none.
const (
	IterationCurrent      = "current"
	IterationClear        = "clear"
	IterationByStatusDate = "by_status_date"
)

// BuiltinStatus is the name of a board's built-in status field, the only
// one GitHub keeps a status history (ProjectV2ItemStatusChangedEvent) for,
// so the only one iteration = "by_status_date:<status>" can date.
const BuiltinStatus = "Status"

// FieldValue is a board field a verb sets (the field key), with the value
// from the config. The board decides what the value means: an option name
// of a single-select field, a text, a number or a date.
type FieldValue struct {
	Field string // the board field name, as written in the config
	// Text is the value as written: a string, a date as YYYY-MM-DD, or a
	// number in its shortest form ("3", "2.5"). Number tells a TOML number
	// from a string, Date a TOML date.
	Text   string
	Number bool
	Date   bool
}

// Values of a verb's blockers key. Warn and deny are checks of the issue's
// unresolved blockers (see Statuses.Resolved); link is the blockers effect.
const (
	// BlockersWarn: the verb runs, warning on stderr about each unresolved
	// blocker.
	BlockersWarn = "warn"
	// BlockersDeny: the verb is refused (exit code 2) while the issue has an
	// unresolved blocker.
	BlockersDeny = "deny"
	// BlockersLink: a --by naming issues (12, #12, owner/repo#12 or an
	// issue's URL; comma-separated) also marks the issue blocked by them;
	// free text stays comment-only, and so does a pull request.
	BlockersLink = "link"
)

var blockersValues = []string{BlockersWarn, BlockersDeny, BlockersLink}

var closeReasons = []string{CloseCompleted, CloseNotPlanned, CloseDuplicate}

// CloseReasons lists the reasons close accepts.
func CloseReasons() []string { return slices.Clone(closeReasons) }

// Statuses gives meaning to some board statuses. All are optional: without a
// preset they are empty unless the config sets them.
type Statuses struct {
	Initial string   // set by `new` and assumed for issues not on the board yet
	Final   []string // hidden by `list`, with a counter
	// Resolved are the statuses in which an open blocker no longer blocks:
	// its work is complete (kanban: Ready, Done). A closed blocker is
	// resolved whatever its status.
	Resolved []string
}

// Agents gh dojo agent install writes instructions for: a Claude Code
// skill, a section of AGENTS.md (read by Codex and other agents), and a
// section of GitHub Copilot's repository instructions.
const (
	AgentClaude   = "claude"
	AgentAgentsMD = "agents-md"
	AgentCopilot  = "copilot"
)

var agentTargets = []string{AgentClaude, AgentAgentsMD, AgentCopilot}

// agentFiles are the files agent install writes for each agent, relative
// to the repository root.
var agentFiles = map[string]string{
	AgentClaude:   ".claude/skills/gh-dojo/SKILL.md",
	AgentAgentsMD: "AGENTS.md",
	AgentCopilot:  ".github/copilot-instructions.md",
}

// AgentTargets lists the agents [agent] targets and agent install --agent
// accept.
func AgentTargets() []string { return slices.Clone(agentTargets) }

// AgentFile is the file agent install writes for agent, relative to the
// repository root and slash-separated; "" for an unknown agent.
func AgentFile(agent string) string { return agentFiles[agent] }

// Agent is the [agent] table: what gh dojo agent install writes.
type Agent struct {
	// Set says the config has an [agent] table with a key in it.
	Set bool
	// Targets are the agents to write instructions for, in config order;
	// [AgentClaude] when not set, empty when set to [].
	Targets []string
	// Notes is the path of a markdown file, relative to the repository
	// root (the directory holding .github) and slash-separated, appended
	// as is to the generated instructions; "" for none.
	Notes string
	// Description is appended to the generated description of the skill,
	// which says when an agent loads it; "" for none.
	Description string
}

// Labels maps `new` flags to label names.
type Labels struct {
	// Types are the labels `new --type` accepts, used as is (`--type bug` →
	// "bug"). When empty, --type accepts any repo label that does not start
	// with AreaPrefix.
	Types []string
	// AreaPrefix is prepended to `new --area`: `--area cli` → "area:cli".
	AreaPrefix string
}

// Verb is one process verb, such as `start`.
type Verb struct {
	Name         string
	When         string   // one line: when to use the verb; first line of its --help
	To           string   // target status; "" leaves the status alone
	From         []string // statuses the verb may start from; empty means any
	Require      []string // arguments (ArgComment, ArgBy, ArgAssignee) that must all be given
	RequireOneOf []string // arguments of which at least one must be given
	Comment      string   // comment template (see Render); "" posts no comment

	// AddLabels and RemoveLabels are the labels the verb adds ("+name" in
	// labels) and removes ("-name"), in config order.
	AddLabels    []string
	RemoveLabels []string
	// Assign is who the verb assigns: Me or a login; "" assigns nobody,
	// or the --assignee user when AssignArg is set. assign = true is
	// resolved to board.assignee at load.
	Assign string
	// AssignArg says the verb assigns the user --assignee names (assign =
	// "{assignee}"): Me or a login, given when the verb runs, which makes
	// --assignee required.
	AssignArg bool
	// Unassign is who the verb removes from the assignees: Me, a login,
	// or UnassignAll for every assignee but the one the verb assigns; ""
	// removes nobody. unassign = true is resolved to board.assignee at
	// load. The user the verb assigns is never removed.
	Unassign string
	// Close is the reason the verb closes the issue with (CloseCompleted,
	// CloseNotPlanned, CloseDuplicate); "" leaves it open. Reopen reopens
	// a closed issue. A verb never sets both.
	Close  string
	Reopen bool
	// Confirm says the verb is a decision for the user (confirm = "user"):
	// it runs only with --yes, which an agent adds after asking.
	Confirm bool
	// Batch says the verb takes several issue numbers at once (batch =
	// true): it runs on each, skipping the ones that fail, and ends with a
	// summary. Other verbs take exactly one number.
	Batch bool

	// Iteration is how the verb sets the sprint field: IterationCurrent,
	// IterationClear, or IterationByStatusDate with IterationStatus the
	// status whose date picks the iteration; "" leaves the sprint alone.
	Iteration       string
	IterationStatus string
	// Fields are the board fields the verb sets to fixed values, sorted
	// by field name.
	Fields []FieldValue
	// Blockers is the blockers key: BlockersWarn or BlockersDeny check the
	// issue's unresolved blockers, BlockersLink turns --by N into a
	// blocked-by link; "" does neither.
	Blockers string
}

// ChecksBlockers reports whether the verb checks the issue's unresolved
// blockers (blockers = "warn" or "deny").
func (v Verb) ChecksBlockers() bool {
	return v.Blockers == BlockersWarn || v.Blockers == BlockersDeny
}

// OnlyClearsSprint reports whether clearing the sprint (iteration =
// "clear") is the verb's only board effect: no status, no other fields.
// Such a verb leaves an issue that is not on the board off it: there is
// no sprint to clear.
func (v Verb) OnlyClearsSprint() bool {
	return v.Iteration == IterationClear && v.To == "" && len(v.Fields) == 0
}

// IterationRule is the verb's iteration key as written in the config:
// "current", "clear" or "by_status_date:Ready"; "" when not set.
func (v Verb) IterationRule() string {
	if v.Iteration == IterationByStatusDate {
		return IterationByStatusDate + ":" + v.IterationStatus
	}
	return v.Iteration
}

// Uses reports whether the verb's comment template uses the placeholder
// {arg}. Every argument in Require and RequireOneOf is taken (see Takes).
func (v Verb) Uses(arg string) bool {
	for _, m := range placeholderRE.FindAllStringSubmatch(v.Comment, -1) {
		if m[1] == arg {
			return true
		}
	}
	return false
}

// Assigns reports whether the verb assigns someone: a user of the config
// or the --assignee user.
func (v Verb) Assigns() bool { return v.Assign != "" || v.AssignArg }

// Takes reports whether the verb takes the argument arg on the command
// line: its comment template uses {arg}, or, for ArgAssignee, its assign
// effect assigns the --assignee user. A verb rejects the arguments it
// does not take.
func (v Verb) Takes(arg string) bool {
	return v.Uses(arg) || arg == ArgAssignee && v.AssignArg
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
// one of its parents. The search stops after the repository root, the first
// directory that contains .git (a directory, or a file in worktrees and
// submodules), or at the filesystem root outside a repository.
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
		case err != nil && !notExist(err):
			return "", render.Usage("cannot read %s: %v", path, errors.Unwrap(err)).WithCause(err)
		}
		parent := filepath.Dir(abs)
		if parent == abs || isRepoRoot(abs) {
			return "", render.Usage("no %s in this directory or its parents up to the repository root", FileName).
				WithFix("gh dojo init").
				WithCause(ErrNoConfig)
		}
		abs = parent
	}
}

// notExist reports a missing path, including one below a regular file
// (ENOTDIR, e.g. when .github is a file).
func notExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

func isRepoRoot(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// Parse parses and validates config data. display names the file in errors.
func Parse(data []byte, display string) (*Config, error) {
	repo, err := parseRaw(data, display, false)
	if err != nil {
		return nil, err
	}
	c := &Config{Display: display, origin: map[string]string{}}
	var preset *rawFile
	if repo.Preset != nil {
		name := *repo.Preset
		preset, err = loadPreset(name)
		if errors.Is(err, errUnknownPreset) {
			return nil, c.Errorf("unknown preset %q", name).WithAllowed(Presets()...)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := c.checkRepoVerbs(repo, preset); err != nil {
		return nil, err
	}
	merged := repo
	if preset != nil {
		merged = merge(preset, repo, *repo.Preset, c.origin)
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
func (c *Config) StatusField() string { return c.Fields[statusField] }

// SprintField returns the board name of the sprint field, an iteration
// field; "" when fields.sprint is not set.
func (c *Config) SprintField() string { return c.Fields[sprintField] }

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
// statuses.initial, statuses.final, statuses.resolved, then each verb's to,
// from and the status of iteration = "by_status_date:<status>". A status
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
	for _, s := range c.Statuses.Resolved {
		refs = append(refs, StatusRef{s, "statuses.resolved"})
	}
	for _, v := range c.Verbs {
		if v.To != "" {
			refs = append(refs, StatusRef{v.To, "verbs." + v.Name + ".to"})
		}
		for _, s := range v.From {
			refs = append(refs, StatusRef{s, "verbs." + v.Name + ".from"})
		}
		if v.IterationStatus != "" {
			refs = append(refs, StatusRef{v.IterationStatus, "verbs." + v.Name + ".iteration"})
		}
	}
	return refs
}

// FromPreset returns the name of the preset that set key, "" when the
// value comes from the file (or is not set).
func (c *Config) FromPreset(key string) string { return c.origin[key] }

// KeyPath returns key annotated with the preset that set it, if the value
// comes from the preset and not from the file:
// "verbs.start.to (from preset kanban)". A key inside a verb's field table
// (see FieldKey) comes from the preset when the table does.
func (c *Config) KeyPath(key string) string {
	p := c.origin[key]
	if verb, ok := strings.CutPrefix(key, "verbs."); ok && p == "" {
		if name, _, ok := strings.Cut(verb, ".field."); ok {
			p = c.origin["verbs."+name+".field"]
		}
	}
	if p != "" {
		return fmt.Sprintf("%s (from preset %s)", key, p)
	}
	return key
}

// Errorf returns a config error (exit code 1) prefixed with the file name:
// ".github/dojo.toml: <message>". Use KeyPath for the key in the message.
func (c *Config) Errorf(format string, args ...any) *render.Error {
	return render.Usage("%s: %s", c.Display, fmt.Sprintf(format, args...))
}
