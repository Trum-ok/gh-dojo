package issue

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// milestonesQuery reads all open milestones of a repository, oldest first;
// only needed past the first 100, which MilestonesSelection reads with
// other fields.
const milestonesQuery = `query RepoMilestones($owner: String!, $repo: String!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    milestones(first: 100, after: $cursor, states: OPEN, orderBy: {field: NUMBER, direction: ASC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes { id title dueOn }
    }
  }
}`

// ReadMilestones reads the open milestones of repo after the cursor after,
// oldest first, page by page: for a repository with more than
// MilestonesSelection reads, after is that page's endCursor, and the
// result its continuation. "" reads them all.
func ReadMilestones(ctx context.Context, q graph.Querier, repo Repo, after string) ([]OpenMilestone, error) {
	type resp struct {
		Repository *struct {
			Milestones graph.Connection[OpenMilestone] `json:"milestones"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": repo.Owner, "repo": repo.Name}
	if after != "" {
		vars["cursor"] = after
	}
	page, err := graph.Paginate(ctx, q, milestonesQuery, vars, 0, func(r *resp) *graph.Connection[OpenMilestone] {
		if r.Repository == nil {
			return nil
		}
		return &r.Repository.Milestones
	})
	if err != nil {
		return nil, err
	}
	return page.Nodes, nil
}

// ErrAmbiguousMilestone is the cause of MatchMilestone's error for a title
// that several open milestones' titles start with; test for it with
// errors.Is.
var ErrAmbiguousMilestone = errors.New("ambiguous milestone")

// MilestoneKeyError is err of MatchMilestone for a milestone of the
// config, at key: it names the key, and its fix says to change it.
func MilestoneKeyError(key string, err error) *render.Error {
	e := render.FromError(err)
	fix := fmt.Sprintf("%s, or fix %s", e.Fix, key)
	if errors.Is(err, ErrAmbiguousMilestone) {
		fix = "write more of the title in " + key
	}
	return render.Usage("%s: %s", key, e.What).WithAllowed(e.Allowed...).WithFix(fix).WithCause(err)
}

// SameMilestone reports whether title, as given to a verb or new, names
// the milestone titled current exactly, regardless of case: an issue in
// that milestone has it already, even once the milestone is closed and
// MatchMilestone no longer finds it among the open ones.
func SameMilestone(current, title string) bool {
	return current != "" && strings.EqualFold(current, title)
}

// MatchMilestone returns the milestone of open, the open milestones of
// repo, that title names: the one titled so regardless of case (spelled
// exactly so first), else the only one whose title starts with title
// regardless of case, so "v0.3" names "v0.3 — extensibility". Several
// such are an error listing them (cause ErrAmbiguousMilestone); none is an
// error listing the open milestones, the closest one suggested, with how
// to create it. Both are usage errors (exit code 1); a caller may reword
// the message and fix for where the title comes from.
//
//	error: milestone "v0" is ambiguous: 2 open milestones in acme/shop start with it
//	allowed: v0.3, v0.4
//	fix: write more of the title, up to all of it
//
//	error: unknown milestone "v9": no open milestone in acme/shop has that title or one starting with it
//	allowed: v0.3, v0.4
//	fix: if it is new, create it: gh api repos/acme/shop/milestones -f title="v9"
func MatchMilestone(title string, open []OpenMilestone, repo Repo) (*OpenMilestone, error) {
	var fold *OpenMilestone
	var prefix []int
	for i := range open {
		t := open[i].Title
		switch {
		case t == title:
			return &open[i], nil
		case fold == nil && strings.EqualFold(t, title):
			fold = &open[i]
		case render.HasPrefixFold(t, title):
			prefix = append(prefix, i)
		}
	}
	switch {
	case fold != nil:
		return fold, nil
	case len(prefix) == 1:
		return &open[prefix[0]], nil
	case len(prefix) > 1:
		titles := make([]string, len(prefix))
		for i, k := range prefix {
			titles[i] = open[k].Title
		}
		return nil, render.Usage("milestone %q is ambiguous: %d open milestones in %s start with it", title, len(prefix), repo).
			WithAllowed(titles...).WithFix("write more of the title, up to all of it").WithCause(ErrAmbiguousMilestone)
	}
	create := fmt.Sprintf("if it is new, create it: gh api repos/%s/milestones -f title=%q", repo, title)
	if len(open) == 0 {
		return nil, render.Usage("unknown milestone %q: %s has no open milestones", title, repo).WithFix(create)
	}
	titles := make([]string, len(open))
	for i, m := range open {
		titles[i] = m.Title
	}
	msg := fmt.Sprintf("unknown milestone %q: no open milestone in %s has that title or one starting with it", title, repo)
	if s := board.Suggest(title, titles); s != "" {
		msg += fmt.Sprintf("; did you mean %q?", s)
	}
	return nil, render.Usage("%s", msg).WithAllowed(titles...).WithFix(create)
}
