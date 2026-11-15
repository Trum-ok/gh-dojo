package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

const (
	doctorFixtures = "testdata/graphql/doctor_"
)

// TestDoctor runs doctor with a cold schema cache on recorded boards and
// hand-crafted token answers. Every run sends the Doctor query, plus the
// board schema when the token works, plus the DoctorLabels query when a
// repository is usable (dojoConfig's preset sets labels.area_prefix).
func TestDoctor(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		fixtures []string
		code     render.Code
		requests int
	}{
		{"ok", dojoConfig, []string{doctorFixtures + "ok.json", boardFixture}, render.ExitOK, 3},
		{"read_project", dojoConfig, []string{doctorFixtures + "read_project.json", boardFixture}, render.ExitNetwork, 3},
		{"no_scope", dojoConfig, []string{doctorFixtures + "no_scope.json", boardFixtures + "insufficient_scopes.json"}, render.ExitNetwork, 3},
		{"unauthorized", dojoConfig, []string{doctorFixtures + "401.json"}, render.ExitNetwork, 1},
		{"status_typo", dojoConfig + `
[verbs.start]
to = "In progres"

[verbs.wait]
from = ["In Progress"]
`, []string{doctorFixtures + "ok.json", boardFixture}, render.ExitUsage, 3},
		{"board_not_found", strings.Replace(dojoConfig, "project = 5", "project = 99", 1),
			[]string{doctorFixtures + "ok.json", boardFixtures + "board_not_found.json"}, render.ExitUsage, 3},
		{"repos", strings.Replace(dojoConfig, `repos = ["Trum-ok/gh-dojo"]`,
			`repos = ["Trum-ok/gh-dojo", "Trum-ok/no-such-repo", "Trum-ok/wiki"]`, 1),
			[]string{doctorFixtures + "repos.json", boardFixture}, render.ExitUsage, 3},
		// labels.types and labels.area_prefix take one DoctorLabels query
		// for every repository: a missing type is a problem, one in
		// another case and no area label are warnings.
		{"labels", strings.Replace(dojoConfig, `repos = ["Trum-ok/gh-dojo"]`,
			`repos = ["Trum-ok/gh-dojo", "Trum-ok/gh-dojo-site"]`, 1) + `
[labels]
types = ["bug", "Enhancement", "chore"]
area_prefix = "service:"
`, []string{doctorFixtures + "labels.json", boardFixture}, render.ExitUsage, 3},
		// The preset's area_prefix written in the config itself: no area
		// label is a warning, as with any prefix the config sets.
		{"area_in_config", dojoConfig + `
[labels]
area_prefix = "area:"
`, []string{doctorFixtures + "ok.json", boardFixture}, render.ExitOK, 3},
		// Over 100 labels: a type the search finds needs no more reads, an
		// area count is a lower bound and none found is not confirmed; a
		// type not found reads all the labels (two pages for 150).
		{"labels_many", strings.Replace(dojoConfig, `repos = ["Trum-ok/gh-dojo"]`,
			`repos = ["Trum-ok/big", "Trum-ok/huge", "Trum-ok/paged"]`, 1) + `
[labels]
types = ["bug", "Docs"]
`, []string{doctorFixtures + "labels_many.json", boardFixture}, render.ExitUsage, 5},
		// Verbs' labels and assignees take one more read: each label
		// missing or spelled in another case and each user who cannot be
		// assigned is a problem.
		{"verbs", effectsDojoConfig, []string{doctorFixtures + "ok.json", boardFixture, doctorFixtures + "verbs.json"},
			render.ExitUsage, 4},
		{"verbs_ok", doctorVerbsOKConfig, []string{doctorFixtures + "ok.json", boardFixture, doctorFixtures + "verbs_ok.json"},
			render.ExitOK, 4},
		// A login to unassign who cannot be assigned is a warning: the verb
		// removes current assignees by ID, and the login is likely a typo.
		{"verbs_unassign", doctorVerbsOKConfig + "\n[verbs.leave]\nwhen = \"x\"\nunassign = \"octo-nobody\"\n",
			[]string{doctorFixtures + "ok.json", boardFixture, doctorFixtures + "verbs_unassign.json"}, render.ExitOK, 4},
		// The labels of from_labels and refuse_labels are checked too: a
		// missing one refuses every issue or never refuses; one in another
		// case is fine, since the checks match names regardless of case.
		{"verbs_label_checks", doctorVerbsOKConfig + `
[verbs.tag]
when = "It is a bug"
labels = ["+bug"]

[verbs.poke]
when = "Ask how it goes"
comment = "{comment}"
refuse_labels = ["Bug"]

[verbs.resume]
when = "Back to work"
comment = "{comment}"
from_labels = ["on-hold"]

[verbs.ship]
when = "Ship it"
comment = "{comment}"
refuse_labels = ["wip"]
`, []string{doctorFixtures + "ok.json", boardFixture, doctorFixtures + "verbs_label_checks.json"}, render.ExitUsage, 4},
		// The organization's SAML SSO blocks both the repository and the
		// board: each gets the SSO fix.
		{"sso", orgConfig, []string{doctorFixtures + "sso.json", boardFixtures + "org_board_saml.json"}, render.ExitNetwork, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, tt.config)
			srv := withApp(t, tt.fixtures...)
			code, out := runOut("doctor")
			if code != tt.code {
				t.Errorf("exit code = %d, want %d", code, tt.code)
			}
			if n := srv.Requests(); n != tt.requests {
				t.Errorf("requests = %d, want %d", n, tt.requests)
			}
			if strings.Contains(string(out), graphtest.FakeToken) {
				t.Error("the token leaked into the output")
			}
			testutil.Golden(t, "doctor/"+tt.name, out)
		})
	}
}

