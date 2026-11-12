package board_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// dojoItemsBoard is the recorded gh-dojo board (user Trum-ok, project 5); the
// items reader needs only its owner and number.
var dojoItemsBoard = &board.Schema{Owner: "Trum-ok", OwnerType: board.OwnerUser, Number: 5}

func readItems(t *testing.T, s *board.Schema, files ...string) (*board.Items, *graphtest.Server, error) {
	t.Helper()
	srv := graphtest.New(t, files...)
	items, err := board.ReadItems(context.Background(), srv.Client(), s, "Status")
	return items, srv, err
}

// TestItemsRecorded reads the gh-dojo board as recorded: one page.
func TestItemsRecorded(t *testing.T) {
	items, srv, err := readItems(t, dojoItemsBoard, fixture("items_dojo"))
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
	testutil.Golden(t, "items/dojo", dumpItems(items))
	if items.Viewer != "Trum-ok" || items.Incomplete {
		t.Errorf("viewer %q, incomplete %v", items.Viewer, items.Incomplete)
	}
}

// TestItemsBlockersRecorded reads the gh-dojo board as recorded, with each
// issue's blockers (next): still one request.
func TestItemsBlockersRecorded(t *testing.T) {
	srv := graphtest.New(t, fixture("items_dojo_blockers"))
	items, err := board.ReadItemsWith(context.Background(), srv.Client(), dojoItemsBoard, "Status", board.ItemsOptions{Blockers: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
	testutil.Golden(t, "items/dojo_blockers", dumpItems(items))
}

// TestItemsPages reads a hand-crafted board of two pages with a draft, a
// pull request, an item whose content the token cannot see, a null item,
// assignees withheld below one issue's content, an issue repeated on page 2
// and an issue with more labels and assignees than the windows.
func TestItemsPages(t *testing.T) {
	items, srv, err := readItems(t, dojoItemsBoard, fixture("items_two_pages"))
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	// The repeat is dropped, the null item and the hidden content are
	// hidden items, and only the withheld assignees make it incomplete.
	if len(items.Items) != 13 || items.TotalCount != 13 || !items.Incomplete {
		t.Errorf("items %d, total %d, incomplete %v; want 13, 13, true", len(items.Items), items.TotalCount, items.Incomplete)
	}
	testutil.Golden(t, "items/two_pages", dumpItems(items))
}

func TestItemsBoardNotFound(t *testing.T) {
	gone := &board.Schema{Owner: "Trum-ok", OwnerType: board.OwnerUser, Number: 99}
	_, _, err := readItems(t, gone, fixture("items_board_not_found"))
	if e := render.FromError(err); e.Code != render.ExitNotFound || !strings.Contains(e.Text(), "board Trum-ok/99 not found") {
		t.Errorf("err = %v (code %d), want board not found", err, e.Code)
	}
}

func dumpItems(items *board.Items) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "viewer: %s\nstatus field: %s\ntotal: %d\nincomplete: %v\n",
		items.Viewer, items.StatusFieldID, items.TotalCount, items.Incomplete)
	for _, it := range items.Items {
		if it.Type != board.ContentIssue {
			fmt.Fprintf(&b, "- %q status=%q\n", it.Type, it.Status)
			continue
		}
		fmt.Fprintf(&b, "- %s#%d %s status=%q %q labels=%v/%d assignees=%v/%d\n",
			it.Repo, it.Number, it.State, it.Status, it.Title,
			it.Labels.Nodes, it.Labels.TotalCount, it.Assignees.Nodes, it.Assignees.TotalCount)
		if it.BlockedBy.TotalCount > 0 {
			fmt.Fprintf(&b, "  blocked by %d:", it.BlockedBy.TotalCount)
			for _, bl := range it.BlockedBy.Nodes {
				fmt.Fprintf(&b, " %s#%d %s", bl.Repo, bl.Number, bl.State)
			}
			b.WriteString("\n")
		}
	}
	return []byte(b.String())
}
