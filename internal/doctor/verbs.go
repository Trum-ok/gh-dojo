package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// verbsCheck is what checkVerbs asks GitHub about the verbs: label names
// exactly as written, logins to assign (config.Me as the viewer's).
type verbsCheck struct {
	// labels are the labels verbs add and remove, each spelling once, as
	// a verb looks them up by their exact names; then those that
	// from_labels and refuse_labels check and no other entry names in any
	// case, as the checks match names regardless of case. Config order.
	labels []string
	users  []string          // distinct regardless of case
	alias  map[string]string // label name or lowercase login → alias
	// milestones says a verb names a milestone in the config (not
	// "{milestone}"): the open milestones are read.
	milestones bool
}

func newVerbsCheck(cfg *config.Config, viewer string) *verbsCheck {
	c := &verbsCheck{alias: map[string]string{}}
	add := func(name string) {
		c.alias[name] = fmt.Sprintf("label%d", len(c.labels))
		c.labels = append(c.labels, name)
	}
	for _, v := range cfg.Verbs {
		for _, name := range slices.Concat(v.AddLabels, v.RemoveLabels) {
			if _, ok := c.alias[name]; !ok {
				add(name)
			}
		}
	}
	for _, v := range cfg.Verbs {
		for _, name := range slices.Concat(v.FromLabels, v.RefuseLabels) {
			if !slices.ContainsFunc(c.labels, func(l string) bool { return strings.EqualFold(l, name) }) {
				add(name)
			}
		}
	}
	for _, v := range cfg.Verbs {
		c.milestones = c.milestones || v.Milestone != ""
		for _, login := range verbUsers(v) {
			if login.name == config.Me {
				login.name = viewer
			}
			if key := "@" + strings.ToLower(login.name); login.name != "" && c.alias[key] == "" {
				c.alias[key] = fmt.Sprintf("user%d", len(c.users))
				c.users = append(c.users, login.name)
			}
		}
	}
	return c
}

// verbUser is a user a verb's config names, with its key: "assign" or
// "unassign".
type verbUser struct{ key, name string }

// verbUsers are the users v assigns and unassigns by login or "@me";
// --assignee and unassign = "all" name nobody to check.
func verbUsers(v config.Verb) []verbUser {
	var out []verbUser
	if v.Assign != "" {
		out = append(out, verbUser{"assign", v.Assign})
	}
	if v.Unassign != "" && v.Unassign != config.UnassignAll {
		out = append(out, verbUser{"unassign", v.Unassign})
	}
	return out
}

func (c *verbsCheck) empty() bool { return len(c.labels) == 0 && len(c.users) == 0 && !c.milestones }

// query is the DoctorVerbs query: the first 100 labels of the repository
// for their spelling, each label by its exact name, the assignable users
// matching each login, and the first 100 open milestones when a verb
// names one.
func (c *verbsCheck) query(repo issue.Repo) (string, map[string]any) {
	params := []string{"$owner: String!", "$name: String!"}
	fields := []string{"    labels(first: 100) { nodes { name } }"}
	vars := map[string]any{"owner": repo.Owner, "name": repo.Name}
	for _, name := range c.labels {
		alias := c.alias[name]
		params = append(params, "$"+alias+": String!")
		fields = append(fields, fmt.Sprintf("    %s: label(name: $%s) { name }", alias, alias))
		vars[alias] = name
	}
	for _, login := range c.users {
		alias := c.alias["@"+strings.ToLower(login)]
		params = append(params, "$"+alias+": String!")
		fields = append(fields, fmt.Sprintf("    %s: assignableUsers(query: $%s, first: 100) { nodes { login } }", alias, alias))
		vars[alias] = login
	}
	if c.milestones {
		fields = append(fields, "    "+issue.MilestonesSelection)
	}
	return "query DoctorVerbs(" + strings.Join(params, ", ") + ") {\n  repository(owner: $owner, name: $name) {\n" +
		strings.Join(fields, "\n") + "\n  }\n}", vars
}

