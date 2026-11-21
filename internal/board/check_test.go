package board_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
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
		{"sprint_field_type", "[fields]\nsprint = \"Priority\"\n"},
		{"sprint_field_missing", "[fields]\nsprint = \"Sprint\"\n"},
		{"iteration_status", "[fields]\nsprint = \"Sprint\"\n[verbs.done]\niteration = \"by_status_date:Redy\"\n"},
		{"field_unknown", "[verbs.done.field]\nPriorty = \"P1\"\n"},
		{"field_option", "[verbs.done.field]\nPriority = \"p3\"\n"},
		{"field_option_case", "[verbs.done.field]\nPriority = \"p1\"\n"},
		{"field_number", "[verbs.done.field]\nEstimate = \"3\"\n"},
		{"field_date", "[verbs.done.field]\n\"Target date\" = \"tomorrow\"\n"},
		{"field_builtin", "[verbs.done.field]\nLabels = \"bug\"\n"},
		{"field_preset_verb", "[verbs.start.field]\nSize = \"XXL\"\n"},
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

// TestCheckStatusCase: statuses match the board's options regardless of
// case, so the kanban preset's "In progress" fits a board with GitHub's
// "In Progress", and Option returns the board's spelling.
func TestCheckStatusCase(t *testing.T) {
	s := loadSchema(t)
	status := statusField(t, s)
	for i := range status.Options {
		status.Options[i].Name = strings.ToUpper(status.Options[i].Name)
	}
	c := parseConfig(t, dojoBoard+"[statuses]\nresolved = [\"ready\", \"Done\"]\n")
	if err := board.Check(c, s); err != nil {
		t.Fatal(err)
	}
	o, err := status.Option("In progress")
	if err != nil || o.Name != "IN PROGRESS" {
		t.Errorf("Option(In progress) = %v, %v; want IN PROGRESS", o, err)
	}
	if got, want := status.Spell([]string{"ready", "Shipped"}), []string{"READY", "Shipped"}; !slices.Equal(got, want) {
		t.Errorf("Spell = %q, want %q", got, want)
	}
}

// TestCheckStatusAmbiguous: a status two options match, differing only in
// case, is an error the board fixes, even when one is spelled exactly so.
func TestCheckStatusAmbiguous(t *testing.T) {
	s := loadSchema(t)
	status := statusField(t, s)
	status.Options = append(status.Options, board.Option{ID: "x", Name: "done"})
	err := board.Check(parseConfig(t, dojoBoard), s)
	assertError(t, err, render.ExitUsage, "check/status_ambiguous")
	if !board.IsStale(err) {
		t.Errorf("IsStale(%v) = false; Resolve would not re-read a board just fixed", err)
	}
}

func statusField(t *testing.T, s *board.Schema) *board.Field {
	t.Helper()
	for i := range s.Fields {
		if s.Fields[i].Name == "Status" {
			return &s.Fields[i]
		}
	}
	t.Fatal("no Status field")
	return nil
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

// TestCheckFieldEffects: every kind of field the field effect sets, and a
// sprint field that is an iteration field, match the board.
func TestCheckFieldEffects(t *testing.T) {
	s := loadSchema(t)
	c := parseConfig(t, dojoBoard+`[verbs.triage]
when = "Triaged"
[verbs.triage.field]
Priority = "P1"
Estimate = 2.5
"Start date" = 2026-11-12
"Target date" = "2026-11-30"
`)
	if err := board.Check(c, s); err != nil {
		t.Fatal(err)
	}
	v, _ := c.Verb("triage")
	var got []string
	for _, fv := range v.Fields {
		f, input, err := s.FieldInput(fv)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %v", f.Name, input))
	}
	want := []string{
		"Estimate map[number:2.5]", "Priority map[singleSelectOptionId:0a877460]",
		"Start date map[date:2026-11-12]", "Target date map[date:2026-11-30]",
	}
	if !slices.Equal(got, want) {
		t.Errorf("inputs = %q, want %q", got, want)
	}

	l, _, _ := newLoader(t, fixture("iteration_board"))
	it, err := l.Load(context.Background(), board.Ref{Owner: "acme", Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	c = parseConfig(t, `version = 1
[board]
owner = "acme"
project = 3
[fields]
status = "Status"
sprint = "Sprint"
[verbs.done]
when = "Released"
to = "Done"
iteration = "by_status_date:Ready"
[verbs.done.field]
Notes = "released"
`)
	if err := board.Check(c, it); err != nil {
		t.Fatal(err)
	}
}

// sprintsBoard is a config on the sprints preset pointing at the
// hand-crafted acme/3 board with a Sprint iteration field.
const sprintsBoard = `version = 1
preset = "sprints"

[board]
owner = "acme"
project = 3
repos = ["acme/shop"]
`

// TestCheckSprintsPreset: the sprints preset with only [board] matches a
// board with its statuses and an iteration field named Sprint, and
// overrides on top of it are checked too. The gh-dojo board has the
// statuses but no Sprint field.
func TestCheckSprintsPreset(t *testing.T) {
	l, srv, _ := newLoader(t, fixture("iteration_board"))
	it, err := l.LoadChecked(context.Background(), parseConfig(t, sprintsBoard))
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
	if err := board.Check(parseConfig(t, sprintsBoard+`[verbs.done]
from = ["Ready", "Waiting"]
[verbs.triage]
when = "Triaged"
to = "Todo"
[verbs.triage.field]
Priority = "P1"
`), it); err != nil {
		t.Errorf("overrides: %v", err)
	}
	tests := []struct {
		name   string
		toml   string
		schema *board.Schema
	}{
		{"sprints_no_sprint_field", "", loadSchema(t)},
		{"sprints_override_status", "[verbs.ready]\nto = \"Verified\"\n", it},
		{"sprints_sprint_field_type", "[fields]\nsprint = \"Priority\"\n", it},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toml := strings.Replace(sprintsBoard, "owner = \"acme\"\nproject = 3", "owner = \""+tt.schema.Owner+"\"\nproject = "+strconv.Itoa(tt.schema.Number), 1)
			err := board.Check(parseConfig(t, toml+tt.toml), tt.schema)
			assertError(t, err, render.ExitUsage, "check/"+tt.name)
		})
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
