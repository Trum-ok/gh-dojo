package batch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/effects"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// Item is one issue of a verb's run: of a batch, or the one issue of a
// verb run on one.
type Item struct {
	Ref    issue.Ref       // in its repository
	Issue  *workflow.Issue // as read; nil until read
	Writes *Writes         // nil until planned
	// Err is why the issue was not applied, or not in full; Why is its
	// reason in the summary, without ";" so the summary can list several.
	Err error
	Why string
	// Skipped: not attempted, after an error stopped the writes (skip).
	Skipped bool
	// relies are the sub-issues of the run its plan counted as resolved
	// (subIssuesInRun): applied before it, which must not fail.
	relies []*Item
}

// Fail records err for an issue nothing was applied to; release refuses
// so a closed issue its verb does not start from.
func (it *Item) Fail(err error) {
	it.Err = err
	it.Why = issueReason(err)
}

// skip records an issue not attempted after cause stopped the writes of
// the run (graph.AffectsAll): the same code, fix and reason.
func (it *Item) skip(cause error) {
	e := render.FromError(cause)
	it.Why = "not attempted: " + render.Reason(cause)
	it.Err = &render.Error{Code: e.Code, What: it.Why, Fix: e.Fix, Cause: cause}
	it.Skipped = true
}

// failRun records the failed write of plan p, with cause and err as in
// Writes.Partial: when something was applied, the reason says what was
// and what was not.
func (it *Item) failRun(p *workflow.Plan, cause, err error) {
	missed := Missing(p.Ops, it.Writes.Done)
	it.Err = it.Writes.Partial(missed, cause, err)
	it.Why = render.Reason(err)
	if len(it.Writes.Done) > 0 {
		done := strings.TrimPrefix(it.Writes.Applied().Confirm(), p.Issue.Name()+" ")
		it.Why = fmt.Sprintf("%s, %s: %s", done, missedText(missed, cause), render.Reason(err))
	}
}

// Wrote reports whether this run applied anything to the issue.
func (it *Item) Wrote() bool { return it.Writes != nil && len(it.Writes.Done) > 0 }

// ReadPlan reads issue ref as a batch reads each issue and plans v on it,
// once more with a fresh board schema when the one used came from the
// cache and may explain the failure (board.IsStale): say, no iteration
// for today, added on the board since the schema was cached. Nothing was
// written yet, so a retry is safe.
//
// err fails the command, with nothing read (the network, a config error
// of v); the issue's own failure (not found, a refusal) is the item's
// Err, with Issue set once the issue was read.
func ReadPlan(ctx context.Context, be Backend, e *workflow.Engine, v config.Verb, ref issue.Ref, args workflow.Args) (*Item, error) {
	it, err := readPlanOnce(ctx, be, e, v, ref, args)
	failed := err
	if err == nil {
		failed = it.Err
	}
	if c, ok := be.(SchemaCacher); failed == nil || !ok || !c.SchemaCached() || !board.IsStale(failed) {
		return it, err
	}
	if be.Reset() != nil {
		return it, err
	}
	return readPlanOnce(ctx, be, e, v, ref, args)
}

// readPlanOnce is one try of ReadPlan.
func readPlanOnce(ctx context.Context, be Backend, e *workflow.Engine, v config.Verb, ref issue.Ref, args workflow.Args) (*Item, error) {
	reads, err := be.ReadIssues(ctx, v, []issue.Ref{ref}, args)
	if err != nil {
		return nil, err
	}
	if len(reads) != 1 {
		return nil, render.Internal("read %d issues for %s", len(reads), ref.String(issue.Repo{}))
	}
	it := &Item{Ref: reads[0].Ref}
	if r := reads[0]; r.Err != nil {
		it.Fail(r.Err)
	} else if p, err := e.Plan(v, r.Issue, args); err != nil {
		it.Issue = &r.Issue
		it.Fail(err)
	} else {
		it.Issue = &r.Issue
		it.Writes = &Writes{First: p}
	}
	return it, nil
}

// Writes is what a verb applied to one issue, across a stale-ID retry.
type Writes struct {
	First  *workflow.Plan // planned from the state the command found
	Fresh  *workflow.Plan // the re-plan of a retry; nil without one
	Done   []workflow.Op  // ops GitHub confirmed, from both attempts
	Stderr io.Writer      // for the warning of an error after every op applied
}

// ApplyFresh applies w.First. When GitHub rejects a board ID from the
// schema cache, part of the plan may have been applied: it drops the
// cache, re-plans with replan and applies what is left, once. A transport
// failure is never retried here: its outcome is unknown and the agent
// re-runs the command. The error is w.Partial's.
func (w *Writes) ApplyFresh(ctx context.Context, be Backend, replan func() (*workflow.Plan, error)) error {
	plan := w.First
	err := w.Apply(ctx, be, plan)
	switch {
	case err == nil:
		return nil
	case !board.IsStale(err):
		return w.Partial(Missing(plan.Ops, w.Done), err, err)
	}
	if rerr := be.Reset(); rerr != nil {
		return w.Partial(Missing(plan.Ops, w.Done), err, err)
	}
	fresh, perr := replan()
	if perr != nil {
		return w.Partial(Missing(plan.Ops, w.Done), err, perr)
	}
	w.replanned(fresh)
	if err := w.Apply(ctx, be, fresh); err != nil {
		return w.Partial(Missing(fresh.Ops, w.Done), err, err)
	}
	return nil
}

// replanned records fresh, the re-plan of a retry, without what the first
// attempt confirmed: that is not sent again, even if the re-read does not
// show it yet.
func (w *Writes) replanned(fresh *workflow.Plan) {
	fresh.Ops = Missing(fresh.Ops, w.Done)
	w.Fresh = fresh
}

