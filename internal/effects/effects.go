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
	AliasBoard     = "board"
	AliasUnarchive = "unarchive"
	AliasStatus    = "status"
	AliasComment   = "comment"
)

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
}

// Mutation is one GraphQL mutation document with its variables.
type Mutation struct {
	Query string
	Vars  map[string]any
	// Steps are the ops of the document in order, each with its alias.
	Steps []Step
}

// Step is one op of a Mutation.
type Step struct {
	Alias string
	Op    workflow.Op
}

// Batches splits ops into the mutations to send, in order. Usually that is
// one. An issue not on the board whose status must be set takes two:
// addProjectV2ItemById returns the item ID the status update needs, and
// GraphQL cannot feed one field's result into another field of the same
// document. Adding to the board alongside a comment alone fits in one.
func Batches(ops []workflow.Op) [][]workflow.Op {
	i := slices.IndexFunc(ops, func(op workflow.Op) bool { _, ok := op.(workflow.AddToBoard); return ok })
	if i < 0 || !slices.ContainsFunc(ops, func(op workflow.Op) bool { _, ok := op.(workflow.SetStatus); return ok }) {
		if len(ops) == 0 {
			return nil
		}
		return [][]workflow.Op{ops}
	}
	rest := slices.Delete(slices.Clone(ops), i, i+1)
	return [][]workflow.Op{{ops[i]}, rest}
}

// Build makes one mutation document from ops: at most one op of each kind,
// with the IDs it needs. A SetStatus without ids.Item is an error: add the
// issue to the board first (see Batches).
//
//	mutation Verb($project: ID!, $item: ID!, $field: ID!, $option: String!, $issue: ID!, $body: String!) {
//	  status: updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, value: {singleSelectOptionId: $option}}) { projectV2Item { id } }
//	  comment: addComment(input: {subjectId: $issue, body: $body}) { commentEdge { node { id } } }
//	}
func Build(ops []workflow.Op, ids IDs) (*Mutation, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("effects: no ops to build a mutation from")
	}
	m := &Mutation{Vars: map[string]any{}}
	var decls, fields []string
	declare := func(name, typ string, value string) error {
		if value == "" {
			return fmt.Errorf("effects: no value for $%s", name)
		}
		if _, ok := m.Vars[name]; ok {
			return nil
		}
		m.Vars[name] = value
		decls = append(decls, "$"+name+": "+typ)
		return nil
	}
	for _, op := range ops {
		var (
			alias, field string
			err          error
		)
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
			if ids.Item == "" {
				return nil, fmt.Errorf("effects: setting the status of an issue not on the board; add it in an earlier mutation")
			}
			err = firstErr(declare("project", "ID!", ids.Project), declare("item", "ID!", ids.Item),
				declare("field", "ID!", ids.StatusField), declare("option", "String!", ids.StatusOption))
			field = "updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, " +
				"value: {singleSelectOptionId: $option}}) { projectV2Item { id } }"
		case workflow.AddComment:
			alias = AliasComment
			err = firstErr(declare("issue", "ID!", ids.Issue), declare("body", "String!", op.Text()))
			field = "addComment(input: {subjectId: $issue, body: $body}) { commentEdge { node { id } } }"
		default:
			return nil, fmt.Errorf("effects: unknown op %T", op)
		}
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(m.Steps, func(s Step) bool { return s.Alias == alias }) {
			return nil, fmt.Errorf("effects: two %s ops in one mutation", alias)
		}
		m.Steps = append(m.Steps, Step{Alias: alias, Op: op})
		fields = append(fields, "  "+alias+": "+field)
	}
	m.Query = "mutation " + OperationName + "(" + strings.Join(decls, ", ") + ") {\n" +
		strings.Join(fields, "\n") + "\n}"
	return m, nil
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
// when there was none): done are those whose field came back non-null,
// missed the rest, each in document order. GraphQL nulls a field that
// failed, so with a partial error done is what took effect.
func (m *Mutation) Outcome(data map[string]json.RawMessage) (done, missed []workflow.Op) {
	for _, s := range m.Steps {
		if v, ok := data[s.Alias]; ok && !isNull(v) {
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
// "not added to the board", "not unarchived", "status not changed",
// "comment not posted".
func Missed(op workflow.Op) string {
	switch op.(type) {
	case workflow.AddToBoard:
		return "not added to the board"
	case workflow.Unarchive:
		return "not unarchived"
	case workflow.SetStatus:
		return "status not changed"
	case workflow.AddComment:
		return "comment not posted"
	}
	return op.Describe() + " not done"
}
