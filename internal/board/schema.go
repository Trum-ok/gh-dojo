// Package board reads the schema of a GitHub Projects v2 board (project,
// fields, single-select options, iterations) for a user or an organization,
// caches it for an hour and resolves names to IDs with helpful errors.
package board

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Ref identifies a board: owner login (user or organization) and project
// number, as in https://github.com/users/<owner>/projects/<number> or
// https://github.com/orgs/<owner>/projects/<number>.
type Ref struct {
	Owner  string
	Number int
}

func (r Ref) String() string { return fmt.Sprintf("%s/%d", r.Owner, r.Number) }

// loginRE accepts GitHub logins, including Enterprise Managed User logins
// with an "_shortcode" suffix.
var loginRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)

// Validate reports a malformed ref as a usage error (exit code 1).
func (r Ref) Validate() error {
	if !loginRE.MatchString(r.Owner) {
		return render.Usage("invalid board owner %q: want a GitHub user or organization login", r.Owner)
	}
	if r.Number <= 0 {
		return render.Usage("invalid project number %d for %s: want a positive number", r.Number, r.Owner)
	}
	return nil
}

// Owner types, as GitHub names them.
const (
	OwnerUser         = "User"
	OwnerOrganization = "Organization"
)

// FieldType is GitHub's ProjectV2FieldType, e.g. "SINGLE_SELECT".
type FieldType string

// Field types that carry values gh-dojo writes. Built-in fields have other
// types (TITLE, ASSIGNEES, LABELS, ...).
const (
	TypeSingleSelect FieldType = "SINGLE_SELECT"
	TypeIteration    FieldType = "ITERATION"
	TypeText         FieldType = "TEXT"
	TypeNumber       FieldType = "NUMBER"
	TypeDate         FieldType = "DATE"
)

// Writable reports whether items' values of this field type can be set
// (updateProjectV2ItemFieldValue); built-in fields like TITLE or LABELS
// can't.
func (t FieldType) Writable() bool {
	switch t {
	case TypeSingleSelect, TypeIteration, TypeText, TypeNumber, TypeDate:
		return true
	}
	return false
}

// Schema is what gh-dojo needs to know about a board to read and write it.
// Fields, options and iterations keep the board's order.
type Schema struct {
	Owner     string    `json:"owner"`     // login as GitHub spells it
	OwnerType string    `json:"ownerType"` // OwnerUser or OwnerOrganization
	Number    int       `json:"number"`
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Closed    bool      `json:"closed"`
	Fields    []Field   `json:"fields"`
	FetchedAt time.Time `json:"fetchedAt"`

	// Cached is true when the schema came from the cache rather than the
	// network. A lookup failure on a cached schema is worth one re-read.
	Cached bool `json:"-"`
}

// Ref returns the schema's board identity.
func (s *Schema) Ref() Ref { return Ref{Owner: s.Owner, Number: s.Number} }

// Field is a board field.
type Field struct {
	ID   string    `json:"id"`
	Name string    `json:"name"`
	Type FieldType `json:"type"`
	// Options of a single-select field, in board order.
	Options []Option `json:"options,omitempty"`
	// Iterations of an iteration field: current and upcoming ones, then
	// completed ones (Completed set), each group in GitHub's order.
	Iterations []Iteration `json:"iterations,omitempty"`
}

// Option is a single-select option.
type Option struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Iteration is one iteration of an iteration field.
type Iteration struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	StartDate string `json:"startDate"` // YYYY-MM-DD
	Duration  int    `json:"duration"`  // days
	Completed bool   `json:"completed,omitempty"`
}

