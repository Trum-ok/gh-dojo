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
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// A verb with batch = true runs on several issues: `gh dojo done 12 13 14`
// (spec F1, "Поведение нескольких номеров"). runBatch reads every card in
// one request per issue.MaxBatch issues, plans each issue on its own, and
// applies the plans one issue after another, each with its own mutation
// (two for an issue not on the board). An issue that fails is skipped; the
// others go on, unless its error would fail every write alike (the token,
// a rate limit, the run interrupted: graph.AffectsAll): then the issues
// left to write are not attempted.
//
// Each issue gets its own mutation rather than one aliased mutation for
// the batch: a lost answer (timeout, 5xx) leaves one issue's outcome
// unknown instead of every issue's, a stale schema ID stops one issue,
// and an issue not on the board needs the item ID of its first mutation
// anyway. The cost is one mutation per issue, as if the verb ran on each.
//
// Output: the confirmation line of each issue done (applied, or already in
// place) on stdout, in the order of the arguments, then a summary. With
// every issue done it is "3 of 3 done" on stdout ("3 of 3 done (2
// written)" when one was in place) and exit code 0. Otherwise it is an
// error naming each issue not done with its reason, then the next steps:
//
//	error: 3 of 4 done; #17: done cannot move #17 from Backlog, it starts only from Ready or In progress
//	this is a process rule: ask the user how to proceed
//
// and the exit code says what to do about the rest (see batchCode). A
// re-run of the same command re-reads every issue: those done are in
// place and cost no write, so it touches only what is left (principle 5).
//
// A stale schema is retried once per run, as for one issue (see
// retryStale): the schema is dropped and the issues it failed are re-read
// in one request, then planned and applied afresh.
func runBatch(cmd *cobra.Command, cfg *config.Config, v config.Verb, refs []issue.Ref, args workflow.Args, dryRun bool) error {
	if err := workflow.CheckArgs(v, args, refArgs(refs, issue.Repo{})...); err != nil {
		return err
	}
	be, err := newBackend(cfg)
	if err != nil {
		return err
	}
	def, err := be.Repo()
	if err != nil {
		return err
	}
	if refs = inRepo(refs, def); len(refs) == 1 { // 12 and owner/repo#12 of the default repository
		return runVerb(cmd, cfg, v, refs[0], args, dryRun)
	}
	if !v.Batch { // issueArgs let one number in two forms through
		return notBatch(cmd, cfg, v, len(refs))
	}
	ctx := cmd.Context()
	b := &batchRun{
		be: be, engine: workflow.New(cfg), v: v, args: args, json: jsonMode(cmd), def: def,
		stdout: cmd.OutOrStdout(), stderr: cmd.ErrOrStderr(), warned: map[string]bool{},
	}
	b.engine.Getenv = getenv
	reads, err := be.ReadIssues(ctx, v, refs, args)
	if err != nil && b.retryStale(err, false) {
		reads, err = be.ReadIssues(ctx, v, refs, args)
	}
	if err != nil {
		return err
	}
	items := make([]*batchItem, len(reads))
	for i, r := range reads {
		items[i] = &batchItem{ref: r.Ref}
		b.plan(items[i], r)
	}
	b.replanStale(ctx, items)
	if dryRun {
		for _, it := range items {
			if it.err == nil && !b.json {
				fmt.Fprintln(b.stdout, it.run.first.DryRun())
			}
		}
	} else {
		b.applyAll(ctx, items)
	}
	// The text was printed issue by issue as it went; JSON comes at the
	// end, for every issue, also when the summary is an error.
	return emitWrite(cmd, verbJSON(v.Name, backendRepo(be), dryRun, items), func(io.Writer) {},
		b.summary(items, dryRun), slices.ContainsFunc(items, (*batchItem).wrote))
}

// batchRun is one run of a verb on several issues.
type batchRun struct {
	be             Backend
	engine         *workflow.Engine
	v              config.Verb
	args           workflow.Args
	stdout, stderr io.Writer
	json           bool            // --json: nothing on stdout until runBatch emits the result
	def            issue.Repo      // the default repository, which output names issues relative to
	warned         map[string]bool // warnings printed, each once per run
	retried        bool            // the stale-schema retry is spent (retryStale)
	stop           error           // the error that stopped the writes (graph.AffectsAll)
}

