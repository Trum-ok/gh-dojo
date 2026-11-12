package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// board schema when the token works.
func TestDoctor(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		fixtures []string
		code     render.Code
		requests int
	}{
		{"ok", dojoConfig, []string{doctorFixtures + "ok.json", boardFixture}, render.ExitOK, 2},
		{"read_project", dojoConfig, []string{doctorFixtures + "read_project.json", boardFixture}, render.ExitNetwork, 2},
		{"no_scope", dojoConfig, []string{doctorFixtures + "no_scope.json", boardFixtures + "insufficient_scopes.json"}, render.ExitNetwork, 2},
		{"unauthorized", dojoConfig, []string{doctorFixtures + "401.json"}, render.ExitNetwork, 1},
		{"status_typo", dojoConfig + `
[verbs.start]
to = "In progres"

[verbs.wait]
from = ["In Progress"]
`, []string{doctorFixtures + "ok.json", boardFixture}, render.ExitUsage, 2},
		{"board_not_found", strings.Replace(dojoConfig, "project = 5", "project = 99", 1),
			[]string{doctorFixtures + "ok.json", boardFixtures + "board_not_found.json"}, render.ExitUsage, 2},
		{"repos", strings.Replace(dojoConfig, `repos = ["Trum-ok/gh-dojo"]`,
			`repos = ["Trum-ok/gh-dojo", "Trum-ok/no-such-repo", "Trum-ok/wiki"]`, 1),
			[]string{doctorFixtures + "repos.json", boardFixture}, render.ExitUsage, 2},
		// labels.types and labels.area_prefix come with the Doctor query,
		// for each repository: a missing type is a problem, one in another
		// case and no area label are warnings.
		{"labels", strings.Replace(dojoConfig, `repos = ["Trum-ok/gh-dojo"]`,
			`repos = ["Trum-ok/gh-dojo", "Trum-ok/gh-dojo-site"]`, 1) + `
[labels]
types = ["bug", "Enhancement", "chore"]
area_prefix = "service:"
`, []string{doctorFixtures + "labels.json", boardFixture}, render.ExitUsage, 2},
		// Verbs' labels and assignees take one more read: each label
		// missing or spelled in another case and each user who cannot be
		// assigned is a problem.
		{"verbs", effectsDojoConfig, []string{doctorFixtures + "ok.json", boardFixture, doctorFixtures + "verbs.json"},
			render.ExitUsage, 3},
		{"verbs_ok", doctorVerbsOKConfig, []string{doctorFixtures + "ok.json", boardFixture, doctorFixtures + "verbs_ok.json"},
			render.ExitOK, 3},
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

// TestDoctorCached: with a warm schema cache doctor sends one request.
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
	if n := srv.Requests() - before; n != 1 {
		t.Errorf("requests = %d, want 1", n)
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
// searches; the area count is exact when the first page has every label or
// the area search was not cut at a page.
func TestDoctorLabelsFound(t *testing.T) {
	c := &labelsCheck{types: []string{"bug"}, prefix: "area:"}
	page := func(n int, prefix string) string {
		var nodes []string
		for i := range n {
			nodes = append(nodes, fmt.Sprintf(`{"name": "%s%d"}`, prefix, i))
		}
		return `{"nodes": [` + strings.Join(nodes, ", ") + `]}`
	}
	for _, tt := range []struct {
		name     string
		answer   string
		names    int
		complete bool
	}{
		{"one page", `{"labels": {"totalCount": 2, "nodes": [{"name": "bug"}, {"name": "area:cli"}]},
			"type0": {"nodes": [{"name": "bug"}]}, "area": {"nodes": [{"name": "area:cli"}]}}`, 2, true},
		{"more labels, area search complete", `{"labels": {"totalCount": 300, "nodes": []},
			"type0": {"nodes": [{"name": "Bug"}]}, "area": ` + page(3, "area:") + `}`, 4, true},
		{"more labels, area search cut", `{"labels": {"totalCount": 300, "nodes": []},
			"area": ` + page(labelsPage, "area:") + `}`, labelsPage, false},
		{"repository null", `null`, 0, true},
	} {
		f := c.found(json.RawMessage(tt.answer))
		if len(f.names) != tt.names || f.complete != tt.complete {
			t.Errorf("%s: %d names, complete %v; want %d, %v", tt.name, len(f.names), f.complete, tt.names, tt.complete)
		}
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
