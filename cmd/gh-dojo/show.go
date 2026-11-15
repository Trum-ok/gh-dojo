package main

import (
	"fmt"
	"io"
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

Prints per issue: state, board status and fields, the sprint (with
fields.sprint), milestone, labels, assignees, blockers both ways, the
parent issue and the sub-issues (GitHub's sub-issues), each with its board
status, status history on the board, the description and the latest
comments, indented. A line left out means none: no milestone, labels,
assignees, blockers, blocked issues, parent or sub-issues. Times are UTC.

A blocker is resolved, no longer blocking, when it is closed or its board
status is in statuses.resolved` + resolvedHelp() + `.
Unresolved blockers are listed first; a blocker whose board status could
not be read counts as unresolved. Sub-issues follow the same rule and
order, and their header counts the closed and the unresolved ones
("3 of 5 closed, 1 unresolved").

Folding, always with a note saying what is hidden:
  - the last ` + strconv.Itoa(defaultShownComments) + ` comments are printed; --comments N prints the last N,
    --comments all up to ` + strconv.Itoa(issue.MaxComments) + ` (the most gh dojo reads);
  - a description is cut at ` + strconv.Itoa(maxBodyRunes) + ` characters, and the comments shown share
    ` + strconv.Itoa(commentBudgetRunes) + ` (` + strconv.Itoa(minCommentRunes) + ` to ` + strconv.Itoa(maxCommentRunes) + ` each); --full prints them whole;
  - the status history is one line with days; --full prints a change per
    line with its time;
  - a session link in a comment ([session] link) keeps the last ` + strconv.Itoa(sessionTail) + `
    characters of its ID; --full prints it whole.
--brief prints three lines per issue (four when GitHub hid part of it):
title; state, status, sprint, milestone, labels, assignees; blockers, the
parent and sub-issues (when it has them) and counts.
It takes neither --comments nor --full. Several issues are printed so
anyway, unless --comments or --full asks for their whole cards; --json has
whole cards unless --brief.

An issue is 42, "#42" (the default repository, board.repos[0]),
owner/repo#42 or its URL; an issue outside the default repository is
printed as owner/repo#42, and so are its links.

Several issues are read in one request (up to ` + strconv.Itoa(issue.MaxBatch) + ` issues per request),
from any repositories. A missing issue or a pull request number is
reported after the others are printed, with exit code 3.`,
		Example: "  gh dojo show 42\n  gh dojo show 42 43 57\n  gh dojo show 42 43 --full\n  gh dojo show acme/api#7\n  gh dojo show 42 --comments all",
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
			app := newApp()
			cfg, err := app.Config()
			if err != nil {
				return err
			}
			repo, err := app.Repo()
			if err != nil {
				return err
			}
			refs := argRefs(args, repo) // validated by showArgs
			shown := int(comments)
			// The window read is at least the one verbs read, so show and
			// verbs send the same request for the default flags.
			window := min(max(shown, issue.DefaultComments), issue.MaxComments)
			res, readErr := app.Cards(cmd.Context(), refs, window)
			if readErr != nil && len(res) == 0 {
				return readErr
			}
			// Several issues are brief in text unless --comments or --full
			// asks for cards: one is usually taken, read it alone.
			cardsAsked := full || cmd.Flags().Changed("comments")
			briefByDefault := !brief && len(refs) > 1 && !cardsAsked && !jsonMode(cmd)
			textBrief := brief || briefByDefault
			v := cardView{statusField: cfg.StatusField(), resolved: cfg.Statuses.Resolved, comments: shown, def: repo,
				full: full, sessionRE: sessionLinkRE(cfg.Session.Link)}
			v.sprintField, v.sprintFieldID = showSprint(cmd.Context(), app, cfg)
			if !full {
				v.bodyRunes, v.commentBudget = maxBodyRunes, commentBudgetRunes
			}
			var cards []*issue.Card
			var failed []issue.Result
			for _, r := range res {
				if r.Err != nil {
					failed = append(failed, r)
				} else {
					cards = append(cards, r.Card)
				}
			}
			unread := refs[len(res):]
			text := func(w io.Writer) {
				for i, c := range cards {
					switch {
					case i == 0:
					case textBrief:
						fmt.Fprintln(w)
					default:
						fmt.Fprintln(w, "\n"+cardSeparator)
					}
					if textBrief {
						fmt.Fprint(w, v.brief(c))
					} else {
						fmt.Fprint(w, v.card(c))
					}
				}
				if briefByDefault {
					fmt.Fprintf(w, "\nbrief, as for several issues: %s N prints one card, --comments N or --full all of them\n",
						cmd.CommandPath())
				}
			}
			err = showError(repo, unread, failed, readErr)
			if len(cards) == 0 { // it failed outright: the error alone, also in JSON
				return err
			}
			return emitResult(cmd, v.result(cards, brief, failed, unread, readErr), text, err)
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

// showArgs accepts one or more issues.
func showArgs(cmd *cobra.Command, args []string) error {
	help := cmd.CommandPath() + " --help"
	if len(args) == 0 {
		return render.Usage("%s needs an issue number, like %s", cmd.Name(), issueExamples).WithFix(help)
	}
	for _, a := range args {
		if err := checkArg(cmd, a, "issues"); err != nil {
			return err
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
// alone, grouped by cause and repository ("issues #9, #10 not found in
// owner/repo"); repo is the default repository, which issues are named
// relative to. The first line decides the exit code and the fix line; the
// fix of every other cause stays on its own line, unless it is that same
// fix.
func showError(repo issue.Repo, unread []issue.Ref, failed []issue.Result, readErr error) error {
	type group struct {
		cause issue.Cause
		repo  issue.Repo
		refs  []issue.Ref
		err   *render.Error
	}
	var groups []*group
	for _, r := range failed {
		i := slices.IndexFunc(groups, func(g *group) bool {
			return r.Cause != "" && g.cause == r.Cause && strings.EqualFold(g.repo.String(), r.Repo.String())
		})
		if i < 0 {
			groups = append(groups, &group{cause: r.Cause, repo: r.Repo, err: render.FromError(r.Err)})
			i = len(groups) - 1
		}
		groups[i].refs = append(groups[i].refs, r.Ref())
	}
	what := func(g *group) string {
		switch {
		case len(g.refs) == 1:
			return g.err.What
		case g.cause == issue.CauseMissing:
			return fmt.Sprintf("issues %s not found in %s", refNames(g.refs, repo), g.repo)
		case g.cause == issue.CausePullRequest:
			return fmt.Sprintf("%s in %s are pull requests; gh dojo works with issues only", refNames(g.refs, repo), g.repo)
		case g.cause == issue.CauseNoRepo:
			return refNames(g.refs, repo) + ": " + g.err.What
		}
		return g.err.What
	}

	var lines []string
	var first *render.Error
	if readErr != nil {
		first = render.FromError(readErr)
		lines = append(lines, refNames(unread, repo)+" not read: "+first.What)
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