// TestDoctorLabelsFailed: a failed DoctorLabels query is a problem of the
// labels check alone; the token, board and repositories stay ok.
func TestDoctorLabelsFailed(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, doctorLabels502Fixture(t), boardFixture)
	code, out := runOut("doctor")
	if code != render.ExitNetwork {
		t.Errorf("exit code = %d, want %d", code, render.ExitNetwork)
	}
	if n := srv.Requests(); n != 2+graph.MaxAttempts {
		t.Errorf("requests = %d, want %d (DoctorLabels retried)", n, 2+graph.MaxAttempts)
	}
	testutil.Golden(t, "doctor/labels_502", out)
}

// doctorLabels502Fixture writes doctor_ok.json with GitHub answering 502 to
// the DoctorLabels query, and returns its path.
func doctorLabels502Fixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(doctorFixtures + "ok.json")
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	for _, it := range f["interactions"].([]any) {
		it := it.(map[string]any)
		if it["operation"] == "DoctorLabels" {
			it["status"], it["repeat"] = 502, true
			it["headers"] = map[string]string{"Content-Type": "text/html"}
			it["response"] = "<html><body><h1>502 Bad Gateway</h1></body></html>"
		}
	}
	if data, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "doctor_labels_502.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDoctorCached: with a warm schema cache doctor sends no board request.
func TestDoctorCached(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, doctorFixtures+"ok.json", boardFixture)
	if _, err := newApp().Schema(context.Background()); err != nil { // warms the cache
		t.Fatal(err)
	}
	before := srv.Requests()
	code, out := runOut("doctor")
	if code != render.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}
	if n := srv.Requests() - before; n != 2 {
		t.Errorf("requests = %d, want 2 (Doctor, DoctorLabels)", n)
	}
	testutil.Golden(t, "doctor/ok_cached", out)
}

// TestDoctorClosedBoard: a closed board is a problem even when the config
// matches it.
func TestDoctorClosedBoard(t *testing.T) {
	withConfig(t, dojoConfig)
	withApp(t, doctorFixtures+"ok.json", closedBoardFixture(t))
	code, out := runOut("doctor")
	if code != render.ExitUsage {
		t.Errorf("exit code = %d, want %d", code, render.ExitUsage)
	}
	testutil.Golden(t, "doctor/closed_board", out)
}

// closedBoardFixture writes boardFixture with the board closed and returns
// its path.
func closedBoardFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(boardFixture)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	f["handcrafted"] = true
	resp := f["interactions"].([]any)[0].(map[string]any)["response"].(map[string]any)
	resp["data"].(map[string]any)["repositoryOwner"].(map[string]any)["projectV2"].(map[string]any)["closed"] = true
	if data, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "user_board_closed.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDoctorNoConfig: the token is still checked; the board and the
