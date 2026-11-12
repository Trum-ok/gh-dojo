package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/effects"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
	"github.com/spf13/cobra"
)

// Verbs are commands built from .github/dojo.toml.
//
// Config errors never break gh dojo itself: --help, --version and built-in
// commands keep working. When the config is missing or broken:
//
//   - no verb commands are registered;
//   - the root help ends with the config error (see configNote), so a broken
//     file is visible right where an agent looks for verbs;
//   - an unknown command that is not close to a built-in one (maybe a verb)
//     fails with "unknown command" plus the first line of the reason, the
//     built-ins as allowed values and gh dojo --help as the fix (see
//     verbsUnavailable and unknownCommand);
//   - built-in commands that need the config load it themselves through
//     loadConfig and return its error.

func init() { registerAll(newVerbCmds) }

// verbsGroup lists the verbs apart from built-in commands in the root help.
var verbsGroup = &cobra.Group{ID: "verbs", Title: "Process verbs (from " + config.FileName + "):"}

// loadConfig loads the config of the current directory, once per process.
// Tests replace it.
var loadConfig = sync.OnceValues(func() (*config.Config, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, render.Usage("cannot get the current directory: %v", err).WithCause(err)
	}
	return config.Load(wd)
})

// getenv reads the agent session variables. Tests replace it.
var getenv = os.Getenv

// Backend reads issues and applies plans; it is the only part of a verb that
// talks to GitHub. The GitHub one is githubBackend (backend.go).
//
// Requests: one query for ReadIssue and one mutation for Apply, so 2 with a
// warm board cache. An issue not on the board needs a second mutation:
// addProjectV2ItemById returns the item ID that the status update needs, so
// that case takes 3 requests.
//
// Retries: after a stale ID or a partial failure, the way to resume is to
// run ReadIssue → Plan → Apply again, never to replay the old plan: the
// fresh plan leaves out what is already done, including a comment already
// posted (see workflow.AlreadyPosted). runVerb does that once on a stale
// schema ID, also dropping what Apply confirmed in case the re-read lags
// behind it; after a partial failure the agent re-runs the command.
type Backend interface {
	// ReadIssue returns the state of issue n in the default repo for verb
	// v, filled as workflow.Issue documents (comments with viewerDidAuthor,
	// labels, assignees, and the viewer when v assigns @me), with a render
	// error on failure (render.NotFound for a missing issue; a config error
	// for a label of v to add that is missing in the repo, a label spelled
	// in another case than the repo's, or an assignee who cannot be
	// assigned). The default repo comes from the app wiring:
	// board.repos[0], or the git remote of the current directory.
	//
	// For a verb with blockers = "warn" or "deny" it fills the unresolved
	// blockers; for blockers = "link" with --by naming issues (a), those
	// issues (workflow.Issue.ByIssues), with exit code 3 when one does not
	// exist, and 1 when one is the issue itself, would close a cycle, or
	// --by mixes issues and text.
	ReadIssue(ctx context.Context, v config.Verb, n int, a workflow.Args) (workflow.Issue, error)
	// ReadIssues reads issues ns like ReadIssue, for a verb with batch =
	// true: one request per issue.MaxBatch issues, with what v looks up
	// read once in each. It returns one IssueRead per number, in order; an
	// issue that fails alone (not found, a pull request, its board place
	// or sprint not read, the issue --by names) has its own Err. err fails them all:
	// a config error of v (a label to add missing in the repo) or a
	// request that read nothing. A request that fails after earlier ones
	// read their issues is the Err of each issue it did not read.
	ReadIssues(ctx context.Context, v config.Verb, ns []int, a workflow.Args) ([]IssueRead, error)
	// Apply applies every op of a non-empty plan, finding the issue's node
	// IDs in p.Issue.Ref. It returns the ops GitHub confirmed, also with an
	// error: the rest of p.Ops did not take effect, or their outcome is
	// unknown. An error with every op confirmed is only worth a warning.
	// runVerb reports a partial write (exit code 5); errors keep their
	// cause, so board.IsStale tells a rejected schema ID.
	Apply(ctx context.Context, p *workflow.Plan) ([]workflow.Op, error)
	// Repo is the default repository, where ReadIssue reads, for the JSON.
	Repo() (issue.Repo, error)
	// Reset drops the cached board schema after GitHub rejected one of its
	// IDs, so the next ReadIssue and Apply use a fresh one.
	Reset() error
}

