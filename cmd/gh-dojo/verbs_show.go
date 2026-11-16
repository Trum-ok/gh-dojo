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

// showFlag is the verb flag.
const showFlag = "show"

// shownCard is the card --show prints; nil prints nothing.
type shownCard struct {
	view cardView
	card *issue.Card
	verb string
}

// newShownCard is the card of it for --show, nil without the flag or when
// the issue was not read.
func newShownCard(ctx context.Context, cfg *config.Config, be Backend, def issue.Repo, v config.Verb, it *batch.Item, show bool) *shownCard {
	if !show {
		return nil
	}
	c := itemCard(it)
	if c == nil {
		return nil
	}
	view := newCardView(cfg, def, defaultShownComments, false)
	if gb, ok := be.(*githubBackend); ok {
		view.sprintField, view.sprintFieldID = showSprint(ctx, gb.app, cfg) // the schema the read loaded
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
	c := s.view.cardJSON(s.card, false)
	r.Issues[0].Card = &c
	return r
}

// showOne refuses --show on several issues: their cards are gh dojo show's.
func showOne(n int) error {
	return render.Usage("--%s prints the card of one issue, got %d issues", showFlag, n).
		WithFix(fmt.Sprintf("drop --%s, then gh dojo show with the numbers", showFlag))
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
