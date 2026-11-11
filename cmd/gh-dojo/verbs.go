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
	// ReadIssue returns the state of issue n in the default repo, filled as
	// workflow.Issue documents (comments with viewerDidAuthor), with a
	// render error on failure (render.NotFound for a missing issue). The
	// default repo comes from the app wiring: board.repos[0], or the git
	// remote of the current directory.
	ReadIssue(ctx context.Context, n int) (workflow.Issue, error)
	// Apply applies every op of a non-empty plan, finding the issue's node
	// IDs in p.Issue.Ref. It returns the ops GitHub confirmed, also with an
	// error: the rest of p.Ops did not take effect, or their outcome is
	// unknown. An error with every op confirmed is only worth a warning.
	// runVerb reports a partial write (exit code 5); errors keep their
	// cause, so board.IsStale tells a rejected schema ID.
	Apply(ctx context.Context, p *workflow.Plan) ([]workflow.Op, error)
	// Reset drops the cached board schema after GitHub rejected one of its
	// IDs, so the next ReadIssue and Apply use a fresh one.
	Reset() error
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
	cmd := &cobra.Command{
		GroupID: verbsGroup.ID,
		Use:     v.Name + " <N>",
		Short:   v.When,
		Long:    verbHelp(cfg, v),
		Args:    issueArg,
		RunE: func(cmd *cobra.Command, posArgs []string) error {
			n, _ := parseIssue(posArgs[0]) // validated by issueArg
			args.HasComment = cmd.Flags().Changed(config.ArgComment)
			args.HasBy = cmd.Flags().Changed(config.ArgBy)
			return runVerb(cmd, cfg, v, n, args, dryRun)
		},
	}
	f := cmd.Flags()
	f.StringVar(&args.Comment, config.ArgComment, "", "text for {comment} in the comment")
	f.StringVar(&args.By, config.ArgBy, "", "value for {by} in the comment: an issue number (12 or #12) or free text")
	f.BoolVar(&dryRun, "dry-run", false, "print the plan without changing anything")
	// Flags the comment template does not use stay registered but hidden, so
	// passing one gets workflow's precise error instead of "unknown flag".
	for _, arg := range config.Args() {
		if !v.Uses(arg) {
			_ = f.MarkHidden(arg)
		}
	}
	return cmd
}

func runVerb(cmd *cobra.Command, cfg *config.Config, v config.Verb, n int, args workflow.Args, dryRun bool) error {
	if err := workflow.CheckArgs(v, n, args); err != nil {
		return err
	}
	be, err := newBackend(cfg)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	engine := workflow.New(cfg)
	engine.Getenv = getenv
	plan, err := readPlan(ctx, be, engine, v, n, args)
	if err != nil {
		return err
	}
	for _, w := range plan.Warnings {
		render.Warn(cmd.ErrOrStderr(), "%s", w)
	}
	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), plan.DryRun())
		return nil
	}
	run := &verbRun{first: plan, stderr: cmd.ErrOrStderr()}
	if err := run.apply(ctx, be, plan); err != nil {
		if !board.IsStale(err) {
			return run.partial(missing(plan.Ops, run.done), err, err)
		}
		// A board ID from the schema cache went stale, and part of the plan
		// may have been applied. Drop the cache, re-read, re-plan and apply
		// what is left, once. A transport failure is never retried here:
		// its outcome is unknown and the agent re-runs the command.
		if rerr := be.Reset(); rerr != nil {
			return run.partial(missing(plan.Ops, run.done), err, err)
		}
		fresh, perr := readPlan(ctx, be, engine, v, n, args)
		if perr != nil {
			return run.partial(missing(plan.Ops, run.done), err, perr)
		}
		// What the first attempt confirmed is not sent again, even if the
		// re-read does not show it yet.
		fresh.Ops = missing(fresh.Ops, run.done)
		run.fresh = fresh
		if err := run.apply(ctx, be, fresh); err != nil {
			return run.partial(missing(fresh.Ops, run.done), err, err)
		}
	}
	fmt.Fprintln(cmd.OutOrStdout(), run.applied().Confirm())
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
// retry, a status or comment the re-read found in place counts as such.
func (r *verbRun) applied() *workflow.Plan {
	shown := *r.first
	shown.Ops = r.done
	if r.fresh != nil {
		shown.StatusUnchanged = r.first.StatusUnchanged ||
			r.fresh.StatusUnchanged && !hasKind(r.done, workflow.SetStatus{})
		shown.CommentPresent = r.first.CommentPresent ||
			r.fresh.CommentPresent && !hasKind(r.done, workflow.AddComment{})
	}
	return &shown
}

