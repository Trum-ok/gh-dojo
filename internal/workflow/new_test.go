package workflow

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// newConfig is a kanban config for new: area labels, no labels.types,
// statuses.initial Backlog, and a verb that only sets the milestone,
// from In progress.
const newConfig = `
version = 1
preset = "kanban"

[board]
owner = "acme"
project = 3

[verbs.move]
when = "The issue moves to another milestone"
from = ["In progress"]
milestone = "{milestone}"
`

func parseNewConfig(t *testing.T, extra string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(newConfig+extra), ".github/dojo.toml")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// usage asserts err is a usage error (exit code 1) whose text has want.
func usage(t *testing.T, err error, want string) {
	t.Helper()
	var e *render.Error
	switch {
	case err == nil:
		t.Fatalf("no error, want one with %q", want)
	case !errors.As(err, &e) || e.Code != render.ExitUsage:
		t.Fatalf("err = %v, want exit code 1", err)
	case !strings.Contains(e.What, want) && !strings.Contains(e.Fix, want):
		t.Fatalf("err = %q (fix %q), want %q in it", e.What, e.Fix, want)
	}
}

func TestNewMarker(t *testing.T) {
	a := NewMarker("Fix  login\tredirect")
	if b := NewMarker(" fix LOGIN redirect "); a != b {
		t.Errorf("markers differ by case and spacing: %q, %q", a, b)
	}
	if a == NewMarker("Fix login redirects") {
		t.Errorf("another title has the same marker %q", a)
	}
	if !strings.HasPrefix(a, "<!-- gh-dojo:new:") {
		t.Errorf("marker = %q", a)
	}
}

func TestCheckNewFlags(t *testing.T) {
	c := parseNewConfig(t, "")
	if err := CheckNewFlags(c, NewLabels{Type: "anything", Areas: []string{"cli"}}); err != nil {
		t.Errorf("--type is checked against the repository, not offline: %v", err)
	}
	noAreas := parseNewConfig(t, "")
	noAreas.Labels.AreaPrefix = ""
	usage(t, CheckNewFlags(noAreas, NewLabels{Areas: []string{"cli"}}), "--area needs labels.area_prefix")
}

// TestResolveNewLabels: the labels as the repository spells them, in flag
// order and once each; a label beyond the labels read asks for all of
// them, and only an empty value before it is refused without that.
func TestResolveNewLabels(t *testing.T) {
	c := parseNewConfig(t, "")
	repo := "acme/api"
	page := []string{"Bug", "area:cli", "docs"}
	names, more, err := ResolveNewLabels(c, NewLabels{Type: "bug", Areas: []string{"CLI"}, Labels: []string{"docs", "bug"}}, page, false, repo)
	if err != nil || more {
		t.Fatalf("ResolveNewLabels = %v, more %v", err, more)
	}
	if want := []string{"Bug", "area:cli", "docs"}; !slices.Equal(names, want) {
		t.Errorf("names = %q, want %q", names, want)
	}

	if _, more, err := ResolveNewLabels(c, NewLabels{Labels: []string{"docs", "wontfix"}}, page, false, repo); err != nil || !more {
		t.Errorf("a label past the page: more = %v, err = %v; want more", more, err)
	}
	_, _, err = ResolveNewLabels(c, NewLabels{Labels: []string{"", "wontfix"}}, page, false, repo)
	usage(t, err, "--label is empty")

	_, _, err = ResolveNewLabels(c, NewLabels{Labels: []string{"wontfix"}}, page, true, repo)
	usage(t, err, `unknown label "wontfix": no such label in acme/api`)
	_, _, err = ResolveNewLabels(c, NewLabels{Areas: []string{"web"}}, page, true, repo)
	usage(t, err, `unknown area "web"`)
	_, _, err = ResolveNewLabels(c, NewLabels{Areas: []string{"web"}}, []string{"bug"}, true, repo)
	usage(t, err, `acme/api has no labels starting with "area:"`)
	_, _, err = ResolveNewLabels(c, NewLabels{Type: "area:cli"}, page, true, repo)
	usage(t, err, "pass --area cli instead")

	_, _, err = ResolveNewLabels(c, NewLabels{Areas: []string{"area:web"}}, page, true, repo)
	usage(t, err, `unknown area "area:web"`)
}

