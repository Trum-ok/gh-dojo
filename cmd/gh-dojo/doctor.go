package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/spf13/cobra"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

func init() { register(newDoctorCmd) }

func newDoctorCmd() (*cobra.Command, error) {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Find what is broken in the setup: config, token scope, board, repositories",
		Long: `Check the setup and print one line per check: ok, problem, warn, or skip
when an earlier problem keeps the check from running. Each problem and
warning is followed by the command that fixes it.

  version  the installed gh-dojo version
  config   ` + config.FileName + ` loads
  token    gh is logged in and the token has the project scope
  board    the board exists and has every field and status the config names
  repo     each repository of board.repos (else the current one) is visible
           and has issues enabled; warns when board.repos spells one otherwise
           than GitHub does
  labels   with labels.types or labels.area_prefix set, in each of those
           repositories: every type label exists (missing: problem, since
           new --type fails; in another case: warn), and how many labels
           start with the area prefix (none: warn when the config sets the
           prefix, not a preset, since --area finds nothing; past 100
           labels the count is a lower bound)
  sprint   with fields.sprint set: the field is an iteration field and an
           iteration covers today
  verbs    the labels verbs add or remove exist in the default repository,
           spelled as there, the users they assign can be assigned, and
           each milestone a verb names is an open one there, matched as the
           verb matches it (its title, or a start only that title has)
  agent    with [agent] set or a file agent install wrote: those files are
           up to date with the config and this gh-dojo (out of date: warn)

It sends one request for the token and the repositories, one more for the
board when its schema is not cached (an hour), one for the labels of
labels.types and labels.area_prefix when set (and, for a repository with
over 100 labels where a type is not found, its labels page by page), and
one for the verbs' labels, assignees and milestones when a verb has them
(and, past 100 open milestones, those page by page).

Exit codes: 0 when every check is ok or warns; 4 when a check failed on auth or the
network; else 1.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := runDoctor(cmd.Context(), cmd.Root())
			err := r.err()
			r.OK = err == nil
			return emitResult(cmd, r, r.write, err)
		},
	}, nil
}

// Doctor check results.
const (
	checkOK      = "ok"
	checkProblem = "problem"
	checkSkip    = "skip" // an earlier problem keeps the check from running
	checkWarn    = "warn" // worth fixing, but not a problem: the exit code stays 0
)

// doctorCheck is one line of doctor's report.
type doctorCheck struct {
	Name    string   `json:"name"`
	Result  string   `json:"result"` // checkOK, checkProblem, checkWarn or checkSkip
	Detail  string   `json:"detail"`
	Allowed []string `json:"allowed"`
	Fix     *string  `json:"fix"` // null for none

	code render.Code // of a problem: render.ExitNetwork for auth and network, else render.ExitUsage
}

// doctorReport is what doctor found, and its JSON.
type doctorReport struct {
	OK     bool          `json:"ok"` // no check is a problem (warnings allowed)
	Checks []doctorCheck `json:"checks"`
}

func (r *doctorReport) ok(name, format string, a ...any) {
	r.Checks = append(r.Checks, doctorCheck{Name: name, Result: checkOK, Detail: fmt.Sprintf(format, a...), Allowed: []string{}})
}

// warn adds a warning with the command that fixes it.
func (r *doctorReport) warn(name, fix, format string, a ...any) {
	r.Checks = append(r.Checks, doctorCheck{
		Name: name, Result: checkWarn, Detail: fmt.Sprintf(format, a...), Allowed: []string{}, Fix: orNull(fix),
	})
}

func (r *doctorReport) skip(name, format string, a ...any) {
	r.Checks = append(r.Checks, doctorCheck{Name: name, Result: checkSkip, Detail: fmt.Sprintf(format, a...), Allowed: []string{}})
}

// problem adds err as a problem: auth and network errors (exit code 4)
// count as such, anything else as a config or board problem.
func (r *doctorReport) problem(name string, err error) {
	e := render.FromError(err)
	code := render.ExitUsage
	if e.Code == render.ExitNetwork {
		code = render.ExitNetwork
	}
	r.Checks = append(r.Checks, doctorCheck{
		Name: name, Result: checkProblem, Detail: e.What, Allowed: orEmpty(e.Allowed), Fix: orNull(e.Fix), code: code,
	})
}

// write prints one line per check, the details of a problem indented below
// it:
//
//	ok       config   .github/dojo.toml: preset kanban, 4 verbs
//	problem  token    github.com as octocat: the token lacks the project scope (has repo)
//	                  fix: gh auth refresh -s project
func (r *doctorReport) write(w io.Writer) {
	const indent = "                  " // past the result and the name
	for _, c := range r.Checks {
		// render's error block keeps long values short and drops a fix too
		// long to be a command; its first line is the detail.
		fix := ""
		if c.Fix != nil {
			fix = *c.Fix
		}
		text := (&render.Error{What: c.Detail, Allowed: c.Allowed, Fix: fix}).Text()
		text = strings.TrimPrefix(text, "error: ")
		fmt.Fprintf(w, "%-8s %-8s %s\n", c.Result, c.Name, strings.ReplaceAll(text, "\n", "\n"+indent))
	}
}

// err is the command's error: nil when every check is ok, else a short
// summary with exit code 4 when a problem is about auth or the network,
// else 1.
func (r *doctorReport) err() error {
	n, code := 0, render.ExitUsage
	for _, c := range r.Checks {
		if c.Result == checkProblem {
			n++
			if c.code == render.ExitNetwork {
				code = render.ExitNetwork
			}
		}
	}
	if n == 0 {
		return nil
	}
	return render.New(code, "doctor found %s; fix as shown above, then run gh dojo doctor again",
		plural(n, "problem", "problems"))
}

// runDoctor runs the checks in order. Requests: one Doctor query (token
// and repositories), plus the board schema when it is not cached (or the
// cached one has no iteration for today), plus one DoctorLabels query with
// labels.types or labels.area_prefix set (and all the labels of a
// repository with over 100 whose type it did not find), plus one
// DoctorVerbs query when a verb has labels or assigns someone.
func runDoctor(ctx context.Context, root *cobra.Command) *doctorReport {
	r := &doctorReport{}
	r.ok("version", "gh dojo %s", version)

	cfg, err := loadConfig()
	if err != nil {
		r.problem("config", err)
		cfg = nil
	} else {
		r.ok("config", "%s", configSummary(cfg))
	}

	a := newApp()
	repos, repoErr := doctorRepos(a, cfg)
	labels := newLabelsCheck(cfg)
	reach, viewer := checkToken(ctx, r, a, repos)

	var schema *board.Schema
	switch {
	case cfg == nil:
		r.skip("board", "needs a config that loads")
	case !reach:
		r.skip("board", "needs GitHub access (see token)")
	default:
		schema = checkBoard(ctx, r, a, cfg)
	}

	switch {
	case cfg == nil:
		r.skip("repo", "needs a config that loads")
	case repoErr != nil:
		r.problem("repo", repoErr)
	case !reach:
		r.skip("repo", "needs GitHub access (see token)")
		if labels != nil {
			r.skip("labels", "needs GitHub access (see token)")
		}
	default:
		for _, rp := range repos {
			checkRepo(r, cfg, rp)
		}
		if labels != nil {
			checkDoctorLabels(ctx, r, a, cfg, labels, repos)
		}
	}

	if cfg != nil && schema != nil {
		checkSprint(r, cfg, schema, doctorClock(a)(), func() (*board.Schema, error) {
			ref := board.RefOf(cfg)
			if err := a.Loader.Invalidate(ref); err != nil {
				return nil, err
			}
			return a.Loader.Fetch(ctx, ref)
		})
	}
	if cfg != nil {
		checkVerbs(ctx, r, a, cfg, repos, reach, viewer)
		checkAgentFiles(r, cfg, root)
	}
	return r
}

// configSummary is the detail of a config that loads.
func configSummary(cfg *config.Config) string {
	s := cfg.Display + ": "
	if cfg.Preset != "" {
		s += "preset " + cfg.Preset + ", "
	}
	return s + plural(len(cfg.Verbs), "verb", "verbs")
}

// doctorRepo is a repository doctor checks, with what the Doctor query
// found.
type doctorRepo struct {
	Repo    issue.Repo
	Default bool
	Current bool // not from board.repos: GH_REPO or the git remote
	Found   *struct {
		NameWithOwner    string `json:"nameWithOwner"`
		HasIssuesEnabled bool   `json:"hasIssuesEnabled"`
	}
	Err error // GitHub denied reading it, e.g. its organization's SAML SSO
}

// doctorRepos lists the repositories to check: board.repos, else the
// current repository. An error means there is no repository to use.
func doctorRepos(a *App, cfg *config.Config) ([]*doctorRepo, error) {
	if cfg == nil {
		return nil, nil
	}
	if len(cfg.Board.Repos) == 0 {
		r, err := a.Repo()
		if err != nil {
			return nil, err
		}
		return []*doctorRepo{{Repo: r, Default: true, Current: true}}, nil
	}
	var repos []*doctorRepo
	for i, name := range cfg.Board.Repos {
		r, err := issue.ParseRepo(name)
		if err != nil {
			return nil, err
		}
		repos = append(repos, &doctorRepo{Repo: r, Default: i == 0})
	}
	return repos, nil
}

// tokenClient is what checkToken needs of the GitHub client; *graph.Client
// has it.
type tokenClient interface {
	QueryHeaders(ctx context.Context, query string, vars map[string]any, out any) (http.Header, error)
	Host() string
	TokenEnv() string
	AuthFix() string
}

// doctorQuery reads the viewer and each repository, aliased repo0, repo1...
// GitHub lists the token's scopes in the response headers.
func doctorQuery(repos []*doctorRepo) (string, map[string]any) {
	if len(repos) == 0 {
		return "query Doctor { viewer { login } }", nil
	}
	params := make([]string, len(repos))
	fields := make([]string, len(repos))
	vars := map[string]any{}
	for i, r := range repos {
		params[i] = fmt.Sprintf("$owner%d: String!, $name%d: String!", i, i)
		fields[i] = fmt.Sprintf("  repo%d: repository(owner: $owner%d, name: $name%d) { nameWithOwner hasIssuesEnabled }", i, i, i)
		vars[fmt.Sprintf("owner%d", i)] = r.Repo.Owner
		vars[fmt.Sprintf("name%d", i)] = r.Repo.Name
	}
	return "query Doctor(" + strings.Join(params, ", ") + ") {\n  viewer { login }\n" +
		strings.Join(fields, "\n") + "\n}", vars
}

// checkToken sends the Doctor query, checks the token's scopes and fills
// what it found about repos. It reports whether GitHub answered, so the
// checks that need it can run, and the authenticated user's login ("" when
// not read).
func checkToken(ctx context.Context, r *doctorReport, a *App, repos []*doctorRepo) (bool, string) {
	c, ok := a.Client.(tokenClient)
	if !ok {
		r.skip("token", "this GitHub client cannot report token scopes")
		return true, ""
	}
	query, vars := doctorQuery(repos)
	var out map[string]json.RawMessage
	h, err := c.QueryHeaders(ctx, query, vars, &out)
	failed, partial := repoErrors(err)
	if err != nil && !partial {
		r.problem("token", err)
		return false, ""
	}
	var viewer struct{ Login string }
	_ = json.Unmarshal(out["viewer"], &viewer)
	for i, rp := range repos {
		_ = json.Unmarshal(out[fmt.Sprintf("repo%d", i)], &rp.Found) // null: not found
		if failed[i] {
			// SAML SSO or another denial: the client mapped it, with the
			// fix for where the token comes from.
			rp.Err = err
		}
	}

	who := c.Host() + " as " + viewer.Login
	if src := c.TokenEnv(); src != "" {
		who += " (token from " + src + ")"
	}
	scopes, listed := graph.Scopes(h)
	switch {
	case !listed:
		r.ok("token", "%s; no scopes listed (a fine-grained or app token): the board check shows its access", who)
	case slices.Contains(scopes, graph.ScopeProject):
		r.ok("token", "%s, with the project scope", who)
	case slices.Contains(scopes, graph.ScopeReadProject):
		r.problem("token", render.Network("%s: the token has read:project but not project, so commands that change the board fail", who).
			WithFix(c.AuthFix()))
	default:
		has := "none"
		if len(scopes) > 0 {
			has = strings.Join(scopes, ", ")
		}
		r.problem("token", render.Network("%s: the token lacks the project scope (has: %s)", who, has).WithFix(c.AuthFix()))
	}
	return true, viewer.Login
}

// repoErrors reads a GraphQL answer whose errors are all about repository
// fields (repo0, repo1...): the viewer and the other repositories are in
// place. partial is false for any other error. failed has the index of
// each repository with an error other than NOT_FOUND, such as an
// organization's SAML SSO denying the token; a NOT_FOUND one is null in
// the data, which reads as not visible.
func repoErrors(err error) (failed map[int]bool, partial bool) {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || len(gqlErr.Errors) == 0 {
		return nil, false
	}
	failed = map[int]bool{}
	for _, item := range gqlErr.Errors {
		var i int
		if len(item.Path) == 0 {
			return nil, false
		}
		alias, _ := item.Path[0].(string)
		if _, serr := fmt.Sscanf(alias, "repo%d", &i); serr != nil || alias != fmt.Sprintf("repo%d", i) {
			return nil, false
		}
		if item.Type != "NOT_FOUND" {
			failed[i] = true
		}
	}
	return failed, true
}

// checkBoard checks the config against the board, from the cache when it
// is fresh; a mismatch on a cached schema re-reads the board first. Each
// mismatch is its own problem. It returns the schema, nil when the board
// could not be read.
func checkBoard(ctx context.Context, r *doctorReport, a *App, cfg *config.Config) *board.Schema {
	s, err := a.Loader.Resolve(ctx, board.RefOf(cfg), func(s *board.Schema) error { return board.Check(cfg, s) })
	if s == nil {
		r.problem("board", err)
		return nil
	}
	if problems := board.Problems(cfg, s); len(problems) > 0 {
		for _, p := range problems {
			r.problem("board", p.Err)
		}
	} else if err != nil {
		r.problem("board", err)
	} else {
		statuses := map[string]bool{}
		for _, ref := range cfg.StatusRefs() {
			statuses[ref.Status] = true
		}
		cached := ""
		if s.Cached {
			cached = " (cached)"
		}
		r.ok("board", "%s %q%s: field %s and %s match", s.Ref(), s.Title, cached, cfg.StatusField(),
			plural(len(statuses), "status", "statuses"))
	}
	if s.Closed {
		r.problem("board", render.Usage("board %s %q is closed", s.Ref(), s.Title).
			WithFix(fmt.Sprintf("gh project close %d --owner %s --undo", s.Number, s.Owner)))
	}
	return s
}

// checkRepo reports what the Doctor query found about one repository.
func checkRepo(r *doctorReport, cfg *config.Config, rp *doctorRepo) {
	name := rp.Repo.String()
	if rp.Default {
		name += " (default)"
	}
	switch {
	case rp.Err != nil:
		e := render.FromError(rp.Err)
		r.problem("repo", render.New(e.Code, "%s: %s", name, e.What).WithFix(e.Fix).WithCause(rp.Err))
	case rp.Found == nil && rp.Current:
		r.problem("repo", render.Usage("%s, the current repository, is not found or not visible to this token", name).
			WithFix(`add repos = ["owner/name"] under [board] in `+cfg.Display))
	case rp.Found == nil:
		r.problem("repo", render.Usage("%s is not found or not visible to this token", name).
			WithFix("edit board.repos in "+cfg.Display))
	case !rp.Found.HasIssuesEnabled:
		r.problem("repo", render.Usage("%s has issues disabled", name).
			WithFix("gh repo edit "+rp.Found.NameWithOwner+" --enable-issues"))
	case rp.Current:
		r.ok("repo", "%s, the current repository: visible, issues enabled", name)
	case rp.Found.NameWithOwner != "" && rp.Found.NameWithOwner != rp.Repo.String():
		// GitHub matches names without case, so it works; output names
		// the default repository as the config spells it.
		r.warn("repo", fmt.Sprintf("write it as %q in board.repos in %s", rp.Found.NameWithOwner, cfg.Display),
			"%s: visible, issues enabled; GitHub spells it %s", name, rp.Found.NameWithOwner)
	default:
		r.ok("repo", "%s: visible, issues enabled", name)
	}
}

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
func (c *labelsCheck) query(repos []*doctorRepo, idx []int) (string, map[string]any) {
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

// doctorUsable reports whether the repo check found rp usable.
func doctorUsable(rp *doctorRepo) bool {
	return rp.Err == nil && rp.Found != nil && rp.Found.HasIssuesEnabled
}

// checkDoctorLabels checks labels.types and labels.area_prefix in each repository
// the repo check found usable: one DoctorLabels query for them all. A
// failed request is a problem of this check alone; a repository it could
// not read is a problem of its own.
func checkDoctorLabels(ctx context.Context, r *doctorReport, a *App, cfg *config.Config, c *labelsCheck, repos []*doctorRepo) {
	var idx []int
	for i, rp := range repos {
		if doctorUsable(rp) {
			idx = append(idx, i)
		}
	}
	var out map[string]json.RawMessage
	var err error
	var failed map[int]bool
	if len(idx) > 0 {
		query, vars := c.query(repos, idx)
		err = a.Client.Query(ctx, query, vars, &out)
		var partial bool
		if failed, partial = repoErrors(err); err != nil && !partial {
			r.problem("labels", err)
			return
		}
	}
	for i, rp := range repos {
		name := rp.Repo.String()
		answer := out[fmt.Sprintf("repo%d", i)]
		switch {
		case !doctorUsable(rp):
			r.skip("labels", "%s: needs the repository (see repo)", name)
		case failed[i] || len(answer) == 0 || string(answer) == "null":
			if err == nil {
				err = render.Network("GitHub did not answer with the labels")
			}
			e := render.FromError(err)
			r.problem("labels", render.New(e.Code, "%s: %s", name, e.What).WithFix(e.Fix).WithCause(err))
		default:
			c.check(ctx, r, a, cfg, rp, len(repos) == 1, c.found(answer))
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
func (c *labelsCheck) check(ctx context.Context, r *doctorReport, a *App, cfg *config.Config, rp *doctorRepo, single bool, f *labelsFound) {
	name := rp.Repo.String()
	if !f.complete && slices.ContainsFunc(c.types, func(t string) bool { return !f.has(t) }) {
		all, err := allLabels(ctx, a.Client, rp.Repo)
		if err != nil {
			e := render.FromError(err)
			r.problem("labels", render.New(e.Code, "%s: %s", name, e.What).WithFix(e.Fix).WithCause(err))
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
			r.warn("labels", fix, "%s: %q is %q in %s; new --type finds it regardless of case", key, t, spelled, name)
			bad++
		default:
			r.problem("labels", render.Usage("%s: no label %q in %s, so gh dojo new --type %s fails", key, t, name, t).
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
			r.warn("labels", fmt.Sprintf("gh label create %q -R %s, or set labels.area_prefix = \"\" in %s",
				c.prefix+"<name>", name, cfg.Display),
				"%s: no label starts with labels.area_prefix %q, so gh dojo new --area finds nothing", name, c.prefix)
		default:
			found = append(found, fmt.Sprintf("%s (%s…)", plural(areas, "area label", "area labels"), c.prefix))
		}
	}
	if len(found) > 0 {
		r.ok("labels", "%s: %s", name, strings.Join(found, ", "))
	}
}

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
func checkVerbs(ctx context.Context, r *doctorReport, a *App, cfg *config.Config, repos []*doctorRepo, reach bool, viewer string) {
	c := newVerbsCheck(cfg, viewer)
	if c.empty() {
		return
	}
	var repo *doctorRepo
	for _, rp := range repos {
		if rp.Default {
			repo = rp
		}
	}
	switch {
	case !reach:
		r.skip("verbs", "needs GitHub access (see token)")
		return
	case repo == nil || repo.Err != nil || repo.Found == nil || !repo.Found.HasIssuesEnabled:
		r.skip("verbs", "needs the default repository (see repo)")
		return
	}
	query, vars := c.query(repo.Repo)
	var out struct {
		Repository map[string]json.RawMessage `json:"repository"`
	}
	if err := a.Client.Query(ctx, query, vars, &out); err != nil {
		r.problem("verbs", err)
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
		rest, err := issue.ReadMilestones(ctx, a.Client, repo.Repo, open.PageInfo.EndCursor)
		if err != nil {
			r.problem("verbs", err)
			return
		}
		milestones = append(milestones, rest...)
	}

	repoName, problems := repo.Repo.String(), 0 // problems counts warnings too: no ok line then
	problem := func(err error) { r.problem("verbs", err); problems++ }
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
			if _, err := issue.MatchMilestone(v.Milestone, milestones, repo.Repo); err != nil {
				problem(milestoneKeyError(cfg.KeyPath("verbs."+v.Name+".milestone"), err))
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
				r.warn("verbs", "check the login in "+key,
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
		r.ok("verbs", "%s: %s", repoName, strings.Join(found, ", "))
	}
}

// checkSprint checks that the sprint field has an iteration for today, the
// date in board.timezone verbs use (iteration = "current"), as verbs see
// it: a completed iteration still covers its days. Without one on a
// cached schema it re-reads the board once with refetch before reporting
// a problem. It runs only when fields.sprint is set.
func checkSprint(r *doctorReport, cfg *config.Config, s *board.Schema, now time.Time, refetch func() (*board.Schema, error)) {
	now = now.In(cfg.Board.Loc())
	name := cfg.SprintField()
	if name == "" {
		return
	}
	f, err := s.Field(name)
	if err != nil {
		return // board.Problems reported it
	}
	if f.Type != board.TypeIteration {
		r.problem("sprint", render.Usage("field %q on board %s is %s, not an iteration field", f.Name, s.Ref(), f.Type).
			WithFix("edit fields.sprint in "+cfg.Display))
		return
	}
	it := f.IterationAt(now)
	if it == nil && s.Cached && refetch != nil {
		if fresh, err := refetch(); err == nil {
			if g, err := fresh.Field(name); err == nil && g.Type == board.TypeIteration {
				s, f, it = fresh, g, g.IterationAt(now)
			}
		}
	}
	today := now.Format(time.DateOnly) + ", " + now.Location().String()
	if it == nil {
		r.problem("sprint", render.Usage("%s has no iteration for today (%s)", f.Name, today).
			WithFix(fmt.Sprintf("add one in the %s field settings: %s/settings", f.Name, s.URL)))
		return
	}
	r.ok("sprint", "%s: today (%s) is in %s", f.Name, today, it.Title)
}

// doctorClock is the App's clock: its loader's, else the system's.
func doctorClock(a *App) func() time.Time {
	if a.Loader != nil && a.Loader.Now != nil {
		return a.Loader.Now
	}
	return time.Now
}