// IssueRead is one issue of ReadIssues: its state, or why it cannot be
// planned.
type IssueRead struct {
	Number int
	Issue  workflow.Issue
	Err    error
}

// newBackend returns the backend for cfg: GitHub through one App per run,
// which loads the same config. Tests replace it.
var newBackend = func(*config.Config) (Backend, error) { return &githubBackend{app: newApp()}, nil }

func newVerbCmds() ([]*cobra.Command, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, nil // see configNote and verbsUnavailable
	}
	cmds := make([]*cobra.Command, 0, len(cfg.Verbs))
	for _, v := range cfg.Verbs {
		cmds = append(cmds, newVerbCmd(cfg, v))
	}
	return cmds, nil
}

func newVerbCmd(cfg *config.Config, v config.Verb) *cobra.Command {
	var (
		args   workflow.Args
		dryRun bool
	)
	use := v.Name + " <N>"
	if v.Batch {
		use = v.Name + " <N>..."
	}
	cmd := &cobra.Command{
		GroupID: verbsGroup.ID,
		Use:     use,
		Short:   v.When,
		Long:    verbHelp(cfg, v),
		Args:    issueArgs(cfg, v),
		RunE: func(cmd *cobra.Command, posArgs []string) error {
			args.HasComment = cmd.Flags().Changed(config.ArgComment)
			args.HasBy = cmd.Flags().Changed(config.ArgBy)
			ns := issueNumbers(posArgs) // validated by issueArgs
			if len(ns) == 1 {
				return runVerb(cmd, cfg, v, ns[0], args, dryRun)
			}
			return runBatch(cmd, cfg, v, ns, args, dryRun)
		},
	}
	f := cmd.Flags()
	f.StringVar(&args.Comment, config.ArgComment, "", "text for {comment} in the comment")
	byUsage := "value for {by} in the comment: an issue number (12 or #12) or free text"
	if v.Blockers == config.BlockersLink {
		byUsage = "value for {by} in the comment: issues (12, #12, owner/repo#12 or URLs, comma-separated), also marked as blocking this one, or free text"
	}
	f.StringVar(&args.By, config.ArgBy, "", byUsage)
	f.BoolVar(&dryRun, "dry-run", false, "print the plan without changing anything")
	f.BoolVar(&args.Yes, "yes", false, "the user agreed: run a verb that is the user's decision")
	// Flags the comment template does not use stay registered but hidden, so
	// passing one gets workflow's precise error instead of "unknown flag".
	for _, arg := range config.Args() {
		if !v.Uses(arg) {
			_ = f.MarkHidden(arg)
		}
	}
	if !v.Confirm {
		_ = f.MarkHidden("yes")
	}
	return cmd
}