// TestResolveNewLabelsTypePrefix: with labels.type_prefix, --type names the
// label with the prefix, given with or without it, and an unknown one
// lists the types the repository has.
func TestResolveNewLabelsTypePrefix(t *testing.T) {
	c := parseNewConfig(t, "")
	c.Labels.TypePrefix = "type: "
	repo := "acme/api"
	all := []string{"Type: Bug", "type: feature", "bug", "area:cli"}
	for _, want := range []string{"bug", "BUG", "type: bug", "TYPE: bug"} {
		names, _, err := ResolveNewLabels(c, NewLabels{Type: want, Areas: []string{"area:cli"}}, all, true, repo)
		if err != nil || !slices.Equal(names, []string{"Type: Bug", "area:cli"}) {
			t.Errorf("--type %q: names = %q, err = %v", want, names, err)
		}
	}
	_, _, err := ResolveNewLabels(c, NewLabels{Type: "chore"}, all, true, repo)
	var e *render.Error
	if !errors.As(err, &e) || !slices.Equal(e.Allowed, []string{"Bug", "feature"}) {
		t.Errorf("unknown type: err = %v, want allowed Bug, feature", err)
	}
	_, _, err = ResolveNewLabels(c, NewLabels{Type: "bug"}, []string{"bug"}, true, repo)
	usage(t, err, `acme/api has no labels starting with "type: "`)
}

// TestResolveNewLabelsAllowed: an unknown --type lists the labels outside
// the areas, an unknown --area the areas.
func TestResolveNewLabelsAllowed(t *testing.T) {
	c := parseNewConfig(t, "")
	all := []string{"bug", "area:cli", "area:docs", "docs"}
	for _, tt := range []struct {
		want    NewLabels
		allowed []string
	}{
		{NewLabels{Type: "chore"}, []string{"bug", "docs"}},
		{NewLabels{Areas: []string{"web"}}, []string{"cli", "docs"}},
		{NewLabels{Labels: []string{"chore"}}, all},
	} {
		_, _, err := ResolveNewLabels(c, tt.want, all, true, "acme/api")
		var e *render.Error
		if !errors.As(err, &e) || !slices.Equal(e.Allowed, tt.allowed) {
			t.Errorf("%+v: err = %v, want allowed %q", tt.want, err, tt.allowed)
		}
	}
}

// newRequest asks for labels bug and area:cli, assignees octocat and
// monalisa, milestone v1.2, blockers #12 and #13 and the parent #40.
func newRequest() NewRequest {
	return NewRequest{
		Repo: "acme/api", Labels: []string{"bug", "area:cli"}, Milestone: "v1.2",
		Assignees: []string{"octocat", "monalisa"},
		Blockers:  []NewLink{{Name: "#12", Arg: "12"}, {Name: "#13", Arg: "13"}},
		Parent:    &NewLink{Name: "#40", Arg: "40", EditArg: "40", SubIssues: 3},
		Initial:   "Backlog",
	}
}

func newFound() *NewFound {
	return &NewFound{Name: "#57", Arg: "57", Number: 57, OnBoard: true}
}

func TestCheckNew(t *testing.T) {
	if err := CheckNew(newRequest(), nil); err != nil {
		t.Errorf("CheckNew = %v", err)
	}
	if err := CheckNew(newRequest(), newFound()); err != nil {
		t.Errorf("CheckNew found = %v", err)
	}
	full := newRequest()
	full.Parent.SubIssues, full.Parent.Full = 100, true
	usage(t, CheckNew(full, nil), "--parent #40 has 100 sub-issues, as many as GitHub allows")
	withParent := newFound()
	withParent.Parent = "#39"
	if err := CheckNew(full, withParent); err != nil {
		t.Errorf("a full parent with the issue found keeping its own: %v", err)
	}
	hidden := newFound()
	hidden.ParentHidden = true
	if err := CheckNew(full, hidden); err != nil {
		t.Errorf("a full parent with the issue found keeping a hidden one: %v", err)
	}

	self := newRequest()
	self.Blockers[1].Found = true
	usage(t, CheckNew(self, newFound()), "--blocked-by names #57 itself")
	cycle := newRequest()
	cycle.Blockers[0].BlockedBy = true
	usage(t, CheckNew(cycle, newFound()), "--blocked-by #12: #57 already blocks #12")
	ownParent := newRequest()
	ownParent.Parent.Found = true
	usage(t, CheckNew(ownParent, newFound()), "--parent names #57 itself")

	many := newFound()
	many.Assignees, many.AssigneesTotal = []string{"OCTOCAT"}, 10
	usage(t, CheckNew(newRequest(), many), "#57 has 10 assignees; assigning monalisa would make more than the 10")
	many.AssigneesTotal = 9
	if err := CheckNew(newRequest(), many); err != nil {
		t.Errorf("10 assignees in all: %v", err)
	}
}

