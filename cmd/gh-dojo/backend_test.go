package main

import (
	"context"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// verbDojoConfig is the recorded gh-dojo board with a session, so start's
// comment is "Started. Session: claude://s/abc" (see withSession).
const verbDojoConfig = dojoConfig + `
[session]
env = ["DOJO_SESSION"]
link = "claude://s/{id}"
`

// Hand-crafted fixtures of verbs on issue #3, generated from the recorded
// card_3.json: cards in the states a run reads and the mutations it sends,
// in order. Mutations are never sent to GitHub.
func verbFixture(name string) string { return "testdata/graphql/" + name + ".json" }

// withWarmVerbApp sets up the gh-dojo board with fixtures and a warm schema
// cache, and returns the server with its request count so far.
func withWarmVerbApp(t *testing.T, fixtures ...string) (*graphtest.Server, int) {
	t.Helper()
	withConfig(t, verbDojoConfig)
	withSession(t, "abc")
	srv := withApp(t, append([]string{boardFixture}, fixtures...)...)
	if _, err := newApp().Schema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return srv, srv.Requests()
}

// runs is one gh dojo command of a test, run against the real backend.
type runs struct {
	args     []string
	code     render.Code
	requests int // GraphQL requests the command sends
	golden   string
}

func checkRuns(t *testing.T, srv *graphtest.Server, before int, steps ...runs) {
	t.Helper()
	for _, r := range steps {
		code, out := runOut(r.args...)
		if code != r.code {
			t.Errorf("%q: exit code = %d, want %d\n%s", r.args, code, r.code, out)
		}
		if n := srv.Requests() - before; n != r.requests {
			t.Errorf("%q: %d requests, want %d", r.args, n, r.requests)
		}
		before = srv.Requests()
		testutil.Golden(t, "backend/"+r.golden, out)
	}
}

// TestVerbOnBoard: a verb on an issue on the board is one read and one
// mutation with a warm schema cache.
func TestVerbOnBoard(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("start_ok"))
	checkRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitOK, 2, "start_ok"})
}

// TestVerbNotOnBoard: adding to the board is a mutation of its own, since
// the status update needs the item ID it returns.
func TestVerbNotOnBoard(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("start_not_on_board"))
	checkRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitOK, 3, "start_not_on_board"})
}

// TestVerbArchived: an archived item is unarchived in the same mutation.
func TestVerbArchived(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("start_archived"))
	checkRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitOK, 2, "start_archived"})
}

// TestVerbRepeat:the status and comment are in place already, so a repeat
// reads and writes nothing.
func TestVerbRepeat(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("start_repeat"))
	checkRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitOK, 1, "start_repeat"})
}

// TestVerbPartial: the comment posts but the status write is refused; the
// command exits 5 saying what was done, and the re-run sends only the
// status, leaving the comment alone.
func TestVerbPartial(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("start_partial"))
	checkRuns(t, srv, before,
		runs{[]string{"start", "3"}, render.ExitPartial, 2, "start_partial"},
		runs{[]string{"start", "3"}, render.ExitOK, 2, "start_partial_rerun"},
	)
}

// TestVerbPartialBoard: the issue was added to the board, then the write of
// status and comment had an unknown outcome (502). Exit 5; the re-run finds
// the item and finishes.
func TestVerbPartialBoard(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("start_partial_board"))
	checkRuns(t, srv, before,
		runs{[]string{"start", "3"}, render.ExitPartial, 3, "start_partial_board"},
		runs{[]string{"start", "3"}, render.ExitOK, 2, "start_partial_board_rerun"},
	)
}

// TestVerbWriteUnknown: a mutation whose outcome is lost (502) with nothing
// confirmed before it is exit code 4 "may have been applied", never resent.
func TestVerbWriteUnknown(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("start_502"))
	checkRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitNetwork, 2, "start_502"})
}

// TestVerbStaleOption:GitHub rejects the cached option ID; the command
// drops the schema cache, re-reads the board and the issue, and applies
// what is left once: card, mutation, schema, card, mutation.
func TestVerbStaleOption(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("start_stale"))
	checkRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitOK, 5, "start_stale"})
}

// TestVerbDryRun reads the issue for real and writes nothing.
func TestVerbDryRun(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("card_3"))
	checkRuns(t, srv, before, runs{[]string{"start", "3", "--dry-run"}, render.ExitOK, 1, "start_dry_run"})
}

// TestVerbRefused: wait runs only from In progress; #3 is Ready. Exit 2, no
// mutation.
func TestVerbRefused(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("card_3"))
	checkRuns(t, srv, before, runs{[]string{"wait", "3", "--by", "12"}, render.ExitDenied, 1, "wait_refused"})
}

// TestVerbBoardUnknown: when the card cannot tell whether the issue is on
// the board, the verb neither plans from "not on the board" nor writes:
// exit 4, retry later. The dry run refuses the same way.
func TestVerbBoardUnknown(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("card_3_board_unknown"), verbFixture("card_3_board_unknown"))
	checkRuns(t, srv, before,
		runs{[]string{"start", "3"}, render.ExitNetwork, 1, "start_board_unknown"},
		runs{[]string{"start", "3", "--dry-run"}, render.ExitNetwork, 1, "start_board_unknown"},
	)
}

// TestVerbBoardMismatch:a verb is the first command to load the checked
// schema, so a status the board lacks surfaces as the board check's error,
// before the issue is read.
func TestVerbBoardMismatch(t *testing.T) {
	withConfig(t, dojoConfig+"\n[verbs.start]\nto = \"In progres\"\n")
	srv := withApp(t, boardFixture)
	code, out := runOut("start", "3")
	if code != render.ExitUsage {
		t.Errorf("exit code = %d, want %d\n%s", code, render.ExitUsage, out)
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("%d requests, want 1: the schema only", n)
	}
	testutil.Golden(t, "backend/board_mismatch", out)
}

// Regressions from the review of #12: a stale-ID retry keeps what the first
// attempt did, never repeats a confirmed write, and reports what was
// actually applied.
func TestVerbStaleRetry(t *testing.T) {
	for _, tt := range []struct {
		fixture  string
		code     render.Code
		requests int
	}{
		// Status stale twice: exit 5, and the comment of the first attempt is named.
		{"start_stale_twice", render.ExitPartial, 5},
		// The re-read fails (502, retried 3 times): exit 5 with the comment named.
		{"start_stale_reread_fails", render.ExitPartial, 6},
		// The re-read lags behind the comment just posted: it is not posted again.
		{"start_stale_lag", render.ExitOK, 5},
		// The re-read lags behind the item just added: the status goes on that item.
		{"start_stale_lag_board", render.ExitOK, 6},
		// Someone else moved the issue meanwhile: the line says what this command did.
		{"start_stale_moved", render.ExitOK, 4},
	} {
		t.Run(tt.fixture, func(t *testing.T) {
			srv, before := withWarmVerbApp(t, verbFixture(tt.fixture))
			checkRuns(t, srv, before, runs{[]string{"start", "3"}, tt.code, tt.requests, tt.fixture})
		})
	}
}

// TestVerbErrorsAllDone: GraphQL errors next to a result for every field
// are a warning, not a failure.
func TestVerbErrorsAllDone(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("start_errors_all_done"))
	checkRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitOK, 2, "start_errors_all_done"})
}

// TestVerbBoardNoItem: the issue was added but the second mutation cannot
// be built without the item ID: exit 5 naming the addition.
func TestVerbBoardNoItem(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("start_board_no_item"))
	checkRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitPartial, 2, "start_board_no_item"})
}
