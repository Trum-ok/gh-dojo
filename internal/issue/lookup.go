package issue

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/graph"
)

// Lookup names what a verb's effects need besides the card: label IDs by
// name, a user who can be assigned, the authenticated user. Read fetches
// them in the card's own request (principle 6), so a verb with labels and
// assign still sends one read and one mutation.
type Lookup struct {
	// Labels are label names, each looked up with repository.label(name:),
	// which matches the name exactly, case included.
	Labels []string
	// User is a login to find among the users who can be assigned issues
	// in the repository (assignableUsers), matched without case.
	User string
	// Viewer reads the authenticated user, for assign = "@me".
	Viewer bool
	// AllLabels reads up to 100 of the issue's labels (GitHub's limit per
	// issue) instead of the card's 20, for a verb that removes or checks
	// labels: a label beyond the window would read as absent.
	AllLabels bool
	// AssigneeIDs reads the node IDs of the issue's assignees too
	// (Card.AssigneeIDs), for a verb that unassigns: removing an assignee
	// takes its ID.
	AssigneeIDs bool
	// AllBlockers reads up to 50 of the issue's blockers (GitHub's limit
	// per issue) instead of the card's 20, for a verb that checks them
	// (blockers = "warn" or "deny"): a resolved blocker beyond the window
	// would count as unresolved.
	AllBlockers bool
	// AllSubIssues reads up to 100 of the issue's sub-issues (GitHub's
	// limit per issue) instead of the card's 20, for a verb that checks
	// them (sub_issues = "warn" or "deny"): one beyond the window would
	// count as unresolved.
	AllSubIssues bool
	// Links are issues to read for blocked-by links (see Links), such as
	// the issue --by names for blockers = "link".
	Links Links
	// Milestone is the title of a milestone to set, as given (see
	// MatchMilestone): the repository's open milestones are read for it
	// (the first 100, see MilestonesSelection), for the caller to match.
	Milestone string
}

// Empty reports whether there is nothing to look up.
func (l Lookup) Empty() bool {
	return len(l.Labels) == 0 && l.User == "" && !l.Viewer && !l.AllLabels && !l.AssigneeIDs && !l.AllBlockers &&
		!l.AllSubIssues && len(l.Links) == 0 && l.Milestone == ""
}

// Found is what a Lookup found. Its IDs are for mutations only; never
// print them.
type Found struct {
	// Labels maps each name of Lookup.Labels that exists in the
	// repository to its label node ID; a missing name has no entry.
	Labels map[string]string
	// User is Lookup.User when it can be assigned in the repository, with
	// its login as GitHub spells it; nil otherwise.
	User *User
	// Viewer is the authenticated user when Lookup.Viewer is set.
	Viewer *User
	// Links are the issues of Lookup.Links, in order; a missing one is
	// Linked.Missing, and the card is still read.
	Links []*Linked
	// Milestones are the repository's open milestones for
	// Lookup.Milestone, oldest first; MoreMilestones says it has more than
	// were read (see ReadMilestones).
	Milestones     []OpenMilestone
	MoreMilestones bool
	// Milestone is the open milestone Lookup.Milestone names, once the
	// caller matched it (MatchMilestone) against all of Milestones; nil
	// until then, or when none matches.
	Milestone *OpenMilestone
}

// OpenMilestone is an open milestone of a repository. DueOn is RFC 3339,
// nil without a due date.
type OpenMilestone struct {
	ID    string  `json:"id"` // node ID; never print it
	Title string  `json:"title"`
	DueOn *string `json:"dueOn"`
}

// User is a GitHub user.
type User struct {
	ID    string `json:"id"` // node ID; never print it
	Login string `json:"login"`
}

// assignableWindow is how many assignable users matching Lookup.User are
// read; the login is then matched exactly among them.
const assignableWindow = 100

// MilestonesSelection reads the first 100 open milestones of a
// repository, oldest first, as gh dojo new and a verb that sets a
// milestone read them; past them, ReadMilestones reads them all.
const MilestonesSelection = `milestones(first: 100, states: OPEN, orderBy: {field: NUMBER, direction: ASC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes { id title dueOn }
    }`

// labelsWindow is the labels selection of cardSelection; Lookup.AllLabels
// widens it to allLabelsWindow. assigneesWindow is its assignees
// selection; Lookup.AssigneeIDs adds their IDs (assigneeIDsWindow).
// blockersWindow is its blockers selection; Lookup.AllBlockers widens it
// to allBlockersWindow. subIssuesWindow is its sub-issues selection;
// Lookup.AllSubIssues widens it to allSubIssuesWindow.
const (
	labelsWindow       = "labels(first: 20)"
	allLabelsWindow    = "labels(first: 100)"
	assigneesWindow    = "assignees(first: 10) { totalCount nodes { login } }"
	assigneeIDsWindow  = "assignees(first: 10) { totalCount nodes { id login } }"
	blockersWindow     = "blockedBy(first: 20)"
	allBlockersWindow  = "blockedBy(first: 50)"
	subIssuesWindow    = "subIssues(first: 20)"
	allSubIssuesWindow = "subIssues(first: 100)"
)

// The parts of cardQuery a Lookup extends.
const (
	cardHead = `query IssueCard($owner: String!, $repo: String!, $number: Int!, $comments: Int!, $status: String!`
	cardRepo = `) {
  repository(owner: $owner, name: $repo) {
    issueOrPullRequest(number: $number) ` + cardSelection
)

// labelAlias is the alias of the i-th label lookup and the name of its
// variable.
func labelAlias(i int) string { return fmt.Sprintf("label%d", i) }

