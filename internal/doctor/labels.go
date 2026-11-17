package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// labelGroup is a group of labels new takes by prefix: --type or --area.
type labelGroup struct {
	flag   string // "type" or "area"
	key    string // its config key, "labels.type_prefix"
	prefix string
}

// labelsCheck is what the DoctorLabels query asks about each repository's
// labels for new: how many start with labels.type_prefix and with
// labels.area_prefix. In each repository it reads the first 100 labels and
// searches for each prefix. Search matches the start of words regardless
// of case: it does not find a prefix of punctuation such as "~" or ".", and
// it also returns labels that only contain the prefix. So past the first
// 100 labels a count is a lower bound.
type labelsCheck struct {
	groups []labelGroup
}

// newLabelsCheck returns nil when the config sets neither prefix: there is
// nothing to ask, as --type then takes any label and --area none.
func newLabelsCheck(cfg *config.Config) *labelsCheck {
	if cfg == nil {
		return nil
	}
	c := &labelsCheck{}
	for _, g := range []labelGroup{
		{"type", "labels.type_prefix", cfg.Labels.TypePrefix},
		{"area", "labels.area_prefix", cfg.Labels.AreaPrefix},
	} {
		if g.prefix != "" {
			c.groups = append(c.groups, g)
		}
	}
	if len(c.groups) == 0 {
		return nil
	}
	return c
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
		for _, g := range c.groups {
			f = append(f, fmt.Sprintf("    %s: labels(first: %d, query: $%s) { nodes { name } }", g.flag, labelsPage, g.flag))
		}
		fields = append(fields, strings.Join(f, "\n")+"\n  }")
		vars[fmt.Sprintf("owner%d", i)] = repos[i].Repo.Owner
		vars[fmt.Sprintf("name%d", i)] = repos[i].Repo.Name
	}
	for _, g := range c.groups {
		params = append(params, fmt.Sprintf("$%s: String!", g.flag))
		vars[g.flag] = g.prefix
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
	for _, g := range c.groups {
		read(g.flag)
	}
	f.total, f.complete = total, total <= n
	return f
}

func (f *labelsFound) add(name string) {
	for _, n := range f.names {
		if n == name {
			return
		}
	}
	f.names = append(f.names, name)
}

// run checks labels.type_prefix and labels.area_prefix in each repository
// the repo check found usable: one DoctorLabels query for them all. A
// failed request is a problem of this check alone; a repository it could
// not read is a problem of its own.
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
			c.check(r, cfg, name, c.found(answer))
		}
	}
}

// check reports one repository's labels: how many each group has. None is
// a warning when the config sets the prefix (new with the group's flag
// finds nothing), and a note on the ok line when a preset does: many teams
// never use the preset's areas. Past 100 labels a count is a lower bound,
// and none found cannot be confirmed.
func (c *labelsCheck) check(r *Report, cfg *config.Config, name string, f *labelsFound) {
	var found []string
	for _, g := range c.groups {
		n := 0
		for _, l := range f.names {
			if hasPrefixFold(l, g.prefix) {
				n++
			}
		}
		one, many := g.flag+" label", g.flag+" labels"
		switch {
		case !f.complete && n == 0:
			found = append(found, fmt.Sprintf("could not confirm %s (%s…): none among the first %d of %d labels or by search",
				many, g.prefix, labelsPage, f.total))
		case !f.complete:
			found = append(found, fmt.Sprintf("at least %s (%s…)", plural(n, one, many), g.prefix))
		case n == 0 && cfg.FromPreset(g.key) != "":
			found = append(found, fmt.Sprintf("no %s (%s…) yet: new --%s fails until one exists", many, g.prefix, g.flag))
		case n == 0:
			r.AddWarn("labels", fmt.Sprintf("gh label create %q -R %s, or set %s = \"\" in %s",
				g.prefix+"<name>", name, g.key, cfg.Display),
				"%s: no label starts with %s %q, so gh dojo new --%s finds nothing", name, g.key, g.prefix, g.flag)
		default:
			found = append(found, fmt.Sprintf("%s (%s…)", plural(n, one, many), g.prefix))
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
