package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/newissue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
	"github.com/spf13/cobra"
)

// newContextFixture is the recorded NewIssueContext read of Trum-ok/gh-dojo.
// Record it again with
//
//	GH_DOJO_RECORD=1 go test ./cmd/gh-dojo -run TestNewContextRecorded
//
// The other new_*.json files are hand-crafted from it (see their comments).
const newContextFixture = "testdata/graphql/new_context.json"

const (
	newSuccessFixture = "testdata/graphql/new_success.json"
	newStatusFails    = "testdata/graphql/new_status_fails.json"
	newRerunFixture   = "testdata/graphql/new_rerun.json"
	newResumeAll      = "testdata/graphql/new_resume_all.json"
	newDoneFixture    = "testdata/graphql/new_done.json"
	newNoStatus       = "testdata/graphql/new_no_status.json"
	newContextAreas   = "testdata/graphql/new_context_areas.json"

	newMilestoneFixture = "testdata/graphql/new_milestone.json"
	newMilestoneKept    = "testdata/graphql/new_milestone_kept.json"
)

var newFixArgs = []string{"new", "Fix login redirect", "--type", "bug", "--area", "cli",
	"--body", "The redirect drops the query string."}

func TestNewContextRecorded(t *testing.T) {
	srv := graphtest.New(t, newContextFixture)
	repo := issue.Repo{Owner: "Trum-ok", Name: "gh-dojo"}
	nc, err := issue.ReadNewContext(context.Background(), srv.Client(), issue.NewRequest{Repo: repo, StatusField: "Status"})
	if err != nil {
		t.Fatal(err)
	}
	if nc.RepoID == "" || len(nc.Issues) == 0 || len(nc.Issues) > issue.NewDedupWindow {
		t.Errorf("repo %q, %d issues", nc.RepoID, len(nc.Issues))
	}
	if !slices.ContainsFunc(nc.Labels.Nodes, func(l issue.Label) bool { return l.Name == "bug" }) {
		t.Errorf("labels = %v, want bug among them", nc.Labels.Nodes)
	}
	if len(nc.Milestones.Nodes) == 0 || nc.Milestones.Nodes[0].ID == "" || nc.Milestones.Nodes[0].Title == "" {
		t.Errorf("milestones = %+v, want the open ones", nc.Milestones.Nodes)
	}
	if srv.Requests() != 1 {
		t.Errorf("requests = %d, want 1", srv.Requests())
	}
}

// warmSchema reads the board once so the next run finds it in the cache,
// and returns the requests sent so far.
func warmSchema(t *testing.T, srv *graphtest.Server) int {
	t.Helper()
	if _, err := newApp().Schema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return srv.Requests()
}

func TestNewSuccess(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, newSuccessFixture)
	before := warmSchema(t, srv)
	code, out := runOut(newFixArgs...)
	testutil.Golden(t, "new/success", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
	if n := srv.Requests() - before; n != 4 {
		t.Errorf("requests with a warm schema = %d, want 4 (read, createIssue, addProjectV2ItemById, status)", n)
	}
}

// reposConfig has a second repository, acme/api, on the gh-dojo board.
var reposConfig = strings.Replace(dojoConfig, `repos = ["Trum-ok/gh-dojo"]`, `repos = ["Trum-ok/gh-dojo", "acme/api"]`, 1)

// TestNewRepo: --repo creates the issue in another repository of
// board.repos, with the same requests as the default one, and names it
// acme/api#15. A repository outside board.repos is refused before any
// request (newApp is noApp), listing them.
func TestNewRepo(t *testing.T) {
	for _, tt := range []struct{ name, golden string }{{"text", "new/repo"}, {"json", "json/new_repo"}} {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, reposConfig)
			srv := withApp(t, boardFixture, "testdata/graphql/new_repo.json")
			before := warmSchema(t, srv)
			args := append(slices.Clone(newFixArgs), "--repo", "ACME/api")
			if tt.name == "json" {
				args = append(args, "--json")
			}
			code, out := runOut(args...)
			testutil.Golden(t, tt.golden, out)
			if code != render.ExitOK {
				t.Errorf("code = %d", code)
			}
			if n := srv.Requests() - before; n != 4 {
				t.Errorf("requests with a warm schema = %d, want 4", n)
			}
		})
	}
	for _, tt := range []struct{ name, config, repo string }{
		{"repo_unknown", reposConfig, "acme/web"},
		{"repo_malformed", reposConfig, "acme"},
		{"repo_no_repos", strings.Replace(dojoConfig, `repos = ["Trum-ok/gh-dojo"]`, "", 1), "acme/api"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, tt.config)
			code, out := runOut("new", "Fix login redirect", "--repo", tt.repo)
			testutil.Golden(t, "new/"+tt.name, out)
			if code != render.ExitUsage {
				t.Errorf("code = %d", code)
			}
		})
	}
}

