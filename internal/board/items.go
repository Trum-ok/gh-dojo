package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Windows of itemsQuery: how many labels and assignees are read per issue,
// and blockers with ItemsOptions.Blockers. GitHub allows an issue at most
// 50 blockers, so BlockersWindow reads them all.
const (
	LabelsWindow    = 100
	AssigneesWindow = 10
	BlockersWindow  = 50
)

// itemsQuery reads one page of the board's items in board order (POSITION,
// archived items left out) with what `list` prints, the viewer's login for
// --mine and the status field's ID (to notice a cached schema that is out of
// date), so a whole board takes ceil(items/100) requests. Windows: labels
// and assignees per issue, each with its totalCount.
var itemsQuery = `query BoardItems($owner: String!, $number: Int!, $status: String!, $cursor: String) {
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
          labels(first: ` + strconv.Itoa(LabelsWindow) + `) { totalCount nodes { name } }
` + assigneesLine + `        }
      }
    }
  }
}`

// assigneesLine is the line of itemsQuery that reads an issue's assignees.
var assigneesLine = "          assignees(first: " + strconv.Itoa(AssigneesWindow) + ") { totalCount nodes { login } }\n"

// Item content types, as GitHub names them. ContentHidden is an item whose
// content the token cannot see.
const (
	ContentIssue       = "Issue"
	ContentPullRequest = "PullRequest"
	ContentDraftIssue  = "DraftIssue"
	ContentHidden      = ""
)

// sprintItems makes itemsQuery also read each item's iteration of the
// field named $sprint (ItemsOptions.Sprint).
var sprintItems = strings.NewReplacer(
	"$status: String!, $cursor", "$status: String!, $sprint: String!, $cursor",
	"      content {\n", "      sprint: fieldValueByName(name: $sprint) {\n"+
		"        ... on ProjectV2ItemFieldIterationValue { iterationId title }\n"+
		"      }\n      content {\n",
)

// blockersLine reads an issue's blockers (ItemsOptions.Blockers): their
// number, state and repository. It is one more connection per issue, so
// the request count stays ceil(items/100).
var blockersLine = "          blockedBy(first: " + strconv.Itoa(BlockersWindow) + ") { totalCount nodes { number state repository { nameWithOwner } } }\n"

// blockerItems makes itemsQuery also read each issue's blockers, not their
// board status, which the caller looks up among the board's items.
var blockerItems = strings.NewReplacer(assigneesLine, assigneesLine+blockersLine)

// placeItems makes itemsQuery also read each issue's blockers with their
// place on the board (ItemsOptions.Places), as ReadPlaces does: the
// caller needs no other items to resolve them.
var placeItems = strings.NewReplacer(assigneesLine, assigneesLine+
	strings.Replace(blockersLine, "repository { nameWithOwner } }", "repository { nameWithOwner }\n"+
		"            projectItems(first: "+strconv.Itoa(projectsWindow)+") {\n"+
		"              totalCount\n"+
		"              nodes {\n"+
		"                isArchived\n"+
		"                project { id }\n"+
		"                status: fieldValueByName(name: $status) {\n"+
		"                  ... on ProjectV2ItemFieldSingleSelectValue { name field { ... on ProjectV2FieldCommon { id } } }\n"+
		"                }\n"+
		"              }\n"+
		"            }\n"+
		"          }", 1))

// filterItems makes itemsQuery read only the items $filter matches
// (ItemsOptions.Filter).
var filterItems = strings.NewReplacer(
	"$cursor: String)", "$cursor: String, $filter: String!)",
	"items(first: 100, after: $cursor)", "items(first: 100, after: $cursor, query: $filter)",
)

// ItemsOptions are what ReadItemsWith reads beyond the items' status,
// labels and assignees, in the same requests; the zero value reads none.
type ItemsOptions struct {
	// Sprint is the name of the sprint field: each item's iteration of it
	// is read (Item.SprintID, Item.Sprint); "" reads none.
	Sprint string
	// Blockers reads each issue's blockers (Item.BlockedBy).
	Blockers bool
	// Places also reads each blocker's place on the board (Blocker.Place),
	// so the items read need not include the blockers. Needs Blockers.
	Places bool
	// Filter reads only the items it matches (see Filter), in board
	// order; "" reads every item.
	Filter string
	// Counts are filters whose item counts are read in the same requests
	// (Items.Counts, in the same order), with no items: what Filter left
	// out can still be counted (principle 7).
	Counts []string
}

