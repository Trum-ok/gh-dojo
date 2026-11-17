package board

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

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
// fields.* must name a board field, the status field must be single-select
// and the sprint field an iteration field, every status the config names
// (config.StatusRefs) must be an option of the status field, and every
// value a verb's field effect sets must fit its board field (FieldInput).
// It needs no network.
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
	Key    string        // the key path, e.g. "verbs.start.to"
	Status string        // the status the board lacks, for a key naming statuses
	Err    *render.Error // as Check reports it, with allowed values and a fix
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
		problems = append(problems, Problem{Key: key, Err: c.Errorf("%s: %s", c.KeyPath(key), e.What).
			WithAllowed(e.Allowed...).WithFix(fix).WithCause(err)})
	}

	var status *Field
	for _, name := range c.FieldNames() {
		key := "fields." + name
		f, err := s.Field(c.Fields[name])
		switch {
		case err != nil:
			add(key, err)
			if name == "sprint" { // e.g. the sprints preset on a board without sprints
				e := problems[len(problems)-1].Err
				e.Fix = fmt.Sprintf("add an iteration field %q on the board (%s), or %s", c.Fields[name], s.URL, e.Fix)
			}
		case name == "status" && f.Type != TypeSingleSelect:
			add(key, s.WrongType(f, TypeSingleSelect))
		case name == "status":
			status = f
		case name == "sprint" && f.Type != TypeIteration:
			add(key, s.WrongType(f, TypeIteration))
		}
	}
	if status != nil {
		for _, ref := range c.StatusRefs() {
			if _, err := status.Option(ref.Status); err != nil {
				add(ref.Key, err)
				problems[len(problems)-1].Status = ref.Status
			}
		}
	}
	for _, v := range c.Verbs {
		for _, fv := range v.Fields {
			if _, _, err := s.FieldInput(fv, c.StatusField(), c.SprintField()); err != nil {
				add(config.FieldKey(v.Name, fv.Field), err)
			}
		}
	}
	return problems
}

// FieldInput returns the field a verb's field effect sets and the value to
// write to it (a ProjectV2FieldValue input): the option of a single-select
// field by name, a text, a number, or a date (YYYY-MM-DD). A field the
// board does not have, or a value that does not fit its type, is an error
// that wraps ErrUnknownField, ErrUnknownOption or ErrFieldType.
//
// reserved are the fields a field effect may not set, the config's status
// and sprint fields (which have their own effects): the allowed values of
// an error leave them out, along with iteration fields.
func (s *Schema) FieldInput(v config.FieldValue, reserved ...string) (*Field, map[string]any, error) {
	var settable []string
	for _, g := range s.Fields {
		if g.Type.Writable() && g.Type != TypeIteration && !slices.Contains(reserved, g.Name) {
			settable = append(settable, g.Name)
		}
	}
	f, err := s.Field(v.Field)
	if err != nil {
		var e *render.Error
		if errors.As(err, &e) {
			e.Allowed = settable
		}
		return nil, nil, err
	}
	typeError := func(format string, a ...any) error {
		return render.Usage("field %q is %s: %s", f.Name, f.Type, fmt.Sprintf(format, a...)).WithCause(ErrFieldType)
	}
	switch f.Type {
	case TypeSingleSelect:
		opt, err := f.Option(v.Text)
		if err != nil {
			return nil, nil, err
		}
		return f, map[string]any{"singleSelectOptionId": opt.ID}, nil
	case TypeText:
		return f, map[string]any{"text": v.Text}, nil
	case TypeNumber:
		n, err := strconv.ParseFloat(v.Text, 64)
		if !v.Number || err != nil {
			return nil, nil, typeError("write a number without quotes, like %s = 3", tomlKey(f.Name))
		}
		return f, map[string]any{"number": n}, nil
	case TypeDate:
		if _, err := time.Parse(time.DateOnly, v.Text); v.Number || err != nil {
			return nil, nil, typeError("write a date, like %s = 2026-11-30", tomlKey(f.Name))
		}
		return f, map[string]any{"date": v.Text}, nil
	case TypeIteration:
		return nil, nil, typeError(`set it with iteration = "current" and fields.sprint = %q`, f.Name)
	}
	return nil, nil, render.Usage("field %q on board %s is %s, which a verb cannot set", f.Name, s.Ref(), f.Type).
		WithAllowed(settable...).WithCause(ErrFieldType)
}

// tomlKey quotes a field name for a TOML example when it needs quotes.
func tomlKey(name string) string {
	key := config.FieldKey("v", name)
	return key[len("verbs.v.field."):]
}

// WrongType is the error for the config's status or sprint field when it
// is not of type want, TypeSingleSelect or TypeIteration; the allowed
// values are the board's fields of that type.
func (s *Schema) WrongType(f *Field, want FieldType) *render.Error {
	kind := "a single-select field"
	if want == TypeIteration {
		kind = "an iteration field"
	}
	var names []string
	for _, g := range s.Fields {
		if g.Type == want {
			names = append(names, g.Name)
		}
	}
	return render.Usage("field %q on board %s is %s, not %s", f.Name, s.Ref(), f.Type, kind).
		WithAllowed(names...).WithCause(ErrFieldType)
}