// TestNewMilestoneDryRun: the dry run reads as the run does and writes
// nothing; its JSON says what would be, with no number yet.
func TestNewMilestoneDryRun(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, "testdata/graphql/new_milestone_dry_run.json")
	before := warmSchema(t, srv)
	code, out := runOut(append(slices.Clone(newFixArgs), "--milestone", "v0.1", "--dry-run", "--json")...)
	testutil.Golden(t, "new/milestone_dry_run_json", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
	if n := srv.Requests() - before; n != 1 {
		t.Errorf("requests with a warm schema = %d, want 1 (read)", n)
	}
}

// TestNewMilestoneNone: --milestone none, as for a verb, is no milestone:
// the same requests and line as without it.
func TestNewMilestoneNone(t *testing.T) {
	withConfig(t, dojoConfig)
	withApp(t, boardFixture, newSuccessFixture)
	code, out := runOut(append(slices.Clone(newFixArgs), "--milestone", "None")...)
	testutil.Golden(t, "new/success", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
}

func TestNewBodyFromStdin(t *testing.T) {
	withConfig(t, dojoConfig)
	withApp(t, boardFixture, newSuccessFixture) // the same body, so the same CreateIssue variables
	code, out := runIn("\nThe redirect drops the query string.\n",
		"new", "Fix login redirect", "--type", "bug", "--area", "cli", "--body-file", "-")
	testutil.Golden(t, "new/success", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
}

func TestNewFailureAfterCreate(t *testing.T) {
	withConfig(t, dojoConfig)
	withApp(t, boardFixture, newStatusFails)
	code, out := runOut(newFixArgs...)
	testutil.Golden(t, "new/status_fails", out)
	if code != render.ExitPartial {
		t.Errorf("code = %d, want %d", code, render.ExitPartial)
	}

	// The same command again finds #15 by its marker and only sets the
	// status: new_rerun.json has no CreateIssue to answer.
	srv := withApp(t, boardFixture, newRerunFixture)
	code, out = runOut(newFixArgs...)
	testutil.Golden(t, "new/rerun", out)
	if code != render.ExitOK {
		t.Errorf("re-run code = %d", code)
	}
	if n := srv.Requests(); n != 3 {
		t.Errorf("re-run requests = %d, want 3 (schema, read, status)", n)
	}
}

// TestNewStaleAfterCreate: a stale schema ID once the issue exists is exit
// code 5 with the cache dropped, not an in-process retry: a lagging re-read
// could miss the new issue and create another.
func TestNewStaleAfterCreate(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, "testdata/graphql/new_stale_status.json")
	before := warmSchema(t, srv)
	code, out := runOut(newFixArgs...)
	testutil.Golden(t, "new/stale_status", out)
	if code != render.ExitPartial {
		t.Errorf("code = %d, want %d", code, render.ExitPartial)
	}
	if n := srv.Requests() - before; n != 4 {
		t.Errorf("requests = %d, want 4 (read, create, add, status): no second attempt", n)
	}
	// The schema cache was dropped, so the re-run reads the board afresh.
	before = srv.Requests()
	if _, err := newApp().Schema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.Requests() != before+1 {
		t.Error("the schema cache was not dropped after a stale ID")
	}
}

// TestNewBoardRace reproduces issue #17: createIssue answers with no board
// item, GitHub refuses addProjectV2ItemById, and the item is on the board all
// the same (put there by GitHub or the board's automation). One re-read of
// the issue's board items finds it, so the run sets the status and succeeds
// instead of exiting with code 5.
func TestNewBoardRace(t *testing.T) {
	noRecheckDelay(t)
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, "testdata/graphql/new_board_race.json")
	before := warmSchema(t, srv)
	code, out := runOut(newFixArgs...)
	testutil.Golden(t, "new/board_race", out)
	if code != render.ExitOK {
		t.Errorf("code = %d, want %d", code, render.ExitOK)
	}
	if n := srv.Requests() - before; n != 5 {
		t.Errorf("requests = %d, want 5 (read, create, add, re-read, status)", n)
	}
}

