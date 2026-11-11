package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/effects"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// githubBackend is the Backend verbs use: it reads cards and applies plans
// through one App.
//
// Requests with a warm schema cache: ReadIssue sends one query, Apply one
// mutation, or two for an issue not on the board whose status is set (see
// effects.Batches). The label and user IDs of the labels and assign
// effects come with the card's query (issue.Lookup); only a label or user
// that does not exist costs one more read, for the error's allowed list.
type githubBackend struct {
	app   *App
	added map[int]string // issue number → board item added in this run
}

func (b *githubBackend) ReadIssue(ctx context.Context, v config.Verb, n int) (workflow.Issue, error) {
	lookup := verbLookup(v)
	card, err := b.app.LookupCard(ctx, n, lookup)
	if err != nil {
		return workflow.Issue{}, err
	}
	if err := statusUnknown(card); err != nil {
		return workflow.Issue{}, err
	}
	if err := b.checkFound(ctx, v, lookup, card); err != nil {
		return workflow.Issue{}, err
	}
	return workflowIssue(card), nil
}

// verbLookup is what the effects of v need looked up with the card: the
// IDs of its labels, and of the user it assigns.
func verbLookup(v config.Verb) issue.Lookup {
	l := issue.Lookup{Labels: append(slices.Clone(v.AddLabels), v.RemoveLabels...)}
	switch v.Assign {
	case "":
	case config.Me:
		l.Viewer = true
	default:
		l.User = v.Assign
	}
	return l
}

// checkFound refuses a verb whose labels do not exist in the repository,
// or whose assignee cannot be assigned there: a config error (exit code 1)
// with the allowed values, which take one more read. None of the verb's
// effects is applied then.
func (b *githubBackend) checkFound(ctx context.Context, v config.Verb, l issue.Lookup, card *issue.Card) error {
	if l.Empty() {
		return nil
	}
	cfg, err := b.app.Config()
	if err != nil {
		return err
	}
	f := card.Found
	if f == nil {
		f = &issue.Found{}
	}
	var missing []string
	for _, name := range l.Labels {
		if f.Labels[name] == "" {
			missing = append(missing, strconv.Quote(name))
		}
	}
	key := "verbs." + v.Name + "."
	switch {
	case len(missing) > 0:
		noun := "label"
		if len(missing) > 1 {
			noun = "labels"
		}
		e := cfg.Errorf("%s: no %s %s in %s", cfg.KeyPath(key+"labels"), noun, strings.Join(missing, ", "), card.Repo).
			WithFix(fmt.Sprintf("gh label create %s -R %s, or fix %s", missing[0], card.Repo, cfg.KeyPath(key+"labels")))
		if names, err := repoLabels(ctx, b.app.Client, card.Repo); err == nil {
			e = e.WithAllowed(closestFirst(names, l.Labels)...)
		}
		return e
	case l.User != "" && f.User == nil:
		e := cfg.Errorf("%s: %s cannot be assigned issues in %s", cfg.KeyPath(key+"assign"), l.User, card.Repo).
			WithFix(fmt.Sprintf("check the login, or give %s access to %s", l.User, card.Repo))
		if logins, err := assignableUsers(ctx, b.app.Client, card.Repo); err == nil {
			e = e.WithAllowed(logins...)
		}
		return e
	case l.Viewer && f.Viewer == nil:
		return render.Network("GitHub did not return the authenticated user for %s = %q", cfg.KeyPath(key+"assign"), config.Me).
			WithFix(render.AuthFix)
	}
	return nil
}

// repoLabels reads the names of all labels of repo, for an allowed list.
func repoLabels(ctx context.Context, q graph.Querier, repo issue.Repo) ([]string, error) {
	type resp struct {
		Repository *struct {
			Labels graph.Connection[newLabel] `json:"labels"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": repo.Owner, "repo": repo.Name}
	page, err := graph.Paginate(ctx, q, newLabelsQuery, vars, 0, func(r *resp) *graph.Connection[newLabel] {
		if r.Repository == nil {
			return nil
		}
		return &r.Repository.Labels
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, len(page.Nodes))
	for i, l := range page.Nodes {
		names[i] = l.Name
	}
	return names, nil
}

// closestFirst moves the names that equal one of wanted but for case to
// the front, so a label written in the wrong case shows its spelling first.
func closestFirst(names, wanted []string) []string {
	var near, rest []string
	for _, n := range names {
		if slices.ContainsFunc(wanted, func(w string) bool { return strings.EqualFold(w, n) }) {
			near = append(near, n)
		} else {
			rest = append(rest, n)
		}
	}
	return append(near, rest...)
}

// assignableQuery reads who can be assigned issues in a repository, for an
// allowed list.
const assignableQuery = `query AssignableUsers($owner: String!, $repo: String!) {
  repository(owner: $owner, name: $repo) {
    assignableUsers(first: 100) { nodes { login } }
  }
}`

// assignableUsers reads up to 100 logins that can be assigned in repo.
func assignableUsers(ctx context.Context, q graph.Querier, repo issue.Repo) ([]string, error) {
	var resp struct {
		Repository *struct {
			AssignableUsers struct {
				Nodes []*struct {
					Login string `json:"login"`
				} `json:"nodes"`
			} `json:"assignableUsers"`
		} `json:"repository"`
	}
	if err := q.Query(ctx, assignableQuery, map[string]any{"owner": repo.Owner, "repo": repo.Name}, &resp); err != nil {
		return nil, err
	}
	if resp.Repository == nil {
		return nil, errors.New("no repository")
	}
	var logins []string
	for _, u := range resp.Repository.AssignableUsers.Nodes {
		if u != nil {
			logins = append(logins, u.Login)
		}
	}
	return logins, nil
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
// labels and users from the card's lookup (see ReadIssue), the board's
// from the schema.
func (b *githubBackend) ids(ctx context.Context, p *workflow.Plan, card *issue.Card) (effects.IDs, error) {
	s, err := b.app.Schema(ctx)
	if err != nil {
		return effects.IDs{}, err
	}
	ids := effects.IDs{Project: s.ID, Issue: card.ID, Item: card.ItemID()}
	if ids.Item == "" {
		ids.Item = b.added[card.Number]
	}
	if f := card.Found; f != nil {
		ids.Labels = f.Labels
		ids.Users = map[string]string{}
		for _, u := range []*issue.User{f.User, f.Viewer} {
			if u != nil {
				ids.Users[u.Login] = u.ID
			}
		}
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
