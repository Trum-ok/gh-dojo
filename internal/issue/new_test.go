package issue_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/issue"
)

// fakeQuerier answers one query with data (the "data" object) and err,
// and keeps the query and variables it was sent. The real reads are tested
// against recorded fixtures in cmd/gh-dojo (TestNewContextRecorded and the
// new_*.json fixtures).
type fakeQuerier struct {
	data  string
	err   error
	query string
	vars  map[string]any
	n     int
}

func (f *fakeQuerier) Query(_ context.Context, q string, vars map[string]any, out any) error {
	f.query, f.vars = q, vars
	f.n++
	if err := json.Unmarshal([]byte(f.data), out); err != nil {
		return err
	}
	return f.err
}

var acmeAPI = issue.Repo{Owner: "acme", Name: "api"}

// TestNewContextQuery: without links or assignees the read is
// NewContextQuery as recorded; with them, each login is looked up under
// its own alias, the viewer is read for @me, the recent issues' assignees
// and parents are read, and the links at the top.
func TestNewContextQuery(t *testing.T) {
	f := &fakeQuerier{data: `{"repository": {"id": "R1"}}`}
	if _, err := issue.ReadNewContext(context.Background(), f, issue.NewRequest{Repo: acmeAPI, StatusField: "Status"}); err != nil {
		t.Fatal(err)
	}
	if f.query != issue.NewContextQuery {
		t.Errorf("without links or assignees the query changed:\n%s", f.query)
	}
	if f.vars["window"] != issue.NewDedupWindow || f.vars["status"] != "Status" || f.vars["owner"] != "acme" {
		t.Errorf("vars = %v", f.vars)
	}

	parent := issue.Ref{Repo: acmeAPI, Number: 40}
	req := issue.NewRequest{Repo: acmeAPI, StatusField: "Status", Logins: []string{"octocat", "monalisa"}, Viewer: true,
		Blockers: []issue.Ref{{Repo: acmeAPI, Number: 12}}, Parent: &parent}
	if _, err := issue.ReadNewContext(context.Background(), f, req); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"$window: Int!, $assignee0: String!, $assignee1: String!, $link0Owner: String!",
		"    assignee0: assignableUsers(query: $assignee0, first: 100) { nodes { id login } }\n    assignee1: assignableUsers(query: $assignee1",
		"        milestone { title dueOn }\n        parent { id number repository { nameWithOwner } }\n        assignees(first: 10) { totalCount nodes { login } }\n",
		"  link1: repository(owner: $link1Owner",
		"subIssues { totalCount } } }\n  }\n  viewer { id login }\n}",
	} {
		if !strings.Contains(f.query, want) {
			t.Errorf("query lacks %q:\n%s", want, f.query)
		}
	}
	if strings.Contains(f.query, "assignee2") || strings.Count(f.query, "subIssues") != 1 {
		t.Errorf("one alias per login, sub-issues read for the parent only:\n%s", f.query)
	}
	if f.vars["assignee1"] != "monalisa" || f.vars["link1Number"] != 40 {
		t.Errorf("vars = %v", f.vars)
	}
}

