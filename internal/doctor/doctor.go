// Package doctor holds gh dojo doctor's checks of the setup: the config,
// the token, the board, the repositories, their labels, the sprint field
// and the verbs. Run sends the requests through Env's client and board
// loader and returns the report; the command prints it, as text or JSON,
// and adds the agent check, which needs the command tree.
package doctor

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Check results.
const (
	ResultOK      = "ok"
	ResultProblem = "problem"
	ResultSkip    = "skip" // an earlier problem keeps the check from running
	ResultWarn    = "warn" // worth fixing, but not a problem: the exit code stays 0
)

// Check is one line of doctor's report.
type Check struct {
	Name    string   `json:"name"`
	Result  string   `json:"result"` // ResultOK, ResultProblem, ResultWarn or ResultSkip
	Detail  string   `json:"detail"`
	Allowed []string `json:"allowed"`
	Fix     *string  `json:"fix"` // null for none

	code render.Code // of a problem: render.ExitNetwork for auth and network, else render.ExitUsage
}

// Report is what doctor found, and its JSON.
type Report struct {
	OK     bool    `json:"ok"` // no check is a problem (warnings allowed); set by the command from Err
	Checks []Check `json:"checks"`
}

// AddOK adds a check that passed.
func (r *Report) AddOK(name, format string, a ...any) {
	r.Checks = append(r.Checks, Check{Name: name, Result: ResultOK, Detail: fmt.Sprintf(format, a...), Allowed: []string{}})
}

// AddWarn adds a warning with the command that fixes it.
func (r *Report) AddWarn(name, fix, format string, a ...any) {
	r.Checks = append(r.Checks, Check{
		Name: name, Result: ResultWarn, Detail: fmt.Sprintf(format, a...), Allowed: []string{}, Fix: render.OrNull(fix),
	})
}

// AddSkip adds a check an earlier problem keeps from running.
func (r *Report) AddSkip(name, format string, a ...any) {
	r.Checks = append(r.Checks, Check{Name: name, Result: ResultSkip, Detail: fmt.Sprintf(format, a...), Allowed: []string{}})
}

// AddProblem adds err as a problem: auth and network errors (exit code 4)
// count as such, anything else as a config or board problem.
func (r *Report) AddProblem(name string, err error) {
	e := render.FromError(err)
	code := render.ExitUsage
	if e.Code == render.ExitNetwork {
		code = render.ExitNetwork
	}
	r.Checks = append(r.Checks, Check{
		Name: name, Result: ResultProblem, Detail: e.What, Allowed: render.OrEmpty(e.Allowed), Fix: render.OrNull(e.Fix), code: code,
	})
}