// TestPlanNewCreate: with no issue found, everything goes into creating
// it, then the board, the status and the links.
func TestPlanNewCreate(t *testing.T) {
	req := newRequest()
	p := PlanNew(parseNewConfig(t, ""), req, nil)
	switch {
	case !p.Create || p.Resumed:
		t.Errorf("Create %v, Resumed %v", p.Create, p.Resumed)
	case !slices.Equal(p.Labels, req.Labels) || p.Milestone != "v1.2" || !slices.Equal(p.Assign, req.Assignees) || !p.Parent:
		t.Errorf("created with labels %q, milestone %q, assignees %q, parent %v", p.Labels, p.Milestone, p.Assign, p.Parent)
	case !p.AddToBoard || p.Unarchive || p.Status != "Backlog":
		t.Errorf("board %v, unarchive %v, status %q", p.AddToBoard, p.Unarchive, p.Status)
	case len(p.Link) != 2 || len(p.AlreadyLinked) != 0 || len(p.Warnings) != 0:
		t.Errorf("links %v, already %v, warnings %q", p.Link, p.AlreadyLinked, p.Warnings)
	}
}

// TestPlanNewResume: an issue an earlier run created gets only what it
// lacks, and keeps its status.
func TestPlanNewResume(t *testing.T) {
	req := newRequest()
	req.Blockers[0].Blocks = true
	req.Parent.ParentOf = true
	f := newFound()
	f.Labels, f.Assignees, f.Milestone, f.Status, f.Parent = []string{"BUG"}, []string{"monalisa"}, "v1.2", "In progress", "#40"
	p := PlanNew(parseNewConfig(t, ""), req, f)
	switch {
	case p.Create || !p.Resumed:
		t.Errorf("Create %v, Resumed %v", p.Create, p.Resumed)
	case !slices.Equal(p.Labels, []string{"area:cli"}):
		t.Errorf("labels %q", p.Labels)
	case !slices.Equal(p.Assign, []string{"octocat"}) || !slices.Equal(p.AlreadyAssigned, []string{"monalisa"}):
		t.Errorf("assign %q, already %q", p.Assign, p.AlreadyAssigned)
	case p.Milestone != "" || p.MilestoneKept != "" || p.Parent || p.ParentKept:
		t.Errorf("milestone %q (kept %q), parent %v (kept %v)", p.Milestone, p.MilestoneKept, p.Parent, p.ParentKept)
	case p.AddToBoard || p.Unarchive || p.Status != "":
		t.Errorf("board %v, unarchive %v, status %q", p.AddToBoard, p.Unarchive, p.Status)
	case len(p.Link) != 1 || p.Link[0].Name != "#13" || len(p.AlreadyLinked) != 1 || p.AlreadyLinked[0].Name != "#12":
		t.Errorf("links %v, already %v", p.Link, p.AlreadyLinked)
	case len(p.Warnings) != 0:
		t.Errorf("warnings %q", p.Warnings)
	}

	f = newFound()
	f.OnBoard, f.Archived = true, true
	if p := PlanNew(parseNewConfig(t, ""), req, f); !p.Unarchive || p.AddToBoard || p.Status != "Backlog" || p.Milestone != "v1.2" {
		t.Errorf("archived, no status: unarchive %v, board %v, status %q, milestone %q", p.Unarchive, p.AddToBoard, p.Status, p.Milestone)
	}
	f = newFound()
	f.OnBoard = false
	if p := PlanNew(parseNewConfig(t, ""), req, f); !p.AddToBoard || p.Unarchive {
		t.Errorf("not on the board: board %v, unarchive %v", p.AddToBoard, p.Unarchive)
	}
}

