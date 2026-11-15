// Package effects turns the ops of a workflow.Plan into GraphQL mutations:
// each op is one aliased field, and all ops of a verb go into one mutation
// document (principle 6). It builds text and variables only; sending them
// is the caller's job.
//
//	ids := effects.IDs{Project: schema.ID, Issue: card.ID, Item: card.ItemID(), ...}
//	for _, batch := range effects.Batches(plan.Ops) {
//		m, err := effects.Build(batch, ids)
//		var data map[string]json.RawMessage
//		err = client.Mutate(ctx, m.Query, m.Vars, &data)
//		done, missed := m.Outcome(data)
//		ids.Item = m.ItemID(data, ids.Item)
//	}
//
// GraphQL runs the fields of a mutation in order and fails them one by one:
// a rejected status change leaves the comment after it posted. Outcome
// tells which ops took effect from the fields that came back non-null.
package effects

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// OperationName names every mutation document Build makes; graphtest
// fixtures are keyed by it.
const OperationName = "Verb"

// Aliases of the mutation fields, one per kind of op. They key the response
// data and the paths of GraphQL errors.
const (
	AliasBoard        = "board"
	AliasUnarchive    = "unarchive"
	AliasReopen       = "reopen"
	AliasStatus       = "status"
	AliasAddLabels    = "addLabels"
	AliasRemoveLabels = "removeLabels"
	AliasAssign       = "assign"
	AliasUnassign     = "unassign"
	AliasComment      = "comment"
	AliasClose        = "close"
	AliasIteration    = "sprint"
	AliasMilestone    = "milestone"
)

// FieldAlias is the alias of the i-th field a SetFields op sets: field0,
// field1, ...
func FieldAlias(i int) string { return fmt.Sprintf("field%d", i) }

// BlockedByAlias is the alias of the i-th AddBlockedBy op: blockedBy0,
// blockedBy1, ...; its variable is blocker0, blocker1, ...
func BlockedByAlias(i int) string { return fmt.Sprintf("blockedBy%d", i) }

// IDs are the node IDs the ops need. Never print them.
type IDs struct {
	Project string // the board (ProjectV2) node ID
	Issue   string // the issue node ID: the content added to the board and the comment subject
	// Item is the issue's board item, "" while the issue is not on the
	// board; Unarchive and SetStatus need it (see Batches).
	Item string
	// StatusField is the ID of the status field and StatusOption the ID of
	// the option to set; SetStatus needs both.
	StatusField  string
	StatusOption string
	// Labels maps label names to label node IDs, Users logins to user node
	// IDs; AddLabels, RemoveLabels, Assign and Unassign look their names
	// up here, ignoring case.
	Labels map[string]string
	Users  map[string]string
	// SprintField is the ID of the sprint field and Iteration the ID of
	// the iteration to set; SetIteration needs the field, and the
	// iteration unless it clears the field.
	SprintField string
	Iteration   string
	// Fields maps the board field names of a SetFields op to their IDs
	// and the values to write.
	Fields map[string]FieldInput
	// Blockers maps the Ref of each AddBlockedBy op to the node ID of the
	// issue it links as blocking the issue.
	Blockers map[string]string
	// Milestone is the node ID of the milestone SetMilestone sets.
	Milestone string
}

// FieldInput is a board field to set: its node ID and the value, a
// ProjectV2FieldValue input such as {"singleSelectOptionId": "..."} or
// {"number": 3} (see board.Schema.FieldInput).
type FieldInput struct {
	ID    string
	Value map[string]any
}

// Mutation is one GraphQL mutation document with its variables.
type Mutation struct {
	Query string
	Vars  map[string]any
	// Steps are the ops of the document in order, each with its alias.
	Steps []Step
}

// Step is one field of a Mutation and the op it applies. An op that sets
// several board fields (SetFields) has a step per field.
type Step struct {
	Alias string
	Op    workflow.Op

	op int // index of Op in the ops built
}

// needsItem reports whether op writes a field of the board item, so it
// needs the item ID.
func needsItem(op workflow.Op) bool {
	switch op.(type) {
	case workflow.SetStatus, workflow.SetIteration, workflow.SetFields:
		return true
	}
	return false
}

