package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// effectsDojoConfig is the recorded gh-dojo board with verbs that use the
// effects close, reopen, labels and assign: the spec's drop as is,
// kanban's done (closes as completed), review (labels and board.assignee
// "@me"), revive (reopen), and verbs with a missing label and a user who
// cannot be assigned. The fixtures are hand-crafted from card_3.json.
var effectsDojoConfig = strings.Replace(verbDojoConfig, "repos = [\"Trum-ok/gh-dojo\"]\n",
	"repos = [\"Trum-ok/gh-dojo\"]\nassignee = \"@me\"\n", 1) + `
[verbs.drop]
when = "задача не нужна или дубль"
close = "not_planned"
require = ["comment"]
confirm = "user"

[verbs.review]
when = "A pull request waits for review"
to = "In review"
from = ["In progress", "Ready"]
labels = ["+enhancement", "-dogfood"]
assign = true
comment = "{comment}"

[verbs.revive]
when = "A closed issue is needed after all"
to = "Backlog"
reopen = true

[verbs.tag]
when = "Tag it"
labels = ["+enhancment"]

[verbs.give]
when = "Hand it over"
assign = "octo-nobody"
`

// withEffectsApp is withWarmVerbApp with effectsDojoConfig.
func withEffectsApp(t *testing.T, fixtures ...string) (*graphtest.Server, int) {
	t.Helper()
	withConfig(t, effectsDojoConfig)
	withSession(t, "abc")
	srv := withApp(t, append([]string{boardFixture}, fixtures...)...)
	if _, err := newApp().Schema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return srv, srv.Requests()
}

// TestVerbDrop is the spec's drop end to end: without --yes the user is
// asked (exit 2) after one read and nothing is written; with --yes the
// comment and the close go in one mutation; a repeat finds both in place
// and needs no --yes.
func TestVerbDrop(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("drop_ok"))
	checkRuns(t, srv, before,
		runs{[]string{"drop", "3", "--comment", "Duplicate of #2."}, render.ExitDenied, 1, "drop_unconfirmed"},
		runs{[]string{"drop", "3", "--comment", "Duplicate of #2.", "--yes"}, render.ExitOK, 2, "drop_ok"},
		runs{[]string{"drop", "3", "--comment", "Duplicate of #2.", "--yes"}, render.ExitOK, 1, "drop_repeat"},
		runs{[]string{"drop", "3", "--comment", "Duplicate of #2."}, render.ExitOK, 1, "drop_repeat"},
	)
}

// TestVerbDropDryRun shows the plan and that it needs --yes; a dry run
// changes nothing, so it does not refuse.
func TestVerbDropDryRun(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("card_3"))
	checkRuns(t, srv, before,
		runs{[]string{"drop", "3", "--comment", "Duplicate of #2.", "--dry-run"}, render.ExitOK, 1, "drop_dry_run"})
}

// TestVerbDoneCloses: kanban's done sets Done and closes the issue as
// completed in one mutation.
func TestVerbDoneCloses(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("done_close"))
	checkRuns(t, srv, before, runs{[]string{"done", "3"}, render.ExitOK, 2, "done_close"})
}

// TestVerbReopen: the reopen and the status change go in one mutation.
func TestVerbReopen(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("revive_ok"))
	checkRuns(t, srv, before, runs{[]string{"revive", "3"}, render.ExitOK, 2, "revive_ok"})
}

// TestVerbLabelsAssign: label and viewer IDs come with the card, so status,
// labels, assignee and comment are one read and one mutation; the repeat
// is one read.
func TestVerbLabelsAssign(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("review_ok"))
	checkRuns(t, srv, before,
		runs{[]string{"review", "3", "--comment", "PR #50"}, render.ExitOK, 2, "review_ok"},
		runs{[]string{"review", "3", "--comment", "PR #50"}, render.ExitOK, 1, "review_repeat"},
	)
}

// TestVerbLabelsDryRun reads the card with its lookups and writes nothing.
func TestVerbLabelsDryRun(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("review_card"))
	checkRuns(t, srv, before,
		runs{[]string{"review", "3", "--comment", "PR #50", "--dry-run"}, render.ExitOK, 1, "review_dry_run"})
}

// TestVerbAssignPartial: the assignee is refused and the rest applies:
// exit 5 naming both. The re-run sends the assignee alone.
func TestVerbAssignPartial(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("review_partial"))
	checkRuns(t, srv, before,
		runs{[]string{"review", "3", "--comment", "PR #50"}, render.ExitPartial, 2, "review_partial"},
		runs{[]string{"review", "3", "--comment", "PR #50"}, render.ExitOK, 2, "review_partial_rerun"},
	)
}

// TestVerbLabelsStale: a stale status option. Labels, assignee and comment
// applied by the first mutation are not sent again, and the line names
// them: card, mutation, schema, card, mutation.
func TestVerbLabelsStale(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("review_stale"))
	checkRuns(t, srv, before, runs{[]string{"review", "3", "--comment", "PR #50"}, render.ExitOK, 5, "review_stale"})
}

// TestVerbUnknownLabelOrUser: a label missing from the repo or a user who
// cannot be assigned is a config error with the allowed values (one more
// read), before any write.
func TestVerbUnknownLabelOrUser(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("label_unknown"), verbFixture("assignee_unknown"))
	checkRuns(t, srv, before,
		runs{[]string{"tag", "3"}, render.ExitUsage, 2, "label_unknown"},
		runs{[]string{"give", "3"}, render.ExitUsage, 2, "assignee_unknown"},
	)
}

// TestVerbEffectsHelp: the help of a verb says what it does besides the
// status, and that a confirm verb needs the user and --yes.
func TestVerbEffectsHelp(t *testing.T) {
	withConfig(t, effectsDojoConfig)
	for _, verb := range []string{"drop", "review", "revive", "done"} {
		code, out := runOut(verb, "--help")
		if code != render.ExitOK {
			t.Fatalf("%s --help = %d\n%s", verb, code, out)
		}
		testutil.Golden(t, "verbs/help_"+verb, out)
	}
}
