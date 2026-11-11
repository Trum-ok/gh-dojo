package workflow

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

const testConfig = `
version = 1
preset = "kanban"

[board]
owner = "acme"
project = 3

[session]
env = ["DOJO_TEST_SESSION", "DOJO_TEST_SESSION_2"]
link = "claude://s/{id}"

[verbs.note]
when = "Leave a note"
require = ["comment"]
comment = "{comment}"

[verbs.ping]
when = "Say something, maybe"
comment = "{comment}"

[verbs.park]
when = "Put the issue back"
to = "Backlog"
comment = "Parked."

[verbs.pair]
when = "Start together with someone"
to = "In progress"
from = ["Backlog"]
require = ["comment", "by"]
comment = "Pairing with {by}: {comment}"
`

func testEngine(t *testing.T) (*Engine, *config.Config) {
	t.Helper()
	c, err := config.Parse([]byte(testConfig), ".github/dojo.toml")
	if err != nil {
		t.Fatal(err)
	}
	e := New(c)
	e.Getenv = env(map[string]string{"DOJO_TEST_SESSION": "abc"})
	return e, c
}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// show renders a plan or an error the way the golden files hold it.
func show(p *Plan, err error) string {
	var b strings.Builder
	if err != nil {
		e := render.FromError(err)
		fmt.Fprintf(&b, "code: %d\n%s\n", e.Code, e.Text())
		return b.String()
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	fmt.Fprintf(&b, "%s\nconfirm: %s\n", p.DryRun(), p.Confirm())
	for _, op := range p.Ops {
		if c, ok := op.(AddComment); ok {
			fmt.Fprintf(&b, "post: %q\n", c.Text())
		}
	}
	return b.String()
}

func onBoard(status string) Issue {
	return Issue{Number: 42, Title: "Fix login redirect", OnBoard: true, Status: status}
}

func TestPlan(t *testing.T) {
	_, c := testEngine(t)
	started := func(body string) Comment { return Comment{Body: body + "\n\n" + Marker("start", body)} }
	sessionBody := "Started. Session: claude://s/abc"

	tests := []struct {
		name   string
		verb   string
		issue  Issue
		args   Args
		setup  func(*Engine)
		code   render.Code // expected error code; 0 means a plan
		noOps  bool        // the plan must be empty
		hasOps int         // number of ops expected when a plan is returned
	}{
		{name: "start_from_backlog", verb: "start", issue: onBoard("Backlog"), hasOps: 2},
		{name: "start_with_comment", verb: "start", issue: onBoard("Waiting"),
			args: Args{Comment: "  picking it up again  ", HasComment: true}, hasOps: 2},
		{name: "start_not_on_board", verb: "start", issue: Issue{Number: 42, Title: "Fix login redirect"}, hasOps: 3},
		{name: "start_not_on_board_stale_status", verb: "start",
			issue: Issue{Number: 42, Status: "Done"}, hasOps: 3},
		{name: "start_no_status", verb: "start", issue: onBoard(""), hasOps: 2},
		{name: "start_from_done", verb: "start", issue: onBoard("Done"), code: render.ExitDenied},
		{name: "ready_from_backlog", verb: "ready", issue: onBoard("Backlog"), code: render.ExitDenied},
		{name: "ready_no_status", verb: "ready", issue: onBoard(""), code: render.ExitDenied},
		{name: "ready_not_on_board", verb: "ready", issue: Issue{Number: 42}, code: render.ExitDenied},
		{name: "ready_no_status_no_initial", verb: "ready", issue: onBoard(""), code: render.ExitDenied,
			setup: func(e *Engine) { e.Initial = "" }},
		{name: "done_from_waiting", verb: "done", issue: onBoard("Waiting"), code: render.ExitDenied},
		{name: "start_closed", verb: "start", issue: Issue{Number: 42, Closed: true, OnBoard: true, Status: "Backlog"},
			code: render.ExitDenied},
		{name: "start_closed_in_target", verb: "start", issue: Issue{Number: 42, Closed: true, OnBoard: true, Status: "In progress"},
			code: render.ExitDenied},
		{name: "start_already_in_target", verb: "start", issue: onBoard("In progress"), hasOps: 1},
		{name: "start_repeat_comment_present", verb: "start", noOps: true, issue: Issue{
			Number: 42, OnBoard: true, Status: "In progress",
			Comments: []Comment{started(sessionBody), {Body: "unrelated"}},
		}},
		{name: "start_repeat_after_partial", verb: "start", hasOps: 1, issue: Issue{
			Number: 42, OnBoard: true, Status: "Backlog",
			Comments: []Comment{started(sessionBody)},
		}},
		{name: "start_comment_changed", verb: "start", hasOps: 1, issue: Issue{
			Number: 42, OnBoard: true, Status: "In progress",
			Comments: []Comment{started("Started. Session: claude://s/old")},
		}},
		{name: "start_same_comment_not_latest", verb: "start", hasOps: 1, issue: Issue{
			Number: 42, OnBoard: true, Status: "In progress",
			Comments: []Comment{started(sessionBody), started("Started. Session: claude://s/other")},
		}},
		{name: "start_other_verb_marker_ignored", verb: "start", noOps: true, issue: Issue{
			Number: 42, OnBoard: true, Status: "In progress",
			Comments: []Comment{started(sessionBody), {Body: "x\n\n" + Marker("wait", "x")}},
		}},
		{name: "start_session_unset", verb: "start", issue: onBoard("Backlog"), hasOps: 1,
			setup: func(e *Engine) { e.Getenv = env(nil) }},
		{name: "start_session_second_var", verb: "start", issue: onBoard("Backlog"), hasOps: 2,
			setup: func(e *Engine) { e.Getenv = env(map[string]string{"DOJO_TEST_SESSION_2": " xyz "}) }},
		{name: "start_session_not_configured", verb: "start", issue: onBoard("Backlog"), hasOps: 2,
			args:  Args{Comment: "on it", HasComment: true},
			setup: func(e *Engine) { e.Session = config.Session{}; e.Getenv = env(nil) }},
		{name: "start_unused_by", verb: "start", issue: onBoard("Backlog"),
			args: Args{By: "12", HasBy: true}, code: render.ExitUsage},
		{name: "park_unused_comment", verb: "park", issue: onBoard("Waiting"),
			args: Args{HasComment: true}, code: render.ExitUsage},
		{name: "park_from_anywhere", verb: "park", issue: onBoard("Done"), hasOps: 2},
		{name: "wait_missing_one_of", verb: "wait", issue: onBoard("In progress"), code: render.ExitUsage},
		{name: "wait_blank_one_of", verb: "wait", issue: onBoard("In progress"),
			args: Args{Comment: "  ", HasComment: true}, code: render.ExitUsage},
		{name: "wait_by_number", verb: "wait", issue: onBoard("In progress"),
			args: Args{By: "12", HasBy: true}, hasOps: 2},
		{name: "wait_by_hash", verb: "wait", issue: onBoard("In progress"),
			args: Args{By: " #12 ", HasBy: true}, hasOps: 2},
		{name: "wait_by_text", verb: "wait", issue: onBoard("In progress"),
			args: Args{By: "the vendor's answer", HasBy: true, Comment: "they promised Friday", HasComment: true}, hasOps: 2},
		{name: "wait_comment_only", verb: "wait", issue: onBoard("In progress"),
			args: Args{Comment: "needs a design decision", HasComment: true}, hasOps: 2},
		{name: "note_missing_require", verb: "note", issue: onBoard("Backlog"), code: render.ExitUsage},
		{name: "note_blank_require", verb: "note", issue: onBoard("Backlog"),
			args: Args{Comment: "\n", HasComment: true}, code: render.ExitUsage},
		{name: "pair_missing_both", verb: "pair", issue: onBoard("Backlog"), code: render.ExitUsage},
		{name: "pair_missing_by", verb: "pair", issue: onBoard("Backlog"),
			args: Args{Comment: "x", HasComment: true}, code: render.ExitUsage},
		{name: "unused_checked_before_require", verb: "note", issue: onBoard("Backlog"),
			args: Args{By: "12", HasBy: true}, code: render.ExitUsage},
		{name: "args_checked_before_closed", verb: "note", issue: Issue{Number: 42, Closed: true}, code: render.ExitUsage},
		{name: "note_comment_only", verb: "note", issue: onBoard("Ready"),
			args: Args{Comment: "Line one\nline two", HasComment: true}, hasOps: 1},
		{name: "note_not_on_board", verb: "note", issue: Issue{Number: 42},
			args: Args{Comment: "hi", HasComment: true}, hasOps: 2},
		{name: "ready_empty_template", verb: "ready", issue: onBoard("In progress"), hasOps: 1},
		{name: "ready_repeat_nothing_to_do", verb: "ready", issue: onBoard("Ready"), noOps: true},
		{name: "ping_no_changes", verb: "ping", issue: onBoard("Backlog"), noOps: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, _ := testEngine(t)
			if tt.setup != nil {
				tt.setup(e)
			}
			v, ok := c.Verb(tt.verb)
			if !ok {
				t.Fatalf("no verb %q", tt.verb)
			}
			p, err := e.Plan(v, tt.issue, tt.args)
			switch {
			case tt.code != 0:
				if err == nil {
					t.Fatalf("Plan() = plan, want error code %d", tt.code)
				}
				if got := render.FromError(err).Code; got != tt.code {
					t.Errorf("code = %d, want %d", got, tt.code)
				}
			case err != nil:
				t.Fatalf("Plan() error: %v", err)
			case tt.noOps && !p.Empty():
				t.Errorf("plan has %d ops, want none", len(p.Ops))
			case !tt.noOps && len(p.Ops) != tt.hasOps:
				t.Errorf("plan has %d ops, want %d", len(p.Ops), tt.hasOps)
			}
			testutil.Golden(t, "plan/"+tt.name, []byte(show(p, err)))
		})
	}
}

