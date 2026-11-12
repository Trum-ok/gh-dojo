package main

import (
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// The JSON of a verb, on one issue or several (batch): per issue the plan,
// what was applied, what was in place already, and the outcome. A dry run
// gives the plan only.

// verbResult is a verb's JSON.
type verbResult struct {
	Verb string `json:"verb"`
	// DefaultRepo is owner/name of the default repository, where a bare
	// number is; null when not known. Each issue has its own repo.
	DefaultRepo *string     `json:"default_repo"`
	DryRun      bool        `json:"dry_run"`
	Issues      []verbIssue `json:"issues"` // in the order of the arguments
	Summary     verbSummary `json:"summary"`
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
	outcomeUnchanged = "unchanged" // nothing to apply, also in a dry run: every effect in place
	outcomePartial   = "partial"   // some ops applied, the rest not: see error
	outcomeFailed    = "failed"    // nothing applied: see error
	// outcomeNotAttempted: an error that stops every write (auth, rate
	// limit, an interrupt) came first; a re-run tries it.
	outcomeNotAttempted = "not_attempted"
)

type verbIssue struct {
	Number  int     `json:"number"`
	Repo    string  `json:"repo"`  // owner/name of the issue, which may differ from the default one
	Title   *string `json:"title"` // null when the issue was not read
	Outcome string  `json:"outcome"`
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
// those of the kind (see MarshalJSON), each always there.
type jsonOp struct {
	// Op is add_to_board, unarchive, reopen, set_status, set_iteration,
	// set_fields, add_labels, remove_labels, assign, unassign,
	// add_blocked_by, add_comment or close.
	Op string
	// From and To are the status (set_status) or iteration title
	// (set_iteration) before and after; "" (null) for none.
	From, To  string
	StartDate string            // set_iteration: the start of To; "" (null) for none
	Fields    []jsonFieldChange // set_fields
	Labels    []string          // add_labels, remove_labels
	Login     string            // assign
	Logins    []string          // unassign; empty in place for unassign = "all" with nobody to remove
	Kept      bool              // unassign, in place: the verb assigns the user it names, so they stay
	Issue     string            // add_blocked_by: "owner/name#12", in full
	Body      string            // add_comment, without the hidden marker; "" (null) when in place
	Reason    string            // close: completed, not_planned, duplicate
}

// MarshalJSON writes op and the fields of its kind, in this order.
func (o jsonOp) MarshalJSON() ([]byte, error) {
	type kv struct {
		k string
		v any
	}
	fields := []kv{{"op", o.Op}}
	switch o.Op {
	case "set_status":
		fields = append(fields, kv{"from", orNull(o.From)}, kv{"to", orNull(o.To)})
	case "set_iteration":
		fields = append(fields, kv{"from", orNull(o.From)}, kv{"to", orNull(o.To)}, kv{"start_date", orNull(o.StartDate)})
	case "set_fields":
		fields = append(fields, kv{"fields", orEmpty(o.Fields)})
	case "add_labels", "remove_labels":
		fields = append(fields, kv{"labels", orEmpty(o.Labels)})
	case "assign":
		fields = append(fields, kv{"login", o.Login})
	case "unassign":
		fields = append(fields, kv{"logins", orEmpty(o.Logins)}, kv{"kept", o.Kept})
	case "add_blocked_by":
		fields = append(fields, kv{"issue", o.Issue})
	case "add_comment":
		fields = append(fields, kv{"body", orNull(o.Body)})
	case "close":
		fields = append(fields, kv{"reason", o.Reason})
	}
	b := []byte{'{'}
	for i, f := range fields {
		if i > 0 {
			b = append(b, ',')
		}
		k, err := marshal(f.k)
		if err != nil {
			return nil, err
		}
		v, err := marshal(f.v)
		if err != nil {
			return nil, err
		}
		b = append(append(append(b, k...), ':'), v...)
	}
	return append(b, '}'), nil
}

type jsonFieldChange struct {
	Field string  `json:"field"`
	From  *string `json:"from"` // null for none
	To    *string `json:"to"`   // null: cleared
}

// opJSON is op as JSON, its issue refs in full (def is the default
// repository, owner/name, that a "#12" of the plan is in). The status of
// an issue not on the board reads from null (no status).
func opJSON(op workflow.Op, def string) jsonOp {
	switch op := op.(type) {
	case workflow.AddToBoard:
		return jsonOp{Op: "add_to_board"}
	case workflow.Unarchive:
		return jsonOp{Op: "unarchive"}
	case workflow.Reopen:
		return jsonOp{Op: "reopen"}
	case workflow.SetStatus:
		return jsonOp{Op: "set_status", From: op.From, To: op.To}
	case workflow.SetIteration:
		return jsonOp{Op: "set_iteration", From: op.From, To: op.To, StartDate: op.Start}
	case workflow.SetFields:
		out := jsonOp{Op: "set_fields", Fields: []jsonFieldChange{}}
		for _, c := range op.Changes {
			out.Fields = append(out.Fields, jsonFieldChange{Field: c.Field, From: orNull(c.From), To: orNull(c.To)})
		}
		return out
	case workflow.AddLabels:
		return jsonOp{Op: "add_labels", Labels: op.Names}
	case workflow.RemoveLabels:
		return jsonOp{Op: "remove_labels", Labels: op.Names}
	case workflow.Assign:
		return jsonOp{Op: "assign", Login: op.Login}
	case workflow.Unassign:
		return jsonOp{Op: "unassign", Logins: op.Logins, Kept: op.Kept}
	case workflow.AddBlockedBy:
		return jsonOp{Op: "add_blocked_by", Issue: fullText(op.Ref, def)}
	case workflow.AddComment:
		return jsonOp{Op: "add_comment", Body: op.Body}
	case workflow.Close:
		return jsonOp{Op: "close", Reason: op.Reason}
	}
	return jsonOp{Op: "unknown"}
}

func opsJSON(ops []workflow.Op, def string) []jsonOp {
	out := []jsonOp{}
	for _, op := range ops {
		out = append(out, opJSON(op, def))
	}
	return out
}

// inPlaceJSON is what p found in place, in the order of the plan's ops:
// the status when unchanged, its InPlace ops, and the comment (without
// its body) when already posted, before a close.
func inPlaceJSON(p *workflow.Plan, def string) []jsonOp {
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
	return opsJSON(append(ops, closing...), def)
}

// verbJSON is the JSON of verb v run on items of repo ("" when not
// known); dry for a dry run.
func verbJSON(v, repo string, dry bool, items []*batchItem) verbResult {
	r := verbResult{Verb: v, DefaultRepo: orNull(repo), DryRun: dry, Issues: []verbIssue{}, Summary: verbSummary{Total: len(items)}}
	for _, it := range items {
		is := it.json(dry, repo)
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

// json is the issue's part of a verb's JSON; def is the default
// repository.
func (it *batchItem) json(dry bool, def string) verbIssue {
	out := verbIssue{
		Number: it.ref.Number, Repo: it.ref.Repo.String(), Outcome: outcomeFailed, Planned: []jsonOp{}, Applied: []jsonOp{}, InPlace: []jsonOp{},
		Warnings: []string{}, Error: errorJSON(it.err),
	}
	if it.read != nil {
		out.Title = orNull(singleLine(it.read.Title))
	}
	if it.run == nil {
		return out
	}
	first := it.run.first
	out.Title = orNull(singleLine(first.Issue.Title))
	out.Planned = opsJSON(first.Ops, def)
	out.NeedsYes = first.NeedsYes
	out.Warnings = orEmpty(first.Warnings)
	if dry {
		out.InPlace = inPlaceJSON(first, def)
		switch {
		case it.err != nil:
		case first.Empty():
			out.Outcome = outcomeUnchanged
		default:
			out.Outcome = outcomePlanned
		}
		return out
	}
	shown := it.run.applied()
	out.Applied = opsJSON(it.run.done, def)
	out.InPlace = inPlaceJSON(shown, def)
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
