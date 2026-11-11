package workflow

import (
	"fmt"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// planIteration adds the iteration effect of v: the sprint field set to the
// iteration of a day, or cleared. Days are dates in loc, the board's time
// zone (UTC when nil):
//
//   - current: the day the issue was read (Issue.ReadAt);
//   - by_status_date:<status>: the day the issue last entered <status> on
//     the board (see statusDate); the day it was read when the verb itself
//     moves the issue there now, or when that day is unknown (with a
//     warning).
//
// A completed iteration still covers its days. An issue already in the
// target iteration, or without one for clear, gets it in InPlace.
//
// Today without an iteration is exit code 1 with the iterations; so is a
// past day the issue entered the status, unless the issue has a sprint
// already: then the user set it, and it stays. Both errors wrap
// board.ErrUnknownIteration, so board.IsStale holds and a cached schema is
// re-read (an iteration may have been added since).
func (p *Plan) planIteration(v config.Verb, atTarget bool, loc *time.Location) error {
	if v.Iteration == "" {
		return nil
	}
	is := p.Issue
	s := is.Sprint
	if s == nil {
		return render.Internal("%s sets the sprint, but the sprint field was not read", v.Name)
	}
	if loc == nil {
		loc = time.UTC
	}
	now := is.ReadAt
	if now.IsZero() {
		now = time.Now()
	}
	today := now.In(loc)

	op := SetIteration{From: s.Current}
	var target *board.Iteration
	switch v.Iteration {
	case config.IterationClear:
	case config.IterationCurrent:
		if target = s.at(today); target == nil {
			return s.noIteration(sprintDay{at: today, when: "today"})
		}
	case config.IterationByStatusDate:
		d, err := statusDate(v, is, today, !atTarget && v.To == v.IterationStatus)
		if err != nil {
			return err
		}
		if d.warning != "" {
			p.Warnings = append(p.Warnings, d.warning)
		}
		if target = s.at(d.at); target == nil {
			if d.past && s.CurrentID != "" {
				// The fix of the error below: the user set the sprint.
				p.InPlace = append(p.InPlace, SetIteration{From: s.Current, To: s.Current})
				return nil
			}
			e := s.noIteration(d)
			if d.past {
				// No iteration is coming for a past day: the user picks the
				// sprint, or the verb takes today's.
				e = e.WithFix(fmt.Sprintf("set the %s of #%d on the board, or set iteration = %q in verbs.%s",
					s.Field, is.Number, config.IterationCurrent, v.Name))
			}
			return e
		}
	default:
		return render.Internal("%s: unknown iteration rule %q", v.Name, v.Iteration)
	}
	if target != nil {
		op.To, op.Start = target.Title, target.StartDate
	}
	if target == nil && s.CurrentID == "" || target != nil && s.CurrentID == target.ID {
		op.From = op.To
		p.InPlace = append(p.InPlace, op)
	} else {
		p.Ops = append(p.Ops, op)
	}
	return nil
}

// sprintDay is a day the iteration effect picks the iteration of: a time in the
// board's time zone, how to name it in an error ("today", "the day #42
// entered Ready"), whether it is a past day the issue entered a status,
// and a warning when it stands in for a day that is not known.
type sprintDay struct {
	at      time.Time
	when    string
	past    bool
	warning string
}

// String is the day as errors name it: "2026-12-20 (today)", or
// "2026-12-20 (today, Europe/Moscow)" in a time zone other than UTC.
func (d sprintDay) String() string {
	when := d.when
	if zone := d.at.Location(); zone != time.UTC {
		when += ", " + zone.String()
	}
	return d.at.Format(time.DateOnly) + " (" + when + ")"
}

// statusDate returns the day issue is entered v.IterationStatus (X), in
// today's time zone. entersNow says the verb moves the issue there itself:
// the day is today. Otherwise:
//
//   - status changes before the board item was created (Issue.AddedAt)
//     belong to an earlier item and are ignored, as are all of them for
//     an issue not on the board;
//   - an issue in X: the day of the last change, if that change is into X;
//     with no changes read and the history complete, the day it was added
//     to the board (it was added in X). A last change that is not into X
//     (the history lags behind the status) or a truncated history leaves
//     the day unknown;
//   - an issue in another status: the day of the last change into X; none
//     means it never entered X, or entered it before the history read.
//
// An unknown day, or an issue that never entered X, is today, with a
// warning. A history GitHub does not keep for the status field is a
// config problem (exit code 1); the config check refuses it already.
func statusDate(v config.Verb, is Issue, today time.Time, entersNow bool) (sprintDay, error) {
	status := v.IterationStatus
	if entersNow {
		return sprintDay{at: today, when: fmt.Sprintf("today, as #%d enters %s", is.Number, status)}, nil
	}
	if is.HistoryUntracked {
		return sprintDay{}, render.Usage("%s cannot date #%d entering %s (iteration = %q): GitHub keeps a status history "+
			"only for a board's built-in %s field", v.Name, is.Number, status, v.IterationRule(), config.BuiltinStatus).
			WithFix(fmt.Sprintf(`set iteration = %q in verbs.%s`, config.IterationCurrent, v.Name))
	}
	var changes []StatusChange
	for _, c := range is.History {
		if is.OnBoard && (is.AddedAt.IsZero() || !c.At.Before(is.AddedAt)) {
			changes = append(changes, c)
		}
	}
	loc := today.Location()
	entered := func(at time.Time, when string) sprintDay {
		return sprintDay{at: at.In(loc), when: fmt.Sprintf(when, is.Number, status), past: true}
	}
	unknown := func(why string, a ...any) sprintDay {
		return sprintDay{at: today, when: "today", warning: fmt.Sprintf(why, a...) + "; using today's sprint"}
	}
	if is.OnBoard && is.Status == status {
		switch last := len(changes) - 1; {
		case last >= 0 && changes[last].To == status:
			return entered(changes[last].At, "the day #%d entered %s"), nil
		case last >= 0:
			return unknown("#%d is in %s, but its status history does not show it entering %s",
				is.Number, status, status), nil
		case !is.HistoryTruncated && !is.AddedAt.IsZero():
			return entered(is.AddedAt, "the day #%d was added to the board in %s"), nil
		}
		return unknown("#%d entered %s before the status changes gh dojo reads", is.Number, status), nil
	}
	for i := len(changes) - 1; i >= 0; i-- {
		if changes[i].To == status {
			return entered(changes[i].At, "the day #%d entered %s"), nil
		}
	}
	if is.HistoryTruncated {
		return unknown("#%d did not enter %s in the status changes gh dojo reads", is.Number, status), nil
	}
	return unknown("#%d never entered %s", is.Number, status), nil
}

// at returns the iteration that covers the date of t, nil when none does.
// Completed iterations count.
func (s *Sprint) at(t time.Time) *board.Iteration {
	for i := range s.Iterations {
		if s.Iterations[i].Contains(t) {
			return &s.Iterations[i]
		}
	}
	return nil
}

// noIteration is the error for a day no iteration covers (exit code 1),
// with the iterations, wrapping board.ErrUnknownIteration. Its fix is for
// today: add an iteration; planIteration replaces it for a past day.
//
//	error: Sprint has no iteration for 2026-12-20 (today)
//	allowed: Sprint 12 (2026-11-09 to 2026-11-22), Sprint 13 (2026-11-23 to 2026-12-06)
//	fix: add an iteration that covers 2026-12-20 to the Sprint field on the board; gh dojo doctor checks today's
func (s *Sprint) noIteration(d sprintDay) *render.Error {
	spans := make([]string, len(s.Iterations))
	first := -1
	for i, it := range s.Iterations {
		spans[i] = it.Span()
		if first < 0 || it.StartDate < s.Iterations[first].StartDate {
			first = i
		}
	}
	what := fmt.Sprintf("%s has no iteration for %s", s.Field, d)
	if first >= 0 && d.at.Format(time.DateOnly) < s.Iterations[first].StartDate {
		what += fmt.Sprintf(": its first, %s, starts %s", s.Iterations[first].Title, s.Iterations[first].StartDate)
	}
	return render.Usage("%s", what).WithAllowed(spans...).
		WithFix(fmt.Sprintf("add an iteration that covers %s to the %s field on the board; gh dojo doctor checks today's",
			d.at.Format(time.DateOnly), s.Field)).
		WithCause(board.ErrUnknownIteration)
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
