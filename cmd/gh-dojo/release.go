package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Trum-ok/gh-dojo/internal/batch"
	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/release"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// gh dojo release (docs/commands.md#release): a verb applied to the issues a release
// includes, found through the API (internal/release) and applied as a
// batch, the path of gh dojo done 12 13 14: the same plans, summary and
// exit codes.
//
// Requests: one for the tags and the head (one per 100 tags), one per
// release.PageSize commits with their pull requests, then the batch's
// (one read per issue.MaxBatch issues, a mutation per issue to write),
// then for a verb that finishes issues one for the board's issues in
// Ready the release does not include (releaseRun.readPending).

func init() { register(newReleaseCmd) }

type releaseOpts struct {
	verb, since string
	exclude     []string // --exclude as given
	dryRun      bool
	args        workflow.Args
}

func newReleaseCmd() (*cobra.Command, error) {
	var o releaseOpts
	cmd := &cobra.Command{
		Use:   "release <ref> --verb <verb>",
		Short: "Apply a verb to the issues a release includes, found through pull requests and commits",
		Long: "Use it once a release is tagged: it applies a verb to the issues the release\n" +
			"includes, as gh dojo <verb> 12 13 14 would. Before the tag, gh dojo release\n" +
			"main --dry-run shows what the next one holds; --dry-run lists each issue with\n" +
			"how it was found and its plan.\n\n" +
			"The release is the commits of <ref> (a tag, branch or commit as GitHub names\n" +
			"it: main, not origin/main; up to 1000) since the previous version tag: the\n" +
			"highest below <ref> when <ref> is one, else the highest; prereleases\n" +
			"(v1.2.3-rc.1) only for a prerelease. --since names the start instead; the\n" +
			"first release needs it.\n\n" +
			"An issue is in it when a merged pull request of a commit closes it (squash,\n" +
			"merge and rebase alike), or a commit message names it after a [release]\n" +
			"keywords one, by default closes, fixes, resolves (owner/repo#N for another\n" +
			"repository). With [release] suffix = true, so does \"(#12)\" ending a message's\n" +
			"first line; else such an issue is left out with a warning. Code in a message\n" +
			"(`Refs #12`) does not count.\n\n" +
			"Listed and left out, not failed: an issue only reverted commits name, a number\n" +
			"that is no issue the token can see, an open issue the verb's from does not\n" +
			"allow (a later release naming it again picks it up), a closed one in a final\n" +
			"status, and --exclude. Any other closed issue is refused (exit code 2).\n" +
			"For a verb that finishes issues (done), a warning names the issues waiting\n" +
			"for a release (statuses.resolved, not final) it does not include, with the\n" +
			"command for those it includes after all.",
		Example: `  gh dojo release main --verb done --dry-run
  gh dojo release v0.2.0 --verb done --dry-run
  gh dojo release v0.2.0 --verb done
  gh dojo release v0.2.0 --verb done --exclude acme/api#3
  gh dojo release v0.1.0 --since 4c40894 --verb done --comment "Released in v0.1.0."`,
		Args: releaseArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.args.HasComment = cmd.Flags().Changed(config.ArgComment)
			o.args.HasBy = cmd.Flags().Changed(config.ArgBy)
			return runRelease(cmd, args[0], &o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.verb, "verb", "", "the verb to apply to the release's issues: any without a required --assignee, --milestone or --of")
	f.StringVar(&o.since, "since", "", "where the release starts, a tag, branch or commit; default: the previous version tag")
	f.StringArrayVar(&o.exclude, "exclude", nil,
		"an `issue` found to leave out, by a pull request or a message: 12, \"#12\", owner/repo#12 or its URL; repeatable, or comma-separated")
	f.BoolVar(&o.dryRun, dryRunFlag, false, "list the issues, how each was found and its plan, without changing anything")
	f.BoolVar(&o.args.Yes, "yes", false, "the user agreed: for a verb that is the user's decision")
	f.StringVar(&o.args.Comment, config.ArgComment, "", "text for {comment} in the verb's comment, for every issue")
	f.StringVar(&o.args.By, config.ArgBy, "", "value for {by} in the verb's comment, for every issue")
	return cmd, nil
}

// releaseArgs takes one ref: a tag, branch or commit, as one word.
func releaseArgs(cmd *cobra.Command, args []string) error {
	help := cmd.CommandPath() + " --help"
	switch {
	case len(args) == 0:
		return render.Usage("release needs the release: a tag, branch or commit, like gh dojo release v0.2.0 --verb done").WithFix(help)
	case len(args) > 1:
		return render.Usage("release takes one tag, branch or commit, got %d", len(args)).WithFix(help)
	}
	return checkRefArg(cmd, "", args[0])
}

// checkRefArg refuses a ref that cannot name a tag, branch or commit.
func checkRefArg(cmd *cobra.Command, flag, ref string) error {
	if ref == "" || strings.ContainsAny(ref, " \t\r\n") || strings.HasPrefix(ref, "-") {
		return render.Usage("%s%q is not a tag, branch or commit", flag, ref).WithFix(cmd.CommandPath() + " --help")
	}
	return nil
}

func runRelease(cmd *cobra.Command, ref string, o *releaseOpts) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	v, err := releaseVerb(cmd, cfg, o.verb)
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("since") {
		if err := checkRefArg(cmd, "--since ", o.since); err != nil {
			return err
		}
	}
	if err := workflow.CheckArgs(v, o.args); err != nil {
		var e *render.Error
		if errors.As(err, &e) {
			e.Fix = cmd.CommandPath() + " --help"
		}
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
	excluded, err := parseBlockers(o.exclude, "exclude", def, issue.Ref{}, cmd.CommandPath()+" --help")
	if err != nil {
		return err
	}
	var q graph.Querier
	if gb, ok := be.(*githubBackend); ok {
		q = gb.app.Client
	} else {
		q = newApp().Client
	}
	ctx := cmd.Context()
	rng, err := release.Read(ctx, q, def, ref, o.since)
	if err != nil {
		return err
	}
	matcher := release.NewMatcher(cfg.Release.Keywords, cfg.Release.Suffix)
	found, reverted, warnings := release.Collect(rng, matcher, clientHost())
	warnings = append(slices.Clone(rng.Warnings), warnings...)
	for _, u := range release.FindUnsuffixed(rng, matcher, found, clientHost()) {
		warnings = append(warnings, unsuffixedWarning(u, v.Name, cfg.Release.Keywords, def))
	}
	for _, fs := range [][]release.Found{found, reverted} {
		for i := range fs {
			fs[i].Ref = fs[i].Ref.In(def) // the default repository as the config spells it
		}
		slices.SortStableFunc(fs, func(a, b release.Found) int { return issue.CompareRefs(def)(a.Ref, b.Ref) })
	}

	res := &releaseRun{rng: rng, def: def, found: found, keywords: cfg.Release.Keywords, suffix: cfg.Release.Suffix,
		repos: []issue.Repo{def}, final: cfg.Statuses.Final, waiting: releaseWaiting(cfg, v)}
	for _, f := range reverted {
		res.skipped = append(res.skipped, releaseSkipped{found: f, reason: skipReverted})
	}
	for _, s := range cfg.Board.Repos {
		if r, err := issue.ParseRepo(s); err == nil { // the config checked it
			res.repos = append(res.repos, r)
		}
	}
	refs, w := res.exclude(excluded)
	warnings = append(warnings, w...)
	for _, w := range warnings {
		render.Warn(cmd.ErrOrStderr(), "%s", w)
	}
	if len(refs) == 0 {
		if w := res.readPending(ctx, be, v, o.args); w != "" {
			render.Warn(cmd.ErrOrStderr(), "%s", w)
			warnings = append(warnings, w)
		}
		return emit(cmd, res.json(v.Name, backendRepo(be), o.dryRun, nil, warnings), func(w io.Writer) {
			fmt.Fprintln(w, res.head(0))
			for _, line := range []string{res.revertedLine(), res.excludedLine()} {
				if line != "" {
					fmt.Fprintln(w, line)
				}
			}
		})
	}
	b := newBatchRun(cmd, be, cfg, v, o.args, def)
	b.Keep = res.keep(cmd)
	out := cmd.OutOrStdout()
	// Once the issues are read and planned: the numbers a message names
	// that are no issue are left out (keep), and so are the issues closed
	// in a final status and those not ready.
	b.Before = func(items []*batch.Item) []*batch.Item {
		n := len(items)
		items = res.ready(v, res.settled(items))
		if !b.JSON {
			fmt.Fprintln(out, res.head(n))
			for _, line := range []string{res.revertedLine(), res.excludedLine(), res.closedLine(), res.notReadyLine(v)} {
				if line != "" {
					fmt.Fprintln(out, line)
				}
			}
			if o.dryRun {
				res.list(out, items)
			}
		}
		return items
	}
	items, err := b.Do(ctx, refs, o.dryRun)
	if err != nil {
		return err
	}
	if w := res.readPending(ctx, be, v, o.args); w != "" {
		render.Warn(cmd.ErrOrStderr(), "%s", w)
		res.skipWarnings = append(res.skipWarnings, w)
	}
	var summary error
	if len(items) > 0 { // none: the head line said why
		summary = b.Summary(items, o.dryRun)
	}
	return emitWrite(cmd, res.json(v.Name, backendRepo(be), o.dryRun, items, append(warnings, res.skipWarnings...)),
		func(io.Writer) {}, summary, slices.ContainsFunc(items, (*batch.Item).Wrote))
}

// releaseVerb is the verb --verb names: any verb but one that needs an
// argument release lacks (see releaseLacks).
func releaseVerb(cmd *cobra.Command, cfg *config.Config, name string) (config.Verb, error) {
	var fit []string
	example := "<verb>"
	for _, v := range cfg.Verbs {
		if releaseLacks(v) == "" {
			fit = append(fit, v.Name)
		}
		if example == "<verb>" && releaseFinishes(cfg, v) {
			example = v.Name
		}
	}
	if example == "<verb>" && len(fit) > 0 {
		example = fit[0]
	}
	if name == "" {
		return config.Verb{}, render.Usage("release needs --verb, the verb to apply to the release's issues").
			WithAllowed(fit...).WithFix(fmt.Sprintf("%s <ref> --verb %s", cmd.CommandPath(), example))
	}
	v, ok := cfg.Verb(name)
	if !ok {
		return config.Verb{}, render.Usage("release --verb %s: no such verb in %s", name, cfg.Display).
			WithAllowed(fit...).WithFix("gh dojo describe")
	}
	if arg := releaseLacks(v); arg != "" {
		return config.Verb{}, render.Usage("release --verb %s: %s needs --%s, which release does not take", name, name, arg).
			WithFix("run it on the issues themselves: " + workflow.Example(v, "<N>..."))
	}
	return v, nil
}

// releaseLacks is the argument verb v requires that release has no flag
// for, "" for none: the user an assign = "{assignee}" verb assigns, the
// milestone a milestone = "{milestone}" verb sets and the original a
// close = "duplicate" verb links differ from release to release, so
// release cannot run such a verb.
func releaseLacks(v config.Verb) string {
	for _, arg := range []string{config.ArgAssignee, config.ArgMilestone, config.ArgOf} {
		if slices.Contains(v.Require, arg) {
			return arg
		}
	}
	return ""
}

// releaseFinishes reports whether describe, the agent doc and release's
// own errors point to gh dojo release for verb v: release can run it, and
// it finishes an issue as a release does (closes it as completed or moves
// it to a final status). Release runs any verb it can; the others are
// rarely what a release means.
func releaseFinishes(cfg *config.Config, v config.Verb) bool {
	return releaseLacks(v) == "" && (v.Close == config.CloseCompleted || config.HasStatus(cfg.Statuses.Final, v.To))
}

// releaseWaiting are the statuses where finished work waits for a release
// that verb v finishes: statuses.resolved (the work is complete) but not
// final nor v's own to, among those v starts from; kanban's Ready for
// done. None for a verb that finishes no issue (releaseFinishes): its
// release has nothing to say about the board.
func releaseWaiting(cfg *config.Config, v config.Verb) []string {
	if !releaseFinishes(cfg, v) {
		return nil
	}
	var out []string
	for _, s := range cfg.Statuses.Resolved {
		if !config.SameStatus(s, v.To) && !config.HasStatus(cfg.Statuses.Final, s) && v.StartsFrom(s, cfg.Statuses.Initial) {
			out = append(out, s)
		}
	}
	return out
}

// statusReader is a Backend that reads the board's issues in some
// statuses, for the issues in Ready a release does not include
// (releaseRun.readPending): githubBackend, the tests' fake.
type statusReader interface {
	// ReadInStatus returns the board's issues in statuses, open and
	// closed, in board order: one request as a rule (board.ReadInStatus).
	ReadInStatus(ctx context.Context, statuses []string) ([]board.Item, error)
}

func (b *githubBackend) ReadInStatus(ctx context.Context, statuses []string) ([]board.Item, error) {
	cfg, err := b.app.Config()
	if err != nil {
		return nil, err
	}
	s, err := b.app.Schema(ctx)
	if err != nil {
		return nil, err
	}
	return board.ReadInStatus(ctx, b.app.Client, s, cfg.StatusField(), statuses)
}

// releasePending is an issue in a status where finished work waits for a
// release (releaseRun.waiting) that the release does not include.
type releasePending struct {
	ref    issue.Ref
	title  string
	closed bool
	status string
}

// readPending reads the board's issues in r.waiting that the release does
// not include, found or left out (an issue release found is named on its
// own lines), into r.pending, and returns the warning naming them, with
// the command that runs v on them, the release's --comment and --by
// given; "" for none. Such an issue is most likely work that went into
// the release with no link in its commits: release cannot know, so the
// user decides.
//
//	in Ready but not found in the release: #88, #89; for any it includes: gh dojo done 88 89 --comment "v0.7.0"
//
// It costs one request (board.ReadInStatus), GitHub filtering the board,
// since what the batch read is only the issues found. A release without
// commits, or whose verb finishes no issue, reads nothing. A read that
// fails is a warning too: the release itself is done.
func (r *releaseRun) readPending(ctx context.Context, be Backend, v config.Verb, a workflow.Args) string {
	sr, ok := be.(statusReader)
	if !ok || len(r.waiting) == 0 || len(r.rng.Commits) == 0 {
		return ""
	}
	in := render.JoinWords(r.waiting, "or")
	items, err := sr.ReadInStatus(ctx, r.waiting)
	if err != nil {
		what, _, _ := strings.Cut(render.FromError(err).What, "\n")
		return "not checked for issues in " + in + " the release does not include: " + what
	}
	for _, it := range items {
		repo, err := issue.ParseRepo(it.Repo)
		if err != nil {
			continue
		}
		if repo.Same(r.def) {
			repo = r.def // as the config spells it
		}
		ref := issue.Ref{Repo: repo, Number: it.Number}
		if r.includes(ref) {
			continue
		}
		r.pending = append(r.pending, releasePending{ref: ref, title: it.Title, closed: it.State == "CLOSED", status: it.Status})
	}
	if len(r.pending) == 0 {
		return ""
	}
	slices.SortStableFunc(r.pending, func(x, y releasePending) int { return issue.CompareRefs(r.def)(x.ref, y.ref) })
	refs := make([]issue.Ref, len(r.pending))
	args := []string{"gh dojo", v.Name}
	for i, p := range r.pending {
		refs[i] = p.ref
		if p.ref.Repo.Same(r.def) {
			args = append(args, strconv.Itoa(p.ref.Number))
		} else {
			args = append(args, p.ref.String(r.def))
		}
	}
	if a.HasComment {
		args = append(args, "--"+config.ArgComment, strconv.Quote(a.Comment))
	}
	if a.HasBy {
		args = append(args, "--"+config.ArgBy, strconv.Quote(a.By))
	}
	return "in " + in + " but not found in the release: " + issue.RefNames(refs, r.def) +
		"; for any it includes: " + strings.Join(args, " ")
}

// includes reports whether the release found ref, whether it runs the
// verb on it or left it out, or found it only through reverted commits.
func (r *releaseRun) includes(ref issue.Ref) bool {
	return slices.ContainsFunc(r.found, func(f release.Found) bool { return f.Ref.Same(ref) }) ||
		slices.ContainsFunc(r.skipped, func(s releaseSkipped) bool { return s.found.Ref.Same(ref) })
}

// releaseRun is what release found, for its output.
type releaseRun struct {
	rng      *release.Range
	def      issue.Repo
	found    []release.Found
	keywords []string
	suffix   bool // [release] suffix
	// repos are the project's own: the default repository and
	// board.repos.
	repos []issue.Repo
	final []string // statuses.final
	// skipped are the issues left out: those only reverted commits name
	// or close, by --exclude, then once read a number a commit message
	// names that is no issue, or an issue not ready for the verb or closed
	// in a final status.
	skipped      []releaseSkipped
	skipWarnings []string
	// waiting are the statuses where finished work waits for this
	// release (releaseWaiting), pending the issues in them it does not
	// include (readPending).
	waiting []string
	pending []releasePending
}

// Why release leaves out an issue it found.
const (
	skipReverted     = "reverted"       // only commits reverted later in the range name or close it
	skipExcluded     = "excluded"       // --exclude names it
	skipPullRequest  = "pull_request"   // a commit message names a pull request
	skipNotFound     = "not_found"      // only a commit message names it, and there is no such issue
	skipRepoNotFound = "repo_not_found" // only a commit message names it, in a repository outside the project's not found or hidden from the token
	skipNotReady     = "not_ready"      // the verb's from refuses the issue's status
	skipClosed       = "closed"         // closed in a final status: done with, the verb is not run
)

type releaseSkipped struct {
	found  release.Found
	reason string // one of the skip constants
	// onBoard and status are where an issue not ready or closed stands;
	// unset for the other reasons.
	onBoard bool
	status  string
}

// place is where s stands, as the text says it (see placeOf).
func (s releaseSkipped) place() string { return placeOf(s.onBoard, s.status) }

// exclude leaves out of the issues found those --exclude names
// (excluded, in the default repository when bare), and returns the rest
// to read, with a warning for each excluded issue the release does not
// include, a typo say. The parsing is --by's (parseBlockers): the same
// forms, repeated or comma-separated.
func (r *releaseRun) exclude(excluded []issue.Ref) (refs []issue.Ref, warnings []string) {
	for _, f := range r.found {
		if slices.ContainsFunc(excluded, f.Ref.Same) {
			r.skipped = append(r.skipped, releaseSkipped{found: f, reason: skipExcluded})
			continue
		}
		refs = append(refs, f.Ref)
	}
	for _, x := range excluded {
		if !slices.ContainsFunc(r.found, func(f release.Found) bool { return f.Ref.Same(x) }) {
			warnings = append(warnings, fmt.Sprintf("--exclude %s: not in the release", x.String(r.def)))
		}
	}
	return refs, warnings
}

// excludedLine names the issues --exclude left out, with how each was
// found; "" for none:
//
//	excluded: acme/api#3 (commit de25f45)
func (r *releaseRun) excludedLine() string {
	return r.skippedLine("excluded", skipExcluded, func(s releaseSkipped) string { return r.sources(s.found) })
}

// revertedLine names the issues only commits reverted later in the range
// name or close, with how each was found; "" for none:
//
//	reverted: #12 (commit 1a2b3c4)
func (r *releaseRun) revertedLine() string {
	return r.skippedLine("reverted", skipReverted, func(s releaseSkipped) string { return r.sources(s.found) })
}

// skippedLine is label and the issues left out for reason, each with what
// about says of it in parentheses, naming at most issue.MaxRefNames; "" for
// none.
func (r *releaseRun) skippedLine(label, reason string, about func(releaseSkipped) string) string {
	var names []string
	for _, s := range r.skipped {
		if s.reason == reason {
			names = append(names, s.found.Ref.String(r.def)+" ("+about(s)+")")
		}
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) > issue.MaxRefNames {
		names = append(names[:issue.MaxRefNames], fmt.Sprintf("and %d more", len(names)-issue.MaxRefNames))
	}
	return label + ": " + strings.Join(names, ", ")
}

// settled leaves out of items, with their reason, the issues closed in a
// final status (statuses.final): done with, whatever the verb. In a
// process where some issues go to Done without a release (docs, CI),
// their commits are still in the range of the next one; running the verb
// on them would refuse them (a verb that works only on open issues, such
// as stage) or repeat it (done), and a repeat recomputes the sprint. Their
// plans, if any, are dropped, so nothing is written to them.
//
// An issue the board's own workflows moved to the final status
// (StatusAutomated), such as "Item closed" moving an issue closed by
// "Fixes #12" from In progress to Done, did not go through the process:
// it stays with the verb, which refuses it or repeats, as before.
func (r *releaseRun) settled(items []*batch.Item) []*batch.Item {
	var out []*batch.Item
	for _, it := range items {
		is := it.Issue
		if is == nil || !is.Closed || !is.OnBoard || is.StatusAutomated || !config.HasStatus(r.final, is.Status) {
			out = append(out, it)
			continue
		}
		r.skipped = append(r.skipped, releaseSkipped{found: r.foundOf(it.Ref), reason: skipClosed, onBoard: true, status: is.Status})
	}
	return out
}

// closedLine names the issues closed in a final status, with their
// statuses; "" for none:
//
//	already closed: #200 (Done), #201 (Done)
func (r *releaseRun) closedLine() string {
	return r.skippedLine("already closed", skipClosed, releaseSkipped.place)
}

// ready leaves out of items, with their reason, the open issues verb v
// refused only for their status (workflow.ErrFrom): work a commit is
// about but not done in the process yet, such as an issue still in
// progress with a "Refs #12" commit. They are listed, not failed: a
// release that refused them would end with exit code 2 on every release
// made while work goes on. Only a later release whose commits name such
// an issue again picks it up, so the line says so.
//
// A closed issue so refused stays a refusal (exit code 2): it was closed
// ("Fixes #12" in a commit, by hand) out of the process, and no later
// release would move it; the user decides. The verb's other refusals (a
// blocker, the user's go-ahead) fail the batch as well.
func (r *releaseRun) ready(v config.Verb, items []*batch.Item) []*batch.Item {
	var out []*batch.Item
	for _, it := range items {
		if it.Err == nil || it.Issue == nil || !errors.Is(it.Err, workflow.ErrFrom) {
			out = append(out, it)
			continue
		}
		is := it.Issue
		where := placeOf(is.OnBoard, is.Status)
		if is.Closed {
			in := "in " + where
			if !is.OnBoard || is.Status == "" {
				in = where
			}
			it.Fail(render.Denied("%s is closed but %s; %s\nthis is a process rule: ask the user how to proceed",
				it.Ref.String(r.def), in, v.Name+" "+v.FromRule()).WithCause(it.Err))
			out = append(out, it)
			continue
		}
		r.skipped = append(r.skipped, releaseSkipped{found: r.foundOf(it.Ref), reason: skipNotReady, onBoard: is.OnBoard, status: is.Status})
	}
	return out
}

// placeOf is where an issue stands on the board: its status, "no status"
// or "not on the board".
func placeOf(onBoard bool, status string) string {
	switch {
	case !onBoard:
		return "not on the board"
	case status == "":
		return "no status"
	}
	return status
}

// notReadyLine names the issues not ready for verb v, with their
// statuses; "" for none:
//
//	not ready (done starts only from Ready): #41 (In progress), #44 (Backlog)
func (r *releaseRun) notReadyLine(v config.Verb) string {
	return r.skippedLine("not ready ("+v.Name+" "+v.FromRule()+")", skipNotReady, releaseSkipped.place)
}

// foundOf is how ref was found.
func (r *releaseRun) foundOf(ref issue.Ref) release.Found {
	if i := slices.IndexFunc(r.found, func(f release.Found) bool { return f.Ref.Same(ref) }); i >= 0 {
		return r.found[i]
	}
	return release.Found{Ref: ref}
}

// keep leaves out, with a warning, a number a commit message names that
// is no issue: a pull request ("Fixes #12" may mean the pull request),
// and, when only messages name it, a missing issue or one of a repository
// outside the project's (r.repos) that is not found, which may be the
// token's lack of access (GitHub answers the same for a repository it
// hides): an example of the syntax in a message, say. A message is text,
// a guess at a link, and failing the release on it would help no one.
//
// A repository of the project's own not found is the token's lack of
// access almost surely, so it fails the batch (exit code 3) as before;
// so does any other error, such as a token GitHub denies outright. An
// issue a merged pull request closes is GitHub's link: if it cannot be
// read, the batch fails as any batch does. A pull request found through a
// closing reference cannot happen.
func (r *releaseRun) keep(cmd *cobra.Command) func(batch.Read) bool {
	return func(read batch.Read) bool {
		f := r.foundOf(read.Ref)
		name := f.Ref.String(r.def)
		var reason, w string
		switch {
		case read.Cause == issue.CausePullRequest:
			reason, w = skipPullRequest, name+" is a pull request, not an issue"
		case read.Cause == "" || !byMessageOnly(f):
			return true
		case read.Cause == issue.CauseMissing:
			reason, w = skipNotFound, name+" not found"
		case read.Cause == issue.CauseNoRepo && !r.ours(f.Ref.Repo):
			reason, w = skipRepoNotFound, fmt.Sprintf("%s: repository %s not found, or the token cannot see it", name, f.Ref.Repo)
		default:
			return true
		}
		r.skipped = append(r.skipped, releaseSkipped{found: f, reason: reason})
		w += ": left out (" + r.sources(f) + ")"
		r.skipWarnings = append(r.skipWarnings, w)
		render.Warn(cmd.ErrOrStderr(), "%s", w)
		return false
	}
}

// ours reports whether repo is one of the project's own (r.repos).
func (r *releaseRun) ours(repo issue.Repo) bool {
	return slices.ContainsFunc(r.repos, repo.Same)
}

// byMessageOnly reports whether only commit messages name f, no merged
// pull request.
func byMessageOnly(f release.Found) bool {
	return len(f.Sources) > 0 && !slices.ContainsFunc(f.Sources, func(s release.Source) bool { return s.Pull != nil })
}

// head is the first line of the text: the range and how many issues it
// includes, n, those not ready included.
//
//	release v0.2.0: 12 commits since v0.1.0, 3 issues
func (r *releaseRun) head(n int) string {
	since := r.rng.Since
	if !r.rng.Picked && len(since) > 7 && strings.HasPrefix(r.rng.SinceCommit, strings.ToLower(since)) {
		since = release.Short(since) // a SHA, shortened as output names commits
	}
	commits := render.Plural(len(r.rng.Commits), "commit", "commits")
	switch {
	case len(r.rng.Commits) == 0:
		return fmt.Sprintf("release %s: no commits since %s, no issues", r.rng.Head, since)
	case len(r.found) == 0 && !slices.ContainsFunc(r.skipped, func(s releaseSkipped) bool { return s.reason == skipReverted }):
		return fmt.Sprintf("release %s: %s since %s, no issues: %s", r.rng.Head, commits, since, r.noIssues())
	case len(r.found) == 0: // the reverted line follows
		return fmt.Sprintf("release %s: %s since %s, no issues", r.rng.Head, commits, since)
	default:
		return fmt.Sprintf("release %s: %s since %s, %s", r.rng.Head, commits, since, render.Plural(n, "issue", "issues"))
	}
}

// noIssues says where release looked.
func (r *releaseRun) noIssues() string {
	suffix := ""
	if r.suffix {
		suffix = " or in a suffix"
	}
	if len(r.keywords) == 0 {
		if r.suffix {
			return "no merged pull request closes one, no commit message names one in a suffix ([release] keywords is empty)"
		}
		return "no merged pull request closes one ([release] keywords is empty)"
	}
	kw := strings.Join(r.keywords[:min(len(r.keywords), 4)], ", ")
	if len(r.keywords) > 4 {
		kw += ", …"
	}
	return "no merged pull request closes one, no commit message names one after " + kw + suffix
}

// list prints the issues planned with how each was found, for a dry run:
//
//	#5: PR #12
//	acme/api#3: commit 1a2b3c4
func (r *releaseRun) list(w io.Writer, items []*batch.Item) {
	for _, it := range items {
		fmt.Fprintf(w, "  %s: %s\n", it.Ref.String(r.def), r.sources(r.foundOf(it.Ref)))
	}
}

// maxSources is how many ways an issue was found the text names.
const maxSources = 3

// sources says how f was found: "PR #12, commit 1a2b3c4 and 2 more"; a
// commit whose suffix names it is "suffix of commit 1a2b3c4".
func (r *releaseRun) sources(f release.Found) string {
	var parts []string
	for _, s := range f.Sources[:min(len(f.Sources), maxSources)] {
		switch {
		case s.Pull != nil:
			parts = append(parts, "PR "+s.Pull.String(r.def))
		case s.By == release.BySuffix:
			parts = append(parts, "suffix of commit "+release.Short(s.Commit))
		default:
			parts = append(parts, "commit "+release.Short(s.Commit))
		}
	}
	text := strings.Join(parts, ", ")
	if n := len(f.Sources) - maxSources; n > 0 {
		text += fmt.Sprintf(" and %d more", n)
	}
	return text
}

// unsuffixedWarning says that commits name u only in a suffix, which
// [release] suffix off does not read, and what to do: run verb on it if
// it is an issue of the release (it is not read, so it may be a pull
// request), and name issues after a keyword in later commits (keywords,
// the config's).
//
//	#85: named only in the suffix of commit b428f16, and a title's (#85) counts only with [release] suffix = true: left out; if #85 is an issue of this release, run gh dojo done 85, and name issues as Refs #85 in later commits
func unsuffixedWarning(u release.Unsuffixed, verb string, keywords []string, def issue.Repo) string {
	name := u.Ref.String(def)
	var commits []string
	for _, c := range u.Commits[:min(len(u.Commits), maxSources)] {
		commits = append(commits, release.Short(c))
	}
	which := "commit " + commits[0]
	if len(u.Commits) > 1 {
		which = "commits " + strings.Join(commits, ", ")
		if n := len(u.Commits) - maxSources; n > 0 {
			which += fmt.Sprintf(" and %d more", n)
		}
	}
	later := "set [release] suffix = true for later commits"
	if kw := keywordExample(keywords); kw != "" {
		later = "name issues as " + kw + " " + name + " in later commits"
	}
	return fmt.Sprintf("%s: named only in the suffix of %s, and a title's (%s) counts only with "+
		"[release] suffix = true: left out; if %s is an issue of this release, run gh dojo %s %s, and %s",
		name, which, name, name, verb, u.Ref.Arg(def), later)
}

// keywordExample is the word of [release] keywords to show before an
// issue, capitalized as a message starts a line: Refs, which only says a
// commit is about the issue, when the project has it, else Fixes, else
// the first; "" for none.
func keywordExample(keywords []string) string {
	if len(keywords) == 0 {
		return ""
	}
	kw := keywords[0]
	for _, want := range []string{"fixes", "refs"} {
		if i := slices.IndexFunc(keywords, func(k string) bool { return strings.EqualFold(k, want) }); i >= 0 {
			kw = keywords[i]
		}
	}
	r, n := utf8.DecodeRuneInString(kw)
	return string(unicode.ToUpper(r)) + kw[n:]
}

// releaseResult is release's JSON: the verb's batch (see verbResult) with
// the range, how each issue was found, and the issues left out.
type releaseResult struct {
	Verb        string           `json:"verb"`
	DefaultRepo *string          `json:"default_repo"`
	DryRun      bool             `json:"dry_run"`
	Release     releaseRangeJSON `json:"release"`
	// Issues are those found, in order (the default repository's first,
	// by number), with how each was found.
	Issues []releaseIssueJSON `json:"issues"`
	// Skipped are the issues found and left out: those only reverted
	// commits name or close, by --exclude, numbers only commit messages
	// name that are no issue (a pull request, a missing issue or
	// repository), issues closed in a final status and issues not ready
	// for the verb.
	Skipped []releaseSkipJSON `json:"skipped"`
	// NotInRelease are the board's issues in a status where finished work
	// waits for a release (Ready for done) that the release does not
	// include (see releaseRun.readPending), the warning's issues.
	NotInRelease []releasePendingJSON `json:"not_in_release"`
	Summary      verbSummaryJSON      `json:"summary"`
	Warnings     []string             `json:"warnings"`
}

type releasePendingJSON struct {
	Number int    `json:"number"`
	Repo   string `json:"repo"`
	Ref    string `json:"ref"`
	Title  string `json:"title"`
	State  string `json:"state"` // open or closed
	Status string `json:"status"`
}

type releaseRangeJSON struct {
	Ref    string `json:"ref"`    // as given
	Commit string `json:"commit"` // its commit's oid
	// Since is where the range starts, as named: the previous version tag
	// release picked (since_picked) or --since; SinceCommit its commit's
	// oid.
	Since       string `json:"since"`
	SincePicked bool   `json:"since_picked"`
	SinceCommit string `json:"since_commit"`
	// Compared: GitHub compared the two; false: ref's history was read
	// down to a --since commit.
	Compared bool `json:"compared"`
	Commits  int  `json:"commits"`
}

type releaseIssueJSON struct {
	verbIssueJSON
	FoundBy []releaseSourceJSON `json:"found_by"`
}

// releaseSourceJSON is one way an issue was found: a merged pull request
// ("owner/name#12") that closes it, with its first commit in the release,
// or a commit whose message names it (pull_request null). By says which:
// pull_request, keyword or suffix.
type releaseSourceJSON struct {
	By          string  `json:"by"`
	PullRequest *string `json:"pull_request"`
	Commit      string  `json:"commit"`
}

type releaseSkipJSON struct {
	Number int    `json:"number"`
	Repo   string `json:"repo"`
	Ref    string `json:"ref"`
	// Reason is reverted, excluded, pull_request, not_found,
	// repo_not_found, closed or not_ready.
	Reason string `json:"reason"`
	// OnBoard and Status are where an issue closed or not ready stands:
	// whether it is on the board and its status, null for none; both null
	// for the other reasons.
	OnBoard *bool               `json:"on_board"`
	Status  *string             `json:"status"`
	FoundBy []releaseSourceJSON `json:"found_by"`
}

func (r *releaseRun) json(verb, repo string, dry bool, items []*batch.Item, warnings []string) releaseResult {
	v := verbJSON(verb, repo, dry, items)
	out := releaseResult{
		Verb: v.Verb, DefaultRepo: v.DefaultRepo, DryRun: dry,
		Release: releaseRangeJSON{
			Ref: r.rng.Head, Commit: r.rng.HeadCommit, Since: r.rng.Since, SincePicked: r.rng.Picked,
			SinceCommit: r.rng.SinceCommit, Compared: r.rng.Compared, Commits: len(r.rng.Commits),
		},
		Issues: []releaseIssueJSON{}, Skipped: []releaseSkipJSON{}, NotInRelease: []releasePendingJSON{},
		Summary: v.Summary, Warnings: render.OrEmpty(warnings),
	}
	for _, p := range r.pending {
		state := "open"
		if p.closed {
			state = "closed"
		}
		out.NotInRelease = append(out.NotInRelease, releasePendingJSON{Number: p.ref.Number, Repo: p.ref.Repo.String(),
			Ref: fullRef(p.ref), Title: p.title, State: state, Status: p.status})
	}
	for i, it := range items {
		out.Issues = append(out.Issues, releaseIssueJSON{verbIssueJSON: v.Issues[i], FoundBy: toReleaseSourcesJSON(r.foundOf(it.Ref))})
	}
	for _, s := range r.skipped {
		skip := releaseSkipJSON{Number: s.found.Ref.Number, Repo: s.found.Ref.Repo.String(), Ref: fullRef(s.found.Ref), Reason: s.reason,
			FoundBy: toReleaseSourcesJSON(s.found)}
		if s.reason == skipClosed || s.reason == skipNotReady {
			skip.OnBoard = &s.onBoard
			if s.onBoard {
				skip.Status = render.OrNull(s.status)
			}
		}
		out.Skipped = append(out.Skipped, skip)
	}
	return out
}

func toReleaseSourcesJSON(f release.Found) []releaseSourceJSON {
	out := []releaseSourceJSON{}
	for _, s := range f.Sources {
		src := releaseSourceJSON{By: string(s.By), Commit: s.Commit}
		if s.Pull != nil {
			src.PullRequest = render.OrNull(fullRef(*s.Pull))
		}
		out = append(out, src)
	}
	return out
}
