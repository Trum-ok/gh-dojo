// Package batch runs a verb on its issues: one, or several (gh dojo done
// 12 13 14). It reads them through a Backend, plans each with the
// workflow engine, applies the plans one issue after another, skipping
// those that fail, and sums the run up.
// The command parses the flags, prints and emits the result.
//
// Run.Do reads every card in one request per issue.MaxBatch issues, plans
// each issue on its own, and applies the plans one issue after another,
// each with its own mutation (two for an issue not on the board). An issue
// that fails is skipped; the others go on, unless its error would fail
// every write alike (the token, a rate limit, the run interrupted:
// graph.AffectsAll): then the issues left to write are not attempted.
//
// Each issue gets its own mutation rather than one aliased mutation for
// the batch: a lost answer (timeout, 5xx) leaves one issue's outcome
// unknown instead of every issue's, a stale schema ID stops one issue,
// and an issue not on the board needs the item ID of its first mutation
// anyway. The cost is one mutation per issue, as if the verb ran on each.
//
// Output (see "Several issues at once" in docs/output.md): the confirmation
// line of each issue done (applied, or already in place) on Stdout, in
// the order of the arguments (but sub-issues before their parent, see
// Run.resolvesSubIssues), then a summary. With every issue done it is
// "3 of 3 done" on Stdout ("3 of 3 done (2 written)" when one was in
// place) and exit code 0. Otherwise it is an error naming each issue not
// done with its reason, then the next steps:
//
//	error: 3 of 4 done; #17: done cannot move #17 from Backlog, it starts only from Ready or In progress
//	this is a process rule: ask the user how to proceed
//
// and the exit code says what to do about the rest (see batchCode). A
// re-run of the same command re-reads every issue: those done are in
// place and cost no write, so it touches only what is left (principle 5).
//
// A stale schema is retried once per run, as for one issue (see
// ReadPlan): the schema is dropped and the issues it failed are re-read
// in one request, then planned and applied afresh.
package batch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// Backend is what a run needs to read issues and apply plans; the
// command's backend has it, and talks to GitHub.
type Backend interface {
	// ReadIssues reads issues refs for verb v: one request per
	// issue.MaxBatch issues, of any repositories. It returns one Read per
	// number, in order; an issue that fails alone has its own Err. err
	// fails them all: a config error of v or a request that read nothing.
	ReadIssues(ctx context.Context, v config.Verb, refs []issue.Ref, a workflow.Args) ([]Read, error)
	// Apply applies every op of a non-empty plan. It returns the ops
	// GitHub confirmed, also with an error: the rest did not take effect,
	// or their outcome is unknown. Errors keep their cause, so
	// board.IsStale tells a rejected schema ID.
	Apply(ctx context.Context, p *workflow.Plan) ([]workflow.Op, error)
	// Reset drops the cached board schema after GitHub rejected one of its
	// IDs, so the next read and Apply use a fresh one.
	Reset() error
}

// SchemaCacher is a Backend whose board schema may come from the cache.
type SchemaCacher interface {
	// SchemaCached reports whether the schema in use came from the cache.
	SchemaCached() bool
}

// Read is one issue of ReadIssues: its state, or why it cannot be
// planned.
type Read struct {
	Ref   issue.Ref
	Issue workflow.Issue
	Err   error
	// Cause says Err is because the issue is not there: no such issue,
	// a pull request, or its repository not found (issue.Cause); "" for
	// any other error.
	Cause issue.Cause
}

// ErrNotSent marks an Apply error after which the remaining ops were never
// sent, so they are known not to have happened.
var ErrNotSent = errors.New("not sent")

