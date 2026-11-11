package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// A verb with batch = true runs on several issues: `gh dojo done 12 13 14`
// (spec F1, "Поведение нескольких номеров"). runBatch reads every card in
// one request per issue.MaxBatch issues, plans each issue on its own, and
// applies the plans one issue after another, each with its own mutation
// (two for an issue not on the board). An issue that fails is skipped; the
// others go on.
//
// Each issue gets its own mutation rather than one aliased mutation for
// the batch: a lost answer (timeout, 5xx) leaves one issue's outcome
// unknown instead of every issue's, a stale schema ID stops one issue,
// and an issue not on the board needs the item ID of its first mutation
// anyway. The cost is one mutation per issue, as if the verb ran on each.
//
// Output: the confirmation line of each issue done (applied, or already in
// place) on stdout, in the order of the arguments, then a summary. With
// every issue done it is "3 of 3 applied" on stdout and exit code 0.
// Otherwise it is an error naming each issue not done with its reason:
//
//	error: 3 of 4 applied; #17: done cannot move #17 from Backlog
//	this is a process rule: ask the user how to proceed
//
// and the exit code says what to do about the rest (see batchCode). A
// re-run of the same command re-reads every issue: those done are in
// place and cost no write, so it touches only what is left (principle 5).
//
// A stale schema ID is retried once per run, as for one issue: the schema
// is dropped and the issue that hit it is re-read with the issues not
// applied yet, in one request, then they are applied from fresh plans.
func runBatch(cmd *cobra.Command, cfg *config.Config, v config.Verb, ns []int, args workflow.Args, dryRun bool) error {
	if err := workflow.CheckArgs(v, ns[0], args); err != nil {
		return err
	}
	be, err := newBackend(cfg)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	b := &batchRun{
		be: be, engine: workflow.New(cfg), v: v, args: args,
		stdout: cmd.OutOrStdout(), stderr: cmd.ErrOrStderr(), warned: map[string]bool{},
	}
	b.engine.Getenv = getenv
	reads, err := be.ReadIssues(ctx, v, ns, args)
	if err != nil && b.staleCache(err) {
		reads, err = be.ReadIssues(ctx, v, ns, args)
	}
	if err != nil {
		return err
	}
	items := make([]*batchItem, len(reads))
	for i, r := range reads {
		items[i] = &batchItem{n: r.Number}
		b.plan(items[i], r)
	}
	b.replanStale(ctx, items)
	if dryRun {
		for _, it := range items {
			if it.err == nil {
				fmt.Fprintln(b.stdout, it.run.first.DryRun())
			}
		}
		return b.summary(items, true)
	}
	b.applyAll(ctx, items)
	return b.summary(items, false)
}

// batchRun is one run of a verb on several issues.
type batchRun struct {
	be             Backend
	engine         *workflow.Engine
	v              config.Verb
	args           workflow.Args
	stdout, stderr io.Writer
	warned         map[string]bool // warnings printed, each once per run
	retried        bool            // the stale-ID retry is spent
}

// batchItem is one issue of a batch run.
type batchItem struct {
	n   int
	run *verbRun // nil until planned
	// err is why the issue was not applied, or not in full; why is its
	// reason in the summary, without ";" so the summary can list several.
	err error
	why string
}

// plan plans verb v on the issue read as r, printing its warnings.
func (b *batchRun) plan(it *batchItem, r IssueRead) {
	if r.Err != nil {
		it.fail(r.Err)
		return
	}
	p, err := b.engine.Plan(b.v, r.Issue, b.args)
	if err != nil {
		it.fail(err)
		return
	}
	for _, w := range p.Warnings {
		if !b.warned[w] {
			b.warned[w] = true
			render.Warn(b.stderr, "%s", w)
		}
	}
	it.run = &verbRun{first: p, stderr: b.stderr}
}

// staleCache reports whether err may come from a board schema read from
// the cache (board.IsStale, as readPlanFresh), and if so drops the cache
// for a retry with a fresh one.
func (b *batchRun) staleCache(err error) bool {
	c, ok := b.be.(schemaCacher)
	return ok && c.SchemaCached() && board.IsStale(err) && b.be.Reset() == nil
}

