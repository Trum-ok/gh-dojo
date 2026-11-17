package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/effects"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// The sprint (fields.sprint, an iteration field) and the field effect.
// Dates are UTC: "today" is the UTC date of the moment a verb reads the
// issue, and an iteration covers a day from its start date for its
// duration in days.

// iterationText says what the iteration effect of v does, for describe
// and the verb's help: "today's iteration", "the iteration of the day it
// entered Ready", or "" for clear and no effect.
func iterationText(v config.Verb) string {
	switch v.Iteration {
	case config.IterationCurrent:
		return "today's iteration"
	case config.IterationByStatusDate:
		return "the iteration of the day it entered " + v.IterationStatus
	}
	return ""
}

// describeIteration is the describe phrase of the iteration effect.
func describeIteration(_ *config.Config, v config.Verb) string {
	switch v.Iteration {
	case "":
		return ""
	case config.IterationClear:
		if v.OnlyClearsSprint() {
			return "clears the sprint; an issue not on the board stays off it"
		}
		return "clears the sprint"
	}
	return "sprint: " + iterationText(v)
}

// fieldsText lists the fields of v with their values: "Priority P1,
// Estimate 3".
func fieldsText(v config.Verb) string {
	parts := make([]string, len(v.Fields))
	for i, f := range v.Fields {
		parts[i] = f.Field + " " + f.Text
	}
	return strings.Join(parts, ", ")
}

// describeFields is the describe phrase of the field effect.
func describeFields(_ *config.Config, v config.Verb) string {
	if len(v.Fields) == 0 {
		return ""
	}
	return "sets " + fieldsText(v)
}

// sprintHelp adds the verb help lines of the iteration and field effects.
func sprintHelp(cfg *config.Config, v config.Verb, line helpLine) {
	zone := "UTC"
	if z := cfg.Board.Zone(); z != "" {
		zone = z
	}
	switch v.Iteration {
	case "":
	case config.IterationClear:
		line("Sprint", "clears %s; an issue without one stays as it is", cfg.SprintField())
	case config.IterationByStatusDate:
		also := ""
		if v.To == v.IterationStatus {
			also = " (today, when this verb moves it there)"
		}
		line("Sprint", "sets %s to %s%s; dates are in %s", cfg.SprintField(), iterationText(v), also, zone)
		line("", "never entered %s, or the day is unknown: today's iteration, with a warning", v.IterationStatus)
		line("", "a past day without an iteration is an error, unless the issue has a sprint already")
		line("", "none for today is an error, see gh dojo doctor")
	default:
		line("Sprint", "sets %s to %s (dates in %s); none for today is an error, see gh dojo doctor",
			cfg.SprintField(), iterationText(v), zone)
	}
	if v.Iteration != "" && v.Iteration != config.IterationClear && v.To != "" {
		line("", "a repeat (already in %s) keeps the sprint it has, unless it entered %s today", v.To, v.To)
	}
	if len(v.Fields) > 0 {
		line("Fields", "sets %s; a field already at its value is left alone", fieldsText(v))
	}
}

// list --sprint values besides iteration titles.
const (
	sprintCurrent = "current"
	sprintNone    = "none"
)

// checkSprintFlag validates list --sprint against the sprint field and
// sets o.sprintIt: the iteration covering today (in board.timezone) for
// "current", the iteration with that title, or nil for "none". An
// iteration titled "current" or "none" wins over the keyword. Errors have
// exit code 1 and list the iterations; a title several iterations share
// lists them with their days. No iteration for today wraps
// board.ErrUnknownIteration, so list re-reads a cached schema first.
func checkSprintFlag(s *board.Schema, cfg *config.Config, o *listOpts, now time.Time) error {
	o.sprintIt = nil
	if o.sprint == "" {
		return nil
	}
	if cfg.SprintField() == "" {
		e := cfg.Errorf("--sprint needs fields.sprint, the board's iteration field").
			WithFix(`add sprint = "<iteration field>" under [fields] in ` + cfg.Display)
		var names []string
		for _, f := range s.Fields {
			if f.Type == board.TypeIteration {
				names = append(names, f.Name)
			}
		}
		return e.WithAllowed(names...)
	}
	f, err := sprintField(cfg, s)
	if err != nil {
		return err
	}
	var titled []string
	for i, it := range f.Iterations {
		if it.Title == o.sprint {
			o.sprintIt = &f.Iterations[i]
			titled = append(titled, it.Span())
		}
	}
	switch {
	case len(titled) > 1:
		o.sprintIt = nil
		return render.Usage("%s has %d iterations titled %q", f.Name, len(titled), o.sprint).WithAllowed(titled...).
			WithFix(fmt.Sprintf("rename one in the %s field settings on the board: %s/settings", f.Name, s.URL))
	case o.sprintIt != nil:
		return nil
	}
	switch strings.ToLower(o.sprint) {
	case sprintNone:
		return nil
	case sprintCurrent:
		today := now.In(cfg.Board.Loc())
		if o.sprintIt = f.IterationAt(today); o.sprintIt == nil {
			return render.Usage("%s has no iteration for today (%s)", f.Name, dateIn(today)).
				WithAllowed(f.IterationTitles()...).WithFix("gh dojo doctor").WithCause(board.ErrUnknownIteration)
		}
		return nil
	}
	_, err = f.Iteration(o.sprint)
	var e *render.Error
	if errors.As(err, &e) {
		e.Allowed = append(slices.Clone(e.Allowed), sprintCurrent, sprintNone)
	}
	return err
}