// Run is one run of a verb on several issues.
type Run struct {
	be      Backend
	Engine  *workflow.Engine
	v       config.Verb
	args    workflow.Args
	def     issue.Repo // the default repository, which output names issues relative to
	Stdout  io.Writer
	Stderr  io.Writer
	JSON    bool            // --json: nothing on Stdout; the command emits the result
	warned  map[string]bool // warnings printed, each once per run
	retried bool            // the stale-schema retry is spent (retryStale)
	stop    error           // the error that stopped the writes (graph.AffectsAll)
	// Keep, when set, says which issues read to plan: release leaves out
	// the numbers a commit message names that are no issue.
	Keep func(Read) bool
	// resolved is statuses.resolved; resolving the issues of the run, by
	// key, whose plan leaves them resolved as sub-issues (see
	// subIssuesInRun).
	resolved  []string
	resolving map[string]*Item
	// Before, when set, is called with the planned issues before the
	// first one is printed or written, and returns those to go on with:
	// release leaves out the issues not ready for the verb.
	Before func(items []*Item) []*Item
}

// New prepares a run of verb v of cfg on several issues through be; def
// is be's default repository. Output goes nowhere until Stdout and Stderr
// are set.
func New(be Backend, cfg *config.Config, v config.Verb, args workflow.Args, def issue.Repo) *Run {
	return &Run{
		be: be, Engine: workflow.New(cfg), v: v, args: args, def: def,
		Stdout: io.Discard, Stderr: io.Discard, warned: map[string]bool{},
		resolved: cfg.Statuses.Resolved, resolving: map[string]*Item{},
	}
}

// Do reads the issues refs in one request per issue.MaxBatch, drops those
// Keep refuses, plans the rest, and applies them one by one (printing the
// plans instead for a dry run). err fails the whole run, with nothing
// written: the issues could not be read.
func (b *Run) Do(ctx context.Context, refs []issue.Ref, dryRun bool) ([]*Item, error) {
	reads, err := b.be.ReadIssues(ctx, b.v, refs, b.args)
	if err != nil && b.retryStale(err, false) {
		reads, err = b.be.ReadIssues(ctx, b.v, refs, b.args)
	}
	if err != nil {
		return nil, err
	}
	if b.Keep != nil {
		reads = slices.DeleteFunc(reads, func(r Read) bool { return !b.Keep(r) })
	}
	items := make([]*Item, len(reads)) // in the order of the arguments
	for i, r := range reads {
		items[i] = &Item{Ref: r.Ref}
	}
	// Planned and applied sub-issues first (see subIssuesInRun).
	order := make([]*Item, 0, len(items))
	for _, i := range b.planOrder(reads) {
		b.plan(items[i], reads[i])
		order = append(order, items[i])
	}
	b.replanStale(ctx, order)
	if b.Before != nil {
		order = b.Before(order)
	}
	if dryRun {
		for _, it := range order {
			if it.Err == nil && !b.JSON {
				fmt.Fprintln(b.Stdout, it.Writes.First.DryRun())
			}
		}
	} else {
		b.applyAll(ctx, order)
	}
	// The result in the order of the arguments, without what Before left
	// out.
	return slices.DeleteFunc(items, func(it *Item) bool { return !slices.Contains(order, it) }), nil
}

// resolvesSubIssues reports whether the run's verb checks sub-issues and
// leaves the issues it runs on resolved.
//
// A verb with sub_issues = "warn" or "deny" that leaves an issue resolved
// (it closes it, or moves it to a status of statuses.resolved) counts the
// sub-issues the same run plans, and so resolves, as resolved: gh dojo
// done 40 41 42 on a parent and its sub-issues in In progress moves all
// three, as running it twice would. The run plans and applies each
// sub-issue before its parent (planOrder), and a parent whose sub-issue
// then fails to be written is refused (applyAll).
func (b *Run) resolvesSubIssues() bool {
	return b.v.ChecksSubIssues() && (b.v.Close != "" || b.v.To != "" && config.HasStatus(b.resolved, b.v.To))
}

// runKey is how the run matches an issue with a sub-issue a card names.
func (b *Run) runKey(ref string) string { return strings.ToLower(ref) }

