package main

import (
	"testing"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// Board items with blockers. Owned by internal/board: the gh-dojo board as
// recorded (#34 in Backlog with resolved blockers, #41 and #42 blocked by
// issues in progress). Hand-crafted here: next's scenarios (see their
// comments).
const (
	nextRecordedFixture = "../../internal/board/testdata/graphql/items_dojo_blockers.json"
	nextItemsFixture    = "testdata/graphql/next_items.json"
)

func TestNext(t *testing.T) {
	tests := []struct {
		name     string
		fixtures []string // nil: nothing may reach GitHub
		config   string   // dojoConfig when ""
		args     []string
		code     render.Code
		requests int
	}{
		// The recorded board: schema (cold cache) + one page of items, as list.
		{name: "recorded", fixtures: []string{boardFixture, nextRecordedFixture}, requests: 2},
		{name: "recorded_all", fixtures: []string{boardFixture, nextRecordedFixture}, args: []string{"--all"}, requests: 2},
		// The only documentation issue is blocked.
		{name: "recorded_blocked", fixtures: []string{boardFixture, nextRecordedFixture},
			args: []string{"--label", "documentation"}, requests: 2},
		// No open issue in the initial status (here Waiting) or without one.
		{name: "recorded_none", fixtures: []string{boardFixture, nextRecordedFixture},
			config: dojoConfig + "[statuses]\ninitial = \"Waiting\"\n", requests: 2},
		// The hand-crafted board: 6 issues to take, 4 blocked, one withheld
		// blocker (a warning), a draft, a pull request and an unseen item.
		{name: "default", fixtures: []string{boardFixture, nextItemsFixture}, requests: 2},
		{name: "all", fixtures: []string{boardFixture, nextItemsFixture}, args: []string{"--all", "--limit", "20"}, requests: 2},
		{name: "limit", fixtures: []string{boardFixture, nextItemsFixture}, args: []string{"--limit", "1"}, requests: 2},
		{name: "label", fixtures: []string{boardFixture, nextItemsFixture}, args: []string{"--label", "documentation"}, requests: 2},
		{name: "mine", fixtures: []string{boardFixture, nextItemsFixture}, args: []string{"--mine"}, requests: 2},
		// Nothing to take: every issue left is blocked or filtered out.
		{name: "nothing", fixtures: []string{boardFixture, nextItemsFixture}, args: []string{"--label", "area:cli", "--label", "bug"}, requests: 2},
		{name: "label_typo", fixtures: []string{boardFixture, nextItemsFixture}, args: []string{"--label", "documentaton"},
			code: render.ExitUsage, requests: 2},
		// Without statuses.initial: a config error before any request.
		{name: "no_initial", config: "version = 1\n[board]\nowner = \"Trum-ok\"\nproject = 5\n[fields]\nstatus = \"Status\"\n",
			code: render.ExitUsage},
		{name: "bad_limit", args: []string{"--limit", "0"}, code: render.ExitUsage},
		{name: "args", args: []string{"42"}, code: render.ExitUsage},
		{name: "args_word", args: []string{"bugs"}, code: render.ExitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.config
			if cfg == "" {
				cfg = dojoConfig
			}
			withConfig(t, cfg)
			var srv *graphtest.Server
			if tt.fixtures != nil {
				srv = withApp(t, tt.fixtures...)
			}
			code, out := runOut(append([]string{"next"}, tt.args...)...)
			if code != tt.code {
				t.Errorf("code = %d, want %d\n%s", code, tt.code, out)
			}
			testutil.Golden(t, "next/"+tt.name, out)
			if srv != nil && srv.Requests() != tt.requests {
				t.Errorf("requests = %d, want %d", srv.Requests(), tt.requests)
			}
		})
	}
}

// TestNextSprint: --sprint current keeps the issues in today's Sprint 12;
// without statuses.resolved only a closed blocker is resolved, so #47,
// blocked by #42 in Ready, is hidden. One request with a warm schema.
func TestNextSprint(t *testing.T) {
	srv, before := withSprintApp(t, time.Time{}, verbFixture("next_sprint_items"))
	code, out := runOut("next", "--sprint", "current")
	if code != render.ExitOK {
		t.Errorf("exit code = %d, want %d\n%s", code, render.ExitOK, out)
	}
	if n := srv.Requests() - before; n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
	testutil.Golden(t, "next/sprint_current", out)
}

func TestNextHelp(t *testing.T) {
	withConfig(t, dojoConfig)
	code, out := runOut("next", "--help")
	if code != 0 {
		t.Errorf("code = %d", code)
	}
	testutil.Golden(t, "next/help", out)
}

// TestNextJSON: next --json cut by --limit, with the counts of what is
// left out and the warning; --all with each issue's unresolved blockers.
func TestNextJSON(t *testing.T) {
	for _, tt := range []struct {
		name  string
		items string
		args  []string
	}{
		{"default", nextItemsFixture, nil},
		{"all", nextRecordedFixture, []string{"--all"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			withApp(t, boardFixture, tt.items)
			code, out := runJSON(t, append([]string{"next", "--json"}, tt.args...)...)
			if code != render.ExitOK {
				t.Errorf("exit code = %d, want 0\n%s", code, out)
			}
			testutil.Golden(t, "json/next_"+tt.name, out)
		})
	}
}