// dateIn is the date of t, with its time zone unless UTC: "2026-11-11",
// "2026-11-11, Europe/Moscow".
func dateIn(t time.Time) string {
	if loc := t.Location(); loc != time.UTC {
		return t.Format(time.DateOnly) + ", " + loc.String()
	}
	return t.Format(time.DateOnly)
}

// showSprint returns the sprint field's name and ID for show's cards; both
// "" without fields.sprint. The schema is the one the cards were read
// with: no request.
func showSprint(ctx context.Context, a *App, cfg *config.Config) (name, id string) {
	if cfg.SprintField() == "" {
		return "", ""
	}
	if s, err := a.Schema(ctx); err == nil {
		if f, err := s.Field(cfg.SprintField()); err == nil {
			return f.Name, f.ID
		}
	}
	return cfg.SprintField(), ""
}

// isSprint reports whether val is the item's value of the sprint field.
func (v cardView) isSprint(val issue.Value) bool {
	if v.sprintField == "" || val.Type != board.TypeIteration {
		return false
	}
	if v.sprintFieldID != "" {
		return val.FieldID == v.sprintFieldID
	}
	return val.Field == v.sprintField
}

// sprint is the card's sprint: the iteration with its days, "none", or
// why it is not known.
func (v cardView) sprint(c *issue.Card) string {
	it, unknown := v.sprintOf(c)
	switch {
	case unknown == sprintBoardUnknown:
		return "unknown (" + unknown + ")"
	case unknown != "":
		return "not read (" + unknown + ")"
	case it != nil:
		return it.Span()
	case c.Item == nil:
		return "none (not on the board)"
	}
	return "none"
}

// Why the sprint of a card is not known, for sprintOf.
const (
	sprintBoardUnknown = "board membership unknown"
	sprintNotRead      = "more fields than read"
)

// sprintOf returns the card's iteration of the sprint field, nil for none;
// unknown says why it is not known ("" when it is).
func (v cardView) sprintOf(c *issue.Card) (it *board.Iteration, unknown string) {
	switch {
	case c.BoardUnknown:
		return nil, sprintBoardUnknown
	case c.Item == nil:
		return nil, ""
	}
	for _, val := range c.Item.Values {
		if v.isSprint(val) {
			return &board.Iteration{Title: val.Text, StartDate: val.StartDate, Duration: val.Duration}, ""
		}
	}
	if c.Item.ValuesTruncated {
		return nil, sprintNotRead
	}
	return nil, ""
}

// Now is the App's clock: its loader's (tests fix it), else the system's.
func (a *App) Now() time.Time {
	if a.Loader != nil && a.Loader.Now != nil {
		return a.Loader.Now()
	}
	return time.Now()
}

