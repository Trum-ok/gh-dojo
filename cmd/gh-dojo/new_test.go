package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/issue"
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
)

var newFixArgs = []string{"new", "Fix login redirect", "--type", "bug", "--area", "cli",
	"--body", "The redirect drops the query string."}

func TestNewContextRecorded(t *testing.T) {
	srv := graphtest.New(t, newContextFixture)
	repo := issue.Repo{Owner: "Trum-ok", Name: "gh-dojo"}
	nc, err := readNewContext(context.Background(), srv.Client(), repo, "Status")
	if err != nil {
		t.Fatal(err)
	}
	if nc.RepoID == "" || len(nc.Issues) == 0 || len(nc.Issues) > newDedupWindow {
		t.Errorf("repo %q, %d issues", nc.RepoID, len(nc.Issues))
	}
	if !slices.ContainsFunc(nc.Labels.Nodes, func(l newLabel) bool { return l.Name == "bug" }) {
		t.Errorf("labels = %v, want bug among them", nc.Labels.Nodes)
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
	if n := srv.Requests() - before; n != 3 {
		t.Errorf("requests with a warm schema = %d, want 3 (read, createIssue, status)", n)
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
	if n := srv.Requests() - before; n != 3 {
		t.Errorf("requests = %d, want 3 (read, create, status): no second attempt", n)
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

// TestNewStaleBeforeCreate: a stale schema ID before anything was written
// re-reads the board and tries once more.
func TestNewStaleBeforeCreate(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, "testdata/graphql/new_stale_create.json")
	before := warmSchema(t, srv)
	code, out := runOut(newFixArgs...)
	testutil.Golden(t, "new/success", out)
	if code != render.ExitOK {
		t.Errorf("code = %d", code)
	}
	if n := srv.Requests() - before; n != 6 {
		t.Errorf("requests = %d, want 6 (read, create, board, read, create, status)", n)
	}
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
	saved := newRecheckDelay
	t.Cleanup(func() { newRecheckDelay = saved })
	newRecheckDelay = 0
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

// TestNewSameTitle: the marker hashes the title alone, case and spacing
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
	if n := srv.Requests(); n != 3 {
		t.Errorf("requests = %d, want 3 (schema, read, createIssue)", n)
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

func TestNewConfiguredTypes(t *testing.T) {
	typed := dojoConfig + "\n[labels]\ntypes = [\"bug\", \"feature\"]\narea_prefix = \"area:\"\n"

	// Outside labels.types: refused before any request (newApp is noApp).
	withConfig(t, typed)
	code, out := runOut("new", "Fix login redirect", "--type", "chore")
	testutil.Golden(t, "new/type_not_configured", out)
	if code != render.ExitUsage {
		t.Errorf("code = %d", code)
	}

	// --area without labels.area_prefix: refused before any request.
	withConfig(t, dojoConfig+"\n[labels]\narea_prefix = \"\"\n")
	code, out = runOut("new", "Fix login redirect", "--area", "cli")
	testutil.Golden(t, "new/area_without_prefix", out)
	if code != render.ExitUsage {
		t.Errorf("code = %d", code)
	}

	// In labels.types but not in the repo: a config problem.
	withConfig(t, typed)
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
