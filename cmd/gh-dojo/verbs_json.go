package main

import (
	"github.com/Trum-ok/gh-dojo/internal/batch"
	"github.com/Trum-ok/gh-dojo/internal/render"
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
	DefaultRepo *string         `json:"default_repo"`
	DryRun      bool            `json:"dry_run"`
	Issues      []verbIssueJSON `json:"issues"` // in the order of the arguments
	Summary     verbSummaryJSON `json:"summary"`
}

type verbSummaryJSON struct {
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

type verbIssueJSON struct {
	Number  int     `json:"number"`
	Repo    string  `json:"repo"`  // owner/name of the issue, which may differ from the default one
	Title   *string `json:"title"` // null when the issue was not read
	Outcome string  `json:"outcome"`
	// Planned are the ops to apply, Applied the ones GitHub confirmed (none
	// in a dry run), InPlace the effects found done already.
	Planned  []opJSON `json:"planned"`
	Applied  []opJSON `json:"applied"`
	InPlace  []opJSON `json:"in_place"`
	NeedsYes bool     `json:"needs_yes"` // confirm = "user": runs only with --yes
	Warnings []string `json:"warnings"`  // also printed on stderr
	// Error is why the issue failed, or what stopped a partial write.
	Error *errorJSON `json:"error"`
	// Card is the issue as show --json has it, as read before the verb's
	// changes; null without --show or when the issue was not read.
	Card *cardJSON `json:"card"`
}

// opJSON is one effect of a plan. Op names its kind; the other fields are
// those of the kind (see MarshalJSON), each always there.
type opJSON struct {
	// Op is add_to_board, unarchive, reopen, set_status, set_iteration,
	// set_fields, set_milestone, add_labels, remove_labels, assign,
	// unassign, add_blocked_by, add_comment or close.
	Op string
	// From and To are the status (set_status), iteration title
	// (set_iteration) or milestone title (set_milestone) before and after;
	// "" (null) for none.
	From, To  string
	StartDate string            // set_iteration: the start of To; "" (null) for none
	Fields    []fieldChangeJSON // set_fields
	Labels    []string          // add_labels, remove_labels
	Login     string            // assign
	Logins    []string          // unassign; empty in place for unassign = "all" with nobody to remove
	Kept      bool              // unassign, in place: the verb assigns the user it names, so they stay
	Issue     string            // add_blocked_by: "owner/name#12", in full
	Body      string            // add_comment, without the hidden marker; "" (null) when in place
	Reason    string            // close: completed, not_planned, duplicate
	Of        string            // close: the original of a duplicate, "owner/name#17" in full; "" (null) otherwise
}

// MarshalJSON writes op and the fields of its kind, in this order.
func (o opJSON) MarshalJSON() ([]byte, error) {
	type kv struct {
		k string
		v any
	}
	fields := []kv{{"op", o.Op}}
	switch o.Op {
	case "set_status":
		fields = append(fields, kv{"from", render.OrNull(o.From)}, kv{"to", render.OrNull(o.To)})
	case "set_iteration":
		fields = append(fields, kv{"from", render.OrNull(o.From)}, kv{"to", render.OrNull(o.To)}, kv{"start_date", render.OrNull(o.StartDate)})
	case "set_fields":
		fields = append(fields, kv{"fields", render.OrEmpty(o.Fields)})
	case "set_milestone":
		fields = append(fields, kv{"from", render.OrNull(o.From)}, kv{"to", render.OrNull(o.To)})
	case "add_labels", "remove_labels":
		fields = append(fields, kv{"labels", render.OrEmpty(o.Labels)})
	case "assign":
		fields = append(fields, kv{"login", o.Login})
	case "unassign":
		fields = append(fields, kv{"logins", render.OrEmpty(o.Logins)}, kv{"kept", o.Kept})
	case "add_blocked_by":
		fields = append(fields, kv{"issue", o.Issue})
	case "add_comment":
		fields = append(fields, kv{"body", render.OrNull(o.Body)})
	case "close":
		fields = append(fields, kv{"reason", o.Reason}, kv{"of", render.OrNull(o.Of)})
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

type fieldChangeJSON struct {
	Field string  `json:"field"`
	From  *string `json:"from"` // null for none
	To    *string `json:"to"`   // null: cleared
}

// toOpJSON is op as JSON, its issue refs in full (def is the default
// repository, owner/name, that a "#12" of the plan is in). The status of
// an issue not on the board reads from null (no status).
func toOpJSON(op workflow.Op, def string) opJSON {
	switch op := op.(type) {
	case workflow.AddToBoard:
		return opJSON{Op: "add_to_board"}
	case workflow.Unarchive:
		return opJSON{Op: "unarchive"}
	case workflow.Reopen:
		return opJSON{Op: "reopen"}
	case workflow.SetStatus:
		return opJSON{Op: "set_status", From: op.From, To: op.To}
	case workflow.SetIteration:
		return opJSON{Op: "set_iteration", From: op.From, To: op.To, StartDate: op.Start}
	case workflow.SetFields:
		out := opJSON{Op: "set_fields", Fields: []fieldChangeJSON{}}
		for _, c := range op.Changes {
			out.Fields = append(out.Fields, fieldChangeJSON{Field: c.Field, From: render.OrNull(c.From), To: render.OrNull(c.To)})
		}
		return out
	case workflow.SetMilestone:
		return opJSON{Op: "set_milestone", From: op.From, To: op.To}
	case workflow.AddLabels:
		return opJSON{Op: "add_labels", Labels: op.Names}
	case workflow.RemoveLabels:
		return opJSON{Op: "remove_labels", Labels: op.Names}
	case workflow.Assign:
		return opJSON{Op: "assign", Login: op.Login}
	case workflow.Unassign:
		return opJSON{Op: "unassign", Logins: op.Logins, Kept: op.Kept}
	case workflow.AddBlockedBy:
		return opJSON{Op: "add_blocked_by", Issue: fullText(op.Ref, def)}
	case workflow.AddComment:
		return opJSON{Op: "add_comment", Body: op.Body}
	case workflow.Close:
		return opJSON{Op: "close", Reason: op.Reason, Of: fullText(op.Of, def)}
	}
	return opJSON{Op: "unknown"}
}

func toOpsJSON(ops []workflow.Op, def string) []opJSON {
	out := []opJSON{}
	for _, op := range ops {
		out = append(out, toOpJSON(op, def))
	}
	return out
}

// inPlaceJSON is what p found in place, in the order of the plan's ops:
// the status when unchanged, its InPlace ops, and the comment (without
// its body) when already posted, before a close.
func inPlaceJSON(p *workflow.Plan, def string) []opJSON {
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
	return toOpsJSON(append(ops, closing...), def)
}

// verbJSON is the JSON of verb v run on items of repo ("" when not
// known); dry for a dry run.
func verbJSON(v, repo string, dry bool, items []*batch.Item) verbResult {
	r := verbResult{Verb: v, DefaultRepo: render.OrNull(repo), DryRun: dry, Issues: []verbIssueJSON{}, Summary: verbSummaryJSON{Total: len(items)}}
	for _, it := range items {
		is := toVerbIssueJSON(it, dry, repo)
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

// toVerbIssueJSON is the issue's part of a verb's JSON; def is the default
// repository.
func toVerbIssueJSON(it *batch.Item, dry bool, def string) verbIssueJSON {
	out := verbIssueJSON{
		Number: it.Ref.Number, Repo: it.Ref.Repo.String(), Outcome: outcomeFailed, Planned: []opJSON{}, Applied: []opJSON{}, InPlace: []opJSON{},
		Warnings: []string{}, Error: toErrorJSON(it.Err),
	}
	if it.Issue != nil {
		out.Title = render.OrNull(render.SingleLine(it.Issue.Title))
	}
	if it.Writes == nil {
		return out
	}
	first := it.Writes.First
	out.Title = render.OrNull(render.SingleLine(first.Issue.Title))
	out.Planned = toOpsJSON(first.Ops, def)
	out.NeedsYes = first.NeedsYes
	out.Warnings = render.OrEmpty(first.Warnings)
	if dry {
		out.InPlace = inPlaceJSON(first, def)
		switch {
		case it.Err != nil:
		case first.Empty():
			out.Outcome = outcomeUnchanged
		default:
			out.Outcome = outcomePlanned
		}
		return out
	}
	shown := it.Writes.Applied()
	out.Applied = toOpsJSON(it.Writes.Done, def)
	out.InPlace = inPlaceJSON(shown, def)
	switch {
	case it.Skipped:
		out.Outcome = outcomeNotAttempted
	case it.Err != nil && len(it.Writes.Done) > 0:
		out.Outcome = outcomePartial
	case it.Err != nil:
	case len(it.Writes.Done) > 0:
		out.Outcome = outcomeApplied
	default:
		out.Outcome = outcomeUnchanged
	}
	return out
}