// repositories need the config.
func TestDoctorNoConfig(t *testing.T) {
	srv := withApp(t, doctorFixtures+"viewer.json") // TestMain's directory has no config
	code, out := runOut("doctor")
	if code != render.ExitUsage {
		t.Errorf("exit code = %d, want %d", code, render.ExitUsage)
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
	testutil.Golden(t, "doctor/no_config", out)
}

// TestDoctorEnvToken: gh auth refresh cannot fix a token from GH_TOKEN.
func TestDoctorEnvToken(t *testing.T) {
	t.Setenv("GH_TOKEN", graphtest.FakeToken)
	withConfig(t, dojoConfig)
	srv := graphtest.New(t, doctorFixtures+"read_project.json", boardFixture)
	withClient(t, graph.New(graph.Options{Host: "github.com", Transport: srv}))
	code, out := runOut("doctor")
	if code != render.ExitNetwork {
		t.Errorf("exit code = %d, want %d", code, render.ExitNetwork)
	}
	testutil.Golden(t, "doctor/env_token", out)
}

// TestDoctorNotLoggedIn: no token at all sends nothing.
func TestDoctorNotLoggedIn(t *testing.T) {
	for _, env := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		t.Setenv(env, "")
	}
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_PATH", "/nonexistent/gh")
	withConfig(t, dojoConfig)
	c := graph.New(graph.Options{Host: "github.com", Transport: noTransport{}})
	withClient(t, c)
	code, out := runOut("doctor")
	if code != render.ExitNetwork {
		t.Errorf("exit code = %d, want %d", code, render.ExitNetwork)
	}
	if c.Requests() != 0 {
		t.Errorf("requests = %d, want 0", c.Requests())
	}
	testutil.Golden(t, "doctor/not_logged_in", out)
}

func TestDoctorArgs(t *testing.T) {
	withConfig(t, dojoConfig)
	code, out := runOut("doctor", "now")
	if code != render.ExitUsage {
		t.Errorf("exit code = %d, want %d", code, render.ExitUsage)
	}
	testutil.Golden(t, "doctor/args", out)
}

// TestDoctorSprint calls the sprint check directly, with sprint fields the
// config would reject against the board.
func TestDoctorSprint(t *testing.T) {
	srv := graphtest.New(t, boardFixtures+"iteration_board.json")
	l := board.NewLoader(srv.Client())
	l.Dir, l.Host = t.TempDir(), "github.com"
	s, err := l.Load(context.Background(), board.Ref{Owner: "acme", Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(`
version = 1
[board]
owner = "acme"
project = 3
[fields]
status = "Status"
`), ".github/dojo.toml")
	if err != nil {
		t.Fatal(err)
	}
	r := &doctorReport{}
	checkSprint(r, cfg, s, testNow, nil) // no fields.sprint: no line
	sprints, err := config.Parse([]byte(sprintsPresetConfig), ".github/dojo.toml")
	if err != nil {
		t.Fatal(err)
	}
	checkSprint(r, sprints, s, testNow, nil) // the sprints preset's Sprint field
	for _, tt := range []struct {
		field string
		now   time.Time
	}{
		{"Sprint", testNow},
		{"Sprint", time.Date(2026, 12, 20, 9, 0, 0, 0, time.UTC)}, // after the last iteration
		{"Sprint", time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)},  // in Sprint 11, completed: verbs take it
		{"Status", testNow},
		{"Nope", testNow}, // board.Problems reports a missing field
	} {
		cfg.Fields["sprint"] = tt.field
		checkSprint(r, cfg, s, tt.now, nil)
	}

	// A cached schema without today's iteration is re-read once: Sprint
	// 12 was added since. A fresh schema is not.
	cfg.Fields["sprint"] = "Sprint"
	stale := *s
	stale.Cached = true
	stale.Fields = slices.Clone(s.Fields)
	for i, f := range stale.Fields {
		if f.Name == "Sprint" {
			stale.Fields[i].Iterations = slices.DeleteFunc(slices.Clone(f.Iterations),
				func(it board.Iteration) bool { return it.Title == "Sprint 12" })
		}
	}
	refetches := 0
	refetch := func() (*board.Schema, error) { refetches++; return s, nil }
	checkSprint(r, cfg, &stale, testNow, refetch)
	stale.Cached = false
	checkSprint(r, cfg, &stale, testNow, refetch)
	if refetches != 1 {
		t.Errorf("%d re-reads of the board, want 1 (for the cached schema only)", refetches)
	}

	// board.timezone: 22:30 UTC on Nov 22 is Nov 23 in Moscow, Sprint 13.
	tz, err := config.Parse([]byte(`
version = 1
[board]
owner = "acme"
project = 3
timezone = "Europe/Moscow"
[fields]
status = "Status"
sprint = "Sprint"
`), ".github/dojo.toml")
	if err != nil {
		t.Fatal(err)
	}
	checkSprint(r, tz, s, time.Date(2026, 11, 22, 22, 30, 0, 0, time.UTC), nil)
	var b strings.Builder
	r.write(&b)
	testutil.Golden(t, "doctor/sprint", []byte(b.String()))
}

