package main

import (
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// The JSON of a verb, on one issue or several (batch): per issue the plan,
// what was applied, what was in place already, and the outcome. A dry run
// gives the plan only.

// verbResult is a verb's JSON.
type verbResult struct {
	Verb    string      `json:"verb"`
	DryRun  bool        `json:"dry_run"`
	Issues  []verbIssue `json:"issues"` // in the order of the arguments
	Summary verbSummary `json:"summary"`
}

type verbSummary struct {
	Total int `json:"total"`
	// Done counts the issues applied or found in place; in a dry run, the
	// issues that can be applied. Written counts those of them that took
	// a write (outcome applied).
	Done    int `json:"done"`
	Written int `json:"written"`
	// Failed counts the issues not applied, or only in part; NotAttempted
	// those left alone after an error that stops every write (auth, rate
	// limit, an interrupt).
	Failed       int `json:"failed"`
	NotAttempted int `json:"not_attempted"`
}

// Outcomes of a verb on one issue.
const (
	outcomePlanned   = "planned"   // dry run: this would be applied
	outcomeApplied   = "applied"   // every planned op applied
	outcomeUnchanged = "unchanged" // nothing to apply: every effect in place
	outcomePartial   = "partial"   // some ops applied, the rest not: see error
	outcomeFailed    = "failed"    // nothing applied: see error
	// outcomeNotAttempted: an error that stops every write (auth, rate
	// limit, an interrupt) came first; a re-run tries it.
	outcomeNotAttempted = "not_attempted"
)

type verbIssue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"` // "" when the issue was not read
	Outcome string `json:"outcome"`
	// Planned are the ops to apply, Applied the ones GitHub confirmed (none
	// in a dry run), InPlace the effects found done already.
	Planned  []jsonOp `json:"planned"`
	Applied  []jsonOp `json:"applied"`
	InPlace  []jsonOp `json:"in_place"`
	NeedsYes bool     `json:"needs_yes"` // confirm = "user": runs only with --yes
	Warnings []string `json:"warnings"`  // also printed on stderr
	// Error is why the issue failed, or what stopped a partial write.
	Error *jsonError `json:"error"`
}

// jsonOp is one effect of a plan. Op names its kind; the other fields are
// those of the kind, left out otherwise.
type jsonOp struct {
	// Op is add_to_board, unarchive, reopen, set_status, set_iteration,
	// set_fields, add_labels, remove_labels, assign, add_blocked_by,
	// add_comment or close.
	Op string `json:"op"`
	// From and To are the status (set_status) or iteration title
	// (set_iteration) before and after; "" for none.
	From      *string           `json:"from,omitempty"`
	To        *string           `json:"to,omitempty"`
	StartDate string            `json:"start_date,omitempty"` // set_iteration: the start of To
	Fields    []jsonFieldChange `json:"fields,omitempty"`     // set_fields
	Labels    []string          `json:"labels,omitempty"`     // add_labels, remove_labels
	Login     string            `json:"login,omitempty"`      // assign
	Issue     string            `json:"issue,omitempty"`      // add_blocked_by: "#12" or "owner/name#12"
	Body      string            `json:"body,omitempty"`       // add_comment, without the hidden marker; "" when in place
	Reason    string            `json:"reason,omitempty"`     // close: completed, not_planned, duplicate
}

