package main

import (
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/spf13/cobra"
)

// Adding a command: create a new file in this package that defines a
// constructor and registers it from init, without touching this file:
//
//	func init() { register(newShowCmd) }
//
//	func newShowCmd() *cobra.Command {
//		return &cobra.Command{
//			Use:   "show <number>...",
//			Short: "Show issues with status, sprint, blockers and comments",
//			Args:  cobra.MinimumNArgs(1),
//			RunE:  func(cmd *cobra.Command, args []string) error { ... },
//		}
//	}
//
// A file that produces several commands at once, such as the verbs built
// from the config, uses registerAll with a func() []*cobra.Command.
//
// Commands return errors built with internal/render (render.Usage,
// render.NotFound, ...) so the agent gets the right exit code and a short
// message; Args validation failures become usage errors automatically.
// Write results to cmd.OutOrStdout() and warnings to cmd.ErrOrStderr().

var constructors []func() []*cobra.Command

// register adds a constructor of one top-level command.
func register(newCmd func() *cobra.Command) {
	registerAll(func() []*cobra.Command { return []*cobra.Command{newCmd()} })
}

// registerAll adds a constructor of several top-level commands.
func registerAll(newCmds func() []*cobra.Command) {
	constructors = append(constructors, newCmds)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "gh-dojo",
		Short: "Process verbs for GitHub Projects, built for AI agents",
		Long: "gh dojo moves issues through a GitHub Projects board with process verbs\n" +
			"defined in .github/dojo.toml, so agents never deal with field or option IDs.",
		Annotations:                map[string]string{cobra.CommandDisplayNameAnnotation: "gh dojo"},
		SilenceUsage:               true,
		SilenceErrors:              true,
		CompletionOptions:          cobra.CompletionOptions{DisableDefaultCmd: true},
		Args:                       unknownCommand,
		SuggestionsMinimumDistance: 2,
		RunE:                       func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return render.Usage("%s", err).WithFix(cmd.CommandPath() + " --help")
	})
	for _, newCmds := range constructors {
		root.AddCommand(newCmds()...)
	}
	for _, c := range root.Commands() {
		wrapArgs(c)
	}
	return root
}

// unknownCommand rejects arguments to the root command, listing every
// available command and suggesting close matches.
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
	e := render.Usage("unknown command %q", args[0])
	if s := cmd.SuggestionsFor(args[0]); len(s) > 0 {
		e = render.Usage("unknown command %q, did you mean %q?", args[0], s[0])
	}
	return e.WithAllowed(allowed...).WithFix(cmd.CommandPath() + " --help")
}

// wrapArgs turns plain Args validation errors of c and its subcommands into
// usage errors pointing at the command's help; *render.Error passes through.
func wrapArgs(c *cobra.Command) {
	if validate := c.Args; validate != nil {
		c.Args = func(cmd *cobra.Command, args []string) error {
			err := validate(cmd, args)
			if _, ok := err.(*render.Error); err == nil || ok {
				return err
			}
			return render.Usage("%s", err).WithFix(cmd.CommandPath() + " --help")
		}
	}
	for _, sub := range c.Commands() {
		wrapArgs(sub)
	}
}
