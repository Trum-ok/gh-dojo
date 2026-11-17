package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// The checks run here against fakes: GitHub's answers by operation name
// and a board schema in memory. cmd/gh-dojo's TestDoctor runs the command
// on recorded and hand-crafted fixtures, with golden files of its output.

// fakeGitHub answers each query by its operation name with data (the
// "data" object) and err, and lists the token's scopes in its headers.
// Without token it is a bare graph.Querier, which cannot report scopes.
type fakeGitHub struct {
	data   map[string]string // operation → data
	errs   map[string]error  // operation → error
	scopes *string           // X-OAuth-Scopes; nil: not listed
	sent   []string          // the operations, in order
	vars   []map[string]any
}

func (f *fakeGitHub) Query(_ context.Context, query string, vars map[string]any, out any) error {
	op := operation(query)
	f.sent = append(f.sent, op)
	f.vars = append(f.vars, vars)
	if data, ok := f.data[op]; ok {
		if err := json.Unmarshal([]byte(data), out); err != nil {
			return err
		}
	} else if f.errs[op] == nil {
		return fmt.Errorf("unexpected query %s", op)
	}
	return f.errs[op]
}

// operation is the name of a query: "query Doctor(...) {" → "Doctor".
func operation(query string) string {
	name := strings.TrimPrefix(query, "query ")
	if i := strings.IndexAny(name, "( {"); i >= 0 {
		name = name[:i]
	}
	return name
}

// fakeToken is fakeGitHub with what the token check needs.
type fakeToken struct{ *fakeGitHub }

func (f fakeToken) QueryHeaders(ctx context.Context, query string, vars map[string]any, out any) (http.Header, error) {
	h := http.Header{}
	if f.scopes != nil {
		h.Set("X-OAuth-Scopes", *f.scopes)
	}
	return h, f.Query(ctx, query, vars, out)
}

func (fakeToken) Host() string     { return "github.com" }
func (fakeToken) TokenEnv() string { return "" }
func (fakeToken) AuthFix() string  { return "gh auth refresh -s project" }

// fakeBoards returns schema, and counts the fetches past the first read.
type fakeBoards struct {
	schema  *board.Schema
	err     error
	fetches int
}

func (b *fakeBoards) Resolve(_ context.Context, _ board.Ref, check func(*board.Schema) error) (*board.Schema, error) {
	if b.schema == nil {
		return nil, b.err
	}
	return b.schema, check(b.schema)
}

func (b *fakeBoards) Invalidate(board.Ref) error { return nil }

func (b *fakeBoards) Fetch(context.Context, board.Ref) (*board.Schema, error) {
	b.fetches++
	return b.schema, b.err
}

var today = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// kanbanBoard is acme's board 3 with the kanban preset's statuses.
func kanbanBoard() *board.Schema {
	var opts []board.Option
	for i, name := range []string{"Backlog", "In progress", "Waiting", "Ready", "Done"} {
		opts = append(opts, board.Option{ID: fmt.Sprint("o", i), Name: name})
	}
	return &board.Schema{
		Owner: "acme", OwnerType: "Organization", Number: 3, ID: "P1", Title: "Roadmap",
		URL: "https://github.com/orgs/acme/projects/3",
		Fields: []board.Field{
			{ID: "F1", Name: "Status", Type: board.TypeSingleSelect, Options: opts},
			{ID: "F2", Name: "Sprint", Type: board.TypeIteration, Iterations: []board.Iteration{
				{ID: "I1", Title: "Sprint 1", StartDate: "2026-11-09", Duration: 14},
			}},
		},
	}
}

const kanbanConfig = `
version = 1
preset = "kanban"

[board]
owner = "acme"
project = 3
repos = ["acme/api"]
`