// newContextData is a context read: labels, two milestones, three recent
// issues, the --assignee lookups, a blocker, the parent and the viewer.
const newContextData = `{
  "repository": {
    "id": "R1",
    "labels": {"totalCount": 2, "pageInfo": {"hasNextPage": false}, "nodes": [{"id": "L1", "name": "bug"}, {"id": "L2", "name": "area:cli"}]},
    "milestones": {"totalCount": 1, "pageInfo": {"hasNextPage": false}, "nodes": [{"id": "M1", "title": "v1.2"}]},
    "assignee0": {"nodes": [{"id": "U1", "login": "octocat"}, {"id": "U3", "login": "octocat2"}]},
    "assignee1": {"nodes": [{"id": "U2", "login": "Monalisa"}]},
    "issues": {"nodes": [
      {"id": "I55", "number": 55, "title": "Fix login redirect", "viewerDidAuthor": true,
       "body": "old\n\n<!-- gh-dojo:new:00000000 -->"},
      {"id": "I56", "number": 56, "title": "Fix login redirect", "viewerDidAuthor": false,
       "body": "<!-- gh-dojo:new:00000000 -->"},
      {"id": "I57", "number": 57, "title": "Fix login redirect", "viewerDidAuthor": true,
       "body": "text\n\n<!-- gh-dojo:new:00000000 -->\n",
       "labels": {"nodes": [{"name": "bug"}, null]},
       "milestone": {"title": "v1.1", "dueOn": null},
       "parent": {"id": "P7", "number": 7, "repository": {"nameWithOwner": "acme/web"}},
       "assignees": {"totalCount": 3, "nodes": [{"login": "octocat"}]},
       "projectItems": {"nodes": [
         {"id": "IT0", "isArchived": false, "project": {"id": "PRJ_other"}, "fieldValueByName": {"name": "Done"}},
         {"id": "IT1", "isArchived": true, "project": {"id": "PRJ"}, "fieldValueByName": {"name": "Backlog"}}
       ]}}
    ]}
  },
  "link0": {"nameWithOwner": "acme/api", "issueOrPullRequest": {"__typename": "Issue", "id": "B12", "title": "Blocker", "state": "OPEN",
    "blocking": {"nodes": [{"id": "I57"}]}, "blockedBy": {"nodes": []}}},
  "link1": {"nameWithOwner": "acme/api", "issueOrPullRequest": {"__typename": "Issue", "id": "P40", "title": "Parent", "state": "OPEN",
    "blocking": {"nodes": []}, "blockedBy": {"nodes": []}, "subIssues": {"totalCount": 4}}},
  "viewer": {"id": "U9", "login": "me"}
}`

func readNewContext(t *testing.T, data string, err error) *issue.NewContext {
	t.Helper()
	parent := issue.Ref{Repo: acmeAPI, Number: 40}
	f := &fakeQuerier{data: data, err: err}
	nc, rerr := issue.ReadNewContext(context.Background(), f, issue.NewRequest{Repo: acmeAPI, StatusField: "Status",
		Logins: []string{"octocat", "monalisa"}, Viewer: true, Blockers: []issue.Ref{{Repo: acmeAPI, Number: 12}}, Parent: &parent})
	if rerr != nil {
		t.Fatal(rerr)
	}
	return nc
}

// TestNewContextFind: the newest open issue the viewer wrote whose body
// ends with the marker, with its item on the board.
func TestNewContextFind(t *testing.T) {
	nc := readNewContext(t, newContextData, nil)
	if nc.RepoID != "R1" || len(nc.Links) != 1 || nc.Links[0].ID != "B12" || nc.Parent == nil || nc.Parent.SubIssues != 4 {
		t.Fatalf("context: repo %q, links %v, parent %+v", nc.RepoID, nc.Links, nc.Parent)
	}
	if !nc.Links[0].Blocks("I57") {
		t.Errorf("#12 should block I57")
	}
	f := nc.Find("<!-- gh-dojo:new:00000000 -->", "PRJ")
	switch {
	case f == nil:
		t.Fatal("not found")
	case f.Number != 57 || f.ID != "I57" || f.Title != "Fix login redirect":
		t.Errorf("found #%d %q (%s)", f.Number, f.Title, f.ID)
	case !slices.Equal(f.Labels, []string{"bug"}) || f.Milestone == nil || f.Milestone.Title != "v1.1":
		t.Errorf("labels %q, milestone %+v", f.Labels, f.Milestone)
	case f.ItemID != "IT1" || !f.Archived || f.Status != "Backlog":
		t.Errorf("item %q, archived %v, status %q", f.ItemID, f.Archived, f.Status)
	case f.ParentID != "P7" || f.Parent == nil || f.Parent.Repo.String() != "acme/web" || f.Parent.Number != 7 || f.ParentHidden:
		t.Errorf("parent %q %+v, hidden %v", f.ParentID, f.Parent, f.ParentHidden)
	case !slices.Equal(f.Assignees, []string{"octocat"}) || f.AssigneesTotal != 3:
		t.Errorf("assignees %q of %d", f.Assignees, f.AssigneesTotal)
	}
	if f := nc.Find("<!-- gh-dojo:new:00000000 -->", "PRJ_none"); f == nil || f.ItemID != "" || f.Status != "" {
		t.Errorf("on another board: %+v", f)
	}
	if f := nc.Find("<!-- gh-dojo:new:11111111 -->", "PRJ"); f != nil {
		t.Errorf("another marker found #%d", f.Number)
	}
}