// query is itemsQuery with what o reads.
func (o ItemsOptions) query() string {
	q := itemsQuery
	if o.Sprint != "" {
		q = sprintItems.Replace(q)
	}
	switch {
	case o.Places:
		q = placeItems.Replace(q)
	case o.Blockers:
		q = blockerItems.Replace(q)
	}
	if o.Filter != "" {
		q = filterItems.Replace(q)
	}
	if len(o.Counts) > 0 {
		var decls, fields strings.Builder
		for i := range o.Counts {
			fmt.Fprintf(&decls, ", $count%d: String!", i)
			fmt.Fprintf(&fields, "  count%d: items(first: 0, query: $count%d) { totalCount }\n", i, i)
		}
		q = strings.Replace(q, "$cursor: String", "$cursor: String"+decls.String(), 1)
		q = strings.Replace(q, "  items(first: 100", fields.String()+"  items(first: 100", 1)
	}
	return q
}

// vars are the variables of o's query beyond the board's and the status
// field's.
func (o ItemsOptions) vars(vars map[string]any) {
	if o.Sprint != "" {
		vars["sprint"] = o.Sprint
	}
	if o.Filter != "" {
		vars["filter"] = o.Filter
	}
	for i, c := range o.Counts {
		vars["count"+strconv.Itoa(i)] = c
	}
}

// Item is a board item. Only issues have the issue fields set.
type Item struct {
	Type   string // ContentIssue, ContentPullRequest, ContentDraftIssue or ContentHidden
	Status string // option name of the status field; "" when unset
	// SprintID and Sprint are the ID and title of the item's iteration of
	// the sprint field, read with ItemsOptions.Sprint only; "" when unset.
	SprintID, Sprint string

	Repo      string // "owner/name"
	Number    int
	Title     string
	State     string // "OPEN" or "CLOSED"
	Labels    graph.Page[string]
	Assignees graph.Page[string]
	// BlockedBy are the issues blocking it, read with ItemsOptions.Blockers
	// only, up to BlockersWindow. TotalCount also counts the blockers the
	// token cannot see, which are not in Nodes.
	BlockedBy graph.Page[Blocker]
	// Incomplete is set when GitHub withheld part of the item, such as its
	// labels or a blocker (Items.Incomplete for any item).
	Incomplete bool
}

// Blocker is an issue that blocks a board item's issue. Its board status
// is read with ItemsOptions.Places only: else look it up among the
// board's items.
type Blocker struct {
	Repo   string // "owner/name"
	Number int
	State  string // "OPEN" or "CLOSED"
	// Place is its place on the board, as ReadPlaces reads it; nil
	// without ItemsOptions.Places.
	Place *Place
}

// Items is the board as ReadItemsWith read it: every item, or those
// ItemsOptions.Filter matches.
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
	// Counts are the item counts of ItemsOptions.Counts, in the same
	// order, as GitHub reported them on the first page.
	Counts []int
}

// ReadItemsWith reads the non-archived items of the board s in board
// order, 100 per request, with statusField's value and what o asks for:
// every item, or those o.Filter matches. A board that no longer exists is
// exit code 3; network and auth problems keep graph's codes.
func ReadItemsWith(ctx context.Context, q graph.Querier, s *Schema, statusField string, o ItemsOptions) (*Items, error) {
	out := &Items{}
	tq := &tolerant{q: q}
	vars := map[string]any{"owner": s.Owner, "number": s.Number, "status": statusField}
	o.vars(vars)
	query := o.query()
	first := true
	page, err := graph.Paginate(ctx, tq, query, vars, 0, func(r *itemsResponse) *graph.Connection[*itemNode] {
		if r.Viewer != nil && out.Viewer == "" {
			out.Viewer = r.Viewer.Login
		}
		if r.RepositoryOwner == nil || r.RepositoryOwner.ProjectV2 == nil {
			return nil
		}
		p := r.RepositoryOwner.ProjectV2
		if first {
			if p.Field != nil {
				out.StatusFieldID = p.Field.ID
			}
			for i := range o.Counts {
				out.Counts = append(out.Counts, p.counts["count"+strconv.Itoa(i)])
			}
		}
		first = false
		for _, i := range tq.page {
			if i < len(p.Items.Nodes) && p.Items.Nodes[i] != nil {
				p.Items.Nodes[i].partial = true
			}
		}
		for i, bs := range tq.places {
			if i < len(p.Items.Nodes) && p.Items.Nodes[i] != nil {
				p.Items.Nodes[i].withheldPlaces = bs
			}
		}
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
		it := n.item(o.Places, s.ID, out.StatusFieldID)
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
		ProjectV2 *itemsProject `json:"projectV2"`
	} `json:"repositoryOwner"`
}

// itemsProject is the board in itemsQuery's response.
type itemsProject struct {
	Field *struct {
		ID string `json:"id"`
	} `json:"field"`
	Items graph.Connection[*itemNode] `json:"items"`
	// counts are the totalCount of each countN alias, by alias.
	counts map[string]int
}

func (p *itemsProject) UnmarshalJSON(data []byte) error {
	type plain itemsProject
	if err := json.Unmarshal(data, (*plain)(p)); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for k, v := range fields {
		if !strings.HasPrefix(k, "count") {
			continue
		}
		var c *struct {
			TotalCount int `json:"totalCount"`
		}
		if err := json.Unmarshal(v, &c); err != nil {
			return err
		}
		if c != nil {
			if p.counts == nil {
				p.counts = map[string]int{}
			}
			p.counts[k] = c.TotalCount
		}
	}
	return nil
}

type itemNode struct {
	partial bool // GitHub withheld part of it (tolerant)
	// withheldPlaces are the indices of the blockers whose projects
	// GitHub withheld in part (tolerant).
	withheldPlaces []int

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
		BlockedBy struct {
			TotalCount int            `json:"totalCount"`
			Nodes      []*blockerNode `json:"nodes"`
		} `json:"blockedBy"`
	} `json:"content"`
}

