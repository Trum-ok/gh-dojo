package workflow

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// gh dojo new's plan. The flow of new is: CheckNewFlags (no network), a
// read of the repository (issue.ReadNewContext), ResolveNewLabels and the
// other lookups, then CheckNew with the issue an earlier run created, if
// any, and PlanNew: what creating the issue, or finishing the one found,
// takes. An executor applies the plan, and a dry run shows it.

// NewMarkerVerb names new's marker: <!-- gh-dojo:new:<hash> -->.
const NewMarkerVerb = "new"

// The flags of new that its errors name.
const (
	NewBlockedByFlag = "blocked-by"
	NewParentFlag    = "parent"
	NewAssigneeFlag  = config.ArgAssignee
)

// MaxAssignees is how many assignees GitHub allows on an issue.
const MaxAssignees = 10

// newHelp is the fix of new's usage errors.
const newHelp = "gh dojo new --help"

// NewDedupKey is what new's marker hashes: the title with whitespace
// collapsed and case folded.
func NewDedupKey(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}

// NewMarker is the hidden marker that ends the body of the issue new
// creates with title: a repeat finds the issue by it, so filing the same
// title again (ignoring case and spacing) resumes it.
func NewMarker(title string) string { return Marker(NewMarkerVerb, NewDedupKey(title)) }

// NewLabels are the label flags of new: --type, --area and --label, as
// given.
type NewLabels struct {
	Type   string // "" when --type is not given
	Areas  []string
	Labels []string
}

// CheckNewFlags rejects, without a request, a --type outside labels.types
// and --area without labels.area_prefix.
func CheckNewFlags(cfg *config.Config, l NewLabels) error {
	if len(l.Areas) > 0 && cfg.Labels.AreaPrefix == "" {
		return cfg.Errorf("--area needs labels.area_prefix, which is not set; pass the label with --label instead").
			WithFix(newHelp)
	}
	if l.Type == "" || len(cfg.Labels.Types) == 0 {
		return nil
	}
	if slices.ContainsFunc(cfg.Labels.Types, func(t string) bool { return strings.EqualFold(t, l.Type) }) {
		return nil
	}
	return render.Usage("unknown type %q", l.Type).WithAllowed(cfg.Labels.Types...).WithFix(newHelp)
}