// Contains reports whether the calendar day of t (in t's location) falls in
// the iteration.
func (it Iteration) Contains(t time.Time) bool {
	start, err := time.Parse(time.DateOnly, it.StartDate)
	if err != nil {
		return false
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return !day.Before(start) && day.Before(start.AddDate(0, 0, it.Duration))
}

// Span names the iteration with its first and last day: "Sprint 12
// (2026-11-09 to 2026-11-22)"; the title alone when the dates are unknown.
func (it Iteration) Span() string {
	start, err := time.Parse(time.DateOnly, it.StartDate)
	if err != nil || it.Duration < 1 {
		return it.Title
	}
	end := start.AddDate(0, 0, it.Duration-1)
	return fmt.Sprintf("%s (%s to %s)", it.Title, it.StartDate, end.Format(time.DateOnly))
}

// Lookup failures. Errors returned by the lookup helpers are *render.Error
// (exit code 1) that wrap one of these, so errors.Is tells them apart.
var (
	ErrUnknownField     = errors.New("unknown field")
	ErrUnknownOption    = errors.New("unknown option")
	ErrUnknownIteration = errors.New("unknown iteration")
	ErrFieldType        = errors.New("wrong field type")
)

// IsUnknown reports whether a lookup failed on an unknown field, option or
// iteration, or on a field whose type is not the expected one: the kind of
// error a stale cached schema causes.
func IsUnknown(err error) bool {
	return errors.Is(err, ErrUnknownField) || errors.Is(err, ErrUnknownOption) ||
		errors.Is(err, ErrUnknownIteration) || errors.Is(err, ErrFieldType)
}

// IsStale reports whether err may come from an out-of-date cached schema:
// a failed lookup (IsUnknown) or an ID GitHub rejected (graph.IsStaleID).
// Invalidate the cache, re-read and try once more; Resolve does that.
func IsStale(err error) bool { return IsUnknown(err) || graph.IsStaleID(err) }

// Field returns the field with exactly this name. Built-in fields (Title,
// Labels, ...) are found too, but the error lists only writable ones.
func (s *Schema) Field(name string) (*Field, error) {
	for i := range s.Fields {
		if s.Fields[i].Name == name {
			return &s.Fields[i], nil
		}
	}
	var writable []string
	for _, f := range s.Fields {
		if f.Type.Writable() {
			writable = append(writable, f.Name)
		}
	}
	return nil, unknown(ErrUnknownField, fmt.Sprintf("field %q on board %s", name, s.Ref()), name, writable)
}

// Option returns the single-select option named name regardless of case,
// as a status is matched (config.SameStatus): "In progress" finds the
// board's "In Progress". A name that two options differing only in case
// match ("Done" and "done") is ambiguous, an error even when one of them
// is spelled exactly so, since the statuses compared later could not tell
// them apart.
func (f *Field) Option(name string) (*Option, error) {
	if f.Type != TypeSingleSelect {
		return nil, f.typeError(TypeSingleSelect)
	}
	var found *Option
	for i := range f.Options {
		o := &f.Options[i]
		if !config.SameStatus(o.Name, name) {
			continue
		}
		if found != nil {
			return nil, render.Usage("%s %q is ambiguous: the board has %q and %q, which differ only in case",
				strings.ToLower(f.Name), name, found.Name, o.Name).
				WithAllowed(f.OptionNames()...).
				WithFix(fmt.Sprintf("rename %q or %q on the board", found.Name, o.Name)).
				WithCause(ErrUnknownOption)
		}
		found = o
	}
	if found != nil {
		return found, nil
	}
	return nil, unknown(ErrUnknownOption, fmt.Sprintf("%s %q", strings.ToLower(f.Name), name), name, f.OptionNames())
}

// exactOption returns the single-select option with exactly this name,
// case included: a field effect's value, which planFields compares with
// the field's value as text.
func (f *Field) exactOption(name string) (*Option, error) {
	if f.Type != TypeSingleSelect {
		return nil, f.typeError(TypeSingleSelect)
	}
	for i := range f.Options {
		if f.Options[i].Name == name {
			return &f.Options[i], nil
		}
	}
	return nil, unknown(ErrUnknownOption, fmt.Sprintf("%s %q", strings.ToLower(f.Name), name), name, f.OptionNames())
}

// Spell returns names as the field's options spell them (see Option), a
// name it has no option for unchanged: a filter GitHub runs gets the
// board's spelling of the config's statuses.
func (f *Field) Spell(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = n
		if o, err := f.Option(n); err == nil {
			out[i] = o.Name
		}
	}
	return out
}

// OptionNames lists single-select option names in board order.
func (f *Field) OptionNames() []string {
	names := make([]string, len(f.Options))
	for i, o := range f.Options {
		names[i] = o.Name
	}
	return names
}

// Iteration returns the iteration with exactly this title.
func (f *Field) Iteration(title string) (*Iteration, error) {
	if f.Type != TypeIteration {
		return nil, f.typeError(TypeIteration)
	}
	for i := range f.Iterations {
		if f.Iterations[i].Title == title {
			return &f.Iterations[i], nil
		}
	}
	return nil, unknown(ErrUnknownIteration, fmt.Sprintf("%s %q", strings.ToLower(f.Name), title), title, f.IterationTitles())
}

// IterationAt returns the iteration containing the day of t, or nil.
func (f *Field) IterationAt(t time.Time) *Iteration {
	for i := range f.Iterations {
		if f.Iterations[i].Contains(t) {
			return &f.Iterations[i]
		}
	}
	return nil
}

// IterationTitles lists iteration titles in field order.
func (f *Field) IterationTitles() []string {
	names := make([]string, len(f.Iterations))
	for i, it := range f.Iterations {
		names[i] = it.Title
	}
	return names
}

func (f *Field) typeError(want FieldType) error {
	return render.Usage("field %q is %s, not %s", f.Name, f.Type, want).WithCause(ErrFieldType)
}

// unknown builds "unknown <what>[; did you mean "X"?]" with the allowed
// values.
func unknown(kind error, what, name string, allowed []string) error {
	msg := "unknown " + what
	if s := suggest(name, allowed); s != "" {
		msg += fmt.Sprintf("; did you mean %q?", s)
	}
	return render.Usage("%s", msg).WithAllowed(allowed...).WithCause(kind)
}

// suggest returns the allowed value closest to name: a case-insensitive
// match, else the single closest one within edit distance 2.
func suggest(name string, allowed []string) string {
	lower := strings.ToLower(name)
	best, bestDist, ties := "", 3, 0
	for _, a := range allowed {
		la := strings.ToLower(a)
		if la == lower {
			return a
		}
		switch d := render.EditDistance(lower, la); {
		case d < bestDist:
			best, bestDist, ties = a, d, 0
		case d == bestDist:
			ties++
		}
	}
	if ties > 0 {
		return ""
	}
	return best
}
