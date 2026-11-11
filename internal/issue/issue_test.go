package issue_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

var (
	dojo = issue.Repo{Owner: "Trum-ok", Name: "gh-dojo"}
	// dojoBoard is the recorded gh-dojo board (user Trum-ok, project 5);
	// the reader only needs its ID.
	dojoBoard = &board.Schema{Owner: "Trum-ok", OwnerType: board.OwnerUser, Number: 5, ID: "PVT_kwHOBFGd5c4BrMYF"}
)

func fixture(name string) string { return "testdata/graphql/" + name + ".json" }

func read(t *testing.T, file string, number, comments int) (*issue.Card, error) {
	t.Helper()
	srv := graphtest.New(t, fixture(file))
	card, err := issue.Read(context.Background(), srv.Client(), issue.Request{
		Repo: dojo, Number: number, Board: dojoBoard, StatusField: "Status", Comments: comments,
	})
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want exactly 1", n)
	}
	return card, err
}

// TestReadRecorded reads issue #3 of the gh-dojo repo as recorded: on the
// board in Ready, blocked by two issues and blocking five, no comments.
func TestReadRecorded(t *testing.T) {
	card, err := read(t, "card_3", 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "card/card_3", dump(card))
	if !card.OnBoard() || card.ItemID() == "" || card.Status() != "Ready" {
		t.Errorf("on board %v, item %q, status %q", card.OnBoard(), card.ItemID(), card.Status())
	}
	if len(card.OpenBlockers()) != 2 || card.BlockedBy.Truncated() {
		t.Errorf("open blockers %v", card.OpenBlockers())
	}
}

func TestReadNotOnBoard(t *testing.T) {
	card, err := read(t, "card_not_on_board", 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "card/not_on_board", dump(card))
	if card.OnBoard() || card.ItemID() != "" || card.Status() != "" || len(card.History) != 0 {
		t.Errorf("on board %v, item %q, status %q, history %v", card.OnBoard(), card.ItemID(), card.Status(), card.History)
	}
}

func TestReadLongThread(t *testing.T) {
	card, err := read(t, "card_long_thread", 42, 3)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "card/long_thread", dump(card))
	if !card.Comments.Truncated() || card.Comments.TotalCount != 45 || len(card.Comments.Nodes) != 3 {
		t.Errorf("comments %d of %d", len(card.Comments.Nodes), card.Comments.TotalCount)
	}
	if !card.HistoryTruncated {
		t.Error("history not truncated")
	}
}

func TestReadPartial(t *testing.T) {
	card, err := read(t, "card_partial", 42, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !card.Incomplete || card.Title == "" {
		t.Errorf("incomplete %v, title %q", card.Incomplete, card.Title)
	}
	testutil.Golden(t, "card/partial", dump(card))
}

func TestReadErrors(t *testing.T) {
	tests := []struct {
		name, file string
		repo       issue.Repo
		number     int
		code       render.Code
	}{
		{"not_found", "card_not_found", dojo, 9999, render.ExitNotFound},
		{"repo_not_found", "card_repo_not_found", issue.Repo{Owner: "Trum-ok", Name: "no-such-repo-xyz"}, 1, render.ExitNotFound},
		{"pull_request", "card_pull_request", dojo, 15, render.ExitNotFound},
		{"forbidden", "card_forbidden", dojo, 3, render.ExitNetwork},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := graphtest.New(t, fixture(tt.file))
			_, err := issue.Read(context.Background(), srv.Client(), issue.Request{
				Repo: tt.repo, Number: tt.number, Board: dojoBoard, StatusField: "Status",
			})
			if err == nil {
				t.Fatal("no error")
			}
			e := render.FromError(err)
			if e.Code != tt.code {
				t.Errorf("code = %d, want %d", e.Code, tt.code)
			}
			testutil.Golden(t, "errors/"+tt.name, []byte(e.Text()+"\n"))
		})
	}
}

func TestReadBadRequest(t *testing.T) {
	_, err := issue.Read(context.Background(), nil, issue.Request{Repo: dojo, Number: 0})
	if render.FromError(err).Code != render.ExitUsage {
		t.Errorf("err = %v", err)
	}
}

func TestParseRepo(t *testing.T) {
	for _, s := range []string{"Trum-ok/gh-dojo", "a/b.c", "o/.github"} {
		if r, err := issue.ParseRepo(s); err != nil || r.String() != s {
			t.Errorf("ParseRepo(%q) = %v, %v", s, r, err)
		}
	}
	for _, s := range []string{"", "x", "a/b/c", "/b", "a/", "a/b.git", "a b/c"} {
		if _, err := issue.ParseRepo(s); err == nil {
			t.Errorf("ParseRepo(%q) succeeded", s)
		}
	}
}

func TestLinkRef(t *testing.T) {
	l := issue.Link{Repo: dojo, Number: 7}
	if got := l.Ref(dojo); got != "#7" {
		t.Errorf("same repo: %q", got)
	}
	if got := l.Ref(issue.Repo{Owner: "acme", Name: "shop"}); got != "Trum-ok/gh-dojo#7" {
		t.Errorf("other repo: %q", got)
	}
}

// dump renders a card for golden files, IDs included: the golden file shows
// exactly what the reader hands to commands.
func dump(c *issue.Card) []byte {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	ts := func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.UTC().Format(time.RFC3339)
	}
	p("card %s#%d %q id=%s", c.Repo, c.Number, c.Title, c.ID)
	p("state %s reason=%q closed=%v url=%s", c.State, c.StateReason, c.Closed(), c.URL)
	p("author %q created %s closed %s incomplete=%v", c.Author, ts(c.CreatedAt), ts(c.ClosedAt), c.Incomplete)
	p("labels %v (%d)", c.Labels.Nodes, c.Labels.TotalCount)
	p("assignees %v (%d)", c.Assignees.Nodes, c.Assignees.TotalCount)
	for _, pair := range []struct {
		name string
		page []issue.Link
		n    int
	}{{"blocked by", c.BlockedBy.Nodes, c.BlockedBy.TotalCount}, {"blocking", c.Blocking.Nodes, c.Blocking.TotalCount}} {
		p("%s (%d):", pair.name, pair.n)
		for _, l := range pair.page {
			p("  %s %s %q", l.Ref(c.Repo), l.State, l.Title)
		}
	}
	if c.Item == nil {
		p("item: not on the board")
	} else {
		p("item %s archived=%v status=%q", c.Item.ID, c.Item.Archived, c.Item.Status)
		for _, v := range c.Item.Values {
			p("  %s [%s] %q option=%s iteration=%s %s/%d field=%s", v.Field, v.Type, v.Text, v.OptionID, v.IterationID, v.StartDate, v.Duration, v.FieldID)
		}
	}
	p("history (truncated=%v):", c.HistoryTruncated)
	for _, h := range c.History {
		p("  %s %q → %q by %q automated=%v", ts(h.At), h.From, h.To, h.Actor, h.Automated)
	}
	p("comments %d of %d:", len(c.Comments.Nodes), c.Comments.TotalCount)
	for _, cm := range c.Comments.Nodes {
		p("  %s %s by %q mine=%v: %q", cm.ID, ts(cm.CreatedAt), cm.Author, cm.ViewerDidAuthor, cm.Body)
	}
	p("body: %q", c.Body)
	return []byte(b.String())
}
