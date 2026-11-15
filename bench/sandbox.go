package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// sandbox is the repository and the kanban board the benchmark works on: the
// same ones the e2e tests use (see e2e/doc.go). The harness seeds and reads
// them through the API directly, never through gh dojo, so a bug in gh dojo
// cannot hide a failure.
type sandbox struct {
	owner, name string // the repository
	project     int    // the board's number, owned by owner
	projectID   string
	statusField string
	options     map[string]string // status name → option ID

	gql  *api.GraphQLClient
	rest *api.RESTClient
}

// statuses of the kanban preset, in board order.
var statuses = []string{"Backlog", "In progress", "Waiting", "Ready", "Done"}

func newSandbox(repo string, project int) (*sandbox, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("repository %q, want owner/name", repo)
	}
	gql, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, err
	}
	rest, err := api.DefaultRESTClient()
	if err != nil {
		return nil, err
	}
	s := &sandbox{owner: owner, name: name, project: project, gql: gql, rest: rest, options: map[string]string{}}

	var q struct {
		User struct {
			ProjectV2 struct {
				ID    string
				Field struct {
					ID      string
					Options []struct{ ID, Name string }
				}
			}
		}
	}
	err = s.do(`query($o: String!, $n: Int!) { user(login: $o) { projectV2(number: $n) { id
  field(name: "Status") { ... on ProjectV2SingleSelectField { id options { id name } } } } } }`,
		map[string]any{"o": owner, "n": project}, &q)
	if err != nil {
		return nil, fmt.Errorf("read board %s/%d: %w", owner, project, err)
	}
	p := q.User.ProjectV2
	s.projectID, s.statusField = p.ID, p.Field.ID
	for _, o := range p.Field.Options {
		s.options[o.Name] = o.ID
	}
	for _, st := range statuses {
		if s.options[st] == "" {
			return nil, fmt.Errorf("board %s/%d has no Status option %q", owner, project, st)
		}
	}
	return s, nil
}

func (s *sandbox) repo() string { return s.owner + "/" + s.name }

