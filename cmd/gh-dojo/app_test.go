package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// Recorded fixtures of the gh-dojo board and its issue #3, shared with the
// packages that own the queries.
const (
	boardFixture = "../../internal/board/testdata/graphql/user_board.json"
	card3Fixture = "../../internal/issue/testdata/graphql/card_3.json"
)

// dojoConfig points at the recorded gh-dojo board (user Trum-ok, project 5).
const dojoConfig = `
version = 1
preset = "kanban"

[board]
owner = "Trum-ok"
project = 5
repos = ["Trum-ok/gh-dojo"]
`

// testNow is the fixed clock of withApp.
var testNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// withApp makes newApp return Apps that replay the fixtures (paths relative
// to cmd/gh-dojo), share a private schema cache for the whole test and run
// on the fixed clock testNow. The current directory has no git remote. Set
// the config with withConfig. The returned server counts requests:
//
//	withConfig(t, dojoConfig)
//	srv := withApp(t, boardFixture, card3Fixture)
//	code, out := runOut("show", "3")
//	testutil.Golden(t, "show/card_3", out)
//	if srv.Requests() != 2 { ... } // schema (cold cache) + card
func withApp(t *testing.T, fixtures ...string) *graphtest.Server {
	t.Helper()
	srv := graphtest.New(t, fixtures...)
	dir := t.TempDir()
	saved := newApp
	t.Cleanup(func() { newApp = saved })
	newApp = func() *App {
		l := board.NewLoader(srv.Client())
		l.Dir, l.Host, l.Now = dir, "github.com", func() time.Time { return testNow }
		return &App{
			Client:      srv.Client(),
			Loader:      l,
			CurrentRepo: func() (string, error) { return "", errors.New("no git remote in tests") },
		}
	}
	return srv
}

// noApp is newApp outside withApp (TestMain installs it): commands must
// not reach GitHub in tests. It records the call, so the test run fails
// (strayAppFailures) even when the command's internal error is what a
// golden file expects, and panics to stop the command.
func noApp() *App {
	strayApps.Lock()
	strayApps.stacks = append(strayApps.stacks, string(debug.Stack()))
	strayApps.Unlock()
	panic("this test reaches GitHub; set it up with withApp")
}

// strayApps are the stacks of noApp calls.
var strayApps struct {
	sync.Mutex
	stacks []string
}

// testFuncRE finds test functions in a stack.
var testFuncRE = regexp.MustCompile(`\.(Test[A-Za-z0-9_]*)\(`)

// strayAppFailures names the tests that called noApp, one line each, and
// forgets them. TestMain fails the run when there are any.
func strayAppFailures() []string {
	strayApps.Lock()
	defer strayApps.Unlock()
	var lines []string
	for _, stack := range strayApps.stacks {
		name := "a test"
		if m := testFuncRE.FindAllStringSubmatch(stack, -1); m != nil {
			name = m[len(m)-1][1]
		}
		lines = append(lines, name+" reached GitHub (newApp without withApp)")
	}
	strayApps.stacks = nil
	return lines
}

func TestNoAppRecorded(t *testing.T) {
	strayAppFailures() // none so far, or TestMain reports them anyway
	root := func() (*cobra.Command, error) {
		return &cobra.Command{Use: "gh-dojo", RunE: func(*cobra.Command, []string) error {
			newApp()
			return nil
		}}, nil
	}
	if code, out := runRootOut(root); code != render.ExitUsage || !strings.Contains(string(out), "internal error") {
		t.Errorf("code = %d, want an internal error:\n%s", code, out)
	}
	got := strayAppFailures()
	if len(got) != 1 || got[0] != "TestNoAppRecorded reached GitHub (newApp without withApp)" {
		t.Errorf("stray calls = %q", got)
	}
}

func TestAppCardWarmCache(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, card3Fixture)
	ctx := context.Background()
	if _, err := newApp().Schema(ctx); err != nil { // warms the cache
		t.Fatal(err)
	}
	before := srv.Requests()

	a := newApp() // a new run
	card, err := a.Card(ctx, issue.Ref{Number: 3}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests() - before; n != 1 {
		t.Errorf("card with a warm schema cache took %d requests, want 1", n)
	}
	if card.Status() != "Ready" || card.ItemID() == "" || card.Repo.String() != "Trum-ok/gh-dojo" {
		t.Errorf("status %q, item %q, repo %s", card.Status(), card.ItemID(), card.Repo)
	}
	// The schema is kept for the rest of the run.
	if _, err := a.Schema(ctx); err != nil || srv.Requests()-before != 1 {
		t.Errorf("second Schema: %v, requests %d", err, srv.Requests()-before)
	}
}

