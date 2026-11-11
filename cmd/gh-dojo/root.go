package main

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/spf13/cobra"
)

// Adding a command: create a new file in this package that defines a
// constructor and registers it from init, without touching this file:
//
//	func init() { register(newShowCmd) }
//
//	func newShowCmd() (*cobra.Command, error) {
//		return &cobra.Command{
//			Use:   "show <number>...",
//			Short: "Show issues with status, sprint, blockers and comments",
//			Args:  cobra.MinimumNArgs(1),
//			RunE:  func(cmd *cobra.Command, args []string) error { ... },
//		}, nil
//	}
//
// A file that produces several commands at once, such as the verbs built
// from the config, uses registerAll with a func() ([]*cobra.Command, error).
//
// A constructor error stops gh dojo before any command runs and is printed
// like a command error: return render errors. Do not fail on a missing or
// broken dojo.toml there, or --help breaks too: verbs.go explains how config
// errors are shown instead. Two commands with the same name or alias,
// such as a config verb named like a built-in command, are an error too.
//
// Commands return errors built with internal/render (render.Usage,
// render.NotFound, ...) so the agent gets the right exit code and a short
// message; Args validation failures become usage errors automatically. A
// command with subcommands needs no RunE: it prints its help, and an unknown
// subcommand is a usage error. Print results with emit (output.go), which
// writes text, or JSON with --json, from the same typed result; write
// warnings to cmd.ErrOrStderr().

// constructor builds one or more top-level commands.
type constructor func() ([]*cobra.Command, error)

var constructors []constructor

// register adds a constructor of one top-level command.
func register(newCmd func() (*cobra.Command, error)) {
	registerAll(func() ([]*cobra.Command, error) {
		c, err := newCmd()
		if err != nil {
			return nil, err
		}
		return []*cobra.Command{c}, nil
	})
}

// registerAll adds a constructor of several top-level commands.
func registerAll(newCmds func() ([]*cobra.Command, error)) {
	constructors = append(constructors, newCmds)
}

// newRootCmd builds the root command with every registered command.
func newRootCmd() (*cobra.Command, error) { return buildRoot(constructors) }

func buildRoot(ctors []constructor) (*cobra.Command, error) {
	root := &cobra.Command{
		Use:   "gh-dojo",
		Short: "Process verbs for GitHub Projects, built for AI agents",
		Long: "gh dojo moves issues through a GitHub Projects board with process verbs\n" +
			"defined in .github/dojo.toml, so agents never deal with field or option IDs." +
			configNote(),
		Version: version,
		Annotations: map[string]string{
			cobra.CommandDisplayNameAnnotation: "gh dojo",
			rootSectionsAnnotation:             processHelp() + exitCodesHelp(),
		},
		SilenceUsage:               true,
		SilenceErrors:              true,
		CompletionOptions:          cobra.CompletionOptions{DisableDefaultCmd: true},
		Args:                       unknownCommand,
		SuggestionsMinimumDistance: 2,
		RunE:                       func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetUsageTemplate(rootUsageTemplate(root.UsageTemplate()))
	addOutputFlags(root)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		// `gh dojo start 42 --dry-run` without a loadable config fails on the
		// verb's flag before the root's Args see "start".
		if args := cmd.Flags().Args(); !cmd.HasParent() && len(args) > 0 {
			return unknownCommand(cmd, args)
		}
		return render.Usage("%s", err).WithFix(cmd.CommandPath() + " --help")
	})
	// Added explicitly: cobra adds its help command only to a root that
	// already has subcommands, so `gh dojo help` would fail without verbs.
	help := newHelpCmd()
	root.SetHelpCommand(help)
	root.AddCommand(help)

	// Names taken so far, by the built-in help command and earlier commands.
	taken := map[string]bool{help.Name(): true}
	for _, newCmds := range ctors {
		cmds, err := newCmds()
		if err != nil {
			return nil, err
		}
		for _, c := range cmds {
			if c == nil {
				return nil, render.Internal("a command constructor returned nil")
			}
			for _, name := range append([]string{c.Name()}, c.Aliases...) {
				if taken[name] {
					// No fix line: every command, --help included, fails until
					// the name is changed.
					return nil, render.Usage("command name %q is used twice; "+
						"a verb in .github/dojo.toml cannot reuse a command name", name)
				}
				taken[name] = true
			}
			if c.GroupID == verbsGroup.ID && !root.ContainsGroup(c.GroupID) {
				root.AddGroup(verbsGroup)
			}
			root.AddCommand(c)
		}
	}
	prepare(root)
	return root, nil
}

