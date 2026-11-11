package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/spf13/cobra"
)

func init() { register(newShowCmd) }

// defaultShownComments is how many of the latest comments show prints
// without --comments.
const defaultShownComments = 5

const showWhen = "Read issues before working on them: board status, blockers, history, description, comments"

func newShowCmd() (*cobra.Command, error) {
	var brief, full bool
	comments := commentsFlag(defaultShownComments)
	cmd := &cobra.Command{
		Use:   "show <N>...",
		Short: showWhen,
		Long: showWhen + `.

Prints per issue: state, board status and fields, milestone, labels,
assignees, blockers both ways with their board status, status history on
the board, the description and the latest comments, indented. Times are
UTC.

A blocker is resolved, no longer blocking, when it is closed or its board
status is in statuses.resolved` + resolvedHelp() + `.
Unresolved blockers are listed first; a blocker whose board status could
not be read counts as unresolved.

Folding, always with a note saying what is hidden:
  - the last ` + strconv.Itoa(defaultShownComments) + ` comments are printed; --comments N prints the last N,
    --comments all up to ` + strconv.Itoa(issue.MaxComments) + ` (the most gh dojo reads);
  - a description is cut at ` + strconv.Itoa(maxBodyRunes) + ` characters, and the comments shown share
    ` + strconv.Itoa(commentBudgetRunes) + ` (` + strconv.Itoa(minCommentRunes) + ` to ` + strconv.Itoa(maxCommentRunes) + ` each); --full prints them whole.
--brief prints three lines per issue (four when GitHub hid part of it):
title; state, status, milestone, labels, assignees; blockers and counts.
It takes neither --comments nor --full.

Several numbers are read in one request (up to ` + strconv.Itoa(issue.MaxBatch) + ` issues per request).
A missing issue or a pull request number is reported after the others are
printed, with exit code 3.`,
		Example: "  gh dojo show 42\n  gh dojo show 42 43 57 --brief\n  gh dojo show 42 --comments all",
		Args:    showArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if brief {
				for _, name := range []string{"comments", "full"} {
					if cmd.Flags().Changed(name) {
						return render.Usage("--brief prints no description or comments; drop --%s", name).
							WithFix(cmd.CommandPath() + " --help")
					}
				}
			}
			var ns []int
			for _, a := range args {
				n, _ := parseIssue(a) // validated by showArgs
				if !slices.Contains(ns, n) {
					ns = append(ns, n)
				}
			}
			app := newApp()
			cfg, err := app.Config()
			if err != nil {
				return err
			}
			shown := int(comments)
			// The window read is at least the one verbs read, so show and
			// verbs send the same request for the default flags.
			window := min(max(shown, issue.DefaultComments), issue.MaxComments)
			res, readErr := app.Cards(cmd.Context(), ns, window)
			if readErr != nil && len(res) == 0 {
				return readErr
			}
			repo, _ := app.Repo() // Cards resolved it already
			v := cardView{statusField: cfg.StatusField(), resolved: cfg.Statuses.Resolved, comments: shown}
			if !full {
				v.bodyRunes, v.commentBudget = maxBodyRunes, commentBudgetRunes
			}
			w := cmd.OutOrStdout()
			printed := 0
			var failed []issue.Result
			for _, r := range res {
				if r.Err != nil {
					failed = append(failed, r)
					continue
				}
				switch {
				case printed == 0:
				case brief:
					fmt.Fprintln(w)
				default:
					fmt.Fprintln(w, "\n"+cardSeparator)
				}
				if brief {
					fmt.Fprint(w, v.brief(r.Card))
				} else {
					fmt.Fprint(w, v.card(r.Card))
				}
				printed++
			}
			return showError(repo, ns[len(res):], failed, readErr)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&brief, "brief", false, "three lines per issue: no description, history or comments")
	f.Var(&comments, "comments", "how many of the latest comments to print, or all (up to "+strconv.Itoa(issue.MaxComments)+")")
	f.BoolVar(&full, "full", false, "print descriptions and comments without cutting them")
	return cmd, nil
}

// resolvedHelp ends show's sentence on resolved blockers with the
// configured statuses.resolved.
func resolvedHelp() string {
	cfg, err := loadConfig()
	switch {
	case err != nil:
		return " of " + config.FileName
	case len(cfg.Statuses.Resolved) == 0:
		return ", here: none (only closing resolves a blocker)"
	}
	return ", here: " + strings.Join(cfg.Statuses.Resolved, ", ")
}

