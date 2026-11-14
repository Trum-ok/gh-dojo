package workflow

import (
	"errors"
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
assignee = "alice"

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

# The spec's drop, as is: --comment is posted as the comment.
[verbs.drop]
when = "The issue is not needed or a duplicate"
close = "not_planned"
require = ["comment"]
confirm = "user"

[verbs.review]
when = "A pull request waits for review"
to = "Ready"
from = ["In progress"]
labels = ["+in-review", "-blocked"]
assign = true

[verbs.take]
when = "Take the issue yourself"
assign = "@me"

[verbs.revive]
when = "A closed issue is needed after all"
to = "Backlog"
reopen = true
comment = "Revived."

# Closes without moving: the board's "Item closed" workflow may move it.
[verbs.shelve]
when = "Not now"
from = ["Backlog"]
close = "not_planned"

[verbs.give]
when = "Hand it over"
assign = "octocat"

# Closes without moving, with from and a comment: the review example's reject.
[verbs.reject]
when = "The user decided the change is not wanted"
from = ["In progress", "Waiting"]
close = "not_planned"
require = ["comment"]
confirm = "user"

# No target and a from without the initial status.
[verbs.nudge]
when = "Remind whoever has it"
from = ["In progress", "Waiting"]
labels = ["+nudged"]

# assign = "{assignee}" and unassign.
[verbs.ask]
when = "Ask someone to review"
to = "Ready"
from = ["In progress"]
assign = "{assignee}"
comment = "Review, please, @{assignee}."

[verbs.send-back]
when = "Back to the author alone"
to = "In progress"
assign = true
unassign = "all"

[verbs.clear]
when = "Nobody works on it"
unassign = "all"

[verbs.let-go]
when = "Let the issue go"
unassign = "@me"
comment = "{comment}"

[verbs.unpair]
when = "Bob leaves"
unassign = "bob"

[verbs.swap]
when = "Hand it over and leave"
assign = "{assignee}"
unassign = "@me"

# The blockers check: begin warns, claim and check refuse.
[verbs.begin]
when = "Begin, even if blocked"
to = "In progress"
blockers = "warn"

[verbs.claim]
when = "Claim an unblocked issue"
to = "In progress"
from = ["Backlog"]
blockers = "deny"

[verbs.check]
when = "Note something on an unblocked issue"
comment = "{comment}"
blockers = "deny"

# The sub_issues check: wrap warns, finish refuses, after blockers.
[verbs.wrap]
when = "Wrap up, even with sub-issues left"
to = "Ready"
sub_issues = "warn"

[verbs.finish]
when = "Finish an issue whose sub-issues are done"
to = "Done"
blockers = "deny"
sub_issues = "deny"
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
	if err := p.Unconfirmed(); err != nil {
		e := render.FromError(err)
		fmt.Fprintf(&b, "unconfirmed: code %d\n%s\n", e.Code, e.Text())
	}
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

// blocked gives is n unresolved blockers, in varied places, and unread
// ones that were not read.
func blocked(is Issue, n, unread int) Issue {
	places := []string{"In progress", "not on the board", "status not read"}
	for i := range n {
		is.Unresolved = append(is.Unresolved, Blocker{Ref: fmt.Sprintf("#%d", 12+i), Place: places[i%len(places)]})
	}
	is.UnreadBlockers = unread
	return is
}

// withSubIssues gives is n unresolved sub-issues, in varied places, and
// unread ones that were not read.
func withSubIssues(is Issue, n, unread int) Issue {
	places := []string{"In progress", "not on the board", "status not read"}
	for i := range n {
		is.UnresolvedSubIssues = append(is.UnresolvedSubIssues, Blocker{Ref: fmt.Sprintf("#%d", 50+i), Place: places[i%len(places)]})
	}
	is.UnreadSubIssues = unread
	return is
}

// byIssue gives is the issue --by names, as the reader finds it.
func byIssue(is Issue, ref string, blocks bool) Issue {
	is.ByIssues = append(is.ByIssues, ByIssue{Ref: ref, Blocks: blocks})
	return is
}

// otherRepo puts is in acme/api, outside the default repository acme/shop,
// as the reader fills it.
func otherRepo(is Issue) Issue {
	is.Display, is.DefaultRepo = fmt.Sprintf("acme/api#%d", is.Number), "acme/shop"
	return is
}

// byPR gives is a pull request --by names.
func byPR(is Issue, ref string) Issue {
	is.ByIssues = append(is.ByIssues, ByIssue{Ref: ref, PullRequest: true})
	return is
}

func TestPlan(t *testing.T) {
	_, c := testEngine(t)
	posted := func(verb, body string) Comment {
		return Comment{Body: body + "\n\n" + Marker(verb, body), ViewerDidAuthor: true}
	}
	started := func(body string) Comment { return posted("start", body) }
	sessionBody := "Started. Session: claude://s/abc"
	byOther := started(sessionBody)
	byOther.ViewerDidAuthor = false

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
		{name: "done_closed_by_pr", verb: "done", issue: Issue{Number: 42, Closed: true, StateReason: "completed",
			OnBoard: true, Status: "Ready"}, args: Args{Comment: "released in v1.2", HasComment: true}, hasOps: 2},
		{name: "done_closes", verb: "done", issue: onBoard("Ready"), hasOps: 2},
		{name: "done_closed_already_done", verb: "done", noOps: true,
			issue: Issue{Number: 42, Closed: true, OnBoard: true, Status: "Done"}},
		{name: "done_closed_not_on_board", verb: "done", issue: Issue{Number: 42, Closed: true}, code: render.ExitDenied},
		{name: "done_closed_wrong_from", verb: "done", issue: Issue{Number: 42, Closed: true, OnBoard: true, Status: "Waiting"},
			code: render.ExitDenied},
		// done closes the issue, so it runs on a closed one even when Done is not final.
		{name: "done_closed_no_final", verb: "done", issue: Issue{Number: 42, Closed: true, OnBoard: true, Status: "Ready"},
			hasOps: 1, setup: func(e *Engine) { e.Final = nil }},
		{name: "park_closed_no_final", verb: "park", issue: Issue{Number: 42, Closed: true, OnBoard: true, Status: "Ready"},
			code: render.ExitDenied},
		// drop: confirm = "user", close = "not_planned", require = ["comment"].
		{name: "drop_without_yes", verb: "drop", issue: onBoard("In progress"),
			args: Args{Comment: "duplicate of #7", HasComment: true}, hasOps: 2},
		{name: "drop_with_yes", verb: "drop", issue: onBoard("In progress"),
			args: Args{Comment: "duplicate of #7", HasComment: true, Yes: true}, hasOps: 2},
		{name: "drop_missing_comment", verb: "drop", issue: onBoard("In progress"), args: Args{Yes: true},
			code: render.ExitUsage},
		{name: "drop_not_on_board", verb: "drop", issue: Issue{Number: 42},
			args: Args{Comment: "not needed", HasComment: true, Yes: true}, hasOps: 3},
		// A repeat finds the issue closed and the comment posted: nothing to
		// do, so no go-ahead is needed either.
		{name: "drop_repeat", verb: "drop", noOps: true, args: Args{Comment: "not needed", HasComment: true},
			issue: Issue{Number: 42, Closed: true, StateReason: "not_planned", OnBoard: true, Status: "Backlog",
				Comments: []Comment{posted("drop", "not needed")}}},
		// drop has no from, yet an issue closed otherwise (released, here)
		// is no repeat: refused, no comment left.
		{name: "drop_closed_as_completed", verb: "drop", code: render.ExitDenied,
			args:  Args{Comment: "not needed", HasComment: true, Yes: true},
			issue: Issue{Number: 42, Closed: true, StateReason: "completed", OnBoard: true, Status: "Done"}},
		{name: "start_yes", verb: "start", issue: onBoard("Backlog"), args: Args{Yes: true}, code: render.ExitUsage},
		// review: labels = ["+in-review", "-blocked"], assign = true (board.assignee alice).
		{name: "review_all", verb: "review", hasOps: 4, issue: Issue{Number: 42, Title: "Fix login redirect",
			OnBoard: true, Status: "In progress", Labels: []string{"bug", "blocked"}, Assignees: []string{"bob"}}},
		{name: "review_some_in_place", verb: "review", hasOps: 2, issue: Issue{Number: 42, OnBoard: true,
			Status: "In progress", Labels: []string{"in-review", "blocked"}, Assignees: []string{"Alice"}}},
		// More labels than were read: -blocked may be beyond the window, so
		// it is removed rather than reported in place.
		{name: "review_labels_truncated", verb: "review", hasOps: 2, issue: Issue{Number: 42, OnBoard: true,
			Status: "In progress", Labels: []string{"in-review"}, LabelsTruncated: true, Assignees: []string{"alice"}}},
		// blocked does not exist in the repository: nothing to remove.
		{name: "review_label_missing", verb: "review", hasOps: 1, issue: Issue{Number: 42, OnBoard: true,
			Status: "In progress", Labels: []string{"in-review"}, LabelsTruncated: true, MissingLabels: []string{"blocked"},
			Assignees: []string{"alice"}}},
		// The login as GitHub spells it, not as the config writes it.
		{name: "give_login_spelling", verb: "give", hasOps: 1,
			issue: Issue{Number: 42, OnBoard: true, Status: "Backlog", AssignLogin: "OctoCat"}},
		{name: "give_login_spelling_repeat", verb: "give", noOps: true,
			issue: Issue{Number: 42, OnBoard: true, Status: "Backlog", AssignLogin: "OctoCat", Assignees: []string{"OctoCat"}}},
		// The board's "Item closed" workflow moved the closed issue to Done:
		// a repeat of a verb that only closes is not refused by from.
		{name: "shelve_repeat_after_automation", verb: "shelve", noOps: true,
			issue: Issue{Number: 42, Closed: true, StateReason: "not_planned", OnBoard: true, Status: "Done"}},
		// An issue closed otherwise (here, as completed) is no repeat of a
		// verb that closes as not planned: refused, though its status is in from.
		{name: "shelve_closed_as_completed", verb: "shelve", code: render.ExitDenied,
			issue: Issue{Number: 42, Closed: true, StateReason: "completed", OnBoard: true, Status: "Backlog"}},
		{name: "shelve_closed_as_duplicate", verb: "shelve", code: render.ExitDenied,
			issue: Issue{Number: 42, Closed: true, StateReason: "duplicate", OnBoard: true, Status: "Backlog"}},
		// reject closes as not planned and has no target. A merged issue,
		// closed as completed, is refused whatever its status: no comment is left.
		{name: "reject_merged", verb: "reject", code: render.ExitDenied, args: Args{Comment: "not needed", HasComment: true, Yes: true},
			issue: Issue{Number: 42, Closed: true, StateReason: "completed", OnBoard: true, Status: "Done"}},
		{name: "reject_merged_status_in_from", verb: "reject", code: render.ExitDenied,
			args:  Args{Comment: "not needed", HasComment: true, Yes: true},
			issue: Issue{Number: 42, Closed: true, StateReason: "completed", OnBoard: true, Status: "Waiting"}},
		// A repeat: closed as not planned, the comment posted, and the
		// board's "Item closed" workflow moved it to Done.
		{name: "reject_repeat", verb: "reject", noOps: true, args: Args{Comment: "not needed", HasComment: true},
			issue: Issue{Number: 42, Closed: true, StateReason: "not_planned", OnBoard: true, Status: "Done",
				Comments: []Comment{posted("reject", "not needed")}}},
		// A repeat after a partial write: closed, but the comment is missing.
		{name: "reject_repeat_no_comment", verb: "reject", hasOps: 1, args: Args{Comment: "not needed", HasComment: true, Yes: true},
			issue: Issue{Number: 42, Closed: true, StateReason: "not_planned", OnBoard: true, Status: "Done"}},
		// The other partial write: the comment posted, the issue still open.
		{name: "reject_repeat_open", verb: "reject", hasOps: 1, args: Args{Comment: "not needed", HasComment: true, Yes: true},
			issue: Issue{Number: 42, OnBoard: true, Status: "Waiting", Comments: []Comment{posted("reject", "not needed")}}},
		// The close reason was not read: the verb's own latest comment
		// tells a repeat.
		{name: "reject_reason_unknown_repeat", verb: "reject", noOps: true, args: Args{Comment: "not needed", HasComment: true},
			issue: Issue{Number: 42, Closed: true, OnBoard: true, Status: "Done",
				Comments: []Comment{posted("reject", "not needed")}}},
		{name: "reject_reason_unknown_other_comment", verb: "reject", code: render.ExitDenied,
			args: Args{Comment: "not needed", HasComment: true, Yes: true},
			issue: Issue{Number: 42, Closed: true, OnBoard: true, Status: "Done",
				Comments: []Comment{posted("reject", "not needed"), posted("note", "merged")}}},
		// Only the viewer's own markers count: another user's reject is no
		// repeat when the reason was not read.
		{name: "reject_reason_unknown_other_user", verb: "reject", code: render.ExitDenied,
			args: Args{Comment: "not needed", HasComment: true, Yes: true},
			issue: Issue{Number: 42, Closed: true, OnBoard: true, Status: "Done",
				Comments: []Comment{{Body: "not needed\n\n" + Marker("reject", "not needed")}}}},
		// shelve has no target: its refusal says where it runs, not where it starts.
		{name: "shelve_open_wrong_from", verb: "shelve", code: render.ExitDenied, issue: onBoard("Done")},
		{name: "nudge_not_on_board", verb: "nudge", code: render.ExitDenied, issue: Issue{Number: 42}},
		{name: "nudge_no_status", verb: "nudge", code: render.ExitDenied, issue: Issue{Number: 42, OnBoard: true}},
		{name: "nudge_in_from", verb: "nudge", hasOps: 1, issue: onBoard("Waiting")},
		// done on an issue closed as not planned moves it to Done, with a warning.
		{name: "done_closed_not_planned", verb: "done", hasOps: 1,
			issue: Issue{Number: 42, Closed: true, StateReason: "not_planned", OnBoard: true, Status: "Ready"}},
		{name: "done_closed_duplicate_repeat", verb: "done", noOps: true,
			issue: Issue{Number: 42, Closed: true, StateReason: "duplicate", OnBoard: true, Status: "Done"}},
		{name: "review_repeat", verb: "review", noOps: true, issue: Issue{Number: 42, OnBoard: true,
			Status: "Ready", Labels: []string{"in-review"}, Assignees: []string{"bob", "alice"}}},
		{name: "take_me", verb: "take", hasOps: 1, issue: Issue{Number: 42, OnBoard: true, Status: "Backlog", Viewer: "carol"}},
		{name: "take_me_repeat", verb: "take", noOps: true,
			issue: Issue{Number: 42, OnBoard: true, Status: "Backlog", Viewer: "carol", Assignees: []string{"carol"}}},
		{name: "take_me_unknown_viewer", verb: "take", issue: onBoard("Backlog"), code: render.ExitUsage},
		// ask: assign = "{assignee}", required; {assignee} is the login as
		// GitHub spells it.
		{name: "ask_assignee", verb: "ask", hasOps: 3, args: Args{Assignee: " bob ", HasAssignee: true},
			issue: Issue{Number: 42, OnBoard: true, Status: "In progress", AssignLogin: "Bob", Assignees: []string{"alice"}}},
		{name: "ask_assignee_repeat", verb: "ask", noOps: true, args: Args{Assignee: "bob", HasAssignee: true},
			issue: Issue{Number: 42, OnBoard: true, Status: "Ready", AssignLogin: "Bob", Assignees: []string{"Bob"},
				Comments: []Comment{posted("ask", "Review, please, @Bob.")}}},
		{name: "ask_me", verb: "ask", hasOps: 3, args: Args{Assignee: "@me", HasAssignee: true},
			issue: Issue{Number: 42, OnBoard: true, Status: "In progress", Viewer: "carol"}},
		{name: "ask_missing_assignee", verb: "ask", code: render.ExitUsage, issue: onBoard("In progress")},
		{name: "ask_blank_assignee", verb: "ask", code: render.ExitUsage, args: Args{Assignee: " ", HasAssignee: true},
			issue: onBoard("In progress")},
		{name: "ask_at_login", verb: "ask", code: render.ExitUsage, args: Args{Assignee: "@bob", HasAssignee: true},
			issue: onBoard("In progress")},
		{name: "ask_bad_login", verb: "ask", code: render.ExitUsage, args: Args{Assignee: "bob smith", HasAssignee: true},
			issue: onBoard("In progress")},
		{name: "take_assignee_not_taken", verb: "take", code: render.ExitUsage, args: Args{Assignee: "bob", HasAssignee: true},
			issue: onBoard("Backlog")},
		// send-back: assign = true (alice), unassign = "all": alice alone.
		{name: "send_back_all", verb: "send-back", hasOps: 2,
			issue: Issue{Number: 42, OnBoard: true, Status: "Ready", Assignees: []string{"Alice", "bob", "carol"}}},
		{name: "send_back_alone_already", verb: "send-back", noOps: true,
			issue: Issue{Number: 42, OnBoard: true, Status: "In progress", Assignees: []string{"alice"}}},
		{name: "send_back_assigns_author", verb: "send-back", hasOps: 3,
			issue: Issue{Number: 42, OnBoard: true, Status: "Ready", Assignees: []string{"bob"}}},
		{name: "clear_all", verb: "clear", hasOps: 1,
			issue: Issue{Number: 42, OnBoard: true, Status: "Backlog", Assignees: []string{"alice", "bob"}}},
		{name: "clear_none", verb: "clear", noOps: true, issue: onBoard("Backlog")},
		// let-go: unassign = "@me", the viewer.
		{name: "let_go", verb: "let-go", hasOps: 1,
			issue: Issue{Number: 42, OnBoard: true, Status: "In progress", Viewer: "carol", Assignees: []string{"bob", "Carol"}}},
		{name: "let_go_not_assigned", verb: "let-go", noOps: true,
			issue: Issue{Number: 42, OnBoard: true, Status: "In progress", Viewer: "carol", Assignees: []string{"bob"}}},
		{name: "let_go_unknown_viewer", verb: "let-go", code: render.ExitUsage, issue: onBoard("In progress")},
		{name: "unpair", verb: "unpair", hasOps: 1,
			issue: Issue{Number: 42, OnBoard: true, Status: "In progress", Assignees: []string{"alice", "Bob"}}},
		// --assignee @me on a verb that unassigns @me: assigning wins.
		{name: "swap_to_self", verb: "swap", hasOps: 1, args: Args{Assignee: "@me", HasAssignee: true},
			issue: Issue{Number: 42, OnBoard: true, Status: "In progress", Viewer: "carol"}},
		{name: "swap", verb: "swap", hasOps: 2, args: Args{Assignee: "bob", HasAssignee: true},
			issue: Issue{Number: 42, OnBoard: true, Status: "In progress", Viewer: "carol", AssignLogin: "bob",
				Assignees: []string{"carol"}}},
		// revive: reopen = true, to = "Backlog".
		{name: "revive_closed", verb: "revive", hasOps: 3,
			issue: Issue{Number: 42, Closed: true, StateReason: "not_planned", OnBoard: true, Status: "Done"}},
		{name: "revive_closed_archived", verb: "revive", hasOps: 4,
			issue: Issue{Number: 42, Closed: true, OnBoard: true, Archived: true, Status: "Done"}},
		{name: "revive_open", verb: "revive", hasOps: 1, issue: onBoard("Backlog")},
		{name: "note_closed", verb: "note", issue: Issue{Number: 42, Closed: true, OnBoard: true, Status: "Done"},
			args: Args{Comment: "x", HasComment: true}, code: render.ExitDenied},
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
		// start → wait → start: a new cycle comments again, even with the same text.
		{name: "start_new_cycle_after_wait", verb: "start", hasOps: 2, issue: Issue{
			Number: 42, OnBoard: true, Status: "Waiting",
			Comments: []Comment{started(sessionBody), posted("wait", "Waiting for #7.")},
		}},
		// Retries: status set but comment failed, so no marker yet.
		{name: "start_retry_comment_failed", verb: "start", hasOps: 1, issue: Issue{
			Number: 42, OnBoard: true, Status: "In progress",
			Comments: []Comment{started(sessionBody), posted("wait", "Waiting for #7.")},
		}},
		{name: "start_marker_by_other_user", verb: "start", hasOps: 1, issue: Issue{
			Number: 42, OnBoard: true, Status: "In progress", Comments: []Comment{byOther},
		}},
		{name: "start_marker_not_at_end", verb: "start", hasOps: 1, issue: Issue{
			Number: 42, OnBoard: true, Status: "In progress", Comments: []Comment{{
				Body:            "quoting:\n\n" + Marker("start", sessionBody) + "\n\nand more",
				ViewerDidAuthor: true,
			}},
		}},
		{name: "start_marker_trailing_space", verb: "start", noOps: true, issue: Issue{
			Number: 42, OnBoard: true, Status: "In progress", Comments: []Comment{{
				Body:            sessionBody + "\r\n\r\n" + Marker("start", sessionBody) + "\n",
				ViewerDidAuthor: true,
			}},
		}},
		{name: "start_session_unset", verb: "start", issue: onBoard("Backlog"), hasOps: 1,
			setup: func(e *Engine) { e.Getenv = env(nil) }},
		{name: "start_session_unset_with_comment", verb: "start", issue: onBoard("Backlog"), hasOps: 2,
			args: Args{Comment: "on it", HasComment: true}, setup: func(e *Engine) { e.Getenv = env(nil) }},
		{name: "start_session_unset_comment_present", verb: "start", noOps: true,
			args:  Args{Comment: "on it", HasComment: true},
			setup: func(e *Engine) { e.Getenv = env(nil) },
			issue: Issue{Number: 42, OnBoard: true, Status: "In progress", Comments: []Comment{started("on it")}}},
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
		// An archived item is unarchived only by a move to a status that is not final.
		{name: "start_archived", verb: "start", hasOps: 3,
			issue: Issue{Number: 42, OnBoard: true, Archived: true, Status: "Backlog"}},
		{name: "ready_repeat_archived", verb: "ready", noOps: true,
			issue: Issue{Number: 42, OnBoard: true, Archived: true, Status: "Ready"}},
		{name: "start_repeat_archived_comment", verb: "start", hasOps: 1,
			issue: Issue{Number: 42, OnBoard: true, Archived: true, Status: "In progress"}},
		{name: "done_archived", verb: "done", hasOps: 2,
			issue: Issue{Number: 42, OnBoard: true, Archived: true, Status: "Ready"}},
		{name: "note_archived", verb: "note", hasOps: 1, args: Args{Comment: "hi", HasComment: true},
			issue: Issue{Number: 42, OnBoard: true, Archived: true, Status: "Ready"}},
		{name: "start_archived_not_on_board", verb: "start", hasOps: 3,
			issue: Issue{Number: 42, Archived: true}},
		// blockers = "warn" / "deny": unresolved and unread blockers.
		{name: "begin_blocked_warns", verb: "begin", hasOps: 1, issue: blocked(onBoard("Backlog"), 2, 0)},
		{name: "begin_unread_warns", verb: "begin", hasOps: 1, issue: blocked(onBoard("Backlog"), 0, 3)},
		{name: "begin_unblocked", verb: "begin", hasOps: 1, issue: onBoard("Backlog")},
		{name: "claim_blocked_refused", verb: "claim", code: render.ExitDenied, issue: blocked(onBoard("Backlog"), 2, 0)},
		{name: "claim_many_blocked_refused", verb: "claim", code: render.ExitDenied, issue: blocked(onBoard("Backlog"), 7, 1)},
		{name: "claim_unblocked", verb: "claim", hasOps: 1, issue: onBoard("Backlog")},
		// Like from, the check is skipped for an issue already in the target.
		{name: "claim_repeat_blocked", verb: "claim", noOps: true, issue: blocked(onBoard("In progress"), 1, 0)},
		// from is checked first.
		{name: "claim_wrong_from_blocked", verb: "claim", code: render.ExitDenied, issue: blocked(onBoard("Ready"), 1, 0)},
		{name: "check_blocked_refused", verb: "check", code: render.ExitDenied,
			args: Args{Comment: "x", HasComment: true}, issue: blocked(onBoard("Ready"), 1, 0)},
		// sub_issues = "warn" / "deny": unresolved and unread sub-issues.
		{name: "wrap_sub_issues_warns", verb: "wrap", hasOps: 1, issue: withSubIssues(onBoard("In progress"), 2, 1)},
		{name: "finish_sub_issues_refused", verb: "finish", code: render.ExitDenied, issue: withSubIssues(onBoard("Ready"), 5, 0)},
		{name: "finish_unread_sub_issues_refused", verb: "finish", code: render.ExitDenied, issue: withSubIssues(onBoard("Ready"), 0, 2)},
		{name: "finish_sub_issues_done", verb: "finish", hasOps: 1, issue: onBoard("Ready")},
		{name: "finish_repeat_sub_issues", verb: "finish", noOps: true, issue: withSubIssues(onBoard("Done"), 1, 0)},
		// Blockers are checked first.
		{name: "finish_blocked_and_sub_issues", verb: "finish", code: render.ExitDenied,
			issue: withSubIssues(blocked(onBoard("Ready"), 1, 0), 1, 0)},
		// blockers = "link" (kanban's wait): --by names an issue the reader found.
		{name: "wait_links", verb: "wait", hasOps: 3, args: Args{By: "12", HasBy: true},
			issue: byIssue(onBoard("In progress"), "#12", false)},
		{name: "wait_link_in_place", verb: "wait", hasOps: 2, args: Args{By: "acme/api#7", HasBy: true},
			issue: byIssue(onBoard("In progress"), "acme/api#7", true)},
		{name: "wait_link_repeat", verb: "wait", noOps: true, args: Args{By: "12", HasBy: true},
			issue: byIssue(Issue{Number: 42, OnBoard: true, Status: "Waiting",
				Comments: []Comment{posted("wait", "Waiting for #12.")}}, "#12", true)},
		// --by 25,26: both linked, the comment names both.
		{name: "wait_links_two", verb: "wait", hasOps: 4, args: Args{By: "25,26", HasBy: true},
			issue: byIssue(byIssue(onBoard("In progress"), "#25", false), "#26", false)},
		{name: "wait_links_two_one_in_place", verb: "wait", hasOps: 4, args: Args{By: "25, acme/api#7, 26", HasBy: true},
			issue: byIssue(byIssue(byIssue(onBoard("In progress"), "#25", false), "acme/api#7", true), "#26", false)},
		// A pull request is named in the comment, with a warning, not linked.
		{name: "wait_link_pull_request", verb: "wait", hasOps: 2, args: Args{By: "40", HasBy: true},
			issue: byPR(onBoard("In progress"), "#40")},
		{name: "wait_link_issue_and_pull_request", verb: "wait", hasOps: 3, args: Args{By: "40,26", HasBy: true},
			issue: byIssue(byPR(onBoard("In progress"), "#40"), "#26", false)},
		// Free text in --by: the reader names no issue, the comment alone.
		{name: "wait_by_text_no_link", verb: "wait", hasOps: 2, args: Args{By: "the API team", HasBy: true},
			issue: onBoard("In progress")},
		// An issue outside the default repository (F12): every line names
		// it acme/api#42; the links are named relative to the default
		// repository, and mentioned in the comment as GitHub links them
		// from acme/api.
		{name: "wait_links_other_repo", verb: "wait", hasOps: 4, args: Args{By: "12, acme/api#7", HasBy: true},
			issue: otherRepo(Issue{Number: 42, Title: "Rate limit the export", OnBoard: true, Status: "In progress",
				ByIssues: []ByIssue{{Ref: "#12", Mention: "acme/shop#12"}, {Ref: "acme/api#7", Mention: "#7"}}})},
		// A bare number in free --by: the default repository's issue.
		{name: "wait_by_number_other_repo", verb: "wait", hasOps: 2, args: Args{By: "#12", HasBy: true},
			issue: otherRepo(onBoard("In progress"))},
		{name: "start_closed_other_repo", verb: "start", issue: otherRepo(Issue{Number: 42, Closed: true}),
			code: render.ExitDenied},
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
	text := AddComment{Body: "Started.", Marker: m}.Text()
	mine := func(body string) Comment { return Comment{Body: body, ViewerDidAuthor: true} }
	for _, tt := range []struct {
		name     string
		comments []Comment
		want     string // "" means no marker
	}{
		{"own comment", []Comment{mine(text)}, m},
		{"latest wins", []Comment{mine(text), mine(AddComment{Body: "x", Marker: Marker("wait", "x")}.Text())},
			Marker("wait", "x")},
		{"plain comment after marker", []Comment{mine(text), mine("thanks")}, m},
		{"other user", []Comment{{Body: text}}, ""},
		{"not at the end", []Comment{mine(text + "\n\nP.S.")}, ""},
		{"no blank line before", []Comment{mine("Started. " + m)}, ""},
		{"marker alone", []Comment{mine(m)}, ""},
		{"two markers count by the last", []Comment{mine(text + "\n\n" + Marker("ready", "y"))}, Marker("ready", "y")},
		{"no comments", nil, ""},
	} {
		got, ok := LastMarker(tt.comments)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("%s: LastMarker = %q, %v; want %q", tt.name, got, ok, tt.want)
		}
	}
}