// TestDoctorLabelsFound: label names come from the first page and the
// searches; they are complete only when the first page has every label.
func TestDoctorLabelsFound(t *testing.T) {
	c := &labelsCheck{types: []string{"bug"}, prefix: "area:"}
	for _, tt := range []struct {
		name     string
		answer   string
		names    int
		complete bool
	}{
		{"one page", `{"labels": {"totalCount": 2, "nodes": [{"name": "bug"}, {"name": "area:cli"}]},
			"type0": {"nodes": [{"name": "bug"}]}, "area": {"nodes": [{"name": "area:cli"}]}}`, 2, true},
		// Search found these past the first page, but not necessarily all.
		{"more labels", `{"labels": {"totalCount": 101, "nodes": [{"name": "x"}]},
			"type0": {"nodes": [{"name": "Bug"}]}, "area": {"nodes": [{"name": "area:cli"}, {"name": "area:db"}]}}`, 4, false},
		{"repository null", `null`, 0, true},
	} {
		f := c.found(json.RawMessage(tt.answer))
		if len(f.names) != tt.names || f.complete != tt.complete {
			t.Errorf("%s: %d names, complete %v; want %d, %v", tt.name, len(f.names), f.complete, tt.names, tt.complete)
		}
	}
	if f := c.found(json.RawMessage(`{"type0": {"nodes": [{"name": "Bug"}]}}`)); !f.has("bug") || f.has("bugs") {
		t.Error("has: want a match regardless of case, and only of the whole name")
	}
}

// withClient makes newApp use c and a private schema cache.
func withClient(t *testing.T, c *graph.Client) {
	t.Helper()
	dir := t.TempDir()
	saved := newApp
	t.Cleanup(func() { newApp = saved })
	newApp = func() *App {
		l := board.NewLoader(c)
		l.Dir, l.Host, l.Now = dir, "github.com", func() time.Time { return testNow }
		return &App{
			Client: c, Loader: l,
			CurrentRepo: func() (string, error) { return "", errors.New("no git remote in tests") },
		}
	}
}

// noTransport fails every request.
type noTransport struct{}

func (noTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network in tests")
}

// doctorVerbsOKConfig has verbs whose labels and assignees all match the
// gh-dojo repository.
var doctorVerbsOKConfig = strings.Replace(dojoConfig, "repos = [\"Trum-ok/gh-dojo\"]\n",
	"repos = [\"Trum-ok/gh-dojo\"]\nassignee = \"@me\"\n", 1) + `
[verbs.review]
when = "A pull request waits for review"
labels = ["+enhancement", "-dogfood"]
assign = true

[verbs.hand]
when = "Hand it to the owner"
assign = "trum-ok"
`

// TestVerbsCheckUsers: doctor checks the users verbs assign and unassign
// by login or "@me" (the viewer), each once regardless of case; --assignee
// and unassign = "all" name nobody to check.
func TestVerbsCheckUsers(t *testing.T) {
	cfg, err := config.Parse([]byte(verbDojoConfig+`
[verbs.pass]
when = "Pass it on"
assign = "{assignee}"
unassign = "@me"

[verbs.hand-back]
when = "Back to the author alone"
assign = "Octocat"
unassign = "all"

[verbs.leave]
when = "Octocat leaves"
unassign = "octocat"
`), ".github/dojo.toml")
	if err != nil {
		t.Fatal(err)
	}
	if got := newVerbsCheck(cfg, "Trum-ok").users; !slices.Equal(got, []string{"Trum-ok", "Octocat"}) {
		t.Errorf("users = %q, want Trum-ok (@me) and Octocat", got)
	}
}
