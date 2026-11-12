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
// effects.Batches). The label and user IDs of the labels, assign and
// unassign effects, and the issue --by names for blockers = "link", come
// with the card's query (issue.Lookup); only a label to add or a user
// that does not exist costs one more read, for the error's allowed list. ReadIssues
// sends one query per issue.MaxBatch issues, with the lookups once in
// each.
type githubBackend struct {
	app   *App
	added map[string]string // refKey of an issue → board item added in this run
	// allowed are the allowed values checkFound read for its errors, by
	// kind ("labels", "assignable") and repository, so a batch reads them
	// once per repository, not once per issue.
	allowed map[string]allowedValues
}

// allowedValues is one read of allowed values, or why it failed.
type allowedValues struct {
	names []string
	err   error
}

// allowedOnce returns read's answer for kind in repo, reading it once per
// run.
func (b *githubBackend) allowedOnce(kind string, repo issue.Repo, read func() ([]string, error)) ([]string, error) {
	key := kind + " " + strings.ToLower(repo.String())
	if v, ok := b.allowed[key]; ok {
		return v.names, v.err
	}
	names, err := read()
	if b.allowed == nil {
		b.allowed = map[string]allowedValues{}
	}
	b.allowed[key] = allowedValues{names, err}
	return names, err
}

func (b *githubBackend) ReadIssue(ctx context.Context, v config.Verb, ref issue.Ref, a workflow.Args) (workflow.Issue, error) {
	res, err := b.ReadIssues(ctx, v, []issue.Ref{ref}, a)
	if err != nil {
		return workflow.Issue{}, err
	}
	return res[0].Issue, res[0].Err
}

// ReadIssues reads the cards of refs with the lookups of v once per
// request (see issue.ReadRefs): for one issue, the request ReadIssue
// sends.
func (b *githubBackend) ReadIssues(ctx context.Context, v config.Verb, refs []issue.Ref, a workflow.Args) ([]IssueRead, error) {
	def, err := b.app.Repo()
	if err != nil {
		return nil, err
	}
	lookup := verbLookup(v, a)
	var bys []issue.Ref // the issues --by names, for blockers = "link"
	if v.Blockers == config.BlockersLink && a.HasBy {
		if bys, err = byRefs(v, a.By, def, issue.Ref{}); err != nil {
			return nil, err
		}
		lookup.Links = issue.Links(bys)
	}
	out := make([]IssueRead, len(refs))
	var read []issue.Ref
	for i, ref := range refs {
		ref = ref.In(def)
		out[i].Ref = ref
		if slices.ContainsFunc(bys, ref.Same) {
			out[i].Err = selfBlock(ref, def, v.Name+" --by")
			continue
		}
		read = append(read, ref)
	}
	if len(read) == 0 {
		return out, nil
	}
	results, err := b.app.cards(ctx, read, 0, lookup)
	if err != nil && len(results) == 0 {
		return nil, err
	}
	byRef := make(map[string]issue.Result, len(results))
	for _, r := range results {
		byRef[refKey(r.Ref())] = r
	}
	for i := range out {
		r := &out[i]
		if r.Err != nil {
			continue
		}
		res, ok := byRef[refKey(r.Ref)]
		switch {
		case !ok && err != nil:
			r.Err = err // not read: a later request failed
			continue
		case !ok:
			r.Err = render.Internal("%s was not read", r.Ref.String(def))
			continue
		case res.Err != nil:
			r.Err = res.Err
			continue
		}
		r.Ref = res.Card.Ref() // as GitHub or the config spells it
		is, failed, err := b.workflowIssue(ctx, v, lookup, res.Card, def)
		if err != nil {
			return nil, err
		}
		r.Issue, r.Err = is, failed
	}
	return out, nil
}