func TestNormalizeBy(t *testing.T) {
	for in, want := range map[string]string{
		"12":           "#12",
		"#12":          "#12",
		" 12 ":         "#12",
		"#0":           "#0",
		"012":          "012",
		"acme/shop#12": "acme/shop#12",
		"@alice":       "@alice",
		"12 and 13":    "12 and 13",
		"":             "",
	} {
		if got := NormalizeBy(in); got != want {
			t.Errorf("NormalizeBy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMarker(t *testing.T) {
	m := Marker("start", "Started.")
	if m != Marker("start", "Started.") {
		t.Error("Marker is not stable")
	}
	if m == Marker("start", "Started!") || m == Marker("ready", "Started.") {
		t.Error("Marker does not depend on the verb and the body")
	}
	if !markerRE.MatchString(m) {
		t.Errorf("Marker %q does not match markerRE", m)
	}
	if got, ok := LastMarker([]Comment{{Body: "a " + m + " b"}}, "start"); !ok || got != m {
		t.Errorf("LastMarker = %q, %v; want %q", got, ok, m)
	}
	if _, ok := LastMarker([]Comment{{Body: m}}, "star"); ok {
		t.Error("LastMarker matched a verb by prefix")
	}
	if _, ok := LastMarker(nil, "start"); ok {
		t.Error("LastMarker found a marker in no comments")
	}
}

func TestExample(t *testing.T) {
	_, c := testEngine(t)
	for verb, want := range map[string]string{
		"start": "gh dojo start 7",
		"wait":  `gh dojo wait 7 --by "<#N or name>"`,
		"pair":  `gh dojo pair 7 --comment "<text>" --by "<#N or name>"`,
	} {
		v, _ := c.Verb(verb)
		if got := Example(v, 7); got != want {
			t.Errorf("Example(%s) = %q, want %q", verb, got, want)
		}
	}
}
