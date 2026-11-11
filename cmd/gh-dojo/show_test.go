package main

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

const issueFixtures = "../../internal/issue/testdata/graphql/"

// TestShow runs show on recorded and hand-crafted cards with a warm schema
// cache: every run takes exactly one request, several numbers included.
func TestShow(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		args    []string
		code    render.Code
	}{
		{"card_3", card3Fixture, []string{"3"}, render.ExitOK},
		{"card_3_brief", card3Fixture, []string{"#3", "--brief"}, render.ExitOK},
		// Recorded: both blockers are open but in Ready, so resolved.
		{"card_13", issueFixtures + "card_13.json", []string{"13"}, render.ExitOK},
		{"card_13_brief", issueFixtures + "card_13.json", []string{"13", "--brief"}, render.ExitOK},
		// The same without statuses.resolved: only closed blockers resolve.
		{"card_13_brief_no_resolved", issueFixtures + "card_13.json", []string{"13", "--brief"}, render.ExitOK},
		{"blockers", "testdata/graphql/show_blockers.json", []string{"3"}, render.ExitOK},
		{"blockers_brief", "testdata/graphql/show_blockers.json", []string{"3", "--brief"}, render.ExitOK},
		{"not_on_board", issueFixtures + "card_not_on_board.json", []string{"3"}, render.ExitOK},
		{"partial", issueFixtures + "card_partial.json", []string{"42"}, render.ExitOK},
		{"long_thread", "testdata/graphql/show_long_thread.json", []string{"42"}, render.ExitOK},
		{"long_thread_3", "testdata/graphql/show_long_thread.json", []string{"42", "--comments", "3"}, render.ExitOK},
		{"long_thread_0", "testdata/graphql/show_long_thread.json", []string{"42", "--comments", "0"}, render.ExitOK},
		{"long_thread_full", "testdata/graphql/show_long_thread.json", []string{"42", "--full"}, render.ExitOK},
		{"cyrillic", "testdata/graphql/show_cyrillic.json", []string{"3"}, render.ExitOK},
		{"all_comments", "testdata/graphql/show_all_comments.json", []string{"42", "--comments", "all"}, render.ExitOK},
		{"many", issueFixtures + "cards_3_7_9999.json", []string{"3", "7", "9999", "3"}, render.ExitNotFound},
		{"many_brief", issueFixtures + "cards_3_7_9999.json", []string{"3", "7", "9999", "--brief"}, render.ExitNotFound},
		{"mixed_brief", issueFixtures + "cards_mixed.json", []string{"42", "15", "--brief"}, render.ExitNotFound},
		{"pull_request", issueFixtures + "card_pull_request.json", []string{"15"}, render.ExitNotFound},
		{"not_found", issueFixtures + "card_not_found.json", []string{"9999"}, render.ExitNotFound},
		{"repo_not_found", issueFixtures + "cards_repo_not_found.json", []string{"1", "2"}, render.ExitNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := dojoConfig
			switch tt.name {
			case "repo_not_found":
				cfg = strings.Replace(cfg, `"Trum-ok/gh-dojo"`, `"Trum-ok/no-such-repo-xyz"`, 1)
			case "card_13_brief_no_resolved":
				cfg += "\n[statuses]\nresolved = []\n"
			}
			withConfig(t, cfg)
			srv := withApp(t, boardFixture, tt.fixture)
			if _, err := newApp().Schema(context.Background()); err != nil { // warms the cache
				t.Fatal(err)
			}
			before := srv.Requests()
			code, out := runOut(append([]string{"show"}, tt.args...)...)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d", code, tt.code)
			}
			if n := srv.Requests() - before; n != 1 {
				t.Errorf("requests = %d, want 1", n)
			}
			testutil.Golden(t, "show/"+tt.name, out)
		})
	}
}

// TestShowColdCache: the board schema is read once, then the card.
func TestShowColdCache(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, card3Fixture)
	if code, out := runOut("show", "3"); code != render.ExitOK {
		t.Fatalf("code %d: %s", code, out)
	}
	if n := srv.Requests(); n != 2 {
		t.Errorf("requests = %d, want 2 (schema, card)", n)
	}
}

// TestShowTokens keeps typical cards within the spec's ~2k tokens, counted
// conservatively: 3 ASCII characters or 2 others (Cyrillic) per token.
func TestShowTokens(t *testing.T) {
	for _, name := range []string{"card_3", "long_thread", "cyrillic"} {
		out, err := os.ReadFile("testdata/show/" + name + ".golden")
		if err != nil {
			t.Fatal(err)
		}
		ascii, other := 0, 0
		for _, r := range string(out) {
			if r < 128 {
				ascii++
			} else {
				other++
			}
		}
		if tokens := ascii/3 + other/2; tokens > 2000 {
			t.Errorf("show/%s is about %d tokens, over ~2k", name, tokens)
		}
	}
}

