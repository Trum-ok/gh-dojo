package main

import (
	"context"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// Board items fixtures, owned by internal/board: the gh-dojo board as
// recorded (14 open issues, one page) and a hand-crafted board of two
// pages with every kind of item list handles.
const (
	itemsFixture         = "../../internal/board/testdata/graphql/items_dojo.json"
	itemsTwoPagesFixture = "../../internal/board/testdata/graphql/items_two_pages.json"
)

func TestList(t *testing.T) {
	tests := []struct {
		name     string
		fixtures []string
		args     []string
		code     render.Code
		requests int
	}{
		// The recorded board: schema (cold cache) + one page of items.
		{"recorded", []string{itemsFixture}, nil, 0, 2},
		{"recorded_status", []string{itemsFixture}, []string{"--status", "In progress", "--status", "Backlog"}, 0, 2},
		{"recorded_limit", []string{itemsFixture}, []string{"--limit", "8"}, 0, 2},
		// The hand-crafted board: schema + two pages of items.
		{"default", []string{itemsTwoPagesFixture}, nil, 0, 3},
		{"all", []string{itemsTwoPagesFixture}, []string{"--all"}, 0, 3},
		{"mine_label", []string{itemsTwoPagesFixture}, []string{"--mine", "--label", "BUG"}, 0, 3},
		{"labels_and", []string{itemsTwoPagesFixture}, []string{"--label", "bug", "--label", "area:cli"}, 0, 3},
		{"final_status", []string{itemsTwoPagesFixture}, []string{"--status", "Done"}, 0, 3},
		{"limit", []string{itemsTwoPagesFixture}, []string{"--limit", "2"}, 0, 3},
		{"nothing", []string{itemsTwoPagesFixture}, []string{"--status", "Ready"}, 0, 3},
		// Bad flags fail before the items are read.
		{"unknown_status", nil, []string{"--status", "in progres"}, render.ExitUsage, 1},
		{"unknown_status_far", nil, []string{"--status", "Blocked"}, render.ExitUsage, 1},
		{"bad_limit", nil, []string{"--limit", "0"}, render.ExitUsage, 0},
		{"args", nil, []string{"42"}, render.ExitUsage, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			var srv *graphtest.Server
			if tt.requests > 0 { // else newApp panics: nothing may reach GitHub
				srv = withApp(t, append([]string{boardFixture}, tt.fixtures...)...)
			}
			code, out := runOut(append([]string{"list"}, tt.args...)...)
			if code != tt.code {
				t.Errorf("code = %d, want %d\n%s", code, tt.code, out)
			}
			testutil.Golden(t, "list/"+tt.name, out)
			if srv != nil && srv.Requests() != tt.requests {
				t.Errorf("requests = %d, want %d", srv.Requests(), tt.requests)
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
