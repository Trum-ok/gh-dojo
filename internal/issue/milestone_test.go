package issue_test

import (
	"errors"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// TestMatchMilestone: a title regardless of case (spelled exactly so
// first), else the only title it starts; several or none are usage errors
// with the candidates or the open milestones.
func TestMatchMilestone(t *testing.T) {
	open := []issue.OpenMilestone{
		{ID: "MI_1", Title: "v0.2"}, {ID: "MI_2", Title: "v0.3 — расширяемость"}, {ID: "MI_3", Title: "v0.30"},
		{ID: "MI_4", Title: "Backlog"}, {ID: "MI_5", Title: "BACKLOG"}, {ID: "MI_6", Title: "Ёлка"},
	}
	repo := issue.Repo{Owner: "Trum-ok", Name: "gh-dojo"}
	for _, tt := range []struct {
		title, want string // want: the ID matched, or the error text
	}{
		{"v0.2", "MI_1"},
		{"V0.2", "MI_1"},
		{"v0.3 — расширяемость", "MI_2"},
		{"V0.3 — РАСШИРЯЕМОСТЬ", "MI_2"},
		{"v0.3 ", "MI_2"},
		{"v0.30", "MI_3"},
		{"BACKLOG", "MI_5"}, // spelled exactly so first
		{"backlog", "MI_4"}, // else the first that folds
		{"ёл", "MI_6"},
		{"v0.3", "error: milestone \"v0.3\" is ambiguous: 2 open milestones in Trum-ok/gh-dojo start with it\n" +
			"allowed: v0.3 — расширяемость, v0.30\nfix: write more of the title, up to all of it"},
		{"v1", "error: unknown milestone \"v1\": no open milestone in Trum-ok/gh-dojo has that title or one starting with it\n" +
			"allowed: v0.2, v0.3 — расширяемость, v0.30, Backlog, BACKLOG, Ёлка\n" +
			"fix: if it is new, create it: gh api repos/Trum-ok/gh-dojo/milestones -f title=\"v1\""},
	} {
		m, err := issue.MatchMilestone(tt.title, open, repo)
		got := ""
		if err != nil {
			e := render.FromError(err)
			got = e.Text()
			if e.Code != render.ExitUsage {
				t.Errorf("%q: code %d, want %d", tt.title, e.Code, render.ExitUsage)
			}
			if errors.Is(err, issue.ErrAmbiguousMilestone) != (tt.title == "v0.3") {
				t.Errorf("%q: ambiguous %v", tt.title, errors.Is(err, issue.ErrAmbiguousMilestone))
			}
		} else {
			got = m.ID
		}
		if got != tt.want {
			t.Errorf("%q:\ngot  %s\nwant %s", tt.title, got, tt.want)
		}
	}
	// A whole title wins over a longer one it starts, in any case.
	both := []issue.OpenMilestone{{ID: "MI_31", Title: "v0.3.1"}, {ID: "MI_3", Title: "v0.3"}}
	for _, title := range []string{"v0.3", "V0.3"} {
		if m, err := issue.MatchMilestone(title, both, repo); err != nil || m.ID != "MI_3" {
			t.Errorf("%q among v0.3.1 and v0.3: %+v, %v; want v0.3", title, m, err)
		}
	}
	if m, err := issue.MatchMilestone("v0.3.", both, repo); err != nil || m.ID != "MI_31" {
		t.Errorf("%q: %+v, %v; want v0.3.1", "v0.3.", m, err)
	}
	for current, want := range map[string]bool{"v0.3 — расширяемость": true, "V0.3 — РАСШИРЯЕМОСТЬ": true, "v0.3": false, "": false} {
		if got := issue.SameMilestone(current, "v0.3 — расширяемость"); got != want {
			t.Errorf("SameMilestone(%q) = %v, want %v", current, got, want)
		}
	}
	_, err := issue.MatchMilestone("v1", nil, repo)
	want := "error: unknown milestone \"v1\": Trum-ok/gh-dojo has no open milestones\n" +
		"fix: if it is new, create it: gh api repos/Trum-ok/gh-dojo/milestones -f title=\"v1\""
	if got := render.FromError(err).Text(); got != want {
		t.Errorf("no milestones:\ngot  %s\nwant %s", got, want)
	}
}