// TestNewBoardFails: when addProjectV2ItemById fails, the run stops with
// exit code 5 before the status. A refusal is followed by one re-read of the
// issue's board items; a lost answer is not (the re-run finds the item). A
// stale project ID also drops the schema cache.
func TestNewBoardFails(t *testing.T) {
	noRecheckDelay(t)
	tests := []struct {
		name, fixture string
		requests      int
		stale         bool
	}{
		{"board_refused", "testdata/graphql/new_board_refused.json", 4, false}, // read, create, add, re-read
		{"board_lost", "testdata/graphql/new_board_lost.json", 3, false},       // read, create, add
		{"stale_board", "testdata/graphql/new_stale_board.json", 4, true},      // read, create, add, re-read
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			srv := withApp(t, boardFixture, tt.fixture)
			before := warmSchema(t, srv)
			code, out := runOut(newFixArgs...)
			testutil.Golden(t, "new/"+tt.name, out)
			if code != render.ExitPartial {
				t.Errorf("code = %d, want %d", code, render.ExitPartial)
			}
			if n := srv.Requests() - before; n != tt.requests {
				t.Errorf("requests = %d, want %d", n, tt.requests)
			}
			before = srv.Requests()
			if _, err := newApp().Schema(context.Background()); err != nil {
				t.Fatal(err)
			}
			if dropped := srv.Requests() != before; dropped != tt.stale {
				t.Errorf("schema cache dropped = %v, want %v", dropped, tt.stale)
			}
		})
	}
}

// noRecheckDelay makes re-reads after a lost or refused write immediate.
func noRecheckDelay(t *testing.T) {
	t.Helper()
	saved := newissue.RecheckDelay
	t.Cleanup(func() { newissue.RecheckDelay = saved })
	newissue.RecheckDelay = 0
}

// TestNewPartialKeepsFix: exit 5 keeps the fix of a cause a re-run alone
// cannot cure.
func TestNewPartialKeepsFix(t *testing.T) {
	withConfig(t, dojoConfig)
	withApp(t, boardFixture, "testdata/graphql/new_status_scopes.json")
	code, out := runOut(newFixArgs...)
	testutil.Golden(t, "new/status_scopes", out)
	if code != render.ExitPartial {
		t.Errorf("code = %d, want %d", code, render.ExitPartial)
	}
}

// TestNewLostCreate: a lost createIssue answer is followed by one re-read;
// the issue found there is finished, else the error stands (exit 4).
func TestNewLostCreate(t *testing.T) {
	noRecheckDelay(t)
	tests := []struct {
		name, fixture string
		code          render.Code
	}{
		{"lost_create", "testdata/graphql/new_lost_create.json", render.ExitOK},
		{"lost_create_missing", "testdata/graphql/new_lost_create_missing.json", render.ExitNetwork},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			withApp(t, boardFixture, tt.fixture)
			code, out := runOut(newFixArgs...)
			testutil.Golden(t, "new/"+tt.name, out)
			if code != tt.code {
				t.Errorf("code = %d, want %d", code, tt.code)
			}
		})
	}
}

// TestNewContentLimit (#67): createIssue refused by GitHub's limit on
// creating content, as an error or as no issue and no error, is exit code
// 4 with the wait, not an internal error to re-run at once; nothing more
// is sent, not even a re-read.
func TestNewContentLimit(t *testing.T) {
	for _, name := range []string{"content_limit", "no_issue"} {
		t.Run(name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			srv := withApp(t, boardFixture, "testdata/graphql/new_"+name+".json")
			before := warmSchema(t, srv)
			code, out := runOut(newFixArgs...)
			testutil.Golden(t, "new/"+name, out)
			if code != render.ExitNetwork {
				t.Errorf("code = %d, want %d", code, render.ExitNetwork)
			}
			if n := srv.Requests() - before; n != 2 {
				t.Errorf("requests = %d, want 2 (read, create)", n)
			}
		})
	}
}

// TestNewSameTitle:the marker hashes the title alone, case and spacing
// folded, so another body under the same title resumes the open issue.
func TestNewSameTitle(t *testing.T) {
	withConfig(t, dojoConfig)
	withApp(t, boardFixture, newRerunFixture)
	code, out := runOut("new", "fix LOGIN  redirect", "--type", "bug", "--area", "cli", "--body", "Another body")
	testutil.Golden(t, "new/same_title", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
}

func TestNewResume(t *testing.T) {
	tests := []struct{ name, fixture string }{
		{"resume_all", newResumeAll}, // labels, board and status left to do
		{"done", newDoneFixture},     // nothing left; a status someone set stays
		{"archived", "testdata/graphql/new_resume_archived.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			withApp(t, boardFixture, tt.fixture)
			code, out := runOut(newFixArgs...)
			testutil.Golden(t, "new/"+tt.name, out)
			if code != render.ExitOK {
				t.Errorf("code = %d", code)
			}
		})
	}
}

