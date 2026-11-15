package main

import (
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// examples/review.toml is a flow configured without code: a code review
// process with no preset. Its board and the cards its verbs read are
// hand-crafted (see the fixtures' comments).
const (
	reviewExample        = "../../examples/review.toml"
	reviewBoardFixture   = "testdata/graphql/review_board.json"
	reviewExampleFixture = "testdata/graphql/review_example.json"
	reviewOnHoldFixture  = "testdata/graphql/review_on_hold.json"
)

// reviewDryRuns is a dry run of every verb of the example, each on its own
// issue of review_example.json in a status the verb starts from.
var reviewDryRuns = map[string][]string{
	"start":           {"start", "21"},
	"hold":            {"hold", "22", "--by", "17"},
	"resume":          {"resume", "23"},
	"review":          {"review", "24", "--comment", "acme/payments#30", "--assignee", "octocat"},
	"request-changes": {"request-changes", "25", "--comment", "Round the refund to the currency's minor unit."},
	"approve":         {"approve", "26"},
	"reject":          {"reject", "27", "--comment", "Cheques are out of scope."},
	"merged":          {"merged", "28"},
}

// TestExampleReview: the example loads, describe explains it, a dry run of
// each verb plans its effects on a board with its statuses, and agent
// install writes its skill.
func TestExampleReview(t *testing.T) {
	data, err := os.ReadFile(reviewExample)
	if err != nil {
		t.Fatal(err)
	}
	toml := string(data)
	c, err := config.Parse(data, "examples/review.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range c.VerbNames() {
		if _, ok := reviewDryRuns[verb]; !ok {
			t.Errorf("verb %s has no dry run in reviewDryRuns", verb)
		}
	}

	t.Run("describe", func(t *testing.T) {
		withConfig(t, toml)
		code, out := runOut("describe")
		if code != render.ExitOK {
			t.Errorf("exit code = %d, want 0", code)
		}
		testutil.Golden(t, "examples/review_describe", out)
	})

	t.Run("dry_run", func(t *testing.T) {
		withConfig(t, toml)
		saved := getenv
		t.Cleanup(func() { getenv = saved })
		getenv = func(name string) string {
			if name == "CLAUDE_CODE_HOST_SESSION_ID" {
				return "abc"
			}
			return ""
		}
		withApp(t, reviewBoardFixture, reviewExampleFixture)
		for _, verb := range slices.Sorted(maps.Keys(reviewDryRuns)) {
			code, out := runOut(append(reviewDryRuns[verb], "--dry-run")...)
			if code != render.ExitOK {
				t.Errorf("%s: exit code = %d, want 0\n%s", verb, code, out)
			}
			testutil.Golden(t, "examples/review_"+verb, out)
		}
	})

	// The help of the verbs that check the label blocked: hold and review
	// refuse it, naming resume, which needs it.
	t.Run("help", func(t *testing.T) {
		withConfig(t, toml)
		for _, verb := range []string{"hold", "resume", "review"} {
			code, out := runOut(verb, "--help")
			if code != render.ExitOK {
				t.Errorf("%s --help: exit code = %d, want 0\n%s", verb, code, out)
			}
			testutil.Golden(t, "examples/review_help_"+verb, out)
		}
	})

	// On hold is the label blocked: review refuses #29 on hold, naming
	// resume; merged on #30 (on hold) and #32 (merged already) refuses
	// #30 alone, in a dry run as in the run, with exit code 2 and one read.
	t.Run("on_hold", func(t *testing.T) {
		withConfig(t, toml)
		srv := withApp(t, reviewBoardFixture, reviewOnHoldFixture)
		checkRuns(t, srv, warmSchema(t, srv),
			runs{[]string{"review", "29", "--comment", "acme/payments#31", "--assignee", "octocat", "--dry-run"},
				render.ExitDenied, 1, "review_on_hold"},
			runs{[]string{"merged", "30", "32", "--dry-run"}, render.ExitDenied, 1, "merged_on_hold_dry_run"},
			runs{[]string{"merged", "30", "32", "--json"}, render.ExitDenied, 1, "merged_on_hold"},
		)
	})

	t.Run("agent_install", func(t *testing.T) {
		dir := withRepo(t, toml, nil)
		if code, out := runOut("agent", "install"); code != render.ExitOK {
			t.Fatalf("exit code = %d\n%s", code, out)
		}
		testutil.Golden(t, "agent/skill_review_example", []byte(readRepoFile(t, dir, skillPath)))
	})
}
