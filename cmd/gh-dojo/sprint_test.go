package main

import (
	"context"
	"testing"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// sprintConfig points at the hand-crafted acme/3 board with a Sprint
// iteration field (Sprint 12 covers testNow, 2026-11-11): the spec's done
// (the sprint of the day the issue entered Ready), start and plan (today's
// sprint), unplan (clear) and triage (the field effect).
const sprintConfig = `
version = 1

[board]
owner = "acme"
project = 3
repos = ["acme/shop"]

[fields]
status = "Status"
sprint = "Sprint"

[statuses]
initial = "Todo"
final = ["Done"]

[verbs.start]
when = "You begin working on the issue"
to = "In progress"
iteration = "current"

[verbs.done]
when = "выкачено на прод"
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

[verbs.triage.field]
Priority = "P1"
Estimate = 3
`

const iterationBoardFixture = boardFixtures + "iteration_board.json"

// withSprintApp sets up the acme/3 board with fixtures, a warm schema cache
// and the clock at now (testNow when zero), and returns the server with
// its request count so far.
func withSprintApp(t *testing.T, now time.Time, fixtures ...string) (*graphtest.Server, int) {
	t.Helper()
	withConfig(t, sprintConfig)
	srv := withApp(t, append([]string{iterationBoardFixture}, fixtures...)...)
	if !now.IsZero() {
		inner := newApp // withApp's cleanup restores the original
		newApp = func() *App {
			a := inner()
			a.Loader.Now = func() time.Time { return now }
			return a
		}
	}
	if _, err := newApp().Schema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return srv, srv.Requests()
}

// TestSprintDone is the spec's done: #42 entered Ready on 2026-11-05, so
// the status and Sprint 11 (not today's Sprint 12) go in one mutation
// after one read. The repeat finds both in place: one read.
func TestSprintDone(t *testing.T) {
	srv, before := withSprintApp(t, time.Time{}, verbFixture("sprint_done"))
	checkRuns(t, srv, before,
		runs{[]string{"done", "42"}, render.ExitOK, 2, "sprint_done"},
		runs{[]string{"done", "42"}, render.ExitOK, 1, "sprint_done_repeat"},
	)
}

func TestSprintDoneDryRun(t *testing.T) {
	srv, before := withSprintApp(t, time.Time{}, verbFixture("sprint_done_card"))
	checkRuns(t, srv, before, runs{[]string{"done", "42", "--dry-run"}, render.ExitOK, 1, "sprint_done_dry_run"})
}

// TestSprintDoneNeverReady: the issue never entered Ready, so done cannot
// date its sprint: exit 2 after one read, nothing written.
func TestSprintDoneNeverReady(t *testing.T) {
	srv, before := withSprintApp(t, time.Time{}, verbFixture("sprint_done_never_ready"))
	checkRuns(t, srv, before, runs{[]string{"done", "42"}, render.ExitDenied, 1, "sprint_done_never_ready"})
}

// TestSprintDonePartial: the sprint write is refused, the status is set:
// exit 5 naming both; the re-run sets the sprint alone.
func TestSprintDonePartial(t *testing.T) {
	srv, before := withSprintApp(t, time.Time{}, verbFixture("sprint_done_partial"))
	checkRuns(t, srv, before,
		runs{[]string{"done", "42"}, render.ExitPartial, 2, "sprint_done_partial"},
		runs{[]string{"done", "42"}, render.ExitOK, 2, "sprint_done_partial_rerun"},
	)
}

// TestSprintPlan sets today's iteration: one read, one mutation.
func TestSprintPlan(t *testing.T) {
	srv, before := withSprintApp(t, time.Time{}, verbFixture("sprint_plan"))
	checkRuns(t, srv, before, runs{[]string{"plan", "42"}, render.ExitOK, 2, "sprint_plan"})
}

// TestSprintPlanNoIteration: no iteration covers today (2026-12-20): exit
// 1 with the iterations and doctor, after one read and no write.
func TestSprintPlanNoIteration(t *testing.T) {
	srv, before := withSprintApp(t, time.Date(2026, 12, 20, 9, 0, 0, 0, time.UTC), verbFixture("sprint_plan_gap"))
	checkRuns(t, srv, before, runs{[]string{"plan", "42"}, render.ExitUsage, 1, "sprint_plan_no_iteration"})
}

// TestSprintUnplan clears the sprint with clearProjectV2ItemFieldValue; the
// repeat finds it clear.
func TestSprintUnplan(t *testing.T) {
	srv, before := withSprintApp(t, time.Time{}, verbFixture("sprint_unplan"))
	checkRuns(t, srv, before,
		runs{[]string{"unplan", "42"}, render.ExitOK, 2, "sprint_unplan"},
		runs{[]string{"unplan", "42"}, render.ExitOK, 1, "sprint_unplan_repeat"},
	)
}

// TestSprintTriage sets a number and a single-select field in one
// mutation; the repeat finds both set.
func TestSprintTriage(t *testing.T) {
	srv, before := withSprintApp(t, time.Time{}, verbFixture("sprint_triage"))
	checkRuns(t, srv, before,
		runs{[]string{"triage", "42"}, render.ExitOK, 2, "sprint_triage"},
		runs{[]string{"triage", "42"}, render.ExitOK, 1, "sprint_triage_repeat"},
	)
}

// TestSprintNotOnBoard: the sprint needs the item, so it goes with the
// status in the mutation after the one adding the issue: 3 requests.
func TestSprintNotOnBoard(t *testing.T) {
	srv, before := withSprintApp(t, time.Time{}, verbFixture("sprint_start_not_on_board"))
	checkRuns(t, srv, before, runs{[]string{"start", "42"}, render.ExitOK, 3, "sprint_start_not_on_board"})
}

// TestSprintList filters by the sprint field: today's, by title, none; a
// title the board lacks is an error after one re-read of the board.
func TestSprintList(t *testing.T) {
	srv, before := withSprintApp(t, time.Time{}, verbFixture("sprint_items"), iterationBoardFixture)
	for _, tt := range []struct {
		args     []string
		code     render.Code
		requests int
		golden   string
	}{
		{[]string{"list", "--sprint", "current"}, render.ExitOK, 1, "current"},
		{[]string{"list", "--sprint", "Sprint 11", "--all"}, render.ExitOK, 1, "title"},
		{[]string{"list", "--sprint", "none"}, render.ExitOK, 1, "none"},
		{[]string{"list", "--sprint", "Sprint 14"}, render.ExitUsage, 1, "unknown"},
	} {
		code, out := runOut(tt.args...)
		if code != tt.code {
			t.Errorf("%q: exit code = %d, want %d\n%s", tt.args, code, tt.code, out)
		}
		if n := srv.Requests() - before; n != tt.requests {
			t.Errorf("%q: %d requests, want %d", tt.args, n, tt.requests)
		}
		before = srv.Requests()
		testutil.Golden(t, "list/sprint_"+tt.golden, out)
	}
}

// TestSprintListNoField: --sprint without fields.sprint names the board's
// iteration fields, without reading the items.
func TestSprintListNoField(t *testing.T) {
	withConfig(t, "version = 1\n[board]\nowner = \"acme\"\nproject = 3\n[fields]\nstatus = \"Status\"\n")
	srv := withApp(t, iterationBoardFixture)
	code, out := runOut("list", "--sprint", "current")
	if code != render.ExitUsage || srv.Requests() != 1 {
		t.Errorf("exit code %d, %d requests; want %d, 1 (the board)", code, srv.Requests(), render.ExitUsage)
	}
	testutil.Golden(t, "list/sprint_no_field", out)
}

// TestSprintShow: show prints the sprint with its days on a line of its
// own, and --brief on the status line.
func TestSprintShow(t *testing.T) {
	srv, before := withSprintApp(t, time.Time{}, verbFixture("sprint_show"))
	checkRuns(t, srv, before,
		runs{[]string{"show", "42"}, render.ExitOK, 1, "sprint_show"},
		runs{[]string{"show", "42", "--brief"}, render.ExitOK, 1, "sprint_show_brief"},
	)
}

// TestSprintHelp: the verb help and describe say what the iteration and
// field effects do.
func TestSprintHelp(t *testing.T) {
	withConfig(t, sprintConfig)
	for _, verb := range []string{"done", "plan", "unplan", "triage"} {
		code, out := runOut(verb, "--help")
		if code != render.ExitOK {
			t.Fatalf("%s --help = %d\n%s", verb, code, out)
		}
		testutil.Golden(t, "verbs/help_sprint_"+verb, out)
	}
	code, out := runOut("describe")
	if code != render.ExitOK {
		t.Fatalf("describe = %d\n%s", code, out)
	}
	testutil.Golden(t, "describe/sprint", out)
}