// Batches splits ops into the mutations to send, in order. Usually that is
// one. An issue not on the board whose item fields (status, sprint, other
// fields) must be set takes two: addProjectV2ItemById returns the item ID
// the field updates need, and GraphQL cannot feed one field's result into
// another field of the same document. Adding to the board alongside a
// comment alone fits in one.
func Batches(ops []workflow.Op) [][]workflow.Op {
	i := slices.IndexFunc(ops, func(op workflow.Op) bool { _, ok := op.(workflow.AddToBoard); return ok })
	if i < 0 || !slices.ContainsFunc(ops, needsItem) {
		if len(ops) == 0 {
			return nil
		}
		return [][]workflow.Op{ops}
	}
	rest := slices.Delete(slices.Clone(ops), i, i+1)
	return [][]workflow.Op{{ops[i]}, rest}
}

// Build makes one mutation document from ops: at most one op of each kind
// (AddBlockedBy one per blocking issue), with the IDs it needs. A SetStatus without ids.Item is an error: add the
// issue to the board first (see Batches).
//
//	mutation Verb($project: ID!, $item: ID!, $field: ID!, $option: String!, $issue: ID!, $body: String!, $reason: IssueClosedStateReason!) {
//	  status: updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, value: {singleSelectOptionId: $option}}) { projectV2Item { id } }
//	  comment: addComment(input: {subjectId: $issue, body: $body}) { commentEdge { node { id } } }
//	  close: closeIssue(input: {issueId: $issue, stateReason: $reason}) { issue { id } }
//	}
//
// A label or user missing from ids is an error: the reader looks them up
// with the card (see issue.Lookup).
func Build(ops []workflow.Op, ids IDs) (*Mutation, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("effects: no ops to build a mutation from")
	}
	m := &Mutation{Vars: map[string]any{}}
	var decls, fields []string
	declare := func(name, typ string, value any) error {
		switch v := value.(type) {
		case string:
			if v == "" {
				return fmt.Errorf("effects: no value for $%s", name)
			}
		case []string:
			if len(v) == 0 {
				return fmt.Errorf("effects: no value for $%s", name)
			}
		}
		if _, ok := m.Vars[name]; ok {
			return nil
		}
		m.Vars[name] = value
		decls = append(decls, "$"+name+": "+typ)
		return nil
	}
	blockers := 0 // AddBlockedBy ops so far
	for n, op := range ops {
		var (
			alias, field string
			err          error
		)
		if needsItem(op) && ids.Item == "" {
			return nil, fmt.Errorf("effects: setting a field of an issue not on the board; add it in an earlier mutation")
		}
		switch op := op.(type) {
		case workflow.AddToBoard:
			alias = AliasBoard
			err = firstErr(declare("project", "ID!", ids.Project), declare("issue", "ID!", ids.Issue))
			field = "addProjectV2ItemById(input: {projectId: $project, contentId: $issue}) { item { id } }"
		case workflow.Unarchive:
			alias = AliasUnarchive
			err = firstErr(declare("project", "ID!", ids.Project), declare("item", "ID!", ids.Item))
			field = "unarchiveProjectV2Item(input: {projectId: $project, itemId: $item}) { item { id } }"
		case workflow.SetStatus:
			alias = AliasStatus
			err = firstErr(declare("project", "ID!", ids.Project), declare("item", "ID!", ids.Item),
				declare("field", "ID!", ids.StatusField), declare("option", "String!", ids.StatusOption))
			field = "updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, " +
				"value: {singleSelectOptionId: $option}}) { projectV2Item { id } }"
		case workflow.Reopen:
			alias = AliasReopen
			err = declare("issue", "ID!", ids.Issue)
			field = "reopenIssue(input: {issueId: $issue}) { issue { id } }"
		case workflow.SetMilestone:
			alias = AliasMilestone
			err = firstErr(declare("issue", "ID!", ids.Issue), declare("milestone", "ID!", ids.Milestone))
			field = "updateIssue(input: {id: $issue, milestoneId: $milestone}) { issue { id } }"
		case workflow.AddLabels:
			alias = AliasAddLabels
			var labels []string
			labels, err = lookupAll("label", ids.Labels, op.Names)
			err = firstErr(err, declare("issue", "ID!", ids.Issue), declare("addLabels", "[ID!]!", labels))
			field = "addLabelsToLabelable(input: {labelableId: $issue, labelIds: $addLabels}) { clientMutationId }"
		case workflow.RemoveLabels:
			alias = AliasRemoveLabels
			var labels []string
			labels, err = lookupAll("label", ids.Labels, op.Names)
			err = firstErr(err, declare("issue", "ID!", ids.Issue), declare("removeLabels", "[ID!]!", labels))
			field = "removeLabelsFromLabelable(input: {labelableId: $issue, labelIds: $removeLabels}) { clientMutationId }"
		case workflow.Assign:
			alias = AliasAssign
			var users []string
			users, err = lookupAll("user", ids.Users, []string{op.Login})
			err = firstErr(err, declare("issue", "ID!", ids.Issue), declare("assignees", "[ID!]!", users))
			field = "addAssigneesToAssignable(input: {assignableId: $issue, assigneeIds: $assignees}) { clientMutationId }"
		case workflow.Unassign:
			alias = AliasUnassign
			var users []string
			users, err = lookupAll("user", ids.Users, op.Logins)
			err = firstErr(err, declare("issue", "ID!", ids.Issue), declare("unassignees", "[ID!]!", users))
			field = "removeAssigneesFromAssignable(input: {assignableId: $issue, assigneeIds: $unassignees}) { clientMutationId }"
		case workflow.AddBlockedBy:
			alias = BlockedByAlias(blockers)
			v := fmt.Sprintf("blocker%d", blockers)
			blockers++
			id := ids.Blockers[op.Ref]
			if id == "" {
				return nil, fmt.Errorf("effects: no ID for blocking issue %s", op.Ref)
			}
			err = firstErr(declare("issue", "ID!", ids.Issue), declare(v, "ID!", id))
			field = BlockedByField(true, "issue", v)
		case workflow.AddComment:
			alias = AliasComment
			err = firstErr(declare("issue", "ID!", ids.Issue), declare("body", "String!", op.Text()))
			field = "addComment(input: {subjectId: $issue, body: $body}) { commentEdge { node { id } } }"
		case workflow.Close:
			alias = AliasClose
			err = firstErr(declare("issue", "ID!", ids.Issue),
				declare("reason", "IssueClosedStateReason!", strings.ToUpper(op.Reason)))
			field = "closeIssue(input: {issueId: $issue, stateReason: $reason}) { issue { id } }"
		case workflow.SetIteration:
			alias = AliasIteration
			err = firstErr(declare("project", "ID!", ids.Project), declare("item", "ID!", ids.Item),
				declare("sprintField", "ID!", ids.SprintField))
			if op.To == "" {
				field = "clearProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $sprintField}) " +
					"{ projectV2Item { id } }"
				break
			}
			err = firstErr(err, declare("iteration", "String!", ids.Iteration))
			field = "updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $sprintField, " +
				"value: {iterationId: $iteration}}) { projectV2Item { id } }"
		case workflow.SetFields:
			// One aliased update per field; Outcome counts the op done
			// only when all of them are.
			if len(op.Changes) == 0 {
				return nil, fmt.Errorf("effects: SetFields without fields")
			}
			if err := firstErr(declare("project", "ID!", ids.Project), declare("item", "ID!", ids.Item)); err != nil {
				return nil, err
			}
			for i, c := range op.Changes {
				in, ok := ids.Fields[c.Field]
				if !ok || in.ID == "" || len(in.Value) == 0 {
					return nil, fmt.Errorf("effects: no ID or value for field %q", c.Field)
				}
				alias := FieldAlias(i)
				if err := firstErr(declare(alias, "ID!", in.ID),
					declare(alias+"Value", "ProjectV2FieldValue!", in.Value)); err != nil {
					return nil, err
				}
				if err := m.add(alias, op, n); err != nil {
					return nil, err
				}
				fields = append(fields, "  "+alias+": updateProjectV2ItemFieldValue(input: {projectId: $project, "+
					"itemId: $item, fieldId: $"+alias+", value: $"+alias+"Value}) { projectV2Item { id } }")
			}
			continue
		default:
			return nil, fmt.Errorf("effects: unknown op %T", op)
		}
		if err != nil {
			return nil, err
		}
		if err := m.add(alias, op, n); err != nil {
			return nil, err
		}
		fields = append(fields, "  "+alias+": "+field)
	}
	m.Query = "mutation " + OperationName + "(" + strings.Join(decls, ", ") + ") {\n" +
		strings.Join(fields, "\n") + "\n}"
	return m, nil
}

