package main

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

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
	dryRun      bool
	args        workflow.Args
}

func newReleaseCmd() (*cobra.Command, error) {
	var o releaseOpts
	cmd := &cobra.Command{
		Use:   "release <ref> --verb <verb>",
		Short: "Apply a verb to the issues a release includes, found through pull requests and commits",
		Long: "Use it once a release is tagged: it finds the issues the release includes and\n" +
			"applies a verb with batch = true to them, as gh dojo <verb> 12 13 14 would.\n\n" +
			"The release is the commits of <ref> (a tag, branch or commit on GitHub) since\n" +
			"the previous release: the highest version tag (v1.2.3) below <ref> when <ref>\n" +
			"is one, else the highest; prereleases (v1.2.3-rc.1) only for a prerelease.\n" +
			"--since names the start instead; the first release needs it. A tag or branch\n" +
			"is compared with <ref> on GitHub; from a commit, <ref>'s history is read down\n" +
			"to it. At most 1000 commits, read without a local checkout.\n\n" +
			"An issue is in the release when a merged pull request of a commit closes it\n" +
			"(its closing keywords or linked issues; squash, merge and rebase alike), or\n" +
			"when a commit message names it after a keyword of [release] keywords, by\n" +
			"default GitHub's closing ones (closes, fixes, resolves); owner/repo#N names\n" +
			"an issue of another repository.\n\n" +
			"An issue whose status the verb's from does not allow (still in progress, say)\n" +
			"is listed as not ready and left out, not failed; its other checks still apply.\n\n" +
			"--dry-run lists each issue with how it was found and its plan.",
		Example: `  gh dojo release v0.2.0 --verb done --dry-run
  gh dojo release v0.2.0 --verb done
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
	f.BoolVar(&o.dryRun, "dry-run", false, "list the issues, how each was found and its plan, without changing anything")
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
	found, warnings := release.Collect(rng, release.NewMatcher(cfg.Release.Keywords), clientHost())
	warnings = append(slices.Clone(rng.Warnings), warnings...)
	for _, w := range warnings {
		render.Warn(cmd.ErrOrStderr(), "%s", w)
	}
	for i := range found {
		found[i].Ref = found[i].Ref.In(def) // the default repository as the config spells it
	}
	slices.SortStableFunc(found, func(a, b release.Found) int { return compareRefs(def)(a.Ref, b.Ref) })

	res := &releaseRun{rng: rng, def: def, found: found, keywords: cfg.Release.Keywords}
	if len(found) == 0 {
		return emit(cmd, res.json(v.Name, backendRepo(be), o.dryRun, nil, warnings), func(w io.Writer) {
			fmt.Fprintln(w, res.head(0))
		})
	}
	b := newBatchRun(cmd, be, cfg, v, o.args, def)
	b.keep = res.keep(cmd)
	out := cmd.OutOrStdout()
	// Once the issues are read and planned: the pull requests a message
	// names are left out (keep), and so are the issues not ready.
	b.before = func(items []*batchItem) []*batchItem {
		n := len(items)
		items = res.ready(items)
		if !b.json {
			fmt.Fprintln(out, res.head(n))
			if line := res.notReadyLine(v); line != "" {
				fmt.Fprintln(out, line)
			}
			if o.dryRun {
				res.list(out, items)
			}
		}
		return items
	}
	refs := make([]issue.Ref, len(found))
	for i, f := range found {
		refs[i] = f.Ref
	}
	items, err := b.run(ctx, refs, o.dryRun)
	if err != nil {
		return err
	}
	var summary error
	if len(items) > 0 { // none: the head line said why
		summary = b.summary(items, o.dryRun)
	}
	return emitWrite(cmd, res.json(v.Name, backendRepo(be), o.dryRun, items, append(warnings, res.skipWarnings...)),
		func(io.Writer) {}, summary, slices.ContainsFunc(items, (*batchItem).wrote))
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
	return v, nil
}

// releaseRun is what release found, for its output.
type releaseRun struct {
	rng      *release.Range
	def      issue.Repo
	found    []release.Found
	keywords []string
	// skipped are the issues left out once read: a pull request a commit
	// message names, or an issue not ready for the verb.
	skipped      []releaseSkipped
	skipWarnings []string
}

// Why release leaves out an issue it found.
const (
	skipPullRequest = "pull_request" // a commit message names a pull request
	skipNotReady    = "not_ready"    // the verb's from refuses the issue's status
)

type releaseSkipped struct {
	found  release.Found
	reason string // skipPullRequest or skipNotReady
	status string // not ready: where the issue stands ("In progress", "no status", "not on the board")
}

// ready leaves out of items, with their reason, the issues the verb
// refused only for their status (workflow.ErrFrom): work a commit is
// about but not done in the process yet, such as an issue still in
// progress with a "Refs #12" commit. They are listed, not failed: a
// release that refused them would end with exit code 2 on every release
// made while work goes on. The verb's other refusals (a closed issue, a
// blocker, the user's go-ahead) still fail the batch.
func (r *releaseRun) ready(items []*batchItem) []*batchItem {
	var out []*batchItem
	for _, it := range items {
		if it.err == nil || it.read == nil || !errors.Is(it.err, workflow.ErrFrom) {
			out = append(out, it)
			continue
		}
		status := it.read.Status
		switch {
		case !it.read.OnBoard:
			status = "not on the board"
		case status == "":
			status = "no status"
		}
		r.skipped = append(r.skipped, releaseSkipped{found: r.foundOf(it.ref), reason: skipNotReady, status: status})
	}
	return out
}

// notReadyLine names the issues not ready for verb v, with their
// statuses; "" for none:
//
//	not ready for done (it starts only from Ready): #41 (In progress), #44 (Backlog)
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
	from := strings.Join(v.From, ", ")
	if n := len(v.From); n > 1 {
		from = strings.Join(v.From[:n-1], ", ") + " or " + v.From[n-1]
	}
	return fmt.Sprintf("not ready for %s (it starts only from %s): %s", v.Name, from, strings.Join(names, ", "))
}

// foundOf is how ref was found.
func (r *releaseRun) foundOf(ref issue.Ref) release.Found {
	if i := slices.IndexFunc(r.found, func(f release.Found) bool { return f.Ref.Same(ref) }); i >= 0 {
		return r.found[i]
	}
	return release.Found{Ref: ref}
}

// keep leaves out a number a commit message names that is a pull request,
// with a warning: "Fixes #12" in a message may mean the pull request,
// and failing the release on it would help no one. A pull request found
// through a closing reference cannot happen.
func (r *releaseRun) keep(cmd *cobra.Command) func(IssueRead) bool {
	return func(read IssueRead) bool {
		if !read.PullRequest {
			return true
		}
		f := r.foundOf(read.Ref)
		r.skipped = append(r.skipped, releaseSkipped{found: f, reason: skipPullRequest})
		w := fmt.Sprintf("%s is a pull request, not an issue: left out (%s)", f.Ref.String(r.def), r.sources(f))
		r.skipWarnings = append(r.skipWarnings, w)
		render.Warn(cmd.ErrOrStderr(), "%s", w)
		return false
	}
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
	commits := plural(len(r.rng.Commits), "commit", "commits")
	switch {
	case len(r.rng.Commits) == 0:
		return fmt.Sprintf("release %s: no commits since %s, no issues", r.rng.Head, since)
	case len(r.found) == 0:
		return fmt.Sprintf("release %s: %s since %s, no issues: %s", r.rng.Head, commits, since, r.noIssues())
	default:
		return fmt.Sprintf("release %s: %s since %s, %s", r.rng.Head, commits, since, plural(n, "issue", "issues"))
	}
}

// noIssues says where release looked.
func (r *releaseRun) noIssues() string {
	if len(r.keywords) == 0 {
		return "no merged pull request closes one ([release] keywords is empty)"
	}
	kw := strings.Join(r.keywords[:min(len(r.keywords), 4)], ", ")
	if len(r.keywords) > 4 {
		kw += ", …"
	}
	return "no merged pull request closes one, no commit message names one after " + kw
}

// list prints the issues planned with how each was found, for a dry run:
//
//	#5: PR #12
//	acme/api#3: commit 1a2b3c4
func (r *releaseRun) list(w io.Writer, items []*batchItem) {
	for _, it := range items {
		fmt.Fprintf(w, "  %s: %s\n", it.ref.String(r.def), r.sources(r.foundOf(it.ref)))
	}
}

// maxSources is how many ways an issue was found the text names.
const maxSources = 3

// sources says how f was found: "PR #12, commit 1a2b3c4 and 2 more".
func (r *releaseRun) sources(f release.Found) string {
	var parts []string
	for _, s := range f.Sources[:min(len(f.Sources), maxSources)] {
		if s.Pull != nil {
			parts = append(parts, "PR "+s.Pull.String(r.def))
		} else {
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
	// Skipped are the issues found and left out: pull requests a commit
	// message names, and issues not ready for the verb.
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
// or a commit whose message names it (pull_request null).
type releaseSource struct {
	PullRequest *string `json:"pull_request"`
	Commit      string  `json:"commit"`
}

type releaseSkip struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"` // pull_request or not_ready
	// Status is where an issue not ready stands: a status, "no status" or
	// "not on the board"; null for a pull request.
	Status  *string         `json:"status"`
	FoundBy []releaseSource `json:"found_by"`
}

func (r *releaseRun) json(verb, repo string, dry bool, items []*batchItem, warnings []string) releaseResult {
	v := verbJSON(verb, repo, dry, items)
	out := releaseResult{
		Verb: v.Verb, DefaultRepo: v.DefaultRepo, DryRun: dry,
		Release: releaseRange{
			Ref: r.rng.Head, Commit: r.rng.HeadCommit, Since: r.rng.Since, SincePicked: r.rng.Picked,
			SinceCommit: r.rng.SinceCommit, Compared: r.rng.Compared, Commits: len(r.rng.Commits),
		},
		Issues: []releaseIssue{}, Skipped: []releaseSkip{}, Summary: v.Summary, Warnings: orEmpty(warnings),
	}
	for i, it := range items {
		out.Issues = append(out.Issues, releaseIssue{verbIssue: v.Issues[i], FoundBy: sourcesJSON(r.foundOf(it.ref))})
	}
	for _, s := range r.skipped {
		out.Skipped = append(out.Skipped, releaseSkip{Ref: fullRef(s.found.Ref), Reason: s.reason, Status: orNull(s.status),
			FoundBy: sourcesJSON(s.found)})
	}
	return out
}

func sourcesJSON(f release.Found) []releaseSource {
	out := []releaseSource{}
	for _, s := range f.Sources {
		src := releaseSource{Commit: s.Commit}
		if s.Pull != nil {
			src.PullRequest = orNull(fullRef(*s.Pull))
		}
		out = append(out, src)
	}
	return out
}
