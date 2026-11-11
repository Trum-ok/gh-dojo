package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
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

// TestDoctorSprint calls the sprint check directly: the config does not
// accept fields.sprint yet.
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
	checkSprint(r, cfg, s, testNow) // no fields.sprint: no line
	for _, tt := range []struct {
		field string
		now   time.Time
	}{
		{"Sprint", testNow},
		{"Sprint", time.Date(2026, 12, 20, 9, 0, 0, 0, time.UTC)}, // after the last iteration
		{"Status", testNow},
		{"Nope", testNow}, // board.Problems reports a missing field
	} {
		cfg.Fields["sprint"] = tt.field
		checkSprint(r, cfg, s, tt.now)
	}
	var b strings.Builder
	r.write(&b)
	testutil.Golden(t, "doctor/sprint", []byte(b.String()))
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
