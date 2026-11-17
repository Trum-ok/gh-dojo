package workflow

import (
	"testing"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// sprintConfig is kanban with a sprint field: the spec's done (the sprint
// of the day the issue entered Ready), ready setting the same rule (it
// enters Ready itself), and verbs for the other iteration rules and the
// field effect.
const sprintConfig = `
version = 1
preset = "kanban"

[board]
owner = "acme"
project = 3

[fields]
sprint = "Sprint"

[verbs.done]
iteration = "by_status_date:Ready"

[verbs.ready]
iteration = "by_status_date:Ready"

[verbs.ship]
when = "Shipped, from anywhere"
to = "Done"
iteration = "by_status_date:Ready"

[verbs.plan]
when = "Take the issue into the current sprint"
iteration = "current"

[verbs.unplan]
when = "Take the issue out of its sprint"
iteration = "clear"

[verbs.triage]
when = "The issue is triaged"
to = "Backlog"

[verbs.triage.field]
Priority = "P1"
Estimate = 3
`

// sprintIterations are the Sprint field of the iteration board fixture.
var sprintIterations = []board.Iteration{
	{ID: "it12", Title: "Sprint 12", StartDate: "2026-11-09", Duration: 14},
	{ID: "it13", Title: "Sprint 13", StartDate: "2026-11-23", Duration: 14},
	{ID: "it11", Title: "Sprint 11", StartDate: "2026-10-26", Duration: 14, Completed: true},
	{ID: "it10", Title: "Sprint 10", StartDate: "2026-10-12", Duration: 14, Completed: true},
}

var sprintNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func day(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestPlanSprint(t *testing.T) {
	c, err := config.Parse([]byte(sprintConfig), ".github/dojo.toml")
	if err != nil {
		t.Fatal(err)
	}
	// sprint returns the sprint field with the item in iteration id ("" none).
	sprint := func(id string) *Sprint {
		s := &Sprint{Field: "Sprint", Iterations: sprintIterations}
		for _, it := range sprintIterations {
			if it.ID == id {
				s.Current, s.CurrentID = it.Title, it.ID
			}
		}
		return s
	}
	issue := func(status, sprintID string, history ...StatusChange) Issue {
		return Issue{Number: 42, Title: "Fix login redirect", OnBoard: true, Status: status,
			Sprint: sprint(sprintID), History: history, ReadAt: sprintNow}
	}
	enteredReady := StatusChange{At: day("2026-10-30T09:00:00Z"), To: "Ready"}
	started := StatusChange{At: day("2026-10-20T09:00:00Z"), To: "In progress"}
	moscow, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	with := func(is Issue, change func(*Issue)) Issue {
		change(&is)
		return is
	}

	tests := []struct {
		name  string
		verb  string
		issue Issue
		loc   *time.Location
		code  render.Code
		ops   int
		stale bool // the error wraps board.ErrUnknownIteration
	}{
		{name: "done_by_ready_date", verb: "done", ops: 3,
			issue: issue("Ready", "it12", started, enteredReady)},
		{name: "done_last_entry", verb: "done", ops: 3, issue: issue("Ready", "", started, enteredReady,
			StatusChange{At: day("2026-11-02T09:00:00Z"), To: "In progress"},
			StatusChange{At: day("2026-11-10T23:59:00Z"), To: "Ready"})},
		{name: "done_in_place", verb: "done", ops: 2, issue: issue("Ready", "it11", enteredReady)},
		// In progress straight to Done: today's sprint, with a warning.
		{name: "done_never_ready", verb: "done", ops: 2, issue: issue("In progress", "it12", started)},
		{name: "done_never_ready_set", verb: "done", ops: 3, issue: issue("In progress", "it11", started)},
		// In Ready, but its entry is older than the history read: today.
		{name: "done_history_truncated", verb: "done", ops: 3, issue: with(issue("Ready", ""), func(is *Issue) {
			is.HistoryTruncated = true
		})},
		{name: "done_other_status_truncated", verb: "done", ops: 3,
			issue: with(issue("In progress", "", started), func(is *Issue) { is.HistoryTruncated = true })},
		// In Ready, but the last change read is into In progress: the
		// history lags, so the day is unknown rather than the older entry.
		{name: "done_history_lags", verb: "done", ops: 3, issue: issue("Ready", "", enteredReady,
			StatusChange{At: day("2026-11-10T09:00:00Z"), To: "In progress"})},
		// In Ready since it was added to the board, with nothing else read.
		{name: "done_added_in_ready", verb: "done", ops: 3, issue: with(issue("Ready", ""), func(is *Issue) {
			is.AddedAt = day("2026-10-28T10:00:00Z")
		})},
		// A Ready entry of an earlier item, removed from the board, is
		// ignored: the current item was added in Ready on 2026-11-03.
		{name: "done_earlier_item", verb: "done", ops: 3, issue: with(issue("Ready", "", StatusChange{
			At: day("2026-10-14T09:00:00Z"), To: "Ready"}), func(is *Issue) {
			is.AddedAt = day("2026-11-03T10:00:00Z")
		})},
		// Not on the board: its history is an earlier item's.
		{name: "ship_not_on_board", verb: "ship", ops: 3, issue: with(issue("", "", enteredReady), func(is *Issue) {
			is.OnBoard = false
		})},
		{name: "done_no_iteration_today", verb: "done", code: render.ExitUsage, stale: true,
			issue: with(issue("In progress", "", started), func(is *Issue) { is.ReadAt = day("2026-12-20T09:00:00Z") })},
		// The user set the sprint, as the past day's error says.
		{name: "done_no_iteration_sprint_set", verb: "done", ops: 2,
			issue: issue("Ready", "it10", StatusChange{At: day("2026-09-01T09:00:00Z"), To: "Ready"})},
		// A past day after the last iteration.
		{name: "done_no_iteration_after", verb: "done", code: render.ExitUsage, stale: true, issue: with(issue("Ready", "",
			StatusChange{At: day("2026-12-10T09:00:00Z"), To: "Ready"}), func(is *Issue) {
			is.ReadAt = day("2027-01-10T09:00:00Z")
		})},
		// 23:30 UTC on Nov 8 is Nov 9 in Moscow: Sprint 12, not 11.
		{name: "done_timezone", verb: "done", ops: 3, loc: moscow, issue: issue("Ready", "",
			StatusChange{At: day("2026-11-08T23:30:00Z"), To: "Ready"})},
		{name: "done_history_untracked", verb: "done", code: render.ExitUsage, issue: func() Issue {
			is := issue("Ready", "")
			is.HistoryUntracked = true
			return is
		}()},
		{name: "done_no_iteration", verb: "done", code: render.ExitUsage, stale: true,
			issue: issue("Ready", "", StatusChange{At: day("2026-09-01T09:00:00Z"), To: "Ready"})},
		{name: "ready_enters_now", verb: "ready", ops: 2, issue: issue("In progress", "", started)},
		{name: "ready_repeat", verb: "ready", issue: issue("Ready", "it12",
			StatusChange{At: day("2026-11-11T08:00:00Z"), To: "Ready"})},
		// A repeat keeps the sprint it has: #42 went to Done in Sprint 11
		// without entering Ready; today's sprint is 12.
		{name: "done_repeat_keeps_sprint", verb: "done", ops: 1, issue: issue("Done", "it11", started,
			StatusChange{At: day("2026-11-02T09:00:00Z"), To: "Done"})},
		// Entered Done today: the retry of a run that may not have set the
		// sprint, planned as a first run (Sprint 11, the Ready day's).
		{name: "done_repeat_today", verb: "done", ops: 2, issue: issue("Done", "it12", started, enteredReady,
			StatusChange{At: day("2026-11-11T08:00:00Z"), To: "Done"})},
		// No sprint: a repeat sets one (today's, never in Ready).
		{name: "done_repeat_no_sprint", verb: "done", ops: 2, issue: issue("Done", "", started,
			StatusChange{At: day("2026-11-02T09:00:00Z"), To: "Done"})},
		// The history does not show the entry: the sprint stays.
		{name: "done_repeat_history_lags", verb: "done", ops: 1, issue: issue("Done", "it11", started)},
		{name: "plan_current", verb: "plan", ops: 1, issue: issue("Backlog", "")},
		{name: "plan_move", verb: "plan", ops: 1, issue: issue("Backlog", "it11")},
		{name: "plan_no_iteration_today", verb: "plan", code: render.ExitUsage, stale: true, issue: func() Issue {
			is := issue("Backlog", "")
			is.ReadAt = day("2026-12-20T09:00:00Z")
			return is
		}()},
		// 23:30 in New York on Nov 22 is Nov 23 in UTC: Sprint 13.
		{name: "plan_utc", verb: "plan", ops: 1, issue: func() Issue {
			is := issue("Backlog", "it12")
			is.ReadAt = day("2026-11-22T23:30:00-05:00")
			return is
		}()},
		// 22:30 UTC on Nov 22 is Nov 23 in Moscow: Sprint 13.
		{name: "plan_timezone", verb: "plan", ops: 1, loc: moscow, issue: with(issue("Backlog", "it12"), func(is *Issue) {
			is.ReadAt = day("2026-11-22T22:30:00Z")
		})},
		{name: "plan_no_iteration_today_timezone", verb: "plan", code: render.ExitUsage, stale: true, loc: moscow,
			issue: with(issue("Backlog", ""), func(is *Issue) { is.ReadAt = day("2026-12-20T09:00:00Z") })},
		{name: "unplan", verb: "unplan", ops: 1, issue: issue("Backlog", "it12")},
		{name: "unplan_in_place", verb: "unplan", issue: issue("Backlog", "")},
		// Not on the board: nothing to clear, and no reason to add it.
		{name: "unplan_not_on_board", verb: "unplan", issue: with(issue("", ""), func(is *Issue) { is.OnBoard = false })},
		{name: "triage_fields", verb: "triage", ops: 1, issue: func() Issue {
			is := issue("Backlog", "")
			is.Fields = map[string]string{"Priority": "P2"}
			return is
		}()},
		{name: "triage_fields_in_place", verb: "triage", issue: func() Issue {
			is := issue("Backlog", "")
			is.Fields = map[string]string{"Priority": "P1", "Estimate": "3"}
			return is
		}()},
		{name: "triage_fields_some", verb: "triage", ops: 2, issue: func() Issue {
			is := issue("In progress", "")
			is.Fields = map[string]string{"Priority": "P1"}
			return is
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, ok := c.Verb(tt.verb)
			if !ok {
				t.Fatalf("no verb %q", tt.verb)
			}
			e := New(c)
			e.Location = tt.loc
			p, err := e.Plan(v, tt.issue, Args{})
			switch {
			case tt.code != 0:
				if got := render.FromError(err).Code; err == nil || got != tt.code {
					t.Errorf("error %v, want code %d", err, tt.code)
				}
				if board.IsStale(err) != tt.stale {
					t.Errorf("board.IsStale(%v) = %v, want %v", err, !tt.stale, tt.stale)
				}
			case err != nil:
				t.Fatalf("Plan() error: %v", err)
			case len(p.Ops) != tt.ops:
				t.Errorf("plan has %d ops, want %d", len(p.Ops), tt.ops)
			}
			testutil.Golden(t, "plan/sprint_"+tt.name, []byte(show(p, err)))
		})
	}
}