// TestNewContextHiddenParent: a recent issue's parent GitHub withholds is
// no error; the issue found says it has one.
func TestNewContextHiddenParent(t *testing.T) {
	data := strings.Replace(newContextData, `"parent": {"id": "P7", "number": 7, "repository": {"nameWithOwner": "acme/web"}}`, `"parent": null`, 1)
	err := &api.GraphQLError{Errors: []api.GraphQLErrorItem{
		{Type: "FORBIDDEN", Message: "no access", Path: []any{"repository", "issues", "nodes", float64(2), "parent"}},
	}}
	f := readNewContext(t, data, err).Find("<!-- gh-dojo:new:00000000 -->", "PRJ")
	if f == nil || !f.ParentHidden || f.ParentID != "" {
		t.Errorf("found %+v, want a hidden parent", f)
	}

	other := &api.GraphQLError{Errors: []api.GraphQLErrorItem{
		{Type: "FORBIDDEN", Message: "no access", Path: []any{"repository", "issues", "nodes", float64(2), "parent"}},
		{Type: "INTERNAL", Message: "boom", Path: []any{"repository", "labels"}},
	}}
	fq := &fakeQuerier{data: data, err: other}
	if _, err := issue.ReadNewContext(context.Background(), fq, issue.NewRequest{Repo: acmeAPI}); err == nil {
		t.Error("another error was dropped")
	}
}

// TestNewContextHiddenProject: a project GitHub withholds from the token on
// a recent issue (FORBIDDEN at ...projectItems.nodes.<j>.project, as in
// testdata/graphql/card_partial.json) is no error, so new neither fails nor
// creates a second issue: the issue is found by its marker, its item on
// another project unknown, so it is not on the board for Find. An error
// elsewhere still fails, whatever else was withheld. The read is the same
// for --dry-run.
func TestNewContextHiddenProject(t *testing.T) {
	// The marker issue's other item is withheld: its project comes back null.
	data := strings.Replace(newContextData, `{"id": "IT0", "isArchived": false, "project": {"id": "PRJ_other"}, "fieldValueByName": {"name": "Done"}}`,
		`{"id": "IT0", "isArchived": false, "project": null, "fieldValueByName": null}`, 1)
	hidden := api.GraphQLErrorItem{Type: "FORBIDDEN", Message: "Resource not accessible by integration",
		Path: []any{"repository", "issues", "nodes", float64(2), "projectItems", "nodes", float64(0), "project"}}
	err := &api.GraphQLError{Errors: []api.GraphQLErrorItem{hidden}}

	const marker = "<!-- gh-dojo:new:00000000 -->"
	f := readNewContext(t, data, err).Find(marker, "PRJ")
	if f == nil || f.Number != 57 || f.ItemID != "IT1" || f.ParentID != "P7" || f.ParentHidden {
		t.Errorf("found %+v, want #57 with its item on the board", f)
	}
	if f := readNewContext(t, data, err).Find(marker, "PRJ_other"); f == nil || f.Number != 57 || f.ItemID != "" {
		t.Errorf("on the withheld project: %+v, want #57 found, no item known", f)
	}

	// Withheld in the item itself, in a NOT_FOUND node, with a withheld parent.
	err = &api.GraphQLError{Errors: []api.GraphQLErrorItem{hidden,
		{Type: "NOT_FOUND", Message: "gone", Path: []any{"repository", "issues", "nodes", float64(0), "projectItems", "nodes", float64(1)}},
		{Type: "FORBIDDEN", Message: "no access", Path: []any{"repository", "issues", "nodes", float64(2), "parent"}},
	}}
	if f := readNewContext(t, data, err).Find(marker, "PRJ"); f == nil || !f.ParentHidden {
		t.Errorf("found %+v, want a hidden parent", f)
	}

	for name, items := range map[string][]api.GraphQLErrorItem{
		"another path":   {hidden, {Type: "FORBIDDEN", Message: "no access", Path: []any{"repository", "labels"}}},
		"another type":   {hidden, {Type: "INTERNAL", Message: "boom", Path: []any{"repository", "issues", "nodes", float64(2), "projectItems"}}},
		"no path":        {hidden, {Type: "FORBIDDEN", Message: "no access"}},
		"not an element": {hidden, {Type: "FORBIDDEN", Message: "no access", Path: []any{"repository", "issues", "nodes"}}},
	} {
		fq := &fakeQuerier{data: data, err: &api.GraphQLError{Errors: items}}
		if _, err := issue.ReadNewContext(context.Background(), fq, issue.NewRequest{Repo: acmeAPI}); err == nil {
			t.Errorf("%s: the error was dropped", name)
		}
	}
}

