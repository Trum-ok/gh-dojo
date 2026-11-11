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

	tests := []struct {
		name  string
		verb  string
		issue Issue
		code  render.Code
		ops   int
	}{
		{name: "done_by_ready_date", verb: "done", ops: 3,
			issue: issue("Ready", "it12", started, enteredReady)},
		{name: "done_last_entry", verb: "done", ops: 3, issue: issue("Ready", "", started, enteredReady,
			StatusChange{At: day("2026-11-02T09:00:00Z"), To: "In progress"},
			StatusChange{At: day("2026-11-10T23:59:00Z"), To: "Ready"})},
		{name: "done_in_place", verb: "done", ops: 2, issue: issue("Ready", "it11", enteredReady)},
		{name: "done_never_ready", verb: "done", code: render.ExitDenied, issue: issue("In progress", "", started)},
		{name: "done_history_truncated", verb: "done", code: render.ExitDenied, issue: func() Issue {
			is := issue("Ready", "", started)
			is.HistoryTruncated = true
			return is
		}()},
		{name: "done_history_untracked", verb: "done", code: render.ExitUsage, issue: func() Issue {
			is := issue("Ready", "")
			is.HistoryUntracked = true
			return is
		}()},
		{name: "done_no_iteration", verb: "done", code: render.ExitUsage,
			issue: issue("Ready", "", StatusChange{At: day("2026-09-01T09:00:00Z"), To: "Ready"})},
		{name: "ready_enters_now", verb: "ready", ops: 2, issue: issue("In progress", "", started)},
		{name: "ready_repeat", verb: "ready", issue: issue("Ready", "it12",
			StatusChange{At: day("2026-11-11T08:00:00Z"), To: "Ready"})},
		{name: "plan_current", verb: "plan", ops: 1, issue: issue("Backlog", "")},
		{name: "plan_move", verb: "plan", ops: 1, issue: issue("Backlog", "it11")},
		{name: "plan_no_iteration_today", verb: "plan", code: render.ExitUsage, issue: func() Issue {
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
		{name: "unplan", verb: "unplan", ops: 1, issue: issue("Backlog", "it12")},
		{name: "unplan_in_place", verb: "unplan", issue: issue("Backlog", "")},
		{name: "unplan_unknown", verb: "unplan", ops: 1, issue: func() Issue {
			is := issue("Backlog", "")
			is.Sprint.Unknown = true
			return is
		}()},
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
			p, err := New(c).Plan(v, tt.issue, Args{})
			switch {
			case tt.code != 0:
				if got := render.FromError(err).Code; err == nil || got != tt.code {
					t.Errorf("error %v, want code %d", err, tt.code)
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