func TestNewNoInitialStatus(t *testing.T) {
	withConfig(t, dojoConfig+"\n[statuses]\ninitial = \"\"\n")
	srv := withApp(t, boardFixture, newNoStatus)
	code, out := runOut("new", "Write the README")
	testutil.Golden(t, "new/no_status", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
	if n := srv.Requests(); n != 4 {
		t.Errorf("requests = %d, want 4 (schema, read, createIssue, addProjectV2ItemById)", n)
	}
}

// TestNewUnknownLabels: an unknown label fails after the read and before
// any write; these fixtures have no mutation to answer.
func TestNewUnknownLabels(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		args    []string
	}{
		{"unknown_type", newContextFixture, []string{"--type", "chore"}},
		{"unknown_label", newContextFixture, []string{"--label", "dogfod"}},
		{"no_areas", newContextFixture, []string{"--area", "cli"}},
		{"unknown_area", newContextAreas, []string{"--type", "bug", "--area", "api"}},
		{"type_is_area", newContextAreas, []string{"--type", "area:cli"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			withApp(t, boardFixture, tt.fixture)
			code, out := runOut(append([]string{"new", "Fix login redirect"}, tt.args...)...)
			testutil.Golden(t, "new/"+tt.name, out)
			if code != render.ExitUsage {
				t.Errorf("code = %d, want %d", code, render.ExitUsage)
			}
		})
	}
}

// TestNewMilestone: --milestone goes into createIssue, with the open
// milestones read in the context query: no request more than without it.
// The title matches ignoring case.
func TestNewMilestone(t *testing.T) {
	for _, title := range []string{"v0.1", "V0.1"} {
		t.Run(title, func(t *testing.T) {
			withConfig(t, dojoConfig)
			srv := withApp(t, boardFixture, newMilestoneFixture)
			before := warmSchema(t, srv)
			code, out := runOut(append(slices.Clone(newFixArgs), "--milestone", title)...)
			testutil.Golden(t, "new/milestone", out)
			if code != render.ExitOK {
				t.Errorf("code = %d", code)
			}
			if n := srv.Requests() - before; n != 4 {
				t.Errorf("requests with a warm schema = %d, want 4 (read, createIssue, addProjectV2ItemById, status)", n)
			}
		})
	}
}

// TestNewMilestonePrefix: --milestone v0.1 names the only open milestone
// whose title starts so, as a verb's milestone effect matches it: no
// request more.
func TestNewMilestonePrefix(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, "testdata/graphql/new_milestone_prefix.json")
	before := warmSchema(t, srv)
	code, out := runOut(append(slices.Clone(newFixArgs), "--milestone", "v0.1")...)
	testutil.Golden(t, "new/milestone_prefix", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
	if n := srv.Requests() - before; n != 4 {
		t.Errorf("requests with a warm schema = %d, want 4 (read, createIssue, addProjectV2ItemById, status)", n)
	}
}

// TestNewMilestoneResume: a re-run sets the milestone of a found issue
// that has none, and keeps a different one it has, with a warning.
func TestNewMilestoneResume(t *testing.T) {
	tests := []struct {
		name, fixture string
		requests      int
	}{
		{"milestone_resume", "testdata/graphql/new_milestone_resume.json", 2}, // read, updateIssue
		{"milestone_kept", newMilestoneKept, 1},                               // read
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			srv := withApp(t, boardFixture, tt.fixture)
			before := warmSchema(t, srv)
			code, out := runOut(append(slices.Clone(newFixArgs), "--milestone", "v0.1")...)
			testutil.Golden(t, "new/"+tt.name, out)
			if code != render.ExitOK {
				t.Errorf("code = %d", code)
			}
			if n := srv.Requests() - before; n != tt.requests {
				t.Errorf("requests with a warm schema = %d, want %d", n, tt.requests)
			}
		})
	}
}

// TestNewMilestoneKeptVerb: the warning about a milestone kept names a
// verb that only sets the --milestone milestone and starts from the
// issue's status (In progress): not plan, which also comments, nor
// backlog-plan, which starts only from Backlog. Without one it names gh
// issue edit.
func TestNewMilestoneKeptVerb(t *testing.T) {
	for _, tt := range []struct{ name, config string }{
		{"milestone_kept_verb", milestoneDojoConfig + `
[verbs.backlog-plan]
when = "Plan an issue not started yet"
from = ["Backlog"]
milestone = "{milestone}"

[verbs.move]
when = "The issue moves to another stage"
milestone = "{milestone}"
`},
		{"milestone_kept_no_verb", milestoneDojoConfig},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, tt.config)
			srv := withApp(t, boardFixture, newMilestoneKept)
			warmSchema(t, srv)
			code, out := runOut(append(slices.Clone(newFixArgs), "--milestone", "v0.1")...)
			testutil.Golden(t, "new/"+tt.name, out)
			if code != render.ExitOK {
				t.Errorf("code = %d", code)
			}
		})
	}
}

