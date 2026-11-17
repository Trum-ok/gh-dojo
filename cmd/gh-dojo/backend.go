package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/batch"
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
	// once per repository, not once per issue; so are the open milestones
	// of a repository with more than one card query reads ("milestones").
	allowed map[string]allowedValues
}

// allowedValues is one read of allowed values, or why it failed.
type allowedValues struct {
	names      []string
	milestones []issue.OpenMilestone
	err        error
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
	b.allowed[key] = allowedValues{names: names, err: err}
	return names, err
}

// milestonesOnce returns the open milestones of repo after the cursor
// after, the end of the first page the card read, read page by page once
// per run.
func (b *githubBackend) milestonesOnce(ctx context.Context, repo issue.Repo, after string) ([]issue.OpenMilestone, error) {
	key := "milestones " + strings.ToLower(repo.String())
	if v, ok := b.allowed[key]; ok {
		return v.milestones, v.err
	}
	all, err := issue.ReadMilestones(ctx, b.app.Client, repo, after)
	if b.allowed == nil {
		b.allowed = map[string]allowedValues{}
	}
	b.allowed[key] = allowedValues{milestones: all, err: err}
	return all, err
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
func (b *githubBackend) ReadIssues(ctx context.Context, v config.Verb, refs []issue.Ref, a workflow.Args) ([]batch.Read, error) {
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
	if v.ClosesDuplicate() && a.HasOf {
		of, err := ofRef(v, a.Of, def)
		if err != nil {
			return nil, err
		}
		lookup.Original = &of
	}
	out := make([]batch.Read, len(refs))
	var read []issue.Ref
	for i, ref := range refs {
		ref = ref.In(def)
		out[i].Ref = ref
		if slices.ContainsFunc(bys, ref.Same) {
			out[i].Err = selfBlock(ref, def, v.Name+" --by")
			continue
		}
		if o := lookup.Original; o != nil && ref.Same(*o) {
			out[i].Err = selfDuplicate(ref, def, v.Name)
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
			r.Err, r.Cause = res.Err, res.Cause
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
	if every, err := b.checkFound(ctx, v, lookup, card); err != nil {
		if every && card.Ref().Same(issue.Ref{Repo: def, Number: card.Number}) {
			return workflow.Issue{}, nil, err
		}
		return workflow.Issue{}, err, nil
	}
	is = workflowIssue(card, def)
	if milestoneInPlace(lookup, card) {
		is.SetMilestone = card.Milestone.Title // whatever the repository's match
	}
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
	if v.ChecksBlockers() || v.ChecksSubIssues() {
		cfg, err := b.app.Config()
		if err != nil {
			return workflow.Issue{}, nil, err
		}
		if v.ChecksBlockers() {
			is.Unresolved, is.UnreadBlockers = unresolvedLinks(card, card.BlockedBy, card.UnresolvedBlockers(cfg.Statuses.Resolved), def)
		}
		if v.ChecksSubIssues() {
			is.UnresolvedSubIssues, is.UnreadSubIssues = unresolvedLinks(card, card.SubIssues, card.UnresolvedSubIssues(cfg.Statuses.Resolved), def)
		}
	}
	if lookup.Original != nil {
		if is.Original, failed, err = originalIssue(v, card, def); failed != nil || err != nil {
			return workflow.Issue{}, failed, err
		}
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

// milestoneInPlace reports whether the card's issue is in the milestone
// the lookup names by its whole title (issue.SameMilestone): it has it
// already, open or closed, and needs no match.
func milestoneInPlace(l issue.Lookup, card *issue.Card) bool {
	return l.Milestone != "" && card.Milestone != nil && issue.SameMilestone(card.Milestone.Title, l.Milestone)
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

// ofRef parses --of of a verb with close = "duplicate": exactly one issue
// (17, #17, owner/repo#17 or its URL), a bare number in def. Anything
// else is exit code 1.
func ofRef(v config.Verb, of string, def issue.Repo) (issue.Ref, error) {
	refs, text, err := splitRefs([]string{of}, def)
	if err != nil {
		return issue.Ref{}, err
	}
	if len(refs) != 1 || len(text) > 0 {
		return issue.Ref{}, render.Usage("%s --%s %q: name the one original issue: 17, #17, owner/repo#17 or its URL",
			v.Name, config.ArgOf, strings.TrimSpace(of)).
			WithFix("gh dojo " + v.Name + " --help")
	}
	return refs[0], nil
}

// originalIssue is the issue --of names as read with the card
// (Lookup.Original): a missing one or a pull request is exit code 3 (err,
// the same for every issue), the card's issue itself exit code 1 (failed,
// this issue's alone). It is named relative to def, and mentioned in the
// comment relative to the card's repository.
func originalIssue(v config.Verb, card *issue.Card, def issue.Repo) (out *workflow.Original, failed, err error) {
	var l *issue.Linked
	if card.Found != nil {
		l = card.Found.Original
	}
	flag := "--" + config.ArgOf
	switch {
	case l == nil:
		return nil, nil, render.Internal("the issue %s names was not read", flag)
	case l.Missing:
		return nil, nil, issue.LinksError(flag, []*issue.Linked{l}, def)
	case l.PullRequest:
		return nil, nil, render.NotFound("%s %s: a pull request, not an issue; only an issue can be the original of a duplicate",
			flag, l.Ref.String(def))
	case l.ID == card.ID:
		return nil, selfDuplicate(card.Ref(), def, v.Name), nil
	}
	return &workflow.Original{Ref: l.Ref.String(def), Mention: l.Ref.String(card.Repo)}, nil, nil
}

// selfDuplicate is the error for an issue named by verb's --of as its own
// original.
func selfDuplicate(target issue.Ref, def issue.Repo, verb string) error {
	name := target.String(def)
	return render.Usage("%s --%s names %s itself; an issue cannot duplicate itself", verb, config.ArgOf, name).
		WithFix(fmt.Sprintf("pass the original issue %s duplicates, not %s itself", name, name))
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
	return workflow.SelfBlockError(target.String(def), flag)
}

// cycleError is the error for a blocker ref of issue target that target
// already blocks, named by flag: linking it would close a cycle, which
// GitHub refuses. Nothing is written; gh dojo show lists target's links
// both ways.
func cycleError(target issue.Ref, def issue.Repo, ref, flag string) error {
	return workflow.CycleError(target.String(def), target.Arg(def), ref, flag)
}

// unresolvedLinks returns the unresolved ones among the card's linked
// issues all (its blockers or its sub-issues; see
// issue.Card.UnresolvedBlockers), each with where it stands as show says
// it, but without why a status was not read (show has it), and how many
// of all were not read at all. They are named relative to def.
func unresolvedLinks(c *issue.Card, all graph.Page[issue.Link], unresolved []issue.Link, def issue.Repo) ([]workflow.Blocker, int) {
	var out []workflow.Blocker
	for _, l := range unresolved {
		place := linkPlace(l)
		if l.StatusUnknown != "" {
			place = "status not read"
		}
		out = append(out, workflow.Blocker{Ref: linkRef(c, l, def), Place: place})
	}
	return out, max(0, all.TotalCount-len(all.Nodes))
}

// verbLookup is what the effects of v, run with a, need looked up with
// the card: the IDs of its labels, of the user it assigns (from the
// config or --assignee), and the open milestones for the one it sets (from
// the config or --milestone). A verb that removes labels, or checks them
// (from_labels, refuse_labels), reads all of the issue's labels, so it
// can tell one is there; a verb that unassigns reads
// the IDs of the issue's assignees, and the viewer for "@me"; a verb that
// checks blockers reads all of its blockers (GitHub allows 50), and one
// that checks sub-issues all of them (GitHub allows 100), so a resolved
// one is not counted as unread.
func verbLookup(v config.Verb, a workflow.Args) issue.Lookup {
	l := issue.Lookup{
		Labels:       append(slices.Clone(v.AddLabels), v.RemoveLabels...),
		AllLabels:    len(v.RemoveLabels) > 0 || v.ChecksLabels(),
		AssigneeIDs:  v.Unassign != "",
		AllBlockers:  v.ChecksBlockers(),
		AllSubIssues: v.ChecksSubIssues(),
		Viewer:       v.Unassign == config.Me,
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
	l.Milestone = v.Milestone
	switch {
	case workflow.ClearsMilestone(v, a): // no milestone to look up
		l.Milestone = ""
	case v.MilestoneArg:
		l.Milestone = strings.TrimSpace(a.Milestone)
	}
	return l
}

// checkFound refuses a verb whose labels to add do not exist in the
// repository, or whose assignee cannot be assigned there: a config error
// (exit code 1) with the allowed values, which take one more read. None of
// the verb's effects is applied then. So is one whose milestone names no
// open milestone of the repository, or several (see matchMilestone),
// unless the issue is in that milestone already (milestoneInPlace). every
// says the error is the same for every issue of the repository (see
// lookupMissing), rather than this issue's alone. The milestone is
// matched first: it is the repository's, so a refusal of this issue alone
// (a label to remove it has in another case) never leaves it unmatched.
//
// Labels are looked up by their exact name, while GitHub's names are
// unique regardless of case. A label to remove that does not exist is in
// place (workflow.Issue.MissingLabels), unless the issue has it spelled
// another way: then it is refused too, or it would stay. A name that
// differs from an existing label only in case gets "write it as" for a
// fix, since gh label create would fail.
func (b *githubBackend) checkFound(ctx context.Context, v config.Verb, l issue.Lookup, card *issue.Card) (every bool, err error) {
	if l.Empty() {
		return false, nil
	}
	cfg, err := b.app.Config()
	if err != nil {
		return true, err
	}
	f := card.Found
	if f == nil {
		f = &issue.Found{}
	}
	if l.Milestone != "" && !milestoneInPlace(l, card) {
		if err := b.matchMilestone(ctx, v, l, card, f); err != nil {
			return true, err
		}
	}
	defer func() { every = err != nil && lookupMissing(v, l, f) }()
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
			return false, e.WithFix(fmt.Sprintf("write it as %q in %s", spelled, labelsKey))
		}
		return false, e.WithFix(fmt.Sprintf("gh label create %s -R %s, or fix %s", quoted[0], card.Repo, labelsKey))
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
		return false, e
	case l.Viewer && f.Viewer == nil:
		return false, render.Network("GitHub did not return the authenticated user for %s", config.Me).
			WithFix(render.AuthFix)
	}
	return false, nil
}

// matchMilestone finds the open milestone of the card's repository that
// the verb's milestone names (Lookup.Milestone), as gh dojo new --milestone
// does (issue.MatchMilestone), and keeps it in f.Milestone, or why none
// matches in f.MilestoneErr, for every card of the repository. The open
// milestones came with the card; past the first 100 the rest are read
// page by page, once per repository and run. No match, or several, is
// exit code 1: a usage error for --milestone, a config error naming the
// key for a title of the config.
func (b *githubBackend) matchMilestone(ctx context.Context, v config.Verb, l issue.Lookup, card *issue.Card, f *issue.Found) error {
	switch {
	case f.Milestone != nil:
		return nil
	case f.MilestoneErr != nil:
		return f.MilestoneErr
	}
	open := f.Milestones
	if f.MoreMilestones {
		rest, err := b.milestonesOnce(ctx, card.Repo, f.MilestonesCursor)
		if err != nil {
			return err
		}
		open = append(slices.Clone(f.Milestones), rest...)
	}
	m, err := issue.MatchMilestone(l.Milestone, open, card.Repo)
	switch {
	case err == nil:
		f.Milestone = m
		return nil
	case !v.MilestoneArg:
		cfg, cerr := b.app.Config()
		if cerr != nil {
			return cerr
		}
		e := issue.MilestoneKeyError(cfg.KeyPath("verbs."+v.Name+".milestone"), err)
		e.What = cfg.Display + ": " + e.What
		err = e
	}
	f.MilestoneErr = err
	return err
}

// repoLabels reads the names of all labels of repo, for an allowed list.
func repoLabels(ctx context.Context, q graph.Querier, repo issue.Repo) ([]string, error) {
	type resp struct {
		Repository *struct {
			Labels graph.Connection[issue.Label] `json:"labels"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": repo.Owner, "repo": repo.Name}
	page, err := graph.Paginate(ctx, q, issue.LabelsQuery, vars, 0, func(r *resp) *graph.Connection[issue.Label] {
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
	for _, ops := range effects.Batches(p.Ops) {
		m, err := effects.Build(ops, ids)
		switch {
		case err != nil && len(done) == 0:
			return nil, render.Internal("cannot build the mutation for %s", p.Issue.Name()).WithCause(err)
		case err != nil:
			// An earlier mutation answered without an ID this one needs,
			// such as the new board item.
			return done, render.Network("GitHub's answer lacked an ID the next write needs").
				WithCause(errors.Join(batch.ErrNotSent, err))
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
		if f.Original != nil {
			ids.Original = f.Original.ID
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
		if f.Milestone != nil {
			ids.Milestone = f.Milestone.ID
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