// checkVerbs checks the labels of every verb against the default
// repository, exactly as a verb looks them up, and that the users verbs
// assign or unassign by login can be assigned there: one DoctorVerbs
// query, sent only when a verb has labels or names someone. A label to add
// that is missing, or any label spelled in another case, fails the verb; a
// label to remove that is missing never removes anything, likely a typo.
// A label of from_labels or refuse_labels that is missing fails the verb
// too (it would refuse every issue, or never refuse); one spelled in
// another case is fine, as the checks match names regardless of case.
// A user to assign who cannot be assigned fails the verb; one to unassign
// is a warning: a verb removes current assignees by their IDs, and someone
// who lost access may still be one, but the login is likely a typo. A
// milestone of the config (not "{milestone}") that matches no open
// milestone, or several, fails the verb, as it would fail every issue;
// past 100 open milestones they are read page by page.
func checkVerbs(ctx context.Context, r *Report, q graph.Querier, cfg *config.Config, repos []*repo, reach bool, viewer string) {
	c := newVerbsCheck(cfg, viewer)
	if c.empty() {
		return
	}
	var def *repo
	for _, rp := range repos {
		if rp.Default {
			def = rp
		}
	}
	switch {
	case !reach:
		r.AddSkip("verbs", "needs GitHub access (see token)")
		return
	case def == nil || !def.usable():
		r.AddSkip("verbs", "needs the default repository (see repo)")
		return
	}
	query, vars := c.query(def.Repo)
	var out struct {
		Repository map[string]json.RawMessage `json:"repository"`
	}
	if err := q.Query(ctx, query, vars, &out); err != nil {
		r.AddProblem("verbs", err)
		return
	}
	var window struct {
		Nodes []struct{ Name string } `json:"nodes"`
	}
	_ = json.Unmarshal(out.Repository["labels"], &window)
	var names []string
	for _, l := range window.Nodes {
		names = append(names, l.Name)
	}
	exists := func(name string) bool {
		var l *struct{ Name string }
		_ = json.Unmarshal(out.Repository[c.alias[name]], &l)
		return l != nil && l.Name != ""
	}
	assignable := func(login string) bool {
		var users struct {
			Nodes []*struct{ Login string } `json:"nodes"`
		}
		_ = json.Unmarshal(out.Repository[c.alias["@"+strings.ToLower(login)]], &users)
		return slices.ContainsFunc(users.Nodes, func(u *struct{ Login string }) bool {
			return u != nil && strings.EqualFold(u.Login, login)
		})
	}

	var open graph.Connection[issue.OpenMilestone]
	_ = json.Unmarshal(out.Repository["milestones"], &open)
	milestones := open.Nodes
	if c.milestones && open.PageInfo.HasNextPage {
		rest, err := issue.ReadMilestones(ctx, q, def.Repo, open.PageInfo.EndCursor)
		if err != nil {
			r.AddProblem("verbs", err)
			return
		}
		milestones = append(milestones, rest...)
	}

	repoName, problems := def.Repo.String(), 0 // problems counts warnings too: no ok line then
	problem := func(err error) { r.AddProblem("verbs", err); problems++ }
	matched := 0 // the verbs' milestones found
	for _, v := range cfg.Verbs {
		for _, check := range []struct {
			key   string
			names []string
			never string // what a missing label makes of the check
		}{
			{"from_labels", v.FromLabels, v.Name + " refuses every issue"},
			{"refuse_labels", v.RefuseLabels, "the check never refuses"},
		} {
			key := cfg.KeyPath("verbs." + v.Name + "." + check.key)
			for _, name := range check.names {
				// A check matches names regardless of case, as GitHub's
				// label names are unique regardless of case: any spelling
				// looked up or in the window will do.
				if slices.ContainsFunc(c.labels, func(l string) bool { return strings.EqualFold(l, name) && exists(l) }) ||
					slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, name) }) {
					continue
				}
				problem(render.Usage("%s: no label %q in %s, so %s", key, name, repoName, check.never).
					WithFix(fmt.Sprintf("gh label create %q -R %s, or fix %s", name, repoName, key)))
			}
		}
		key := cfg.KeyPath("verbs." + v.Name + ".labels")
		for _, name := range append(slices.Clone(v.AddLabels), v.RemoveLabels...) {
			if exists(name) {
				continue
			}
			adds := slices.Contains(v.AddLabels, name)
			e := render.Usage("%s: no label %q in %s", key, name, repoName)
			switch spelled := spelling(name, names); {
			case spelled != "":
				e = e.WithFix(fmt.Sprintf("write it as %q in %s", spelled, key))
			case adds:
				e = e.WithFix(fmt.Sprintf("gh label create %q -R %s, or fix %s", name, repoName, key))
			default:
				e = render.Usage("%s: no label %q in %s, so -%s never removes anything", key, name, repoName, name).
					WithFix(fmt.Sprintf("edit %s, or gh label create %q -R %s", key, name, repoName))
			}
			problem(e)
		}
		if v.Milestone != "" {
			if _, err := issue.MatchMilestone(v.Milestone, milestones, def.Repo); err != nil {
				problem(issue.MilestoneKeyError(cfg.KeyPath("verbs."+v.Name+".milestone"), err))
			} else {
				matched++
			}
		}
		for _, u := range verbUsers(v) {
			key := cfg.KeyPath("verbs." + v.Name + "." + u.key)
			name, who := u.name, u.name // the login, and how the line names it
			if u.name == config.Me {
				name, who = viewer, fmt.Sprintf("%q: you (%s)", config.Me, viewer)
			}
			switch login := u.name; {
			case login == config.Me && viewer == "":
				// The token check did not read the viewer.
			case u.key == "unassign" && !assignable(name):
				r.AddWarn("verbs", "check the login in "+key,
					"%s: %s cannot be assigned issues in %s: the verb removes them only if they are assigned already",
					key, who, repoName)
				problems++
			case login == config.Me && !assignable(viewer):
				problem(render.Usage("%s = %q: you (%s) cannot be assigned issues in %s",
					key, config.Me, viewer, repoName).
					WithFix(fmt.Sprintf("get write access to %s, or change %s", repoName, key)))
			case login != config.Me && !assignable(login):
				problem(render.Usage("%s: %s cannot be assigned issues in %s", key, login, repoName).
					WithFix(fmt.Sprintf("check the login, or give %s access to %s", login, repoName)))
			}
		}
	}
	if problems == 0 {
		var found []string
		if n := len(c.labels); n > 0 {
			found = append(found, plural(n, "label exists", "labels exist"))
		}
		if n := len(c.users); n > 0 {
			found = append(found, plural(n, "assignee", "assignees")+" can be assigned")
		}
		if matched > 0 {
			found = append(found, plural(matched, "milestone is", "milestones are")+" open")
		}
		r.AddOK("verbs", "%s: %s", repoName, strings.Join(found, ", "))
	}
}