// workflowIssue turns a card read with lookup into the state v plans
// with. failed fails this issue alone: its board place or its sprint could
// not be read, a label to remove is spelled another way on it, or an issue
// --by names is this one or blocked by it. err fails every issue: what v
// needs is missing for all of them (a label to add, the assignee, a --by
// issue), or the config or the board schema is wrong. A label or an
// assignee is looked up per repository, so one missing in a repository
// other than the default, def, fails only that issue.
func (b *githubBackend) workflowIssue(ctx context.Context, v config.Verb, lookup issue.Lookup, card *issue.Card, def issue.Repo) (is workflow.Issue, failed, err error) {
	if err := statusUnknown(card, def); err != nil {
		return workflow.Issue{}, err, nil
	}
	if err := b.checkFound(ctx, v, lookup, card); err != nil {
		if lookupMissing(v, lookup, card.Found) && card.Ref().Same(issue.Ref{Repo: def, Number: card.Number}) {
			return workflow.Issue{}, nil, err
		}
		return workflow.Issue{}, err, nil
	}
	is = workflowIssue(card, def)
	if card.Found != nil {
		for _, name := range v.RemoveLabels {
			if card.Found.Labels[name] == "" {
				is.MissingLabels = append(is.MissingLabels, name)
			}
		}
	}
	if failed, err := b.fieldState(ctx, v, card, &is); failed != nil || err != nil {
		return workflow.Issue{}, failed, err
	}
	if v.ChecksBlockers() {
		cfg, err := b.app.Config()
		if err != nil {
			return workflow.Issue{}, nil, err
		}
		is.Unresolved, is.UnreadBlockers = unresolvedBlockers(card, cfg.Statuses.Resolved, def)
	}
	if len(lookup.Links) > 0 {
		if is.ByIssues, failed, err = byIssues(v, card, def); failed != nil || err != nil {
			return workflow.Issue{}, failed, err
		}
	}
	return is, nil, nil
}

// lookupMissing reports whether a lookup of v found nothing for every
// issue alike: a label to add, the user to assign or the viewer is
// missing. checkFound refuses the verb then, whatever the issue.
func lookupMissing(v config.Verb, l issue.Lookup, f *issue.Found) bool {
	if f == nil {
		f = &issue.Found{}
	}
	return slices.ContainsFunc(v.AddLabels, func(name string) bool { return f.Labels[name] == "" }) ||
		l.User != "" && f.User == nil || l.Viewer && f.Viewer == nil
}

// byRefs parses --by of a verb with blockers = "link" for issue target:
// issues to link (see splitRefs), a bare number in def, or none for free
// text, which only fills the comment. Issues and text mixed are exit code
// 1: linking some and not others would be a guess. target is zero for a
// batch, which checks each issue against the refs itself.
func byRefs(v config.Verb, by string, def issue.Repo, target issue.Ref) ([]issue.Ref, error) {
	refs, text, err := splitRefs([]string{by}, def)
	if err != nil {
		return nil, err
	}
	switch {
	case len(refs) == 0:
		return nil, nil
	case len(text) > 0:
		return nil, render.Usage("%s --by %q mixes issues and text: pass only issues, comma-separated, to link them, "+
			"or only text for the comment", v.Name, strings.TrimSpace(by)).
			WithFix("gh dojo " + v.Name + " --help")
	}
	for _, r := range refs {
		if target.Number > 0 && r.Same(target) {
			return nil, selfBlock(target, def, v.Name+" --by")
		}
	}
	return refs, nil
}

// byIssues are the issues --by names as read with the card (Lookup.Links):
// a missing one is exit code 3 (err, the same for every issue), one that
// is the card's issue or would close a cycle exit code 1 (failed, this
// issue's alone); a pull request is kept, to be named in the comment but
// not linked. Each is named relative to def, and mentioned in the comment
// relative to the card's repository.
func byIssues(v config.Verb, card *issue.Card, def issue.Repo) (out []workflow.ByIssue, failed, err error) {
	var links []*issue.Linked
	if card.Found != nil {
		links = card.Found.Links
	}
	if len(links) == 0 {
		return nil, nil, render.Internal("the issues --by names were not read")
	}
	var issues []*issue.Linked // the pull requests are not linked, so not an error
	for _, l := range links {
		if !l.PullRequest {
			issues = append(issues, l)
		}
	}
	if err := issue.LinksError("--by", issues, def); err != nil {
		return nil, nil, err
	}
	out = make([]workflow.ByIssue, 0, len(links))
	for _, l := range links {
		ref := l.Ref.String(def)
		mention := l.Ref.String(card.Repo)
		switch {
		case l.PullRequest:
			out = append(out, workflow.ByIssue{Ref: ref, Mention: mention, PullRequest: true})
			continue
		case l.ID == card.ID:
			return nil, selfBlock(card.Ref(), def, v.Name+" --by"), nil
		case l.BlockedBy(card.ID):
			return nil, cycleError(card.Ref(), def, ref, v.Name+" --by"), nil
		}
		out = append(out, workflow.ByIssue{Ref: ref, Mention: mention, Blocks: l.Blocks(card.ID)})
	}
	return out, nil, nil
}