// cardSeparator separates full cards of several issues.
var cardSeparator = strings.Repeat("=", 40)

// showArgs accepts one or more issue numbers.
func showArgs(cmd *cobra.Command, args []string) error {
	help := cmd.CommandPath() + " --help"
	if len(args) == 0 {
		return render.Usage("%s needs an issue number, like %s", cmd.Name(), issueExamples).WithFix(help)
	}
	for _, a := range args {
		if _, ok := parseIssue(a); !ok {
			return render.Usage("%q is not an issue number; pass numbers like %s", a, issueExamples).WithFix(help)
		}
	}
	return nil
}

// commentsFlag is --comments: a count from 0 to issue.MaxComments, or
// "all" for issue.MaxComments. Its help shows the type as N|all.
type commentsFlag int

func (c *commentsFlag) String() string {
	if int(*c) == issue.MaxComments {
		return "all"
	}
	return strconv.Itoa(int(*c))
}

func (c *commentsFlag) Set(s string) error {
	if s == "all" {
		*c = issue.MaxComments
		return nil
	}
	n, err := strconv.Atoi(s)
	switch {
	case err != nil || n < 0:
		return fmt.Errorf("want a number or all")
	case n > issue.MaxComments:
		return fmt.Errorf("gh dojo reads at most %d; use --comments all", issue.MaxComments)
	}
	*c = commentsFlag(n)
	return nil
}

func (c *commentsFlag) Type() string { return "N|all" }

// showError reports what show could not print, after the cards it could:
// issues left unread by a failed request, then the issues that failed
// alone, grouped by cause ("issues #9, #10 not found in owner/repo"). The
// first line decides the exit code and the fix line; the fix of every
// other cause stays on its own line, unless it is that same fix.
func showError(repo issue.Repo, unread []int, failed []issue.Result, readErr error) error {
	type group struct {
		cause issue.Cause
		ns    []int
		err   *render.Error
	}
	var groups []*group
	for _, r := range failed {
		i := slices.IndexFunc(groups, func(g *group) bool { return r.Cause != "" && g.cause == r.Cause })
		if i < 0 {
			groups = append(groups, &group{cause: r.Cause, err: render.FromError(r.Err)})
			i = len(groups) - 1
		}
		groups[i].ns = append(groups[i].ns, r.Number)
	}
	what := func(g *group) string {
		switch {
		case len(g.ns) == 1:
			return g.err.What
		case g.cause == issue.CauseMissing:
			return fmt.Sprintf("issues %s not found in %s", issueRefs(g.ns), repo)
		case g.cause == issue.CausePullRequest:
			return fmt.Sprintf("%s in %s are pull requests; gh dojo works with issues only", issueRefs(g.ns), repo)
		}
		return g.err.What
	}

	var lines []string
	var first *render.Error
	if readErr != nil {
		first = render.FromError(readErr)
		lines = append(lines, issueRefs(unread)+" not read: "+first.What)
	}
	for _, g := range groups {
		line := what(g)
		switch {
		case first == nil:
			first = g.err
		case g.err.Fix != "" && g.err.Fix != first.Fix:
			line += " (fix: " + g.err.Fix + ")"
		}
		lines = append(lines, line)
	}
	if first == nil {
		return nil
	}
	// The error block keeps 3 lines: cut here, saying how many are left.
	if len(lines) > 3 {
		lines = append(lines[:2], fmt.Sprintf("and %d more problems not shown", len(lines)-2))
	}
	out := *first
	out.What = strings.Join(lines, "\n")
	return &out
}

// maxErrorRefs is how many issue numbers an error line names.
const maxErrorRefs = 10

// issueRefs lists issue numbers as "#9, #10", naming at most maxErrorRefs.
func issueRefs(ns []int) string {
	refs := make([]string, 0, min(len(ns), maxErrorRefs))
	for _, n := range ns[:min(len(ns), maxErrorRefs)] {
		refs = append(refs, "#"+strconv.Itoa(n))
	}
	s := strings.Join(refs, ", ")
	if n := len(ns) - len(refs); n > 0 {
		s += fmt.Sprintf(" and %d more", n)
	}
	return s
}
