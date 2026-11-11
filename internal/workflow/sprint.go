package workflow

import (
	"fmt"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// planIteration adds the iteration effect of v: the sprint field set to the
// iteration of a day, or cleared. Days are UTC dates:
//
//   - current: the day the issue was read (Issue.ReadAt);
//   - by_status_date:<status>: the day the issue last entered <status> on
//     the board, from its status history; the day it was read when the
//     verb itself moves the issue there now (moves is set).
//
// An issue already in the target iteration, or without one for clear, gets
// it in InPlace. A day no iteration covers is exit code 1 with the
// iterations; a status the history cannot date is refused (see
// statusDate).
func (p *Plan) planIteration(v config.Verb, atTarget bool) error {
	if v.Iteration == "" {
		return nil
	}
	is := p.Issue
	s := is.Sprint
	if s == nil {
		return render.Internal("%s sets the sprint, but the sprint field was not read", v.Name)
	}
	today := is.ReadAt
	if today.IsZero() {
		today = time.Now()
	}
	today = today.UTC()

	op := SetIteration{From: s.Current, Unread: s.Unknown}
	if s.Unknown {
		op.From = ""
	}
	var target *board.Iteration
	var err error
	switch v.Iteration {
	case config.IterationClear:
	case config.IterationCurrent:
		target, err = s.at(today, "today")
	case config.IterationByStatusDate:
		var day time.Time
		var when string
		if day, when, err = statusDate(v, is, today, !atTarget && v.To == v.IterationStatus); err == nil {
			target, err = s.at(day, when)
		}
	default:
		return render.Internal("%s: unknown iteration rule %q", v.Name, v.Iteration)
	}
	if err != nil {
		return err
	}
	if target != nil {
		op.To, op.Start = target.Title, target.StartDate
	}
	switch {
	case s.Unknown:
		p.Ops = append(p.Ops, op)
	case target == nil && s.CurrentID == "",
		target != nil && s.CurrentID == target.ID:
		op.From = op.To
		p.InPlace = append(p.InPlace, op)
	default:
		p.Ops = append(p.Ops, op)
	}
	return nil
}

// statusDate returns the day issue is entered v.IterationStatus, in UTC,
// and how to name it in an error. entersNow says the verb moves the issue
// there itself: the day is today. Otherwise it is the last entry into the
// status in the issue's history. A history GitHub does not keep for the
// status field is a config problem (exit code 1); an issue that never
// entered the status, or entered it before the history read, is refused
// (exit code 2): its sprint is the user's call.
func statusDate(v config.Verb, is Issue, today time.Time, entersNow bool) (time.Time, string, error) {
	status := v.IterationStatus
	if entersNow {
		return today, fmt.Sprintf("today, as #%d enters %s", is.Number, status), nil
	}
	rule := fmt.Sprintf("iteration = %q", v.IterationRule())
	if is.HistoryUntracked {
		return time.Time{}, "", render.Usage("%s cannot date #%d entering %s (%s): GitHub keeps a status history "+
			"only for a board's built-in Status field", v.Name, is.Number, status, rule).
			WithFix(fmt.Sprintf(`set iteration = %q in verbs.%s`, config.IterationCurrent, v.Name))
	}
	for i := len(is.History) - 1; i >= 0; i-- {
		if is.History[i].To == status {
			at := is.History[i].At.UTC()
			return at, fmt.Sprintf("the day #%d entered %s", is.Number, status), nil
		}
	}
	why := "it never entered " + status + " on the board"
	if is.HistoryTruncated {
		why = "it entered " + status + " before the status changes gh dojo reads"
	}
	return time.Time{}, "", render.Denied("%s cannot pick the sprint of #%d by the day it entered %s (%s): %s\n"+
		"ask the user how to proceed", v.Name, is.Number, status, rule, why)
}

// at returns the iteration that covers day, or an error (exit code 1)
// listing the iterations; when names the day in it.
//
//	error: Sprint has no iteration for 2026-12-20 (today)
//	allowed: Sprint 12 (2026-11-09 to 2026-11-22), Sprint 13 (2026-11-23 to 2026-12-06)
//	fix: add an iteration that covers 2026-12-20 to the Sprint field on the board; gh dojo doctor checks today's
func (s *Sprint) at(day time.Time, when string) (*board.Iteration, error) {
	for i := range s.Iterations {
		if s.Iterations[i].Contains(day) {
			return &s.Iterations[i], nil
		}
	}
	date := day.Format(time.DateOnly)
	spans := make([]string, len(s.Iterations))
	for i, it := range s.Iterations {
		spans[i] = it.Span()
	}
	return nil, render.Usage("%s has no iteration for %s (%s)", s.Field, date, when).WithAllowed(spans...).
		WithFix(fmt.Sprintf("add an iteration that covers %s to the %s field on the board; gh dojo doctor checks today's",
			date, s.Field))
}

// planFields adds the field effect of v: the fields whose value differs
// from the config's are set, the rest are in place.
func (p *Plan) planFields(v config.Verb) {
	var set, inPlace []FieldChange
	for _, fv := range v.Fields {
		current, ok := p.Issue.Fields[fv.Field]
		c := FieldChange{Field: fv.Field, From: current, To: fv.Text, Value: fv}
		if ok && current == fv.Text {
			inPlace = append(inPlace, c)
		} else {
			set = append(set, c)
		}
	}
	if len(set) > 0 {
		p.Ops = append(p.Ops, SetFields{Changes: set})
	}
	if len(inPlace) > 0 {
		p.InPlace = append(p.InPlace, SetFields{Changes: inPlace})
	}
}