// do runs a GraphQL query or mutation with a raw string, for the mutations
// go-gh's struct tags make awkward.
//
// GitHub refuses a write to the board that races another one with "a
// temporary conflict"; parallel runs hit it, so it is retried.
func (s *sandbox) do(query string, vars map[string]any, out any) error {
	var err error
	for attempt := range 4 {
		if err = s.gql.Do(query, vars, out); err == nil || !strings.Contains(err.Error(), "temporary conflict") {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	return err
}

// issue is a seeded issue: its number and node ID.
type issue struct {
	Number int
	ID     string
	item   string // its board item ID, once on the board
}

// create opens an issue and, when status is not empty, puts it on the board
// in that status. Each later call lands after the earlier ones in board order.
func (s *sandbox) create(title, body, status string, labels ...string) (*issue, error) {
	in := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		in["labels"] = labels
	}
	var out struct {
		Number int    `json:"number"`
		NodeID string `json:"node_id"`
	}
	if err := s.rest.Post(fmt.Sprintf("repos/%s/issues", s.repo()), jsonBody(in), &out); err != nil {
		return nil, fmt.Errorf("create %q: %w", title, err)
	}
	is := &issue{Number: out.Number, ID: out.NodeID}
	if status == "" {
		return is, nil
	}
	if err := s.addToBoard(is); err != nil {
		return is, err
	}
	return is, s.setStatus(is, status)
}

func (s *sandbox) addToBoard(is *issue) error {
	var out struct {
		AddProjectV2ItemByID struct{ Item struct{ ID string } } `json:"addProjectV2ItemById"`
	}
	err := s.do(`mutation($p: ID!, $c: ID!) { addProjectV2ItemById(input: {projectId: $p, contentId: $c}) { item { id } } }`,
		map[string]any{"p": s.projectID, "c": is.ID}, &out)
	if err != nil {
		return fmt.Errorf("add #%d to the board: %w", is.Number, err)
	}
	is.item = out.AddProjectV2ItemByID.Item.ID
	return nil
}

func (s *sandbox) setStatus(is *issue, status string) error {
	err := s.do(`mutation($p: ID!, $i: ID!, $f: ID!, $o: String!) {
  updateProjectV2ItemFieldValue(input: {projectId: $p, itemId: $i, fieldId: $f, value: {singleSelectOptionId: $o}}) { clientMutationId } }`,
		map[string]any{"p": s.projectID, "i": is.item, "f": s.statusField, "o": s.options[status]}, nil)
	if err != nil {
		return fmt.Errorf("set #%d to %s: %w", is.Number, status, err)
	}
	return nil
}

// block marks is blocked by blocker.
func (s *sandbox) block(is, blocker *issue) error {
	err := s.do(`mutation($i: ID!, $b: ID!) { addBlockedBy(input: {issueId: $i, blockingIssueId: $b}) { clientMutationId } }`,
		map[string]any{"i": is.ID, "b": blocker.ID}, nil)
	if err != nil {
		return fmt.Errorf("block #%d by #%d: %w", is.Number, blocker.Number, err)
	}
	return nil
}

func (s *sandbox) comment(is *issue, body string) error {
	return s.rest.Post(fmt.Sprintf("repos/%s/issues/%d/comments", s.repo(), is.Number), jsonBody(map[string]any{"body": body}), nil)
}

// state is what the graders read of an issue.
type state struct {
	Number      int
	Title       string
	State       string // OPEN or CLOSED
	StateReason string // COMPLETED, NOT_PLANNED, … when closed
	Labels      []string
	Status      string // on the sandbox board; "" when not on it or unset
	OnBoard     bool
	BlockedBy   []int
	Comments    []string
}

const stateFields = `number title state stateReason
  labels(first: 20) { nodes { name } }
  blockedBy(first: 20) { nodes { number } }
  comments(first: 50) { nodes { body } }
  projectItems(first: 20) { nodes { project { id } fieldValueByName(name: "Status") { ... on ProjectV2ItemFieldSingleSelectValue { name } } } }`

type stateNode struct {
	Number      int
	Title       string
	State       string
	StateReason *string
	Labels      struct{ Nodes []struct{ Name string } }
	BlockedBy   struct{ Nodes []struct{ Number int } }
	Comments    struct{ Nodes []struct{ Body string } }
	ProjectItem struct {
		Nodes []struct {
			Project          struct{ ID string }
			FieldValueByName *struct{ Name string }
		}
	} `json:"projectItems"`
}

func (s *sandbox) toState(n stateNode) state {
	st := state{Number: n.Number, Title: n.Title, State: n.State}
	if n.StateReason != nil {
		st.StateReason = *n.StateReason
	}
	for _, l := range n.Labels.Nodes {
		st.Labels = append(st.Labels, l.Name)
	}
	for _, b := range n.BlockedBy.Nodes {
		st.BlockedBy = append(st.BlockedBy, b.Number)
	}
	for _, c := range n.Comments.Nodes {
		st.Comments = append(st.Comments, c.Body)
	}
	for _, it := range n.ProjectItem.Nodes {
		if it.Project.ID == s.projectID {
			st.OnBoard = true
			if it.FieldValueByName != nil {
				st.Status = it.FieldValueByName.Name
			}
		}
	}
	return st
}

// read returns the state of an issue.
func (s *sandbox) read(n int) (state, error) {
	var out struct {
		Repository struct{ Issue *stateNode }
	}
	err := s.do(`query($o: String!, $r: String!, $n: Int!) { repository(owner: $o, name: $r) { issue(number: $n) { `+stateFields+` } } }`,
		map[string]any{"o": s.owner, "r": s.name, "n": n}, &out)
	if err != nil {
		return state{}, fmt.Errorf("read #%d: %w", n, err)
	}
	if out.Repository.Issue == nil {
		return state{}, fmt.Errorf("read #%d: no such issue", n)
	}
	return s.toState(*out.Repository.Issue), nil
}

// issues returns the state of every issue in the repository, open or closed,
// whose title starts with prefix, newest first.
func (s *sandbox) issues(prefix string) ([]state, []string, error) {
	var out struct {
		Repository struct {
			Issues struct {
				Nodes []struct {
					ID string
					stateNode
				}
			}
		}
	}
	err := s.do(`query($o: String!, $r: String!) { repository(owner: $o, name: $r) {
  issues(first: 100, orderBy: {field: CREATED_AT, direction: DESC}) { nodes { id `+stateFields+` } } } }`,
		map[string]any{"o": s.owner, "r": s.name}, &out)
	if err != nil {
		return nil, nil, fmt.Errorf("list issues: %w", err)
	}
	var states []state
	var ids []string
	for _, n := range out.Repository.Issues.Nodes {
		if strings.HasPrefix(n.Title, prefix) {
			states = append(states, s.toState(n.stateNode))
			ids = append(ids, n.ID)
		}
	}
	return states, ids, nil
}

// sweep deletes every issue whose title starts with prefix. Deleting needs
// admin rights on the repository.
func (s *sandbox) sweep(prefix string) error {
	_, ids, err := s.issues(prefix)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.do(`mutation($i: ID!) { deleteIssue(input: {issueId: $i}) { clientMutationId } }`, map[string]any{"i": id}, nil); err != nil {
			return fmt.Errorf("delete %s: %w", id, err)
		}
	}
	return nil
}

// busy reports the e2e tests' issues, open or not yet deleted: the two must
// not share the board at the same time.
func (s *sandbox) busy() error {
	st, _, err := s.issues("e2e: ")
	if err != nil {
		return err
	}
	if len(st) > 0 {
		return fmt.Errorf("the e2e tests' issues are in %s (#%d…): wait for them to finish", s.repo(), st[0].Number)
	}
	return nil
}

// settle waits out the lag of the board's reads after a write (#54).
func settle() { time.Sleep(4 * time.Second) }

func hasLabel(st state, l string) bool { return slices.Contains(st.Labels, l) }
