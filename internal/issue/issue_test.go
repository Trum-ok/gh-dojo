package issue_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	// statusID is the ID of the board's Status field.
	statusID = "PVTSSF_lAHOBFGd5c4BrMYFzhpgKzQ"
	// dojoStatus is how App.Card asks for the board's status.
	dojoStatus = issue.Request{Repo: dojo, Board: dojoBoard, StatusField: "Status", StatusFieldID: statusID}
)

func fixture(name string) string { return "testdata/graphql/" + name + ".json" }

func read(t *testing.T, file string, number, comments int) (*issue.Card, error) {
	t.Helper()
	srv := graphtest.New(t, fixture(file))
	req := dojoStatus
	req.Number, req.Comments = number, comments
	card, err := issue.Read(context.Background(), srv.Client(), req)
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
	// The withheld project may be this board: not "not on the board".
	if card.OnBoard() || !card.BoardUnknown || !card.StatusUnknown() {
		t.Errorf("on board %v, board unknown %v, status unknown %v", card.OnBoard(), card.BoardUnknown, card.StatusUnknown())
	}
	testutil.Golden(t, "card/partial", dump(card))
}

// variant writes a hand-crafted copy of the recorded card_3.json, changed
// by edit (given the issue object of the response), and returns its path.
func variant(t *testing.T, edit func(issue map[string]any)) string {
	t.Helper()
	data, err := os.ReadFile(fixture("card_3"))
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	f["handcrafted"] = true // nothing to record
	in := f["interactions"].([]any)[0].(map[string]any)
	delete(in, "variables") // any request
	edit(in["response"].(map[string]any)["data"].(map[string]any)["repository"].(map[string]any)["issueOrPullRequest"].(map[string]any))
	if data, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "card.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// boardItem returns the recorded item of the gh-dojo board.
func boardItem(is map[string]any) map[string]any {
	return is["projectItems"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
}

// renameStatus renames the Status field to name in the item's values, as
// GitHub returns it after the field is renamed: the lookup by the old name
// finds nothing.
func renameStatus(is map[string]any, name string) {
	item := boardItem(is)
	item["status"] = nil
	for _, v := range item["fieldValues"].(map[string]any)["nodes"].([]any) {
		if f, ok := v.(map[string]any)["field"].(map[string]any); ok && f["id"] == statusID {
			f["name"] = name
		}
	}
}

// dropStatusValue removes the Status value from the fieldValues window and
// marks the window truncated.
func dropStatusValue(is map[string]any) {
	fv := boardItem(is)["fieldValues"].(map[string]any)
	var kept []any
	for _, v := range fv["nodes"].([]any) {
		if f, ok := v.(map[string]any)["field"].(map[string]any); !ok || f["id"] != statusID {
			kept = append(kept, v)
		}
	}
	fv["nodes"], fv["totalCount"] = kept, 120
}

func readVariant(t *testing.T, req issue.Request, edit func(map[string]any)) *issue.Card {
	t.Helper()
	srv := graphtest.New(t, variant(t, edit))
	req.Number = 3
	card, err := issue.Read(context.Background(), srv.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	return card
}

func TestReadStatus(t *testing.T) {
	byName := dojoStatus
	byName.StatusFieldID = ""
	tests := []struct {
		name    string
		req     issue.Request
		edit    func(map[string]any)
		status  string
		unknown bool
	}{
		{"recorded", dojoStatus, func(map[string]any) {}, "Ready", false},
		// The field was renamed after the schema was cached: matched by ID.
		{"renamed", dojoStatus, func(is map[string]any) { renameStatus(is, "Stage") }, "Ready", false},
		{"renamed_by_name", byName, func(is map[string]any) { renameStatus(is, "Stage") }, "", false},
		// Another field took the old name: its value is not the status.
		{"renamed_name_reused", dojoStatus, func(is map[string]any) {
			renameStatus(is, "Stage")
			boardItem(is)["status"] = map[string]any{
				"__typename": "ProjectV2ItemFieldSingleSelectValue", "name": "Urgent", "optionId": "o1",
				"field": map[string]any{"id": "PVTSSF_new", "name": "Status", "dataType": "SINGLE_SELECT"},
			}
		}, "Ready", false},
		// More values than the window: the lookup by name still has it.
		{"outside_window", dojoStatus, dropStatusValue, "Ready", false},
		// Renamed and outside the window: neither finds it.
		{"renamed_outside_window", dojoStatus, func(is map[string]any) {
			renameStatus(is, "Stage")
			dropStatusValue(is)
		}, "", true},
		{"unset", dojoStatus, func(is map[string]any) {
			boardItem(is)["status"] = nil
			dropStatusValue(is)
			boardItem(is)["fieldValues"].(map[string]any)["totalCount"] = 4
		}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card := readVariant(t, tt.req, tt.edit)
			if card.Status() != tt.status || card.StatusUnknown() != tt.unknown || !card.OnBoard() {
				t.Errorf("status %q unknown %v on board %v, want %q unknown %v",
					card.Status(), card.StatusUnknown(), card.OnBoard(), tt.status, tt.unknown)
			}
		})
	}
}

func TestReadArchived(t *testing.T) {
	card := readVariant(t, dojoStatus, func(is map[string]any) { boardItem(is)["isArchived"] = true })
	if !card.OnBoard() || !card.Archived() || card.Status() != "Ready" {
		t.Errorf("on board %v, archived %v, status %q", card.OnBoard(), card.Archived(), card.Status())
	}
	if card := readVariant(t, dojoStatus, func(map[string]any) {}); card.Archived() {
		t.Error("recorded item archived")
	}
}

func TestReadBoardUnknown(t *testing.T) {
	// On 25 projects; the 20 read are other boards.
	card := readVariant(t, dojoStatus, func(is map[string]any) {
		pi := is["projectItems"].(map[string]any)
		boardItem(is)["project"] = map[string]any{"id": "PVT_other"}
		pi["totalCount"] = 25
	})
	if card.OnBoard() || !card.BoardUnknown || !card.StatusUnknown() || card.Incomplete {
		t.Errorf("on board %v, board unknown %v, status unknown %v, incomplete %v",
			card.OnBoard(), card.BoardUnknown, card.StatusUnknown(), card.Incomplete)
	}
	if want := "the issue is on 25 projects and only the first 1 were read"; card.BoardUnknownReason != want {
		t.Errorf("reason %q, want %q", card.BoardUnknownReason, want)
	}

	// Truncated, but the board's item is among those read.
	card = readVariant(t, dojoStatus, func(is map[string]any) { is["projectItems"].(map[string]any)["totalCount"] = 25 })
	if !card.OnBoard() || card.BoardUnknown {
		t.Errorf("on board %v, board unknown %v", card.OnBoard(), card.BoardUnknown)
	}

	// Not on any board: known.
	card = readVariant(t, dojoStatus, func(is map[string]any) {
		is["projectItems"] = map[string]any{"totalCount": 0, "nodes": []any{}}
	})
	if card.OnBoard() || card.BoardUnknown || card.StatusUnknown() {
		t.Errorf("on board %v, board unknown %v", card.OnBoard(), card.BoardUnknown)
	}
}

// withheldAt makes GitHub withhold the response at path (below the issue)
// with a FORBIDDEN error.
func withheldAt(path ...any) func(f map[string]any) {
	return func(f map[string]any) {
		full := append([]any{"repository", "issueOrPullRequest"}, path...)
		f["errors"] = []any{map[string]any{"type": "FORBIDDEN", "path": full, "message": "Resource not accessible by integration"}}
	}
}

func TestReadWithheldValues(t *testing.T) {
	// GitHub withheld the board item's values: the status is unknown, the
	// item is known.
	path := variant(t, func(is map[string]any) {
		item := boardItem(is)
		item["status"], item["fieldValues"] = nil, nil
	})
	data, _ := os.ReadFile(path)
	var f map[string]any
	_ = json.Unmarshal(data, &f)
	withheldAt("projectItems", "nodes", 0, "fieldValues")(f["interactions"].([]any)[0].(map[string]any)["response"].(map[string]any))
	data, _ = json.Marshal(f)
	_ = os.WriteFile(path, data, 0o644)

	srv := graphtest.New(t, path)
	req := dojoStatus
	req.Number = 3
	card, err := issue.Read(context.Background(), srv.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !card.Incomplete || !card.OnBoard() || card.BoardUnknown || !card.StatusUnknown() || card.Status() != "" {
		t.Errorf("incomplete %v on board %v board unknown %v status unknown %v status %q",
			card.Incomplete, card.OnBoard(), card.BoardUnknown, card.StatusUnknown(), card.Status())
	}
}

// event is a status change in the timeline JSON.
func event(at, project, from, to string) map[string]any {
	return map[string]any{
		"createdAt": at, "previousStatus": from, "status": to, "wasAutomated": false,
		"actor": map[string]any{"login": "Trum-ok"}, "project": map[string]any{"id": project},
	}
}

func TestReadHistoryTruncated(t *testing.T) {
	// The item was added at 2026-11-11T18:19:12Z (recorded).
	board, other := dojoBoard.ID, "PVT_other"
	tests := []struct {
		name      string
		total     int
		events    []any
		truncated bool
	}{
		{"all_read", 1, []any{
			event("2026-11-11T18:19:14Z", board, "", "Backlog"),
		}, false},
		// Older events exist, but the window reaches past the item's
		// creation: they are other boards'.
		{"older_other_boards", 40, []any{
			event("2026-11-01T09:00:00Z", other, "Todo", "Done"),
			event("2026-11-11T18:19:14Z", board, "", "Backlog"),
		}, false},
		// Older events exist and the window starts after the item was
		// added: some may be this board's.
		{"older_may_be_ours", 40, []any{
			event("2026-11-11T18:42:17Z", other, "Todo", "Done"),
			event("2026-11-11T19:02:20Z", board, "In progress", "Ready"),
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card := readVariant(t, dojoStatus, func(is map[string]any) {
				is["timelineItems"] = map[string]any{"totalCount": tt.total, "nodes": tt.events}
			})
			if card.HistoryTruncated != tt.truncated {
				t.Errorf("truncated %v, want %v (history %v)", card.HistoryTruncated, tt.truncated, card.History)
			}
		})
	}

	// Not on the board: any older event may be this board's.
	card := readVariant(t, dojoStatus, func(is map[string]any) {
		is["projectItems"] = map[string]any{"totalCount": 0, "nodes": []any{}}
		is["timelineItems"] = map[string]any{"totalCount": 40, "nodes": []any{event("2026-11-01T09:00:00Z", other, "Todo", "Done")}}
	})
	if !card.HistoryTruncated {
		t.Error("not on the board, older events: not truncated")
	}
}

func TestReadHistoryUntracked(t *testing.T) {
	// fields.status names a custom field: GitHub records no status changes
	// for it, so the Status field's events are not its history.
	req := dojoStatus
	req.StatusField, req.StatusFieldID = "Size", "PVTSSF_lAHOBFGd5c4BrMYFzhpgLF4"
	card := readVariant(t, req, func(is map[string]any) { boardItem(is)["status"] = nil })
	if !card.HistoryUntracked || len(card.History) != 0 || card.HistoryTruncated {
		t.Errorf("untracked %v, history %v, truncated %v", card.HistoryUntracked, card.History, card.HistoryTruncated)
	}
	if card.Status() != "M" {
		t.Errorf("status %q, want the Size value M", card.Status())
	}
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
	switch {
	case c.Item == nil && c.BoardUnknown:
		p("item: board membership unknown: %s", c.BoardUnknownReason)
	case c.Item == nil:
		p("item: not on the board")
	default:
		p("item %s archived=%v created %s status=%q unknown=%v values truncated=%v",
			c.Item.ID, c.Item.Archived, ts(c.Item.CreatedAt), c.Item.Status, c.Item.StatusUnknown, c.Item.ValuesTruncated)
		for _, v := range c.Item.Values {
			p("  %s [%s] %q option=%s iteration=%s %s/%d field=%s", v.Field, v.Type, v.Text, v.OptionID, v.IterationID, v.StartDate, v.Duration, v.FieldID)
		}
	}
	p("history (truncated=%v untracked=%v):", c.HistoryTruncated, c.HistoryUntracked)
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