// rootSectionsAnnotation holds the root help sections that follow the
// command list: the process (see processHelp) and the exit codes.
const rootSectionsAnnotation = "gh-dojo:sections"

// rootUsageTemplate adds the root help sections to cobra's usage template,
// right before the flags. Subcommands inherit the template, so it prints
// them only for the root.
func rootUsageTemplate(tmpl string) string {
	const flags = "{{if .HasAvailableLocalFlags}}"
	sections := `{{if not .HasParent}}{{index .Annotations "` + rootSectionsAnnotation + `"}}{{end}}`
	return strings.Replace(tmpl, flags, sections+flags, 1)
}

// exitCodesHelp lists the exit codes with what to do next, for the root
// help.
func exitCodesHelp() string {
	var b strings.Builder
	b.WriteString("\n\nExit codes:")
	for _, c := range render.ExitCodes {
		fmt.Fprintf(&b, "\n  %d  %s: %s", c.Code, c.Meaning, c.Next)
	}
	if cfg, err := loadConfig(); err == nil && slices.ContainsFunc(cfg.Verbs, func(v config.Verb) bool { return v.Batch }) {
		b.WriteString("\n  A verb on several issues (batch) exits 0 when all are done, 5 when some were\n" +
			"  written and a re-run may finish the rest, else with the code of the failed\n" +
			"  ones (2 first, then 1, 4, 3); a summary names each issue not done.")
	}
	return b.String()
}

// newHelpCmd replaces cobra's help command, which prints the root help and
// exits 0 for an unknown topic; this one fails with a usage error.
func newHelpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		RunE: func(c *cobra.Command, args []string) error {
			cmd, rest, err := c.Root().Find(args)
			if err != nil {
				return err
			}
			if len(rest) > 0 {
				return unknownCommand(cmd, rest)
			}
			cmd.InitDefaultHelpFlag()
			cmd.InitDefaultVersionFlag()
			return cmd.Help()
		},
	}
}

// unknownCommand rejects arguments to a command that only groups others,
// listing its available subcommands and suggesting close matches. At the
// root, a name not close to a built-in command may be a verb, so the error
// also says why no verbs are loaded, if so (see verbs.go).
func unknownCommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	var allowed []string
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() {
			allowed = append(allowed, c.Name())
		}
	}
	what := "unknown command " + strconv.Quote(args[0])
	if cmd.HasParent() {
		what += " for " + strconv.Quote(cmd.CommandPath())
	}
	if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
		what += ", did you mean " + strconv.Quote(s[0]) + "?"
	} else if why := verbsUnavailable(); why != "" && !cmd.HasParent() {
		what += "; verbs unavailable: " + why
	}
	return render.Usage("%s", what).WithAllowed(allowed...).WithFix(cmd.CommandPath() + " --help")
}

// prepare walks c and its subcommands. Plain Args validation errors become
// usage errors pointing at the command's help; *render.Error passes through.
// A command with subcommands rejects unknown ones unless it validates its own
// args, and prints its help when it has no RunE: cobra would otherwise print
// the help and exit 0 on a mistyped subcommand.
func prepare(c *cobra.Command) {
	if c.HasSubCommands() {
		if c.Args == nil {
			c.Args = unknownCommand
		}
		if c.SuggestionsMinimumDistance <= 0 {
			c.SuggestionsMinimumDistance = 2 // cobra's default, applied only in its own errors
		}
		if !c.Runnable() {
			c.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
		}
	}
	if validate := c.Args; validate != nil {
		c.Args = func(cmd *cobra.Command, args []string) error {
			err := validate(cmd, args)
			var e *render.Error
			if err == nil || errors.As(err, &e) {
				return err
			}
			return render.Usage("%s", err).WithFix(cmd.CommandPath() + " --help")
		}
	}
	for _, sub := range c.Commands() {
		prepare(sub)
	}
}
