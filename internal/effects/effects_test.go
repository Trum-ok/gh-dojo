package effects_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/effects"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

var (
	toBoard   = workflow.AddToBoard{}
	unarchive = workflow.Unarchive{}
	status    = workflow.SetStatus{From: "Backlog", To: "In progress"}
	comment   = workflow.AddComment{Body: "Started.", Marker: workflow.Marker("start", "Started.")}
	reopen    = workflow.Reopen{}
	addLabels = workflow.AddLabels{Names: []string{"in-review", "Needs QA"}}
	rmLabels  = workflow.RemoveLabels{Names: []string{"blocked"}}
	assign    = workflow.Assign{Login: "Trum-ok"}
	unassign  = workflow.Unassign{Logins: []string{"octocat", "Hubot"}, All: true}
	closing   = workflow.Close{Reason: "not_planned"}
	sprint    = workflow.SetIteration{From: "Sprint 11", To: "Sprint 12", Start: "2026-11-09"}
	unsprint  = workflow.SetIteration{From: "Sprint 12"}
	setFields = workflow.SetFields{Changes: []workflow.FieldChange{
		{Field: "Estimate", To: "3"}, {Field: "Priority", From: "P2", To: "P1"},
	}}
	blockedBy = workflow.AddBlockedBy{Ref: "#12"}
	blocked2  = workflow.AddBlockedBy{Ref: "acme/api#7"}
	milestone = workflow.SetMilestone{From: "v0.2", To: "v0.3 — extensibility"}
)

var ids = effects.IDs{
	Project: "PVT_1", Issue: "I_1", Item: "PVTI_1", StatusField: "PVTSSF_1", StatusOption: "47fc9ee4",
	Labels:      map[string]string{"in-review": "LA_1", "needs qa": "LA_2", "blocked": "LA_3"},
	Users:       map[string]string{"trum-ok": "U_1", "octocat": "U_2", "hubot": "U_3"},
	SprintField: "PVTIF_1", Iteration: "it12",
	Fields: map[string]effects.FieldInput{
		"Estimate": {ID: "PVTF_est", Value: map[string]any{"number": 3.0}},
		"Priority": {ID: "PVTSSF_prio", Value: map[string]any{"singleSelectOptionId": "0a877460"}},
	},
	Blockers:  map[string]string{"#12": "I_12", "acme/api#7": "I_7"},
	Milestone: "MI_3",
}

func TestBuild(t *testing.T) {
	notOnBoard := ids
	notOnBoard.Item = ""
	tests := []struct {
		name string
		ops  []workflow.Op
		ids  effects.IDs
	}{
		{"status_comment", []workflow.Op{status, comment}, ids},
		{"status", []workflow.Op{status}, ids},
		{"comment", []workflow.Op{comment}, ids},
		{"board", []workflow.Op{toBoard}, notOnBoard},
		{"board_comment", []workflow.Op{toBoard, comment}, notOnBoard},
		{"unarchive_status_comment", []workflow.Op{unarchive, status, comment}, ids},
		{"comment_close", []workflow.Op{comment, workflow.Close{Reason: "not_planned"}}, ids},
		{"status_close_completed", []workflow.Op{status, workflow.Close{Reason: "completed"}}, ids},
		{"close_duplicate", []workflow.Op{workflow.Close{Reason: "duplicate"}}, ids},
		{"reopen_status", []workflow.Op{reopen, status}, ids},
		{"labels_assign", []workflow.Op{addLabels, rmLabels, assign}, ids},
		{"status_unassign_assign_comment", []workflow.Op{status, unassign, assign, comment}, ids},
		{"all", []workflow.Op{unarchive, reopen, status, addLabels, rmLabels, assign, comment}, ids},
		{"board_labels_close", []workflow.Op{toBoard, addLabels, comment, closing}, notOnBoard},
		{"status_sprint_close", []workflow.Op{status, sprint, closing}, ids},
		{"sprint_clear", []workflow.Op{unsprint}, ids},
		{"status_fields", []workflow.Op{status, setFields, comment}, ids},
		{"status_blocked_by_comment", []workflow.Op{status, blockedBy, comment}, ids},
		{"status_blocked_by_two", []workflow.Op{status, blockedBy, blocked2, comment}, ids},
		{"milestone_comment", []workflow.Op{milestone, comment}, ids},
		{"board_milestone", []workflow.Op{toBoard, milestone}, notOnBoard},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := effects.Build(tt.ops, tt.ids)
			if err != nil {
				t.Fatal(err)
			}
			var vars strings.Builder
			enc := json.NewEncoder(&vars)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(m.Vars); err != nil {
				t.Fatal(err)
			}
			var aliases []string
			for _, s := range m.Steps {
				aliases = append(aliases, s.Alias)
			}
			got := fmt.Sprintf("%s\n--- vars\n%s--- steps\n%s\n", m.Query, vars.String(), strings.Join(aliases, ", "))
			testutil.Golden(t, "build/"+tt.name, []byte(got))
			if !strings.HasPrefix(m.Query, "mutation "+effects.OperationName+"(") {
				t.Errorf("query does not start with the named operation: %q", m.Query)
			}
		})
	}
}

