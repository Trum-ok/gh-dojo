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
	if len(card.UnresolvedBlockers(nil)) != 2 || card.BlockedBy.Truncated() {
		t.Errorf("unresolved blockers %v", card.UnresolvedBlockers(nil))
	}
}

// TestReadResolvedBlockers reads issue #13 of the gh-dojo repo as
// recorded: blocked by two open issues that are in Ready on the board, so
// done though open; in milestone v0.0 without a due date.
func TestReadResolvedBlockers(t *testing.T) {
	card, err := read(t, "card_13", 13, 0)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "card/card_13", dump(card))
	if len(card.UnresolvedBlockers(nil)) != 2 {
		t.Errorf("unresolved without resolved statuses %v", card.UnresolvedBlockers(nil))
	}
	if u := card.UnresolvedBlockers([]string{"Ready", "Done"}); len(u) != 0 {
		t.Errorf("unresolved blockers %v, want none: both are in Ready", u)
	}
	if m := card.Milestone; m == nil || m.Title == "" || !m.DueOn.IsZero() {
		t.Errorf("milestone %+v", m)
	}
}

// TestReadLinkStatus: a linked issue's place on the configured board,
// its status matched by the status field's ID when known. "Not on the
// board", "on the board, no status" and "unknown" are told apart.
func TestReadLinkStatus(t *testing.T) {
	// item is a project item of a linked issue with a status.
	item := func(project, field, status string) map[string]any {
		return map[string]any{
			"isArchived": false,
			"project":    map[string]any{"id": project},
			"status":     map[string]any{"name": status, "field": map[string]any{"id": field}},
		}
	}
	noStatus := map[string]any{"project": map[string]any{"id": dojoBoard.ID}, "status": nil}
	archived := item(dojoBoard.ID, statusID, "Ready")
	archived["isArchived"] = true
	other := item("PVT_other", "F_other", "Done")
	byName := dojoStatus
	byName.StatusFieldID = ""
	noBoard := dojoStatus
	noBoard.Board = nil
	type want struct {
		onBoard, archived bool
		status, unknown   string
	}
	tests := []struct {
		name  string
		req   issue.Request
		items []any
		total int // projectItems.totalCount; 0 means len(items)
		edit  func(is map[string]any)
		want  want
	}{
		{"this_board", dojoStatus, []any{other, item(dojoBoard.ID, statusID, "Ready")}, 0, nil, want{onBoard: true, status: "Ready"}},
		{"archived", dojoStatus, []any{archived}, 0, nil, want{onBoard: true, archived: true, status: "Ready"}},
		{"other_board_only", dojoStatus, []any{other}, 0, nil, want{}},
		{"no_items", dojoStatus, []any{}, 0, nil, want{}},
		{"no_status", dojoStatus, []any{noStatus}, 0, nil, want{onBoard: true}},
		// The board may be among the projects not read.
		{"more_projects", dojoStatus, []any{other}, 12, nil, want{unknown: "on 12 projects, only the first 1 read"}},
		{"more_projects_found", dojoStatus, []any{item(dojoBoard.ID, statusID, "Ready")}, 12, nil, want{onBoard: true, status: "Ready"}},
		{"withheld_project", dojoStatus, []any{nil, map[string]any{"project": nil, "status": nil}}, 0, nil,
			want{unknown: "GitHub withheld its projects from the token"}},
		// A field with the status field's old name but another ID: not the status.
		{"other_field", dojoStatus, []any{item(dojoBoard.ID, "PVTSSF_new", "Urgent")}, 0, nil,
			want{onBoard: true, unknown: "the board's status field was renamed since it was cached"}},
		{"other_field_by_name", byName, []any{item(dojoBoard.ID, "PVTSSF_new", "Urgent")}, 0, nil, want{onBoard: true, status: "Urgent"}},
		// The card's own item shows the name no longer finds the status
		// field, so a link's empty lookup by that name is not "no status".
		{"renamed_no_value", dojoStatus, []any{noStatus}, 0, func(is map[string]any) { renameStatus(is, "Stage") },
			want{onBoard: true, unknown: "the board's status field was renamed since it was cached"}},
		{"no_board", noBoard, []any{item(dojoBoard.ID, statusID, "Ready")}, 0, nil, want{unknown: "the board was not read"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card := readVariant(t, tt.req, func(is map[string]any) {
				total := tt.total
				if total == 0 {
					total = len(tt.items)
				}
				for _, dir := range []string{"blockedBy", "blocking"} {
					for _, l := range is[dir].(map[string]any)["nodes"].([]any) {
						l.(map[string]any)["projectItems"] = map[string]any{"totalCount": total, "nodes": tt.items}
					}
				}
				if tt.edit != nil {
					tt.edit(is)
				}
			})
			for _, l := range append(card.BlockedBy.Nodes, card.Blocking.Nodes...) {
				got := want{l.OnBoard, l.Archived, l.Status, l.StatusUnknown}
				if got != tt.want {
					t.Errorf("%s: %+v, want %+v", linkRef(l, dojo), got, tt.want)
				}
				// Unknown never resolves an open blocker.
				if tt.want.unknown != "" && !l.Closed() && l.Resolved([]string{"Ready", "Done", "Urgent"}) {
					t.Errorf("%s with unknown status is resolved", linkRef(l, dojo))
				}
			}
		})
	}
}