// replanStale reads and plans once more, with a fresh board schema, the
// issues whose read or plan failed on the cached one (say, no iteration
// for today, added since it was cached), as readPlanFresh does for one
// issue. Nothing was written yet, so a retry is safe; it costs the schema
// and one read per issue.MaxBatch issues.
func (b *batchRun) replanStale(ctx context.Context, items []*batchItem) {
	var stale []*batchItem
	var ns []int
	for _, it := range items {
		if it.err != nil && board.IsStale(it.err) {
			stale = append(stale, it)
			ns = append(ns, it.n)
		}
	}
	if len(stale) == 0 || !b.staleCache(stale[0].err) {
		return
	}
	reads, err := b.be.ReadIssues(ctx, b.v, ns, b.args)
	for i, it := range stale {
		r := IssueRead{Number: it.n, Err: err}
		if err == nil {
			r = reads[i]
		}
		it.err, it.why, it.run = nil, "", nil
		b.plan(it, r)
	}
}

// applyAll applies the plan of each issue in turn, printing the
// confirmation line of each one done.
func (b *batchRun) applyAll(ctx context.Context, items []*batchItem) {
	for i := 0; i < len(items); i++ {
		it := items[i]
		if it.err != nil {
			continue
		}
		p := it.run.first
		if it.run.fresh != nil {
			p = it.run.fresh
		} else if err := p.Unconfirmed(); err != nil {
			it.fail(err)
			continue
		}
		err := it.run.apply(ctx, b.be, p)
		switch {
		case err == nil:
			fmt.Fprintln(b.stdout, it.run.applied().Confirm())
			continue
		case !board.IsStale(err) || b.retried:
			it.failRun(p, err, err)
			continue
		}
		// A board ID from the schema cache went stale: drop the cache,
		// re-read this issue and the ones not applied yet, re-plan them,
		// and go on from this one, once per run.
		b.retried = true
		if rerr := b.be.Reset(); rerr != nil {
			it.failRun(p, err, err)
			continue
		}
		b.reread(ctx, items[i:], err)
		i-- // this issue again, with its fresh plan
	}
}

// reread re-reads the issues of items that have not failed, in one request
// per issue.MaxBatch, after items[0] hit the stale schema ID of staleErr.
// items[0] gets the fresh plan less what it applied already (as runVerb's
// retry); the others, never applied, start over from their fresh plans.
func (b *batchRun) reread(ctx context.Context, items []*batchItem, staleErr error) {
	stale := items[0]
	var todo []*batchItem
	var ns []int
	for _, it := range items {
		if it.err == nil {
			todo = append(todo, it)
			ns = append(ns, it.n)
		}
	}
	reads, err := b.be.ReadIssues(ctx, b.v, ns, b.args)
	for i, it := range todo {
		r := IssueRead{Number: it.n, Err: err}
		if err == nil {
			r = reads[i]
		}
		if it != stale {
			it.run = nil
			b.plan(it, r)
			continue
		}
		first := it.run.first
		if r.Err != nil {
			it.failRun(first, staleErr, r.Err)
			continue
		}
		fresh, perr := b.engine.Plan(b.v, r.Issue, b.args)
		if perr != nil {
			it.failRun(first, staleErr, perr)
			continue
		}
		// What the first attempt confirmed is not sent again, even if the
		// re-read does not show it yet.
		fresh.Ops = missing(fresh.Ops, it.run.done)
		it.run.fresh = fresh
	}
}

// fail records err for an issue nothing was applied to.
func (it *batchItem) fail(err error) {
	it.err = err
	it.why = reason(err)
}

// failRun records the failed write of plan p, with cause and err as in
// verbRun.partial: when something was applied, the reason says what was
// and what was not.
func (it *batchItem) failRun(p *workflow.Plan, cause, err error) {
	missed := missing(p.Ops, it.run.done)
	it.err = it.run.partial(missed, cause, err)
	it.why = reason(err)
	if len(it.run.done) > 0 {
		done := strings.TrimPrefix(it.run.applied().Confirm(), fmt.Sprintf("#%d ", it.n))
		it.why = fmt.Sprintf("%s, %s: %s", done, missedText(missed, cause), reason(err))
	}
}

// wrote reports whether this run applied anything to the issue.
func (it *batchItem) wrote() bool { return it.run != nil && len(it.run.done) > 0 }

// maxSummaryRunes is how long the summary's first line may grow before the
// issues left are only named: the error block cuts a line at 200 runes,
// "error: " included.
const maxSummaryRunes = 190

func runes(s string) int { return utf8.RuneCountInString(s) }

// generalReason words why, the reason issue n failed, without n, so the
// issues that failed alike share one entry: "issue #9 not found" reads
// "issue not found", "#9 is closed" reads "it is closed".
func generalReason(why string, n int) string {
	re := regexp.MustCompile(`(issue )?#` + strconv.Itoa(n) + `\b`)
	return re.ReplaceAllStringFunc(why, func(m string) string {
		if strings.HasPrefix(m, "issue ") {
			return "issue"
		}
		return "it"
	})
}