// planOrder returns the indexes of reads in the order to plan and apply
// them: the order of the arguments, but each issue after its unresolved
// sub-issues of the run (when resolvesSubIssues).
func (b *Run) planOrder(reads []Read) []int {
	index := map[string]int{}
	for i, r := range reads {
		index[b.runKey(r.Ref.String(b.def))] = i
	}
	order := make([]int, 0, len(reads))
	seen := make([]bool, len(reads))
	var visit func(i int)
	visit = func(i int) {
		if seen[i] {
			return
		}
		seen[i] = true
		if r := reads[i]; r.Err == nil && b.resolvesSubIssues() {
			for _, s := range r.Issue.UnresolvedSubIssues {
				if j, ok := index[b.runKey(s.Ref)]; ok {
					visit(j)
				}
			}
		}
		order = append(order, i)
	}
	for i := range reads {
		visit(i)
	}
	return order
}

// subIssuesInRun drops from is the unresolved sub-issues planned in this
// run (resolving) and returns their items, which the issue then relies
// on being written.
func (b *Run) subIssuesInRun(is *workflow.Issue) []*Item {
	if !b.resolvesSubIssues() {
		return nil
	}
	var relies []*Item
	var left []workflow.Blocker
	for _, s := range is.UnresolvedSubIssues {
		if it := b.resolving[b.runKey(s.Ref)]; it != nil && it.Err == nil && it.Writes != nil {
			relies = append(relies, it)
			continue
		}
		left = append(left, s)
	}
	is.UnresolvedSubIssues = left
	return relies
}

// plan plans verb v on the issue read as r, printing its warnings.
func (b *Run) plan(it *Item, r Read) {
	if r.Err != nil {
		it.Fail(r.Err)
		return
	}
	it.Issue = &r.Issue
	it.relies = b.subIssuesInRun(it.Issue)
	p, err := b.Engine.Plan(b.v, *it.Issue, b.args)
	if err != nil {
		it.Fail(err)
		return
	}
	if b.resolvesSubIssues() {
		b.resolving[b.runKey(it.Ref.String(b.def))] = it
	}
	for _, w := range p.Warnings {
		if !b.warned[w] {
			b.warned[w] = true
			render.Warn(b.Stderr, "%s", w)
		}
	}
	it.Writes = &Writes{First: p, Stderr: b.Stderr}
}

// retryStale reports whether to retry after err with a fresh board
// schema, and if so drops the cached one. It says yes once per run,
// whichever step asks first: the read of the batch, the re-read of the
// issues whose plan failed on the cached schema, or a write GitHub refused
// for a stale ID (onWrite). err must be board.IsStale. For a read or a
// plan, the schema must also have come from the cache, as for ReadPlan:
// a fresh one would fail the same way.
func (b *Run) retryStale(err error, onWrite bool) bool {
	if b.retried || !board.IsStale(err) {
		return false
	}
	if c, ok := b.be.(SchemaCacher); !onWrite && (!ok || !c.SchemaCached()) {
		return false
	}
	b.retried = true
	return b.be.Reset() == nil
}

// replanStale reads and plans once more, with a fresh board schema, the
// issues whose read or plan failed on the cached one (say, no iteration
// for today, added since it was cached), as ReadPlan does for one issue.
// Nothing was written yet, so a retry is safe; it costs the schema and
// one read per issue.MaxBatch issues.
func (b *Run) replanStale(ctx context.Context, items []*Item) {
	var stale []*Item
	var refs []issue.Ref
	for _, it := range items {
		if it.Err != nil && board.IsStale(it.Err) {
			stale = append(stale, it)
			refs = append(refs, it.Ref)
		}
	}
	if len(stale) == 0 || !b.retryStale(stale[0].Err, false) {
		return
	}
	reads, err := b.be.ReadIssues(ctx, b.v, refs, b.args)
	for i, it := range stale {
		r := Read{Ref: it.Ref, Err: err}
		if err == nil {
			r = reads[i]
		}
		it.Err, it.Why, it.Issue, it.Writes = nil, "", nil, nil
		b.plan(it, r)
	}
}