// runVerb runs verb v on issue n. An error that read nothing, such as
// bad arguments or the network, is the command's alone; once the issue
// was read (or found missing), its JSON is a verb result for it, as for
// one issue of a batch, also when it is refused (outcome "failed").
func runVerb(cmd *cobra.Command, cfg *config.Config, v config.Verb, n int, args workflow.Args, dryRun bool) error {
	if err := workflow.CheckArgs(v, args, n); err != nil {
		return err
	}
	be, err := newBackend(cfg)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	engine := workflow.New(cfg)
	engine.Getenv = getenv
	it, err := readPlanFresh(ctx, be, engine, v, n, args)
	if err != nil {
		return err
	}
	repo := backendRepo(be)
	none := func(io.Writer) {} // the text of a failed issue is its error
	if it.run == nil {
		return emitResult(cmd, verbJSON(v.Name, repo, dryRun, []*batchItem{it}), none, it.err)
	}
	run, plan := it.run, it.run.first
	run.stderr = cmd.ErrOrStderr()
	for _, w := range plan.Warnings {
		render.Warn(cmd.ErrOrStderr(), "%s", w)
	}
	if dryRun {
		return emit(cmd, verbJSON(v.Name, repo, true, []*batchItem{it}), func(w io.Writer) {
			fmt.Fprintln(w, plan.DryRun())
		})
	}
	if it.err = plan.Unconfirmed(); it.err != nil {
		return emitResult(cmd, verbJSON(v.Name, repo, false, []*batchItem{it}), none, it.err)
	}
	it.err = run.applyFresh(ctx, be, func() (*workflow.Plan, error) { return readPlan(ctx, be, engine, v, n, args) })
	// A partial write prints what it applied as JSON; its text is the error.
	return emitWrite(cmd, verbJSON(v.Name, repo, false, []*batchItem{it}), func(w io.Writer) {
		if it.err == nil {
			fmt.Fprintln(w, run.applied().Confirm())
		}
	}, it.err, it.wrote())
}

// backendRepo is the default repository of be, owner/name, for the JSON;
// "" when not known.
func backendRepo(be Backend) string {
	r, err := be.Repo()
	if err != nil || r == (issue.Repo{}) {
		return ""
	}
	return r.String()
}

// applyFresh applies r.first. When GitHub rejects a board ID from the
// schema cache, part of the plan may have been applied: it drops the
// cache, re-plans with replan and applies what is left, once. A transport
// failure is never retried here: its outcome is unknown and the agent
// re-runs the command. The error is r.partial's.
func (r *verbRun) applyFresh(ctx context.Context, be Backend, replan func() (*workflow.Plan, error)) error {
	plan := r.first
	err := r.apply(ctx, be, plan)
	switch {
	case err == nil:
		return nil
	case !board.IsStale(err):
		return r.partial(missing(plan.Ops, r.done), err, err)
	}
	if rerr := be.Reset(); rerr != nil {
		return r.partial(missing(plan.Ops, r.done), err, err)
	}
	fresh, perr := replan()
	if perr != nil {
		return r.partial(missing(plan.Ops, r.done), err, perr)
	}
	// What the first attempt confirmed is not sent again, even if the
	// re-read does not show it yet.
	fresh.Ops = missing(fresh.Ops, r.done)
	r.fresh = fresh
	if err := r.apply(ctx, be, fresh); err != nil {
		return r.partial(missing(fresh.Ops, r.done), err, err)
	}
	return nil
}

// verbRun is what one verb command applied, across a stale-ID retry.
type verbRun struct {
	first  *workflow.Plan // planned from the state the command found
	fresh  *workflow.Plan // the re-plan of a retry; nil without one
	done   []workflow.Op  // ops GitHub confirmed, from both attempts
	stderr io.Writer
}

// apply applies p and records what GitHub confirmed. An error with every
// op of p confirmed is a warning.
func (r *verbRun) apply(ctx context.Context, be Backend, p *workflow.Plan) error {
	if p.Empty() {
		return nil
	}
	done, err := be.Apply(ctx, p)
	r.done = append(r.done, done...)
	if err != nil && len(missing(p.Ops, done)) == 0 {
		render.Warn(r.stderr, "GitHub reported an error after applying every change: %s", reason(err))
		return nil
	}
	return err
}

