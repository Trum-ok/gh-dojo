package board

import (
	"context"
	"errors"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// projectsQuery reads one page of an owner's projects, user or
// organization, in number order with their item counts. Projects the token
// cannot see are left out by GitHub.
const projectsQuery = `query OwnerProjects($owner: String!, $first: Int!, $cursor: String) {
  repositoryOwner(login: $owner) {
    __typename
    login
    ... on User { projectsV2(first: $first, after: $cursor, orderBy: {field: NUMBER, direction: ASC}) { ...projects } }
    ... on Organization { projectsV2(first: $first, after: $cursor, orderBy: {field: NUMBER, direction: ASC}) { ...projects } }
  }
}

fragment projects on ProjectV2Connection {
  totalCount
  pageInfo { hasNextPage endCursor }
  nodes { number title closed items { totalCount } }
}`

// projectsPageSize is how many projects one request reads; tests lower it
// to record several pages.
var projectsPageSize = 100

// Project is one of an owner's projects, as ListProjects reads it.
type Project struct {
	Number int
	Title  string
	Closed bool
	Items  int // items on the board, archived ones included
}

// Projects is an owner's projects in number order.
type Projects struct {
	Owner     string // login as GitHub spells it
	OwnerType string // OwnerUser or OwnerOrganization
	Projects  []Project
	// TotalCount is the count GitHub reported on the last page. It differs
	// from len(Projects) when projects changed between pages.
	TotalCount int
}

type projectsResponse struct {
	RepositoryOwner *struct {
		Typename   string                         `json:"__typename"`
		Login      string                         `json:"login"`
		ProjectsV2 *graph.Connection[projectNode] `json:"projectsV2"`
	} `json:"repositoryOwner"`
}

type projectNode struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Closed bool   `json:"closed"`
	Items  struct {
		TotalCount int `json:"totalCount"`
	} `json:"items"`
}

// ListProjects reads every project of owner (a user or an organization)
// that the token can see, 100 per request. An unknown owner is exit
// code 3; network and auth problems keep graph's codes.
func ListProjects(ctx context.Context, q graph.Querier, owner string) (*Projects, error) {
	if err := (Ref{Owner: owner, Number: 1}).Validate(); err != nil {
		return nil, err
	}
	out := &Projects{}
	vars := map[string]any{"owner": owner, "first": projectsPageSize}
	page, err := graph.Paginate(ctx, q, projectsQuery, vars, 0, func(r *projectsResponse) *graph.Connection[projectNode] {
		o := r.RepositoryOwner
		if o == nil || o.ProjectsV2 == nil {
			return nil
		}
		out.Owner, out.OwnerType = o.Login, o.Typename
		return o.ProjectsV2
	})
	if err != nil {
		if errors.Is(err, graph.ErrParentNotFound) || render.FromError(err).Code == render.ExitNotFound {
			return nil, render.NotFound("GitHub user or organization %q not found", owner).WithCause(err)
		}
		return nil, err
	}
	out.TotalCount = page.TotalCount
	seen := map[int]bool{} // projects changed between pages repeat one
	for _, n := range page.Nodes {
		if seen[n.Number] {
			continue
		}
		seen[n.Number] = true
		out.Projects = append(out.Projects, Project{Number: n.Number, Title: n.Title, Closed: n.Closed, Items: n.Items.TotalCount})
	}
	return out, nil
}
