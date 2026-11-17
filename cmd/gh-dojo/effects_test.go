package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// effectsDojoConfig is the recorded gh-dojo board with verbs that use the
// effects close, reopen, labels and assign: drop (closes as not planned,
// the user's decision), kanban's done (closes as completed), review
// (labels and board.assignee "@me"), revive (reopen), and verbs with a
// missing label and a user who cannot be assigned. The fixtures are
// hand-crafted from card_3.json.
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

[verbs.recase]
when = "Labels, one in another case than the repository's"
labels = ["+enhancement", "-Dogfood"]

[verbs.tagcase]
when = "Tag it, in another case than the repository's"
labels = ["+Enhancement"]

[verbs.untag]
when = "Remove a label the repository does not have"
labels = ["-no-such-label"]

[verbs.hand]
when = "Hand it to the owner, login in lowercase"
assign = "trum-ok"

[verbs.dup]
when = "A duplicate of another issue"
close = "duplicate"
comment = "Duplicate of {of}."
`

// assignDojoConfig is effectsDojoConfig with pass (assign =
// "{assignee}") and hand-back (back to "@me" alone, unassign = "all").
var assignDojoConfig = effectsDojoConfig + `
[verbs.pass]
when = "Pass it to someone"
assign = "{assignee}"
comment = "Over to @{assignee}."

