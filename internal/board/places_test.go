package board_test

import (
	"context"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
)

// TestPlacesRecorded reads the place on the gh-dojo board of two of its
// issues, as recorded: #1 open in Ready, #37 closed in Done; one request.
func TestPlacesRecorded(t *testing.T) {
	srv := graphtest.New(t, fixture("places_dojo"))
	s := &board.Schema{Owner: "Trum-ok", OwnerType: board.OwnerUser, Number: 5, ID: "PVT_kwHOBFGd5c4BrMYF"}
	keys := []board.IssueKey{{Repo: "Trum-ok/gh-dojo", Number: 1}, {Repo: "Trum-ok/gh-dojo", Number: 37}}
	got, err := board.ReadPlaces(context.Background(), srv.Client(), s, "Status", "PVTSSF_lAHOBFGd5c4BrMYFzhpgKzQ", keys)
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
	want := map[board.IssueKey]board.Place{
		keys[0]: {State: "OPEN", OnBoard: true, Status: "Ready"},
		keys[1]: {State: "CLOSED", OnBoard: true, Status: "Done"},
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: %+v, want %+v", k, got[k], w)
		}
	}
}

// TestPlacesNone sends nothing for no issues.
func TestPlacesNone(t *testing.T) {
	got, err := board.ReadPlaces(context.Background(), nil, &board.Schema{}, "Status", "", nil)
	if err != nil || len(got) != 0 {
		t.Errorf("ReadPlaces(none) = %v, %v", got, err)
	}
}
