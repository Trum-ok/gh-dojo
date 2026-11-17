package effects

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/graph"
)

// CreateIssueMutation creates the issue with its labels, milestone, parent
// and assignees ($milestone, $parent and $assignees omitted for none). It
// is put on the board by AddToBoardMutation, not createIssue's
// projectV2Ids, which GitHub adds asynchronously, without returning the
// item (#17).
const CreateIssueMutation = `mutation CreateIssue($repo: ID!, $title: String!, $body: String!, $labels: [ID!], $milestone: ID, $parent: ID, $assignees: [ID!]) {
  createIssue(input: {repositoryId: $repo, title: $title, body: $body, labelIds: $labels, milestoneId: $milestone, parentIssueId: $parent, assigneeIds: $assignees}) {
    issue { id number }
  }
}`

// AddLabelsMutation adds labels a resumed issue lacks: $issue, $labels.
const AddLabelsMutation = "mutation AddLabels($issue: ID!, $labels: [ID!]!) {\n  " +
	addLabelsHead + "labels" + addLabelsTail + "\n}"

// SetMilestoneMutation sets the milestone of a resumed issue that has
// none: $issue, $milestone.
const SetMilestoneMutation = `mutation SetMilestone($issue: ID!, $milestone: ID!) {
  updateIssue(input: {id: $issue, milestoneId: $milestone}) { issue { id } }
}`

// UnarchiveMutation restores a resumed issue's archived board item:
// $project, $item.
const UnarchiveMutation = "mutation Unarchive($project: ID!, $item: ID!) {\n  " + unarchiveField + "\n}"

// AddToBoardMutation adds the issue to the board, or returns its item if
// it is there already: $project, $issue.
const AddToBoardMutation = "mutation AddToBoard($project: ID!, $issue: ID!) {\n  " + addToBoardField + "\n}"

// SetStatusMutation sets the status of a board item: $project, $item,
// $field, $option.
const SetStatusMutation = "mutation SetStatus($project: ID!, $item: ID!, $field: ID!, $option: String!) {\n  " +
	setStatusInput + " {\n    projectV2Item { id }\n  }\n}"

// AliasParent is the alias of the addSubIssue field of NewFinish.
const AliasParent = "parent"

// LinkAlias is the alias of the i-th addBlockedBy field of NewFinish, and
// the name of its variable: link0, link1, ...
func LinkAlias(i int) string { return fmt.Sprintf("link%d", i) }

// NewFinish is new's last mutation: what is left once the issue is on the
// board, one aliased field each, the status first.
//
//	mutation NewFinish($issue: ID!, $project: ID!, $item: ID!, $field: ID!, $option: String!, $assignees: [ID!]!, $link0: ID!, $parent: ID!) {
//	  status: updateProjectV2ItemFieldValue(...) { projectV2Item { id } }
//	  assign: addAssigneesToAssignable(input: {assignableId: $issue, assigneeIds: $assignees}) { clientMutationId }
//	  link0: addBlockedBy(input: {issueId: $issue, blockingIssueId: $link0}) { issue { id } }
//	  parent: addSubIssue(input: {issueId: $parent, subIssueId: $issue}) { issue { id } }
//	}
//
// Its fields come back under AliasStatus, AliasAssign, LinkAlias(i) and
// AliasParent; see Answered.
type NewFinish struct {
	Status    bool     // set the status: IDs.Project, Item, StatusField and StatusOption
	Assignees []string // node IDs of the users to assign
	Links     []string // node IDs of the issues that block it
	Parent    string   // node ID of the issue to make it a sub-issue of; "" for none
}

// Build makes f's document and variables for the issue ids.Issue.
func (f NewFinish) Build(ids IDs) *Mutation {
	vars := map[string]any{"issue": ids.Issue}
	decls := []string{"$issue: ID!"}
	var fields []string
	if f.Status {
		vars["project"], vars["item"], vars["field"], vars["option"] = ids.Project, ids.Item, ids.StatusField, ids.StatusOption
		decls = append(decls, "$project: ID!", "$item: ID!", "$field: ID!", "$option: String!")
		fields = append(fields, "  "+AliasStatus+": "+setStatusInput+" { projectV2Item { id } }")
	}
	if len(f.Assignees) > 0 {
		vars["assignees"] = f.Assignees
		decls = append(decls, "$assignees: [ID!]!")
		fields = append(fields, "  "+AliasAssign+": "+assignField)
	}
	for i, id := range f.Links {
		a := LinkAlias(i)
		vars[a] = id
		decls = append(decls, "$"+a+": ID!")
		fields = append(fields, "  "+a+": "+BlockedByField(true, "issue", a))
	}
	if f.Parent != "" {
		vars["parent"] = f.Parent
		decls = append(decls, "$parent: ID!")
		fields = append(fields, "  "+AliasParent+": addSubIssue(input: {issueId: $parent, subIssueId: $issue}) { issue { id } }")
	}
	return &Mutation{
		Query: "mutation NewFinish(" + strings.Join(decls, ", ") + ") {\n" + strings.Join(fields, "\n") + "\n}",
		Vars:  vars,
	}
}

// Answered reports whether the field alias came back non-null in data,
// the "data" object of a response: GraphQL nulls a field that failed, so
// it took effect.
func Answered(data map[string]json.RawMessage, alias string) bool {
	v, ok := data[alias]
	return ok && !graph.IsNull(v)
}