[verbs.hand-back]
when = "Back to the author alone"
assign = true
unassign = "all"
`

// withAssignApp is withEffectsApp with assignDojoConfig.
func withAssignApp(t *testing.T, fixtures ...string) (*graphtest.Server, int) {
	t.Helper()
	srv, before := withEffectsApp(t, fixtures...)
	withConfig(t, assignDojoConfig)
	return srv, before
}

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

// TestVerbDrop runs drop end to end: without --yes the user is asked
// (exit 2) after one read and nothing is written; with --yes the comment
// and the close go in one mutation; a repeat finds both in place and
// needs no --yes.
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

// TestVerbDuplicate: close = "duplicate" reads the --of issue with the
// card and closes the issue as its duplicate, linked to it
// (duplicateIssueId), in the comment's mutation; {of} names it in the
// comment. The repeat finds both in place.
func TestVerbDuplicate(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("duplicate_ok"))
	checkRuns(t, srv, before,
		runs{[]string{"dup", "3", "--of", "26"}, render.ExitOK, 2, "duplicate_ok"},
		runs{[]string{"dup", "3", "--of", "#26"}, render.ExitOK, 1, "duplicate_repeat"},
	)
}

// TestVerbDuplicateDryRun shows the original in the plan.
func TestVerbDuplicateDryRun(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("duplicate_card"))
	checkRuns(t, srv, before,
		runs{[]string{"dup", "3", "--of", "26", "--dry-run"}, render.ExitOK, 1, "duplicate_dry_run"})
}

// TestVerbDuplicateRefused: --of is required, names one issue and not the
// issue itself, all refused before any read; an original that does not
// exist is exit code 3 after the read, with nothing written.
func TestVerbDuplicateRefused(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("duplicate_missing"))
	checkRuns(t, srv, before,
		runs{[]string{"dup", "3"}, render.ExitUsage, 0, "duplicate_no_of"},
		runs{[]string{"dup", "3", "--of", "26, 27"}, render.ExitUsage, 0, "duplicate_two"},
		runs{[]string{"dup", "3", "--of", "the old one"}, render.ExitUsage, 0, "duplicate_text"},
		runs{[]string{"dup", "3", "--of", "3"}, render.ExitUsage, 0, "duplicate_self"},
		runs{[]string{"dup", "3", "--of", "9999"}, render.ExitNotFound, 1, "duplicate_missing"},
		runs{[]string{"drop", "3", "--comment", "x", "--of", "26"}, render.ExitUsage, 0, "duplicate_not_taken"},
	)
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

// TestVerbLabelsBeyondWindow: a verb that removes labels reads up to 100
// of them, so a label past the card's 20 is removed, not reported in place.
func TestVerbLabelsBeyondWindow(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("review_many_labels"))
	checkRuns(t, srv, before,
		runs{[]string{"review", "3", "--comment", "PR #50", "--dry-run"}, render.ExitOK, 1, "review_many_labels"})
}

// TestVerbLabelCase: label lookups match case, GitHub's names are unique
// regardless of it. A label written in another case is refused with its
// spelling as the fix (gh label create would fail), the near miss first
// among the allowed values; a label to remove counts too when the issue
// has it, or it would stay.
func TestVerbLabelCase(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("label_case"), verbFixture("label_case_add"))
	checkRuns(t, srv, before,
		runs{[]string{"recase", "3"}, render.ExitUsage, 2, "label_case"},
		runs{[]string{"tagcase", "3"}, render.ExitUsage, 2, "label_case_add"},
	)
}

// TestVerbRemoveAbsentLabel: a label to remove that the repository does
// not have is in place, not an error: nothing to remove, nothing written.
func TestVerbRemoveAbsentLabel(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("label_absent"))
	checkRuns(t, srv, before, runs{[]string{"untag", "3"}, render.ExitOK, 1, "label_absent"})
}

// TestVerbAssignLoginCase: the login is assigned and printed as GitHub
// spells it, whatever the case in the config.
func TestVerbAssignLoginCase(t *testing.T) {
	srv, before := withEffectsApp(t, verbFixture("assign_case"))
	checkRuns(t, srv, before, runs{[]string{"hand", "3"}, render.ExitOK, 2, "assign_case"})
}

// TestVerbAssigneeFlag: assign = "{assignee}" takes the user from
// --assignee, required, looked up with the card like a login of the config:
// assigned and named in the comment as GitHub spells the login. One who
// cannot be assigned is exit code 1 with who can (one more read); a
// missing or malformed one is refused before any read.
func TestVerbAssigneeFlag(t *testing.T) {
	srv, before := withAssignApp(t, verbFixture("assignee_flag"), verbFixture("assignee_unknown"))
	checkRuns(t, srv, before,
		runs{[]string{"pass", "3", "--assignee", "trum-ok"}, render.ExitOK, 2, "assignee_flag"},
		runs{[]string{"pass", "3", "--assignee", "octo-nobody"}, render.ExitUsage, 2, "assignee_flag_unknown"},
		runs{[]string{"pass", "3"}, render.ExitUsage, 0, "assignee_flag_missing"},
		runs{[]string{"pass", "3", "--assignee", "@trum-ok"}, render.ExitUsage, 0, "assignee_flag_at"},
		runs{[]string{"hand", "3", "--assignee", "trum-ok"}, render.ExitUsage, 0, "assignee_flag_not_taken"},
	)
}

// TestVerbUnassignAll: unassign = "all" with assign = true leaves the
// author ("@me") the only assignee: the card reads the viewer and the
// assignees' IDs, and the removal is one mutation. The dry run's JSON
// names the logins; the repeat writes nothing.
func TestVerbUnassignAll(t *testing.T) {
	srv, before := withAssignApp(t, verbFixture("unassign_all"))
	checkRuns(t, srv, before,
		runs{[]string{"hand-back", "3", "--dry-run", "--json"}, render.ExitOK, 1, "unassign_all_dry_run"},
		runs{[]string{"hand-back", "3"}, render.ExitOK, 2, "unassign_all"},
		runs{[]string{"hand-back", "3"}, render.ExitOK, 1, "unassign_all_repeat"},
	)
}

// TestVerbEffectsHelp: the help of a verb says what it does besides the
// status, that a confirm verb needs the user and --yes, and whom it
// assigns and unassigns.
func TestVerbEffectsHelp(t *testing.T) {
	withConfig(t, assignDojoConfig)
	for _, verb := range []string{"drop", "review", "revive", "done", "pass", "hand-back"} {
		code, out := runOut(verb, "--help")
		if code != render.ExitOK {
			t.Fatalf("%s --help = %d\n%s", verb, code, out)
		}
		testutil.Golden(t, "verbs/help_"+verb, out)
	}
}

// TestUnassignJSON: an unassign op names its logins, and kept says the
// verb leaves a user it names because it assigns them too.
func TestUnassignJSON(t *testing.T) {
	for _, tt := range []struct {
		op   workflow.Unassign
		want string
	}{
		{workflow.Unassign{Logins: []string{"bob", "carol"}, All: true}, `{"op":"unassign","logins":["bob","carol"],"kept":false}`},
		{workflow.Unassign{All: true}, `{"op":"unassign","logins":[],"kept":false}`},
		{workflow.Unassign{Logins: []string{"carol"}, Kept: true}, `{"op":"unassign","logins":["carol"],"kept":true}`},
	} {
		got, err := json.Marshal(toOpJSON(tt.op, "Trum-ok/gh-dojo"))
		if err != nil || string(got) != tt.want {
			t.Errorf("%+v: %s, %v; want %s", tt.op, got, err, tt.want)
		}
	}
}
