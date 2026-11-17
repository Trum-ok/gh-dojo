package main

import (
	"context"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// Board items fixtures. Owned by internal/board: the gh-dojo board as
// recorded (14 open issues, one page) and a hand-crafted board of two pages
// with every kind of item list handles. Hand-crafted here: scenarios only
// list cares about.
const (
	itemsFixture         = "../../internal/board/testdata/graphql/items_dojo.json"
	itemsTwoPagesFixture = "../../internal/board/testdata/graphql/items_two_pages.json"
	listLabelsFixture    = "testdata/graphql/list_labels.json"
	listChangedFixture   = "testdata/graphql/list_board_changed.json"
	listRenamedFixture   = "testdata/graphql/list_field_renamed.json"
)

func TestList(t *testing.T) {
	tests := []struct {
		name     string
		fixtures []string // nil: nothing may reach GitHub
		warm     bool     // read the schema into the cache first (not counted)
		args     []string
		code     render.Code
		requests int
	}{
		// The recorded board: schema (cold cache) + one page of items.
		{name: "recorded", fixtures: []string{boardFixture, itemsFixture}, requests: 2},
		{name: "recorded_status", fixtures: []string{boardFixture, itemsFixture},
			args: []string{"--status", "In progress", "--status", "Backlog"}, requests: 2},
		{name: "recorded_limit", fixtures: []string{boardFixture, itemsFixture}, args: []string{"--limit", "8"}, requests: 2},
		// Ready is resolved: one line of numbers, cut by --limit like any group,
		// and listed with titles when --status names it.
		{name: "recorded_limit_folded", fixtures: []string{boardFixture, itemsFixture}, args: []string{"--limit", "4"}, requests: 2},
		{name: "recorded_ready", fixtures: []string{boardFixture, itemsFixture}, args: []string{"--status", "Ready"}, requests: 2},
		// The hand-crafted board: schema + two pages of items.
		{name: "default", fixtures: []string{boardFixture, itemsTwoPagesFixture}, requests: 3},
		{name: "all", fixtures: []string{boardFixture, itemsTwoPagesFixture}, args: []string{"--all"}, requests: 3},
		{name: "mine_label", fixtures: []string{boardFixture, itemsTwoPagesFixture},
			args: []string{"--mine", "--label", "BUG"}, requests: 3},
		{name: "labels_and", fixtures: []string{boardFixture, itemsTwoPagesFixture},
			args: []string{"--label", "bug", "--label", "area:cli"}, requests: 3},
		{name: "final_status", fixtures: []string{boardFixture, itemsTwoPagesFixture},
			args: []string{"--status", "Done"}, requests: 3},
		// With --status, final statuses are filtered out: only "closed" is hidden.
		{name: "status_hidden_closed", fixtures: []string{boardFixture, itemsTwoPagesFixture},
			args: []string{"--status", "In progress"}, requests: 3},
		{name: "status_none", fixtures: []string{boardFixture, itemsTwoPagesFixture},
			args: []string{"--status", "none", "--status", "Waiting"}, requests: 3},
		{name: "limit", fixtures: []string{boardFixture, itemsTwoPagesFixture}, args: []string{"--limit", "2"}, requests: 3},
		{name: "nothing", fixtures: []string{boardFixture, itemsTwoPagesFixture}, args: []string{"--status", "Ready"}, requests: 3},
		// #27 has 102 labels, 100 read: it may have the label, so it is shown
		// and counted as not fully checked; no typo error either.
		{name: "label_window", fixtures: []string{boardFixture, itemsTwoPagesFixture},
			args: []string{"--label", "l102"}, requests: 3},
		// A cached schema without the status "Icebox" an item has: re-read once.
		{name: "stale_item_status", fixtures: []string{boardFixture, itemsTwoPagesFixture}, warm: true, requests: 3},
		// The status field renamed since the schema was cached: the re-read
		// schema fails the config check (#6).
		{name: "field_renamed", fixtures: []string{listRenamedFixture}, warm: true, code: render.ExitUsage, requests: 2},
		{name: "board_changed", fixtures: []string{boardFixture, listChangedFixture}, requests: 3},
		// --label checks against every issue on the board, closed ones too.
		{name: "label_typo", fixtures: []string{boardFixture, listLabelsFixture},
			args: []string{"--label", "bgu"}, code: render.ExitUsage, requests: 2},
		{name: "label_closed_only", fixtures: []string{boardFixture, listLabelsFixture},
			args: []string{"--label", "wontfix"}, requests: 2},
		// Bad flags fail before the items are read.
		{name: "unknown_status", fixtures: []string{boardFixture}, args: []string{"--status", "in progres"}, code: render.ExitUsage, requests: 1},
		{name: "unknown_status_far", fixtures: []string{boardFixture}, args: []string{"--status", "Blocked"}, code: render.ExitUsage, requests: 1},
		{name: "bad_limit", args: []string{"--limit", "0"}, code: render.ExitUsage},
		{name: "args", args: []string{"42"}, code: render.ExitUsage},
		{name: "args_word", args: []string{"bugs"}, code: render.ExitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			everyItem(t)
			var srv *graphtest.Server
			if tt.fixtures != nil {
				srv = withApp(t, tt.fixtures...)
			}
			before := 0
			if tt.warm {
				if _, err := newApp().Schema(context.Background()); err != nil {
					t.Fatal(err)
				}
				before = srv.Requests()
			}
			code, out := runOut(append([]string{"list"}, tt.args...)...)
			if code != tt.code {
				t.Errorf("code = %d, want %d\n%s", code, tt.code, out)
			}
			testutil.Golden(t, "list/"+tt.name, out)
			if srv != nil && srv.Requests()-before != tt.requests {
				t.Errorf("requests = %d, want %d", srv.Requests()-before, tt.requests)
			}
		})
	}
}

// TestListStaleStatus re-reads a cached schema once before rejecting a
// status: the option may have been added since it was cached.
func TestListStaleStatus(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture)
	if _, err := newApp().Schema(context.Background()); err != nil { // warms the cache
		t.Fatal(err)
	}
	code, out := runOut("list", "--status", "Blocked")
	if code != render.ExitUsage {
		t.Errorf("code = %d, want %d\n%s", code, render.ExitUsage, out)
	}
	if n := srv.Requests(); n != 2 {
		t.Errorf("requests = %d, want 2: warm-up and one re-read", n)
	}
}

func TestListHelp(t *testing.T) {
	withConfig(t, dojoConfig)
	code, out := runOut("list", "--help")
	if code != 0 {
		t.Errorf("code = %d", code)
	}
	testutil.Golden(t, "list/help", out)
}
