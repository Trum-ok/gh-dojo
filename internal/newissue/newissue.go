// Package newissue runs gh dojo new: it creates an issue with its labels,
// on the board, in the initial status, or finishes the one a failed run
// left. The command parses the flags, calls Run and emits the result.
//
// Its plan is separate from its execution, as a verb's: the read is
// issue.ReadNewContext, the checks and the plan workflow.CheckNew and
// workflow.PlanNew (no network), the mutations effects'; this package
// sends them (Issue.apply) and records how far the run got, for the
// confirmation (Issue.Confirm), the dry run's plan (Issue.DryRunText) and
// the command's JSON.
//
// Requests with a warm schema cache: one read (issue.ReadNewContext: repo
// ID, labels, open milestones, recent issues, the --blocked-by and
// --parent issues, and the --assignee users), createIssue (with its
// labels, milestone, parent and assignees), addProjectV2ItemById, then the
// status with the blocked-by links (addBlockedBy, aliased, in the same
// mutation): four. Without statuses.initial and --blocked-by the last
// write is skipped. --parent and --assignee cost no request: createIssue
// takes them (parentIssueId, assigneeIds), and only a resumed issue
// without them gets addSubIssue or addAssigneesToAssignable, in that last
// mutation. An --assignee who cannot be assigned costs one more read, for
// the allowed list, before failing.
//
// The board item is added explicitly rather than with createIssue's
// projectV2Ids: GitHub adds those asynchronously, so the createIssue answer
// often had no item yet, and the addProjectV2ItemById then sent was refused
// in live runs (issue #17), likely racing that add. addProjectV2ItemById is
// synchronous and returns the existing item for content already on the
// board. Should it still be refused (the board's auto-add workflow racing
// it, say), the issue's board items are re-read once (RecheckDelay later)
// and an item found there is used.
//
// Retries are safe (principle 5): the body ends with a hidden marker
// <!-- gh-dojo:new:<hash> --> of the normalized title
// (workflow.NewDedupKey), and the read looks for it among the repo's
// issue.NewDedupWindow newest open issues, keeping those the viewer wrote.
// A match is resumed instead of created again: missing labels and
// --assignee users are added (assignees it has stay), the --milestone is
// set if it has none (a different one stays, with a warning), the issue is
// unarchived or put on the board and, if it has no status, given the
// initial one; blocked-by links it lacks are added, and so is the --parent
// if it has no parent (a different one stays, with a warning). So filing
// an open issue again with the same title within the window resumes it.
// An issue that dropped out of the window (issue.NewDedupWindow newer open
// issues in the repo, by anyone) or was closed is created again.
//
// Nothing is written twice within a run either: a lost createIssue answer
// is followed by one re-read (RecheckDelay later), and a stale schema ID
// once the issue exists is exit code 5, never an in-process retry, since a
// lagging re-read could miss the new issue and create another.
package newissue

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/effects"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// RecheckDelay is the pause before re-reading after a lost createIssue
// answer or a refused addProjectV2ItemById, so the issue or its board item,
// if there, shows up. Tests set it to 0.
var RecheckDelay = time.Second

// Options are the arguments of gh dojo new, parsed and checked by the
// command before any request.
type Options struct {
	Title  string
	Body   string
	Type   string // "" when --type is not given
	Areas  []string
	Labels []string
	// Milestone is the --milestone title, "" when not given.
	Milestone string
	// Blockers are the issues --blocked-by names.
	Blockers []issue.Ref
	// Parent is the issue --parent names; nil without --parent.
	Parent *issue.Ref
	// Assignees are the logins --assignee names: without duplicates,
	// config.Me for the authenticated user.
	Assignees []string
	// Repo is the repository of --repo, one of board.repos; zero for the
	// default one.
	Repo issue.Repo
	// DryRun reads and checks everything and writes nothing: the Issue's
	// fields say what would be done (see Issue.DryRunText).
	DryRun bool
}

// Board is the board schema of a run, checked against the config; the
// command's App is one.
type Board interface {
	// Schema returns the board schema, from the cache when fresh.
	Schema(ctx context.Context) (*board.Schema, error)
	// SchemaCached reports whether the schema in use came from the cache.
	SchemaCached() bool
	// ResetSchema drops the schema, so the next Schema re-reads the board.
	ResetSchema() error
}

// Env is what Run works with.
type Env struct {
	Config *config.Config
	Client graph.Doer
	Board  Board
	// Repo returns the default repository, which output names issues
	// relative to.
	Repo func() (issue.Repo, error)
	// Host returns the GitHub host, for an issue's URL in a warning.
	Host func() string
	// Warn gets the run's warnings; nil drops them.
	Warn io.Writer
}