// TestNewUnknownMilestone: an unknown milestone fails after the read and
// before any write, listing the open milestones, and so does a start of
// several titles, listing them; these fixtures have no
// mutation to answer. Past the first 100, all open milestones are read.
func TestNewUnknownMilestone(t *testing.T) {
	tests := []struct {
		name, fixture, milestone string
	}{
		{"milestone_unknown", "testdata/graphql/new_milestone_read.json", "v02"},
		{"milestone_ambiguous", "testdata/graphql/new_milestone_read.json", "v0"},
		{"milestone_none", "testdata/graphql/new_milestone_none.json", "v0.1"},
		{"milestone_pages", "testdata/graphql/new_milestone_pages.json", "v1.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			withApp(t, boardFixture, tt.fixture)
			code, out := runOut(append(slices.Clone(newFixArgs), "--milestone", tt.milestone)...)
			testutil.Golden(t, "new/"+tt.name, out)
			if code != render.ExitUsage {
				t.Errorf("code = %d, want %d", code, render.ExitUsage)
			}
		})
	}
}

// TestNewDashTitle: flags go before --, the title after it; a flag after
// -- is part of the arguments. Both fail before any request here.
func TestNewDashTitle(t *testing.T) {
	withConfig(t, dojoConfig+"\n[labels]\narea_prefix = \"\"\n")
	tests := []struct {
		name string
		args []string
	}{
		// --area is parsed: refused as labels.area_prefix is not set.
		{"dash_title_flags_before", []string{"new", "--area", "cli", "--", "-v is ignored"}},
		// --type after -- is two more arguments.
		{"dash_title_flags_after", []string{"new", "--", "-v is ignored", "--type", "bug"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runOut(tt.args...)
			testutil.Golden(t, "new/"+tt.name, out)
			if code != render.ExitUsage {
				t.Errorf("code = %d", code)
			}
		})
	}
}

func TestNewLabelGroups(t *testing.T) {
	// --area without labels.area_prefix: refused before any request.
	withConfig(t, dojoConfig+"\n[labels]\narea_prefix = \"\"\n")
	code, out := runOut("new", "Fix login redirect", "--area", "cli")
	testutil.Golden(t, "new/area_without_prefix", out)
	if code != render.ExitUsage {
		t.Errorf("code = %d", code)
	}

	// A type prefix no label of the repo has: --type finds nothing, and
	// the error says so rather than listing nothing.
	withConfig(t, dojoConfig+"\n[labels]\ntype_prefix = \"type: \"\n")
	withApp(t, boardFixture, newContextFixture)
	code, out = runOut("new", "Fix login redirect", "--type", "feature")
	testutil.Golden(t, "new/type_missing_in_repo", out)
	if code != render.ExitUsage {
		t.Errorf("code = %d", code)
	}
}

func TestNewUsage(t *testing.T) {
	withConfig(t, dojoConfig)
	tests := []struct {
		name string
		args []string
	}{
		{"no_title", []string{"new"}},
		{"two_titles", []string{"new", "Fix", "login"}},
		{"blank_title", []string{"new", "  "}},
		{"two_bodies", []string{"new", "Fix login", "--body", "x", "--body-file", "-"}},
		{"missing_body_file", []string{"new", "Fix login", "--body-file", "testdata/nope.md"}},
		{"empty_milestone", []string{"new", "Fix login", "--milestone", " "}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runOut(tt.args...)
			testutil.Golden(t, "new/"+tt.name, out)
			if code != render.ExitUsage {
				t.Errorf("code = %d", code)
			}
		})
	}
}

func TestNewHelp(t *testing.T) {
	withConfig(t, dojoConfig)
	code, out := runOut("new", "--help")
	testutil.Golden(t, "new/help", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
}

// runIn is runOut with stdin.
func runIn(stdin string, args ...string) (render.Code, []byte) {
	return runRootOut(func() (*cobra.Command, error) {
		root, err := newRootCmd()
		if root != nil {
			root.SetIn(strings.NewReader(stdin))
		}
		return root, err
	}, args...)
}
