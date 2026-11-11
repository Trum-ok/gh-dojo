package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

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
	var (
		brief    bool
		full     bool
		comments string
	)
	cmd := &cobra.Command{
		Use:   "show <N>...",
		Short: showWhen,
		Long: showWhen + `.

Prints per issue: state, board status and fields, labels, assignees,
blockers both ways (open first), status history on the board, the
description and the latest comments. Times are UTC.

Folding, always with a note saying what is hidden:
  - the last ` + strconv.Itoa(defaultShownComments) + ` comments are printed; --comments N prints the last N,
    --comments all up to ` + strconv.Itoa(issue.MaxComments) + ` (the most gh dojo reads);
  - a description over ` + strconv.Itoa(maxBodyRunes) + ` characters or a comment over ` + strconv.Itoa(maxCommentRunes) + `
    is cut; --full prints them whole.
--brief prints only state, status, labels, assignees, blockers and counts.

Several numbers are read in one request (up to ` + strconv.Itoa(issue.MaxBatch) + ` issues per request).
A missing issue or a pull request number is reported after the others are
printed, with exit code 3.`,
		Example: "  gh dojo show 42\n  gh dojo show 42 43 57 --brief\n  gh dojo show 42 --comments all",
		Args:    showArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			shown, err := parseCommentsFlag(comments)
			if err != nil {
				return err.WithFix(cmd.CommandPath() + " --help")
			}
			var ns []int
			for _, a := range args {
				n, _ := parseIssue(a) // validated by showArgs
				if !slices.Contains(ns, n) {
					ns = append(ns, n)
				}
			}
			app := newApp()
			cfg, err2 := app.Config()
			if err2 != nil {
				return err2
			}
			// The window read is at least the one verbs read, so show and
			// verbs send the same request for the default flags.
			window := min(max(shown, issue.DefaultComments), issue.MaxComments)
			res, readErr := app.Cards(cmd.Context(), ns, window)
			if readErr != nil && len(res) == 0 {
				return readErr
			}
			v := cardView{
				statusField: cfg.StatusField(), comments: shown,
			}
			if !full {
				v.bodyRunes, v.commentRunes = maxBodyRunes, maxCommentRunes
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
			return showError(ns[len(res):], failed, readErr)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&brief, "brief", false, "three lines per issue: no description, history or comments")
	f.StringVar(&comments, "comments", strconv.Itoa(defaultShownComments), "how many of the latest comments to print: a number or all")
	f.BoolVar(&full, "full", false, "print long descriptions and comments whole")
	return cmd, nil
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

// parseCommentsFlag parses --comments: a count from 0 to issue.MaxComments,
// or "all" for issue.MaxComments.
func parseCommentsFlag(s string) (int, *render.Error) {
	if s == "all" {
		return issue.MaxComments, nil
	}
	n, err := strconv.Atoi(s)
	switch {
	case err != nil || n < 0:
		return 0, render.Usage("invalid --comments %q: want a number or all", s)
	case n > issue.MaxComments:
		return 0, render.Usage("--comments %d is more than gh dojo reads (%d); use --comments all", n, issue.MaxComments)
	}
	return n, nil
}

// showError reports what show could not print, after the cards it could:
// issues that failed alone (missing, a pull request) and issues left
// unread by a failed request. A single failed issue keeps its own error.
func showError(unread []int, failed []issue.Result, readErr error) error {
	if readErr == nil && len(failed) == 0 {
		return nil
	}
	if readErr == nil && len(failed) == 1 {
		return failed[0].Err
	}
	// The first error decides the exit code and the fix: a failed request
	// (exit code 4, say) matters more than a missing issue.
	var lines []string
	var first *render.Error
	if readErr != nil {
		first = render.FromError(readErr)
		refs := make([]string, len(unread))
		for i, n := range unread {
			refs[i] = "#" + strconv.Itoa(n)
		}
		lines = append(lines, strings.Join(refs, ", ")+" not read: "+first.What)
	}
	for _, r := range failed {
		e := render.FromError(r.Err)
		if first == nil {
			first = e
		}
		lines = append(lines, e.What)
	}
	// The error block keeps 3 lines: cut here, saying how many are left.
	if len(lines) > 3 {
		lines = append(lines[:2], fmt.Sprintf("and %d more not shown", len(lines)-2))
	}
	out := *first
	out.What = strings.Join(lines, "\n")
	return &out
}
