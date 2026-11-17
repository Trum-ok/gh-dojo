package issue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// What gh dojo new reads before it writes, in one request (see
// ReadNewContext): the repository's ID, labels, open milestones and newest
// open issues, to find the one an earlier run created by the marker its body
// ends with; the --blocked-by and --parent issues as links; the users
// --assignee names. The plan made of it is workflow.PlanNew's.

// NewDedupWindow is how many of the repository's newest open issues
// ReadNewContext searches for new's marker ($window of NewContextQuery).
const NewDedupWindow = 20

// NewContextQuery reads what new needs before writing: the repo ID, its
// labels (by name, for the allowed lists), its open milestones (for
// --milestone) and the newest open issues, to find one a failed run already
// created. $status is the board's status field name. newContextQuery
// extends it for links and assignees.
const NewContextQuery = `query NewIssueContext($owner: String!, $repo: String!, $status: String!, $window: Int!) {
  repository(owner: $owner, name: $repo) {
    id
    labels(first: 100, orderBy: {field: NAME, direction: ASC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes { id name }
    }
    ` + MilestonesSelection + `
    issues(last: $window, states: OPEN, orderBy: {field: CREATED_AT, direction: ASC}) {
      nodes {
        id
        number
        title
        body
        viewerDidAuthor
        labels(first: 50) { nodes { name } }
        milestone { title dueOn }
        projectItems(first: 20) {
          nodes {
            id
            isArchived
            project { id }
            fieldValueByName(name: $status) { ... on ProjectV2ItemFieldSingleSelectValue { name } }
          }
        }
      }
    }
  }
}`

// newIssueMilestone is the milestone of a recent issue in NewContextQuery;
// newIssueParent its parent, read after it for --parent, and
// newIssueAssignees its assignees (GitHub allows 10), for --assignee.
// newIssuesField starts the recent issues, which the assignable users for
// --assignee go before.
const (
	newIssueMilestone = "        milestone { title dueOn }\n"
	newIssueParent    = "        parent { id number repository { nameWithOwner } }\n"
	newIssueAssignees = "        assignees(first: 10) { totalCount nodes { login } }\n"
	newIssuesField    = "    issues(last: $window"
)

// assigneeAlias is the alias of the i-th --assignee login's lookup in
// newContextQuery, and the name of its variable.
func assigneeAlias(i int) string { return fmt.Sprintf("assignee%d", i) }

// NewRequest names what ReadNewContext reads besides NewContextQuery.
type NewRequest struct {
	Repo Repo // the repository the issue is created in
	// StatusField is the board's status field name.
	StatusField string
	// Blockers are the --blocked-by issues and Parent the --parent one, nil
	// without it; both are read as links (see Links).
	Blockers []Ref
	Parent   *Ref
	// Logins are the --assignee logins to look up among the repository's
	// assignable users, in order; Viewer reads the authenticated user, for
	// @me. Either reads the recent issues' assignees too.
	Logins []string
	Viewer bool
}

// links are the issues r reads as links: the blockers, then the parent.
func (r NewRequest) links() Links {
	links := Links(slices.Clone(r.Blockers))
	if r.Parent != nil {
		links = append(links, *r.Parent) // the last one
	}
	return links
}

