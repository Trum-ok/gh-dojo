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
)

// reviewDryRuns is a dry run of every verb of the example, each on its own
// issue of review_example.json in a status the verb starts from.
var reviewDryRuns = map[string][]string{
	"start":           {"start", "21"},
	"hold":            {"hold", "22", "--by", "17"},
	"resume":          {"resume", "23"},
	"review":          {"review", "24", "--comment", "acme/payments#30"},
	"request-changes": {"request-changes", "25", "--comment", "Round the refund to the currency's minor unit."},
	"approve":         {"approve", "26"},
	"reject":          {"reject", "27", "--comment", "Cheques are out of scope."},
	"merge":           {"merge", "28"},
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

	t.Run("agent_install", func(t *testing.T) {
		dir := withRepo(t, toml, nil)
		if code, out := runOut("agent", "install"); code != render.ExitOK {
			t.Fatalf("exit code = %d\n%s", code, out)
		}
		testutil.Golden(t, "agent/skill_review_example", []byte(readRepoFile(t, dir, skillPath)))
	})
}