// selfBlock is the error for an issue named as its own blocker by flag.
func selfBlock(target issue.Ref, def issue.Repo, flag string) error {
	name := target.String(def)
	return render.Usage("%s names %s itself; an issue cannot block itself", flag, name).
		WithFix(fmt.Sprintf("pass the issues that block %s, not %s itself", name, name))
}

// cycleError is the error for a blocker ref of issue target that target
// already blocks, named by flag: linking it would close a cycle, which
// GitHub refuses. Nothing is written; gh dojo show lists target's links
// both ways.
func cycleError(target issue.Ref, def issue.Repo, ref, flag string) error {
	name := target.String(def)
	return render.Usage("%s %s: %s already blocks %s, so %s cannot block %s too (a cycle)", flag, ref, name, ref, ref, name).
		WithFix("gh dojo show " + target.Arg(def))
}

// unresolvedBlockers returns the card's blockers that still block (see
// issue.Card.UnresolvedBlockers), each with where it stands as show says
// it, but without why a status was not read (show has it), and how many
// blockers were not read at all. They are named relative to def.
func unresolvedBlockers(c *issue.Card, resolved []string, def issue.Repo) ([]workflow.Blocker, int) {
	var out []workflow.Blocker
	for _, l := range c.UnresolvedBlockers(resolved) {
		place := linkPlace(l)
		if l.StatusUnknown != "" {
			place = "status not read"
		}
		out = append(out, workflow.Blocker{Ref: linkRef(c, l, def), Place: place})
	}
	return out, max(0, c.BlockedBy.TotalCount-len(c.BlockedBy.Nodes))
}

// verbLookup is what the effects of v, run with a, need looked up with
// the card: the IDs of its labels, and of the user it assigns (from the
// config or --assignee). A verb that removes labels reads all of the
// issue's labels, so it can tell one is there; a verb that unassigns reads
// the IDs of the issue's assignees, and the viewer for "@me"; a verb that
// checks blockers reads all of its blockers (GitHub allows 50), so a
// resolved one is not counted as unread.
func verbLookup(v config.Verb, a workflow.Args) issue.Lookup {
	l := issue.Lookup{
		Labels:      append(slices.Clone(v.AddLabels), v.RemoveLabels...),
		AllLabels:   len(v.RemoveLabels) > 0,
		AssigneeIDs: v.Unassign != "",
		AllBlockers: v.ChecksBlockers(),
		Viewer:      v.Unassign == config.Me,
	}
	login := v.Assign
	if v.AssignArg {
		login = strings.TrimSpace(a.Assignee)
	}
	switch login {
	case "":
	case config.Me:
		l.Viewer = true
	default:
		l.User = login
	}
	return l
}

