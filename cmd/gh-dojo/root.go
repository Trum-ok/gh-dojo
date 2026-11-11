package main

import "github.com/spf13/cobra"

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
		Annotations:       map[string]string{cobra.CommandDisplayNameAnnotation: "gh dojo"},
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	for _, newCmds := range constructors {
		root.AddCommand(newCmds()...)
	}
	return root
}
