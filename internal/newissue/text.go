package newissue

import (
	"slices"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// Confirm is the confirmation line of a finished run:
//
//	#57 "Fix login redirect" created, labels bug, area:cli, no status → Backlog
//	#57 "Fix login redirect" created, milestone v1.2, no status → Backlog, blocked by #12 and #13
//	#57 "Fix login redirect" already created, labels bug, unarchived, Backlog (unchanged)
//	#57 "Fix login redirect" already created, milestone v1.1 (kept, not v1.2), Backlog (unchanged)
//	#57 "Fix login redirect" created, no status → Backlog, sub-issue of #40
//	#57 "Fix login redirect" already created, assigned monalisa, already assigned octocat, Backlog (unchanged)
func (r *Issue) Confirm() string {
	done := []string{"created"}
	if r.Resumed {
		done[0] = "already created"
	}
	if len(r.Labels) > 0 {
		done = append(done, "labels "+strings.Join(r.Labels, ", "))
	}
	if len(r.Assigned) > 0 {
		done = append(done, "assigned "+render.JoinWords(r.Assigned, "and"))
	}
	if len(r.AssignedFound) > 0 {
		done = append(done, "already assigned "+render.JoinWords(r.AssignedFound, "and"))
	}
	if r.Milestone != nil {
		m := "milestone " + render.SingleLine(r.Milestone.Title)
		if r.milestoneKept != "" {
			m += " (kept, not " + render.SingleLine(r.milestoneKept) + ")"
		}
		done = append(done, m)
	}
	if r.Unarchived {
		done = append(done, "unarchived")
	}
	switch {
	case r.StatusSet:
		done = append(done, render.Transition("", r.Status))
	case r.Status != "":
		done = append(done, render.Transition(r.Status, r.Status))
	case r.noInitial:
		done = append(done, "on the board with no status (statuses.initial is not set)")
	}
	if len(r.Linked) > 0 {
		done = append(done, "blocked by "+issue.JoinLinked(r.Linked, r.Def))
	}
	if len(r.LinkedFound) > 0 {
		done = append(done, "already blocked by "+issue.JoinLinked(r.LinkedFound, r.Def))
	}
	switch {
	case r.opts.Parent == nil:
	case r.parentKept != nil:
		of := r.parentName(r.Parent)
		if r.parentHidden {
			of = "a parent not readable"
		}
		done = append(done, "sub-issue of "+of+" (kept, not "+r.parentKept.String(r.Def)+")")
	case r.ParentSet:
		done = append(done, "sub-issue of "+r.parentName(r.Parent))
	case r.Parent != nil:
		done = append(done, "already a sub-issue of "+r.parentName(r.Parent))
	}
	return render.Confirm(r.Name(), r.Title(), done...)
}

// DryRunText is the plan a dry run prints, from the fields planDry
// filled:
//
//	dry run: new "Fix login redirect"
//	  create: in Trum-ok/gh-dojo
//	  labels: bug, area:cli
//	  assignees: octocat and monalisa
//	  milestone: v1.2
//	  add to the board
//	  status: no status → Backlog
//	  blocked by: #12 and #13
//	  sub-issue of: #40
//
// and for an issue an earlier run created, what finishing it would do:
//
//	dry run: new #57 "Fix login redirect"
//	  create: already created by an earlier run; finishes it
//	  labels: +area:cli (bug already in place)
//	  assignees: +monalisa (octocat already assigned)
//	  milestone: v1.1 kept, not v1.2
//	  status: Backlog (unchanged)
//	  blocked by: #13 (#12 already linked)
func (r *Issue) DryRunText() string {
	var b strings.Builder
	line := func(s string) { b.WriteString("\n  " + s) }
	if r.Resumed {
		b.WriteString(render.Confirm("dry run: new "+r.Name(), r.title))
		line("create: already created by an earlier run; finishes it")
	} else {
		b.WriteString(render.Confirm("dry run: new", r.opts.Title))
		line("create: in " + r.Repo.String())
	}
	inPlace := len(r.Labels) - len(r.labelsMissing)
	switch {
	case !r.Resumed && len(r.Labels) > 0:
		line("labels: " + strings.Join(r.Labels, ", "))
	case len(r.labelsMissing) > 0 && inPlace > 0:
		var have []string
		for _, l := range r.Labels {
			if !slices.Contains(r.labelsMissing, l) {
				have = append(have, l)
			}
		}
		line("labels: +" + strings.Join(r.labelsMissing, " +") + " (" + strings.Join(have, ", ") + " already in place)")
	case len(r.labelsMissing) > 0:
		line("labels: +" + strings.Join(r.labelsMissing, " +"))
	case len(r.Labels) > 0:
		line("labels: " + strings.Join(r.Labels, ", ") + " already in place")
	}
	switch {
	case !r.Resumed && len(r.Assigned) > 0:
		line("assignees: " + render.JoinWords(r.Assigned, "and"))
	case len(r.Assigned) > 0 && len(r.AssignedFound) > 0:
		line("assignees: +" + strings.Join(r.Assigned, " +") + " (" + render.JoinWords(r.AssignedFound, "and") + " already assigned)")
	case len(r.Assigned) > 0:
		line("assignees: +" + strings.Join(r.Assigned, " +"))
	case len(r.AssignedFound) > 0:
		line("assignees: " + render.JoinWords(r.AssignedFound, "and") + " already assigned")
	}
	switch {
	case r.milestoneKept != "" && r.Milestone != nil:
		line("milestone: " + render.SingleLine(r.Milestone.Title) + " kept, not " + render.SingleLine(r.milestoneKept))
	case r.MilestoneSet && r.Resumed:
		line("milestone: none → " + render.SingleLine(r.Milestone.Title))
	case r.MilestoneSet:
		line("milestone: " + render.SingleLine(r.Milestone.Title))
	case r.opts.Milestone != "" && r.Milestone != nil:
		line("milestone: " + render.SingleLine(r.Milestone.Title) + " already set")
	}
	if r.added {
		line(workflow.AddToBoard{}.Describe())
	}
	if r.Unarchived {
		line(workflow.Unarchive{}.Describe())
	}
	switch {
	case r.StatusSet:
		line("status: " + render.Transition("", r.Status))
	case r.Status != "":
		line("status: " + render.Transition(r.Status, r.Status))
	case r.noInitial:
		line("status: none set (statuses.initial is not set)")
	}
	switch {
	case len(r.Linked) > 0 && len(r.LinkedFound) > 0:
		line("blocked by: " + issue.JoinLinked(r.Linked, r.Def) + " (" + issue.JoinLinked(r.LinkedFound, r.Def) + " already linked)")
	case len(r.Linked) > 0:
		line("blocked by: " + issue.JoinLinked(r.Linked, r.Def))
	case len(r.LinkedFound) > 0:
		line("blocked by: " + issue.JoinLinked(r.LinkedFound, r.Def) + " already linked")
	}
	switch {
	case r.opts.Parent == nil:
	case r.parentKept != nil && r.parentHidden:
		line("sub-issue of: a parent not readable, kept, not " + r.parentKept.String(r.Def))
	case r.parentKept != nil:
		line("sub-issue of: " + r.parentName(r.Parent) + " kept, not " + r.parentKept.String(r.Def))
	case r.ParentSet:
		line("sub-issue of: " + r.parentName(r.Parent))
	case r.Parent != nil:
		line("sub-issue of: " + r.parentName(r.Parent) + " already")
	}
	return b.String()
}