// Issue is the issue new created or resumed, and how far the run got: its
// fields after Run say what was done (in a dry run, what would be).
type Issue struct {
	env  Env
	opts Options

	warnings []string // for Env.Warn

	Number  int    // the issue's number, once it exists
	id      string // its node ID
	title   string // its title on GitHub when found, "" when created now
	Resumed bool   // an earlier run created it; this one found it by its marker
	Labels  []string
	// labelsMissing are the labels a resumed issue lacks, for a dry run.
	labelsMissing []string

	Assigned      []string // the --assignee logins assigned now, as GitHub spells them
	AssignedFound []string // those a resumed issue had already

	itemID     string
	added      bool   // this run added the issue to the board
	Unarchived bool   // the board item was archived when found, and this run restored it
	Status     string // the status found, "" for none
	StatusSet  bool   // the status was set to the initial one now
	noInitial  bool   // statuses.initial is empty: no status was set

	Milestone     *issue.OpenMilestone // the issue's milestone, nil for none
	MilestoneSet  bool                 // set to --milestone now
	milestoneKept string               // the --milestone not applied: a resumed issue had another

	Repo        issue.Repo      // the issue's repository: --repo, or the default one
	Def         issue.Repo      // the default repository, which output names issues relative to
	Linked      []*issue.Linked // blocked-by links added now
	LinkedFound []*issue.Linked // blocked-by links a resumed issue had already

	Parent       *issue.Ref // the issue's parent, nil for none or not known
	ParentSet    bool       // made a sub-issue of --parent now
	parentKept   *issue.Ref // the --parent not applied: a resumed issue had another
	parentHidden bool       // a resumed issue has a parent the token cannot read
	parentErr    error      // GitHub refused the parent of createIssue for good (parentRefused)
}

// Run creates the issue described by o, or finishes the one a failed run
// left, and returns it: nil when no issue exists, else with partial's
// error when the run stopped after the issue existed. Warnings go to
// env.Warn.
func Run(ctx context.Context, env Env, o Options) (*Issue, error) {
	if env.Warn == nil {
		env.Warn = io.Discard
	}
	for attempt := 1; ; attempt++ {
		r := &Issue{env: env, opts: o}
		err := r.run(ctx)
		for _, w := range r.warnings {
			render.Warn(env.Warn, "%s", w)
		}
		if err == nil {
			return r, nil
		}
		stale := env.Board.SchemaCached() && board.IsStale(err)
		if r.Number == 0 {
			// Nothing written yet: a schema ID GitHub rejected is fixed by
			// re-reading the board and trying once more.
			if stale && attempt == 1 && env.Board.ResetSchema() == nil {
				continue
			}
			return nil, err
		}
		if stale {
			// The issue exists; retrying here could miss it in a lagging
			// read and create another. The re-run reads the board afresh.
			_ = env.Board.ResetSchema()
		}
		return r, r.partial(err)
	}
}

// Wrote reports whether this run wrote anything: created the issue, or
// finished one an earlier run created.
func (r *Issue) Wrote() bool {
	if r.opts.DryRun {
		return false
	}
	return !r.Resumed || r.added || r.StatusSet || r.Unarchived || r.MilestoneSet || len(r.Assigned) > 0 ||
		len(r.Linked) > 0 || r.ParentSet
}

// Title is the issue's title on GitHub when found, else the one given.
func (r *Issue) Title() string { return cmp.Or(r.title, r.opts.Title) }

// stepError says which write failed, as "setting status Backlog failed".
type stepError struct {
	step string
	err  error
	// parent: the step is the --parent link, which GitHub refused for good
	// (parentRefused): re-running cannot help.
	parent bool
}

