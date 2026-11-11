package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
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
// schema ID; after a partial failure the agent re-runs the command.
type Backend interface {
	// ReadIssue returns the state of issue n in the default repo, filled as
	// workflow.Issue documents (comments with viewerDidAuthor), with a
	// render error on failure (render.NotFound for a missing issue). The
	// default repo comes from the app wiring: board.repos[0], or the git
	// remote of the current directory.
	ReadIssue(ctx context.Context, n int) (workflow.Issue, error)
	// Apply applies every op of a non-empty plan, finding the issue's node
	// IDs in p.Issue.Ref. On success the command prints p.Confirm(); a
	// partial failure is a render.Partial error that says what was done.
	// Errors keep their cause, so board.IsStale tells a rejected schema ID.
	Apply(ctx context.Context, p *workflow.Plan) error
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
	if err := apply(ctx, be, plan); err != nil {
		if !board.IsStale(err) {
			return err
		}
		// A board ID from the schema cache went stale, and part of the plan
		// may have been applied. Drop the cache, re-read, re-plan and apply
		// what is left, once. A transport failure is never retried here:
		// its outcome is unknown and the agent re-runs the command.
		if rerr := be.Reset(); rerr != nil {
			return err
		}
		fresh, perr := readPlan(ctx, be, engine, v, n, args)
		if perr != nil {
			return perr
		}
		if err := apply(ctx, be, fresh); err != nil {
			return err
		}
	}
	// The first plan's line even after a retry: it starts from the state
	// this command found, and the retry only finished it.
	fmt.Fprintln(cmd.OutOrStdout(), plan.Confirm())
	return nil
}

// readPlan reads issue n and plans verb v on it.
func readPlan(ctx context.Context, be Backend, e *workflow.Engine, v config.Verb, n int, args workflow.Args) (*workflow.Plan, error) {
	is, err := be.ReadIssue(ctx, n)
	if err != nil {
		return nil, err
	}
	return e.Plan(v, is, args)
}

// apply applies a plan with something to do.
func apply(ctx context.Context, be Backend, p *workflow.Plan) error {
	if p.Empty() {
		return nil
	}
	return be.Apply(ctx, p)
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
