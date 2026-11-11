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
		Long: `Check the setup and print one line per check: ok, problem, or skip when an
earlier problem keeps the check from running. Each problem is followed by the
command that fixes it.

  version  the installed gh-dojo version
  config   ` + config.FileName + ` loads
  token    gh is logged in and the token has the project scope
  board    the board exists and has every field and status the config names
  repo     each repository of board.repos (else the current one) is visible
           and has issues enabled

It sends one request for the token and the repositories, and one more for
the board when its schema is not cached (an hour).

Exit codes: 0 when every check is ok; 4 when a check failed on auth or the
network; else 1.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := runDoctor(cmd.Context())
			r.write(cmd.OutOrStdout())
			return r.err()
		},
	}, nil
}

// Doctor check results.
const (
	checkOK      = "ok"
	checkProblem = "problem"
	checkSkip    = "skip" // an earlier problem keeps the check from running
)

// doctorCheck is one line of doctor's report.
type doctorCheck struct {
	Name    string   `json:"name"`
	Result  string   `json:"result"` // checkOK, checkProblem or checkSkip
	Detail  string   `json:"detail"`
	Allowed []string `json:"allowed,omitempty"`
	Fix     string   `json:"fix,omitempty"`

	code render.Code // of a problem: render.ExitNetwork for auth and network, else render.ExitUsage
}

// doctorReport is what doctor found, as data (for --json later).
type doctorReport struct {
	Checks []doctorCheck `json:"checks"`
}

func (r *doctorReport) ok(name, format string, a ...any) {
	r.Checks = append(r.Checks, doctorCheck{Name: name, Result: checkOK, Detail: fmt.Sprintf(format, a...)})
}

func (r *doctorReport) skip(name, format string, a ...any) {
	r.Checks = append(r.Checks, doctorCheck{Name: name, Result: checkSkip, Detail: fmt.Sprintf(format, a...)})
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
		Name: name, Result: checkProblem, Detail: e.What, Allowed: e.Allowed, Fix: e.Fix, code: code,
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
		text := (&render.Error{What: c.Detail, Allowed: c.Allowed, Fix: c.Fix}).Text()
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
// and repositories), plus the board schema when it is not cached.
func runDoctor(ctx context.Context) *doctorReport {
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
	reach := checkToken(ctx, r, a, repos)

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
		checkSprint(r, cfg, schema, doctorClock(a)())
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
// checks that need it can run.
func checkToken(ctx context.Context, r *doctorReport, a *App, repos []*doctorRepo) bool {
	c, ok := a.Client.(tokenClient)
	if !ok {
		r.skip("token", "this GitHub client cannot report token scopes")
		return true
	}
	query, vars := doctorQuery(repos)
	var out map[string]json.RawMessage
	h, err := c.QueryHeaders(ctx, query, vars, &out)
	failed, partial := repoErrors(err)
	if err != nil && !partial {
		r.problem("token", err)
		return false
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
	return true
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
	default:
		r.ok("repo", "%s: visible, issues enabled", name)
	}
}

// checkSprint checks that the sprint field has an iteration for today. It
// runs only when fields.sprint is set, which the config rejects until
// iteration effects come: this is their hook.
func checkSprint(r *doctorReport, cfg *config.Config, s *board.Schema, now time.Time) {
	name := cfg.Fields["sprint"]
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
	today := now.Format(time.DateOnly)
	for _, it := range f.Iterations {
		if !it.Completed && it.Contains(now) {
			r.ok("sprint", "%s: today (%s) is in %s", f.Name, today, it.Title)
			return
		}
	}
	r.problem("sprint", render.Usage("%s has no iteration for today (%s)", f.Name, today).
		WithFix(fmt.Sprintf("add one in the %s field settings: %s/settings", f.Name, s.URL)))
}

// doctorClock is the App's clock: its loader's, else the system's.
func doctorClock(a *App) func() time.Time {
	if a.Loader != nil && a.Loader.Now != nil {
		return a.Loader.Now
	}
	return time.Now
}
