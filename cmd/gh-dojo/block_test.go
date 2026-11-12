package main

import (
	"slices"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// Hand-crafted fixtures of blocked-by links on issue #3 of the gh-dojo
// repo, from the recorded reads internal/issue/testdata/graphql/links_3.json
// (#2 blocks #3, #26 does not) and card_3_links.json. Mutations are never
// sent to GitHub.

// withLinksApp sets up the gh-dojo config with fixtures. block and unblock
// do not read the board, so there is no schema to warm.
func withLinksApp(t *testing.T, fixtures ...string) (*graphtest.Server, int) {
	t.Helper()
	withConfig(t, verbDojoConfig)
	srv := withApp(t, fixtures...)
	return srv, srv.Requests()
}

// checkLinkRuns runs gh dojo commands and checks their exit code, requests
// and output (golden files under testdata/links/).
func checkLinkRuns(t *testing.T, srv *graphtest.Server, before int, steps ...runs) {
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
		testutil.Golden(t, "links/"+r.golden, out)
	}
}

// TestBlock: one read and one mutation for the link missing; the link in
// place is "already". The repeat is one read.
func TestBlock(t *testing.T) {
	srv, before := withLinksApp(t, verbFixture("links_block"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"block", "3", "--by", "2,26"}, render.ExitOK, 2, "block_ok"},
		// An empty part ("2,") is skipped; an issue URL is a reference.
		runs{[]string{"block", "#3", "--by", "2,", "--by", "https://github.com/Trum-ok/gh-dojo/issues/26"}, render.ExitOK, 1, "block_repeat"},
	)
}

// TestBlockOtherRepo: the issue to block is in another repository
// (acme/api, outside board.repos); a bare --by number is in the default
// one. The line names acme/api's issues in full and the default
// repository's as #3, in the text and the JSON alike.
func TestBlockOtherRepo(t *testing.T) {
	srv, before := withLinksApp(t, verbFixture("links_block_repos"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"block", "acme/api#7", "--by", "3,acme/api#8"}, render.ExitOK, 2, "block_other_repo"},
		runs{[]string{"block", "https://github.com/acme/api/issues/7", "--by", "Trum-ok/gh-dojo#3", "--by", "acme/api#8", "--json"},
			render.ExitOK, 1, "block_other_repo_json"},
		// Written in other cases: named as GitHub and the config spell them.
		runs{[]string{"block", "ACME/Api#7", "--by", "trum-ok/GH-dojo#3,Acme/API#8", "--json"},
			render.ExitOK, 1, "block_other_repo_case"},
	)
}

func TestUnblock(t *testing.T) {
	srv, before := withLinksApp(t, verbFixture("links_unblock"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"unblock", "3", "--by", "2", "--by", "26"}, render.ExitOK, 2, "unblock_ok"},
		runs{[]string{"unblock", "3", "--by", "2", "--by", "26"}, render.ExitOK, 1, "unblock_repeat"},
	)
}

// TestBlockPartial: GitHub links one issue and fails the other: exit 5
// naming both; the re-run sends the missing link alone.
func TestBlockPartial(t *testing.T) {
	srv, before := withLinksApp(t, verbFixture("links_block_partial"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"block", "3", "--by", "2", "--by", "26"}, render.ExitPartial, 2, "block_partial"},
		runs{[]string{"block", "3", "--by", "2", "--by", "26"}, render.ExitOK, 2, "block_partial_rerun"},
	)
}

// TestBlockNotFound: a missing blocker or issue is exit 3 after the one
// read, before any write.
func TestBlockNotFound(t *testing.T) {
	srv, before := withLinksApp(t, verbFixture("links_missing"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"block", "3", "--by", "2,26", "--by", "9999", "--by", "Trum-ok/no-such-repo#1"},
			render.ExitNotFound, 1, "block_missing"},
		runs{[]string{"unblock", "9998", "--by", "2"}, render.ExitNotFound, 1, "unblock_missing_target"},
	)
}

// TestBlockArgs: bad arguments are exit 1 without a request.
func TestBlockArgs(t *testing.T) {
	srv, _ := withLinksApp(t, boardFixture)
	before := warmSchema(t, srv) // the fixture must be used
	checkLinkRuns(t, srv, before,
		runs{[]string{"block", "3"}, render.ExitUsage, 0, "block_no_by"},
		runs{[]string{"block", "3", "--by", "3"}, render.ExitUsage, 0, "block_self"},
		runs{[]string{"unblock", "3", "--by", "Trum-ok/gh-dojo#3"}, render.ExitUsage, 0, "unblock_self"},
		runs{[]string{"block", "3", "--by", "the API team"}, render.ExitUsage, 0, "block_text"},
		runs{[]string{"block", "3", "--by", ","}, render.ExitUsage, 0, "block_no_by"},
		runs{[]string{"block", "3", "4", "--by", "2"}, render.ExitUsage, 0, "block_two_issues"},
		runs{[]string{"block", "--help"}, render.ExitOK, 0, "block_help"},
		runs{[]string{"unblock", "--help"}, render.ExitOK, 0, "unblock_help"},
	)
}