// Err is the command's error: nil when no check is a problem, else a
// short summary with exit code 4 when a problem is about auth or the
// network, else 1.
func (r *Report) Err() error {
	n, code := 0, render.ExitUsage
	for _, c := range r.Checks {
		if c.Result == ResultProblem {
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
		render.Plural(n, "problem", "problems"))
}

// Boards reads board schemas through the cache; *board.Loader is one.
type Boards interface {
	// Resolve reads the schema, from the cache when fresh, and re-reads a
	// cached one once when check fails on it.
	Resolve(ctx context.Context, ref board.Ref, check func(*board.Schema) error) (*board.Schema, error)
	// Invalidate drops the cached schema.
	Invalidate(ref board.Ref) error
	// Fetch reads the schema from GitHub and caches it.
	Fetch(ctx context.Context, ref board.Ref) (*board.Schema, error)
}

// TokenClient is what the token check needs of the GitHub client besides
// Query; *graph.Client has it. With a client that lacks it the check is
// skipped.
type TokenClient interface {
	QueryHeaders(ctx context.Context, query string, vars map[string]any, out any) (http.Header, error)
	Host() string
	TokenEnv() string
	AuthFix() string
}

// Env is what Run checks and what it checks with.
type Env struct {
	Version   string         // of this gh-dojo
	Config    *config.Config // nil when it does not load
	ConfigErr error          // why it does not load
	// Client sends the queries; the token check needs it to be a
	// TokenClient too.
	Client graph.Querier
	Boards Boards
	// CurrentRepo is the repository to check when board.repos is empty.
	CurrentRepo func() (issue.Repo, error)
	Now         func() time.Time // today, for the sprint check
}

// Run runs the checks in order. Requests: one Doctor query (token and
// repositories), plus the board schema when it is not cached (or the
// cached one has no iteration for today), plus one DoctorLabels query with
// labels.type_prefix or labels.area_prefix set, plus one
// DoctorVerbs query when a verb has labels or assigns someone.
func Run(ctx context.Context, env Env) *Report {
	r := &Report{}
	r.AddOK("version", "gh dojo %s", env.Version)

	cfg := env.Config
	if env.ConfigErr != nil {
		r.AddProblem("config", env.ConfigErr)
		cfg = nil
	} else {
		r.AddOK("config", "%s", configSummary(cfg))
		for _, d := range cfg.Deprecated {
			r.AddWarn("config", fmt.Sprintf("remove %s from %s", d.Key, cfg.Display),
				"%s does nothing: %s", d.Key, d.Why)
		}
	}

	repos, repoErr := listRepos(cfg, env.CurrentRepo)
	labels := newLabelsCheck(cfg)
	reach, viewer := checkToken(ctx, r, env.Client, repos)

	var schema *board.Schema
	sprintReported := false
	switch {
	case cfg == nil:
		r.AddSkip("board", "needs a config that loads")
	case !reach:
		r.AddSkip("board", "needs GitHub access (see token)")
	default:
		schema, sprintReported = checkBoard(ctx, r, env.Boards, cfg)
	}

	switch {
	case cfg == nil:
		r.AddSkip("repo", "needs a config that loads")
	case repoErr != nil:
		r.AddProblem("repo", repoErr)
	case !reach:
		r.AddSkip("repo", "needs GitHub access (see token)")
		if labels != nil {
			r.AddSkip("labels", "needs GitHub access (see token)")
		}
	default:
		for _, rp := range repos {
			checkRepo(r, cfg, rp)
		}
		if labels != nil {
			labels.run(ctx, r, env.Client, cfg, repos)
		}
	}

	if cfg != nil && schema != nil && !sprintReported {
		checkSprint(r, cfg, schema, env.Now(), func() (*board.Schema, error) {
			ref := board.RefOf(cfg)
			if err := env.Boards.Invalidate(ref); err != nil {
				return nil, err
			}
			return env.Boards.Fetch(ctx, ref)
		})
	}
	if cfg != nil {
		checkVerbs(ctx, r, env.Client, cfg, repos, reach, viewer)
	}
	return r
}

// configSummary is the detail of a config that loads.
func configSummary(cfg *config.Config) string {
	s := cfg.Display + ": "
	if cfg.Preset != "" {
		s += "preset " + cfg.Preset + ", "
	}
	return s + render.Plural(len(cfg.Verbs), "verb", "verbs")
}

// checkBoard checks the config against the board, from the cache when it
// is fresh; a mismatch on a cached schema re-reads the board first. Each
// mismatch is its own problem. It returns the schema, nil when the board
// could not be read, and whether one of the problems is fields.sprint (a
// field the board lacks or of another type), which leaves checkSprint
// nothing to add.
func checkBoard(ctx context.Context, r *Report, boards Boards, cfg *config.Config) (s *board.Schema, sprintReported bool) {
	s, err := boards.Resolve(ctx, board.RefOf(cfg), func(s *board.Schema) error { return board.Check(cfg, s) })
	if s == nil {
		r.AddProblem("board", err)
		return nil, false
	}
	if problems := board.Problems(cfg, s); len(problems) > 0 {
		for _, p := range problems {
			r.AddProblem("board", p.Err)
			sprintReported = sprintReported || p.Key == "fields.sprint"
		}
	} else if err != nil {
		r.AddProblem("board", err)
	} else {
		statuses := map[string]bool{}
		for _, ref := range cfg.StatusRefs() {
			statuses[ref.Status] = true
		}
		cached := ""
		if s.Cached {
			cached = " (cached)"
		}
		r.AddOK("board", "%s %q%s: field %s and %s match", s.Ref(), s.Title, cached, cfg.StatusField(),
			render.Plural(len(statuses), "status", "statuses"))
	}
	if s.Closed {
		r.AddProblem("board", render.Usage("board %s %q is closed", s.Ref(), s.Title).
			WithFix(fmt.Sprintf("gh project close %d --owner %s --undo", s.Number, s.Owner)))
	}
	return s, sprintReported
}

// checkSprint checks that the sprint field has an iteration for today, the
// date in board.timezone verbs use (iteration = "current"), as verbs see
// it: a completed iteration still covers its days. Without one on a
// cached schema it re-reads the board once with refetch before reporting
// a problem. It adds no line when fields.sprint is not set.
func checkSprint(r *Report, cfg *config.Config, s *board.Schema, now time.Time, refetch func() (*board.Schema, error)) {
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
		// Run reports it through board.Problems, with the allowed fields,
		// and skips this check; called alone, it names the fix only.
		r.AddProblem("sprint", s.WrongType(f, board.TypeIteration).WithAllowed().WithFix("edit fields.sprint in "+cfg.Display))
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
		r.AddProblem("sprint", render.Usage("%s has no iteration for today (%s)", f.Name, today).
			WithFix(fmt.Sprintf("add one in the %s field settings: %s/settings", f.Name, s.URL)))
		return
	}
	r.AddOK("sprint", "%s: today (%s) is in %s", f.Name, today, it.Title)
}

// named is err of one repository, its message prefixed with the name.
func named(name string, err error) *render.Error {
	e := render.FromError(err)
	return render.New(e.Code, "%s: %s", name, e.What).WithFix(e.Fix).WithCause(err)
}