// lookupQuery is cardQuery with the fields of l: aliased label lookups,
// the assignable user and the open milestones below the repository, the viewer and the links at
// the top. It returns the extra variables too. An empty l gives cardQuery
// itself.
//
// AllLabels widens the issue's labels window, AllBlockers its blockers
// window, AllSubIssues its sub-issues window; AssigneeIDs reads the
// assignees' IDs.
//
//	query IssueCard(..., $label0: String!, $user: String!) {
//	  repository(owner: $owner, name: $repo) {
//	    issueOrPullRequest(number: $number) { ... }
//	    label0: label(name: $label0) { id name }
//	    assignable: assignableUsers(query: $user, first: 100) { nodes { id login } }
//	  }
//	  viewer { id login }
//	}
func lookupQuery(l Lookup) (string, map[string]any) {
	if l.Empty() {
		return cardQuery, nil
	}
	p := l.parts()
	card := l.widen(cardRepo)
	return cardHead + p.decls + card + p.repo + "\n  }\n" + p.top + "}\n\n" + cardFragments, p.vars
}

// lookupParts are the pieces of a Lookup in a card query: variable
// declarations (each starting with ", "), fields below the repository
// (each on its own line, indented by four spaces), fields at the top of
// the document, and the variables.
type lookupParts struct {
	decls, repo, top string
	vars             map[string]any
}

// parts returns the pieces lookupQuery and batchQuery add for l.
func (l Lookup) parts() lookupParts {
	vars := map[string]any{}
	var decls, repo strings.Builder
	for i, name := range l.Labels {
		alias := labelAlias(i)
		vars[alias] = name
		fmt.Fprintf(&decls, ", $%s: String!", alias)
		fmt.Fprintf(&repo, "\n    %s: label(name: $%s) { id name }", alias, alias)
	}
	if l.User != "" {
		vars["user"] = l.User
		decls.WriteString(", $user: String!")
		fmt.Fprintf(&repo, "\n    assignable: assignableUsers(query: $user, first: %d) { nodes { id login } }", assignableWindow)
	}
	if l.Milestone != "" {
		repo.WriteString("\n    " + MilestonesSelection)
	}
	top := ""
	if l.Viewer {
		top = "  viewer { id login }\n"
	}
	decls.WriteString(l.Links.Decls())
	top += l.Links.Fields()
	l.Links.AddVars(vars)
	return lookupParts{decls: decls.String(), repo: repo.String(), top: top, vars: vars}
}

// widen widens the issue's windows of a card selection: labels for
// Lookup.AllLabels, assignees with their IDs for Lookup.AssigneeIDs,
// blockers for Lookup.AllBlockers, sub-issues for Lookup.AllSubIssues.
func (l Lookup) widen(s string) string {
	if l.AllLabels {
		s = strings.Replace(s, labelsWindow, allLabelsWindow, 1)
	}
	if l.AssigneeIDs {
		s = strings.Replace(s, assigneesWindow, assigneeIDsWindow, 1)
	}
	if l.AllBlockers {
		s = strings.Replace(s, blockersWindow, allBlockersWindow, 1)
	}
	if l.AllSubIssues {
		s = strings.Replace(s, subIssuesWindow, allSubIssuesWindow, 1)
	}
	return s
}

// repoJSON is the repository of a card response: the issue, and the
// lookups a Lookup added under their aliases.
type repoJSON struct {
	IssueOrPullRequest *issueJSON
	Assignable         *conn[*User]
	Milestones         *graph.Connection[OpenMilestone]
	labels             map[string]*labelJSON // by alias
}

type labelJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (r *repoJSON) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	return r.fromFields(fields)
}

// fromFields fills r from the fields of the repository object, decoded
// once by the caller; fields it does not know are skipped.
func (r *repoJSON) fromFields(fields map[string]json.RawMessage) error {
	for key, raw := range fields {
		var err error
		switch {
		case key == "issueOrPullRequest":
			err = json.Unmarshal(raw, &r.IssueOrPullRequest)
		case key == "assignable":
			err = json.Unmarshal(raw, &r.Assignable)
		case key == "milestones":
			err = json.Unmarshal(raw, &r.Milestones)
		case strings.HasPrefix(key, "label"):
			if r.labels == nil {
				r.labels = map[string]*labelJSON{}
			}
			var l labelJSON
			if err = json.Unmarshal(raw, &l); err == nil && l.ID != "" {
				r.labels[key] = &l
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// found turns the lookups of a response into Found.
func (l Lookup) found(r *repoJSON, viewer *User, data map[string]json.RawMessage) (*Found, error) {
	f := &Found{Labels: map[string]string{}}
	if r != nil {
		for i, name := range l.Labels {
			if lb := r.labels[labelAlias(i)]; lb != nil {
				f.Labels[name] = lb.ID
			}
		}
		if l.Milestone != "" && r.Milestones != nil {
			f.Milestones = r.Milestones.Nodes
			f.MoreMilestones = r.Milestones.PageInfo.HasNextPage
		}
		if l.User != "" && r.Assignable != nil {
			for _, u := range r.Assignable.Nodes {
				if u != nil && u.ID != "" && strings.EqualFold(u.Login, l.User) {
					f.User = u
					break
				}
			}
		}
	}
	if l.Viewer && viewer != nil && viewer.ID != "" {
		f.Viewer = viewer
	}
	if len(l.Links) > 0 {
		links, err := l.Links.Decode(data)
		if err != nil {
			return nil, err
		}
		f.Links = links
	}
	return f, nil
}