// checkFound refuses a verb whose labels to add do not exist in the
// repository, or whose assignee cannot be assigned there: a config error
// (exit code 1) with the allowed values, which take one more read. None of
// the verb's effects is applied then.
//
// Labels are looked up by their exact name, while GitHub's names are
// unique regardless of case. A label to remove that does not exist is in
// place (workflow.Issue.MissingLabels), unless the issue has it spelled
// another way: then it is refused too, or it would stay. A name that
// differs from an existing label only in case gets "write it as" for a
// fix, since gh label create would fail.
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
	for _, name := range v.AddLabels {
		if f.Labels[name] == "" {
			missing = append(missing, name)
		}
	}
	for _, name := range v.RemoveLabels {
		if f.Labels[name] == "" && spelling(name, card.Labels.Nodes) != "" {
			missing = append(missing, name)
		}
	}
	key := "verbs." + v.Name + "."
	switch {
	case len(missing) > 0:
		noun := "label"
		if len(missing) > 1 {
			noun = "labels"
		}
		quoted := make([]string, len(missing))
		for i, name := range missing {
			quoted[i] = strconv.Quote(name)
		}
		labelsKey := cfg.KeyPath(key + "labels")
		e := cfg.Errorf("%s: no %s %s in %s", labelsKey, noun, strings.Join(quoted, ", "), card.Repo)
		names, err := b.allowedOnce("labels", card.Repo, func() ([]string, error) { return repoLabels(ctx, b.app.Client, card.Repo) })
		if err == nil {
			e = e.WithAllowed(closestFirst(names, missing)...)
		}
		if spelled := spelling(missing[0], append(slices.Clone(card.Labels.Nodes), names...)); spelled != "" {
			return e.WithFix(fmt.Sprintf("write it as %q in %s", spelled, labelsKey))
		}
		return e.WithFix(fmt.Sprintf("gh label create %s -R %s, or fix %s", quoted[0], card.Repo, labelsKey))
	case l.User != "" && f.User == nil:
		e := cfg.Errorf("%s: %s cannot be assigned issues in %s", cfg.KeyPath(key+"assign"), l.User, card.Repo).
			WithFix(fmt.Sprintf("check the login, or give %s access to %s", l.User, card.Repo))
		if v.AssignArg {
			e = render.Usage("--%s: %s cannot be assigned issues in %s", config.ArgAssignee, l.User, card.Repo).
				WithFix(fmt.Sprintf("check the login, or give %s access to %s", l.User, card.Repo))
		}
		if logins, err := b.allowedOnce("assignable", card.Repo, func() ([]string, error) {
			return assignableUsers(ctx, b.app.Client, card.Repo)
		}); err == nil {
			e = e.WithAllowed(logins...)
		}
		return e
	case l.Viewer && f.Viewer == nil:
		return render.Network("GitHub did not return the authenticated user for %s", config.Me).
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

// spelling returns the name among names that equals name but for case, ""
// when there is none.
func spelling(name string, names []string) string {
	for _, n := range names {
		if n != name && strings.EqualFold(n, name) {
			return n
		}
	}
	return ""
}

// closestFirst moves the names that equal one of missing but for case to
// the front, so a label written in the wrong case shows its spelling
// first. Labels that were found stay in place.
func closestFirst(names, missing []string) []string {
	var near, rest []string
	for _, n := range names {
		if slices.ContainsFunc(missing, func(w string) bool { return strings.EqualFold(w, n) }) {
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
func statusUnknown(c *issue.Card, def issue.Repo) error {
	var e *render.Error
	switch {
	case c.BoardUnknown:
		reason := c.BoardUnknownReason
		if reason == "" {
			reason = "GitHub did not return all of its projects"
		}
		e = render.Network("cannot tell whether %s is on the board: %s", c.Ref().String(def), reason)
	case c.StatusUnknown():
		e = render.Network("cannot read the status of %s on the board", c.Ref().String(def))
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

func (b *githubBackend) Repo() (issue.Repo, error) { return b.app.Repo() }

// Apply sends the plan's ops as effects.Batches, one mutation each, and
// returns the ops GitHub confirmed. It stops at the first mutation that
// left an op unconfirmed. A mutation that answered for every field but
// also carried errors counts as done; its error is returned at the end if
// nothing else failed, for runVerb to warn about. Errors keep their cause,
// so board.IsStale sees a rejected schema ID.
func (b *githubBackend) Apply(ctx context.Context, p *workflow.Plan) ([]workflow.Op, error) {
	card, ok := p.Issue.Ref.(*issue.Card)
	if !ok || card == nil {
		return nil, render.Internal("the plan of %s has no issue card to apply it to", p.Issue.Name())
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
			return nil, render.Internal("cannot build the mutation for %s", p.Issue.Name()).WithCause(err)
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
			b.remember(card.Ref(), item)
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

// remember keeps the board item this run added for issue ref: a re-read
// right after the write may not show it yet.
func (b *githubBackend) remember(ref issue.Ref, item string) {
	if b.added == nil {
		b.added = map[string]string{}
	}
	b.added[refKey(ref)] = item
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
		ids.Item = b.added[refKey(card.Ref())]
	}
	if err := b.fieldIDs(s, p.Ops, &ids); err != nil {
		return effects.IDs{}, err
	}
	if f := card.Found; f != nil {
		def, err := b.app.Repo()
		if err != nil {
			return effects.IDs{}, err
		}
		ids.Blockers = map[string]string{}
		for _, l := range f.Links {
			if l.ID != "" {
				ids.Blockers[l.Ref.String(def)] = l.ID // as workflow.AddBlockedBy names it
			}
		}
		ids.Labels = f.Labels
		ids.Users = map[string]string{}
		for _, u := range []*issue.User{f.User, f.Viewer} {
			if u != nil {
				ids.Users[u.Login] = u.ID
			}
		}
		for login, id := range card.AssigneeIDs { // to unassign
			ids.Users[login] = id
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