func TestAppCardColdCache(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, card3Fixture)
	if _, err := newApp().Card(context.Background(), issue.Ref{Number: 3}, 0); err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 2 {
		t.Errorf("requests = %d, want 2 (schema, card)", n)
	}
}

func TestAppBoardMismatch(t *testing.T) {
	withConfig(t, dojoConfig+"\n[verbs.start]\nto = \"In progres\"\n")
	srv := withApp(t, boardFixture)
	_, err := newApp().Card(context.Background(), issue.Ref{Number: 3}, 0)
	assertCode(t, err, render.ExitUsage, "app/board_mismatch")
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want 1: no card read after a bad config", n)
	}
}

// TestAppResetSchemaConcurrent runs Schema and ResetSchema together: a
// schema kept after both is never the dropped cache.
func TestAppResetSchemaConcurrent(t *testing.T) {
	withConfig(t, dojoConfig)
	withApp(t, boardFixture)
	ctx := context.Background()
	for range 200 {
		if _, err := newApp().Schema(ctx); err != nil { // warm the cache
			t.Fatal(err)
		}
		a := newApp()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = a.Schema(ctx) }()
		go func() { defer wg.Done(); _ = a.ResetSchema() }()
		wg.Wait()
		if a.schema != nil && a.schema.Cached {
			t.Fatal("the App kept the schema ResetSchema dropped")
		}
	}
}

func TestAppResetSchema(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture)
	ctx := context.Background()
	a := newApp()
	if _, err := a.Schema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.ResetSchema(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Schema(ctx); err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 2 {
		t.Errorf("requests = %d, want 2: reset drops the cache too", n)
	}
}

func TestAppCardResolvesRepoFirst(t *testing.T) {
	withConfig(t, "\nversion = 1\npreset = \"kanban\"\n[board]\nowner = \"Trum-ok\"\nproject = 5\n")
	var q countingClient
	a := &App{
		Client: &q, Loader: board.NewLoader(&q),
		CurrentRepo: func() (string, error) { return "", errors.New("no git remote") },
	}
	_, err := a.Card(context.Background(), issue.Ref{Number: 3}, 0)
	if render.FromError(err).Code != render.ExitUsage {
		t.Errorf("err = %v, want a config error", err)
	}
	if q.n != 0 {
		t.Errorf("requests = %d, want 0: the repository is resolved before the schema", q.n)
	}
}

// countingClient counts requests and fails them.
type countingClient struct{ n int }

func (c *countingClient) Query(context.Context, string, map[string]any, any) error {
	c.n++
	return errors.New("no network in tests")
}

func (c *countingClient) Mutate(context.Context, string, map[string]any, any) error {
	c.n++
	return errors.New("no network in tests")
}