func parse(t *testing.T, text string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(text), ".github/dojo.toml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func scopes(s string) *string { return &s }

// line is a check as "result name: detail", for comparing reports.
func line(c Check) string {
	s := c.Result + " " + c.Name + ": " + c.Detail
	if c.Fix != nil {
		s += " [fix: " + *c.Fix + "]"
	}
	return s
}

func lines(r *Report) []string {
	var out []string
	for _, c := range r.Checks {
		out = append(out, line(c))
	}
	return out
}

// wantLines fails unless r's lines are want, in order.
func wantLines(t *testing.T, r *Report, want ...string) {
	t.Helper()
	if got := lines(r); !slices.Equal(got, want) {
		t.Errorf("checks:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func env(cfg *config.Config, gh *fakeGitHub, boards *fakeBoards) Env {
	return Env{
		Version: "v1.0.0", Config: cfg, Client: fakeToken{gh}, Boards: boards,
		CurrentRepo: func() (issue.Repo, error) { return issue.Repo{}, errors.New("no git remote") },
		Now:         func() time.Time { return today },
	}
}

// TestRun: every check in order on a setup that works; the area labels a
// preset's prefix finds none of are a note, not a warning. The requests
// are the Doctor and DoctorLabels queries alone: the kanban preset's verbs
// have no labels or assignees.
func TestRun(t *testing.T) {
	gh := &fakeGitHub{scopes: scopes("project, repo"), data: map[string]string{
		"Doctor": `{"viewer": {"login": "octocat"},
			"repo0": {"nameWithOwner": "acme/api", "hasIssuesEnabled": true}}`,
		"DoctorLabels": `{"repo0": {"labels": {"totalCount": 1, "nodes": [{"name": "bug"}]}, "area": {"nodes": []}}}`,
	}}
	r := Run(context.Background(), env(parse(t, kanbanConfig), gh, &fakeBoards{schema: kanbanBoard()}))
	wantLines(t, r,
		"ok version: gh dojo v1.0.0",
		"ok config: .github/dojo.toml: preset kanban, 4 verbs",
		"ok token: github.com as octocat, with the project scope",
		`ok board: acme/3 "Roadmap": field Status and 5 statuses match`,
		"ok repo: acme/api (default): visible, issues enabled",
		"ok labels: acme/api: no area labels (area:…) yet: new --area fails until one exists",
	)
	if err := r.Err(); err != nil {
		t.Errorf("Err = %v, want nil", err)
	}
	if !slices.Equal(gh.sent, []string{"Doctor", "DoctorLabels"}) {
		t.Errorf("sent %v, want Doctor and DoctorLabels", gh.sent)
	}
	if v := gh.vars[0]; v["owner0"] != "acme" || v["name0"] != "api" {
		t.Errorf("Doctor vars = %v", v)
	}
}

// TestRunSprintFieldType: a sprint field of another type is one problem,
// the board's; the sprint check adds no second one.
func TestRunSprintFieldType(t *testing.T) {
	gh := &fakeGitHub{scopes: scopes("project, repo"), data: map[string]string{
		"Doctor": `{"viewer": {"login": "octocat"},
			"repo0": {"nameWithOwner": "acme/api", "hasIssuesEnabled": true}}`,
		"DoctorLabels": `{"repo0": {"labels": {"totalCount": 1, "nodes": [{"name": "bug"}]}, "area": {"nodes": []}}}`,
	}}
	cfg := parse(t, kanbanConfig+"\n[fields]\nsprint = \"Status\"\n")
	r := Run(context.Background(), env(cfg, gh, &fakeBoards{schema: kanbanBoard()}))
	var problems []string
	for _, c := range r.Checks {
		if c.Result == "problem" && strings.Contains(c.Detail, "not an iteration field") {
			problems = append(problems, line(c))
		}
	}
	if len(problems) != 1 || !strings.HasPrefix(problems[0], "problem board: ") {
		t.Errorf("problems about the sprint field:\n  %s\nwant one, the board's", strings.Join(problems, "\n  "))
	}
}

// TestRunNoConfig: the token is still checked; the board and the
// repositories need the config, and the Doctor query reads the viewer
// alone.
func TestRunNoConfig(t *testing.T) {
	gh := &fakeGitHub{scopes: scopes("project"), data: map[string]string{"Doctor": `{"viewer": {"login": "octocat"}}`}}
	e := env(nil, gh, &fakeBoards{})
	e.ConfigErr = render.Usage("no .github/dojo.toml").WithFix("gh dojo init")
	r := Run(context.Background(), e)
	wantLines(t, r,
		"ok version: gh dojo v1.0.0",
		"problem config: no .github/dojo.toml [fix: gh dojo init]",
		"ok token: github.com as octocat, with the project scope",
		"skip board: needs a config that loads",
		"skip repo: needs a config that loads",
	)
	if err := r.Err(); render.FromError(err).Code != render.ExitUsage {
		t.Errorf("Err = %v, want exit code 1", err)
	}
	if !slices.Equal(gh.sent, []string{"Doctor"}) || gh.vars[0] != nil {
		t.Errorf("sent %v with %v, want Doctor without variables", gh.sent, gh.vars)
	}
}

// TestRunUnreachable: a token GitHub refuses is exit code 4, and every
// check that needs GitHub is skipped without a request.
func TestRunUnreachable(t *testing.T) {
	gh := &fakeGitHub{errs: map[string]error{"Doctor": render.Network("GitHub rejected the token").WithFix("gh auth login")}}
	r := Run(context.Background(), env(parse(t, kanbanConfig), gh, &fakeBoards{}))
	wantLines(t, r,
		"ok version: gh dojo v1.0.0",
		"ok config: .github/dojo.toml: preset kanban, 4 verbs",
		"problem token: GitHub rejected the token [fix: gh auth login]",
		"skip board: needs GitHub access (see token)",
		"skip repo: needs GitHub access (see token)",
		"skip labels: needs GitHub access (see token)",
	)
	if err := r.Err(); render.FromError(err).Code != render.ExitNetwork {
		t.Errorf("Err = %v, want exit code 4", err)
	}
	if len(gh.sent) != 1 {
		t.Errorf("sent %v, want the Doctor query alone", gh.sent)
	}
}

// TestRunCurrentRepo: without board.repos the current repository is
// checked; without one, that is the repo check's problem.
func TestRunCurrentRepo(t *testing.T) {
	cfg := parse(t, strings.Replace(kanbanConfig, `repos = ["acme/api"]`, "", 1))
	gh := &fakeGitHub{scopes: scopes("project"), data: map[string]string{
		"Doctor":       `{"viewer": {"login": "octocat"}, "repo0": {"nameWithOwner": "acme/web", "hasIssuesEnabled": true}}`,
		"DoctorLabels": `{"repo0": {"labels": {"totalCount": 1, "nodes": [{"name": "area:ui"}]}, "area": {"nodes": [{"name": "area:ui"}]}}}`,
	}}
	e := env(cfg, gh, &fakeBoards{schema: kanbanBoard()})
	e.CurrentRepo = func() (issue.Repo, error) { return issue.Repo{Owner: "acme", Name: "web"}, nil }
	r := Run(context.Background(), e)
	if got := lines(r)[4:]; !slices.Equal(got, []string{
		"ok repo: acme/web (default), the current repository: visible, issues enabled",
		"ok labels: acme/web: 1 area label (area:…)",
	}) {
		t.Errorf("repo and labels: %q", got)
	}

	gh = &fakeGitHub{scopes: scopes("project"), data: map[string]string{"Doctor": `{"viewer": {"login": "octocat"}}`}}
	e = env(cfg, gh, &fakeBoards{schema: kanbanBoard()})
	e.CurrentRepo = func() (issue.Repo, error) { return issue.Repo{}, render.Usage("the current repository is unknown") }
	r = Run(context.Background(), e)
	if got := lines(r)[4]; got != "problem repo: the current repository is unknown" {
		t.Errorf("repo: %q", got)
	}
}

// TestCheckToken: the project scope is ok, read:project or none a problem
// with exit code 4 and the auth fix; a token that lists no scopes is ok,
// and a client that cannot report them skips the check.
func TestCheckToken(t *testing.T) {
	for _, tt := range []struct {
		scopes *string
		want   string
	}{
		{scopes("project, repo"), "ok token: github.com as octocat, with the project scope"},
		{nil, "ok token: github.com as octocat; no scopes listed (a fine-grained or app token): the board check shows its access"},
		{scopes("read:project, repo"), "problem token: github.com as octocat: the token has read:project but not project, " +
			"so commands that change the board fail [fix: gh auth refresh -s project]"},
		{scopes("repo, gist"), "problem token: github.com as octocat: the token lacks the project scope (has: gist, repo) [fix: gh auth refresh -s project]"},
		{scopes(""), "problem token: github.com as octocat: the token lacks the project scope (has: none) [fix: gh auth refresh -s project]"},
	} {
		gh := &fakeGitHub{scopes: tt.scopes, data: map[string]string{"Doctor": `{"viewer": {"login": "octocat"}}`}}
		r := &Report{}
		reach, viewer := checkToken(context.Background(), r, fakeToken{gh}, nil)
		if !reach || viewer != "octocat" || len(r.Checks) != 1 || line(r.Checks[0]) != tt.want {
			t.Errorf("reach %v, viewer %q, checks %q; want %q", reach, viewer, lines(r), tt.want)
		}
		if r.Checks[0].Result == ResultProblem && r.Checks[0].code != render.ExitNetwork {
			t.Errorf("%s: code %d, want 4", tt.want, r.Checks[0].code)
		}
	}

	r := &Report{}
	if reach, _ := checkToken(context.Background(), r, &fakeGitHub{}, nil); !reach ||
		line(r.Checks[0]) != "skip token: this GitHub client cannot report token scopes" {
		t.Errorf("a client without scopes: reach %v, %q", reach, lines(r))
	}
}

// TestRepoErrors: errors only on repository fields leave the rest of the
// answer usable; NOT_FOUND reads as not visible, any other type fails
// that repository alone.
func TestRepoErrors(t *testing.T) {
	gql := func(items ...api.GraphQLErrorItem) error { return &api.GraphQLError{Errors: items} }
	failed, partial := repoErrors(gql(
		api.GraphQLErrorItem{Type: "NOT_FOUND", Path: []any{"repo0"}},
		api.GraphQLErrorItem{Type: "FORBIDDEN", Path: []any{"repo2", "nameWithOwner"}},
	))
	if !partial || len(failed) != 1 || !failed[2] {
		t.Errorf("failed %v, partial %v; want repo2 failed", failed, partial)
	}
	for _, err := range []error{
		nil,
		errors.New("network down"),
		gql(api.GraphQLErrorItem{Type: "FORBIDDEN", Path: []any{"viewer"}}),
		gql(api.GraphQLErrorItem{Type: "FORBIDDEN", Path: []any{"repo01"}}),
		gql(api.GraphQLErrorItem{Message: "parse error"}),
	} {
		if _, partial := repoErrors(err); partial {
			t.Errorf("%v: partial, want not", err)
		}
	}
}

// TestCheckRepo: each state of a repository as the Doctor query found it.
func TestCheckRepo(t *testing.T) {
	cfg := parse(t, kanbanConfig)
	type found = struct {
		NameWithOwner    string `json:"nameWithOwner"`
		HasIssuesEnabled bool   `json:"hasIssuesEnabled"`
	}
	api := issue.Repo{Owner: "acme", Name: "api"}
	for _, tt := range []struct {
		rp   repo
		want string
	}{
		{repo{Repo: api, Default: true, Found: &found{"acme/api", true}}, "ok repo: acme/api (default): visible, issues enabled"},
		{repo{Repo: api, Found: &found{"Acme/API", true}},
			`warn repo: acme/api: visible, issues enabled; GitHub spells it Acme/API [fix: write it as "Acme/API" in board.repos in .github/dojo.toml]`},
		{repo{Repo: api, Found: &found{"acme/api", false}}, "problem repo: acme/api has issues disabled [fix: gh repo edit acme/api --enable-issues]"},
		{repo{Repo: api}, "problem repo: acme/api is not found or not visible to this token [fix: edit board.repos in .github/dojo.toml]"},
		{repo{Repo: api, Default: true, Current: true}, "problem repo: acme/api (default), the current repository, is not found or not visible " +
			`to this token [fix: add repos = ["owner/name"] under [board] in .github/dojo.toml]`},
		{repo{Repo: api, Err: render.Network("SAML SSO").WithFix("authorize the token")},
			"problem repo: acme/api: SAML SSO [fix: authorize the token]"},
	} {
		r := &Report{}
		checkRepo(r, cfg, &tt.rp)
		if got := line(r.Checks[0]); got != tt.want {
			t.Errorf("got  %s\nwant %s", got, tt.want)
		}
	}
}

// TestLabelsFound: label names come from the first page and the
// searches; they are complete only when the first page has every label.
func TestLabelsFound(t *testing.T) {
	c := &labelsCheck{groups: []labelGroup{{"type", "labels.type_prefix", "type: "}, {"area", "labels.area_prefix", "area:"}}}
	for _, tt := range []struct {
		name     string
		answer   string
		names    int
		complete bool
	}{
		{"one page", `{"labels": {"totalCount": 2, "nodes": [{"name": "type: bug"}, {"name": "area:cli"}]},
			"type": {"nodes": [{"name": "type: bug"}]}, "area": {"nodes": [{"name": "area:cli"}]}}`, 2, true},
		// Search found these past the first page, but not necessarily all.
		{"more labels", `{"labels": {"totalCount": 101, "nodes": [{"name": "x"}]},
			"type": {"nodes": [{"name": "Type: Bug"}]}, "area": {"nodes": [{"name": "area:cli"}, {"name": "area:db"}]}}`, 4, false},
		{"repository null", `null`, 0, true},
	} {
		f := c.found(json.RawMessage(tt.answer))
		if len(f.names) != tt.names || f.complete != tt.complete {
			t.Errorf("%s: %d names, complete %v; want %d, %v", tt.name, len(f.names), f.complete, tt.names, tt.complete)
		}
	}
}

// TestLabelsCheck: in one DoctorLabels query for the usable repositories,
// how many labels each prefix has, counted regardless of case; none with a
// prefix the config sets is a warning, and past 100 labels a count is a
// lower bound.
func TestLabelsCheck(t *testing.T) {
	cfg := parse(t, strings.Replace(kanbanConfig, `repos = ["acme/api"]`, `repos = ["acme/api", "acme/big", "acme/off"]`, 1)+`
[labels]
type_prefix = "kind/"
area_prefix = "svc:"
`)
	usable := func(name string, ok bool) *repo {
		rp := &repo{Repo: issue.Repo{Owner: "acme", Name: name}}
		rp.Found = &struct {
			NameWithOwner    string `json:"nameWithOwner"`
			HasIssuesEnabled bool   `json:"hasIssuesEnabled"`
		}{"acme/" + name, ok}
		return rp
	}
	repos := []*repo{usable("api", true), usable("big", true), usable("off", false)}
	gh := &fakeGitHub{data: map[string]string{
		"DoctorLabels": `{
			"repo0": {"labels": {"totalCount": 3, "nodes": [{"name": "Kind/Bug"}, {"name": "kind/docs"}, {"name": "bug"}]},
				"type": {"nodes": [{"name": "Kind/Bug"}, {"name": "kind/docs"}]}, "area": {"nodes": []}},
			"repo1": {"labels": {"totalCount": 150, "nodes": [{"name": "bug"}]}, "type": {"nodes": []},
				"area": {"nodes": [{"name": "svc:db"}, {"name": "svc:ui"}]}}}`,
	}}
	r := &Report{}
	newLabelsCheck(cfg).run(context.Background(), r, gh, cfg, repos)
	wantLines(t, r,
		`warn labels: acme/api: no label starts with labels.area_prefix "svc:", so gh dojo new --area finds nothing [fix: gh label create "svc:<name>" -R acme/api, or set labels.area_prefix = "" in .github/dojo.toml]`,
		"ok labels: acme/api: 2 type labels (kind/…)",
		"ok labels: acme/big: could not confirm type labels (kind/…): none among the first 100 of 150 labels or by search, at least 2 area labels (svc:…)",
		"skip labels: acme/off: needs the repository (see repo)",
	)
	if !slices.Equal(gh.sent, []string{"DoctorLabels"}) {
		t.Errorf("sent %v, want DoctorLabels alone", gh.sent)
	}
	if v := gh.vars[0]; v["owner0"] != "acme" || v["name1"] != "big" || v["owner2"] != nil || v["type"] != "kind/" || v["area"] != "svc:" {
		t.Errorf("DoctorLabels vars = %v, want repo0 and repo1 only", v)
	}

	// A failed query is the labels check's own problem, once.
	gh = &fakeGitHub{errs: map[string]error{"DoctorLabels": render.Network("GitHub is down")}}
	r = &Report{}
	newLabelsCheck(cfg).run(context.Background(), r, gh, cfg, repos)
	wantLines(t, r, "problem labels: GitHub is down")
}

// TestVerbsCheckLabels: doctor looks up each label verbs add or remove by
// its exact spelling, and a label a check names only when no entry names
// it in any case, so +bug and Bug are one label; checks come after the
// effects whatever the verb order.
func TestVerbsCheckLabels(t *testing.T) {
	cfg := parse(t, kanbanConfig+`
[verbs.poke]
when = "Ask how it goes"
comment = "{comment}"
refuse_labels = ["Bug", "wip"]

[verbs.tag]
when = "It is a bug"
labels = ["+bug", "-triage"]
from_labels = ["WIP", "ready"]

[verbs.retag]
when = "It is a Bug"
labels = ["+Bug"]
`)
	want := []string{"bug", "triage", "Bug", "wip", "ready"}
	if got := newVerbsCheck(cfg, "Trum-ok").labels; !slices.Equal(got, want) {
		t.Errorf("labels = %q, want %q", got, want)
	}
}

// TestVerbsCheckUsers: doctor checks the users verbs assign and unassign
// by login or "@me" (the viewer), each once regardless of case; --assignee
// and unassign = "all" name nobody to check.
func TestVerbsCheckUsers(t *testing.T) {
	cfg := parse(t, kanbanConfig+`
[verbs.pass]
when = "Pass it on"
assign = "{assignee}"
unassign = "@me"

[verbs.hand-back]
when = "Back to the author alone"
assign = "Octocat"
unassign = "all"

[verbs.leave]
when = "Octocat leaves"
unassign = "octocat"
`)
	if got := newVerbsCheck(cfg, "Trum-ok").users; !slices.Equal(got, []string{"Trum-ok", "Octocat"}) {
		t.Errorf("users = %q, want Trum-ok (@me) and Octocat", got)
	}
}

// TestCheckVerbs: one DoctorVerbs query on the default repository. A label
// to add in another case, one to remove that is missing, a user to assign
// who cannot be, and a milestone matching none are problems; a user to
// unassign who cannot be assigned is a warning. Without any, one ok line.
func TestCheckVerbs(t *testing.T) {
	extra := `
[verbs.tag]
when = "It is a bug"
labels = ["+Bug", "-triage"]
assign = "@me"

[verbs.hand]
when = "Hand it over"
assign = "monalisa"
unassign = "hubot"

[verbs.ship]
when = "Due in v1"
milestone = "v1"
`
	cfg := parse(t, kanbanConfig+extra)
	def := &repo{Repo: issue.Repo{Owner: "acme", Name: "api"}, Default: true}
	def.Found = &struct {
		NameWithOwner    string `json:"nameWithOwner"`
		HasIssuesEnabled bool   `json:"hasIssuesEnabled"`
	}{"acme/api", true}
	gh := &fakeGitHub{data: map[string]string{"DoctorVerbs": `{"repository": {
		"labels": {"nodes": [{"name": "bug"}]},
		"label0": null, "label1": null,
		"user0": {"nodes": [{"login": "octocat"}]}, "user1": {"nodes": []}, "user2": {"nodes": [{"login": "hubot2"}]},
		"milestones": {"nodes": [{"id": "M1", "title": "v2"}]}}}`}}
	r := &Report{}
	checkVerbs(context.Background(), r, gh, cfg, []*repo{def}, true, "octocat")
	wantLines(t, r,
		`problem verbs: verbs.tag.labels: no label "Bug" in acme/api [fix: write it as "bug" in verbs.tag.labels]`,
		`problem verbs: verbs.tag.labels: no label "triage" in acme/api, so -triage never removes anything [fix: edit verbs.tag.labels, or gh label create "triage" -R acme/api]`,
		"problem verbs: verbs.hand.assign: monalisa cannot be assigned issues in acme/api [fix: check the login, or give monalisa access to acme/api]",
		"warn verbs: verbs.hand.unassign: hubot cannot be assigned issues in acme/api: the verb removes them only if they are assigned already [fix: check the login in verbs.hand.unassign]",
		`problem verbs: verbs.ship.milestone: unknown milestone "v1": no open milestone in acme/api has that title or one starting with it; did you mean "v2"? [fix: if it is new, create it: gh api repos/acme/api/milestones -f title="v1", or fix verbs.ship.milestone]`,
	)
	if v := gh.vars[0]; v["label0"] != "Bug" || v["user0"] != "octocat" || v["user1"] != "monalisa" {
		t.Errorf("DoctorVerbs vars = %v", v)
	}

	gh.data["DoctorVerbs"] = `{"repository": {"labels": {"nodes": []},
		"label0": {"name": "Bug"}, "label1": {"name": "triage"},
		"user0": {"nodes": [{"login": "octocat"}]}, "user1": {"nodes": [{"login": "MonaLisa"}]}, "user2": {"nodes": [{"login": "hubot"}]},
		"milestones": {"nodes": [{"id": "M1", "title": "v1.0"}]}}}`
	r = &Report{}
	checkVerbs(context.Background(), r, gh, cfg, []*repo{def}, true, "octocat")
	wantLines(t, r, "ok verbs: acme/api: 2 labels exist, 3 assignees can be assigned, 1 milestone is open")

	// Without GitHub or the default repository: skipped, no request.
	gh.sent = nil
	r = &Report{}
	checkVerbs(context.Background(), r, gh, cfg, []*repo{def}, false, "")
	checkVerbs(context.Background(), r, gh, cfg, []*repo{{Repo: def.Repo, Default: true}}, true, "octocat")
	wantLines(t, r, "skip verbs: needs GitHub access (see token)", "skip verbs: needs the default repository (see repo)")
	if len(gh.sent) != 0 {
		t.Errorf("sent %v, want nothing", gh.sent)
	}

	// The preset's verbs alone name nothing to check: no line, no request.
	r = &Report{}
	checkVerbs(context.Background(), r, gh, parse(t, kanbanConfig), []*repo{def}, true, "octocat")
	if len(r.Checks) != 0 || len(gh.sent) != 0 {
		t.Errorf("checks %q, sent %v; want none", lines(r), gh.sent)
	}
}

// TestCheckSprint: fields.sprint must be an iteration field with an
// iteration for today; a cached schema without one is re-read once.
func TestCheckSprint(t *testing.T) {
	cfg := parse(t, kanbanConfig+"\n[fields]\nsprint = \"Sprint\"\n")
	r := &Report{}
	checkSprint(r, cfg, kanbanBoard(), today, nil)
	checkSprint(r, cfg, kanbanBoard(), today.AddDate(0, 1, 0), nil)
	cfg.Fields["sprint"] = "Status"
	checkSprint(r, cfg, kanbanBoard(), today, nil)
	wantLines(t, r,
		"ok sprint: Sprint: today (2026-11-11, UTC) is in Sprint 1",
		"problem sprint: Sprint has no iteration for today (2026-12-11, UTC) [fix: add one in the Sprint field settings: https://github.com/orgs/acme/projects/3/settings]",
		`problem sprint: field "Status" on board acme/3 is SINGLE_SELECT, not an iteration field [fix: edit fields.sprint in .github/dojo.toml]`,
	)

	cfg.Fields["sprint"] = "Sprint"
	stale := kanbanBoard()
	stale.Cached, stale.Fields[1].Iterations = true, nil
	boards := &fakeBoards{schema: kanbanBoard()}
	r = &Report{}
	checkSprint(r, cfg, stale, today, func() (*board.Schema, error) { return boards.Fetch(context.Background(), board.Ref{}) })
	wantLines(t, r, "ok sprint: Sprint: today (2026-11-11, UTC) is in Sprint 1")
	if boards.fetches != 1 {
		t.Errorf("%d re-reads, want 1", boards.fetches)
	}

	// A fresh schema is not re-read.
	fresh := kanbanBoard()
	fresh.Fields[1].Iterations = nil
	r = &Report{}
	checkSprint(r, cfg, fresh, today, func() (*board.Schema, error) {
		t.Error("a fresh schema was re-read")
		return nil, nil
	})
	wantLines(t, r, "problem sprint: Sprint has no iteration for today (2026-11-11, UTC) [fix: add one in the Sprint field settings: https://github.com/orgs/acme/projects/3/settings]")

	// No line without fields.sprint, nor for a field the board lacks:
	// board.Problems reports that.
	r = &Report{}
	checkSprint(r, parse(t, kanbanConfig), kanbanBoard(), today, nil)
	cfg.Fields["sprint"] = "Nope"
	checkSprint(r, cfg, kanbanBoard(), today, nil)
	wantLines(t, r)

	// A completed iteration still covers its days, as verbs see it.
	cfg.Fields["sprint"] = "Sprint"
	done := kanbanBoard()
	done.Fields[1].Iterations = append([]board.Iteration{
		{ID: "I0", Title: "Sprint 0", StartDate: "2026-10-26", Duration: 14, Completed: true},
	}, done.Fields[1].Iterations...)
	r = &Report{}
	checkSprint(r, cfg, done, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), nil)
	wantLines(t, r, "ok sprint: Sprint: today (2026-11-01, UTC) is in Sprint 0")

	// board.timezone: 22:30 UTC on Nov 22 is Nov 23 in Moscow, after
	// Sprint 1.
	tz := parse(t, strings.Replace(kanbanConfig, "project = 3\n", "project = 3\ntimezone = \"Europe/Moscow\"\n", 1)+
		"\n[fields]\nsprint = \"Sprint\"\n")
	r = &Report{}
	checkSprint(r, tz, kanbanBoard(), time.Date(2026, 11, 22, 22, 30, 0, 0, time.UTC), nil)
	wantLines(t, r, "problem sprint: Sprint has no iteration for today (2026-11-23, Europe/Moscow) [fix: add one in the Sprint field settings: https://github.com/orgs/acme/projects/3/settings]")
}

// TestErr: a problem about auth or the network makes the exit code 4,
// else 1; warnings and skips are no error.
func TestErr(t *testing.T) {
	r := &Report{}
	r.AddOK("version", "gh dojo v1")
	r.AddWarn("repo", "fix it", "spelled otherwise")
	r.AddSkip("board", "needs GitHub access")
	if err := r.Err(); err != nil {
		t.Fatalf("Err = %v, want nil", err)
	}
	r.AddProblem("board", errors.New("unexpected"))
	if e := render.FromError(r.Err()); e.Code != render.ExitUsage || e.What != "doctor found 1 problem; fix as shown above, then run gh dojo doctor again" {
		t.Errorf("Err = %d %q", e.Code, e.What)
	}
	r.AddProblem("token", render.Network("401"))
	if e := render.FromError(r.Err()); e.Code != render.ExitNetwork || !strings.HasPrefix(e.What, "doctor found 2 problems") {
		t.Errorf("Err = %d %q", e.Code, e.What)
	}
	if c := r.Checks[3]; c.Fix != nil || c.Allowed == nil {
		t.Errorf("a problem without a fix or allowed values: fix %v, allowed %v; want null and []", c.Fix, c.Allowed)
	}
}