// applied is the plan of what this command did, for the confirmation
// line: the first plan's starting state with the confirmed ops. After a
// retry, an effect the re-read found in place counts as such, unless this
// command applied it.
func (r *verbRun) applied() *workflow.Plan {
	shown := *r.first
	shown.Ops = r.done
	if r.fresh != nil {
		shown.StatusUnchanged = r.first.StatusUnchanged ||
			r.fresh.StatusUnchanged && !hasKind(r.done, workflow.SetStatus{})
		shown.CommentPresent = r.first.CommentPresent ||
			r.fresh.CommentPresent && !hasKind(r.done, workflow.AddComment{})
		shown.InPlace = slices.Clone(r.first.InPlace)
		for _, op := range r.fresh.InPlace {
			if !hasKind(r.done, op) && !hasKind(shown.InPlace, op) {
				shown.InPlace = append(shown.InPlace, op)
			}
		}
	}
	return &shown
}

// partial reports a failed write. With nothing confirmed, err itself is
// the answer. Otherwise it is exit code 5, naming what was done and what
// was not:
//
//	#42 comment left; status not changed: GitHub token is missing a required scope (project); gh auth refresh -s project, then re-run the same command
//
// The re-run re-reads the issue, so its plan leaves out what is done; but
// a blocked-by link GitHub refused as a dependency cycle is exit code 1,
// since a re-run would fail the same way. cause
// is the error of the write that left missed undone: per-field GraphQL
// errors or ops never sent (errNotSent) mean they did not happen, a lost
// answer (timeout, 5xx) that they are unconfirmed. err is what stopped the command, and gives the reason.
func (r *verbRun) partial(missed []workflow.Op, cause, err error) error {
	if len(r.done) == 0 {
		return err
	}
	if graph.IsCycle(err) {
		// Re-running cannot help: the link makes a cycle through other issues.
		return render.Usage("%s; %s: %s", r.applied().Confirm(), missedWords(missed), reason(err)).
			WithFix(fmt.Sprintf("gh dojo show %d", r.first.Issue.Number)).WithCause(err)
	}
	e := render.FromError(err)
	next := "re-run the same command"
	if fix := strings.TrimSpace(e.Fix); fix != "" {
		next = fix + ", then " + next
	}
	return render.Partial("%s; %s: %s; %s", r.applied().Confirm(), missedText(missed, cause), reason(err), next).
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
// failed with cause (see partial): the effects not done, or "the rest is
// unconfirmed".
func missedText(missed []workflow.Op, cause error) string {
	var gqlErr *api.GraphQLError
	if (errors.As(cause, &gqlErr) || errors.Is(cause, errNotSent)) && len(missed) > 0 {
		return missedWords(missed)
	}
	return "the rest is unconfirmed"
}

// reason is the first clause of an error's message, for a partial report.
func reason(err error) string {
	what, _, _ := strings.Cut(render.FromError(err).What, "\n")
	what, _, _ = strings.Cut(what, ";")
	return strings.TrimSpace(what)
}

// missing returns the ops of ops that are not among done, compared by
// content: a re-plan may hold an op of a kind already done with other
// values, such as a label to add that the first plan did not have, and
// that one is still to send. SetFields compares field by field, since
// GitHub may set some fields of one and not others (see effects.Outcome).
func missing(ops, done []workflow.Op) []workflow.Op {
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

// readPlan reads issue n and plans verb v on it.
func readPlan(ctx context.Context, be Backend, e *workflow.Engine, v config.Verb, n int, args workflow.Args) (*workflow.Plan, error) {
	is, err := be.ReadIssue(ctx, v, n, args)
	if err != nil {
		return nil, err
	}
	return e.Plan(v, is, args)
}

// schemaCacher is a Backend whose board schema may come from the cache.
type schemaCacher interface {
	// SchemaCached reports whether the schema in use came from the cache.
	SchemaCached() bool
}

// readPlanFresh reads issue n as runBatch reads each issue and plans v
// on it, once more with a fresh board schema when the one used came from
// the cache and may explain the failure (board.IsStale): say, no
// iteration for today, added on the board since the schema was cached.
// Nothing was written yet, so a retry is safe.
//
// err fails the command, with nothing read (the network, a config error
// of v); the issue's own failure (not found, a refusal) is the item's
// err, with read set once the issue was read.
func readPlanFresh(ctx context.Context, be Backend, e *workflow.Engine, v config.Verb, n int, args workflow.Args) (*batchItem, error) {
	it, err := readPlanItem(ctx, be, e, v, n, args)
	failed := err
	if err == nil {
		failed = it.err
	}
	if c, ok := be.(schemaCacher); failed == nil || !ok || !c.SchemaCached() || !board.IsStale(failed) {
		return it, err
	}
	if be.Reset() != nil {
		return it, err
	}
	return readPlanItem(ctx, be, e, v, n, args)
}

// readPlanItem is one try of readPlanFresh.
func readPlanItem(ctx context.Context, be Backend, e *workflow.Engine, v config.Verb, n int, args workflow.Args) (*batchItem, error) {
	reads, err := be.ReadIssues(ctx, v, []int{n}, args)
	if err != nil {
		return nil, err
	}
	if len(reads) != 1 {
		return nil, render.Internal("read %d issues for #%d", len(reads), n)
	}
	it := &batchItem{n: n}
	if r := reads[0]; r.Err != nil {
		it.fail(r.Err)
	} else if p, err := e.Plan(v, r.Issue, args); err != nil {
		it.read = &r.Issue
		it.fail(err)
	} else {
		it.read = &r.Issue
		it.run = &verbRun{first: p}
	}
	return it, nil
}

var issueRE = regexp.MustCompile(`^#?[1-9][0-9]*$`)

// parseIssue parses an issue number, "42" or "#42", up to GitHub's maximum
// (a GraphQL Int, 2^31-1).
func parseIssue(s string) (int, bool) {
	if !issueRE.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 32)
	return int(n), err == nil
}

// issueExamples shows how to pass an issue number. A bare #42 is a comment
// in the shell, so the # form is quoted.
const issueExamples = `42 or "#42"`

// issueArg accepts exactly one issue number.
func issueArg(cmd *cobra.Command, args []string) error {
	help := cmd.CommandPath() + " --help"
	switch {
	case len(args) == 0:
		return render.Usage("%s needs an issue number, like %s", cmd.Name(), issueExamples).WithFix(help)
	case len(args) > 1:
		return render.Usage("%s takes one issue number, got %d; several at once are not supported yet",
			cmd.Name(), len(args)).WithFix(help)
	}
	if _, ok := parseIssue(args[0]); !ok {
		return render.Usage("%q is not an issue number; pass one like %s", args[0], issueExamples).WithFix(help)
	}
	return nil
}

// issueArgs accepts the issue numbers of verb v: one, or with batch =
// true one or more. A number repeated counts once.
func issueArgs(cfg *config.Config, v config.Verb) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		help := cmd.CommandPath() + " --help"
		switch {
		case len(args) == 0 && v.Batch:
			return render.Usage("%s needs one or more issue numbers, like %s", cmd.Name(), issueExamples).WithFix(help)
		case len(args) == 0:
			return render.Usage("%s needs an issue number, like %s", cmd.Name(), issueExamples).WithFix(help)
		}
		for _, a := range args {
			if _, ok := parseIssue(a); !ok {
				return render.Usage("%q is not an issue number; pass one like %s", a, issueExamples).WithFix(help)
			}
		}
		if n := len(issueNumbers(args)); n > 1 && !v.Batch {
			return render.Usage("%s takes one issue number, got %d; several at once need batch = true in verbs.%s%s",
				cmd.Name(), n, v.Name, batchVerbsNote(cfg)).
				WithFix(fmt.Sprintf("run %s once per issue", cmd.CommandPath()))
		}
		return nil
	}
}

