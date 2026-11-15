package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// checkNewRuns runs gh dojo new commands in order against srv, checking
// each one's exit code, requests and output (testdata/new/<golden>).
func checkNewRuns(t *testing.T, srv *graphtest.Server, before int, steps ...runs) {
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
		testutil.Golden(t, "new/"+r.golden, out)
	}
}

// newAssigneeArgs are newFixArgs with assignees: @me, octocat, and the
// viewer's own login again, which counts once.
var newAssigneeArgs = append(slices.Clone(newFixArgs), "--assignee", "@me", "--assignee", "octocat, trum-ok")

// TestNewAssignee: the assignees are looked up in new's read (each login
// among the assignable users, the viewer for @me) and set by createIssue:
// still 4 requests with a warm schema.
func TestNewAssignee(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, verbFixture("new_assignee"))
	checkNewRuns(t, srv, warmSchema(t, srv), runs{newAssigneeArgs, render.ExitOK, 4, "assignee"})
}

// TestNewAssigneeDryRun: the dry run reads and checks the assignees as the
// run does, and shows them in the plan and the JSON.
func TestNewAssigneeDryRun(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, verbFixture("new_assignee_dry_run"))
	args := append(slices.Clone(newAssigneeArgs), "--dry-run")
	checkNewRuns(t, srv, warmSchema(t, srv),
		runs{args, render.ExitOK, 1, "assignee_dry_run"},
		runs{append(args, "--json"), render.ExitOK, 1, "assignee_dry_run_json"},
	)
}

// TestNewAssigneeResume: a re-run finds the issue done with one of the
// assignees and assigns the other alone, keeping the one it has; the next
// finds both and writes nothing. The dry run of the first shows that plan.
func TestNewAssigneeResume(t *testing.T) {
	withConfig(t, dojoConfig)
	args := append(slices.Clone(newFixArgs), "--assignee", "trum-ok,octocat")
	srv := withApp(t, boardFixture, verbFixture("new_assignee_resume_dry_run"))
	checkNewRuns(t, srv, warmSchema(t, srv),
		runs{append(slices.Clone(args), "--dry-run"), render.ExitOK, 1, "assignee_resume_dry_run"})
	srv = withApp(t, boardFixture, verbFixture("new_assignee_resume"))
	checkNewRuns(t, srv, warmSchema(t, srv),
		runs{args, render.ExitOK, 2, "assignee_resume"},
		runs{append(slices.Clone(args), "--json"), render.ExitOK, 1, "assignee_repeat_json"},
	)
}

// TestNewAssigneeRefused: logins that cannot be assigned in the issue's
// repository (--repo's) fail after the read, all named at once, with who
// can be assigned (one more read); an issue an earlier run left with as
// many assignees as GitHub allows is refused before any write.
func TestNewAssigneeRefused(t *testing.T) {
	withConfig(t, reposConfig)
	srv := withApp(t, boardFixture, verbFixture("new_assignee_unknown"))
	checkNewRuns(t, srv, warmSchema(t, srv),
		runs{append(slices.Clone(newFixArgs), "--repo", "acme/api", "--assignee", "octocat,octo-nobody,ghost"),
			render.ExitUsage, 2, "assignee_unknown"})
	srv = withApp(t, boardFixture, verbFixture("new_assignee_full"))
	checkNewRuns(t, srv, warmSchema(t, srv),
		runs{append(slices.Clone(newFixArgs), "--assignee", "octocat"), render.ExitUsage, 1, "assignee_full"})
}

// TestNewAssigneeUsage: a value that is not a login, an empty one or more
// than GitHub allows fail before any request (exit 1).
func TestNewAssigneeUsage(t *testing.T) {
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = "user" + strings.Repeat("x", i)
	}
	for _, tt := range []struct{ name, value string }{
		{"assignee_at", "@octocat"},
		{"assignee_not_login", "octo cat"},
		{"assignee_empty", " , "},
		{"assignee_too_many", strings.Join(eleven, ",")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			code, out := runOut("new", "Fix login redirect", "--assignee", tt.value)
			testutil.Golden(t, "new/"+tt.name, out)
			if code != render.ExitUsage {
				t.Errorf("code = %d", code)
			}
		})
	}
}

func TestParseAssignees(t *testing.T) {
	got, err := parseAssignees([]string{"@me", "octocat, Monalisa", "OCTOCAT", "monalisa,"}, "help")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"@me", "octocat", "Monalisa"}; !slices.Equal(got, want) {
		t.Errorf("parseAssignees = %q, want %q", got, want)
	}
}

// TestNewContextQueryAssignees: without --assignee the read is
// newContextQuery as recorded; with it, each login is looked up under its
// own alias, @me reads the viewer, and the recent issues their assignees.
func TestNewContextQueryAssignees(t *testing.T) {
	if q := newContextQueryWith(nil, false, nil); q != newContextQuery {
		t.Errorf("without assignees the query changed:\n%s", q)
	}
	q := newContextQueryWith(nil, false, []string{"@me", "octocat", "monalisa"})
	for _, want := range []string{
		"$window: Int!, $assignee0: String!, $assignee1: String!)",
		"    assignee0: assignableUsers(query: $assignee0, first: 100) { nodes { id login } }\n    assignee1: assignableUsers(query: $assignee1",
		newIssueAssignees,
		"  viewer { id login }\n}",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %q:\n%s", want, q)
		}
	}
	if strings.Contains(q, "assignee2") {
		t.Errorf("@me got an alias:\n%s", q)
	}
}