// summary ends the run: "N of N applied" on stdout when every issue is
// done, else the error that names each issue not done (see runBatch). A dry
// run applies nothing: it reports only the issues that cannot be planned.
func (b *batchRun) summary(items []*batchItem, dry bool) error {
	var failed []*batchItem
	for _, it := range items {
		if it.err != nil {
			failed = append(failed, it)
		}
	}
	if len(failed) == 0 {
		if !dry {
			fmt.Fprintf(b.stdout, "%d of %d applied\n", len(items), len(items))
		}
		return nil
	}
	head := fmt.Sprintf("%d of %d applied", len(items)-len(failed), len(items))
	if dry {
		head = fmt.Sprintf("dry run: %d of %d can be applied", len(items)-len(failed), len(items))
	}

	// One entry per reason, in the order of the arguments: "#17, #18: why",
	// the reasons of several issues worded without their own numbers.
	type group struct {
		key, why string
		ns       []int
	}
	var groups []*group
	for _, it := range failed {
		key := generalReason(it.why, it.n)
		i := slices.IndexFunc(groups, func(g *group) bool { return g.key == key })
		if i < 0 {
			groups = append(groups, &group{key: key, why: it.why})
			i = len(groups) - 1
		}
		groups[i].ns = append(groups[i].ns, it.n)
	}
	entries := make([]string, len(groups))
	for i, g := range groups {
		why := g.why
		if len(g.ns) > 1 {
			why = g.key
		}
		entries[i] = issueRefs(g.ns) + ": " + why
	}
	line := head
	for i, entry := range entries {
		// The issues of the groups after this one, named if they do not fit.
		var rest []int
		for _, g := range groups[i+1:] {
			rest = append(rest, g.ns...)
		}
		slices.Sort(rest)
		more := ""
		if len(rest) > 0 {
			more = "; also not applied: " + issueRefs(rest)
		}
		if i > 0 && runes(line+"; "+entry+more) > maxSummaryRunes {
			rest = nil
			for _, g := range groups[i:] {
				rest = append(rest, g.ns...)
			}
			slices.Sort(rest)
			line += "; also not applied: " + issueRefs(rest)
			break
		}
		line += "; " + entry
	}

	wrote := slices.ContainsFunc(items, (*batchItem).wrote)
	code := batchCode(failed, wrote)
	out := &render.Error{Code: code, What: line}
	for _, it := range failed {
		e := render.FromError(it.err)
		if e.Code != code && (code != render.ExitPartial || e.Code != render.ExitNetwork) {
			continue
		}
		// The next step of the failure that decides the code: its fix,
		// and the advice of its message ("ask the user how to proceed").
		out.Fix = e.Fix
		if _, advice, ok := strings.Cut(e.What, "\n"); ok && code != render.ExitPartial {
			out.What += "\n" + advice
		}
		break
	}
	if code == render.ExitPartial {
		out.What += "\nre-run the same command: it skips the issues done and finishes the rest"
	}
	return out
}

// batchCode is the exit code of a batch run with failed issues, wrote
// when it applied anything:
//
//   - 5 when it applied something and an issue failed in a way a re-run
//     may finish (network, an unconfirmed or partial write): re-run it;
//   - otherwise the code of the failures, the first of 2 (a process
//     refusal: ask the user), 1 (bad arguments), 3 (not found), 4
//     (network, nothing applied), 5.
//
// 0, every issue done, has no failed issue.
func batchCode(failed []*batchItem, wrote bool) render.Code {
	codes := map[render.Code]bool{}
	for _, it := range failed {
		codes[render.FromError(it.err).Code] = true
	}
	if wrote && (codes[render.ExitNetwork] || codes[render.ExitPartial]) {
		return render.ExitPartial
	}
	for _, c := range []render.Code{render.ExitDenied, render.ExitUsage, render.ExitNotFound, render.ExitNetwork} {
		if codes[c] {
			return c
		}
	}
	return render.ExitPartial
}

// batchHelp is the Batch section of the help of a verb with batch = true.
func batchHelp(v config.Verb, line func(label, format string, a ...any)) {
	if !v.Batch {
		return
	}
	line("Batch", "several numbers at once: gh dojo %s 12 13 14; each issue is applied", v.Name)
	line("", "on its own, a failed one is skipped, and a summary ends the run:")
	line("", "\"3 of 4 applied; #17: <reason>\"; a re-run skips what is done")
	line("", "exit code: 0 all done; 5 some applied and a re-run may finish the rest;")
	line("", "else the failures' code, 2 first, then 1, 3, 4")
}