type jsonFieldChange struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// opJSON is op as JSON. The status of an issue not on the board reads
// from "" (no status).
func opJSON(op workflow.Op) jsonOp {
	str := func(s string) *string { return &s }
	switch op := op.(type) {
	case workflow.AddToBoard:
		return jsonOp{Op: "add_to_board"}
	case workflow.Unarchive:
		return jsonOp{Op: "unarchive"}
	case workflow.Reopen:
		return jsonOp{Op: "reopen"}
	case workflow.SetStatus:
		return jsonOp{Op: "set_status", From: str(op.From), To: str(op.To)}
	case workflow.SetIteration:
		return jsonOp{Op: "set_iteration", From: str(op.From), To: str(op.To), StartDate: op.Start}
	case workflow.SetFields:
		out := jsonOp{Op: "set_fields", Fields: []jsonFieldChange{}}
		for _, c := range op.Changes {
			out.Fields = append(out.Fields, jsonFieldChange{Field: c.Field, From: c.From, To: c.To})
		}
		return out
	case workflow.AddLabels:
		return jsonOp{Op: "add_labels", Labels: orEmpty(op.Names)}
	case workflow.RemoveLabels:
		return jsonOp{Op: "remove_labels", Labels: orEmpty(op.Names)}
	case workflow.Assign:
		return jsonOp{Op: "assign", Login: op.Login}
	case workflow.AddBlockedBy:
		return jsonOp{Op: "add_blocked_by", Issue: op.Ref}
	case workflow.AddComment:
		return jsonOp{Op: "add_comment", Body: op.Body}
	case workflow.Close:
		return jsonOp{Op: "close", Reason: op.Reason}
	}
	return jsonOp{Op: "unknown"}
}

func opsJSON(ops []workflow.Op) []jsonOp {
	out := []jsonOp{}
	for _, op := range ops {
		out = append(out, opJSON(op))
	}
	return out
}

// inPlaceJSON is what p found in place, in the order of the plan's ops:
// the status when unchanged, its InPlace ops, and the comment (without
// its body) when already posted, before a close.
func inPlaceJSON(p *workflow.Plan) []jsonOp {
	var ops, closing []workflow.Op
	if p.StatusUnchanged {
		ops = append(ops, workflow.SetStatus{From: p.Target, To: p.Target})
	}
	for _, op := range p.InPlace {
		if _, ok := op.(workflow.Close); ok {
			closing = append(closing, op)
		} else {
			ops = append(ops, op)
		}
	}
	if p.CommentPresent {
		ops = append(ops, workflow.AddComment{})
	}
	return opsJSON(append(ops, closing...))
}

// verbJSON is the JSON of verb v run on items; dry for a dry run.
func verbJSON(v string, dry bool, items []*batchItem) verbResult {
	r := verbResult{Verb: v, DryRun: dry, Issues: []verbIssue{}, Summary: verbSummary{Total: len(items)}}
	for _, it := range items {
		is := it.json(dry)
		switch is.Outcome {
		case outcomePartial, outcomeFailed:
			r.Summary.Failed++
		case outcomeNotAttempted:
			r.Summary.NotAttempted++
		case outcomeApplied:
			r.Summary.Written++
			r.Summary.Done++
		default:
			r.Summary.Done++
		}
		r.Issues = append(r.Issues, is)
	}
	return r
}

// json is the issue's part of a verb's JSON.
func (it *batchItem) json(dry bool) verbIssue {
	out := verbIssue{
		Number: it.n, Outcome: outcomeFailed, Planned: []jsonOp{}, Applied: []jsonOp{}, InPlace: []jsonOp{},
		Warnings: []string{}, Error: errorJSON(it.err),
	}
	if it.run == nil {
		return out
	}
	first := it.run.first
	out.Title = singleLine(first.Issue.Title)
	out.Planned = opsJSON(first.Ops)
	out.NeedsYes = first.NeedsYes
	out.Warnings = orEmpty(first.Warnings)
	if dry {
		out.InPlace = inPlaceJSON(first)
		if it.err == nil {
			out.Outcome = outcomePlanned
		}
		return out
	}
	shown := it.run.applied()
	out.Applied = opsJSON(it.run.done)
	out.InPlace = inPlaceJSON(shown)
	switch {
	case it.skipped:
		out.Outcome = outcomeNotAttempted
	case it.err != nil && len(it.run.done) > 0:
		out.Outcome = outcomePartial
	case it.err != nil:
	case len(it.run.done) > 0:
		out.Outcome = outcomeApplied
	default:
		out.Outcome = outcomeUnchanged
	}
	return out
}