// blockersConfig is the spec's start: blockers = "warn".
const blockersConfig = verbDojoConfig + `
[verbs.start]
blockers = "warn"
`

// TestVerbBlockersWarn: start with blockers = "warn" on #3, blocked by
// unresolved issues (one In progress, one whose status was not read, one
// not read at all): a warning on stderr, the verb runs. The dry run warns
// too.
func TestVerbBlockersWarn(t *testing.T) {
	withConfig(t, blockersConfig)
	withSession(t, "abc")
	srv := withApp(t, boardFixture, verbFixture("card_3_blocked"), verbFixture("start_blocked"))
	before := warmSchema(t, srv)
	checkLinkRuns(t, srv, before,
		runs{[]string{"start", "3", "--dry-run"}, render.ExitOK, 1, "start_warn_dry_run"},
		runs{[]string{"start", "3"}, render.ExitOK, 2, "start_warn"},
	)
}

// TestVerbBlockersDeny: with blockers = "deny" the same start is refused
// (exit 2) with the list and no fix line, in a dry run too; nothing is
// written.
func TestVerbBlockersDeny(t *testing.T) {
	withConfig(t, verbDojoConfig+"\n[verbs.start]\nblockers = \"deny\"\n")
	withSession(t, "abc")
	srv := withApp(t, boardFixture, verbFixture("card_3_blocked"))
	before := warmSchema(t, srv)
	checkLinkRuns(t, srv, before,
		runs{[]string{"start", "3"}, render.ExitDenied, 1, "start_deny"},
		runs{[]string{"start", "3", "--dry-run"}, render.ExitDenied, 1, "start_deny"},
	)
}

// TestVerbBlockersResolved: the recorded #3 is blocked by #1 and #2, both
// open but in Ready (statuses.resolved): resolved, so deny lets start run.
func TestVerbBlockersResolved(t *testing.T) {
	withConfig(t, verbDojoConfig+"\n[verbs.start]\nblockers = \"deny\"\n")
	withSession(t, "abc")
	srv := withApp(t, boardFixture, verbFixture("start_resolved"))
	before := warmSchema(t, srv)
	checkLinkRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitOK, 2, "start_resolved"})
}

// TestVerbWaitLinks: kanban's wait (blockers = "link") with --by 26 reads
// #26 with the card and links it in the verb's one mutation; the repeat is
// one read and changes nothing.
func TestVerbWaitLinks(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("wait_link"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"wait", "3", "--by", "26"}, render.ExitOK, 2, "wait_link"},
		runs{[]string{"wait", "3", "--by", "#26"}, render.ExitOK, 1, "wait_link_repeat"},
	)
}

// TestVerbWaitLinkDryRun: a link in place shows as such in the plan.
func TestVerbWaitLinkDryRun(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("wait_link_dry_run"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"wait", "3", "--by", "2", "--dry-run"}, render.ExitOK, 1, "wait_link_dry_run"})
}

// TestVerbWaitLinkPartial: the link fails, status and comment apply: exit
// 5 naming the link; the re-run sends the link alone.
func TestVerbWaitLinkPartial(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("wait_link_partial"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"wait", "3", "--by", "26"}, render.ExitPartial, 2, "wait_link_partial"},
		runs{[]string{"wait", "3", "--by", "26"}, render.ExitOK, 2, "wait_link_partial_rerun"},
	)
}

// TestVerbWaitLinkErrors: a --by issue that does not exist is exit 3 after
// the card's read; the issue itself is exit 1 before any request.
func TestVerbWaitLinkErrors(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("wait_link_missing"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"wait", "3", "--by", "9999"}, render.ExitNotFound, 1, "wait_link_missing"},
		runs{[]string{"wait", "3", "--by", "3"}, render.ExitUsage, 0, "wait_link_self"},
		runs{[]string{"wait", "3", "--by", "trum-ok/gh-dojo#3"}, render.ExitUsage, 0, "wait_link_self"},
	)
}

// TestNewBlockedBy: the --blocked-by issue is read with new's context and
// linked in the status mutation: still 4 requests.
func TestNewBlockedBy(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, verbFixture("new_blocked_by"))
	before := warmSchema(t, srv)
	checkLinkRuns(t, srv, before, runs{append(slices.Clone(newFixArgs), "--blocked-by", "26"), render.ExitOK, 4, "new"})
}

// TestNewBlockedByResume: a re-run finds the issue done but unlinked and
// adds the link alone; the next finds it linked and writes nothing.
func TestNewBlockedByResume(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, verbFixture("new_blocked_by_resume"))
	before := warmSchema(t, srv)
	args := append(slices.Clone(newFixArgs), "--blocked-by", "#26")
	checkLinkRuns(t, srv, before,
		runs{args, render.ExitOK, 2, "new_resume"},
		runs{args, render.ExitOK, 1, "new_repeat"},
	)
}

