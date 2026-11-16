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

// labelsCheck is what the DoctorLabels query asks about each repository's
// labels for new: labels.types and labels.area_prefix. In each repository
// it reads the first 100 labels and searches for each type and for the
// prefix. Search matches the start of words regardless of case: it finds a
// type, one letter long or not, but not a prefix of punctuation such as
// "~" or ".", and it also returns labels that only contain the prefix. So
// past the first 100 labels an area count is a lower bound, and a type the
// search did not find is looked up in all the labels, as new does.
type labelsCheck struct {
	types  []string
	prefix string
}

// newLabelsCheck returns nil when the config has neither labels.types nor
// labels.area_prefix: there is nothing to ask.
func newLabelsCheck(cfg *config.Config) *labelsCheck {
	if cfg == nil || len(cfg.Labels.Types) == 0 && cfg.Labels.AreaPrefix == "" {
		return nil
	}
	return &labelsCheck{types: cfg.Labels.Types, prefix: cfg.Labels.AreaPrefix}
}

// labelsPage is the most labels one field of the DoctorLabels query reads.
const labelsPage = 100

// query is the DoctorLabels query for the repositories at idx, each aliased
// by its index in repos: repo0, repo1...
func (c *labelsCheck) query(repos []*repo, idx []int) (string, map[string]any) {
	var params, fields []string
	vars := map[string]any{}
	for _, i := range idx {
		params = append(params, fmt.Sprintf("$owner%d: String!, $name%d: String!", i, i))
		f := []string{
			fmt.Sprintf("  repo%d: repository(owner: $owner%d, name: $name%d) {", i, i, i),
			fmt.Sprintf("    labels(first: %d) { totalCount nodes { name } }", labelsPage),
		}
		for j := range c.types {
			f = append(f, fmt.Sprintf("    type%d: labels(first: %d, query: $type%d) { nodes { name } }", j, labelsPage, j))
		}
		if c.prefix != "" {
			f = append(f, fmt.Sprintf("    area: labels(first: %d, query: $area) { nodes { name } }", labelsPage))
		}
		fields = append(fields, strings.Join(f, "\n")+"\n  }")
		vars[fmt.Sprintf("owner%d", i)] = repos[i].Repo.Owner
		vars[fmt.Sprintf("name%d", i)] = repos[i].Repo.Name
	}
	for j, t := range c.types {
		params = append(params, fmt.Sprintf("$type%d: String!", j))
		vars[fmt.Sprintf("type%d", j)] = t
	}
	if c.prefix != "" {
		params = append(params, "$area: String!")
		vars["area"] = c.prefix
	}
	return "query DoctorLabels(" + strings.Join(params, ", ") + ") {\n" + strings.Join(fields, "\n") + "\n}", vars
}

// labelsFound is what doctor found of one repository's labels.
type labelsFound struct {
	names    []string // distinct
	total    int      // how many labels the repository has
	complete bool     // names has every label of the repository
}

// found reads one repository of the DoctorLabels answer.
func (c *labelsCheck) found(answer json.RawMessage) *labelsFound {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(answer, &fields)
	f := &labelsFound{}
	read := func(key string) (total, n int) {
		var conn struct {
			TotalCount int                      `json:"totalCount"`
			Nodes      []*struct{ Name string } `json:"nodes"`
		}
		_ = json.Unmarshal(fields[key], &conn)
		for _, l := range conn.Nodes {
			if l != nil {
				f.add(l.Name)
			}
		}
		return conn.TotalCount, len(conn.Nodes)
	}
	total, n := read("labels")
	for j := range c.types {
		read(fmt.Sprintf("type%d", j))
	}
	read("area")
	f.total, f.complete = total, total <= n
	return f
}

func (f *labelsFound) add(name string) {
	if !slices.Contains(f.names, name) {
		f.names = append(f.names, name)
	}
}

// has reports whether a label matches name regardless of case, as new
// looks labels up.
func (f *labelsFound) has(name string) bool {
	return slices.ContainsFunc(f.names, func(n string) bool { return strings.EqualFold(n, name) })
}

// allLabels reads every label of repo, page by page, as new does past the
// first 100.
func allLabels(ctx context.Context, q graph.Querier, repo issue.Repo) (*labelsFound, error) {
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
	f := &labelsFound{total: len(page.Nodes), complete: true}
	for _, l := range page.Nodes {
		f.add(l.Name)
	}
	return f, nil
}

