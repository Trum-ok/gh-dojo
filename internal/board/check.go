package board

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// RefOf returns the board a config points at.
func RefOf(c *config.Config) Ref { return Ref{Owner: c.Board.Owner, Number: c.Board.Project} }

// LoadChecked loads the schema of the config's board, from the cache when
// it is fresh, and checks the config against it (see Check). A mismatch
// found on a cached schema drops the cache and checks once more against a
// fresh read, so a status just added on the board is picked up; only then
// is it an error. With a warm cache and a matching config it sends no
// request.
func (l *Loader) LoadChecked(ctx context.Context, c *config.Config) (*Schema, error) {
	s, err := l.Resolve(ctx, RefOf(c), func(s *Schema) error { return Check(c, s) })
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Check reports names in the config that the board does not have: every
// fields.* must name a board field, the status field must be single-select,
// and every status the config names (config.StatusRefs) must be an option of
// it. It needs no network.
//
// The error (exit code 1) shows the first problem with its key path, the
// allowed values in board order and a near-miss suggestion, then how many
// more problems there are and where:
//
//	error: .github/dojo.toml: verbs.start.to: unknown status "In progres"; did you mean "In progress"?
//	and 1 more problem: verbs.wait.to
//	allowed: Backlog, Ready, In progress, In review, Waiting, Done
//	fix: edit verbs.start.to in .github/dojo.toml
//
// It wraps the lookup error (ErrUnknownField, ErrUnknownOption or
// ErrFieldType), so IsStale holds and Loader.Resolve retries on a cached
// schema.
func Check(c *config.Config, s *Schema) error {
	problems := Problems(c, s)
	if len(problems) == 0 {
		return nil
	}
	first := problems[0].Err
	if n := len(problems) - 1; n > 0 {
		noun := "problems"
		if n == 1 {
			noun = "problem"
		}
		var more []string
		for _, p := range problems[1:] {
			more = append(more, p.Key)
		}
		if len(more) > maxMoreKeys {
			more = append(more[:maxMoreKeys:maxMoreKeys], "…")
		}
		first.What += fmt.Sprintf("\nand %d more %s: %s", n, noun, strings.Join(more, ", "))
	}
	return first
}

// maxMoreKeys is how many keys of further problems Check names.
const maxMoreKeys = 4

// Problem is a name in the config that the board does not have.
type Problem struct {
	Key string        // the key path, e.g. "verbs.start.to"
	Err *render.Error // as Check reports it, with allowed values and a fix
}

// Problems lists every name in the config the board does not have, in the
// order Check looks at them; see Check.
func Problems(c *config.Config, s *Schema) []Problem {
	var problems []Problem
	add := func(key string, err error) {
		var e *render.Error
		if !errors.As(err, &e) {
			e = render.FromError(err)
		}
		fix := fmt.Sprintf("edit %s in %s", key, c.Display)
		if c.KeyPath(key) != key { // set by the preset: override it in the file
			fix = fmt.Sprintf("set %s in %s to override preset %s", key, c.Display, c.Preset)
		}
		problems = append(problems, Problem{key, c.Errorf("%s: %s", c.KeyPath(key), e.What).
			WithAllowed(e.Allowed...).WithFix(fix).WithCause(err)})
	}

	var status *Field
	for _, name := range c.FieldNames() {
		key := "fields." + name
		f, err := s.Field(c.Fields[name])
		switch {
		case err != nil:
			add(key, err)
		case name == "status" && f.Type != TypeSingleSelect:
			add(key, s.notSingleSelect(f))
		case name == "status":
			status = f
		}
	}
	if status != nil {
		for _, ref := range c.StatusRefs() {
			if _, err := status.Option(ref.Status); err != nil {
				add(ref.Key, err)
			}
		}
	}
	return problems
}

// notSingleSelect is the error for a status field of another type; the
// allowed values are the board's single-select fields.
func (s *Schema) notSingleSelect(f *Field) error {
	var names []string
	for _, g := range s.Fields {
		if g.Type == TypeSingleSelect {
			names = append(names, g.Name)
		}
	}
	return render.Usage("field %q on board %s is %s, not a single-select field", f.Name, s.Ref(), f.Type).
		WithAllowed(names...).WithCause(ErrFieldType)
}