// TestBuildDeclaresWhatItUses checks every declared variable is used and
// every used one declared: GitHub rejects a document otherwise.
func TestBuildDeclaresWhatItUses(t *testing.T) {
	for _, ops := range [][]workflow.Op{
		{status, comment}, {status}, {comment}, {toBoard}, {toBoard, comment}, {unarchive, status},
		{closing}, {reopen}, {addLabels}, {rmLabels}, {assign}, {reopen, status, addLabels, rmLabels, assign, comment},
		{unassign}, {unassign, assign, comment},
		{comment, closing}, {sprint}, {unsprint}, {setFields}, {status, sprint, setFields, comment}, {blockedBy}, {status, blockedBy, comment}, {blockedBy, blocked2},
		{milestone}, {status, setFields, milestone, addLabels, comment},
	} {
		m, err := effects.Build(ops, ids)
		if err != nil {
			t.Fatal(err)
		}
		head, body, _ := strings.Cut(m.Query, "{")
		for name := range m.Vars {
			if !strings.Contains(head, "$"+name+":") || !strings.Contains(body, "$"+name+"}") && !strings.Contains(body, "$"+name+",") {
				t.Errorf("%v: $%s declared %v, used %v", ops, name, strings.Contains(head, "$"+name+":"), strings.Contains(body, "$"+name))
			}
		}
		if n := strings.Count(head, "$"); n != len(m.Vars) {
			t.Errorf("%v: %d declarations, %d vars", ops, n, len(m.Vars))
		}
	}
}

func TestBuildCommentText(t *testing.T) {
	m, err := effects.Build([]workflow.Op{comment}, ids)
	if err != nil {
		t.Fatal(err)
	}
	if m.Vars["body"] != comment.Text() || !strings.HasSuffix(comment.Text(), comment.Marker) {
		t.Errorf("body = %q, want the comment text ending with its marker", m.Vars["body"])
	}
}

