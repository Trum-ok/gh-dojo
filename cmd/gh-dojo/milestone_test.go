package main

import (
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// milestoneDojoConfig is effectsDojoConfig with verbs that set a
// milestone: plan takes it from --milestone, ship and ship-later name one
// in the config. The gh-dojo repository's open milestones are v0.0 to
// v0.3, each "v0.N — <name>" (recorded in
// internal/issue/testdata/graphql/card_55_milestones.json).
var milestoneDojoConfig = effectsDojoConfig + `
[verbs.plan]
when = "The issue moves to another stage"
milestone = "{milestone}"
comment = "Moved to {milestone}."
batch = true

[verbs.ship]
when = "Due in the agent release"
milestone = "v0.2"

[verbs.ship-later]
when = "Due in a release without a milestone yet"
milestone = "v9"
`

// withMilestoneApp is withEffectsApp with milestoneDojoConfig.
func withMilestoneApp(t *testing.T, fixtures ...string) (*graphtest.Server, int) {
	t.Helper()
	srv, before := withEffectsApp(t, fixtures...)
	withConfig(t, milestoneDojoConfig)
	return srv, before
}

// TestVerbMilestone: plan --milestone v0.3 puts #3 (in v0.0) in the open
// milestone whose title starts so, read with the card: one read and one
// mutation, which also posts the comment naming it as the repository
// spells it. The repeat, in another case, finds both in place.
func TestVerbMilestone(t *testing.T) {
	srv, before := withMilestoneApp(t, verbFixture("milestone_plan"))
	checkRuns(t, srv, before,
		runs{[]string{"plan", "3", "--milestone", "v0.3"}, render.ExitOK, 2, "milestone_plan"},
		runs{[]string{"plan", "3", "--milestone", "V0.3"}, render.ExitOK, 1, "milestone_plan_repeat"},
	)
}

// TestVerbMilestoneNone: plan --milestone none takes #3 out of v0.0. The
// dry run plans v0.0 → none; the card is read without the open
// milestones, and one mutation clears it and posts the comment, where
// {milestone} is "none". The repeat, in another case, finds both in place.
func TestVerbMilestoneNone(t *testing.T) {
	srv, before := withMilestoneApp(t, verbFixture("milestone_clear"))
	checkRuns(t, srv, before,
		runs{[]string{"plan", "3", "--milestone", "none", "--dry-run"}, render.ExitOK, 1, "milestone_clear_dry_run"},
		runs{[]string{"plan", "3", "--milestone", "none"}, render.ExitOK, 2, "milestone_clear"},
		runs{[]string{"plan", "3", "--milestone", "None", "--json"}, render.ExitOK, 1, "milestone_clear_repeat"},
	)
}

// TestVerbMilestoneMatch: what --milestone and a title of the config
// match, in dry runs. A start several titles share, or none, is exit code
// 1 with the candidates or the open milestones, after the card's one read;
// for a title of the config the error names its key. A missing
// --milestone, or one given to a verb that does not take it, is refused
// before any read.
func TestVerbMilestoneMatch(t *testing.T) {
	srv, before := withMilestoneApp(t, verbFixture("milestone_card"))
	checkRuns(t, srv, before,
		runs{[]string{"plan", "3", "--milestone", "v0.3", "--dry-run"}, render.ExitOK, 1, "milestone_dry_run"},
		runs{[]string{"plan", "3", "--milestone", "v0.3", "--dry-run", "--json"}, render.ExitOK, 1, "milestone_dry_run_json"},
		runs{[]string{"plan", "3", "--milestone", "v0.0 — самохостинг", "--dry-run"}, render.ExitOK, 1, "milestone_in_place"},
		runs{[]string{"ship", "3", "--dry-run"}, render.ExitOK, 1, "milestone_config"},
		runs{[]string{"plan", "3", "--milestone", "v0"}, render.ExitUsage, 1, "milestone_ambiguous"},
		runs{[]string{"plan", "3", "--milestone", "v9"}, render.ExitUsage, 1, "milestone_unknown"},
		runs{[]string{"ship-later", "3"}, render.ExitUsage, 1, "milestone_config_unknown"},
		runs{[]string{"plan", "3"}, render.ExitUsage, 0, "milestone_missing"},
		runs{[]string{"done", "3", "--milestone", "v0.3"}, render.ExitUsage, 0, "milestone_not_taken"},
	)
}

// TestVerbMilestonePages: past the first 100 open milestones the card
// read says there are more, and they are read page by page before
// matching: one request more.
func TestVerbMilestonePages(t *testing.T) {
	srv, before := withMilestoneApp(t, verbFixture("milestone_pages"))
	checkRuns(t, srv, before,
		runs{[]string{"plan", "3", "--milestone", "v0.3", "--dry-run"}, render.ExitOK, 2, "milestone_pages"})
}

// TestVerbMilestoneBatch: plan 3 7 reads both cards and the open
// milestones in one request and writes each issue in a mutation of its
// own.
func TestVerbMilestoneBatch(t *testing.T) {
	srv, before := withMilestoneApp(t, verbFixture("milestone_batch"))
	checkRuns(t, srv, before,
		runs{[]string{"plan", "3", "7", "--milestone", "v0.3"}, render.ExitOK, 3, "milestone_batch"})
}

// TestVerbMilestoneRepos: the milestone is matched in each issue's own
// repository, read in the cards' one request: acme/api has no open
// milestone starting with v0.3, so its issue fails alone.
func TestVerbMilestoneRepos(t *testing.T) {
	srv, before := withMilestoneApp(t, verbFixture("milestone_repos"))
	checkRuns(t, srv, before,
		runs{[]string{"plan", "3", "acme/api#7", "--milestone", "v0.3", "--dry-run"}, render.ExitUsage, 1, "milestone_repos"})
}

// TestVerbMilestonePartial: GitHub sets the milestone and refuses the
// comment: exit 5 naming both. The re-run finds the milestone in place and
// posts the comment alone.
func TestVerbMilestonePartial(t *testing.T) {
	srv, before := withMilestoneApp(t, verbFixture("milestone_partial"))
	checkRuns(t, srv, before,
		runs{[]string{"plan", "3", "--milestone", "v0.3"}, render.ExitPartial, 2, "milestone_partial"},
		runs{[]string{"plan", "3", "--milestone", "v0.3"}, render.ExitOK, 2, "milestone_partial_rerun"},
	)
}

// TestVerbMilestoneClosed: #3 is in v0.3, closed since the verb set it.
// Named by its whole title, in any case, it is in place, though no open
// milestone matches any more: the repeat writes nothing.
func TestVerbMilestoneClosed(t *testing.T) {
	srv, before := withMilestoneApp(t, verbFixture("milestone_closed"))
	checkRuns(t, srv, before,
		runs{[]string{"plan", "3", "--milestone", "V0.3 — РАСШИРЯЕМОСТЬ"}, render.ExitOK, 1, "milestone_closed"})
}

// TestVerbMilestoneLabelBatch: in replan 3 4 5, #3 has the label to remove
// spelled otherwise than the repository's lookup: #3 fails alone, and #4
// and #5 still get the milestone, matched once for the repository before
// any issue's own checks.
func TestVerbMilestoneLabelBatch(t *testing.T) {
	srv, before := withMilestoneApp(t, verbFixture("milestone_label_batch"))
	withConfig(t, milestoneDojoConfig+`
[verbs.replan]
when = "Moved to another stage, unblocked"
labels = ["-blocked"]
milestone = "{milestone}"
batch = true
`)
	checkRuns(t, srv, before,
		runs{[]string{"replan", "3", "4", "5", "--milestone", "v0.3"}, render.ExitUsage, 4, "milestone_label_batch"})
}

// TestReleaseMilestoneVerb: release has no --milestone, so a verb with
// milestone = "{milestone}" is refused before any read, as one with
// assign = "{assignee}" is.
func TestReleaseMilestoneVerb(t *testing.T) {
	srv, before := withMilestoneApp(t)
	checkRuns(t, srv, before,
		runs{[]string{"release", "v0.3.0", "--verb", "plan"}, render.ExitUsage, 0, "milestone_release"})
}

// TestVerbMilestoneHelp: the help of a verb says which milestone it sets
// and how a title matches.
func TestVerbMilestoneHelp(t *testing.T) {
	withConfig(t, milestoneDojoConfig)
	for _, verb := range []string{"plan", "ship"} {
		code, out := runOut(verb, "--help")
		if code != render.ExitOK {
			t.Fatalf("%s --help = %d\n%s", verb, code, out)
		}
		testutil.Golden(t, "verbs/help_"+verb, out)
	}
}