// TestPlanNewRechecked: the issue this run created, found again after a
// lost answer, is finished, not resumed: its status is set even if it has
// one.
func TestPlanNewRechecked(t *testing.T) {
	f := newFound()
	f.Status, f.Rechecked = "In progress", true
	p := PlanNew(parseNewConfig(t, ""), newRequest(), f)
	if p.Create || p.Resumed || p.Status != "Backlog" {
		t.Errorf("Create %v, Resumed %v, status %q", p.Create, p.Resumed, p.Status)
	}
}

// TestPlanNewKept: a resumed issue keeps another milestone or parent it
// has, with a warning naming the command that would change it.
func TestPlanNewKept(t *testing.T) {
	c := parseNewConfig(t, "")
	f := newFound()
	f.Milestone, f.Status, f.Parent = "v1.1", "In progress", "acme/web#7"
	p := PlanNew(c, newRequest(), f)
	if p.MilestoneKept != "v1.2" || p.Milestone != "" || !p.ParentKept || p.ParentHidden || p.Parent {
		t.Errorf("milestone %q kept %q, parent %v kept %v hidden %v", p.Milestone, p.MilestoneKept, p.Parent, p.ParentKept, p.ParentHidden)
	}
	want := []string{
		`#57 already has milestone "v1.1"; kept it, --milestone "v1.2" not applied (to change it: gh dojo move 57 --milestone "v1.2")`,
		"#57 is already a sub-issue of acme/web#7; kept it, --parent 40 not applied (to move it: gh issue edit 57 -R acme/api --parent 40)",
	}
	if !slices.Equal(p.Warnings, want) {
		t.Errorf("warnings =\n%q\nwant\n%q", p.Warnings, want)
	}

	f = newFound()
	f.Milestone, f.ParentHidden = "v1.1", true
	p = PlanNew(c, newRequest(), f)
	want = []string{
		`#57 already has milestone "v1.1"; kept it, --milestone "v1.2" not applied (to change it: gh issue edit 57 -R acme/api --milestone "v1.2")`,
		"#57 has a parent the token cannot read; kept it, --parent 40 not applied (to see it: gh issue view 57 -R acme/api)",
	}
	if !p.ParentKept || !p.ParentHidden || !slices.Equal(p.Warnings, want) {
		t.Errorf("kept %v, hidden %v, warnings =\n%q\nwant\n%q", p.ParentKept, p.ParentHidden, p.Warnings, want)
	}

	f = newFound()
	if p := PlanNew(c, newRequest(), f); !p.Parent || p.Milestone != "v1.2" {
		t.Errorf("without a parent or milestone: parent %v, milestone %q", p.Parent, p.Milestone)
	}
}

// TestPlanNewKeptOtherRepo: the milestone warning names the issue as
// output does, with its repository when it is not the default one.
func TestPlanNewKeptOtherRepo(t *testing.T) {
	f := newFound()
	f.Name, f.Milestone = "acme/other#57", "v1.1"
	req := newRequest()
	req.Repo = "acme/other"
	p := PlanNew(parseNewConfig(t, ""), req, f)
	if len(p.Warnings) == 0 || !strings.HasPrefix(p.Warnings[0], `acme/other#57 already has milestone "v1.1"`) {
		t.Errorf("warnings = %q", p.Warnings)
	}
}

// TestOnlySetsMilestone: a verb that sets the --milestone milestone and
// nothing else; one that also comments, or needs --yes, is not.
func TestOnlySetsMilestone(t *testing.T) {
	c := parseNewConfig(t, `
[verbs.plan]
when = "Plan it"
milestone = "{milestone}"
comment = "Moved to {milestone}."

[verbs.careful]
when = "Plan it, carefully"
milestone = "{milestone}"
confirm = "user"
`)
	got := map[string]bool{}
	for _, v := range c.Verbs {
		if v.MilestoneArg {
			got[v.Name] = OnlySetsMilestone(v)
		}
	}
	if !got["move"] || got["plan"] || got["careful"] {
		t.Errorf("OnlySetsMilestone = %v, want only move", got)
	}
	f := newFound()
	f.Status = "Backlog" // move starts only from In progress
	if fix := milestoneFix(c, f, "acme/api", "v2"); fix != `gh issue edit 57 -R acme/api --milestone "v2"` {
		t.Errorf("milestoneFix = %q", fix)
	}
}