// add appends the step of the n-th op; an alias already used means two
// ops of one kind.
func (m *Mutation) add(alias string, op workflow.Op, n int) error {
	if slices.ContainsFunc(m.Steps, func(s Step) bool { return s.Alias == alias }) {
		return fmt.Errorf("effects: two %s ops in one mutation", alias)
	}
	m.Steps = append(m.Steps, Step{Alias: alias, Op: op, op: n})
	return nil
}

// BlockedByField is the mutation field that adds (add) or removes a
// blocked-by link: the issue in $<issue> is blocked by the issue in
// $<blocker>, both node IDs. It answers the blocked issue's ID, so a
// non-null result means the field took effect.
//
//	addBlockedBy(input: {issueId: $issue, blockingIssueId: $blocker}) { issue { id } }
func BlockedByField(add bool, issue, blocker string) string {
	name := "removeBlockedBy"
	if add {
		name = "addBlockedBy"
	}
	return name + "(input: {issueId: $" + issue + ", blockingIssueId: $" + blocker + "}) { issue { id } }"
}

// lookupAll returns the IDs of names in ids, ignoring case.
func lookupAll(kind string, ids map[string]string, names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	for _, name := range names {
		id := ids[name]
		if id == "" {
			for k, v := range ids {
				if strings.EqualFold(k, name) {
					id = v
				}
			}
		}
		if id == "" {
			return nil, fmt.Errorf("effects: no ID for %s %q", kind, name)
		}
		out = append(out, id)
	}
	return out, nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Outcome splits the ops of m by the response data (the "data" object, nil
// when there was none): done are those whose fields all came back
// non-null, missed the rest, each in document order. GraphQL nulls a field
// that failed, so with a partial error done is what took effect. An op
// with several fields (SetFields) that took effect in part is split: a
// SetFields with the fields set is done, one with the rest missed, so a
// partial report names each field.
func (m *Mutation) Outcome(data map[string]json.RawMessage) (done, missed []workflow.Op) {
	answered := func(alias string) bool {
		v, found := data[alias]
		return found && !isNull(v)
	}
	for i, s := range m.Steps {
		if i > 0 && m.Steps[i-1].op == s.op {
			continue // judged with the op's first step
		}
		if set, ok := s.Op.(workflow.SetFields); ok {
			var got, lost []workflow.FieldChange
			for k, t := range m.Steps[i:] { // a step per change, in order
				if t.op != s.op {
					break
				}
				if answered(t.Alias) {
					got = append(got, set.Changes[k])
				} else {
					lost = append(lost, set.Changes[k])
				}
			}
			if len(got) > 0 {
				done = append(done, workflow.SetFields{Changes: got})
			}
			if len(lost) > 0 {
				missed = append(missed, workflow.SetFields{Changes: lost})
			}
			continue
		}
		ok := true
		for _, t := range m.Steps[i:] {
			if t.op != s.op {
				break
			}
			if !answered(t.Alias) {
				ok = false
			}
		}
		if ok {
			done = append(done, s.Op)
		} else {
			missed = append(missed, s.Op)
		}
	}
	return done, missed
}

// ItemID returns the board item ID that addProjectV2ItemById returned in
// data, or item when m has no AddToBoard or it did not succeed.
func (m *Mutation) ItemID(data map[string]json.RawMessage, item string) string {
	raw, ok := data[AliasBoard]
	if !ok || isNull(raw) {
		return item
	}
	var out struct {
		Item *struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Item == nil || out.Item.ID == "" {
		return item
	}
	return out.Item.ID
}

func isNull(v json.RawMessage) bool {
	s := strings.TrimSpace(string(v))
	return s == "" || s == "null"
}

// Missed says that op did not take effect, for a partial report:
// "not added to the board", "not unarchived", "not reopened", "status not
// changed", "labels not added", "labels not removed", "not assigned",
// "not unassigned", "not blocked by #12", "comment not posted", "not closed", "sprint not
// set", "sprint not cleared", "Priority not set", "Priority and Estimate
// not set", "milestone not set".
func Missed(op workflow.Op) string {
	switch op := op.(type) {
	case workflow.AddToBoard:
		return "not added to the board"
	case workflow.Unarchive:
		return "not unarchived"
	case workflow.Reopen:
		return "not reopened"
	case workflow.SetStatus:
		return "status not changed"
	case workflow.SetMilestone:
		return "milestone not set"
	case workflow.AddLabels:
		return "labels not added"
	case workflow.RemoveLabels:
		return "labels not removed"
	case workflow.Assign:
		return "not assigned"
	case workflow.Unassign:
		return "not unassigned"
	case workflow.AddBlockedBy:
		return "not blocked by " + op.Ref
	case workflow.AddComment:
		return "comment not posted"
	case workflow.Close:
		return "not closed"
	case workflow.SetIteration:
		if op.To == "" {
			return "sprint not cleared"
		}
		return "sprint not set"
	case workflow.SetFields:
		names := make([]string, len(op.Changes))
		for i, c := range op.Changes {
			names[i] = c.Field
		}
		if n := len(names); n > 1 {
			return strings.Join(names[:n-1], ", ") + " and " + names[n-1] + " not set"
		}
		return strings.Join(names, "") + " not set"
	}
	return op.Describe() + " not done"
}
