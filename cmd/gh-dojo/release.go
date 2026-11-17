package main

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Trum-ok/gh-dojo/internal/batch"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/release"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// gh dojo release (spec F11): a verb applied to the issues a release
// includes, found through the API (internal/release) and applied as a
// batch, the path of gh dojo done 12 13 14: the same plans, summary and
// exit codes.
//
// Requests: one for the tags and the head (one per 100 tags), one per
// release.PageSize commits with their pull requests, then the batch's
// (one read per issue.MaxBatch issues, a mutation per issue to write).

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
		Long: "Use it once a release is tagged: it finds the issues the release includes and\n" +
			"applies a verb with batch = true to them, as gh dojo <verb> 12 13 14 would.\n" +
			"Before the tag, gh dojo release main --dry-run shows what the next one holds.\n\n" +
			"The release is the commits of <ref> (a tag, branch or commit on GitHub) since\n" +
			"the previous release: the highest version tag (v1.2.3) below <ref> when <ref>\n" +
			"is one, else the highest; prereleases (v1.2.3-rc.1) only for a prerelease.\n" +
			"--since names the start instead; the first release needs it. A tag or branch\n" +
			"is compared with <ref> on GitHub; from a commit, <ref>'s history is read down\n" +
			"to it. At most 1000 commits, read without a local checkout, so a branch is\n" +
			"named as on GitHub: main, not origin/main.\n\n" +
			"An issue is in the release when a merged pull request of a commit closes it\n" +
			"(its closing keywords or linked issues; squash, merge and rebase alike), or\n" +
			"when a commit message names it after a keyword of [release] keywords, by\n" +
			"default GitHub's closing ones (closes, fixes, resolves); owner/repo#N names\n" +
			"an issue of another repository. With [release] suffix = true, so do the\n" +
			"parentheses that end a message's first line: \"fix: login limit (#12)\",\n" +
			"\"(#12, #13)\". Code in a message (`Refs #12`, a ``` block)\n" +
			"does not count: put examples of the syntax in backticks.\n\n" +
			"A message names issues as text, so a number only messages name that is no\n" +
			"issue (a pull request, a missing issue, or one of a repository outside\n" +
			"board.repos that is not found or the token cannot see) is left out with a\n" +
			"warning, not failed. --exclude leaves out any issue found, however it was\n" +
			"found.\n\n" +
			"An open issue whose status the verb's from does not allow (still in progress,\n" +
			"say) is listed as not ready and left out, not failed; only a later release\n" +
			"whose commits name it again picks it up. A closed one is refused (exit code 2),\n" +
			"unless it is in a final status (statuses.final): then it is done with, listed as\n" +
			"already closed and left out, and the verb does not touch it.\n\n" +
			"--dry-run lists each issue with how it was found and its plan.",
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
	f.StringVar(&o.verb, "verb", "", "the verb to apply to the release's issues: one with batch = true")
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
	found, warnings := release.Collect(rng, release.NewMatcher(cfg.Release.Keywords, cfg.Release.Suffix), clientHost())
	warnings = append(slices.Clone(rng.Warnings), warnings...)
	for i := range found {
		found[i].Ref = found[i].Ref.In(def) // the default repository as the config spells it
	}
	slices.SortStableFunc(found, func(a, b release.Found) int { return compareRefs(def)(a.Ref, b.Ref) })

	res := &releaseRun{rng: rng, def: def, found: found, keywords: cfg.Release.Keywords, suffix: cfg.Release.Suffix,
		repos: []issue.Repo{def}, final: cfg.Statuses.Final}
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
		return emit(cmd, res.json(v.Name, backendRepo(be), o.dryRun, nil, warnings), func(w io.Writer) {
			fmt.Fprintln(w, res.head(0))
			if line := res.excludedLine(); line != "" {
				fmt.Fprintln(w, line)
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
			for _, line := range []string{res.excludedLine(), res.closedLine(), res.notReadyLine(v)} {
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
	var summary error
	if len(items) > 0 { // none: the head line said why
		summary = b.Summary(items, o.dryRun)
	}
	return emitWrite(cmd, res.json(v.Name, backendRepo(be), o.dryRun, items, append(warnings, res.skipWarnings...)),
		func(io.Writer) {}, summary, slices.ContainsFunc(items, (*batch.Item).Wrote))
}

// releaseVerb is the verb --verb names: one with batch = true.
func releaseVerb(cmd *cobra.Command, cfg *config.Config, name string) (config.Verb, error) {
	var batch []string
	for _, v := range cfg.Verbs {
		if v.Batch {
			batch = append(batch, v.Name)
		}
	}
	example := "<verb>"
	if len(batch) > 0 {
		example = batch[0]
	}
	if name == "" {
		return config.Verb{}, render.Usage("release needs --verb, the verb to apply to the release's issues").
			WithAllowed(batch...).WithFix(fmt.Sprintf("%s <ref> --verb %s", cmd.CommandPath(), example))
	}
	v, ok := cfg.Verb(name)
	if !ok {
		return config.Verb{}, render.Usage("release --verb %s: no such verb in %s", name, cfg.Display).
			WithAllowed(batch...).WithFix("gh dojo describe")
	}
	if !v.Batch {
		e := render.Usage("release --verb %s: %s takes one issue at a time; release needs a verb with batch = true%s",
			name, name, batchVerbsNote(cfg))
		return config.Verb{}, e.WithFix(fmt.Sprintf("add batch = true under [verbs.%s] in %s", name, cfg.Display))
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
	// skipped are the issues left out: by --exclude, then once read a
	// number a commit message names that is no issue, or an issue not
	// ready for the verb or closed in a final status.
	skipped      []releaseSkipped
	skipWarnings []string
}

// Why release leaves out an issue it found.
const (
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
	// status is where an issue not ready or closed stands ("In progress",
	// "Done", "no status", "not on the board").
	status string
}

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
	var names []string
	for _, s := range r.skipped {
		if s.reason == skipExcluded {
			names = append(names, s.found.Ref.String(r.def)+" ("+r.sources(s.found)+")")
		}
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) > maxErrorRefs {
		names = append(names[:maxErrorRefs], fmt.Sprintf("and %d more", len(names)-maxErrorRefs))
	}
	return "excluded: " + strings.Join(names, ", ")
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
		if is == nil || !is.Closed || !is.OnBoard || is.StatusAutomated || !slices.Contains(r.final, is.Status) {
			out = append(out, it)
			continue
		}
		r.skipped = append(r.skipped, releaseSkipped{found: r.foundOf(it.Ref), reason: skipClosed, status: is.Status})
	}
	return out
}

// closedLine names the issues closed in a final status, with their
// statuses; "" for none:
//
//	already closed: #200 (Done), #201 (Done)
func (r *releaseRun) closedLine() string {
	var names []string
	for _, s := range r.skipped {
		if s.reason == skipClosed {
			names = append(names, s.found.Ref.String(r.def)+" ("+s.status+")")
		}
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) > maxErrorRefs {
		names = append(names[:maxErrorRefs], fmt.Sprintf("and %d more", len(names)-maxErrorRefs))
	}
	return "already closed: " + strings.Join(names, ", ")
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
		where := placeOf(*it.Issue)
		if it.Issue.Closed {
			in := "in " + where
			if !it.Issue.OnBoard || it.Issue.Status == "" {
				in = where
			}
			it.Fail(render.Denied("%s is closed but %s; %s\nthis is a process rule: ask the user how to proceed",
				it.Ref.String(r.def), in, fromRule(v)).WithCause(it.Err))
			out = append(out, it)
			continue
		}
		r.skipped = append(r.skipped, releaseSkipped{found: r.foundOf(it.Ref), reason: skipNotReady, status: where})
	}
	return out
}

// placeOf is where is stands on the board: its status, "no status" or
// "not on the board".
func placeOf(is workflow.Issue) string {
	switch {
	case !is.OnBoard:
		return "not on the board"
	case is.Status == "":
		return "no status"
	}
	return is.Status
}

// fromRule says where verb v runs: "done starts only from Ready or In
// progress", "hold runs only in In review", as its refusal does.
func fromRule(v config.Verb) string {
	from := strings.Join(v.From, ", ")
	if n := len(v.From); n > 1 {
		from = strings.Join(v.From[:n-1], ", ") + " or " + v.From[n-1]
	}
	if v.To == "" {
		return v.Name + " runs only in " + from
	}
	return v.Name + " starts only from " + from
}

// notReadyLine names the issues not ready for verb v, with their
// statuses; "" for none:
//
//	not ready (done starts only from Ready): #41 (In progress), #44 (Backlog)
func (r *releaseRun) notReadyLine(v config.Verb) string {
	var names []string
	for _, s := range r.skipped {
		if s.reason == skipNotReady {
			names = append(names, s.found.Ref.String(r.def)+" ("+s.status+")")
		}
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) > maxErrorRefs {
		names = append(names[:maxErrorRefs], fmt.Sprintf("and %d more", len(names)-maxErrorRefs))
	}
	return fmt.Sprintf("not ready (%s): %s", fromRule(v), strings.Join(names, ", "))
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
	return slices.ContainsFunc(r.repos, func(o issue.Repo) bool { return strings.EqualFold(o.String(), repo.String()) })
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
	case len(r.found) == 0:
		return fmt.Sprintf("release %s: %s since %s, no issues: %s", r.rng.Head, commits, since, r.noIssues())
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

// releaseResult is release's JSON: the verb's batch (see verbResult) with
// the range, how each issue was found, and the issues left out.
type releaseResult struct {
	Verb        string       `json:"verb"`
	DefaultRepo *string      `json:"default_repo"`
	DryRun      bool         `json:"dry_run"`
	Release     releaseRange `json:"release"`
	// Issues are those found, in order (the default repository's first,
	// by number), with how each was found.
	Issues []releaseIssue `json:"issues"`
	// Skipped are the issues found and left out: by --exclude, numbers
	// only commit messages name that are no issue (a pull request, a
	// missing issue or repository), issues closed in a final status and
	// issues not ready for the verb.
	Skipped  []releaseSkip `json:"skipped"`
	Summary  verbSummary   `json:"summary"`
	Warnings []string      `json:"warnings"`
}

type releaseRange struct {
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

type releaseIssue struct {
	verbIssue
	FoundBy []releaseSource `json:"found_by"`
}

// releaseSource is one way an issue was found: a merged pull request
// ("owner/name#12") that closes it, with its first commit in the release,
// or a commit whose message names it (pull_request null). By says which:
// pull_request, keyword or suffix.
type releaseSource struct {
	By          string  `json:"by"`
	PullRequest *string `json:"pull_request"`
	Commit      string  `json:"commit"`
}

type releaseSkip struct {
	Number int    `json:"number"`
	Repo   string `json:"repo"`
	Ref    string `json:"ref"`
	// Reason is excluded, pull_request, not_found, repo_not_found, closed
	// or not_ready.
	Reason string `json:"reason"`
	// Status is where an issue closed or not ready stands: a status, "no
	// status" or "not on the board"; null for the other reasons.
	Status  *string         `json:"status"`
	FoundBy []releaseSource `json:"found_by"`
}

func (r *releaseRun) json(verb, repo string, dry bool, items []*batch.Item, warnings []string) releaseResult {
	v := verbJSON(verb, repo, dry, items)
	out := releaseResult{
		Verb: v.Verb, DefaultRepo: v.DefaultRepo, DryRun: dry,
		Release: releaseRange{
			Ref: r.rng.Head, Commit: r.rng.HeadCommit, Since: r.rng.Since, SincePicked: r.rng.Picked,
			SinceCommit: r.rng.SinceCommit, Compared: r.rng.Compared, Commits: len(r.rng.Commits),
		},
		Issues: []releaseIssue{}, Skipped: []releaseSkip{}, Summary: v.Summary, Warnings: render.OrEmpty(warnings),
	}
	for i, it := range items {
		out.Issues = append(out.Issues, releaseIssue{verbIssue: v.Issues[i], FoundBy: sourcesJSON(r.foundOf(it.Ref))})
	}
	for _, s := range r.skipped {
		out.Skipped = append(out.Skipped, releaseSkip{Number: s.found.Ref.Number, Repo: s.found.Ref.Repo.String(), Ref: fullRef(s.found.Ref), Reason: s.reason, Status: render.OrNull(s.status),
			FoundBy: sourcesJSON(s.found)})
	}
	return out
}

func sourcesJSON(f release.Found) []releaseSource {
	out := []releaseSource{}
	for _, s := range f.Sources {
		src := releaseSource{By: string(s.By), Commit: s.Commit}
		if s.Pull != nil {
			src.PullRequest = render.OrNull(fullRef(*s.Pull))
		}
		out = append(out, src)
	}
	return out
}