func TestNewContextRepoNotFound(t *testing.T) {
	f := &fakeQuerier{data: `{"repository": null}`}
	_, err := issue.ReadNewContext(context.Background(), f, issue.NewRequest{Repo: acmeAPI})
	if err == nil || !strings.Contains(err.Error(), "repository acme/api not found, or the token cannot see it") {
		t.Errorf("err = %v", err)
	}
}

// TestMatchAssignees: logins in order, ignoring case, each user once
// (@me and the viewer's own login too); unknown logins listed, and a
// missing viewer reported.
func TestMatchAssignees(t *testing.T) {
	nc := readNewContext(t, newContextData, nil)
	users, unknown, noViewer := nc.MatchAssignees([]string{"@me", "OCTOCAT", "monalisa"}, "@me")
	var logins []string
	for _, u := range users {
		logins = append(logins, u.Login)
	}
	if !slices.Equal(logins, []string{"me", "octocat", "Monalisa"}) || unknown != nil || noViewer {
		t.Errorf("users %q, unknown %q, noViewer %v", logins, unknown, noViewer)
	}
	nc.Assignable[1] = nil
	if _, unknown, _ := nc.MatchAssignees([]string{"octocat", "monalisa"}, "@me"); !slices.Equal(unknown, []string{"monalisa"}) {
		t.Errorf("unknown = %q", unknown)
	}
	nc.Viewer = nil
	if _, _, noViewer := nc.MatchAssignees([]string{"octocat", "@me"}, "@me"); !noViewer {
		t.Error("no viewer not reported")
	}
}

// TestNewContextAllLabels: past the first page, all labels are read once.
func TestNewContextAllLabels(t *testing.T) {
	data := strings.Replace(newContextData, `"labels": {"totalCount": 2, "pageInfo": {"hasNextPage": false}`,
		`"labels": {"totalCount": 3, "pageInfo": {"hasNextPage": true, "endCursor": "c1"}`, 1)
	nc := readNewContext(t, data, nil)
	f := &fakeQuerier{data: `{"repository": {"labels": {"totalCount": 3, "pageInfo": {"hasNextPage": false},
		"nodes": [{"id": "L1", "name": "bug"}, {"id": "L2", "name": "area:cli"}, {"id": "L3", "name": "docs"}]}}}`}
	for range 2 {
		all, err := nc.AllLabels(context.Background(), f, acmeAPI)
		if err != nil {
			t.Fatal(err)
		}
		if got := issue.LabelNames(all); !slices.Equal(got, []string{"bug", "area:cli", "docs"}) {
			t.Errorf("labels = %q", got)
		}
	}
	if f.n != 1 || f.query != issue.LabelsQuery {
		t.Errorf("%d reads of\n%s", f.n, f.query)
	}

	nc = readNewContext(t, newContextData, nil)
	none := &fakeQuerier{}
	if all, err := nc.AllLabels(context.Background(), none, acmeAPI); err != nil || len(all) != 2 || none.n != 0 {
		t.Errorf("one page: %v, %v, %d reads", all, err, none.n)
	}
	if all, err := nc.AllMilestones(context.Background(), none, acmeAPI); err != nil || len(all) != 1 || none.n != 0 {
		t.Errorf("one page of milestones: %v, %v, %d reads", all, err, none.n)
	}
}

func TestReadBoardItem(t *testing.T) {
	f := &fakeQuerier{data: `{"node": {"projectItems": {"nodes": [
		{"id": "IT0", "isArchived": false, "project": {"id": "PRJ_other"}},
		{"id": "IT1", "isArchived": true, "project": {"id": "PRJ"}},
		{"id": "IT2", "isArchived": false, "project": {"id": "PRJ"}, "fieldValueByName": {"name": "Backlog"}}
	]}}}`}
	it := issue.ReadBoardItem(context.Background(), f, "I57", "Status", "PRJ")
	if it == nil || it.ID != "IT2" || it.Status == nil || it.Status.Name != "Backlog" {
		t.Errorf("item = %+v", it)
	}
	if f.vars["issue"] != "I57" || f.vars["status"] != "Status" {
		t.Errorf("vars = %v", f.vars)
	}
	if it := issue.ReadBoardItem(context.Background(), f, "I57", "Status", "PRJ_none"); it != nil {
		t.Errorf("item on another board = %+v", it)
	}
}
