package main

import (
	"context"
	"errors"
	"testing"
	"time"

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

// noApp is newApp outside withApp: commands must not reach GitHub in tests.
func noApp() *App { panic("this test reaches GitHub; set it up with withApp") }

func TestAppCardWarmCache(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, card3Fixture)
	ctx := context.Background()
	if _, err := newApp().Schema(ctx); err != nil { // warms the cache
		t.Fatal(err)
	}
	before := srv.Requests()

	a := newApp() // a new run
	card, err := a.Card(ctx, 3, 0)
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
	if _, err := newApp().Card(context.Background(), 3, 0); err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 2 {
		t.Errorf("requests = %d, want 2 (schema, card)", n)
	}
}

func TestAppBoardMismatch(t *testing.T) {
	withConfig(t, dojoConfig+"\n[verbs.start]\nto = \"In progres\"\n")
	srv := withApp(t, boardFixture)
	_, err := newApp().Card(context.Background(), 3, 0)
	assertCode(t, err, render.ExitUsage, "app/board_mismatch")
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want 1: no card read after a bad config", n)
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

func TestAppMissingConfig(t *testing.T) {
	// TestMain's config dir has no dojo.toml; an App without a client shows
	// nothing is sent.
	_, err := (&App{}).Card(context.Background(), 3, 0)
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
	is := workflowIssue(card)
	if is.Number != 42 || is.Title != "Fix login" || !is.Closed || !is.OnBoard || is.Status != "Ready" || is.Ref != card {
		t.Errorf("issue = %+v", is)
	}
	if len(is.Comments) != 2 || is.Comments[0].Body != "a" || !is.Comments[1].ViewerDidAuthor {
		t.Errorf("comments = %+v", is.Comments)
	}
	if is := workflowIssue(&issue.Card{Number: 1, State: issue.StateOpen}); is.OnBoard || is.Status != "" || is.Closed {
		t.Errorf("not on board: %+v", is)
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