// fieldState fills what the iteration and field effects of v plan with:
// the time of the read, the status history, the sprint field with the
// item's iteration, and the item's values of the fields v sets. The
// schema is the one the card was read with, so it costs no request.
// failed fails this card alone: its sprint was not read (see itemSprint).
// err fails every card: the config or the board schema is wrong.
func (b *githubBackend) fieldState(ctx context.Context, v config.Verb, card *issue.Card, is *workflow.Issue) (failed, err error) {
	is.ReadAt = b.app.Now()
	if v.Iteration == "" && len(v.Fields) == 0 {
		return nil, nil
	}
	cfg, err := b.app.Config()
	if err != nil {
		return nil, err
	}
	s, err := b.app.Schema(ctx)
	if err != nil {
		return nil, err
	}
	if v.Iteration != "" {
		f, err := sprintField(cfg, s)
		if err != nil {
			return nil, err
		}
		if is.Sprint, failed = itemSprint(f, card); failed != nil {
			return failed, nil
		}
		if card.Item != nil {
			is.AddedAt = card.Item.CreatedAt
		}
		for _, h := range card.History {
			is.History = append(is.History, workflow.StatusChange{At: h.At, To: h.To})
		}
		is.HistoryTruncated, is.HistoryUntracked = card.HistoryTruncated, card.HistoryUntracked
	}
	if len(v.Fields) > 0 && card.Item != nil {
		is.Fields = map[string]string{}
		for _, fv := range v.Fields {
			f, err := s.Field(fv.Field)
			if err != nil {
				return nil, err
			}
			if val, ok := card.Item.ValueByID(f.ID); ok {
				is.Fields[fv.Field] = val.Text
			}
		}
	}
	return nil, nil
}

// sprintField returns the board's sprint field, which the schema check
// found to be an iteration field.
func sprintField(cfg *config.Config, s *board.Schema) (*board.Field, error) {
	name := cfg.SprintField()
	if name == "" {
		return nil, cfg.Errorf("fields.sprint is not set").
			WithFix(`add sprint = "<iteration field>" under [fields] in ` + cfg.Display)
	}
	f, err := s.Field(name)
	if err != nil {
		return nil, err
	}
	if f.Type != board.TypeIteration {
		return nil, s.WrongType(f, board.TypeIteration).WithFix("edit fields.sprint in " + cfg.Display)
	}
	return f, nil
}

// itemSprint is the sprint field f with the card item's iteration. An item
// whose values were not all read and do not include f may have one: that
// is an internal error, since GitHub allows fewer fields on a board than
// the values a card reads.
func itemSprint(f *board.Field, c *issue.Card) (*workflow.Sprint, error) {
	sp := &workflow.Sprint{Field: f.Name, Iterations: f.Iterations}
	if c.Item == nil {
		return sp, nil
	}
	if val, ok := c.Item.ValueByID(f.ID); ok {
		sp.Current, sp.CurrentID = val.Text, val.IterationID
	} else if c.Item.ValuesTruncated {
		return nil, render.Internal("the %s of #%d was not read: its board item has more field values than read", f.Name, c.Number)
	}
	return sp, nil
}

// fieldIDs adds the IDs of the sprint field, the iteration to set and the
// fields SetFields sets, from the schema. A name the schema lacks wraps a
// board lookup error, so a stale cached schema is re-read (board.IsStale).
func (b *githubBackend) fieldIDs(s *board.Schema, ops []workflow.Op, ids *effects.IDs) error {
	for _, op := range ops {
		switch op := op.(type) {
		case workflow.SetIteration:
			cfg, err := b.app.Config()
			if err != nil {
				return err
			}
			f, err := sprintField(cfg, s)
			if err != nil {
				return err
			}
			ids.SprintField = f.ID
			if op.To == "" {
				continue
			}
			it, err := f.Iteration(op.To)
			for i := range f.Iterations { // a title may repeat: the start date tells
				if c := &f.Iterations[i]; c.Title == op.To && c.StartDate == op.Start {
					it, err = c, nil
				}
			}
			if err != nil {
				return err
			}
			ids.Iteration = it.ID
		case workflow.SetFields:
			cfg, err := b.app.Config()
			if err != nil {
				return err
			}
			ids.Fields = map[string]effects.FieldInput{}
			for _, c := range op.Changes {
				f, value, err := s.FieldInput(c.Value, cfg.StatusField(), cfg.SprintField())
				if err != nil {
					return err
				}
				ids.Fields[c.Field] = effects.FieldInput{ID: f.ID, Value: value}
			}
		}
	}
	return nil
}

// SchemaCached reports whether the board schema the backend reads and
// writes with came from the cache (see batch.ReadPlan).
func (b *githubBackend) SchemaCached() bool { return b.app.SchemaCached() }

// SchemaCached reports whether the schema the App keeps came from the
// cache; false before Schema loaded one.
func (a *App) SchemaCached() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.schema != nil && a.schema.Cached
}