// batchItem is one issue of a batch run.
type batchItem struct {
	ref  issue.Ref       // in its repository
	read *workflow.Issue // nil until read
	run  *verbRun        // nil until planned
	// err is why the issue was not applied, or not in full; why is its
	// reason in the summary, without ";" so the summary can list several.
	err error
	why string
	// skipped: not attempted, after an error stopped the writes (skip).
	skipped bool
}

// plan plans verb v on the issue read as r, printing its warnings.
func (b *batchRun) plan(it *batchItem, r IssueRead) {
	if r.Err != nil {
		it.fail(r.Err)
		return
	}
	it.read = &r.Issue
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

// retryStale reports whether to retry after err with a fresh board
// schema, and if so drops the cached one. It says yes once per run,
// whichever step asks first: the read of the batch, the re-read of the
// issues whose plan failed on the cached schema, or a write GitHub refused
// for a stale ID (onWrite). err must be board.IsStale. For a read or a
// plan, the schema must also have come from the cache, as for
// readPlanFresh: a fresh one would fail the same way.
func (b *batchRun) retryStale(err error, onWrite bool) bool {
	if b.retried || !board.IsStale(err) {
		return false
	}
	if c, ok := b.be.(schemaCacher); !onWrite && (!ok || !c.SchemaCached()) {
		return false
	}
	b.retried = true
	return b.be.Reset() == nil
}

// replanStale reads and plans once more, with a fresh board schema, the
// issues whose read or plan failed on the cached one (say, no iteration
// for today, added since it was cached), as readPlanFresh does for one
// issue. Nothing was written yet, so a retry is safe; it costs the schema
// and one read per issue.MaxBatch issues.
func (b *batchRun) replanStale(ctx context.Context, items []*batchItem) {
	var stale []*batchItem
	var refs []issue.Ref
	for _, it := range items {
		if it.err != nil && board.IsStale(it.err) {
			stale = append(stale, it)
			refs = append(refs, it.ref)
		}
	}
	if len(stale) == 0 || !b.retryStale(stale[0].err, false) {
		return
	}
	reads, err := b.be.ReadIssues(ctx, b.v, refs, b.args)
	for i, it := range stale {
		r := IssueRead{Ref: it.ref, Err: err}
		if err == nil {
			r = reads[i]
		}
		it.err, it.why, it.read, it.run = nil, "", nil, nil
		b.plan(it, r)
	}
}

// applyAll applies the plan of each issue in turn, printing the
// confirmation line of each one done. After an error that would fail
// every write alike (graph.AffectsAll), the issues left with something to
// write are not attempted; those already in place are still done.
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
		if b.stop != nil && !p.Empty() {
			it.skip(b.stop)
			continue
		}
		err := it.run.apply(ctx, b.be, p)
		switch {
		case err == nil:
			if !b.json {
				fmt.Fprintln(b.stdout, it.run.applied().Confirm())
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

// reread re-reads the issues of items that have not failed, in one request
// per issue.MaxBatch, after items[0] hit the stale schema ID of staleErr.
// items[0] gets the fresh plan less what it applied already (as runVerb's
// retry); the others, never applied, start over from their fresh plans.
func (b *batchRun) reread(ctx context.Context, items []*batchItem, staleErr error) {
	stale := items[0]
	var todo []*batchItem
	var refs []issue.Ref
	for _, it := range items {
		if it.err == nil {
			todo = append(todo, it)
			refs = append(refs, it.ref)
		}
	}
	reads, err := b.be.ReadIssues(ctx, b.v, refs, b.args)
	for i, it := range todo {
		r := IssueRead{Ref: it.ref, Err: err}
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
	it.why = issueReason(err)
}

// skip records an issue not attempted after cause stopped the writes of
// the run (graph.AffectsAll): the same code, fix and reason.
func (it *batchItem) skip(cause error) {
	e := render.FromError(cause)
	it.why = "not attempted: " + reason(cause)
	it.err = &render.Error{Code: e.Code, What: it.why, Fix: e.Fix, Cause: cause}
	it.skipped = true
}

// failRun records the failed write of plan p, with cause and err as in
// verbRun.partial: when something was applied, the reason says what was
// and what was not.
func (it *batchItem) failRun(p *workflow.Plan, cause, err error) {
	missed := missing(p.Ops, it.run.done)
	it.err = it.run.partial(missed, cause, err)
	it.why = reason(err)
	if len(it.run.done) > 0 {
		done := strings.TrimPrefix(it.run.applied().Confirm(), p.Issue.Name()+" ")
		it.why = fmt.Sprintf("%s, %s: %s", done, missedText(missed, cause), reason(err))
	}
}

// wrote reports whether this run applied anything to the issue.
func (it *batchItem) wrote() bool { return it.run != nil && len(it.run.done) > 0 }

// issueReason is why err failed one issue, for the summary: the first
// clause of its message (reason), but the whole first line of a refusal,
// which says what the verb allows ("it starts only from Ready"), with its
// "; " read ", " since the summary separates issues with ";".
func issueReason(err error) string {
	e := render.FromError(err)
	if e.Code != render.ExitDenied {
		return reason(err)
	}
	what, _, _ := strings.Cut(e.What, "\n")
	return strings.ReplaceAll(strings.TrimSpace(what), "; ", ", ")
}

// maxSummaryRunes is how long a line of the summary error may grow: the
// error block cuts a line at 200 runes, "error: " included, and the
// summary never relies on that cut, which would hide issues silently.
const maxSummaryRunes = 190

func runes(s string) int { return utf8.RuneCountInString(s) }

// issueRefRE matches an issue reference in a reason: "#9", "issue #9",
// "acme/api#9".
var issueRefRE = regexp.MustCompile(`(issue )?([A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9._-]+)?#(\d+)\b`)

// generalReason words why, the reason issue ref failed, without naming
// it, so the issues that failed alike share one entry: "issue #9 not
// found" reads "issue not found", "#9 is closed" reads "it is closed". A
// bare #9 is the issue whatever its repository: "issue #9 not found in
// acme/api" names the repository apart.
func generalReason(why string, ref issue.Ref) string {
	num := strconv.Itoa(ref.Number)
	return issueRefRE.ReplaceAllStringFunc(why, func(m string) string {
		sub := issueRefRE.FindStringSubmatch(m)
		switch {
		case sub[3] != num:
			return m
		case sub[2] != "":
			if r, err := issue.ParseRepo(sub[2]); err != nil || !ref.Same(issue.Ref{Repo: r, Number: ref.Number}) {
				return m
			}
		}
		if sub[1] != "" {
			return "issue"
		}
		return "it"
	})
}

// failGroup is the issues that failed for one reason.
type failGroup struct {
	key, why string // the reason without naming the issue, and with it
	refs     []issue.Ref
	err      error // of the first issue
	code     render.Code
}

// reason is the group's reason in the summary.
func (g *failGroup) reason() string {
	if len(g.refs) > 1 {
		return g.key
	}
	return g.why
}

// next is the group's advice, the line after the first of its error
// ("ask the user how to proceed"), and its fix.
func (g *failGroup) next() (advice, fix string) {
	e := render.FromError(g.err)
	if _, a, ok := strings.Cut(e.What, "\n"); ok {
		advice = strings.TrimSpace(a)
		if len(g.refs) > 1 {
			advice = generalReason(advice, g.refs[0])
		}
	}
	return advice, strings.TrimSpace(e.Fix)
}

// retryable reports whether a re-run may finish the group's issues:
// network failures, partial or unconfirmed writes, issues not attempted.
func (g *failGroup) retryable() bool {
	return g.code == render.ExitNetwork || g.code == render.ExitPartial
}

// summary ends the run: "N of N done" on stdout when every issue is done,
// else the error that names each issue not done (see runBatch). A dry run
// applies nothing: it reports only the issues that cannot be planned.
//
// The error's first line holds one entry per reason ("#17, #18: why"),
// those of the code that decides first, then in the order of the
// arguments. Entries that do not fit are named after "also not done", as
// many as fit, the rest counted ("and 5 more"). Then the next steps, at
// most two lines: the advice of each kind of failure ("ask the user how
// to proceed"), and for the issues a re-run may finish, "re-run the same
// command to retry #21", whatever the code; the fix line is the first
// failure's that has one.
func (b *batchRun) summary(items []*batchItem, dry bool) error {
	var failed []*batchItem
	written := 0 // issues done with a write
	for _, it := range items {
		switch {
		case it.err != nil:
			failed = append(failed, it)
		case it.wrote():
			written++
		}
	}
	done := len(items) - len(failed)
	head := fmt.Sprintf("%d of %d done", done, len(items))
	if written < done {
		head += fmt.Sprintf(" (%d written)", written)
	}
	if dry {
		head = fmt.Sprintf("dry run: %d of %d can be applied", done, len(items))
	}
	if len(failed) == 0 {
		if !dry && !b.json {
			fmt.Fprintln(b.stdout, head)
		}
		return nil
	}
	wrote := slices.ContainsFunc(items, (*batchItem).wrote)
	groups := failGroups(failed, codeOrder(wrote))
	out := &render.Error{Code: batchCode(failed, wrote), What: summaryLine(head, groups, b.def)}

	// Next steps: the advice of each kind, the retry of the issues a re-run
	// may finish, and the first fix.
	var advice []string
	var retry []issue.Ref
	retryFix := ""
	for _, g := range groups {
		a, fix := g.next()
		if a != "" && !slices.Contains(advice, a) && runes(strings.Join(append(advice, a), "; ")) <= maxSummaryRunes {
			advice = append(advice, a)
		}
		if out.Fix == "" {
			out.Fix = fix
		}
		if g.retryable() {
			retry = append(retry, g.refs...)
			if retryFix == "" {
				retryFix = fix
			}
		}
	}
	var next []string
	if len(advice) > 0 {
		next = append(next, strings.Join(advice, "; "))
	}
	if len(retry) > 0 {
		slices.SortFunc(retry, compareRefs(b.def))
		lead, tail := "re-run the same command to retry ", ""
		if retryFix != "" && retryFix != out.Fix {
			lead = retryFix + ", then " + lead
		}
		if !dry {
			tail = ": it skips the issues done"
		}
		hint := lead + refsWithin(retry, maxSummaryRunes-runes(lead+tail), b.def) + tail
		if out.Code == render.ExitNetwork || out.Code == render.ExitPartial {
			next = append([]string{hint}, next...) // the step of the code first
		} else {
			next = append(next, hint)
		}
	}
	for _, l := range next {
		out.What += "\n" + l
	}
	return out
}

// failGroups sorts the failed issues into one group per reason, worded
// without their numbers (generalReason), those whose code comes first in
// order first, else in the order of the arguments.
func failGroups(failed []*batchItem, order []render.Code) []*failGroup {
	var groups []*failGroup
	for _, it := range failed {
		key := generalReason(it.why, it.ref)
		i := slices.IndexFunc(groups, func(g *failGroup) bool { return g.key == key })
		if i < 0 {
			groups = append(groups, &failGroup{key: key, why: it.why, err: it.err, code: render.FromError(it.err).Code})
			i = len(groups) - 1
		}
		groups[i].refs = append(groups[i].refs, it.ref)
	}
	rank := func(c render.Code) int {
		if i := slices.Index(order, c); i >= 0 {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(groups, func(a, b *failGroup) int { return rank(a.code) - rank(b.code) })
	return groups
}

// summaryLine is the first line of the summary error: head, then the
// entry of each group while it fits in maxSummaryRunes with room left to
// name the issues after it; their issues follow "also not done", as many
// as fit, the rest counted. The first entry is always given: when it is
// too long, its issues are named as few as needed (at least one, the rest
// counted), then its reason is cut, leaving room to name a few issues
// after it.
func summaryLine(head string, groups []*failGroup, def issue.Repo) string {
	line := head
	for i, g := range groups {
		rest := groupRefs(groups[i+1:], def)
		why := g.reason()
		if entry := "; " + refNames(g.refs, def) + ": " + why; runes(line+entry) <= maxSummaryRunes-tailRunes(rest, 1, def) {
			line += entry
			continue
		}
		if i > 0 {
			return line + alsoNotDone(groupRefs(groups[i:], def), maxSummaryRunes-runes(line), def)
		}
		room := maxSummaryRunes - runes(line) - tailRunes(rest, 3, def)
		refs := refsWithin(g.refs, room-runes("; : "+why), def)
		line += "; " + refs + ": " + clipRunes(why, max(room-runes("; "+refs+": "), 2))
		return line + alsoNotDone(rest, maxSummaryRunes-runes(line), def)
	}
	return line
}

const alsoNotDonePrefix = "; also not done: "

// alsoNotDone names refs after the entries of the summary line, within
// budget runes; "" for none.
func alsoNotDone(refs []issue.Ref, budget int, def issue.Repo) string {
	if len(refs) == 0 {
		return ""
	}
	return alsoNotDonePrefix + refsWithin(refs, budget-runes(alsoNotDonePrefix), def)
}

// tailRunes is the length of alsoNotDone of refs naming up to k of them.
func tailRunes(refs []issue.Ref, k int, def issue.Repo) int {
	if len(refs) == 0 {
		return 0
	}
	k = min(k, len(refs))
	return runes(alsoNotDonePrefix + refsText(refs[:k], len(refs)-k, def))
}

// groupRefs lists the issues of groups in order (see compareRefs).
func groupRefs(groups []*failGroup, def issue.Repo) []issue.Ref {
	var refs []issue.Ref
	for _, g := range groups {
		refs = append(refs, g.refs...)
	}
	slices.SortFunc(refs, compareRefs(def))
	return refs
}

// refsWithin names refs as refNames does, as many as fit in budget runes
// (at least one), the rest counted: "#4, #5 and 12 more".
func refsWithin(refs []issue.Ref, budget int, def issue.Repo) string {
	for k := min(len(refs), maxErrorRefs); k > 1; k-- {
		if s := refsText(refs[:k], len(refs)-k, def); runes(s) <= budget {
			return s
		}
	}
	return refsText(refs[:1], len(refs)-1, def)
}

// refsText is "#1, #2", then " and N more" for more issues not named.
func refsText(shown []issue.Ref, more int, def issue.Repo) string {
	s := refNames(shown, def)
	if more > 0 {
		s += fmt.Sprintf(" and %d more", more)
	}
	return s
}

// codeOrder is the order in which the codes of failed issues decide the
// code of a batch run (batchCode), wrote when it applied anything.
func codeOrder(wrote bool) []render.Code {
	if wrote {
		return []render.Code{render.ExitPartial, render.ExitNetwork, render.ExitDenied, render.ExitUsage, render.ExitNotFound}
	}
	return []render.Code{render.ExitDenied, render.ExitUsage, render.ExitNetwork, render.ExitNotFound, render.ExitPartial}
}

// batchCode is the exit code of a batch run with failed issues, wrote
// when it applied anything:
//
//   - 5 when it applied something and an issue failed in a way a re-run
//     may finish (network, an unconfirmed or partial write, not
//     attempted): re-run it;
//   - otherwise the code of the failures, the first of 2 (a process
//     refusal: ask the user), 1 (bad arguments), 4 (network, nothing
//     applied: a re-run may finish), 3 (not found).
//
// 0, every issue done, has no failed issue. Whatever the code, the summary
// names the issues a re-run may finish.
func batchCode(failed []*batchItem, wrote bool) render.Code {
	codes := map[render.Code]bool{}
	for _, it := range failed {
		codes[render.FromError(it.err).Code] = true
	}
	for _, c := range codeOrder(wrote) {
		if !codes[c] {
			continue
		}
		if c == render.ExitNetwork && wrote {
			return render.ExitPartial
		}
		return c
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
	line("", "\"3 of 4 done; #17: <reason>\"; a re-run skips what is done")
	line("", "exit code: 0 all done; 5 some written and a re-run may finish the rest;")
	line("", "else the failures' code, 2 first, then 1, 4, 3")
}