// TestNewBlockedByErrors: a missing blocker is exit 3 after the one read,
// before anything is created; a value that is not an issue is exit 1
// without a request.
func TestNewBlockedByErrors(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, verbFixture("new_blocked_by_missing"))
	before := warmSchema(t, srv)
	checkLinkRuns(t, srv, before,
		runs{append(slices.Clone(newFixArgs), "--blocked-by", "9999"), render.ExitNotFound, 1, "new_missing"},
		runs{append(slices.Clone(newFixArgs), "--blocked-by", "soon"), render.ExitUsage, 0, "new_not_issue"},
	)
}

// TestUnblockFindsLinkByTarget: whether a link is in place comes from the
// issue's own blockers, read whole, not from the issues the blocker blocks,
// which may be more than are read.
func TestUnblockFindsLinkByTarget(t *testing.T) {
	srv, before := withLinksApp(t, verbFixture("links_unblock_window"))
	checkLinkRuns(t, srv, before, runs{[]string{"unblock", "3", "--by", "2"}, render.ExitOK, 2, "unblock_window"})
}

// TestBlockCycle: a blocker the issue already blocks is refused before any
// write (exit 1); a pull request is exit 3.
func TestBlockCycle(t *testing.T) {
	srv, before := withLinksApp(t, verbFixture("links_cycle"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"block", "3", "--by", "26"}, render.ExitUsage, 1, "block_cycle"},
		runs{[]string{"block", "3", "--by", "40"}, render.ExitNotFound, 1, "block_pull_request"},
	)
}

// TestBlockCycleRefused: GitHub refusing a link as a cycle (through other
// issues) is exit 1, not a partial 5: a re-run would fail the same way.
func TestBlockCycleRefused(t *testing.T) {
	srv, before := withLinksApp(t, verbFixture("links_cycle_refused"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"block", "3", "--by", "2", "--by", "26"}, render.ExitUsage, 2, "block_cycle_refused"})
}

// TestVerbBlockersMany: a blockers check reads up to 50 blockers (GitHub's
// limit), so resolved ones past the card's usual 20 do not count as
// unresolved: deny lets start run.
func TestVerbBlockersMany(t *testing.T) {
	withConfig(t, verbDojoConfig+"\n[verbs.start]\nblockers = \"deny\"\n")
	withSession(t, "abc")
	srv := withApp(t, boardFixture, verbFixture("start_many_blockers"))
	before := warmSchema(t, srv)
	checkLinkRuns(t, srv, before, runs{[]string{"start", "3", "--dry-run"}, render.ExitOK, 1, "start_many_blockers"})
}

// TestVerbWaitLinkMany: --by naming several issues links each; one linked
// already is "already", a pull request is named in the comment with a
// warning but not linked. Issues and text mixed are exit 1 before any
// request.
func TestVerbWaitLinkMany(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("wait_link_many"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"wait", "3", "--by", "2, https://github.com/Trum-ok/gh-dojo/issues/26, 40,", "--dry-run"},
			render.ExitOK, 1, "wait_link_many"},
		runs{[]string{"wait", "3", "--by", "26, the API team"}, render.ExitUsage, 0, "wait_link_mixed"},
	)
}

// TestVerbWaitLinkTwo: two issues in --by are two blocked-by links in the
// verb's one mutation, and the comment names both.
func TestVerbWaitLinkTwo(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("wait_link_two"))
	checkLinkRuns(t, srv, before, runs{[]string{"wait", "3", "--by", "26,25"}, render.ExitOK, 2, "wait_link_two"})
}

// TestVerbWaitLinkCycle: a --by issue that #3 already blocks is refused
// after the read (exit 1); GitHub refusing the link as a cycle after the
// status and comment applied is exit 1 too, not 5.
func TestVerbWaitLinkCycle(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("wait_link_cycle"), verbFixture("wait_link_cycle_refused"))
	checkLinkRuns(t, srv, before,
		runs{[]string{"wait", "3", "--by", "26"}, render.ExitUsage, 1, "wait_link_cycle"},
		runs{[]string{"wait", "3", "--by", "26"}, render.ExitUsage, 2, "wait_link_cycle_refused"},
	)
}

// TestNewBlockedByCycle: the issue an earlier run created already blocks
// the --blocked-by issue: exit 1 before any write.
func TestNewBlockedByCycle(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, verbFixture("new_blocked_by_cycle"))
	before := warmSchema(t, srv)
	checkLinkRuns(t, srv, before,
		runs{append(slices.Clone(newFixArgs), "--blocked-by", "26"), render.ExitUsage, 1, "new_cycle"})
}