// TestReadLinkWithheldItems: GitHub withheld a linked issue's project
// items (an error below blockedBy.nodes.N.projectItems); its place on the
// board is unknown, not "not on the board".
func TestReadLinkWithheldItems(t *testing.T) {
	path := variant(t, func(is map[string]any) {
		for _, l := range is["blockedBy"].(map[string]any)["nodes"].([]any) {
			l.(map[string]any)["projectItems"] = nil
		}
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	resp := f["interactions"].([]any)[0].(map[string]any)["response"].(map[string]any)
	resp["errors"] = []any{map[string]any{
		"type": "FORBIDDEN", "message": "Resource not accessible by integration",
		"path": []any{"repository", "issueOrPullRequest", "blockedBy", "nodes", 0, "projectItems"},
	}}
	if data, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := graphtest.New(t, path)
	req := dojoStatus
	req.Number = 3
	card, err := issue.Read(context.Background(), srv.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !card.Incomplete {
		t.Error("card not incomplete")
	}
	for i, l := range card.BlockedBy.Nodes {
		wantUnknown := i == 0
		if (l.StatusUnknown != "") != wantUnknown || l.OnBoard {
			t.Errorf("%s: on board %v, unknown %q; want unknown %v", linkRef(l, dojo), l.OnBoard, l.StatusUnknown, wantUnknown)
		}
	}
}

// linkNode is a linked issue as cardQuery's link fragment reads it, in
// status on the gh-dojo board ("" for not on it).
func linkNode(repo string, number int, state, status string) map[string]any {
	items := []any{}
	if status != "" {
		items = append(items, map[string]any{
			"isArchived": false,
			"project":    map[string]any{"id": dojoBoard.ID},
			"status":     map[string]any{"name": status, "field": map[string]any{"id": statusID}},
		})
	}
	return map[string]any{
		"number": number, "title": fmt.Sprintf("Issue %d", number), "state": state,
		"repository":   map[string]any{"nameWithOwner": repo},
		"projectItems": map[string]any{"totalCount": len(items), "nodes": items},
	}
}

// TestReadSubIssues: the parent and the sub-issues are read as blockers
// are, each with its place on the board; a sub-issue withheld from the
// token is not read (Incomplete) and still counted.
func TestReadSubIssues(t *testing.T) {
	path := variant(t, func(is map[string]any) {
		is["parent"] = linkNode("Trum-ok/gh-dojo", 2, "OPEN", "In progress")
		is["subIssues"] = map[string]any{"totalCount": 5, "nodes": []any{
			linkNode("Trum-ok/gh-dojo", 10, "OPEN", "In progress"),
			linkNode("Trum-ok/gh-dojo", 11, "OPEN", "Ready"),
			linkNode("acme/api", 7, "CLOSED", ""),
			linkNode("Trum-ok/gh-dojo", 12, "OPEN", ""),
			nil,
		}}
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	resp := f["interactions"].([]any)[0].(map[string]any)["response"].(map[string]any)
	resp["errors"] = []any{map[string]any{
		"type": "FORBIDDEN", "message": "Resource not accessible by integration",
		"path": []any{"repository", "issueOrPullRequest", "subIssues", "nodes", 4},
	}}
	if data, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := graphtest.New(t, path)
	req := dojoStatus
	req.Number = 3
	card, err := issue.Read(context.Background(), srv.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "card/sub_issues", dump(card))
	if !card.Incomplete {
		t.Error("card not incomplete")
	}
	var got []string
	for _, l := range card.UnresolvedSubIssues([]string{"Ready", "Done"}) {
		got = append(got, linkRef(l, dojo))
	}
	if want := "#10 #12"; strings.Join(got, " ") != want {
		t.Errorf("unresolved sub-issues %v, want %s", got, want)
	}
	if n := card.SubIssues.TotalCount - len(card.SubIssues.Nodes); n != 1 {
		t.Errorf("sub-issues not read = %d, want 1", n)
	}
}

// TestReadSubIssuesWindow: AllSubIssues reads 100 sub-issues, GitHub's
// limit per issue, instead of the card's 20.
func TestReadSubIssuesWindow(t *testing.T) {
	path := variant(t, func(map[string]any) {})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	in := f["interactions"].([]any)[0].(map[string]any)
	q := in["query"].(string)
	if !strings.Contains(q, "subIssues(first: 20)") {
		t.Fatalf("card query has no subIssues(first: 20):\n%s", q)
	}
	in["query"] = strings.Replace(q, "subIssues(first: 20)", "subIssues(first: 100)", 1)
	if data, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := graphtest.New(t, path)
	req := dojoStatus
	req.Number, req.Lookup = 3, issue.Lookup{AllSubIssues: true}
	if _, err := issue.Read(context.Background(), srv.Client(), req); err != nil {
		t.Fatal(err)
	}
}

func TestLinkResolved(t *testing.T) {
	resolved := []string{"Ready", "Done"}
	tests := []struct {
		link issue.Link
		want bool
	}{
		{issue.Link{State: issue.StateOpen, Status: "In progress"}, false},
		{issue.Link{State: issue.StateOpen, Status: "Ready"}, true},
		{issue.Link{State: issue.StateOpen}, false},
		{issue.Link{State: issue.StateClosed, Status: "In progress"}, true},
		{issue.Link{State: issue.StateClosed}, true},
		// Unknown status: unresolved while open.
		{issue.Link{State: issue.StateOpen, Status: "Ready", StatusUnknown: "on 12 projects, only the first 10 read"}, false},
		{issue.Link{State: issue.StateClosed, StatusUnknown: "on 12 projects, only the first 10 read"}, true},
	}
	for _, tt := range tests {
		if got := tt.link.Resolved(resolved); got != tt.want {
			t.Errorf("%+v resolved = %v, want %v", tt.link, got, tt.want)
		}
		if got := tt.link.Resolved(nil); got != tt.link.Closed() {
			t.Errorf("%+v resolved without statuses = %v, want %v", tt.link, got, tt.link.Closed())
		}
	}
}

func TestReadMilestone(t *testing.T) {
	card := readVariant(t, dojoStatus, func(is map[string]any) {
		is["milestone"] = map[string]any{"title": "v0.1", "dueOn": "2026-12-01T00:00:00Z"}
	})
	if m := card.Milestone; m == nil || m.Title != "v0.1" || !m.DueOn.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("milestone %+v", m)
	}
	card = readVariant(t, dojoStatus, func(is map[string]any) { is["milestone"] = nil })
	if card.Milestone != nil {
		t.Errorf("milestone %+v, want nil", card.Milestone)
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

// linkRef names a linked issue relative to repo, as the dumps do.
func linkRef(l issue.Link, repo issue.Repo) string {
	return issue.Ref{Repo: l.Repo, Number: l.Number}.String(repo)
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
	if m := c.Milestone; m != nil {
		p("milestone %q due %s", m.Title, ts(m.DueOn))
	} else {
		p("milestone none")
	}
	for _, pair := range []struct {
		name string
		page []issue.Link
		n    int
	}{{"blocked by", c.BlockedBy.Nodes, c.BlockedBy.TotalCount}, {"blocking", c.Blocking.Nodes, c.Blocking.TotalCount},
		{"sub-issues", c.SubIssues.Nodes, c.SubIssues.TotalCount}} {
		p("%s (%d):", pair.name, pair.n)
		for _, l := range pair.page {
			p("  %s %s on board=%v archived=%v status=%q unknown=%q %q", linkRef(l, c.Repo), l.State, l.OnBoard, l.Archived, l.Status, l.StatusUnknown, l.Title)
		}
	}
	if l := c.Parent; l != nil {
		p("parent %s %s on board=%v archived=%v status=%q unknown=%q %q", linkRef(*l, c.Repo), l.State, l.OnBoard, l.Archived, l.Status, l.StatusUnknown, l.Title)
	} else {
		p("parent none")
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
