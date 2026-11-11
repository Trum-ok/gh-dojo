package config

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// rawFile is one TOML file (repo config or preset) as written. Pointers tell
// a key that is absent from one set to an empty value, which matters when the
// repo config is merged over a preset.
type rawFile struct {
	Version  *int                `toml:"version"`
	Preset   *string             `toml:"preset"`
	Board    rawBoard            `toml:"board"`
	Fields   map[string]string   `toml:"fields"`
	Statuses rawStatuses         `toml:"statuses"`
	Labels   rawLabels           `toml:"labels"`
	Session  rawSession          `toml:"session"`
	Verbs    map[string]*rawVerb `toml:"verbs"`

	verbOrder []string // verb names in file order
}

type rawBoard struct {
	Owner    *string   `toml:"owner"`
	Project  *int      `toml:"project"`
	Repos    *[]string `toml:"repos"`
	Assignee *string   `toml:"assignee"`
}

type rawStatuses struct {
	Initial  *string   `toml:"initial"`
	Final    *[]string `toml:"final"`
	Resolved *[]string `toml:"resolved"`
}

type rawLabels struct {
	Types      *[]string `toml:"types"`
	AreaPrefix *string   `toml:"area_prefix"`
}

type rawSession struct {
	Env  *[]string `toml:"env"`
	Link *string   `toml:"link"`
}

type rawVerb struct {
	When         *string    `toml:"when"`
	To           *string    `toml:"to"`
	From         *[]string  `toml:"from"`
	Require      *[]string  `toml:"require"`
	RequireOneOf *[]string  `toml:"require_one_of"`
	Comment      *string    `toml:"comment"`
	Labels       *[]string  `toml:"labels"`
	Assign       *rawAssign `toml:"assign"`
	Close        *string    `toml:"close"`
	Reopen       *bool      `toml:"reopen"`
	Confirm      *string    `toml:"confirm"`
	Disabled     *bool      `toml:"disabled"`
}

// rawAssign is the value of a verb's assign key: true (board.assignee),
// false (nobody, to turn off a preset's assign) or a login ("@me" or a
// user).
type rawAssign struct {
	Bool *bool
	User *string
}

// UnmarshalTOML accepts a boolean or a string.
func (a *rawAssign) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case bool:
		a.Bool = &v
	case string:
		a.User = &v
	default:
		return fmt.Errorf("wrong type: got %s, want string or boolean", typeName(fmt.Sprintf("%T", v)))
	}
	return nil
}

// setKeys lists the keys of v that are set, other than disabled.
func (v *rawVerb) setKeys() []string {
	var keys []string
	for _, k := range []struct {
		name string
		set  bool
	}{
		{"when", v.When != nil}, {"to", v.To != nil}, {"from", v.From != nil},
		{"require", v.Require != nil}, {"require_one_of", v.RequireOneOf != nil},
		{"comment", v.Comment != nil}, {"labels", v.Labels != nil}, {"assign", v.Assign != nil},
		{"close", v.Close != nil}, {"reopen", v.Reopen != nil}, {"confirm", v.Confirm != nil},
	} {
		if k.set {
			keys = append(keys, k.name)
		}
	}
	return keys
}

// Allowed keys per table, in documentation order.
var (
	topKeys      = []string{"version", "preset", "board", "fields", "statuses", "labels", "session", "verbs"}
	presetKeys   = []string{"version", "fields", "statuses", "labels", "session", "verbs"}
	boardKeys    = []string{"owner", "project", "repos", "assignee"}
	statusesKeys = []string{"initial", "final", "resolved"}
	labelsKeys   = []string{"types", "area_prefix"}
	sessionKeys  = []string{"env", "link"}
	verbKeys     = []string{
		"when", "to", "from", "require", "require_one_of", "comment",
		"labels", "assign", "close", "reopen", "confirm", "disabled",
	}

	// unsupportedVerbKeys are verb keys from the spec that this version
	// recognizes but does not implement yet.
	unsupportedVerbKeys = []string{"blockers", "iteration", "field", "batch", "pre", "post"}

	// unsupportedKeys are other keys from the spec that this version
	// recognizes but does not implement yet, with what they are for.
	unsupportedKeys = map[string]string{}
)

// statusField is the only [fields] entry this version uses; other logical
// fields (sprint, ...) are rejected until the effects that use them exist.
const (
	statusField     = "status"
	fieldsLaterNote = "sprint fields and iteration effects come in v0.1"
)

// fieldError is the error for a [fields] entry other than status: status
// in another case is a typo, anything else a field this version does not
// use yet. Nil for status itself.
func fieldError(display, name string) error {
	switch {
	case name == statusField:
		return nil
	case strings.EqualFold(name, statusField):
		return render.Usage("%s: unknown key fields.%s; did you mean fields.%s?", display, name, statusField).
			WithFix(fmt.Sprintf("rename %s to %s in [fields] of %s", name, statusField, display))
	}
	return unsupported(display, "fields."+name, fieldsLaterNote)
}