// Apply applies p and records what GitHub confirmed. An error with every
// op of p confirmed is a warning.
func (w *Writes) Apply(ctx context.Context, be Backend, p *workflow.Plan) error {
	if p.Empty() {
		return nil
	}
	done, err := be.Apply(ctx, p)
	w.Done = append(w.Done, done...)
	if err != nil && len(Missing(p.Ops, done)) == 0 {
		render.Warn(w.Stderr, "GitHub reported an error after applying every change: %s", render.Reason(err))
		return nil
	}
	return err
}

// Applied is the plan of what this command did, for the confirmation
// line: the first plan's starting state with the confirmed ops. After a
// retry, an effect the re-read found in place counts as such, unless this
// command applied it.
func (w *Writes) Applied() *workflow.Plan {
	shown := *w.First
	shown.Ops = w.Done
	if w.Fresh != nil {
		shown.StatusUnchanged = w.First.StatusUnchanged ||
			w.Fresh.StatusUnchanged && !hasKind(w.Done, workflow.SetStatus{})
		shown.CommentPresent = w.First.CommentPresent ||
			w.Fresh.CommentPresent && !hasKind(w.Done, workflow.AddComment{})
		shown.InPlace = slices.Clone(w.First.InPlace)
		for _, op := range w.Fresh.InPlace {
			if !hasKind(w.Done, op) && !hasKind(shown.InPlace, op) {
				shown.InPlace = append(shown.InPlace, op)
			}
		}
	}
	return &shown
}

// Partial reports a failed write. With nothing confirmed, err itself is
// the answer. Otherwise it is exit code 5, naming what was done and what
// was not:
//
//	#42 comment left; status not changed: GitHub token is missing a required scope (project); gh auth refresh -s project, then re-run the same command
//
// The re-run re-reads the issue, so its plan leaves out what is done; but
// a blocked-by link GitHub refused as a dependency cycle is exit code 1,
// since a re-run would fail the same way. cause
// is the error of the write that left missed undone: per-field GraphQL
// errors or ops never sent (ErrNotSent) mean they did not happen, a lost
// answer (timeout, 5xx) that they are unconfirmed. err is what stopped the command, and gives the reason.
func (w *Writes) Partial(missed []workflow.Op, cause, err error) error {
	if len(w.Done) == 0 {
		return err
	}
	if graph.IsCycle(err) {
		// Re-running cannot help: the link makes a cycle through other issues.
		return render.Usage("%s; %s: %s", w.Applied().Confirm(), missedWords(missed), render.Reason(err)).
			WithFix("gh dojo show " + strings.TrimPrefix(w.First.Issue.Name(), "#")).WithCause(err)
	}
	e := render.FromError(err)
	next := "re-run the same command"
	switch fix := strings.TrimSpace(e.Fix); {
	case e.Wait != "": // a rate limit: its fix already says to wait, then re-run
		next = "wait " + e.Wait + ", then " + next
	case fix != "":
		next = fix + ", then " + next
	}
	return render.Partial("%s; %s: %s; %s", w.Applied().Confirm(), missedText(missed, cause), render.Reason(err), next).
		WithFix(e.Fix).WithCause(err)
}

// missedWords says what ops did not take effect: "status not changed, comment not posted".
func missedWords(missed []workflow.Op) string {
	words := make([]string, len(missed))
	for i, op := range missed {
		words[i] = effects.Missed(op)
	}
	return strings.Join(words, ", ")
}

// missedText says what of missed did not take effect, after a write that
// failed with cause (see Partial): the effects not done, or "the rest is
// unconfirmed".
func missedText(missed []workflow.Op, cause error) string {
	var gqlErr *api.GraphQLError
	if (errors.As(cause, &gqlErr) || errors.Is(cause, ErrNotSent)) && len(missed) > 0 {
		return missedWords(missed)
	}
	return "the rest is unconfirmed"
}

// Missing returns the ops of ops that are not among done, compared by
// content: a re-plan may hold an op of a kind already done with other
// values, such as a label to add that the first plan did not have, and
// that one is still to send. SetFields compares field by field, since
// GitHub may set some fields of one and not others (see effects.Outcome).
func Missing(ops, done []workflow.Op) []workflow.Op {
	var out []workflow.Op
	for _, op := range ops {
		if set, ok := op.(workflow.SetFields); ok {
			if rest := missingFields(set, done); len(rest.Changes) > 0 {
				out = append(out, rest)
			}
			continue
		}
		if !slices.ContainsFunc(done, func(d workflow.Op) bool { return reflect.DeepEqual(d, op) }) {
			out = append(out, op)
		}
	}
	return out
}

// missingFields returns the changes of set that no SetFields of done has.
func missingFields(set workflow.SetFields, done []workflow.Op) workflow.SetFields {
	var rest workflow.SetFields
	for _, c := range set.Changes {
		if !slices.ContainsFunc(done, func(d workflow.Op) bool {
			got, ok := d.(workflow.SetFields)
			return ok && slices.ContainsFunc(got.Changes, func(g workflow.FieldChange) bool { return reflect.DeepEqual(g, c) })
		}) {
			rest.Changes = append(rest.Changes, c)
		}
	}
	return rest
}

// hasKind reports whether ops has an op of the same kind as op.
func hasKind(ops []workflow.Op, op workflow.Op) bool {
	return slices.ContainsFunc(ops, func(o workflow.Op) bool { return reflect.TypeOf(o) == reflect.TypeOf(op) })
}