// ResolveNewLabels maps --type, --area and --label to labels of the
// repository repo ("owner/name"), in order and without duplicates, each
// named as the repository spells it. labels are the repository's label
// names read so far, by name; complete says they are all of them. A value
// not among them while !complete gives more: read all the labels and call
// again. An empty value, an unknown label, or (without labels.types) an
// area label given as --type is a usage error with the allowed values.
func ResolveNewLabels(cfg *config.Config, want NewLabels, labels []string, complete bool, repo string) (names []string, more bool, err error) {
	prefix := cfg.Labels.AreaPrefix
	type wanted struct {
		flag, value, name string
	}
	var wants []wanted
	if want.Type != "" {
		name := want.Type
		for _, t := range cfg.Labels.Types {
			if strings.EqualFold(t, want.Type) {
				name = t
			}
		}
		wants = append(wants, wanted{"type", want.Type, name})
	}
	for _, a := range want.Areas {
		wants = append(wants, wanted{"area", a, prefix + a})
	}
	for _, l := range want.Labels {
		wants = append(wants, wanted{"label", l, l})
	}
	for _, w := range wants {
		if strings.TrimSpace(w.value) == "" {
			return nil, false, render.Usage("--%s is empty", w.flag).WithFix(newHelp)
		}
		// GitHub matches label names ignoring case.
		i := slices.IndexFunc(labels, func(n string) bool { return strings.EqualFold(n, w.name) })
		if i < 0 && !complete {
			return nil, true, nil
		}
		if i < 0 {
			return nil, false, unknownNewLabel(cfg, repo, w.flag, w.value, w.name, labels)
		}
		name := labels[i]
		if w.flag == "type" && len(cfg.Labels.Types) == 0 && prefix != "" && hasPrefixFold(name, prefix) {
			return nil, false, render.Usage("--type %q is an area label; pass --area %s instead", w.value, name[len(prefix):]).
				WithFix(newHelp)
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names, false, nil
}

// unknownNewLabel is the error for a --type, --area or --label value with
// no matching label among all of the repository's.
func unknownNewLabel(cfg *config.Config, repo, flag, value, name string, all []string) error {
	prefix := cfg.Labels.AreaPrefix
	var allowed []string
	switch flag {
	case "type":
		if len(cfg.Labels.Types) > 0 {
			// The type is in labels.types (CheckNewFlags), so the repo lacks it.
			return cfg.Errorf("labels.types has %q, but %s has no such label", name, repo).
				WithFix(fmt.Sprintf("gh label create %q -R %s", name, repo))
		}
		for _, l := range all {
			if prefix == "" || !hasPrefixFold(l, prefix) {
				allowed = append(allowed, l)
			}
		}
	case "area":
		for _, l := range all {
			if prefix != "" && hasPrefixFold(l, prefix) {
				allowed = append(allowed, l[len(prefix):])
			}
		}
		if len(allowed) == 0 {
			return render.Usage("unknown area %q: %s has no labels starting with %q", value, repo, prefix).
				WithFix("gh label list -R " + repo)
		}
	default:
		allowed = all
	}
	return render.Usage("unknown %s %q: no such label in %s", flag, value, repo).WithAllowed(allowed...).
		WithFix("gh label list -R " + repo)
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// NewRequest is what new is asked to do, resolved against the repository:
// labels, milestone and logins as the repository spells them.
type NewRequest struct {
	Repo      string // the issue's repository, "owner/name"
	Labels    []string
	Milestone string // the open milestone to put it in; "" for none
	Assignees []string
	// Blockers are the --blocked-by issues, in order; Parent the --parent
	// one, nil without it.
	Blockers []NewLink
	Parent   *NewLink
	// Initial is the status to set (statuses.initial, as the board spells
	// it); "" for none.
	Initial string
}

// NewLink is a --blocked-by or --parent issue, as read. Its relations are
// to the issue an earlier run created (NewFound), all false without one.
type NewLink struct {
	Name string // as output names it: "#12", or "owner/repo#12"
	Arg  string // as an argument: "12", or "owner/repo#12"
	// EditArg is how gh issue edit --parent, run in the new issue's
	// repository, takes the parent: its number there, else its URL.
	EditArg string
	// SubIssues is how many sub-issues a parent has, and Full says it has
	// as many as GitHub allows.
	SubIssues int
	Full      bool

	Found     bool // it is the issue found
	Blocks    bool // it blocks the issue found: the link is in place
	BlockedBy bool // the issue found blocks it: linking would close a cycle
	ParentOf  bool // it is the parent of the issue found

	// Ref is opaque to this package: the executor keeps the issue it
	// links here.
	Ref any
}

// NewFound is the issue an earlier run created, found by its marker, or
// the one this run created, found by a re-read after GitHub's answer was
// lost (Rechecked).
type NewFound struct {
	Name      string // as output names it: "#57", or "owner/repo#57"
	Arg       string // as an argument: "57", or "owner/repo#57"
	Number    int
	Labels    []string
	Milestone string // its milestone's title, "" for none
	// Assignees are its assignees' logins; AssigneesTotal how many it has.
	Assignees      []string
	AssigneesTotal int
	OnBoard        bool
	Archived       bool
	Status         string // "" for none, or not on the board
	// Parent names its parent as output does, "" for none or one the
	// token cannot read (ParentHidden).
	Parent       string
	ParentHidden bool
	// Rechecked: this run created it, and found it again after GitHub's
	// answer to creating it was lost. It is finished, not resumed: its
	// status is set even if it has one.
	Rechecked bool
}

// resumed reports whether f is an earlier run's issue, which keeps a
// status it has.
func (f *NewFound) resumed() bool { return f != nil && !f.Rechecked }

// CheckNew refuses what no run can do, before anything is written: a
// --parent with as many sub-issues as GitHub allows, unless the issue
// found has a parent already; and for an issue an earlier run created,
// a --blocked-by issue that is the issue itself or that it blocks (a
// cycle), a --parent that is the issue itself, and assignees past
// GitHub's limit. All are exit code 1.
func CheckNew(req NewRequest, found *NewFound) error {
	if p := req.Parent; p != nil && p.Full && (found == nil || found.Parent == "" && !found.ParentHidden) {
		return render.Usage("--%s %s has %d sub-issues, as many as GitHub allows", NewParentFlag, p.Name, p.SubIssues).
			WithFix("pass another parent, or drop --" + NewParentFlag)
	}
	if found == nil {
		return nil
	}
	for _, l := range req.Blockers {
		switch {
		case l.Found:
			return SelfBlockError(found.Name, "--"+NewBlockedByFlag)
		case l.BlockedBy:
			return CycleError(found.Name, found.Arg, l.Name, "--"+NewBlockedByFlag)
		}
	}
	if p := req.Parent; p != nil && p.Found {
		return render.Usage("--%s names %s itself; an issue cannot be its own parent", NewParentFlag, found.Name).
			WithFix("pass the issue " + found.Name + " is a part of")
	}
	if missing, _ := found.missingAssignees(req.Assignees); len(missing) > 0 && found.AssigneesTotal+len(missing) > MaxAssignees {
		return render.Usage("%s has %d assignees; assigning %s would make more than the %d GitHub allows",
			found.Name, found.AssigneesTotal, join(missing, "and"), MaxAssignees).
			WithFix(fmt.Sprintf("unassign someone first (gh issue edit %d -R %s --remove-assignee <login>), or drop --%s",
				found.Number, req.Repo, NewAssigneeFlag))
	}
	return nil
}

// SelfBlockError is the error for a blocker that names the issue name
// itself, given with flag.
func SelfBlockError(name, flag string) error {
	return render.Usage("%s names %s itself; an issue cannot block itself", flag, name).
		WithFix(fmt.Sprintf("pass the issues that block %s, not %s itself", name, name))
}

// CycleError is the error for a blocker ref, given with flag, that the
// issue name (arg as an argument) already blocks: linking it would close
// a cycle, which GitHub refuses. gh dojo show lists the issue's links both
// ways.
func CycleError(name, arg, ref, flag string) error {
	return render.Usage("%s %s: %s already blocks %s, so %s cannot block %s too (a cycle)", flag, ref, name, ref, ref, name).
		WithFix("gh dojo show " + arg)
}

// missingAssignees splits logins into those f lacks, to assign, and those
// it has already, ignoring case.
func (f *NewFound) missingAssignees(logins []string) (missing, have []string) {
	for _, u := range logins {
		if slices.ContainsFunc(f.Assignees, func(l string) bool { return strings.EqualFold(l, u) }) {
			have = append(have, u)
		} else {
			missing = append(missing, u)
		}
	}
	return missing, have
}

// NewPlan is what new does: create the issue, or finish the one found.
// Its fields are the effects still to apply, apart from those found in
// place.
type NewPlan struct {
	// Create: no issue was found; createIssue sets Labels, Milestone,
	// Assign and Parent along with it.
	Create bool
	// Resumed: the issue was created by an earlier run, found by its
	// marker. It keeps a status it has, even one found only while adding
	// it to the board.
	Resumed bool

	Labels        []string // labels to add
	LabelsInPlace []string // labels the issue found has already
	Milestone     string   // the milestone to set, "" for none
	// MilestoneKept is the --milestone not applied: the issue found has
	// another milestone, which stays.
	MilestoneKept string

	Assign          []string // logins to assign
	AlreadyAssigned []string // logins the issue found has already

	AddToBoard bool
	Unarchive  bool
	Status     string // the status to set, "" to leave it alone

	Link          []NewLink // blocked-by links to add
	AlreadyLinked []NewLink // blocked-by links in place

	// Parent: make it a sub-issue of the --parent issue. ParentKept: the
	// issue found has another parent, which stays; ParentHidden says the
	// token cannot read it.
	Parent       bool
	ParentKept   bool
	ParentHidden bool

	// Warnings are for stderr, one line each: what of the request is not
	// applied, and the command that would apply it.
	Warnings []string
}

// PlanNew plans new for req: creating the issue, or, with found, finishing
// it. A resumed issue keeps the milestone, parent and status it has
// (someone may have set them on purpose), with a warning for a different
// milestone or parent; its assignees stay, --assignee only adds.
func PlanNew(cfg *config.Config, req NewRequest, found *NewFound) *NewPlan {
	p := &NewPlan{Resumed: found.resumed()}
	if found == nil {
		p.Create = true
		p.Labels, p.Milestone, p.Assign = req.Labels, req.Milestone, req.Assignees
		p.Parent = req.Parent != nil
		p.AddToBoard = true
		p.Status = req.Initial
		p.Link = req.Blockers
		return p
	}
	for _, l := range req.Labels {
		if slices.ContainsFunc(found.Labels, func(n string) bool { return strings.EqualFold(n, l) }) {
			p.LabelsInPlace = append(p.LabelsInPlace, l)
		} else {
			p.Labels = append(p.Labels, l)
		}
	}
	p.Assign, p.AlreadyAssigned = found.missingAssignees(req.Assignees)
	switch {
	case req.Milestone == "":
	case found.Milestone == "":
		p.Milestone = req.Milestone
	case found.Milestone != req.Milestone:
		p.MilestoneKept = req.Milestone
		p.Warnings = append(p.Warnings, fmt.Sprintf(
			"#%d already has milestone %q; kept it, --milestone %q not applied (to change it: %s)",
			found.Number, found.Milestone, req.Milestone, MilestoneFix(cfg, found, req.Repo, req.Milestone)))
	}
	if par := req.Parent; par != nil {
		switch {
		case par.ParentOf:
		case found.ParentHidden:
			// It has a parent the token cannot read: addSubIssue would
			// fail, or move it from a parent nobody here can see.
			p.ParentKept, p.ParentHidden = true, true
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"%s has a parent the token cannot read; kept it, --%s %s not applied (to see it: gh issue view %d -R %s)",
				found.Name, NewParentFlag, par.Arg, found.Number, req.Repo))
		case found.Parent == "":
			p.Parent = true
		default:
			p.ParentKept = true
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"%s is already a sub-issue of %s; kept it, --%s %s not applied (to move it: gh issue edit %d -R %s --parent %s)",
				found.Name, found.Parent, NewParentFlag, par.Arg, found.Number, req.Repo, par.EditArg))
		}
	}
	p.AddToBoard = !found.OnBoard
	p.Unarchive = found.OnBoard && found.Archived
	if !p.Resumed || found.Status == "" {
		p.Status = req.Initial
	}
	for _, l := range req.Blockers {
		if l.Blocks {
			p.AlreadyLinked = append(p.AlreadyLinked, l)
		} else {
			p.Link = append(p.Link, l)
		}
	}
	return p
}