// newContextQuery is NewContextQuery that also reads the links of r (see
// Links), and, with a parent (the last link), how many sub-issues it has
// and the parent of each recent issue, so a resumed one shows whether it
// has one; with assignees, each login among the repository's assignable
// users (aliased assignee0, assignee1...), the viewer, and the assignees of
// each recent issue. Without any of them it is NewContextQuery itself.
//
//	query NewIssueContext(..., $assignee0: String!, $link0Owner: String!, ...) {
//	  repository(owner: $owner, name: $repo) {
//	    ...
//	    assignee0: assignableUsers(query: $assignee0, first: 100) { nodes { id login } }
//	    issues(...) { nodes { ... assignees(first: 10) { totalCount nodes { login } } ... } }
//	  }
//	  link0: repository(...) { ... }
//	  viewer { id login }
//	}
func newContextQuery(r NewRequest) string {
	q := NewContextQuery
	links := r.links()
	issueFields := newIssueMilestone
	sub := -1
	if r.Parent != nil {
		issueFields += newIssueParent
		sub = len(links) - 1
	}
	if len(r.Logins) > 0 || r.Viewer {
		issueFields += newIssueAssignees
	}
	q = strings.Replace(q, newIssueMilestone, issueFields, 1)
	var decls, repoFields strings.Builder
	for i := range r.Logins {
		alias := assigneeAlias(i)
		fmt.Fprintf(&decls, ", $%s: String!", alias)
		fmt.Fprintf(&repoFields, "    %s: assignableUsers(query: $%s, first: %d) { nodes { id login } }\n", alias, alias, assignableWindow)
	}
	decls.WriteString(links.Decls())
	if decls.Len() > 0 {
		q = strings.Replace(q, "$window: Int!)", "$window: Int!"+decls.String()+")", 1)
	}
	if repoFields.Len() > 0 {
		q = strings.Replace(q, newIssuesField, repoFields.String()+newIssuesField, 1)
	}
	if len(links) == 0 && !r.Viewer {
		return q
	}
	q = strings.TrimSuffix(q, "}") + links.FieldsCountingSubIssues(sub)
	if r.Viewer {
		q += "  viewer { id login }\n"
	}
	return q + "}"
}

// LabelsQuery reads all labels of a repository, by name; only needed past
// the first 100, which NewContextQuery reads.
const LabelsQuery = `query RepoLabels($owner: String!, $repo: String!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    labels(first: 100, after: $cursor, orderBy: {field: NAME, direction: ASC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes { id name }
    }
  }
}`

// Label is a repository label.
type Label struct {
	ID   string `json:"id"` // node ID; never print it
	Name string `json:"name"`
}

// NewContext is what ReadNewContext read.
type NewContext struct {
	RepoID string // node ID; never print it
	// Labels are the repository's labels: the first page.
	Labels    graph.Connection[Label]
	allLabels []Label // all of them, once read past the first page
	// Milestones are the repository's open milestones: the first page.
	Milestones    graph.Connection[OpenMilestone]
	allMilestones []OpenMilestone // all of them, once read past the first page
	// Issues are the repository's newest open issues, oldest first.
	Issues []*newIssueJSON
	// Links are the NewRequest.Blockers issues, in order.
	Links []*Linked
	// Parent is the NewRequest.Parent issue, read as a link; nil without it.
	Parent *Linked
	// Assignable are the assignable users read for each NewRequest.Logins
	// login, in order; Viewer is the authenticated user, read for
	// NewRequest.Viewer.
	Assignable [][]*User
	Viewer     *User
}

// BoardItem is an issue's item on a board, as new reads it.
type BoardItem struct {
	ID         string `json:"id"` // node ID; never print it
	IsArchived bool   `json:"isArchived"`
	Project    *struct {
		ID string `json:"id"`
	} `json:"project"`
	Status *struct {
		Name string `json:"name"`
	} `json:"fieldValueByName"`
}

