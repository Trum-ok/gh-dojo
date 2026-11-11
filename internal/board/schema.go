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

	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Ref identifies a board: owner login (user or organization) and project
// number, as in https://github.com/users/<owner>/projects/<number>.
type Ref struct {
	Owner  string
	Number int
}

func (r Ref) String() string { return fmt.Sprintf("%s/%d", r.Owner, r.Number) }

var loginRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

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

// Lookup failures. Errors returned by the lookup helpers are *render.Error
// (exit code 1) that wrap one of these, so errors.Is tells them apart.
var (
	ErrUnknownField     = errors.New("unknown field")
	ErrUnknownOption    = errors.New("unknown option")
	ErrUnknownIteration = errors.New("unknown iteration")
	ErrFieldType        = errors.New("wrong field type")
)

// IsUnknown reports whether err is an unknown field, option or iteration:
// the kind of error a stale cache causes.
func IsUnknown(err error) bool {
	return errors.Is(err, ErrUnknownField) || errors.Is(err, ErrUnknownOption) || errors.Is(err, ErrUnknownIteration)
}

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

// FieldNames lists all field names in board order, built-in ones included.
func (s *Schema) FieldNames() []string {
	names := make([]string, len(s.Fields))
	for i, f := range s.Fields {
		names[i] = f.Name
	}
	return names
}

// Option returns the single-select option with exactly this name.
func (f *Field) Option(name string) (*Option, error) {
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
		switch d := levenshtein(lower, la); {
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

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