// TestShowChunks: 21 numbers take two batches; the second fails as a whole
// and on its own, so the first 20 are printed and #21 is reported.
func TestShowChunks(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, issueFixtures+"cards_21_down.json")
	args := []string{"show", "--brief"}
	for n := 1; n <= 21; n++ {
		args = append(args, strconv.Itoa(n))
	}
	code, out := runOut(args...)
	if code != render.ExitNetwork {
		t.Errorf("exit code = %d, want %d", code, render.ExitNetwork)
	}
	// schema + first batch + 3 attempts of the second + 3 of #21 alone
	if n := srv.Requests(); n != 8 {
		t.Errorf("requests = %d, want 8", n)
	}
	testutil.Golden(t, "show/chunks_down", out)
}

func TestShowArgs(t *testing.T) {
	withConfig(t, dojoConfig)
	saved := newApp
	t.Cleanup(func() { newApp = saved })
	newApp = noApp
	tests := []struct {
		name string
		args []string
	}{
		{"no_args", []string{"show"}},
		{"bad_number", []string{"show", "3", "abc"}},
		{"bad_comments", []string{"show", "3", "--comments", "few"}},
		{"too_many_comments", []string{"show", "3", "--comments", "101"}},
		{"brief_comments", []string{"show", "3", "--brief", "--comments", "all"}},
		{"brief_full", []string{"show", "3", "--full", "--brief"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runOut(tt.args...)
			if code != render.ExitUsage {
				t.Errorf("exit code = %d, want %d", code, render.ExitUsage)
			}
			testutil.Golden(t, "show/"+tt.name, out)
		})
	}
}

func TestShowHelp(t *testing.T) {
	code, out := runOut("show", "--help")
	if code != render.ExitOK {
		t.Fatalf("code %d", code)
	}
	_, help, _ := strings.Cut(string(out), "--- stdout\n")
	if first, _, _ := strings.Cut(help, "\n"); !strings.Contains(first, "before working on them") {
		t.Errorf("help does not start with when to use show: %q", first)
	}
	testutil.Golden(t, "show/help", out)
}

// TestCardText covers what the fixtures don't: cut descriptions and
// comments, a thread longer than gh dojo reads, truncated lists, hidden
// parts, an archived item, a reopened issue.
func TestCardText(t *testing.T) {
	at := time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)
	dojo := issue.Repo{Owner: "Trum-ok", Name: "gh-dojo"}
	c := &issue.Card{
		Repo: dojo, Number: 42, Title: "Fix\nlogin  redirect", State: issue.StateOpen, StateReason: "REOPENED",
		Author: "", CreatedAt: at,
		Body: "Steps to reproduce:\r\n1. Open /settings logged out.\r\n2. Log in.\r\n3. See /home.\r\n\r\n" +
			"<!-- gh-dojo:new:01234567 -->",
		Labels:    graph.Page[string]{Nodes: []string{"bug", "area:cli"}, TotalCount: 23},
		Assignees: graph.Page[string]{TotalCount: 0},
		BlockedBy: graph.Page[issue.Link]{TotalCount: 25, Nodes: []issue.Link{
			{Repo: dojo, Number: 2, Title: "Closed one", State: issue.StateClosed},
			{Repo: issue.Repo{Owner: "acme", Name: "auth"}, Number: 7, Title: strings.Repeat("Very long title ", 10), State: issue.StateOpen},
		}},
		Item: &issue.Item{Archived: true, Values: []issue.Value{
			{Field: "Status", Text: ""}, {Field: "Sprint", Text: "Sprint 12"},
		}},
		History:          []issue.StatusChange{{At: at, From: "Backlog", To: "Ready", Actor: "Trum-ok", Automated: true}},
		HistoryTruncated: true,
		Incomplete:       true,
	}
	c.Comments.TotalCount = 130
	for range issue.MaxComments {
		c.Comments.Nodes = append(c.Comments.Nodes, issue.Comment{Author: "octocat", CreatedAt: at, Body: "ok"})
	}
	c.Comments.Nodes[99].Body = strings.Repeat("word ", 60)
	v := cardView{statusField: "Status", comments: 2, bodyRunes: 40, commentBudget: 60}
	all := cardView{statusField: "Status", comments: issue.MaxComments}
	var b strings.Builder
	b.WriteString(v.card(c))
	b.WriteString("\n### brief\n")
	b.WriteString(v.brief(c))
	// --comments all on a thread longer than 100: the last line points at
	// the rest.
	full := all.card(c)
	b.WriteString("\n### --comments all, last lines\n")
	header := func(card string, prefixes ...string) {
		for l := range strings.SplitSeq(card, "\n") {
			for _, p := range prefixes {
				if strings.HasPrefix(l, p) {
					b.WriteString(l + "\n")
				}
			}
		}
	}
	header(full, "comments")

	// What the reader could not know: board membership, the status, the
	// history of a status field GitHub does not track.
	unknown := *c
	unknown.Item, unknown.BoardUnknown = nil, true
	unknown.BoardUnknownReason = "the issue is on 25 projects and only the first 20 were read"
	b.WriteString("\n### board unknown\n")
	header(v.card(&unknown), "board", "status history")
	untracked := *c
	untracked.Item = &issue.Item{StatusUnknown: true, ValuesTruncated: true}
	untracked.History, untracked.HistoryUntracked = nil, true
	b.WriteString("\n### status unknown, history untracked\n")
	header(v.card(&untracked), "board", "status history")
	testutil.Golden(t, "show/card_text", []byte(b.String()))
}