// partial reports a failed write. With nothing confirmed, err itself is
// the answer. Otherwise it is exit code 5, naming what was done and what
// was not:
//
//	#42 comment left; status not changed: GitHub token is missing a required scope (project); gh auth refresh -s project, then re-run the same command
//
// The re-run re-reads the issue, so its plan leaves out what is done. cause
// is the error of the write that left missed undone: per-field GraphQL
// errors or ops never sent (errNotSent) mean they did not happen, a lost
// answer (timeout, 5xx) that they are unconfirmed. err is what stopped the command, and gives the reason.
func (r *verbRun) partial(missed []workflow.Op, cause, err error) error {
	if len(r.done) == 0 {
		return err
	}
	var gqlErr *api.GraphQLError
	what := "the rest is unconfirmed"
	if (errors.As(cause, &gqlErr) || errors.Is(cause, errNotSent)) && len(missed) > 0 {
		words := make([]string, len(missed))
		for i, op := range missed {
			words[i] = effects.Missed(op)
		}
		what = strings.Join(words, ", ")
	}
	e := render.FromError(err)
	next := "re-run the same command"
	if fix := strings.TrimSpace(e.Fix); fix != "" {
		next = fix + ", then " + next
	}
	return render.Partial("%s; %s: %s; %s", r.applied().Confirm(), what, reason(err), next).
		WithFix(e.Fix).WithCause(err)
}

// reason is the first clause of an error's message, for a partial report.
func reason(err error) string {
	what, _, _ := strings.Cut(render.FromError(err).What, "\n")
	what, _, _ = strings.Cut(what, ";")
	return strings.TrimSpace(what)
}

// missing returns the ops whose kind is not among done.
func missing(ops, done []workflow.Op) []workflow.Op {
	var out []workflow.Op
	for _, op := range ops {
		if !hasKind(done, op) {
			out = append(out, op)
		}
	}
	return out
}

// hasKind reports whether ops has an op of the same kind as op (a plan has
// at most one of each).
func hasKind(ops []workflow.Op, op workflow.Op) bool {
	return slices.ContainsFunc(ops, func(o workflow.Op) bool { return reflect.TypeOf(o) == reflect.TypeOf(op) })
}

// readPlan reads issue n and plans verb v on it.
func readPlan(ctx context.Context, be Backend, e *workflow.Engine, v config.Verb, n int, args workflow.Args) (*workflow.Plan, error) {
	is, err := be.ReadIssue(ctx, n)
	if err != nil {
		return nil, err
	}
	return e.Plan(v, is, args)
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
	} else {
		line("Requires", "nothing besides the issue number")
	}
	if len(optional) > 0 {
		line("Optional", "%s", strings.Join(optional, ", "))
	}

	line("Board", "adds the issue if it is not there")
	if v.Comment != "" {
		line("Comment", "%s", strconv.Quote(v.Comment))
		line("", "skipped when it renders empty or is already the latest gh-dojo comment")
	}
	return b.String()
}

// configNote is appended to the root help when there is no config or it
// cannot be loaded.
func configNote() string {
	_, err := loadConfig()
	if err == nil {
		return ""
	}
	if errors.Is(err, config.ErrNoConfig) {
		return fmt.Sprintf("\n\nNo process verbs: no %s found (create one: see %s).", config.FileName, config.DocsURL)
	}
	text := strings.ReplaceAll(render.FromError(err).Text(), "\n", "\n  ")
	return "\n\nNo process verbs: " + config.FileName + " could not be loaded:\n  " + text
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
