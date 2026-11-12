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
  sprint   with fields.sprint set: the field is an iteration field and an
           iteration covers today
  verbs    the labels verbs add or remove exist in the default repository,
           spelled as there, and the users they assign can be assigned
  agent    with [agent] set or a file agent install wrote: those files are
           up to date with the config and this gh-dojo (out of date: warn)

It sends one request for the token and the repositories, one more for the
board when its schema is not cached (an hour), and one for the verbs'
labels and assignees when a verb has them.

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
// cached one has no iteration for today), plus one DoctorVerbs query when
// a verb has labels or assigns someone.
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
	default:
		for _, rp := range repos {
			checkRepo(r, cfg, rp)
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

// verbsCheck is what checkVerbs asks GitHub about the verbs: label names
// exactly as written, logins to assign (config.Me as the viewer's).
type verbsCheck struct {
	labels []string          // distinct, config order
	users  []string          // distinct regardless of case
	alias  map[string]string // label name or lowercase login → alias
}

func newVerbsCheck(cfg *config.Config, viewer string) *verbsCheck {
	c := &verbsCheck{alias: map[string]string{}}
	for _, v := range cfg.Verbs {
		for _, name := range append(slices.Clone(v.AddLabels), v.RemoveLabels...) {
			if _, ok := c.alias[name]; !ok {
				c.alias[name] = fmt.Sprintf("label%d", len(c.labels))
				c.labels = append(c.labels, name)
			}
		}
		login := v.Assign
		if login == config.Me {
			login = viewer
		}
		if key := "@" + strings.ToLower(login); login != "" && c.alias[key] == "" {
			c.alias[key] = fmt.Sprintf("user%d", len(c.users))
			c.users = append(c.users, login)
		}
	}
	return c
}

func (c *verbsCheck) empty() bool { return len(c.labels) == 0 && len(c.users) == 0 }

// query is the DoctorVerbs query: the first 100 labels of the repository
// for their spelling, each label by its exact name, and the assignable
// users matching each login.
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
	return "query DoctorVerbs(" + strings.Join(params, ", ") + ") {\n  repository(owner: $owner, name: $name) {\n" +
		strings.Join(fields, "\n") + "\n  }\n}", vars
}

// checkVerbs checks the labels of every verb against the default
// repository, exactly as a verb looks them up, and that the users verbs
// assign can be assigned there: one DoctorVerbs query, sent only when a
// verb has labels or assigns someone. A label to add that is missing, or
// any label spelled in another case, fails the verb; a label to remove
// that is missing never removes anything, likely a typo.
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

	repoName, problems := repo.Repo.String(), 0
	problem := func(err error) { r.problem("verbs", err); problems++ }
	for _, v := range cfg.Verbs {
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
		switch login := v.Assign; {
		case login == "":
		case login == config.Me && viewer == "":
			// The token check did not read the viewer.
		case login == config.Me && !assignable(viewer):
			problem(render.Usage("%s = %q: you (%s) cannot be assigned issues in %s",
				cfg.KeyPath("verbs."+v.Name+".assign"), config.Me, viewer, repoName).
				WithFix(fmt.Sprintf("get write access to %s, or change %s", repoName, cfg.KeyPath("verbs."+v.Name+".assign"))))
		case login != config.Me && !assignable(login):
			problem(render.Usage("%s: %s cannot be assigned issues in %s",
				cfg.KeyPath("verbs."+v.Name+".assign"), login, repoName).
				WithFix(fmt.Sprintf("check the login, or give %s access to %s", login, repoName)))
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
