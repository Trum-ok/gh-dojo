package board

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// inStatusQuery reads the board's issues in some statuses, filtered by
// GitHub through items' query (the board's own filter syntax, $filter
// from statusFilter): a request per 100 matching items, however large the
// board, with what release names of them.
var inStatusQuery = `query BoardItemsInStatus($owner: String!, $number: Int!, $status: String!, $filter: String!, $cursor: String) {
  repositoryOwner(login: $owner) {
    ... on User { projectV2(number: $number) { ...inStatus } }
    ... on Organization { projectV2(number: $number) { ...inStatus } }
  }
}

fragment inStatus on ProjectV2 {
  items(first: 100, after: $cursor, query: $filter) {
    totalCount
    pageInfo { hasNextPage endCursor }
    nodes {
      status: fieldValueByName(name: $status) {
        ... on ProjectV2ItemFieldSingleSelectValue { name }
      }
      content {
        __typename
        ... on Issue {
          number
          title
          state
          repository { nameWithOwner }
        }
      }
    }
  }
}`

// statusFilter is the board filter for the issues in statuses:
// is:issue status:Ready,"In review". A name with a space, comma, colon
// or quote is quoted.
func statusFilter(statuses []string) string {
	names := make([]string, len(statuses))
	for i, s := range statuses {
		if strings.ContainsAny(s, " \t,:\"") {
			s = `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
		}
		names[i] = s
	}
	return "is:issue status:" + strings.Join(names, ",")
}

// ReadInStatus reads the issues of the board s whose statusField is one
// of statuses (open and closed, archived items left out), in board order,
// filtered by GitHub: ceil(matching/100) requests, one as a rule. Only
// issues in exactly one of statuses are returned, whatever the filter
// matched; items the token cannot see are left out. Errors are
// ReadItemsWith's.
func ReadInStatus(ctx context.Context, q graph.Querier, s *Schema, statusField string, statuses []string) ([]Item, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	tq := &tolerant{q: q}
	vars := map[string]any{"owner": s.Owner, "number": s.Number, "status": statusField, "filter": statusFilter(statuses)}
	page, err := graph.Paginate(ctx, tq, inStatusQuery, vars, 0, func(r *itemsResponse) *graph.Connection[*itemNode] {
		if r.RepositoryOwner == nil || r.RepositoryOwner.ProjectV2 == nil {
			return nil
		}
		return &r.RepositoryOwner.ProjectV2.Items
	})
	if err != nil {
		if errors.Is(err, graph.ErrParentNotFound) || render.FromError(err).Code == render.ExitNotFound {
			return nil, notFound(s.Ref(), s.OwnerType, err)
		}
		return nil, err
	}
	var out []Item
	for _, n := range page.Nodes {
		it := n.item(false, "", "")
		if it.Type != ContentIssue || !slices.Contains(statuses, it.Status) {
			continue
		}
		if slices.ContainsFunc(out, func(o Item) bool { return o.Number == it.Number && strings.EqualFold(o.Repo, it.Repo) }) {
			continue // a board changed between pages repeats items
		}
		out = append(out, it)
	}
	return out, nil
}