// applyAll applies the plan of each issue in turn, printing the
// confirmation line of each one done. After an error that would fail
// every write alike (graph.AffectsAll), the issues left with something to
// write are not attempted; those already in place are still done.
func (b *Run) applyAll(ctx context.Context, items []*Item) {
	for i := 0; i < len(items); i++ {
		it := items[i]
		if it.Err != nil {
			continue
		}
		p := it.Writes.First
		if it.Writes.Fresh != nil {
			p = it.Writes.Fresh
		} else if err := p.Unconfirmed(); err != nil {
			it.Fail(err)
			continue
		}
		if b.stop != nil && !p.Empty() {
			it.skip(b.stop)
			continue
		}
		if s := retriedSubIssue(it); s != nil {
			// A sub-issue failed on the network or was not attempted: the
			// re-run that finishes it finishes the parent too.
			it.skip(s.Err)
			continue
		}
		if err := b.failedSubIssues(it, p); err != nil {
			it.Fail(err)
			continue
		}
		err := it.Writes.Apply(ctx, b.be, p)
		switch {
		case err == nil:
			if !b.JSON {
				fmt.Fprintln(b.Stdout, it.Writes.Applied().Confirm())
			}
		case b.retryStale(err, true):
			// A board ID from the schema cache went stale: re-read this
			// issue and the ones not applied yet, re-plan them, and go on
			// from this one.
			b.reread(ctx, items[i:], err)
			i-- // this issue again, with its fresh plan
		default:
			it.failRun(p, err, err)
			if graph.AffectsAll(err) || ctx.Err() != nil {
				b.stop = err
			}
		}
	}
}

// retriedSubIssue returns a sub-issue it relies on that a re-run of the
// same command may finish: not attempted, or failed on the network, with
// an unknown outcome or in part (exit codes 4 and 5); nil when none did.
func retriedSubIssue(it *Item) *Item {
	for _, s := range it.relies {
		if s.Err == nil {
			continue
		}
		code := render.FromError(s.Err).Code
		if s.Skipped || code == render.ExitNetwork || code == render.ExitPartial || errors.Is(s.Err, graph.ErrUnknownOutcome) {
			return s
		}
	}
	return nil
}

// failedSubIssues refuses the plan p of it, as the sub_issues check would,
// when a sub-issue of the run its plan counted as resolved failed: it
// stays unresolved. A warning is enough for sub_issues = "warn".
func (b *Run) failedSubIssues(it *Item, p *workflow.Plan) error {
	var refs []string
	for _, s := range it.relies {
		if s.Err != nil {
			refs = append(refs, s.Ref.String(b.def))
		}
	}
	if len(refs) == 0 {
		return nil
	}
	list := strings.Join(refs, ", ")
	if b.v.SubIssues == config.BlockersWarn {
		render.Warn(b.Stderr, "%s has unresolved sub-issues: %s (failed in this run)", p.Issue.Name(), list)
		return nil
	}
	action := "move"
	if b.v.To == "" {
		action = "run on"
	}
	return render.Denied("%s cannot %s %s while it has unresolved sub-issues: %s (failed in this run)\n"+
		"this is a process rule: ask the user how to proceed", b.v.Name, action, p.Issue.Name(), list)
}

// reread re-reads the issues of items that have not failed, in one request
// per issue.MaxBatch, after items[0] hit the stale schema ID of staleErr.
// items[0] gets the fresh plan less what it applied already (as
// Writes.ApplyFresh); the others, never applied, start over from their
// fresh plans.
func (b *Run) reread(ctx context.Context, items []*Item, staleErr error) {
	stale := items[0]
	var todo []*Item
	var refs []issue.Ref
	for _, it := range items {
		if it.Err == nil {
			todo = append(todo, it)
			refs = append(refs, it.Ref)
		}
	}
	reads, err := b.be.ReadIssues(ctx, b.v, refs, b.args)
	for i, it := range todo {
		r := Read{Ref: it.Ref, Err: err}
		if err == nil {
			r = reads[i]
		}
		if it != stale {
			it.Writes = nil
			b.plan(it, r)
			continue
		}
		first := it.Writes.First
		if r.Err != nil {
			it.failRun(first, staleErr, r.Err)
			continue
		}
		it.relies = b.subIssuesInRun(&r.Issue)
		fresh, perr := b.Engine.Plan(b.v, r.Issue, b.args)
		if perr != nil {
			it.failRun(first, staleErr, perr)
			continue
		}
		it.Writes.replanned(fresh)
	}
}