// MilestoneFix is the command that puts the issue found, of repo
// ("owner/name"), in the milestone title: a verb that only sets the
// --milestone milestone (see OnlySetsMilestone) and starts from the
// issue's status (statuses.initial without one), else gh issue edit.
func MilestoneFix(cfg *config.Config, found *NewFound, repo, title string) string {
	status := found.Status
	if status == "" {
		status = cfg.Statuses.Initial
	}
	for _, v := range cfg.Verbs {
		if OnlySetsMilestone(v) && (len(v.From) == 0 || slices.Contains(v.From, status)) {
			return fmt.Sprintf("gh dojo %s %s --%s %q", v.Name, found.Arg, config.ArgMilestone, title)
		}
	}
	return fmt.Sprintf("gh issue edit %d -R %s --milestone %q", found.Number, repo, title)
}

// OnlySetsMilestone reports whether v does nothing but set the
// --milestone milestone: no status, comment, labels, assignees, close,
// reopen, sprint or fields; it needs nothing besides --milestone, not the
// user's go-ahead, and checks neither labels nor, to refuse, blockers or
// sub-issues. Such a verb is a safe way to change an issue's milestone.
func OnlySetsMilestone(v config.Verb) bool {
	return v.MilestoneArg && v.To == "" && v.Comment == "" && len(v.AddLabels)+len(v.RemoveLabels) == 0 &&
		!v.Assigns() && v.Unassign == "" && v.Close == "" && !v.Reopen && v.Iteration == "" && len(v.Fields) == 0 &&
		!v.Confirm && slices.Equal(v.Require, []string{config.ArgMilestone}) && len(v.RequireOneOf) == 0 &&
		!v.ChecksLabels() && v.Blockers != config.BlockersDeny && v.SubIssues != config.BlockersDeny
}
