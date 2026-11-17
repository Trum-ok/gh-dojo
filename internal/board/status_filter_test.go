package board_test

import (
	"context"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// TestReadInStatus reads the hand-crafted board filtered by Ready and
// In review (a name with a space, quoted in the filter), two pages: only
// the issues in exactly those statuses are kept, of any repository, open
// or closed, each once; the issue in Done the filter matched, the pull
// request and the hidden item are left out.
func TestReadInStatus(t *testing.T) {
	srv := graphtest.New(t, fixture("items_in_status"))
	items, err := board.ReadInStatus(context.Background(), srv.Client(), dojoItemsBoard, "Status", []string{"Ready", "In review"})
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	testutil.Golden(t, "items/in_status", dumpItems(&board.Items{Items: items, TotalCount: len(items)}))
}

// TestReadInStatusUnspelled: a status the filter has no checked spelling
// for (a double quote) reads every item, the recorded board in one
// request, and still keeps only the issues in the statuses asked for.
func TestReadInStatusUnspelled(t *testing.T) {
	srv := graphtest.New(t, fixture("items_dojo"))
	items, err := board.ReadInStatus(context.Background(), srv.Client(), dojoItemsBoard, "Status", []string{"Ready", `Say "hi"`})
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
	if len(items) == 0 {
		t.Fatal("no issues in Ready")
	}
	for _, it := range items {
		if it.Type != board.ContentIssue || it.Status != "Ready" {
			t.Errorf("#%d: %s in %q, want an issue in Ready", it.Number, it.Type, it.Status)
		}
	}
}

// TestReadInStatusNone sends nothing for no statuses: no querier at all.
func TestReadInStatusNone(t *testing.T) {
	items, err := board.ReadInStatus(context.Background(), nil, dojoItemsBoard, "Status", nil)
	if err != nil || len(items) != 0 {
		t.Errorf("items %v, err %v; want none", items, err)
	}
}