// run checks labels.types and labels.area_prefix in each repository the
// repo check found usable: one DoctorLabels query for them all. A failed
// request is a problem of this check alone; a repository it could not
// read is a problem of its own.
func (c *labelsCheck) run(ctx context.Context, r *Report, q graph.Querier, cfg *config.Config, repos []*repo) {
	var idx []int
	for i, rp := range repos {
		if rp.usable() {
			idx = append(idx, i)
		}
	}
	var out map[string]json.RawMessage
	var err error
	var failed map[int]bool
	if len(idx) > 0 {
		query, vars := c.query(repos, idx)
		err = q.Query(ctx, query, vars, &out)
		var partial bool
		if failed, partial = repoErrors(err); err != nil && !partial {
			r.AddProblem("labels", err)
			return
		}
	}
	for i, rp := range repos {
		name := rp.Repo.String()
		answer := out[fmt.Sprintf("repo%d", i)]
		switch {
		case !rp.usable():
			r.AddSkip("labels", "%s: needs the repository (see repo)", name)
		case failed[i] || len(answer) == 0 || string(answer) == "null":
			if err == nil {
				err = render.Network("GitHub did not answer with the labels")
			}
			r.AddProblem("labels", named(name, err))
		default:
			c.check(ctx, r, q, cfg, rp, len(repos) == 1, c.found(answer))
		}
	}
}

// check reports one repository's labels. A missing type label is a
// problem: new --type fails on it. One in another case is a warning, since
// new matches labels regardless of case; its fix renames the label, or with
// one repository edits the config. With over 100 labels, a type found
// neither among the first 100 nor by search is looked up in all of them.
// No label with the area prefix is a warning when the config sets the
// prefix (new --area finds nothing), and a note on the ok line when a
// preset does: many teams never use the preset's areas. Past 100 labels
// the area count is a lower bound, and none found cannot be confirmed.
func (c *labelsCheck) check(ctx context.Context, r *Report, q graph.Querier, cfg *config.Config, rp *repo, single bool, f *labelsFound) {
	name := rp.Repo.String()
	if !f.complete && slices.ContainsFunc(c.types, func(t string) bool { return !f.has(t) }) {
		all, err := allLabels(ctx, q, rp.Repo)
		if err != nil {
			r.AddProblem("labels", named(name, err))
			return
		}
		f = all
	}
	var found []string
	key, bad := cfg.KeyPath("labels.types"), 0
	for _, t := range c.types {
		switch spelled := spelling(t, f.names); {
		case slices.Contains(f.names, t):
		case spelled != "":
			fix := fmt.Sprintf("gh label edit %q --name %q -R %s", spelled, t, name)
			if single {
				fix = fmt.Sprintf("write it as %q in %s, or %s", spelled, key, fix)
			}
			r.AddWarn("labels", fix, "%s: %q is %q in %s; new --type finds it regardless of case", key, t, spelled, name)
			bad++
		default:
			r.AddProblem("labels", render.Usage("%s: no label %q in %s, so gh dojo new --type %s fails", key, t, name, t).
				WithFix(fmt.Sprintf("gh label create %q -R %s, or fix %s", t, name, key)))
			bad++
		}
	}
	if n := len(c.types); n > 0 && bad == 0 {
		found = append(found, plural(n, "type label exists", "type labels exist"))
	}
	if c.prefix != "" {
		areas := 0
		for _, n := range f.names {
			if hasPrefixFold(n, c.prefix) {
				areas++
			}
		}
		switch {
		case !f.complete && areas == 0:
			found = append(found, fmt.Sprintf("could not confirm area labels (%s…): none among the first %d of %d labels or by search",
				c.prefix, labelsPage, f.total))
		case !f.complete:
			found = append(found, fmt.Sprintf("at least %s (%s…)", plural(areas, "area label", "area labels"), c.prefix))
		case areas == 0 && cfg.FromPreset("labels.area_prefix") != "":
			found = append(found, fmt.Sprintf("no area labels (%s…) yet: new --area fails until one exists", c.prefix))
		case areas == 0:
			r.AddWarn("labels", fmt.Sprintf("gh label create %q -R %s, or set labels.area_prefix = \"\" in %s",
				c.prefix+"<name>", name, cfg.Display),
				"%s: no label starts with labels.area_prefix %q, so gh dojo new --area finds nothing", name, c.prefix)
		default:
			found = append(found, fmt.Sprintf("%s (%s…)", plural(areas, "area label", "area labels"), c.prefix))
		}
	}
	if len(found) > 0 {
		r.AddOK("labels", "%s: %s", name, strings.Join(found, ", "))
	}
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

// hasPrefixFold reports whether s starts with prefix regardless of case,
// byte by byte.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
