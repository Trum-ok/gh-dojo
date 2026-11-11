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

func listProjects(t *testing.T, owner string, files ...string) (*board.Projects, *graphtest.Server, error) {
	t.Helper()
	srv := graphtest.New(t, files...)
	p, err := board.ListProjects(context.Background(), srv.Client(), owner)
	return p, srv, err
}

func TestListProjects(t *testing.T) {
	tests := []struct {
		name, owner, fixture string
		pageSize, requests   int
	}{
		{"user", "Trum-ok", "projects_user", 0, 1},
		// The same projects, two per request: three recorded pages.
		{"user_pages", "trum-ok", "projects_user_pages", 2, 3},
		{"org", "github", "projects_org", 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.pageSize > 0 {
				board.SetProjectsPageSize(t, tt.pageSize)
			}
			p, srv, err := listProjects(t, tt.owner, fixture(tt.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if n := srv.Requests(); n != tt.requests {
				t.Errorf("requests = %d, want %d", n, tt.requests)
			}
			if len(p.Projects) != p.TotalCount {
				t.Errorf("%d projects, total %d", len(p.Projects), p.TotalCount)
			}
			testutil.Golden(t, "projects/"+tt.name, dumpProjects(p))
		})
	}
}

func TestListProjectsOwnerNotFound(t *testing.T) {
	_, _, err := listProjects(t, "no-such-user-xyz-123987", fixture("projects_owner_not_found"))
	assertError(t, err, render.ExitNotFound, "errors/owner_not_found")
}

func TestListProjectsInvalidOwner(t *testing.T) {
	_, err := board.ListProjects(context.Background(), nil, "../etc")
	if render.FromError(err).Code != render.ExitUsage {
		t.Errorf("err = %v, want a usage error", err)
	}
}

func dumpProjects(p *board.Projects) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %d projects\n", p.OwnerType, p.Owner, p.TotalCount)
	for _, pr := range p.Projects {
		fmt.Fprintf(&b, "  %d %q items=%d closed=%v\n", pr.Number, pr.Title, pr.Items, pr.Closed)
	}
	return []byte(b.String())
}
