package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

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
//   - a command name that is neither built-in nor close to one (a verb the
//     user expected) fails with the config error, not "unknown command" (see
//     verbsUnavailable);
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
// talks to GitHub.
type Backend interface {
	// ReadIssue returns the state of issue n in the board's default repo,
	// filled as workflow.Issue documents, with a render error on failure
	// (render.NotFound for a missing issue).
	ReadIssue(ctx context.Context, n int) (workflow.Issue, error)
	// Apply applies every op of a non-empty plan. On success the command
	// prints p.Confirm(); a partial failure is a render.Partial error that
	// says what was done.
	Apply(ctx context.Context, p *workflow.Plan) error
}

// newBackend returns the backend for cfg. Tests replace it.
var newBackend = func(cfg *config.Config) (Backend, error) { return pendingBackend{}, nil }

// pendingBackend stands in until the GitHub reader and executor land
// (issue #12).
type pendingBackend struct{}

func (pendingBackend) ReadIssue(context.Context, int) (workflow.Issue, error) {
	return workflow.Issue{}, errNotImplemented()
}

func (pendingBackend) Apply(context.Context, *workflow.Plan) error { return errNotImplemented() }

func errNotImplemented() error {
	return render.Usage("verbs cannot reach GitHub yet: not implemented in this build")
}

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
	issue, err := be.ReadIssue(ctx, n)
	if err != nil {
		return err
	}
	engine := workflow.New(cfg)
	engine.Getenv = getenv
	plan, err := engine.Plan(v, issue, args)
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
	if !plan.Empty() {
		if err := be.Apply(ctx, plan); err != nil {
			return err
		}
	}
	fmt.Fprintln(cmd.OutOrStdout(), plan.Confirm())
	return nil
}

var issueRE = regexp.MustCompile(`^#?[1-9][0-9]*$`)

// parseIssue parses an issue number: "42" or "#42".
func parseIssue(s string) (int, bool) {
	if !issueRE.MatchString(s) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, "#"))
	return n, err == nil
}

// issueArg accepts exactly one issue number.
func issueArg(cmd *cobra.Command, args []string) error {
	help := cmd.CommandPath() + " --help"
	switch {
	case len(args) == 0:
		return render.Usage("%s needs an issue number, like 42 or #42", cmd.Name()).WithFix(help)
	case len(args) > 1:
		return render.Usage("%s takes one issue number, got %d; several at once are not supported yet",
			cmd.Name(), len(args)).WithFix(help)
	}
	if _, ok := parseIssue(args[0]); !ok {
		return render.Usage("%q is not an issue number; pass one like 42 or #42", args[0]).WithFix(help)
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
		line("", "skipped when it renders empty or repeats the last %s comment", v.Name)
	}
	return b.String()
}

// configNote is appended to the root help when the config cannot be loaded.
func configNote() string {
	_, err := loadConfig()
	if err == nil {
		return ""
	}
	text := strings.ReplaceAll(render.FromError(err).Text(), "\n", "\n  ")
	return "\n\nNo process verbs: " + config.FileName + " could not be loaded.\n  " + text
}

// verbsUnavailable returns the config error for an unknown command name,
// which may be a verb, or nil when the config loads.
func verbsUnavailable(name string) error {
	_, err := loadConfig()
	if err == nil {
		return nil
	}
	e := *render.FromError(err)
	e.What = fmt.Sprintf("unknown command %q; verbs are unavailable: %s", name, e.What)
	return &e
}
