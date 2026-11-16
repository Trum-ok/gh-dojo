package effects_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/effects"
)

// TestNewFinish: the status first, then the assignees, the links and the
// parent, each under its alias; only what is asked for.
func TestNewFinish(t *testing.T) {
	ids := effects.IDs{Project: "PRJ", Issue: "I57", Item: "IT1", StatusField: "F1", StatusOption: "O1"}
	m := effects.NewFinish{Status: true, Assignees: []string{"U1", "U2"}, Links: []string{"B12", "B13"}, Parent: "P40"}.Build(ids)
	want := `mutation NewFinish($issue: ID!, $project: ID!, $item: ID!, $field: ID!, $option: String!, $assignees: [ID!]!, $link0: ID!, $link1: ID!, $parent: ID!) {
  status: updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, value: {singleSelectOptionId: $option}}) { projectV2Item { id } }
  assign: addAssigneesToAssignable(input: {assignableId: $issue, assigneeIds: $assignees}) { clientMutationId }
  link0: addBlockedBy(input: {issueId: $issue, blockingIssueId: $link0}) { issue { id } }
  link1: addBlockedBy(input: {issueId: $issue, blockingIssueId: $link1}) { issue { id } }
  parent: addSubIssue(input: {issueId: $parent, subIssueId: $issue}) { issue { id } }
}`
	if m.Query != want {
		t.Errorf("query =\n%s\nwant\n%s", m.Query, want)
	}
	wantVars := map[string]any{"issue": "I57", "project": "PRJ", "item": "IT1", "field": "F1", "option": "O1",
		"assignees": []string{"U1", "U2"}, "link0": "B12", "link1": "B13", "parent": "P40"}
	if !reflect.DeepEqual(m.Vars, wantVars) {
		t.Errorf("vars = %v, want %v", m.Vars, wantVars)
	}

	m = effects.NewFinish{Links: []string{"B12"}}.Build(ids)
	want = `mutation NewFinish($issue: ID!, $link0: ID!) {
  link0: addBlockedBy(input: {issueId: $issue, blockingIssueId: $link0}) { issue { id } }
}`
	if m.Query != want || !reflect.DeepEqual(m.Vars, map[string]any{"issue": "I57", "link0": "B12"}) {
		t.Errorf("links only: %s %v", m.Query, m.Vars)
	}
}

func TestAnswered(t *testing.T) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"status": {"projectV2Item": {"id": "IT1"}}, "link0": null}`), &data); err != nil {
		t.Fatal(err)
	}
	for alias, want := range map[string]bool{effects.AliasStatus: true, effects.LinkAlias(0): false, effects.AliasParent: false} {
		if got := effects.Answered(data, alias); got != want {
			t.Errorf("Answered(%s) = %v, want %v", alias, got, want)
		}
	}
	if effects.Answered(nil, effects.AliasStatus) {
		t.Error("no data answered")
	}
}