type newIssueJSON struct {
	ID              string `json:"id"`
	Number          int    `json:"number"`
	Title           string `json:"title"`
	Body            string `json:"body"`
	ViewerDidAuthor bool   `json:"viewerDidAuthor"`
	Labels          struct {
		Nodes []*struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Milestone *OpenMilestone `json:"milestone"`
	// Parent is read with a parent only (newIssueParent).
	Parent *struct {
		ID         string `json:"id"`
		Number     int    `json:"number"`
		Repository *struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
	} `json:"parent"`
	// Assignees are read with assignees only (newIssueAssignees).
	Assignees *struct {
		TotalCount int `json:"totalCount"`
		Nodes      []*struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	ProjectItems struct {
		Nodes []*BoardItem `json:"nodes"`
	} `json:"projectItems"`
	parentHidden bool // GitHub withheld its parent (hiddenParents)
}

// ReadNewContext reads what new needs before writing, in one request (see
// newContextQuery): a missing link is Linked.Missing, not an error, and so
// is a recent issue's parent GitHub withholds from the token.
func ReadNewContext(ctx context.Context, q graph.Querier, req NewRequest) (*NewContext, error) {
	links := req.links()
	var r *struct {
		ID         string                          `json:"id"`
		Labels     graph.Connection[Label]         `json:"labels"`
		Milestones graph.Connection[OpenMilestone] `json:"milestones"`
		Issues     struct {
			Nodes []*newIssueJSON `json:"nodes"`
		} `json:"issues"`
	}
	repo := req.Repo
	vars := map[string]any{"owner": repo.Owner, "repo": repo.Name, "status": req.StatusField, "window": NewDedupWindow}
	links.AddVars(vars)
	for i, login := range req.Logins {
		vars[assigneeAlias(i)] = login
	}
	var data map[string]json.RawMessage
	err := q.Query(ctx, newContextQuery(req), vars, &data)
	var assignable [][]*User
	if raw, ok := data["repository"]; ok {
		if uerr := json.Unmarshal(raw, &r); uerr != nil {
			return nil, render.Internal("cannot decode GitHub's answer").WithCause(uerr)
		}
		if r != nil && len(req.Logins) > 0 {
			var uerr error
			if assignable, uerr = decodeAssignable(raw, len(req.Logins)); uerr != nil {
				return nil, render.Internal("cannot decode GitHub's answer").WithCause(uerr)
			}
		}
	}
	hidden, others := hiddenParents(err)
	if err != nil && r != nil && (others == nil || onlyLinkErrors(others)) {
		err = nil // a missing --blocked-by issue (LinksError names it), or a parent withheld
	}
	if r == nil && (err == nil || render.FromError(err).Code == render.ExitNotFound) {
		return nil, repoNotFound(repo, err)
	}
	if err != nil {
		return nil, err
	}
	nc := &NewContext{RepoID: r.ID, Labels: r.Labels, Milestones: r.Milestones, Issues: r.Issues.Nodes}
	for i, is := range nc.Issues {
		if is != nil && hidden[i] {
			is.parentHidden = true
		}
	}
	if len(links) > 0 {
		if nc.Links, err = links.Decode(data); err != nil {
			return nil, render.Internal("cannot decode GitHub's answer").WithCause(err)
		}
	}
	if req.Parent != nil {
		n := len(req.Blockers)
		nc.Links, nc.Parent = nc.Links[:n], nc.Links[n]
	}
	nc.Assignable = assignable
	if raw, ok := data["viewer"]; ok && !graph.IsNull(raw) {
		var v User
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, render.Internal("cannot decode GitHub's answer").WithCause(err)
		}
		if v.ID != "" {
			nc.Viewer = &v
		}
	}
	return nc, nil
}

// decodeAssignable decodes the n assignable-user lookups of a context
// read (assignee0, assignee1...) from the repository object raw; a lookup
// GitHub left out is empty.
func decodeAssignable(raw json.RawMessage, n int) ([][]*User, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	out := make([][]*User, n)
	for i := range n {
		f, ok := fields[assigneeAlias(i)]
		if !ok || graph.IsNull(f) {
			continue
		}
		var c struct {
			Nodes []*User `json:"nodes"`
		}
		if err := json.Unmarshal(f, &c); err != nil {
			return nil, err
		}
		out[i] = c.Nodes
	}
	return out, nil
}

// hiddenParents returns the indexes of the recent issues whose parent
// GitHub withheld from the token (NOT_FOUND or FORBIDDEN at
// repository.issues.nodes.<i>.parent), and err without those errors: nil
// when they were all of them, or when err is not a GraphQL error.
func hiddenParents(err error) (map[int]bool, error) {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil {
		return nil, err
	}
	hidden := map[int]bool{}
	var rest []api.GraphQLErrorItem
	for _, item := range gqlErr.Errors {
		p := item.Path
		if len(p) == 5 && p[0] == "repository" && p[1] == "issues" && p[2] == "nodes" && p[4] == "parent" &&
			(item.Type == "NOT_FOUND" || item.Type == "FORBIDDEN") {
			if i, ok := p[3].(float64); ok {
				hidden[int(i)] = true
				continue
			}
		}
		rest = append(rest, item)
	}
	if len(rest) == 0 {
		return hidden, nil
	}
	return hidden, &api.GraphQLError{Errors: rest}
}

// Created is an issue an earlier run of new created, found by the marker
// its body ends with (see NewContext.Find).
type Created struct {
	ID     string // node ID; never print it
	Number int
	Title  string
	Labels []string
	// Milestone is its milestone, nil for none; its ID is not read.
	Milestone *OpenMilestone
	// ItemID is its item on the board, "" when not on it; Archived says
	// the item is archived, Status is its status, "" for none.
	ItemID   string
	Archived bool
	Status   string
	// ParentID is the node ID of its parent, "" for none, and Parent names
	// it; ParentHidden says it has a parent GitHub withheld from the token.
	// Read with NewRequest.Parent only.
	ParentID     string
	Parent       *Ref
	ParentHidden bool
	// Assignees are the logins of its assignees, and AssigneesTotal how
	// many it has; read with assignees only.
	Assignees      []string
	AssigneesTotal int
}

// Find returns the newest open issue the viewer wrote whose body ends with
// marker, with its item on the board projectID, or nil.
func (nc *NewContext) Find(marker, projectID string) *Created {
	for i := len(nc.Issues) - 1; i >= 0; i-- {
		is := nc.Issues[i]
		if is == nil || !is.ViewerDidAuthor || !strings.HasSuffix(strings.TrimSpace(is.Body), marker) {
			continue
		}
		f := &Created{ID: is.ID, Number: is.Number, Title: is.Title, Milestone: is.Milestone, ParentHidden: is.parentHidden}
		if p := is.Parent; p != nil && p.ID != "" && p.Repository != nil {
			if repo, err := ParseRepo(p.Repository.NameWithOwner); err == nil {
				f.ParentID, f.Parent = p.ID, &Ref{Repo: repo, Number: p.Number}
			}
		}
		for _, l := range is.Labels.Nodes {
			if l != nil {
				f.Labels = append(f.Labels, l.Name)
			}
		}
		if a := is.Assignees; a != nil {
			for _, u := range a.Nodes {
				if u != nil {
					f.Assignees = append(f.Assignees, u.Login)
				}
			}
			f.AssigneesTotal = max(a.TotalCount, len(f.Assignees))
		}
		for _, it := range is.ProjectItems.Nodes {
			if it != nil && it.Project != nil && it.Project.ID == projectID {
				f.ItemID, f.Archived = it.ID, it.IsArchived
				if it.Status != nil {
					f.Status = it.Status.Name
				}
			}
		}
		return f
	}
	return nil
}

// LabelNames returns the names of labels, in order.
func LabelNames(labels []Label) []string {
	out := make([]string, len(labels))
	for i, l := range labels {
		out[i] = l.Name
	}
	return out
}

// AllLabels returns all labels of repo: the first page read with the
// context, and the rest, read once, when there are more.
func (nc *NewContext) AllLabels(ctx context.Context, q graph.Querier, repo Repo) ([]Label, error) {
	if !nc.Labels.PageInfo.HasNextPage {
		return nc.Labels.Nodes, nil
	}
	if nc.allLabels == nil {
		all, err := ReadLabels(ctx, q, repo)
		if err != nil {
			return nil, err
		}
		nc.allLabels = all
	}
	return nc.allLabels, nil
}

// ReadLabels reads all labels of repo, page by page.
func ReadLabels(ctx context.Context, q graph.Querier, repo Repo) ([]Label, error) {
	type resp struct {
		Repository *struct {
			Labels graph.Connection[Label] `json:"labels"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": repo.Owner, "repo": repo.Name}
	page, err := graph.Paginate(ctx, q, LabelsQuery, vars, 0, func(r *resp) *graph.Connection[Label] {
		if r.Repository == nil {
			return nil
		}
		return &r.Repository.Labels
	})
	if err != nil {
		return nil, err
	}
	return page.Nodes, nil
}

// AllMilestones returns all open milestones of repo: the first page read
// with the context, and the rest, read once, when there are more.
func (nc *NewContext) AllMilestones(ctx context.Context, q graph.Querier, repo Repo) ([]OpenMilestone, error) {
	if !nc.Milestones.PageInfo.HasNextPage {
		return nc.Milestones.Nodes, nil
	}
	if nc.allMilestones == nil {
		rest, err := ReadMilestones(ctx, q, repo, nc.Milestones.PageInfo.EndCursor)
		if err != nil {
			return nil, err
		}
		nc.allMilestones = append(slices.Clone(nc.Milestones.Nodes), rest...)
	}
	return nc.allMilestones, nil
}

// MatchAssignees matches logins to the users who can be assigned issues in
// the repository, in order and without duplicates (me, the viewer, and the
// viewer's own login are one user): me is the viewer, any other login is
// matched among the assignable users read for it (the logins other than
// me are NewRequest.Logins, in order), ignoring case. unknown are the
// logins that cannot be assigned; noViewer says me was asked for but
// GitHub returned no viewer.
func (nc *NewContext) MatchAssignees(logins []string, me string) (users []*User, unknown []string, noViewer bool) {
	add := func(u *User) {
		if !slices.ContainsFunc(users, func(x *User) bool { return x.ID == u.ID }) {
			users = append(users, u)
		}
	}
	i := 0 // index among the logins looked up
	for _, login := range logins {
		if login == me {
			if nc.Viewer == nil {
				return nil, nil, true
			}
			add(nc.Viewer)
			continue
		}
		var found *User
		if i < len(nc.Assignable) {
			found = matchUser(nc.Assignable[i], login)
		}
		i++
		if found == nil {
			unknown = append(unknown, login)
			continue
		}
		add(found)
	}
	return users, unknown, false
}

// boardItemsQuery re-reads an issue's board items after
// addProjectV2ItemById was refused. $status is the board's status field name.
const boardItemsQuery = `query IssueBoardItems($issue: ID!, $status: String!) {
  node(id: $issue) {
    ... on Issue {
      projectItems(first: 20) {
        nodes {
          id
          isArchived
          project { id }
          fieldValueByName(name: $status) { ... on ProjectV2ItemFieldSingleSelectValue { name } }
        }
      }
    }
  }
}`

// ReadBoardItem reads the board items of the issue with node ID issueID
// and returns its unarchived item on the board projectID, or nil when
// there is none or the read fails: new re-reads so after GitHub refused to
// add the issue, which may be there all the same.
func ReadBoardItem(ctx context.Context, q graph.Querier, issueID, statusField, projectID string) *BoardItem {
	var out struct {
		Node *struct {
			ProjectItems struct {
				Nodes []*BoardItem `json:"nodes"`
			} `json:"projectItems"`
		} `json:"node"`
	}
	vars := map[string]any{"issue": issueID, "status": statusField}
	if err := q.Query(ctx, boardItemsQuery, vars, &out); err != nil || out.Node == nil {
		return nil
	}
	for _, it := range out.Node.ProjectItems.Nodes {
		if it != nil && !it.IsArchived && it.Project != nil && it.Project.ID == projectID && it.ID != "" {
			return it
		}
	}
	return nil
}
