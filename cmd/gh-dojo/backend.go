package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Trum-ok/gh-dojo/internal/effects"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// githubBackend is the Backend verbs use: it reads cards and applies plans
// through one App.
//
// Requests with a warm schema cache: ReadIssue sends one query, Apply one
// mutation, or two for an issue not on the board whose status is set (see
// effects.Batches).
type githubBackend struct {
	app   *App
	added map[int]string // issue number → board item added in this run
}

func (b *githubBackend) ReadIssue(ctx context.Context, n int) (workflow.Issue, error) {
	card, err := b.app.Card(ctx, n, 0)
	if err != nil {
		return workflow.Issue{}, err
	}
	if err := statusUnknown(card); err != nil {
		return workflow.Issue{}, err
	}
	return workflowIssue(card), nil
}

// statusUnknown refuses a card whose board membership or status could not
// be read: planning from "not on the board" or "no status" would check
// from against an assumed initial status and might add a second item.
func statusUnknown(c *issue.Card) error {
	var e *render.Error
	switch {
	case c.BoardUnknown:
		reason := c.BoardUnknownReason
		if reason == "" {
			reason = "GitHub did not return all of its projects"
		}
		e = render.Network("cannot tell whether #%d is on the board: %s", c.Number, reason)
	case c.StatusUnknown():
		e = render.Network("cannot read the status of #%d on the board", c.Number)
	default:
		return nil
	}
	// GitHub withheld part of the card from this token: a retry or more
	// access may help. A full window of projects needs neither.
	if c.Incomplete {
		e.What += "; retry later"
		e = e.WithFix(render.AuthFix)
	}
	return e
}

func (b *githubBackend) Reset() error { return b.app.ResetSchema() }

// Apply sends the plan's ops as effects.Batches, one mutation each, and
// returns the ops GitHub confirmed. It stops at the first mutation that
// left an op unconfirmed. A mutation that answered for every field but
// also carried errors counts as done; its error is returned at the end if
// nothing else failed, for runVerb to warn about. Errors keep their cause,
// so board.IsStale sees a rejected schema ID.
func (b *githubBackend) Apply(ctx context.Context, p *workflow.Plan) ([]workflow.Op, error) {
	card, ok := p.Issue.Ref.(*issue.Card)
	if !ok || card == nil {
		return nil, render.Internal("the plan of #%d has no issue card to apply it to", p.Issue.Number)
	}
	ids, err := b.ids(ctx, p, card)
	if err != nil {
		return nil, err
	}
	var (
		done    []workflow.Op
		ignored error // an error next to a result for every field
	)
	for _, batch := range effects.Batches(p.Ops) {
		m, err := effects.Build(batch, ids)
		switch {
		case err != nil && len(done) == 0:
			return nil, render.Internal("cannot build the mutation for #%d", p.Issue.Number).WithCause(err)
		case err != nil:
			// An earlier mutation answered without an ID this one needs,
			// such as the new board item.
			return done, render.Network("GitHub's answer lacked an ID the next write needs").
				WithCause(errors.Join(errNotSent, err))
		}
		var data map[string]json.RawMessage
		err = b.app.Client.Mutate(ctx, m.Query, m.Vars, &data)
		ok, missed := m.Outcome(data)
		done = append(done, ok...)
		if item := m.ItemID(data, ""); item != "" {
			ids.Item = item
			b.remember(card.Number, item)
		}
		switch {
		case len(missed) == 0 && err != nil:
			ignored = err
		case len(missed) > 0 && err == nil:
			return done, render.Network("GitHub returned no result for part of the write")
		case err != nil:
			return done, err
		}
	}
	return done, ignored
}

// errNotSent marks an Apply error after which the remaining ops were never
// sent, so they are known not to have happened.
var errNotSent = errors.New("not sent")

// remember keeps the board item this run added for issue n: a re-read
// right after the write may not show it yet.
func (b *githubBackend) remember(n int, item string) {
	if b.added == nil {
		b.added = map[int]string{}
	}
	b.added[n] = item
}

// ids collects the node IDs the plan's ops need: the issue's from its card,
// the board's from the schema.
func (b *githubBackend) ids(ctx context.Context, p *workflow.Plan, card *issue.Card) (effects.IDs, error) {
	s, err := b.app.Schema(ctx)
	if err != nil {
		return effects.IDs{}, err
	}
	ids := effects.IDs{Project: s.ID, Issue: card.ID, Item: card.ItemID()}
	if ids.Item == "" {
		ids.Item = b.added[card.Number]
	}
	for _, op := range p.Ops {
		set, ok := op.(workflow.SetStatus)
		if !ok {
			continue
		}
		cfg, err := b.app.Config()
		if err != nil {
			return effects.IDs{}, err
		}
		field, err := s.Field(cfg.StatusField())
		if err != nil {
			return effects.IDs{}, err
		}
		opt, err := field.Option(set.To)
		if err != nil {
			return effects.IDs{}, err
		}
		ids.StatusField, ids.StatusOption = field.ID, opt.ID
	}
	return ids, nil
}