func (e *stepError) Error() string { return e.step + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func (r *Issue) run(ctx context.Context) error {
	cfg := r.env.Config
	s, err := r.env.Board.Schema(ctx)
	if err != nil {
		return err
	}
	def, err := r.env.Repo()
	if err != nil {
		return err
	}
	repo := def
	if r.opts.Repo != (issue.Repo{}) {
		repo = r.opts.Repo
	}
	statusField, err := s.Field(cfg.StatusField())
	if err != nil {
		return err
	}
	var initial *board.Option
	if name := cfg.Statuses.Initial; name != "" {
		if initial, err = statusField.Option(name); err != nil {
			return err
		}
	} else {
		r.noInitial = true
	}

	r.Repo, r.Def = repo, def
	nc, err := issue.ReadNewContext(ctx, r.env.Client, r.contextRequest(repo, statusField.Name, r.opts.Blockers))
	if err != nil {
		return err
	}
	labels, err := r.resolveLabels(ctx, nc)
	if err != nil {
		return err
	}
	var milestone *issue.OpenMilestone
	if r.opts.Milestone != "" {
		all, err := nc.AllMilestones(ctx, r.env.Client, repo)
		if err != nil {
			return err
		}
		if milestone, err = issue.MatchMilestone(r.opts.Milestone, all, repo); err != nil {
			return err
		}
	}
	assignees, err := r.resolveAssignees(ctx, nc)
	if err != nil {
		return err
	}
	if err := issue.LinksError("--"+workflow.NewBlockedByFlag, nc.Links, def); err != nil {
		return err
	}
	if err := parentError(nc.Parent, def); err != nil {
		return err
	}
	r.Labels = issue.LabelNames(labels)

	in := input{nc: nc, labels: labels, milestone: milestone, assignees: assignees, initial: initial}
	marker := workflow.NewMarker(r.opts.Title)
	found := nc.Find(marker, s.ID)
	req, state := r.request(in, found), r.foundState(found, false)
	if err := workflow.CheckNew(req, state); err != nil {
		return err
	}
	plan := workflow.PlanNew(cfg, req, state)
	r.Resumed = plan.Resumed
	if plan.Create && !r.opts.DryRun {
		if err := r.create(ctx, nc.RepoID, withMarker(r.opts.Body, marker), in); err != nil {
			if r.Number != 0 || !errors.Is(err, graph.ErrUnknownOutcome) {
				return err
			}
			// The answer was lost: look once whether the issue was created,
			// and finish it if so.
			if found = r.recheck(ctx, repo, statusField.Name, marker, s.ID); found == nil {
				return err
			}
			r.warnings = append(r.warnings, fmt.Sprintf("GitHub's answer to creating the issue was lost; found %s on a re-read", issue.Ref{Repo: repo, Number: found.Number}.String(def)))
			plan = workflow.PlanNew(cfg, r.request(in, found), r.foundState(found, true))
		}
	}
	r.warnings = append(r.warnings, plan.Warnings...)
	if found != nil {
		r.Number, r.id, r.title = found.Number, found.ID, found.Title
		r.itemID, r.Status = found.ItemID, found.Status
		r.Milestone = found.Milestone
		r.AssignedFound = plan.AlreadyAssigned
		r.labelsMissing = plan.Labels
		r.milestoneKept = plan.MilestoneKept
		if p := nc.Parent; p != nil {
			r.Parent = found.Parent
			if plan.ParentKept {
				r.parentKept, r.parentHidden = &p.Ref, plan.ParentHidden
			}
		}
	}
	if r.opts.DryRun {
		r.planDry(plan, in)
		return nil
	}
	return r.apply(ctx, s, statusField, plan, in)
}

// input is what new resolved against the repository: the context read,
// the labels, the --milestone (nil for none), the --assignee users and the
// initial status (nil for none).
type input struct {
	nc        *issue.NewContext
	labels    []issue.Label
	milestone *issue.OpenMilestone
	assignees []*issue.User
	initial   *board.Option
}

// resolveLabels maps --type, --area and --label to labels of the issue's
// repository (see workflow.ResolveNewLabels), reading all of them only
// when one is not among the first page.
func (r *Issue) resolveLabels(ctx context.Context, nc *issue.NewContext) ([]issue.Label, error) {
	want := workflow.NewLabels{Type: r.opts.Type, Areas: r.opts.Areas, Labels: r.opts.Labels}
	labels, complete := nc.Labels.Nodes, !nc.Labels.PageInfo.HasNextPage
	for {
		names, more, err := workflow.ResolveNewLabels(r.env.Config, want, issue.LabelNames(labels), complete, r.Repo.String())
		if err != nil {
			return nil, err
		}
		if !more {
			out := make([]issue.Label, len(names))
			for i, n := range names {
				out[i] = labelNamed(labels, n)
			}
			return out, nil
		}
		if labels, err = nc.AllLabels(ctx, r.env.Client, r.Repo); err != nil {
			return nil, err
		}
		complete = true
	}
}

// resolveAssignees resolves the --assignee logins to users who can be
// assigned issues in the issue's repository (see
// issue.NewContext.MatchAssignees). Logins that cannot be assigned are one
// usage error (exit code 1) naming them all, with the repository's
// assignable users as the allowed values, which takes one more read.
func (r *Issue) resolveAssignees(ctx context.Context, nc *issue.NewContext) ([]*issue.User, error) {
	users, unknown, noViewer := nc.MatchAssignees(r.opts.Assignees, config.Me)
	if noViewer {
		return nil, render.Network("GitHub did not return the authenticated user for --%s %s", workflow.NewAssigneeFlag, config.Me).
			WithFix(render.AuthFix)
	}
	if len(unknown) == 0 {
		return users, nil
	}
	e := issue.NotAssignable(render.Usage, "--"+workflow.NewAssigneeFlag, unknown, r.Repo)
	if logins, err := issue.ReadAssignable(ctx, r.env.Client, r.Repo); err == nil {
		e = e.WithAllowed(logins...)
	}
	return nil, e
}

// labelNamed is the label of labels with name, which is among them.
func labelNamed(labels []issue.Label, name string) issue.Label {
	return labels[slices.IndexFunc(labels, func(l issue.Label) bool { return l.Name == name })]
}

// contextRequest is what issue.ReadNewContext reads for the run in repo:
// the blockers (none for a re-read), the --parent and the --assignee
// users.
func (r *Issue) contextRequest(repo issue.Repo, statusField string, blockers []issue.Ref) issue.NewRequest {
	logins, viewer := assigneeLogins(r.opts.Assignees)
	return issue.NewRequest{Repo: repo, StatusField: statusField, Blockers: blockers, Parent: r.opts.Parent,
		Logins: logins, Viewer: viewer}
}

// assigneeLogins splits the --assignee values into the logins to look up
// among the assignable users, in order, and whether config.Me is among
// them (the viewer is read for it).
func assigneeLogins(assignees []string) (logins []string, viewer bool) {
	for _, a := range assignees {
		if a == config.Me {
			viewer = true
		} else {
			logins = append(logins, a)
		}
	}
	return logins, viewer
}

// request is what the run asks for, for workflow.CheckNew and PlanNew, the
// links related to found (nil for none).
func (r *Issue) request(in input, found *issue.Created) workflow.NewRequest {
	req := workflow.NewRequest{Repo: r.Repo.String(), Labels: issue.LabelNames(in.labels), Assignees: loginsOf(in.assignees)}
	if in.milestone != nil {
		req.Milestone = in.milestone.Title
	}
	if in.initial != nil {
		req.Initial = in.initial.Name
	}
	for _, l := range in.nc.Links {
		req.Blockers = append(req.Blockers, r.link(l, found))
	}
	if p := in.nc.Parent; p != nil {
		l := r.link(p, found)
		if found != nil && found.ParentID != "" && !l.ParentOf {
			// For the warning that the issue keeps its parent: gh issue
			// edit takes the new parent's number in the issue's
			// repository, else its URL.
			l.EditArg = fmt.Sprintf("https://%s/%s/issues/%d", r.env.Host(), p.Ref.Repo, p.Ref.Number)
			if p.Ref.Repo.Same(r.Repo) {
				l.EditArg = strconv.Itoa(p.Ref.Number)
			}
		}
		req.Parent = &l
	}
	return req
}

// link is l for the plan, related to found (nil for none).
func (r *Issue) link(l *issue.Linked, found *issue.Created) workflow.NewLink {
	out := workflow.NewLink{Name: l.Ref.String(r.Def), Arg: l.Ref.Arg(r.Def),
		SubIssues: l.SubIssues, Full: l.SubIssues >= issue.MaxSubIssues, Ref: l}
	if found != nil {
		out.Found = l.ID == found.ID
		out.Blocks, out.BlockedBy = l.Blocks(found.ID), l.BlockedBy(found.ID)
		out.ParentOf = found.ParentID != "" && found.ParentID == l.ID
	}
	return out
}

// foundState is found for the plan; nil for nil. rechecked: this run
// created it, and found it again after the answer was lost.
func (r *Issue) foundState(found *issue.Created, rechecked bool) *workflow.NewFound {
	if found == nil {
		return nil
	}
	ref := issue.Ref{Repo: r.Repo, Number: found.Number}
	out := &workflow.NewFound{Name: ref.String(r.Def), Arg: ref.Arg(r.Def), Number: found.Number,
		Labels: found.Labels, Assignees: found.Assignees, AssigneesTotal: found.AssigneesTotal,
		OnBoard: found.ItemID != "", Archived: found.Archived, Status: found.Status,
		ParentHidden: found.ParentHidden, Rechecked: rechecked}
	if found.Milestone != nil {
		out.Milestone = found.Milestone.Title
	}
	if found.ParentID != "" {
		out.Parent = r.parentName(found.Parent)
	}
	return out
}

// linksOf returns the issues the plan's links keep (workflow.NewLink.Ref).
func linksOf(links []workflow.NewLink) []*issue.Linked {
	var out []*issue.Linked
	for _, l := range links {
		out = append(out, l.Ref.(*issue.Linked))
	}
	return out
}

// usersOf returns the users of in with logins, in order.
func usersOf(in input, logins []string) []*issue.User {
	var out []*issue.User
	for _, login := range logins {
		if i := slices.IndexFunc(in.assignees, func(u *issue.User) bool { return u.Login == login }); i >= 0 {
			out = append(out, in.assignees[i])
		}
	}
	return out
}

// apply writes what plan has left once the issue exists (createIssue sets
// its labels, milestone, assignees and parent): a resumed issue's labels
// and milestone, one mutation each, then the board item, then the rest in
// one mutation (see finish), recording each step as it takes effect.
func (r *Issue) apply(ctx context.Context, s *board.Schema, statusField *board.Field, plan *workflow.NewPlan, in input) error {
	if !plan.Create && len(plan.Labels) > 0 {
		var ids []string
		for _, n := range plan.Labels {
			ids = append(ids, labelNamed(in.labels, n).ID)
		}
		vars := map[string]any{"issue": r.id, "labels": ids}
		if err := r.env.Client.Mutate(ctx, effects.AddLabelsMutation, vars, nil); err != nil {
			return &stepError{step: "adding labels " + strings.Join(r.Labels, ", ") + " failed", err: err}
		}
	}
	if !plan.Create && plan.Milestone != "" {
		vars := map[string]any{"issue": r.id, "milestone": in.milestone.ID}
		if err := r.env.Client.Mutate(ctx, effects.SetMilestoneMutation, vars, nil); err != nil {
			return &stepError{step: "setting milestone " + render.SingleLine(in.milestone.Title) + " failed", err: err}
		}
		r.Milestone, r.MilestoneSet = in.milestone, true
	}
	if plan.Unarchive {
		vars := map[string]any{"project": s.ID, "item": r.itemID}
		if err := r.env.Client.Mutate(ctx, effects.UnarchiveMutation, vars, nil); err != nil {
			return &stepError{step: "unarchiving it on board " + s.Ref().String() + " failed", err: err}
		}
		r.Unarchived = true
	}
	if plan.AddToBoard {
		if err := r.addToBoard(ctx, s.ID, statusField.Name); err != nil {
			return &stepError{step: "adding it to board " + s.Ref().String() + " failed", err: err}
		}
	}
	// A resumed issue keeps a status it already has, even one found only
	// while adding it to the board: someone may have moved it on purpose.
	initial := in.initial
	if plan.Status == "" || plan.Resumed && r.Status != "" {
		initial = nil
	}
	r.LinkedFound = linksOf(plan.AlreadyLinked)
	var assign []*issue.User
	var parent *issue.Linked
	if !plan.Create {
		assign = usersOf(in, plan.Assign)
		if plan.Parent {
			parent = in.nc.Parent
		}
	}
	if pending := linksOf(plan.Link); len(pending) > 0 || parent != nil || len(assign) > 0 {
		if err := r.finish(ctx, s.ID, statusField.ID, initial, assign, pending, parent); err != nil {
			return err
		}
	} else if initial != nil {
		vars := map[string]any{"project": s.ID, "item": r.itemID, "field": statusField.ID, "option": initial.ID}
		if err := r.env.Client.Mutate(ctx, effects.SetStatusMutation, vars, nil); err != nil {
			return &stepError{step: "setting status " + initial.Name + " failed", err: err}
		}
		r.Status, r.StatusSet = initial.Name, true
	}
	if r.parentErr != nil {
		// The rest is done; createIssue refused the parent for good.
		return &stepError{step: "making it a sub-issue of " + in.nc.Parent.Ref.String(r.Def) + " failed", err: r.parentErr, parent: true}
	}
	return nil
}

// finish sets the status initial (nil leaves it alone), assigns the users
// assign, adds the blocked-by links and makes the issue a sub-issue of
// parent (nil for none) in one mutation (see effects.NewFinish). What took
// effect is recorded (StatusSet, Assigned, Linked, ParentSet) even when
// the rest failed; an error after everything took effect is only a
// warning.
func (r *Issue) finish(ctx context.Context, projectID, fieldID string, initial *board.Option, assign []*issue.User, links []*issue.Linked, parent *issue.Linked) error {
	f := effects.NewFinish{Status: initial != nil, Assignees: userIDs(assign)}
	ids := effects.IDs{Project: projectID, Issue: r.id, Item: r.itemID, StatusField: fieldID}
	if initial != nil {
		ids.StatusOption = initial.ID
	}
	for _, l := range links {
		f.Links = append(f.Links, l.ID)
	}
	if parent != nil {
		f.Parent = parent.ID
	}
	m := f.Build(ids)
	var data map[string]json.RawMessage
	err := r.env.Client.Mutate(ctx, m.Query, m.Vars, &data)
	var steps []string
	if initial != nil {
		if effects.Answered(data, effects.AliasStatus) {
			r.Status, r.StatusSet = initial.Name, true
		} else {
			steps = append(steps, "setting status "+initial.Name)
		}
	}
	if len(assign) > 0 {
		if effects.Answered(data, effects.AliasAssign) {
			r.Assigned = loginsOf(assign)
		} else {
			steps = append(steps, "assigning "+render.JoinWords(loginsOf(assign), "and"))
		}
	}
	var missed []*issue.Linked
	for i, l := range links {
		if effects.Answered(data, effects.LinkAlias(i)) {
			r.Linked = append(r.Linked, l)
		} else {
			missed = append(missed, l)
		}
	}
	if len(missed) > 0 {
		steps = append(steps, "marking it blocked by "+issue.JoinLinked(missed, r.Def))
	}
	parentMissed := false
	if parent != nil {
		if effects.Answered(data, effects.AliasParent) {
			r.Parent, r.ParentSet = &parent.Ref, true
		} else {
			steps = append(steps, "making it a sub-issue of "+parent.Ref.String(r.Def))
			parentMissed = true
		}
	}
	if parentMissed && len(steps) == 1 && parentRefused(err, effects.AliasParent, false) {
		return &stepError{step: steps[0] + " failed", err: err, parent: true}
	}
	switch {
	case len(steps) == 0 && err != nil:
		r.warnings = append(r.warnings, "GitHub reported an error after applying every change: "+render.Reason(err))
	case len(steps) > 0 && err == nil:
		err = render.Network("GitHub returned no result for part of the write")
		fallthrough
	case len(steps) > 0:
		return &stepError{step: strings.Join(steps, " and ") + " failed", err: err}
	}
	return nil
}

// parentRefused reports whether err is GitHub refusing the --parent link
// for good, so that re-running cannot help: every GraphQL error of err is
// at field (the alias of the write), of type UNPROCESSABLE or FORBIDDEN, or
// an input error without a type; with named, its message must also name
// the parent or sub-issues (createIssue, whose other parts may fail too).
// A parent with as many sub-issues as GitHub allows, one 8 levels deep, a
// cycle, or no access to the parent's repository are such refusals.
//
// The words named looks for are a guess: GitHub's messages for these
// refusals of createIssue were not checked (they cannot be produced
// read-only). At worst a refusal they miss costs one more run: it is exit
// code 5, and the re-run finds the issue and sends addSubIssue, whose
// refusal is told by its path alone.
func parentRefused(err error, field string, named bool) bool {
	items := graph.ErrorItems(err)
	if errors.Is(err, graph.ErrUnknownOutcome) || len(items) == 0 {
		return false
	}
	for _, item := range items {
		if len(item.Path) == 0 || item.Path[0] != field {
			return false
		}
		switch item.Type {
		case "UNPROCESSABLE", "FORBIDDEN", "":
		default:
			return false
		}
		if named && !containsAnyFold(item.Message, "parent", "sub-issue", "sub issue", "subissue") {
			return false
		}
	}
	return true
}

// containsAnyFold reports whether s contains any of words, ignoring case.
func containsAnyFold(s string, words ...string) bool {
	s = strings.ToLower(s)
	return slices.ContainsFunc(words, func(w string) bool { return strings.Contains(s, w) })
}

// addToBoard puts the issue on the board projectID and sets r.itemID (and
// r.Status, when a re-read finds the item with one). A refusal that is not
// a lost answer is followed by one re-read of the issue's board items
// (RecheckDelay later): the item may be there all the same, added by
// GitHub or the board's automation.
func (r *Issue) addToBoard(ctx context.Context, projectID, statusField string) error {
	var out struct {
		Add *struct {
			Item *struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"addProjectV2ItemById"`
	}
	vars := map[string]any{"project": projectID, "issue": r.id}
	err := r.env.Client.Mutate(ctx, effects.AddToBoardMutation, vars, &out)
	if err == nil {
		if out.Add == nil || out.Add.Item == nil || out.Add.Item.ID == "" {
			return render.Internal("addProjectV2ItemById returned no item")
		}
		r.itemID, r.added = out.Add.Item.ID, true
		return nil
	}
	if errors.Is(err, graph.ErrUnknownOutcome) || ctx.Err() != nil {
		// A lost answer or an interrupted run: the re-run finds the item.
		return err
	}
	if !recheckPause(ctx) {
		return err
	}
	it := issue.ReadBoardItem(ctx, r.env.Client, r.id, statusField, projectID)
	if it == nil {
		return err
	}
	r.itemID = it.ID
	if it.Status != nil {
		r.Status = it.Status.Name
	}
	r.warnings = append(r.warnings, fmt.Sprintf("GitHub refused adding %s to the board, but a re-read found it there", r.Name()))
	return nil
}

// recheckPause waits RecheckDelay before a re-read; false when ctx ends
// first.
func recheckPause(ctx context.Context) bool {
	if RecheckDelay <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(RecheckDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// noIssueCreated is the error for a createIssue answer with no issue and
// no error: GitHub answers so when its limit on creating content is hit
// (seen on this repository, #67, while addComment got the limit's error).
// So it is exit code 4 with the limit's wait, not an internal error to
// re-run at once. Should GitHub have created the issue after all, a
// re-run finds it by its marker.
func noIssueCreated() *render.Error {
	e := render.ContentLimited("")
	e.What = "GitHub created no issue and gave no reason, as it does when its limit on creating issues and comments is hit"
	return e
}

// recheck re-reads the newest issues after a lost createIssue answer and
// returns the issue with marker, or nil when it is not there or the read
// fails.
func (r *Issue) recheck(ctx context.Context, repo issue.Repo, statusField, marker, projectID string) *issue.Created {
	if !recheckPause(ctx) {
		return nil
	}
	nc, err := issue.ReadNewContext(ctx, r.env.Client, r.contextRequest(repo, statusField, nil))
	if err != nil {
		return nil
	}
	return nc.Find(marker, projectID)
}

// withMarker appends a hidden marker to text as its last paragraph.
func withMarker(text, marker string) string {
	if text == "" {
		return marker
	}
	return fmt.Sprintf("%s\n\n%s", text, marker)
}

// create sends createIssue with what in resolved: the labels, the
// milestone and the parent unless nil, and the assignees. On an error with
// the issue in the payload (a part of the input GitHub refused) the issue
// exists: r.Number is set and the error is returned for a partial result.
func (r *Issue) create(ctx context.Context, repoID, body string, in input) error {
	milestone, parent, assignees := in.milestone, in.nc.Parent, in.assignees
	ids := make([]string, len(in.labels))
	for i, l := range in.labels {
		ids[i] = l.ID
	}
	var out struct {
		Create *struct {
			Issue *struct {
				ID     string `json:"id"`
				Number int    `json:"number"`
			} `json:"issue"`
		} `json:"createIssue"`
	}
	vars := map[string]any{
		"repo": repoID, "title": r.opts.Title, "body": body, "labels": ids,
	}
	if milestone != nil {
		vars["milestone"] = milestone.ID
	}
	if parent != nil {
		vars["parent"] = parent.ID
	}
	if len(assignees) > 0 {
		vars["assignees"] = userIDs(assignees)
	}
	err := r.env.Client.Mutate(ctx, effects.CreateIssueMutation, vars, &out)
	if out.Create == nil || out.Create.Issue == nil || out.Create.Issue.Number == 0 {
		if err == nil {
			err = noIssueCreated()
		}
		return err
	}
	is := out.Create.Issue
	r.Number, r.id = is.Number, is.ID
	// A parent GitHub refused for good leaves the issue with the rest: the
	// run goes on, and ends with this refusal (see apply).
	parentErr := err != nil && parent != nil && parentRefused(err, "createIssue", true)
	if err != nil && !parentErr {
		parts := []string{"labels"}
		if milestone != nil {
			parts = append(parts, "milestone")
		}
		if parent != nil {
			parts = append(parts, "parent")
		}
		if len(assignees) > 0 {
			parts = append(parts, "assignees")
		}
		return &stepError{step: "GitHub refused part of the request (" + render.JoinWords(parts, "or") + ")", err: err}
	}
	r.Milestone, r.MilestoneSet = milestone, milestone != nil
	if len(assignees) > 0 {
		r.Assigned = loginsOf(assignees)
	}
	switch {
	case parentErr:
		r.parentErr = err
	case parent != nil:
		r.Parent, r.ParentSet = &parent.Ref, true
	}
	return nil
}

// planDry fills the run's fields as applying plan would leave them, for a
// dry run: the issue created with the labels, milestone, parent and
// assignees, or the resumed one finished; on the board, unarchived, in the
// initial status unless a resumed issue has one, and blocked by the links
// it lacks (Linked) apart from those it has (LinkedFound).
func (r *Issue) planDry(plan *workflow.NewPlan, in input) {
	r.Assigned = slices.Clone(plan.Assign)
	if plan.Milestone != "" {
		r.Milestone, r.MilestoneSet = in.milestone, true
	}
	if plan.Parent {
		r.Parent, r.ParentSet = &in.nc.Parent.Ref, true
	}
	r.Linked, r.LinkedFound = linksOf(plan.Link), linksOf(plan.AlreadyLinked)
	r.added = plan.AddToBoard
	r.Unarchived = plan.Unarchive
	if plan.Status != "" {
		r.Status, r.StatusSet = plan.Status, true
	}
}

// Name is how output names the issue: "#57", or "owner/repo#57" outside
// the default repository.
func (r *Issue) Name() string { return issue.Ref{Repo: r.Repo, Number: r.Number}.String(r.Def) }

// parentName names a parent issue as output names issues; "?" for nil.
func (r *Issue) parentName(p *issue.Ref) string {
	if p == nil {
		return "?"
	}
	return p.String(r.Def)
}

// parentError is the error for a --parent issue that cannot be a parent,
// exit code 3: missing, or a pull request; nil for nil or an issue.
func parentError(p *issue.Linked, def issue.Repo) error {
	switch {
	case p == nil:
		return nil
	case p.PullRequest:
		return render.NotFound("--%s %s: a pull request, not an issue; only an issue can have sub-issues", workflow.NewParentFlag, p.Ref.String(def))
	}
	return issue.LinksError("--"+workflow.NewParentFlag, []*issue.Linked{p}, def)
}

// partial is the exit-code-5 error of a run that stopped after the issue
// existed: what failed, why, and that re-running finishes it. A cause with
// its own fix (auth, say) keeps it, followed by the re-run. A blocked-by
// link GitHub refused as a dependency cycle is exit code 1 instead: a
// re-run would fail the same way.
func (r *Issue) partial(err error) error {
	step, cause := "finishing it failed", err
	var se *stepError
	if errors.As(err, &se) {
		step, cause = se.step, se.err
	}
	what := "created"
	if r.Resumed {
		what = "found (created earlier)"
	}
	e := render.FromError(cause)
	why, _, _ := strings.Cut(e.What, "\n")
	if se != nil && se.parent {
		// Re-running cannot help a parent GitHub refuses: full, too deep, a
		// cycle, or out of the token's reach. GitHub's own words say which.
		items := graph.ErrorItems(cause)
		forbidden := false
		if len(items) > 0 {
			why = "GitHub: " + render.SingleLine(items[0].Message)
			forbidden = slices.ContainsFunc(items, func(i api.GraphQLErrorItem) bool { return i.Type == "FORBIDDEN" })
		}
		fix := "pass another --" + workflow.NewParentFlag + " to make it a sub-issue (gh issue edit " + strconv.Itoa(r.Number) +
			" -R " + r.Repo.String() + " --parent <issue>), or leave it without one"
		again := "; re-running will not help"
		if forbidden {
			// The token may lack access to the parent's repository.
			again = "; re-running with the same token will not help"
			fix = "if the token lacks access to the parent's repository: " + render.AuthFix +
				", then re-run the same command; else " + fix
		}
		return render.Usage("%s %s, but %s%s\n%s", r.Name(), what, step, again, why).
			WithFix(fix).WithCause(err)
	}
	if graph.IsCycle(cause) {
		// Nor a blocked-by link that makes a cycle through other issues.
		return render.Usage("%s %s, but %s\n%s", r.Name(), what, step, why).
			WithFix("gh dojo show " + strings.TrimPrefix(r.Name(), "#")).WithCause(err)
	}
	fix := fmt.Sprintf("re-run the same gh dojo new command: it finds %s and finishes it", r.Name())
	switch {
	case e.Wait != "": // a rate limit: its fix already says to wait, then re-run
		fix = "wait " + e.Wait + ", then " + fix
	case e.Fix != "" && e.Fix != render.InternalFix:
		fix = e.Fix + "; then re-run the same gh dojo new command"
	}
	return render.Partial("%s %s, but %s\n%s", r.Name(), what, step, why).WithFix(fix).WithCause(err)
}

// loginsOf returns the logins of users, as GitHub spells them.
func loginsOf(users []*issue.User) []string {
	out := make([]string, len(users))
	for i, u := range users {
		out[i] = u.Login
	}
	return out
}

// userIDs returns the node IDs of users, for a mutation.
func userIDs(users []*issue.User) []string {
	out := make([]string, len(users))
	for i, u := range users {
		out[i] = u.ID
	}
	return out
}
