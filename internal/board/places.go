package board

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/graph"
)

// IssueKey names an issue: "owner/name" and number.
type IssueKey struct {
	Repo   string
	Number int
}

func (k IssueKey) String() string { return k.Repo + "#" + strconv.Itoa(k.Number) }

// Place is an issue's place on a board, read by ReadPlaces (or with a
// blocker, ItemsOptions.Places): what show reads of a linked issue, for
// one that is not among the board's items (not on the board, or archived
// there).
type Place struct {
	State    string // "OPEN" or "CLOSED"; "" when Unknown
	OnBoard  bool   // an item of the board, archived or not
	Archived bool
	Status   string // its status there; "" for none, or when Unknown
	// Unknown says why the place could not be read, as a lowercase phrase
	// ("GitHub withheld it from the token"); "" when the fields above are
	// accurate. Withheld says GitHub withheld it from the token.
	Unknown  string
	Withheld bool
}

// placesChunk is how many issues one request of ReadPlaces reads.
const placesChunk = 100

// projectsWindow is how many projects of an issue ReadPlaces reads, as
// show does for a linked issue.
const projectsWindow = 10

// Reasons a place is Unknown.
const (
	placeWithheld = "GitHub withheld it from the token"
	placeRenamed  = "the board's status field was renamed since it was read"
)

