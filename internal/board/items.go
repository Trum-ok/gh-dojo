package board

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Windows of itemsQuery: how many labels and assignees are read per issue.
const (
	LabelsWindow    = 100
	AssigneesWindow = 10
)

// itemsQuery reads one page of the board's items in board order (POSITION,
// archived items left out) with what `list` prints, the viewer's login for
// --mine and the status field's ID (to notice a cached schema that is out of
// date), so a whole board takes ceil(items/100) requests. Windows: labels
// 100 and assignees 10 per issue, each with its totalCount.
const itemsQuery = `query BoardItems($owner: String!, $number: Int!, $status: String!, $cursor: String) {
  viewer { login }
  repositoryOwner(login: $owner) {
    ... on User { projectV2(number: $number) { ...items } }
    ... on Organization { projectV2(number: $number) { ...items } }
  }
}

fragment items on ProjectV2 {
  field(name: $status) { ... on ProjectV2SingleSelectField { id } }
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
          labels(first: 100) { totalCount nodes { name } }
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

// sprintItemsQuery is itemsQuery that also reads each item's iteration of
// the field named $sprint (ReadItemsSprint).
var sprintItemsQuery = strings.NewReplacer(
	"$status: String!, $cursor", "$status: String!, $sprint: String!, $cursor",
	"      content {\n", "      sprint: fieldValueByName(name: $sprint) {\n"+
		"        ... on ProjectV2ItemFieldIterationValue { iterationId title }\n"+
		"      }\n      content {\n",
).Replace(itemsQuery)

// Item is a board item. Only issues have the issue fields set.
type Item struct {
	Type   string // ContentIssue, ContentPullRequest, ContentDraftIssue or ContentHidden
	Status string // option name of the status field; "" when unset
	// SprintID and Sprint are the ID and title of the item's iteration of
	// the sprint field, read by ReadItemsSprint only; "" when unset.
	SprintID, Sprint string

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
	// StatusFieldID is the ID of the single-select field named like the
	// status field, "" when the board has no such field. A cached schema
	// with another ID is out of date.
	StatusFieldID string
	// Items are every non-archived item in board order, each issue once.
	// Items the token cannot see are there as ContentHidden.
	Items []Item
	// TotalCount is the item count GitHub reported on the last page. It
	// differs from len(Items) when the board changed between pages.
	TotalCount int
	// Incomplete is set when GitHub withheld part of a visible item, such as
	// its labels; the item is listed with what could be read.
	Incomplete bool
}

// ReadItems reads every non-archived item of the board s in board order,
// 100 per request, with statusField's value. A board that no longer exists
// is exit code 3; network and auth problems keep graph's codes.
func ReadItems(ctx context.Context, q graph.Querier, s *Schema, statusField string) (*Items, error) {
	return ReadItemsSprint(ctx, q, s, statusField, "")
}

// ReadItemsSprint is ReadItems that also reads each item's iteration of
// the field named sprintField (Item.SprintID, Item.Sprint), in the same
// requests; "" reads none, as ReadItems.
func ReadItemsSprint(ctx context.Context, q graph.Querier, s *Schema, statusField, sprintField string) (*Items, error) {
	out := &Items{}
	tq := &tolerant{q: q}
	vars := map[string]any{"owner": s.Owner, "number": s.Number, "status": statusField}
	query := itemsQuery
	if sprintField != "" {
		query = sprintItemsQuery
		vars["sprint"] = sprintField
	}
	first := true
	page, err := graph.Paginate(ctx, tq, query, vars, 0, func(r *itemsResponse) *graph.Connection[*itemJSON] {
		if r.Viewer != nil && out.Viewer == "" {
			out.Viewer = r.Viewer.Login
		}
		if r.RepositoryOwner == nil || r.RepositoryOwner.ProjectV2 == nil {
			return nil
		}
		p := r.RepositoryOwner.ProjectV2
		if first && p.Field != nil {
			out.StatusFieldID = p.Field.ID
		}
		first = false
		return &p.Items
	})
	if err != nil {
		if errors.Is(err, graph.ErrParentNotFound) || render.FromError(err).Code == render.ExitNotFound {
			return nil, notFound(s.Ref(), s.OwnerType, err)
		}
		return nil, err
	}
	out.Incomplete = tq.partial
	out.TotalCount = page.TotalCount
	out.Items = make([]Item, 0, len(page.Nodes))
	seen := map[string]bool{} // a board changed between pages repeats items
	for _, n := range page.Nodes {
		it := n.item()
		if it.Type == ContentIssue {
			key := it.Repo + "#" + strconv.Itoa(it.Number)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

// Suggest returns the value in allowed closest to name, as the lookup
// errors do: a case-insensitive match, else the single closest one within
// edit distance 2; "" when there is none.
func Suggest(name string, allowed []string) string { return suggest(name, allowed) }

type itemsResponse struct {
	Viewer *struct {
		Login string `json:"login"`
	} `json:"viewer"`
	RepositoryOwner *struct {
		ProjectV2 *struct {
			Field *struct {
				ID string `json:"id"`
			} `json:"field"`
			Items graph.Connection[*itemJSON] `json:"items"`
		} `json:"projectV2"`
	} `json:"repositoryOwner"`
}

type itemJSON struct {
	Status *struct {
		Name string `json:"name"`
	} `json:"status"`
	Sprint *struct {
		IterationID string `json:"iterationId"`
		Title       string `json:"title"`
	} `json:"sprint"`
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

// item converts a node; a null node or content is ContentHidden.
func (n *itemJSON) item() Item {
	var it Item
	if n == nil {
		return it
	}
	if n.Status != nil {
		it.Status = n.Status.Name
	}
	if n.Sprint != nil {
		it.SprintID, it.Sprint = n.Sprint.IterationID, n.Sprint.Title
	}
	c := n.Content
	if c == nil {
		return it
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
// FORBIDDEN inside items (e.g. an issue in a repository the token cannot
// see): the rest of the page is accurate. A withheld item or content reads
// as ContentHidden; anything withheld below the content sets partial.
type tolerant struct {
	q       graph.Querier
	partial bool
}

func (t *tolerant) Query(ctx context.Context, query string, vars map[string]any, out any) error {
	err := t.q.Query(ctx, query, vars, out)
	if err == nil {
		return nil
	}
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil || len(gqlErr.Errors) == 0 {
		return err
	}
	partial := false
	for _, e := range gqlErr.Errors {
		p := e.Path
		if len(p) < 5 || p[0] != "repositoryOwner" || p[1] != "projectV2" || p[2] != "items" || p[3] != "nodes" {
			return err
		}
		if e.Type != "NOT_FOUND" && e.Type != "FORBIDDEN" {
			return err
		}
		// nodes.<i> and nodes.<i>.content are counted as hidden items.
		if len(p) > 6 || (len(p) == 6 && p[5] != "content") {
			partial = true
		}
	}
	t.partial = t.partial || partial
	return nil
}