func TestExample(t *testing.T) {
	_, c := testEngine(t)
	for verb, want := range map[string]string{
		"start": "gh dojo start 7",
		"wait":  `gh dojo wait 7 --by "<#N or name>"`,
		"pair":  `gh dojo pair 7 --comment "<text>" --by "<#N or name>"`,
		"drop":  `gh dojo drop 7 --comment "<text>"`,
	} {
		v, _ := c.Verb(verb)
		if got := Example(v, "7"); got != want {
			t.Errorf("Example(%s) = %q, want %q", verb, got, want)
		}
	}
}

// TestErrFrom: a refusal for the status alone wraps ErrFrom, which
// gh dojo release reads as "not ready"; other refusals do not.
func TestErrFrom(t *testing.T) {
	e, c := testEngine(t)
	ready, _ := c.Verb("ready")
	start, _ := c.Verb("start")
	if _, err := e.Plan(ready, onBoard("Backlog"), Args{}); !errors.Is(err, ErrFrom) {
		t.Errorf("ready from Backlog: %v, want ErrFrom", err)
	}
	closed := Issue{Number: 42, Closed: true, OnBoard: true, Status: "Backlog"}
	if _, err := e.Plan(start, closed, Args{}); err == nil || errors.Is(err, ErrFrom) {
		t.Errorf("start on a closed issue: %v, want a refusal other than ErrFrom", err)
	}
}