func TestAppCardStatusByFieldID(t *testing.T) {
	// The cached schema still names the field Status; GitHub has renamed it
	// to Stage since. The card's status is matched by the field's ID.
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, renamedStatusFixture(t))
	card, err := newApp().Card(context.Background(), issue.Ref{Number: 3}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if card.Status() != "Ready" || card.StatusUnknown() {
		t.Errorf("status %q, unknown %v; want Ready", card.Status(), card.StatusUnknown())
	}
	if n := srv.Requests(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

// renamedStatusFixture writes card3Fixture as GitHub returns it after the
// Status field is renamed to Stage, and returns its path.
func renamedStatusFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(card3Fixture)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	f["handcrafted"] = true
	resp := f["interactions"].([]any)[0].(map[string]any)["response"].(map[string]any)
	is := resp["data"].(map[string]any)["repository"].(map[string]any)["issueOrPullRequest"].(map[string]any)
	item := is["projectItems"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
	item["status"] = nil // nothing is named Status any more
	for _, v := range item["fieldValues"].(map[string]any)["nodes"].([]any) {
		if field, ok := v.(map[string]any)["field"].(map[string]any); ok && field["name"] == "Status" {
			field["name"] = "Stage"
		}
	}
	if data, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "card_3_renamed.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAppRepo(t *testing.T) {
	noRepos := "\nversion = 1\npreset = \"kanban\"\n[board]\nowner = \"Trum-ok\"\nproject = 5\n"
	withConfig(t, noRepos)
	a := &App{CurrentRepo: func() (string, error) { return "", errors.New("no git remote") }}
	_, err := a.Repo()
	assertCode(t, err, render.ExitUsage, "app/no_repo")

	a.CurrentRepo = func() (string, error) { return "acme/shop", nil }
	if r, err := a.Repo(); err != nil || r.String() != "acme/shop" {
		t.Errorf("Repo from the git remote = %v, %v", r, err)
	}

	withConfig(t, dojoConfig)
	if r, err := (&App{}).Repo(); err != nil || r.String() != "Trum-ok/gh-dojo" {
		t.Errorf("Repo from board.repos = %v, %v", r, err)
	}
}

// TestAppRepoGHRepo runs the real CurrentRepo: GH_REPO is the current
// repository, but board.repos wins over it, and a malformed one is
// reported as such, not as a missing remote.
func TestAppRepoGHRepo(t *testing.T) {
	a := &App{CurrentRepo: gitRepo}
	t.Setenv("GH_REPO", "acme/shop")
	withConfig(t, dojoConfig)
	if r, err := a.Repo(); err != nil || r.String() != "Trum-ok/gh-dojo" {
		t.Errorf("board.repos with GH_REPO set = %v, %v", r, err)
	}

	withConfig(t, "\nversion = 1\npreset = \"kanban\"\n[board]\nowner = \"Trum-ok\"\nproject = 5\n")
	if r, err := a.Repo(); err != nil || r.String() != "acme/shop" {
		t.Errorf("GH_REPO = %v, %v", r, err)
	}

	t.Setenv("GH_REPO", "acme")
	_, err := a.Repo()
	assertCode(t, err, render.ExitUsage, "app/bad_gh_repo")
}

func TestAppMissingConfig(t *testing.T) {
	// TestMain's config dir has no dojo.toml; an App without a client shows
	// nothing is sent.
	_, err := (&App{}).Card(context.Background(), issue.Ref{Number: 3}, 0)
	if render.FromError(err).Code != render.ExitUsage {
		t.Errorf("err = %v, want a config error", err)
	}
}

func TestWorkflowIssue(t *testing.T) {
	card := &issue.Card{
		ID: "I_1", Number: 42, Title: "Fix login", State: issue.StateClosed,
		Item: &issue.Item{ID: "PVTI_1", Status: "Ready"},
	}
	card.Comments.Nodes = []issue.Comment{{Body: "a"}, {Body: "b", ViewerDidAuthor: true}}
	is := workflowIssue(card, issue.Repo{})
	if is.Number != 42 || is.Title != "Fix login" || !is.Closed || !is.OnBoard || is.Status != "Ready" || is.Ref != card {
		t.Errorf("issue = %+v", is)
	}
	if len(is.Comments) != 2 || is.Comments[0].Body != "a" || !is.Comments[1].ViewerDidAuthor {
		t.Errorf("comments = %+v", is.Comments)
	}
	if is.Archived {
		t.Errorf("archived: %+v", is)
	}
	card.Item.Archived = true
	if is := workflowIssue(card, issue.Repo{}); !is.Archived {
		t.Errorf("archived item: %+v", is)
	}
	if is := workflowIssue(&issue.Card{Number: 1, State: issue.StateOpen}, issue.Repo{}); is.OnBoard || is.Status != "" || is.Closed {
		t.Errorf("not on board: %+v", is)
	}
}

// TestStatusAutomated: the status is the board's workflows' only when the
// last change read, since the item was added, is into it and automated.
func TestStatusAutomated(t *testing.T) {
	added := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return time.Date(2026, 11, d, 12, 0, 0, 0, time.UTC) }
	byPerson := issue.StatusChange{At: day(3), From: "Backlog", To: "In progress", Actor: "alice"}
	closed := issue.StatusChange{At: day(5), From: "In progress", To: "Done", Actor: "github-project-automation", Automated: true}
	tests := []struct {
		name    string
		item    *issue.Item
		history []issue.StatusChange
		want    bool
	}{
		{"closed by automation", &issue.Item{Status: "Done", CreatedAt: added}, []issue.StatusChange{byPerson, closed}, true},
		{"moved by a person", &issue.Item{Status: "Done", CreatedAt: added},
			[]issue.StatusChange{closed, {At: day(6), From: "Done", To: "Done", Actor: "alice"}}, false},
		{"history behind the status", &issue.Item{Status: "Ready", CreatedAt: added}, []issue.StatusChange{closed}, false},
		{"no history", &issue.Item{Status: "Done", CreatedAt: added}, nil, false},
		{"an earlier item's", &issue.Item{Status: "Done", CreatedAt: day(10)}, []issue.StatusChange{closed}, false},
		{"not on the board", nil, []issue.StatusChange{closed}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card := &issue.Card{Number: 1, State: issue.StateClosed, Item: tt.item, History: tt.history}
			if got := workflowIssue(card, issue.Repo{}).StatusAutomated; got != tt.want {
				t.Errorf("StatusAutomated = %v, want %v", got, tt.want)
			}
		})
	}
}

func assertCode(t *testing.T, err error, code render.Code, golden string) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	e := render.FromError(err)
	if e.Code != code {
		t.Errorf("code = %d, want %d (%v)", e.Code, code, err)
	}
	testutil.Golden(t, golden, []byte(e.Text()+"\n"))
}