// batchVerbsNote names the verbs that take several numbers, for the error
// of a verb that does not.
func batchVerbsNote(cfg *config.Config) string {
	var names []string
	for _, v := range cfg.Verbs {
		if v.Batch {
			names = append(names, v.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "\nverbs that take several: " + strings.Join(names, ", ")
}

// issueNumbers parses the validated issue arguments, dropping repeats.
func issueNumbers(args []string) []int {
	var ns []int
	for _, a := range args {
		if n, ok := parseIssue(a); ok && !slices.Contains(ns, n) {
			ns = append(ns, n)
		}
	}
	return ns
}

// verbHelp is the long help of a verb: its when line, then what it checks,
// needs and does.
func verbHelp(cfg *config.Config, v config.Verb) string {
	var b strings.Builder
	line := func(label, format string, a ...any) {
		if label != "" {
			label += ":"
		}
		fmt.Fprintf(&b, "\n  %-10s %s", label, fmt.Sprintf(format, a...))
	}
	b.WriteString(v.When + "\n")

	from := "any status"
	if len(v.From) > 0 {
		from = strings.Join(v.From, ", ")
		if initial := cfg.Statuses.Initial; slices.Contains(v.From, initial) {
			from += fmt.Sprintf(" (no status or not on the board counts as %s)", initial)
		}
	}
	line("From", "%s", from)
	if v.To != "" {
		line("To", "%s; an issue already there keeps it, so a repeat is safe", v.To)
	} else {
		line("To", "the status is not changed")
	}

	var required, optional []string
	for _, arg := range config.Args() {
		switch {
		case slices.Contains(v.Require, arg):
			required = append(required, "--"+arg)
		case slices.Contains(v.RequireOneOf, arg):
		case v.Uses(arg):
			optional = append(optional, "--"+arg)
		}
	}
	if len(v.RequireOneOf) > 0 {
		names := make([]string, len(v.RequireOneOf))
		for i, arg := range v.RequireOneOf {
			names[i] = "--" + arg
		}
		required = append(required, "at least one of "+strings.Join(names, ", "))
	}
	if len(required) > 0 {
		line("Requires", "%s", strings.Join(required, "; "))
	} else if v.Batch {
		line("Requires", "nothing besides the issue numbers")
	} else {
		line("Requires", "nothing besides the issue number")
	}
	if len(optional) > 0 {
		line("Optional", "%s", strings.Join(optional, ", "))
	}
	batchHelp(v, line)
	if v.Confirm {
		line("Confirm", "the user's decision: ask the user first; runs only with --yes")
	}

	if v.OnlyClearsSprint() {
		line("Board", "leaves an issue that is not there off the board")
	} else {
		line("Board", "adds the issue if it is not there")
	}
	if v.Reopen {
		line("Reopen", "reopens a closed issue")
	}
	sprintHelp(cfg, v, line)
	if len(v.AddLabels)+len(v.RemoveLabels) > 0 {
		var signs []string
		for _, l := range v.AddLabels {
			signs = append(signs, "+"+l)
		}
		for _, l := range v.RemoveLabels {
			signs = append(signs, "-"+l)
		}
		line("Labels", "%s (+ adds, - removes)", strings.Join(signs, " "))
	}
	switch v.Assign {
	case "":
	case config.Me:
		line("Assign", "you (the authenticated user); other assignees stay")
	default:
		line("Assign", "%s; other assignees stay", v.Assign)
	}
	switch v.Blockers {
	case config.BlockersWarn:
		line("Blockers", "warns about unresolved blockers (%s), then runs", unresolvedMeaning(cfg))
	case config.BlockersDeny:
		line("Blockers", "refused (exit code 2) while a blocker is unresolved (%s): ask the user", unresolvedMeaning(cfg))
	case config.BlockersLink:
		line("Blockers", "--by naming issues (12, #12, owner/repo#12 or URLs, comma-separated) also marks this one "+
			"blocked by them; a pull request is only named in the comment")
	}
	if v.Close != "" {
		line("Close", "closes the issue as %s; an issue already closed stays as it is", workflow.ReasonText(v.Close))
	}
	if v.Comment != "" {
		line("Comment", "posted unless it renders empty or is already the latest gh-dojo comment:")
		// The template as it will read in the issue, indented below the
		// label; blank lines stay blank, without trailing spaces.
		for _, l := range strings.Split(strings.Trim(v.Comment, "\r\n"), "\n") {
			if l = strings.TrimRight(l, " \t\r"); l == "" {
				b.WriteString("\n")
			} else {
				line("", "  %s", l)
			}
		}
	}
	return b.String()
}

// unresolvedMeaning says when a blocker is unresolved under cfg's
// statuses.resolved: "open and not in Ready or Done".
func unresolvedMeaning(cfg *config.Config) string {
	r := cfg.Statuses.Resolved
	switch len(r) {
	case 0:
		return "open"
	case 1:
		return "open and not in " + r[0]
	}
	return "open and not in " + strings.Join(r[:len(r)-1], ", ") + " or " + r[len(r)-1]
}

// configNote is appended to the root help when there is no config or it
// cannot be loaded.
func configNote() string {
	_, err := loadConfig()
	if err == nil {
		return ""
	}
	if errors.Is(err, config.ErrNoConfig) {
		return fmt.Sprintf("\n\nNo process verbs: no %s found (create one: gh dojo init; format: %s).", config.FileName, config.DocsURL)
	}
	text := strings.ReplaceAll(render.FromError(err).Text(), "\n", "\n  ")
	return "\n\nNo process verbs: " + config.FileName + " could not be loaded:\n  " + text
}

// processHelp is the lifecycle section of the root help: the statuses each
// verb moves an issue from and to, in config order, then the initial,
// final and resolved statuses. A verb that closes or reopens the issue, or
// asks the user first, says so after its target. It is empty when the
// config cannot be loaded (see configNote).
//
//	Lifecycle (from → to):
//	  start       Backlog, Waiting, Ready → In progress
//	  done        Ready, In progress → Done; closes as completed
//	  Initial:    Backlog (new issues; also an issue with no status or not on the board)
//	  Final:      Done (gh dojo list hides it unless --all)
//	  Resolved:   Ready, Done (a blocker in these no longer blocks)
func processHelp() string {
	cfg, err := loadConfig()
	if err != nil {
		return ""
	}
	width := 11 // cobra's minimum, so the names line up with the command list
	for _, v := range cfg.Verbs {
		width = max(width, len(v.Name))
	}
	var b strings.Builder
	line := func(name, format string, a ...any) {
		fmt.Fprintf(&b, "\n  %-*s %s", width, name, fmt.Sprintf(format, a...))
	}
	for _, v := range cfg.Verbs {
		from, to := "any status", "status unchanged"
		if len(v.From) > 0 {
			from = strings.Join(v.From, ", ")
		}
		if v.To != "" {
			to = v.To
		}
		var also []string
		switch {
		case v.Reopen:
			also = append(also, "reopens")
		case v.Close != "":
			also = append(also, "closes as "+workflow.ReasonText(v.Close))
		}
		if v.Confirm {
			also = append(also, "asks the user first (--yes)")
		}
		if len(also) > 0 {
			to += "; " + strings.Join(also, ", ")
		}
		line(v.Name, "%s → %s", from, to)
	}
	if s := cfg.Statuses.Initial; s != "" {
		line("Initial:", "%s (new issues; also an issue with no status or not on the board)", s)
	}
	if s := cfg.Statuses.Final; len(s) > 0 {
		line("Final:", "%s (gh dojo list hides it unless --all)", strings.Join(s, ", "))
	}
	if s := cfg.Statuses.Resolved; len(s) > 0 {
		line("Resolved:", "%s (a blocker in these no longer blocks)", strings.Join(s, ", "))
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n\nLifecycle (from → to):" + b.String()
}

// verbsUnavailable says in one line why no verbs are loaded, or returns ""
// when the config loads. The root help has the full error.
func verbsUnavailable() string {
	_, err := loadConfig()
	switch {
	case err == nil:
		return ""
	case errors.Is(err, config.ErrNoConfig):
		return "no " + config.FileName + " found"
	}
	what, _, _ := strings.Cut(render.FromError(err).What, "\n")
	return what
}