// blockerNode is a blocker; its state and projects are a placeNode, the
// projects read with ItemsOptions.Places only.
type blockerNode struct {
	Number     int `json:"number"`
	Repository *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	placeNode
}

// item converts a node; a null node or content is ContentHidden. places
// says the blockers' places were read, on the board boardID whose status
// field is statusFieldID.
func (n *itemNode) item(places bool, boardID, statusFieldID string) Item {
	var it Item
	if n == nil {
		return it
	}
	it.Incomplete = n.partial
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
	it.BlockedBy.TotalCount = c.BlockedBy.TotalCount
	for j, b := range c.BlockedBy.Nodes {
		if b == nil || b.Repository == nil {
			continue // withheld from the token: counted, not read
		}
		bl := Blocker{Repo: b.Repository.NameWithOwner, Number: b.Number, State: b.State}
		if places {
			p := b.place(boardID, statusFieldID, slices.Contains(n.withheldPlaces, j))
			bl.Place = &p
		}
		it.BlockedBy.Nodes = append(it.BlockedBy.Nodes, bl)
	}
	return it
}

// tolerant passes on a page whose only GraphQL errors are NOT_FOUND or
// FORBIDDEN inside items (e.g. an issue in a repository the token cannot
// see): the rest of the page is accurate. A withheld item or content reads
// as ContentHidden; anything withheld below the content sets partial.
//
// page holds the indices of the nodes of the last page read with parts
// withheld, for Item.Incomplete; places, by node index, the indices of
// the blockers whose projects were withheld in part, for Blocker.Place.
type tolerant struct {
	q       graph.Querier
	partial bool
	page    []int
	places  map[int][]int
}

func (t *tolerant) Query(ctx context.Context, query string, vars map[string]any, out any) error {
	t.page = t.page[:0]
	t.places = nil
	err := t.q.Query(ctx, query, vars, out)
	if err == nil {
		return nil
	}
	items := graph.ErrorItems(err)
	if len(items) == 0 {
		return err
	}
	partial := false
	var page []int
	places := map[int][]int{}
	for _, e := range items {
		p := e.Path
		if len(p) < 5 || p[0] != "repositoryOwner" || p[1] != "projectV2" || p[2] != "items" || p[3] != "nodes" {
			return err
		}
		if !graph.Withheld(e) {
			return err
		}
		// nodes.<i> and nodes.<i>.content are counted as hidden items.
		if len(p) > 6 || (len(p) == 6 && p[5] != "content") {
			partial = true
			if i, ok := pathIndex(p[4]); ok {
				page = append(page, i)
				// nodes.<i>.content.blockedBy.nodes.<j>.projectItems...
				if len(p) > 9 && p[6] == "blockedBy" && p[9] == "projectItems" {
					if j, ok := pathIndex(p[8]); ok && !slices.Contains(places[i], j) {
						places[i] = append(places[i], j)
					}
				}
			}
		}
	}
	t.partial = t.partial || partial
	t.page = page
	t.places = places
	return nil
}

// pathIndex is a list index in a GraphQL error path: a JSON number.
func pathIndex(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), n >= 0
	case int:
		return n, n >= 0
	case json.Number:
		i, err := strconv.Atoi(string(n))
		return i, err == nil && i >= 0
	}
	return 0, false
}
