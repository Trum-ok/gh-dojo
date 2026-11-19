package main

import (
	"errors"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// maxFlagOwners is how many other commands an unknown flag's error names
// before "and N more".
const maxFlagOwners = 4

// flagError turns a flag parsing error of cmd into a usage error pointing
// at its help. An unknown long flag also gets cmd's flags as the allowed
// values, the other commands that take it, and a close flag of cmd: agents
// carry a flag from one command to the next (#63), so the error names
// what to run instead of a second call for --help.
func flagError(cmd *cobra.Command, err error) error {
	help := cmd.CommandPath() + " --help"
	var missing *pflag.NotExistError
	if !errors.As(err, &missing) || missing.GetSpecifiedShortnames() != "" {
		return render.Usage("%s", err).WithFix(help)
	}
	name := missing.GetSpecifiedName()
	msg := err.Error()
	if owners := flagOwners(cmd, name); len(owners) > 0 {
		msg += " (a flag of " + joinCapped(owners, maxFlagOwners) + ")"
	}
	if s := closeFlag(cmd, name); s != "" {
		msg += "; did you mean --" + s + "?"
	}
	return render.Usage("%s", msg).WithAllowedAll(localFlags(cmd)...).WithFix(help)
}

// localFlags lists the flags cmd shows in its help, as --name, without
// --help and the output flags every command takes.
func localFlags(cmd *cobra.Command) []string {
	var names []string
	cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if !f.Hidden && f.Name != "help" {
			names = append(names, "--"+f.Name)
		}
	})
	return names
}

// flagOwners lists the other commands that show flag name in their help,
// by their path under the root ("new", "agent install").
func flagOwners(cmd *cobra.Command, name string) []string {
	root := cmd.Root()
	var owners []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if !sub.IsAvailableCommand() {
				continue
			}
			if sub != cmd {
				if f := sub.Flag(name); f != nil && !f.Hidden {
					owners = append(owners, strings.TrimPrefix(sub.CommandPath(), root.CommandPath()+" "))
				}
			}
			walk(sub)
		}
	}
	walk(root)
	return owners
}

// closeFlag returns the flag of cmd that name most likely meant: a typo
// within edit distance 2 (--dryrun), else one with the same last word
// (--blocked-by for --by); "" when none, or when two are equally close.
func closeFlag(cmd *cobra.Command, name string) string {
	lower := strings.ToLower(name)
	best, bestRank, ties := "", 4, 0
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		rank := 4 // no match
		// At most half the name may differ, or every short name is close
		// to every other (--x to --by); --josn is still --json.
		if d := render.EditDistance(lower, f.Name); d <= 2 && 2*d <= len(lower) {
			rank = d
		} else if lastWord(lower) == lastWord(f.Name) {
			rank = 3
		}
		switch {
		case rank < bestRank:
			best, bestRank, ties = f.Name, rank, 0
		case rank == bestRank && rank < 4:
			ties++
		}
	})
	if ties > 0 {
		return ""
	}
	return best
}

// lastWord is the part of a flag name after its last hyphen.
func lastWord(name string) string { return name[strings.LastIndexByte(name, '-')+1:] }

// joinCapped joins names with commas, the first limit of them, then
// "and N more"; never "and 1 more", which takes the place of the name.
func joinCapped(names []string, limit int) string {
	if len(names) <= limit+1 {
		return render.JoinWords(names, "and")
	}
	return strings.Join(names[:limit], ", ") + " and " + strconv.Itoa(len(names)-limit) + " more"
}
