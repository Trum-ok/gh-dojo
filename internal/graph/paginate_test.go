package graph_test

import (
	"context"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
)

// itemsQuery pages through the gh-dojo board 5 items at a time, so the
// recorded fixture has several pages.
const itemsQuery = `query BoardItems($owner: String!, $number: Int!, $cursor: String) {
  user(login: $owner) {
    projectV2(number: $number) {
      items(first: 5, after: $cursor) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes { id type }
      }
    }
  }
}`

type item struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type itemsResponse struct {
	User *struct {
		ProjectV2 *struct {
			Items graph.Connection[item] `json:"items"`
		} `json:"projectV2"`
	} `json:"user"`
}

func items(r *itemsResponse) *graph.Connection[item] {
	if r.User == nil || r.User.ProjectV2 == nil {
		return nil
	}
	return &r.User.ProjectV2.Items
}

var boardVars = map[string]any{"owner": "Trum-ok", "number": 5}

func TestPaginateAll(t *testing.T) {
	srv := graphtest.New(t, fixture("board_items"))
	page, err := graph.Paginate(context.Background(), srv.Client(), itemsQuery, boardVars, 0, items)
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalCount < 6 {
		t.Fatalf("fixture has %d items; need more than one page", page.TotalCount)
	}
	if len(page.Nodes) != page.TotalCount || page.Truncated() {
		t.Errorf("collected %d of %d, truncated=%v; want all", len(page.Nodes), page.TotalCount, page.Truncated())
	}
	if want := (page.TotalCount + 4) / 5; srv.Requests() != want {
		t.Errorf("requests = %d, want %d pages", srv.Requests(), want)
	}
	seen := map[string]bool{}
	for _, n := range page.Nodes {
		if n.ID == "" || seen[n.ID] {
			t.Errorf("bad or duplicate node %+v", n)
		}
		seen[n.ID] = true
	}
	if _, ok := boardVars["cursor"]; ok {
		t.Error("Paginate modified the caller's vars")
	}
}

func TestPaginateLimitKeepsTotalCount(t *testing.T) {
	srv := graphtest.New(t, fixture("board_items_first_pages"))
	page, err := graph.Paginate(context.Background(), srv.Client(), itemsQuery, boardVars, 7, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Nodes) != 7 {
		t.Errorf("collected %d nodes, want 7", len(page.Nodes))
	}
	if !page.Truncated() || page.TotalCount <= 7 {
		t.Errorf("TotalCount = %d, Truncated = %v; want the full count and truncated", page.TotalCount, page.Truncated())
	}
	if srv.Requests() != 2 {
		t.Errorf("requests = %d, want 2", srv.Requests())
	}
}

func TestPaginateMissingParent(t *testing.T) {
	srv := graphtest.New(t, fixture("board_items_missing"))
	page, err := graph.Paginate(context.Background(), srv.Client(), itemsQuery, boardVars, 0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Nodes) != 0 || page.TotalCount != 0 {
		t.Errorf("page = %+v, want empty", page)
	}
}
