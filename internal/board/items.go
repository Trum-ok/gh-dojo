package board

import (
	"context"
	"errors"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// ItemsPageSize is how many board items one request reads.
const ItemsPageSize = 100

// itemsQuery reads one page of the board's items in board order (POSITION,
// archived items left out) with what `list` prints, and the viewer's login
// for --mine, so a whole board takes ceil(items/100) requests. Windows:
// labels 20 and assignees 10 per issue, each with its totalCount.
const itemsQuery = `query BoardItems($owner: String!, $number: Int!, $status: String!, $cursor: String) {
  viewer { login }
  repositoryOwner(login: $owner) {
    ... on User { projectV2(number: $number) { ...items } }
    ... on Organization { projectV2(number: $number) { ...items } }
  }
}

fragment items on ProjectV2 {
  items(first: 100, after: $cursor) {
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
          labels(first: 20) { totalCount nodes { name } }
          assignees(first: 10) { totalCount nodes { login } }
        }
      }
    }
  }
}`

// Item content types, as GitHub names them. ContentHidden is an item whose
// content the token cannot see.
const (
	ContentIssue       = "Issue"
	ContentPullRequest = "PullRequest"
	ContentDraftIssue  = "DraftIssue"
	ContentHidden      = ""
)

// Item is a board item. Only issues have the issue fields set.
type Item struct {
	Type   string // ContentIssue, ContentPullRequest, ContentDraftIssue or ContentHidden
	Status string // option name of the status field; "" when unset

	Repo      string // "owner/name"
	Number    int
	Title     string
	State     string // "OPEN" or "CLOSED"
	Labels    graph.Page[string]
	Assignees graph.Page[string]
}

// Items is the whole board, read by ReadItems.
type Items struct {
	// Viewer is the login of the token's user.
	Viewer string
	// Items are every non-archived item in board order.
	Items []Item
	// Incomplete is set when GitHub withheld some items' content (see
	// ContentHidden); the rest is accurate.
	Incomplete bool
}

// ReadItems reads every non-archived item of the board s in board order,
// 100 per request, with statusField's value. A board that no longer exists
// is exit code 3; network and auth problems keep graph's codes.
func ReadItems(ctx context.Context, q graph.Querier, s *Schema, statusField string) (*Items, error) {
	out := &Items{}
	tq := &tolerant{q: q}
	vars := map[string]any{"owner": s.Owner, "number": s.Number, "status": statusField}
	page, err := graph.Paginate(ctx, tq, itemsQuery, vars, 0, func(r *itemsResponse) *graph.Connection[*itemJSON] {
		if r.Viewer != nil && out.Viewer == "" {
			out.Viewer = r.Viewer.Login
		}
		if r.RepositoryOwner == nil || r.RepositoryOwner.ProjectV2 == nil {
			return nil
		}
		return &r.RepositoryOwner.ProjectV2.Items
	})
	if err != nil {
		if errors.Is(err, graph.ErrParentNotFound) || render.FromError(err).Code == render.ExitNotFound {
			return nil, notFound(s.Ref(), err)
		}
		return nil, err
	}
	out.Incomplete = tq.incomplete
	out.Items = make([]Item, 0, len(page.Nodes))
	for _, n := range page.Nodes {
		if n == nil {
			out.Incomplete = true
			continue
		}
		out.Items = append(out.Items, n.item())
	}
	return out, nil
}

type itemsResponse struct {
	Viewer *struct {
		Login string `json:"login"`
	} `json:"viewer"`
	RepositoryOwner *struct {
		ProjectV2 *struct {
			Items graph.Connection[*itemJSON] `json:"items"`
		} `json:"projectV2"`
	} `json:"repositoryOwner"`
}

type itemJSON struct {
	Status *struct {
		Name string `json:"name"`
	} `json:"status"`
	Content *struct {
		Typename   string `json:"__typename"`
		Number     int    `json:"number"`
		Title      string `json:"title"`
		State      string `json:"state"`
		Repository *struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
		Labels struct {
			TotalCount int `json:"totalCount"`
			Nodes      []*struct {
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"labels"`
		Assignees struct {
			TotalCount int `json:"totalCount"`
			Nodes      []*struct {
				Login string `json:"login"`
			} `json:"nodes"`
		} `json:"assignees"`
	} `json:"content"`
}

func (n *itemJSON) item() Item {
	var it Item
	if n.Status != nil {
		it.Status = n.Status.Name
	}
	c := n.Content
	if c == nil {
		return it // ContentHidden
	}
	it.Type = c.Typename
	if it.Type != ContentIssue {
		return it
	}
	it.Number, it.Title, it.State = c.Number, c.Title, c.State
	if c.Repository != nil {
		it.Repo = c.Repository.NameWithOwner
	}
	it.Labels.TotalCount = c.Labels.TotalCount
	for _, l := range c.Labels.Nodes {
		if l != nil {
			it.Labels.Nodes = append(it.Labels.Nodes, l.Name)
		}
	}
	it.Assignees.TotalCount = c.Assignees.TotalCount
	for _, a := range c.Assignees.Nodes {
		if a != nil {
			it.Assignees.Nodes = append(it.Assignees.Nodes, a.Login)
		}
	}
	return it
}

// tolerant passes on a page whose only GraphQL errors are NOT_FOUND or
// FORBIDDEN below an item (e.g. an issue in a repository the token cannot
// see): the rest of the page is accurate, and the item reads as hidden.
type tolerant struct {
	q          graph.Querier
	incomplete bool
}

func (t *tolerant) Query(ctx context.Context, query string, vars map[string]any, out any) error {
	err := t.q.Query(ctx, query, vars, out)
	if err != nil && onlyItemErrors(err) {
		t.incomplete = true
		return nil
	}
	return err
}

// onlyItemErrors reports whether every GraphQL error points inside an item
// (repositoryOwner.projectV2.items.nodes.<i>...).
func onlyItemErrors(err error) bool {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil || len(gqlErr.Errors) == 0 {
		return false
	}
	for _, e := range gqlErr.Errors {
		p := e.Path
		if len(p) < 5 || p[0] != "repositoryOwner" || p[1] != "projectV2" || p[2] != "items" || p[3] != "nodes" {
			return false
		}
		switch e.Type {
		case "NOT_FOUND", "FORBIDDEN":
		default:
			return false
		}
	}
	return true
}
