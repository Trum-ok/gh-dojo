package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"

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
	app *App
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

// Apply sends the plan's ops as effects.Batches, one mutation each. It
// stops at the first failed mutation: when nothing took effect yet that
// error is returned as is; otherwise a render.Partial error says what was
// done and what was not (see partial). Either way the cause stays
// reachable, so board.IsStale sees a rejected schema ID.
func (b *githubBackend) Apply(ctx context.Context, p *workflow.Plan) error {
	card, ok := p.Issue.Ref.(*issue.Card)
	if !ok || card == nil {
		return render.Internal("the plan of #%d has no issue card to apply it to", p.Issue.Number)
	}
	ids, err := b.ids(ctx, p, card)
	if err != nil {
		return err
	}
	var done []workflow.Op
	batches := effects.Batches(p.Ops)
	for i, batch := range batches {
		m, err := effects.Build(batch, ids)
		if err != nil {
			return render.Internal("cannot build the mutation for #%d", p.Issue.Number).WithCause(err)
		}
		var data map[string]json.RawMessage
		err = b.app.Client.Mutate(ctx, m.Query, m.Vars, &data)
		ok, missed := m.Outcome(data)
		done = append(done, ok...)
		ids.Item = m.ItemID(data, ids.Item)
		if err == nil && len(missed) > 0 {
			err = render.Network("GitHub returned no result for part of the write")
		}
		if err == nil && i+1 < len(batches) && ids.Item == "" {
			err = render.Network("GitHub did not return the board item of #%d", p.Issue.Number)
		}
		if err != nil {
			for _, rest := range batches[i+1:] {
				missed = append(missed, rest...)
			}
			return partial(p, done, missed, err)
		}
	}
	return nil
}

// ids collects the node IDs the plan's ops need: the issue's from its card,
// the board's from the schema.
func (b *githubBackend) ids(ctx context.Context, p *workflow.Plan, card *issue.Card) (effects.IDs, error) {
	s, err := b.app.Schema(ctx)
	if err != nil {
		return effects.IDs{}, err
	}
	ids := effects.IDs{Project: s.ID, Issue: card.ID, Item: card.ItemID()}
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

// partial reports a write that failed with err after done took effect and
// missed did not. With nothing done, err itself is the answer. Otherwise it
// is exit code 5, re-run the same command: the re-run re-reads the issue,
// so its plan leaves out what is done.
//
//	#42 added to the board; status not changed, comment not posted: GitHub API rejected the request; re-run the same command
//
// When GitHub answered with per-field errors the missed ops are known not
// to have happened; when the answer was lost (timeout, 5xx) they are only
// unconfirmed.
func partial(p *workflow.Plan, done, missed []workflow.Op, err error) error {
	if len(done) == 0 {
		return err
	}
	e := render.FromError(err)
	reason, _, _ := strings.Cut(strings.TrimSpace(strings.SplitN(e.What, "\n", 2)[0]), ";")
	shown := *p
	shown.Ops = done
	head := shown.Confirm()

	var what string
	if gqlErr := (*api.GraphQLError)(nil); errors.As(err, &gqlErr) {
		words := make([]string, len(missed))
		for i, op := range missed {
			words[i] = effects.Missed(op)
		}
		what = strings.Join(words, ", ")
	} else {
		what = "the rest is unconfirmed"
	}
	return render.Partial("%s; %s: %s; re-run the same command", head, what, reason).
		WithFix(e.Fix).WithCause(err)
}
