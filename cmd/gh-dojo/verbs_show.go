package main

import (
	"context"
	"fmt"
	"io"

	"github.com/Trum-ok/gh-dojo/internal/batch"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// A verb with --show also prints the issue's card, as gh dojo show N does,
// so taking an issue and reading it is one command: "take #42" is
// start 42 --show, not show 42 then start 42 (#57).
//
// The card is the one the verb read to plan: the same request, so --show
// costs nothing more. A verb that wrote something prints the card as it
// was before its changes, under a line that says so; the confirmation line
// says what changed. Patching the card with the plan would leave it half
// new (the status, but not a sprint or a link), and a read after the write
// may lag behind it.
//
// The card is printed whenever the issue was read: after the confirmation
// line, the dry-run plan, or alone when the verb refused it (its error is
// on stderr), since that is when the agent needs it most.

// showFlag is the verb flag. --comments and --full fold the card as show's
// flags do, and ask for it without --show (#95), as --jq asks for --json:
// start 42 --full is the card whole.
const showFlag = "show"

// showOpts is the card a verb's --show, --comments and --full ask for.
type showOpts struct {
	flag     string // the flag that asked for the card, "" for none
	comments int    // how many of the latest comments to print
	full     bool
}

// on says the card was asked for.
func (o showOpts) on() bool { return o.flag != "" }

// window is how many of the latest comments the verb reads: what verbs
// read anyway, or more for --comments; the window show reads for the same
// flags.
func (o showOpts) window() int {
	return min(max(o.comments, issue.DefaultComments), issue.MaxComments)
}

// verbCommentsFlag is a verb's --comments, show's flag whose error points
// at --comment: one letter apart, a verb takes both.
type verbCommentsFlag struct{ commentsFlag }

func (c *verbCommentsFlag) Set(s string) error {
	if err := c.commentsFlag.Set(s); err != nil {
		return fmt.Errorf("%w (the comment to post is --%s)", err, config.ArgComment)
	}
	return nil
}

// shownCard is the card --show prints; nil prints nothing.
type shownCard struct {
	view cardView
	card *issue.Card
	verb string
}

// newShownCard is the card of it for --show, nil without the flag or when
// the issue was not read.
func newShownCard(ctx context.Context, cfg *config.Config, be Backend, def issue.Repo, v config.Verb, it *batch.Item, show showOpts) *shownCard {
	if !show.on() {
		return nil
	}
	c := itemCard(it)
	if c == nil {
		return nil
	}
	view := newCardView(cfg, def, show.comments, show.full)
	if gb, ok := be.(*githubBackend); ok {
		view.sprintField, view.sprintFieldID = showSprint(ctx, gb.app, cfg) // the schema the read loaded
		view.statusFieldID = showStatusID(ctx, gb.app, cfg)
	} else {
		view.sprintField = cfg.SprintField()
	}
	return &shownCard{view: view, card: c, verb: v.Name}
}

// itemCard is the card a verb read for it: the first read, the one it
// planned from; nil when it was not read.
func itemCard(it *batch.Item) *issue.Card {
	var is *workflow.Issue
	switch {
	case it.Writes != nil:
		is = &it.Writes.First.Issue
	case it.Issue != nil:
		is = it.Issue
	default:
		return nil
	}
	c, _ := is.Ref.(*issue.Card)
	return c
}

// write prints the card; lead puts a blank line before it, after the
// confirmation line or the plan; written says the verb changed the issue,
// so the card is from before.
func (s *shownCard) write(w io.Writer, lead, written bool) {
	if s == nil {
		return
	}
	if lead {
		fmt.Fprintln(w)
	}
	if written {
		fmt.Fprintf(w, "card as read before %s:\n", s.verb)
	}
	fmt.Fprint(w, s.view.card(s.card))
}

// alone prints the card with nothing before it: the text of a verb that
// failed, whose error goes to stderr.
func (s *shownCard) alone(written bool) func(io.Writer) {
	return func(w io.Writer) { s.write(w, false, written) }
}

// json adds the card to the verb's JSON of one issue, as show --json has
// it.
func (s *shownCard) json(r verbResult) verbResult {
	if s == nil || len(r.Issues) != 1 {
		return r
	}
	c := s.view.toCardJSON(s.card, false)
	r.Issues[0].Card = &c
	return r
}

// showOne refuses the card's flag, --show, --comments or --full, on
// several issues: their cards are gh dojo show's.
func showOne(flag string, n int) error {
	return render.Usage("--%s prints the card of one issue, got %d issues", flag, n).
		WithFix(fmt.Sprintf("drop --%s, then gh dojo show with the numbers", flag))
}

// newCardView renders cards as show does with comments and full.
func newCardView(cfg *config.Config, def issue.Repo, comments int, full bool) cardView {
	v := cardView{statusField: cfg.StatusField(), resolved: cfg.Statuses.Resolved, comments: comments, def: def,
		full: full, sessionRE: sessionLinkRE(cfg.Session.Link)}
	if !full {
		v.bodyRunes, v.commentBudget = maxBodyRunes, commentBudgetRunes
	}
	return v
}