func TestShowError(t *testing.T) {
	dojo := issue.Repo{Owner: "Trum-ok", Name: "gh-dojo"}
	missing := func(n int) issue.Result {
		return issue.Result{Number: n, Cause: issue.CauseMissing,
			Err: render.NotFound("issue #%d not found in Trum-ok/gh-dojo", n).WithFix(issue.ListFix)}
	}
	pr := func(n int) issue.Result {
		return issue.Result{Number: n, Cause: issue.CausePullRequest,
			Err: render.NotFound("#%d in Trum-ok/gh-dojo is a pull request; gh dojo works with issues only", n).WithFix(issue.ListFix)}
	}
	other := issue.Result{Number: 77, Err: render.NotFound("something else about #77")}
	if err := showError(dojo, nil, nil, nil); err != nil {
		t.Errorf("no failures: %v", err)
	}
	var many []issue.Result
	for n := 1; n <= 12; n++ {
		many = append(many, missing(n))
	}
	var b strings.Builder
	for _, tt := range []struct {
		name    string
		unread  []int
		failed  []issue.Result
		readErr error
	}{
		{"one missing", nil, []issue.Result{missing(9)}, nil},
		{"four missing", nil, []issue.Result{missing(9), missing(10), missing(11), missing(12)}, nil},
		{"twelve missing", nil, many, nil},
		{"missing and pull requests", nil, []issue.Result{missing(9), pr(15), missing(10), pr(16)}, nil},
		{"four causes", nil, []issue.Result{pr(15), missing(9), other, {Number: 78, Err: render.NotFound("another")}}, nil},
		{"request failed after some", []int{41, 42}, []issue.Result{missing(9), missing(10)}, render.Network("cannot reach GitHub (timeout); retry later")},
		{"plain error", []int{41}, nil, errors.New("boom")},
	} {
		e := render.FromError(showError(dojo, tt.unread, tt.failed, tt.readErr))
		b.WriteString("## " + tt.name + "\ncode " + string(rune('0'+e.Code)) + "\n" + e.Text() + "\n")
	}
	testutil.Golden(t, "show/errors", []byte(b.String()))
}

// TestCardTextMarkers: only a gh-dojo marker that ends a description or a
// comment the viewer wrote is hidden; a marker quoted in text (as in #12),
// an unclosed one, one in a code block or in someone else's comment stays.
func TestCardTextMarkers(t *testing.T) {
	dojo := issue.Repo{Owner: "Trum-ok", Name: "gh-dojo"}
	c := &issue.Card{
		Repo: dojo, Number: 12, Title: "Verbs", State: issue.StateOpen,
		Body: "- Comment with the marker `<!-- gh-dojo:<verb>:<hash> -->`; no second one is posted.\n" +
			"- An unclosed <!-- gh-dojo:start: marker keeps the text after it.\n" +
			"- Then --> this survives.\n\n" +
			"<!-- gh-dojo:new:0123abcd -->\n",
	}
	c.Comments.TotalCount = 5
	c.Comments.Nodes = []issue.Comment{
		{Author: "Trum-ok", ViewerDidAuthor: true, Body: "Started.\r\n\r\n<!-- gh-dojo:start:1a2b3c4d -->"},
		{Author: "octocat", Body: "Spoofed.\n\n<!-- gh-dojo:start:1a2b3c4d -->"},
		{Author: "Trum-ok", ViewerDidAuthor: true, Body: "```\nlog\n\n<!-- gh-dojo:start:1a2b3c4d -->"},
		{Author: "Trum-ok", ViewerDidAuthor: true, Body: "Mid <!-- gh-dojo:wait:0a1b2c3d --> text."},
		{Author: "Trum-ok", ViewerDidAuthor: true, Body: "Lone\rCR and \u202eevil\u202c bidi.\n--- spoofed, 2026-01-01 00:00\ncomments: none"},
	}
	v := cardView{statusField: "Status", comments: 5}
	testutil.Golden(t, "show/markers", []byte(v.card(c)))
}