// ReadPlaces reads the place on the board s of each issue in keys: its
// state, whether it is an item of the board (archived or not) and its
// status there, by the field named statusField whose ID is statusFieldID.
// It sends one request per placesChunk issues, none for no keys. An issue
// or repository that does not exist or that the token cannot see is
// Unknown and Withheld, not an error.
func ReadPlaces(ctx context.Context, q graph.Querier, s *Schema, statusField, statusFieldID string, keys []IssueKey) (map[IssueKey]Place, error) {
	out := map[IssueKey]Place{}
	for chunk := range slices.Chunk(keys, placesChunk) {
		if err := readPlaces(ctx, q, s, statusField, statusFieldID, chunk, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// placesQuery builds the query for keys, grouped by repository in the
// order they first appear:
//
//	query BlockerPlaces($status: String!, $r0Owner: String!, $r0Name: String!) {
//	  r0: repository(owner: $r0Owner, name: $r0Name) {
//	    i12: issue(number: 12) { ...place }
//	  }
//	}
//
// Owners and names are variables; numbers are written in the query.
func placesQuery(keys []IssueKey) (query string, vars map[string]any, repos []string, err error) {
	byRepo := map[string][]int{}
	for _, k := range keys {
		if _, ok := byRepo[k.Repo]; !ok {
			repos = append(repos, k.Repo)
		}
		if !slices.Contains(byRepo[k.Repo], k.Number) {
			byRepo[k.Repo] = append(byRepo[k.Repo], k.Number)
		}
	}
	vars = map[string]any{}
	var decls, fields strings.Builder
	decls.WriteString("$status: String!")
	for i, repo := range repos {
		owner, name, ok := strings.Cut(repo, "/")
		if !ok || owner == "" || name == "" {
			return "", nil, nil, fmt.Errorf("bad repository %q", repo)
		}
		a := "r" + strconv.Itoa(i)
		vars[a+"Owner"], vars[a+"Name"] = owner, name
		fmt.Fprintf(&decls, ", $%sOwner: String!, $%sName: String!", a, a)
		fmt.Fprintf(&fields, "  %s: repository(owner: $%sOwner, name: $%sName) {\n", a, a, a)
		for _, n := range byRepo[repo] {
			fmt.Fprintf(&fields, "    i%d: issue(number: %d) { ...place }\n", n, n)
		}
		fields.WriteString("  }\n")
	}
	query = "query BlockerPlaces(" + decls.String() + ") {\n" + fields.String() + "}\n\n" +
		`fragment place on Issue {
  state
  projectItems(first: ` + strconv.Itoa(projectsWindow) + `) {
    totalCount
    nodes {
      isArchived
      project { id }
      status: fieldValueByName(name: $status) {
        ... on ProjectV2ItemFieldSingleSelectValue { name field { ... on ProjectV2FieldCommon { id } } }
      }
    }
  }
}`
	return query, vars, repos, nil
}

type placeNode struct {
	State        string `json:"state"`
	ProjectItems struct {
		TotalCount int `json:"totalCount"`
		Nodes      []*struct {
			IsArchived bool `json:"isArchived"`
			Project    *struct {
				ID string `json:"id"`
			} `json:"project"`
			Status *struct {
				Name  string `json:"name"`
				Field *struct {
					ID string `json:"id"`
				} `json:"field"`
			} `json:"status"`
		} `json:"nodes"`
	} `json:"projectItems"`
}

func readPlaces(ctx context.Context, q graph.Querier, s *Schema, statusField, statusFieldID string, keys []IssueKey, out map[IssueKey]Place) error {
	query, vars, repos, err := placesQuery(keys)
	if err != nil {
		return err
	}
	vars["status"] = statusField
	var data map[string]map[string]*placeNode
	withheld := map[IssueKey]bool{} // projects withheld below the issue
	if err := q.Query(ctx, query, vars, &data); err != nil {
		paths, ok := placeErrors(err)
		if !ok {
			return err
		}
		for _, p := range paths {
			if len(p) < 3 {
				continue // the repository or the issue: read as null
			}
			r, _ := p[0].(string)
			i, _ := p[1].(string)
			ri, err1 := strconv.Atoi(strings.TrimPrefix(r, "r"))
			n, err2 := strconv.Atoi(strings.TrimPrefix(i, "i"))
			if err1 == nil && err2 == nil && ri < len(repos) {
				withheld[IssueKey{repos[ri], n}] = true
			}
		}
	}
	for _, k := range keys {
		ri := slices.Index(repos, k.Repo)
		var pj *placeNode
		if issues := data["r"+strconv.Itoa(ri)]; issues != nil {
			pj = issues["i"+strconv.Itoa(k.Number)]
		}
		out[k] = pj.place(s.ID, statusFieldID, withheld[k])
	}
	return nil
}

// place converts an issue's place on the board boardID; nil is an issue
// the token cannot see. hidden says GitHub withheld some of its projects.
func (pj *placeNode) place(boardID, statusFieldID string, hidden bool) Place {
	if pj == nil {
		return Place{Unknown: placeWithheld, Withheld: true}
	}
	p := Place{State: pj.State}
	items := pj.ProjectItems
	unseen := hidden
	for _, it := range items.Nodes {
		if it == nil || it.Project == nil {
			unseen = true
			continue
		}
		if it.Project.ID != boardID {
			continue
		}
		p.OnBoard, p.Archived = true, it.IsArchived
		switch st := it.Status; {
		case st == nil:
			// on the board without a status
		case statusFieldID != "" && (st.Field == nil || st.Field.ID != statusFieldID):
			p.Unknown = placeRenamed
		default:
			p.Status = st.Name
		}
		return p
	}
	switch {
	case unseen:
		p.Unknown, p.Withheld = "GitHub withheld its projects from the token", true
	case items.TotalCount > len(items.Nodes):
		p.Unknown = fmt.Sprintf("on %d projects, only the first %d read", items.TotalCount, len(items.Nodes))
	}
	return p
}

// placeErrors returns the paths of a GraphQL error whose items are all
// NOT_FOUND or FORBIDDEN below a repository alias of placesQuery; ok is
// false for any other error.
func placeErrors(err error) (paths [][]any, ok bool) {
	items := graph.ErrorItems(err)
	if len(items) == 0 {
		return nil, false
	}
	for _, e := range items {
		if len(e.Path) == 0 || !graph.Withheld(e) {
			return nil, false
		}
		if r, _ := e.Path[0].(string); !strings.HasPrefix(r, "r") {
			return nil, false
		}
		paths = append(paths, e.Path)
	}
	return paths, true
}