// fieldsKeyError returns the fieldError of the first [fields] entry that is
// not status, in file order, nil when there is none or data does not parse.
// It reads [fields] values of any type, so an unsupported field written as
// a table ([fields.sprint]) is reported as unsupported rather than as a
// type mismatch.
func fieldsKeyError(data []byte, display string) error {
	var probe struct {
		Fields map[string]any `toml:"fields"`
	}
	md, err := toml.Decode(string(data), &probe)
	if err != nil {
		return nil
	}
	for _, key := range md.Keys() {
		if len(key) == 2 && key[0] == "fields" {
			if err := fieldError(display, key[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// unsupported is the error for a key this version recognizes but does not
// implement; note says what the key is for and when it comes.
func unsupported(display, path, note string) error {
	return render.Usage("%s: %s is not supported in this version (%s)", display, path, note).
		WithFix(fmt.Sprintf("remove %s from %s", path, display))
}

// parseRaw decodes data and rejects unknown and unsupported keys.
func parseRaw(data []byte, display string, preset bool) (*rawFile, error) {
	var raw rawFile
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		if ferr := fieldsKeyError(data, display); ferr != nil {
			return nil, ferr
		}
		return nil, decodeError(display, err)
	}
	// The decoder silently skips a non-table value for a map.
	for _, k := range []string{"fields", "verbs"} {
		if t := md.Type(k); t != "" && t != "Hash" {
			if t == "ArrayHash" {
				t = "array of tables"
			}
			return nil, render.Usage("%s: %s: wrong type: got %s, want table", display, k, strings.ToLower(t))
		}
	}
	undecoded := map[string]bool{}
	for _, key := range md.Undecoded() {
		undecoded[key.String()] = true
	}
	for _, key := range md.Keys() { // file order: the first problem wins
		switch {
		case len(key) == 2 && key[0] == "fields" && key[1] != statusField:
			return nil, fieldError(display, key[1])
		case undecoded[key.String()]:
			return nil, undecodedError(display, key)
		}
	}
	if preset {
		for _, k := range topKeys {
			if !slices.Contains(presetKeys, k) && md.IsDefined(k) {
				return nil, render.Usage("%s: %s is not allowed in a preset", display, k)
			}
		}
		if raw.Version == nil || *raw.Version != Version {
			return nil, render.Usage("%s: version must be %d", display, Version)
		}
	}
	for _, key := range md.Keys() {
		if len(key) >= 2 && key[0] == "verbs" && !slices.Contains(raw.verbOrder, key[1]) {
			raw.verbOrder = append(raw.verbOrder, key[1])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(raw.Verbs)) { // safety net, normally a no-op
		if !slices.Contains(raw.verbOrder, name) {
			raw.verbOrder = append(raw.verbOrder, name)
		}
	}
	return &raw, nil
}

// typeErrorRE matches the decoder's type mismatch errors, which are not
// ParseErrors: `toml: line 3 (last key "board.project"): incompatible types:
// TOML value has type string; destination has type integer`.
var (
	typeErrorRE = regexp.MustCompile(`^toml: (?:line (\d+) )?\(last key "([^"]*)"\): (.*)$`)
	mismatchRE  = []*regexp.Regexp{
		regexp.MustCompile(`^incompatible types: TOML value has type (\S+); destination has type (\S+)$`),
		regexp.MustCompile(`^type mismatch for (\S+): expected table but found (\S+)$`),
	}
)

// typeName turns the decoder's type names, some of them Go types, into TOML
// ones.
func typeName(s string) string {
	switch {
	case strings.HasPrefix(s, "int"), strings.HasPrefix(s, "uint"):
		return "integer"
	case strings.HasPrefix(s, "float"):
		return "float"
	case s == "bool":
		return "boolean"
	case s == "slice", strings.HasPrefix(s, "[]"):
		return "array"
	case s == "map", s == "struct", strings.Contains(s, "."), strings.HasPrefix(s, "map["):
		return "table"
	}
	return s
}

func decodeError(display string, err error) error {
	var perr toml.ParseError
	if errors.As(err, &perr) {
		msg := perr.Message
		if perr.LastKey != "" && !strings.Contains(msg, perr.LastKey) {
			msg = perr.LastKey + ": " + msg
		}
		return render.Usage("%s:%d: %s", display, perr.Position.Line, msg).WithCause(err)
	}
	if m := typeErrorRE.FindStringSubmatch(err.Error()); m != nil {
		where := display
		if m[1] != "" {
			where += ":" + m[1]
		}
		msg := m[3]
		if t := mismatchRE[0].FindStringSubmatch(msg); t != nil {
			msg = fmt.Sprintf("wrong type: got %s, want %s", typeName(t[1]), typeName(t[2]))
		} else if t := mismatchRE[1].FindStringSubmatch(msg); t != nil {
			msg = fmt.Sprintf("wrong type: got %s, want table", typeName(t[2]))
		}
		return render.Usage("%s: %s: %s", where, m[2], msg).WithCause(err)
	}
	return render.Usage("%s: %s", display, strings.TrimPrefix(err.Error(), "toml: ")).WithCause(err)
}

// undecodedError explains a key the decoder did not use. Keys below a
// verb are cut to verbs.<name>.<key>, others to <table>.<key>, so a nested
// table such as [verbs.start.pre] is reported once, by its own name.
func undecodedError(display string, key toml.Key) error {
	var path string
	var allowed []string
	switch {
	case len(key) >= 3 && key[0] == "verbs":
		path = strings.Join(key[:3], ".")
		if slices.Contains(unsupportedVerbKeys, key[2]) {
			return render.Usage("%s: %s is not supported in this version", display, path).
				WithAllowed(verbKeys...)
		}
		allowed = verbKeys
	case len(key) >= 2:
		path = strings.Join(key[:2], ".")
		if note, ok := unsupportedKeys[path]; ok {
			return unsupported(display, path, note)
		}
		switch key[0] {
		case "board":
			allowed = boardKeys
		case "statuses":
			allowed = statusesKeys
		case "labels":
			allowed = labelsKeys
		case "session":
			allowed = sessionKeys
		default:
			// A scalar or array where a table was expected; the decoder
			// reports those itself, so this is only a safety net.
			path, allowed = key[0], topKeys
		}
	default:
		path, allowed = key.String(), topKeys
	}
	return render.Usage("%s: unknown key %s", display, path).WithAllowed(allowed...)
}

// merge applies repo over preset: scalar and list keys override one by one,
// verbs merge key by key (disabled comes from the repo only; build drops
// disabled verbs), and a new verb name adds a verb after the preset's. origin
// records the key paths whose value comes from the preset.
func merge(preset, repo *rawFile, name string, origin map[string]string) *rawFile {
	m := merger{preset: name, origin: origin}
	out := *repo
	out.Fields = map[string]string{}
	for k, v := range preset.Fields {
		out.Fields[k] = v
		origin["fields."+k] = name
	}
	for k, v := range repo.Fields {
		out.Fields[k] = v
		delete(origin, "fields."+k)
	}
	out.Statuses.Initial = pick(m, "statuses.initial", preset.Statuses.Initial, repo.Statuses.Initial)
	out.Statuses.Final = pick(m, "statuses.final", preset.Statuses.Final, repo.Statuses.Final)
	out.Statuses.Resolved = pick(m, "statuses.resolved", preset.Statuses.Resolved, repo.Statuses.Resolved)
	out.Labels.Types = pick(m, "labels.types", preset.Labels.Types, repo.Labels.Types)
	out.Labels.AreaPrefix = pick(m, "labels.area_prefix", preset.Labels.AreaPrefix, repo.Labels.AreaPrefix)
	out.Session.Env = pick(m, "session.env", preset.Session.Env, repo.Session.Env)
	out.Session.Link = pick(m, "session.link", preset.Session.Link, repo.Session.Link)

	out.Verbs = map[string]*rawVerb{}
	out.verbOrder = nil
	for _, n := range append(slices.Clone(preset.verbOrder), repo.verbOrder...) {
		if _, seen := out.Verbs[n]; !seen {
			out.Verbs[n] = mergeVerb(m, n, preset.Verbs[n], repo.Verbs[n])
			out.verbOrder = append(out.verbOrder, n)
		}
	}
	return &out
}

type merger struct {
	preset string
	origin map[string]string
}

func mergeVerb(m merger, name string, base, over *rawVerb) *rawVerb {
	if base == nil {
		return over
	}
	if over == nil {
		over = &rawVerb{}
	}
	p := "verbs." + name + "."
	return &rawVerb{
		When:         pick(m, p+"when", base.When, over.When),
		To:           pick(m, p+"to", base.To, over.To),
		From:         pick(m, p+"from", base.From, over.From),
		Require:      pick(m, p+"require", base.Require, over.Require),
		RequireOneOf: pick(m, p+"require_one_of", base.RequireOneOf, over.RequireOneOf),
		Comment:      pick(m, p+"comment", base.Comment, over.Comment),
		Labels:       pick(m, p+"labels", base.Labels, over.Labels),
		Assign:       pick(m, p+"assign", base.Assign, over.Assign),
		Close:        pick(m, p+"close", base.Close, over.Close),
		Reopen:       pick(m, p+"reopen", base.Reopen, over.Reopen),
		Confirm:      pick(m, p+"confirm", base.Confirm, over.Confirm),
		Disabled:     over.Disabled,
	}
}

// pick returns over if set, else base, recording base's origin.
func pick[T any](m merger, key string, base, over *T) *T {
	if over != nil {
		return over
	}
	if base != nil {
		m.origin[key] = m.preset
	}
	return base
}