func TestBuildErrors(t *testing.T) {
	noItem := ids
	noItem.Item = ""
	noOption := ids
	noOption.StatusOption = ""
	noIteration := ids
	noIteration.Iteration = ""
	noBlocker := ids
	noBlocker.Blockers = nil
	noMilestone := ids
	noMilestone.Milestone = ""
	for name, tt := range map[string]struct {
		ops []workflow.Op
		ids effects.IDs
	}{
		"empty":        {nil, ids},
		"status_item":  {[]workflow.Op{status}, noItem},
		"status_board": {[]workflow.Op{toBoard, status}, noItem},
		"no_option":    {[]workflow.Op{status}, noOption},
		"twice":        {[]workflow.Op{comment, comment}, ids},
		"unarchive":    {[]workflow.Op{unarchive}, noItem},
		"label_id":     {[]workflow.Op{workflow.AddLabels{Names: []string{"nope"}}}, ids},
		"remove_id":    {[]workflow.Op{workflow.RemoveLabels{Names: []string{"nope"}}}, ids},
		"user_id":      {[]workflow.Op{workflow.Assign{Login: "nobody"}}, ids},
		"unassign_id":  {[]workflow.Op{workflow.Unassign{Logins: []string{"octocat", "nobody"}}}, ids},
		"no_labels":    {[]workflow.Op{workflow.AddLabels{}}, ids},
		"close_twice":  {[]workflow.Op{closing, closing}, ids},
		"sprint_item":  {[]workflow.Op{sprint}, noItem},
		"fields_item":  {[]workflow.Op{setFields}, noItem},
		"no_iteration": {[]workflow.Op{sprint}, noIteration},
		"no_field":     {[]workflow.Op{workflow.SetFields{Changes: []workflow.FieldChange{{Field: "Size", To: "M"}}}}, ids},
		"no_changes":   {[]workflow.Op{workflow.SetFields{}}, ids},
		"fields_twice": {[]workflow.Op{setFields, setFields}, ids},
		"blocker_id":   {[]workflow.Op{blockedBy}, noBlocker},
		"milestone_id": {[]workflow.Op{milestone}, noMilestone},
		"milestone_2x": {[]workflow.Op{milestone, milestone}, ids},
	} {
		if _, err := effects.Build(tt.ops, tt.ids); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestBatches(t *testing.T) {
	tests := []struct {
		ops  []workflow.Op
		want [][]workflow.Op
	}{
		{nil, nil},
		{[]workflow.Op{status, comment}, [][]workflow.Op{{status, comment}}},
		{[]workflow.Op{toBoard, status, comment}, [][]workflow.Op{{toBoard}, {status, comment}}},
		{[]workflow.Op{toBoard, status}, [][]workflow.Op{{toBoard}, {status}}},
		{[]workflow.Op{toBoard, comment}, [][]workflow.Op{{toBoard, comment}}},
		{[]workflow.Op{toBoard}, [][]workflow.Op{{toBoard}}},
		{[]workflow.Op{unarchive, status, comment}, [][]workflow.Op{{unarchive, status, comment}}},
		{[]workflow.Op{toBoard, reopen, status, addLabels, comment, closing},
			[][]workflow.Op{{toBoard}, {reopen, status, addLabels, comment, closing}}},
		{[]workflow.Op{toBoard, addLabels, assign, comment, closing},
			[][]workflow.Op{{toBoard, addLabels, assign, comment, closing}}},
		{[]workflow.Op{toBoard, sprint, comment}, [][]workflow.Op{{toBoard}, {sprint, comment}}},
		{[]workflow.Op{toBoard, setFields}, [][]workflow.Op{{toBoard}, {setFields}}},
	}
	for _, tt := range tests {
		if got := effects.Batches(tt.ops); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Batches(%v) = %v, want %v", tt.ops, got, tt.want)
		}
	}
	ops := []workflow.Op{toBoard, status, comment}
	effects.Batches(ops)
	if !slices.Equal(ops, []workflow.Op{toBoard, status, comment}) {
		t.Errorf("Batches changed its input: %v", ops)
	}
}

func TestOutcome(t *testing.T) {
	m, err := effects.Build([]workflow.Op{status, comment}, ids)
	if err != nil {
		t.Fatal(err)
	}
	data := func(s string) map[string]json.RawMessage {
		var d map[string]json.RawMessage
		if err := json.Unmarshal([]byte(s), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	tests := []struct {
		data         map[string]json.RawMessage
		done, missed []workflow.Op
	}{
		{data(`{"status": {"projectV2Item": {"id": "PVTI_1"}}, "comment": {"commentEdge": {"node": {"id": "IC_1"}}}}`),
			[]workflow.Op{status, comment}, nil},
		{data(`{"status": null, "comment": {"commentEdge": {"node": {"id": "IC_1"}}}}`),
			[]workflow.Op{comment}, []workflow.Op{status}},
		{data(`{"status": {"projectV2Item": {"id": "PVTI_1"}}}`), []workflow.Op{status}, []workflow.Op{comment}},
		{nil, nil, []workflow.Op{status, comment}},
	}
	for i, tt := range tests {
		done, missed := m.Outcome(tt.data)
		if !reflect.DeepEqual(done, tt.done) || !reflect.DeepEqual(missed, tt.missed) {
			t.Errorf("%d: done %v, missed %v; want %v, %v", i, done, missed, tt.done, tt.missed)
		}
	}
}

// TestOutcomeBlockedBy: each blocked-by link is its own op, so one GitHub
// refused is missed alone.
func TestOutcomeBlockedBy(t *testing.T) {
	m, err := effects.Build([]workflow.Op{blockedBy, blocked2}, ids)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"blockedBy0": {"issue": {"id": "I_1"}}, "blockedBy1": null}`), &d); err != nil {
		t.Fatal(err)
	}
	done, missed := m.Outcome(d)
	if !reflect.DeepEqual(done, []workflow.Op{blockedBy}) || !reflect.DeepEqual(missed, []workflow.Op{blocked2}) {
		t.Errorf("done %v, missed %v", done, missed)
	}
}

// TestOutcomeFields: a SetFields set in part is split into the fields set
// (done) and the rest (missed), so a partial report names each.
func TestOutcomeFields(t *testing.T) {
	m, err := effects.Build([]workflow.Op{status, setFields, comment}, ids)
	if err != nil {
		t.Fatal(err)
	}
	ok := `{"projectV2Item": {"id": "PVTI_1"}}`
	estimate := workflow.SetFields{Changes: setFields.Changes[:1]}
	priority := workflow.SetFields{Changes: setFields.Changes[1:]}
	for _, tt := range []struct {
		data         string
		done, missed []workflow.Op
	}{
		{`{"status": ` + ok + `, "field0": ` + ok + `, "field1": ` + ok + `, "comment": {}}`,
			[]workflow.Op{status, setFields, comment}, nil},
		{`{"status": ` + ok + `, "field0": ` + ok + `, "field1": null, "comment": {}}`,
			[]workflow.Op{status, estimate, comment}, []workflow.Op{priority}},
		{`{"status": ` + ok + `, "field0": null, "field1": ` + ok + `}`,
			[]workflow.Op{status, priority}, []workflow.Op{estimate, comment}},
		{`{"status": ` + ok + `, "field0": null, "field1": null}`,
			[]workflow.Op{status}, []workflow.Op{setFields, comment}},
	} {
		var d map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tt.data), &d); err != nil {
			t.Fatal(err)
		}
		done, missed := m.Outcome(d)
		if !reflect.DeepEqual(done, tt.done) || !reflect.DeepEqual(missed, tt.missed) {
			t.Errorf("%s: done %v, missed %v; want %v, %v", tt.data, done, missed, tt.done, tt.missed)
		}
	}
	if got := effects.Missed(sprint) + "; " + effects.Missed(unsprint) + "; " + effects.Missed(setFields) + "; " +
		effects.Missed(estimate); got !=
		"sprint not set; sprint not cleared; Estimate and Priority not set; Estimate not set" {
		t.Errorf("Missed: %s", got)
	}
}

func TestItemID(t *testing.T) {
	m, err := effects.Build([]workflow.Op{toBoard}, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ data, want string }{
		{`{"board": {"item": {"id": "PVTI_new"}}}`, "PVTI_new"},
		{`{"board": null}`, "old"},
		{`{"board": {"item": null}}`, "old"},
		{`{}`, "old"},
	} {
		var d map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tt.data), &d); err != nil {
			t.Fatal(err)
		}
		if got := m.ItemID(d, "old"); got != tt.want {
			t.Errorf("ItemID(%s) = %q, want %q", tt.data, got, tt.want)
		}
	}
}

func TestMissed(t *testing.T) {
	var got []string
	for _, op := range []workflow.Op{toBoard, unarchive, reopen, status, milestone, addLabels, rmLabels, assign, blockedBy, comment, closing} {
		got = append(got, effects.Missed(op))
	}
	want := []string{"not added to the board", "not unarchived", "not reopened", "status not changed", "milestone not set",
		"labels not added", "labels not removed", "not assigned", "not blocked by #12", "comment not posted", "not closed"}
	if !slices.Equal(got, want) {
		t.Errorf("Missed = %q, want %q", got, want)
	}
}
