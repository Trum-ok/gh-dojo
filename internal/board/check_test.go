package board_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// dojoBoard is a config on the kanban preset pointing at the recorded
// gh-dojo board (statuses Backlog, Ready, In progress, In review, Waiting,
// Done).
const dojoBoard = `version = 1
preset = "kanban"

[board]
owner = "Trum-ok"
project = 5
repos = ["Trum-ok/gh-dojo"]
`

func parseConfig(t *testing.T, toml string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(toml), config.FileName)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCheckOK(t *testing.T) {
	l, srv, _ := newLoader(t, fixture("user_board"))
	c := parseConfig(t, dojoBoard)
	s, err := l.LoadChecked(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID == "" || srv.Requests() != 1 {
		t.Errorf("schema %q, requests %d", s.ID, srv.Requests())
	}
	// Warm cache: no request at all.
	if _, err := l.LoadChecked(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests with a warm cache = %d, want 1 in total", n)
	}
}

func TestCheckErrors(t *testing.T) {
	tests := []struct{ name, toml string }{
		{"status_typo", "[verbs.start]\nto = \"In progres\"\n"},
		{"new_verb_status", "[verbs.park]\nwhen = \"x\"\nto = \"Parked\"\n"},
		{"status_field_case", "[fields]\nstatus = \"status\"\n"},
		{"status_field_type", "[fields]\nstatus = \"Estimate\"\n"},
		{"unknown_field", "[fields]\nstatus = \"Stage\"\n"},
		{"several", "[statuses]\nfinal = [\"Shipped\"]\n[verbs.start]\nto = \"In progres\"\n[verbs.wait]\nto = \"Parked\"\n"},
		{"two", "[verbs.start]\nto = \"In progres\"\nfrom = [\"Todo\"]\n"},
		{"resolved_typo", "[statuses]\nresolved = [\"Redy\", \"Done\"]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := loadSchema(t)
			err := board.Check(parseConfig(t, dojoBoard+tt.toml), s)
			assertError(t, err, render.ExitUsage, "check/"+tt.name)
			if !board.IsStale(err) {
				t.Errorf("IsStale(%v) = false; Resolve would not retry", err)
			}
		})
	}
}

// TestCheckPresetKey shows where a preset value is fixed: in the repo's
// file, overriding the preset.
func TestCheckPresetKey(t *testing.T) {
	s := loadSchema(t)
	for i := range s.Fields {
		if s.Fields[i].Name == "Status" {
			s.Fields[i].Options = s.Fields[i].Options[:3] // Backlog, Ready, In progress
		}
	}
	err := board.Check(parseConfig(t, dojoBoard), s)
	assertError(t, err, render.ExitUsage, "check/preset_status_missing")
}

func TestCheckStaleCacheRereads(t *testing.T) {
	l, srv, _ := newLoader(t, fixture("stale_then_fresh"))
	ctx := context.Background()
	if _, err := l.Load(ctx, userBoard); err != nil { // caches a board without Waiting
		t.Fatal(err)
	}
	s, err := l.LoadChecked(ctx, parseConfig(t, dojoBoard))
	if err != nil {
		t.Fatalf("LoadChecked after a re-read: %v", err)
	}
	if s.Cached || srv.Requests() != 2 {
		t.Errorf("cached %v, requests %d; want a fresh read (2 requests)", s.Cached, srv.Requests())
	}
	// The fresh schema was cached: the next check sends nothing.
	if _, err := l.LoadChecked(ctx, parseConfig(t, dojoBoard)); err != nil || srv.Requests() != 2 {
		t.Errorf("third check: %v, requests %d", err, srv.Requests())
	}
}

func TestCheckRealMismatchRereadsOnce(t *testing.T) {
	l, srv, _ := newLoader(t, fixture("user_board"))
	ctx := context.Background()
	if _, err := l.Load(ctx, userBoard); err != nil {
		t.Fatal(err)
	}
	_, err := l.LoadChecked(ctx, parseConfig(t, dojoBoard+"[verbs.start]\nto = \"In progres\"\n"))
	if !errors.Is(err, board.ErrUnknownOption) {
		t.Fatalf("err = %v, want unknown option", err)
	}
	if n := srv.Requests(); n != 2 {
		t.Errorf("requests = %d, want 2 (warm-up, one re-read)", n)
	}
	// A cold cache fails without a second read.
	l2, srv2, _ := newLoader(t, fixture("user_board"))
	if _, err := l2.LoadChecked(ctx, parseConfig(t, dojoBoard+"[verbs.start]\nto = \"In progres\"\n")); err == nil {
		t.Fatal("no error")
	}
	if n := srv2.Requests(); n != 1 {
		t.Errorf("cold cache requests = %d, want 1", n)
	}
}

func TestRefOf(t *testing.T) {
	if got := board.RefOf(parseConfig(t, dojoBoard)); got != userBoard {
		t.Errorf("RefOf = %v", got)
	}
}

func loadSchema(t *testing.T) *board.Schema {
	t.Helper()
	l, _, _ := newLoader(t, fixture("user_board"))
	s, err := l.Load(context.Background(), userBoard)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
