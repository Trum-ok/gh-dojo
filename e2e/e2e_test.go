//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// titlePrefix starts the title of every issue the tests create; sweep
// deletes the issues with it that an earlier run left behind.
const titlePrefix = "e2e: "

// bin is the gh-dojo binary TestMain builds from this module.
var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gh-dojo-e2e-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin = filepath.Join(dir, "gh-dojo")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-ldflags", "-X main.version=e2e", "-o", bin, "./cmd/gh-dojo")
	build.Dir = ".."
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build gh-dojo:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// sandbox is the repository and owner the tests work in.
type sandbox struct {
	repo  string // owner/name
	owner string // the boards' owner
	gql   *api.GraphQLClient
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	repo := os.Getenv("GH_DOJO_E2E_REPO")
	if repo == "" {
		t.Skip("GH_DOJO_E2E_REPO is not set: see the package doc")
	}
	owner, _, ok := strings.Cut(repo, "/")
	if !ok {
		t.Fatalf("GH_DOJO_E2E_REPO = %q, want owner/name", repo)
	}
	if o := os.Getenv("GH_DOJO_E2E_OWNER"); o != "" {
		owner = o
	}
	gql, err := api.DefaultGraphQLClient()
	if err != nil {
		t.Fatalf("GraphQL client: %v", err)
	}
	return &sandbox{repo: repo, owner: owner, gql: gql}
}

func TestPresets(t *testing.T) {
	s := newSandbox(t)
	s.sweep(t)
	for _, p := range []struct {
		preset, env string
		sprints     bool
	}{
		{"kanban", "GH_DOJO_E2E_KANBAN", false},
		{"sprints", "GH_DOJO_E2E_SPRINTS", true},
	} {
		t.Run(p.preset, func(t *testing.T) {
			v := os.Getenv(p.env)
			if v == "" {
				t.Skipf("%s is not set", p.env)
			}
			project, err := strconv.Atoi(v)
			if err != nil || project <= 0 {
				t.Fatalf("%s = %q, want a project number", p.env, v)
			}
			s.scenario(t, p.preset, project, p.sprints)
		})
	}
}

// scenario walks one preset through every command on its board, from init
// to closing the issues, and deletes the issues it created.
func (s *sandbox) scenario(t *testing.T, preset string, project int, sprints bool) {
	d := newDojo(t, s.repo)
	run := os.Getenv("GITHUB_RUN_ID")
	if run == "" {
		run = time.Now().UTC().Format("20060102-150405")
	}
	title := func(name string) string { return fmt.Sprintf("%s%s %s %s", titlePrefix, preset, run, name) }
	ref := func(n int) string { return fmt.Sprintf("%s#%d", s.repo, n) }

	// init writes the config at the root of the git repository and checks it
	// against the board; board.repos comes from the origin remote.
	var initOut struct {
		Written      bool     `json:"written"`
		Board        string   `json:"board"`
		Preset       string   `json:"preset"`
		Repos        []string `json:"repos"`
		MatchesBoard bool     `json:"matches_board"`
		Problems     []any    `json:"problems"`
	}
	d.json(&initOut, 0, "init", "--owner", s.owner, "--project", strconv.Itoa(project), "--preset", preset)
	if want := fmt.Sprintf("%s/%d", s.owner, project); !initOut.Written || initOut.Board != want ||
		initOut.Preset != preset || !slices.Equal(initOut.Repos, []string{s.repo}) || !initOut.MatchesBoard {
		t.Fatalf("init = %+v, want written, board %s, preset %s, repos [%s], matching the board", initOut, want, preset, s.repo)
	}

	var doctor struct {
		OK     bool    `json:"ok"`
		Checks []check `json:"checks"`
	}
	d.json(&doctor, 0, "doctor")
	if !doctor.OK {
		t.Fatalf("doctor found problems: %+v", doctor.Checks)
	}
	if sprints && !slices.Contains(doctor.Checks, check{Name: "sprint", Result: "ok"}) {
		t.Fatalf("doctor has no ok sprint check: %+v", doctor.Checks)
	}

	// new: A and C free, B blocked by A.
	a := s.create(t, d, title("A"), "--type", "enhancement", "--body", "Created by the gh-dojo e2e tests.")
	b := s.create(t, d, title("B"), "--type", "bug", "--blocked-by", strconv.Itoa(a))
	c := s.create(t, d, title("C"), "--type", "enhancement")

	if got := d.show(b); !blockedBy(got, ref(a), false) {
		t.Fatalf("show #%d: blocked_by = %+v, want %s unresolved", b, got.BlockedBy, ref(a))
	}

	// next: the free ones, not B.
	if got, ok := d.listUntil(func(is []listIssue) bool {
		return hasIssue(is, a) && hasIssue(is, c) && !hasIssue(is, b)
	}, "next", "--limit", "100"); !ok {
		t.Fatalf("next = %v, want #%d and #%d without #%d", numbers(got), a, c, b)
	}

	// A dry run plans and changes nothing; the blocker is a warning.
	dry := d.verb(0, "start", strconv.Itoa(b), "--dry-run")
	if dry.Issues[0].Outcome != "planned" || len(dry.Issues[0].Warnings) == 0 {
		t.Fatalf("start --dry-run #%d = %+v, want planned with a blocker warning", b, dry.Issues[0])
	}
	d.wantStatus(b, "Backlog")

	// The config init writes has no [session], so start's "Started. Session:
	// {session}" line drops out and only --comment is posted.
	d.verb(0, "start", strconv.Itoa(a), "--comment", "e2e: started")
	d.wantStatus(a, "In progress")
	if sprints {
		d.wantSprintToday(a)
	}

	var posted struct {
		Posted bool `json:"posted"`
	}
	d.json(&posted, 0, "comment", strconv.Itoa(a), "e2e: a comment")
	if !posted.Posted {
		t.Fatalf("comment #%d not posted", a)
	}

	// start warns about the blocker and still runs; wait --by links it again
	// (already there) and moves B to Waiting.
	started := d.verb(0, "start", strconv.Itoa(b))
	if len(started.Issues[0].Warnings) == 0 {
		t.Errorf("start #%d: no warning about its blocker #%d", b, a)
	}
	d.verb(0, "wait", strconv.Itoa(b), "--by", strconv.Itoa(a))
	if got := d.wantStatus(b, "Waiting"); !hasComment(got, fmt.Sprintf("Waiting for #%d.", a)) {
		t.Errorf("#%d comments = %+v, want Waiting for #%d.", b, got.Comments, a)
	}

	d.verb(0, "ready", strconv.Itoa(a), "--comment", "e2e: ready")
	d.wantStatus(a, "Ready")
	if got := d.show(b); !blockedBy(got, ref(a), true) || got.UnresolvedBlockers != 0 {
		t.Fatalf("show #%d: blocked_by = %+v, want %s resolved once it is Ready", b, got.BlockedBy, ref(a))
	}

	// block and unblock, without a status change.
	var link struct {
		Action    string   `json:"action"`
		Changed   []string `json:"changed"`
		Unchanged []string `json:"unchanged"`
	}
	d.json(&link, 0, "block", strconv.Itoa(c), "--by", strconv.Itoa(b))
	if !slices.Equal(link.Changed, []string{ref(b)}) {
		t.Fatalf("block #%d --by #%d: changed = %v", c, b, link.Changed)
	}
	if got := d.show(c); !blockedBy(got, ref(b), false) || got.Board.Status == nil || *got.Board.Status != "Backlog" {
		t.Fatalf("show #%d after block: %+v", c, got)
	}
	d.json(&link, 0, "block", strconv.Itoa(c), "--by", strconv.Itoa(b))
	if len(link.Changed) != 0 || !slices.Equal(link.Unchanged, []string{ref(b)}) {
		t.Fatalf("block #%d --by #%d again: %+v, want unchanged", c, b, link)
	}
	d.json(&link, 0, "unblock", strconv.Itoa(c), "--by", strconv.Itoa(b))
	if !slices.Equal(link.Changed, []string{ref(b)}) {
		t.Fatalf("unblock #%d --by #%d: changed = %v", c, b, link.Changed)
	}
	if got := d.show(c); len(got.BlockedBy) != 0 {
		t.Fatalf("show #%d after unblock: blocked_by = %+v", c, got.BlockedBy)
	}

	// A process check refuses: done starts from Ready or In progress.
	refused := d.verb(2, "done", strconv.Itoa(c))
	if refused.Issues[0].Outcome != "failed" {
		t.Fatalf("done #%d from Backlog = %+v, want failed", c, refused.Issues[0])
	}
	d.wantStatus(c, "Backlog")

	// done on two issues at once closes them.
	d.verb(0, "ready", strconv.Itoa(b))
	done := d.verb(0, "done", strconv.Itoa(a), strconv.Itoa(b))
	if done.Summary.Done != 2 {
		t.Fatalf("done #%d #%d: summary %+v", a, b, done.Summary)
	}
	for _, n := range []int{a, b} {
		got := d.wantStatus(n, "Done")
		if got.State != "closed" || got.StateReason == nil || *got.StateReason != "completed" {
			t.Errorf("#%d after done: state %s (%v), want closed as completed", n, got.State, got.StateReason)
		}
		if sprints {
			d.wantSprintToday(n)
		}
	}
	if got := d.show(a); !hasComment(got, "e2e: started") || !hasComment(got, "e2e: a comment") || !hasComment(got, "e2e: ready") {
		t.Errorf("#%d comments = %+v, want those of start, comment and ready", a, got.Comments)
	}

	// list hides the final status, --all shows it.
	if got, ok := d.listUntil(func(is []listIssue) bool {
		return hasIssue(is, c) && !hasIssue(is, a) && !hasIssue(is, b)
	}, "list", "--limit", "100"); !ok {
		t.Fatalf("list = %v, want #%d without the done #%d and #%d", numbers(got), c, a, b)
	}
	if got, ok := d.listUntil(func(is []listIssue) bool {
		return hasIssue(is, a) && hasIssue(is, b)
	}, "list", "--all", "--limit", "100"); !ok {
		t.Fatalf("list --all = %v, want #%d and #%d", numbers(got), a, b)
	}
}

// create runs new with title and the extra args, checks the issue is in
// Backlog and deletes it when the test ends.
func (s *sandbox) create(t *testing.T, d *dojo, title string, args ...string) int {
	t.Helper()
	var out struct {
		Number  int     `json:"number"`
		Repo    string  `json:"repo"`
		Created bool    `json:"created"`
		Status  *string `json:"status"`
	}
	d.json(&out, 0, append([]string{"new", title}, args...)...)
	if out.Number > 0 {
		n := out.Number
		t.Cleanup(func() { s.delete(t, n) })
	}
	if !out.Created || out.Repo != s.repo || out.Status == nil || *out.Status != "Backlog" {
		t.Fatalf("new %q = %+v, want created in %s, Backlog", title, out, s.repo)
	}
	return out.Number
}

// sweep deletes the e2e issues an earlier run left behind.
func (s *sandbox) sweep(t *testing.T) {
	t.Helper()
	owner, name, _ := strings.Cut(s.repo, "/")
	var cursor *string
	var left []int
	for {
		var resp struct {
			Repository struct {
				Issues struct {
					Nodes []struct {
						Number int    `json:"number"`
						Title  string `json:"title"`
					} `json:"nodes"`
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"issues"`
			} `json:"repository"`
		}
		err := s.gql.Do(`query($owner: String!, $name: String!, $after: String) {
			repository(owner: $owner, name: $name) {
				issues(first: 100, after: $after) { nodes { number title } pageInfo { hasNextPage endCursor } }
			}
		}`, map[string]any{"owner": owner, "name": name, "after": cursor}, &resp)
		if err != nil {
			t.Fatalf("list the issues of %s: %v", s.repo, err)
		}
		for _, n := range resp.Repository.Issues.Nodes {
			if strings.HasPrefix(n.Title, titlePrefix) {
				left = append(left, n.Number)
			}
		}
		if !resp.Repository.Issues.PageInfo.HasNextPage {
			break
		}
		end := resp.Repository.Issues.PageInfo.EndCursor
		cursor = &end
	}
	if len(left) > 0 {
		t.Logf("deleting %d issues an earlier run left: %v", len(left), left)
	}
	for _, n := range left {
		s.delete(t, n)
	}
}

// delete deletes issue n of the sandbox; a failure fails the test, so a
// token that cannot clean up is noticed.
func (s *sandbox) delete(t *testing.T, n int) {
	t.Helper()
	owner, name, _ := strings.Cut(s.repo, "/")
	var issue struct {
		Repository struct {
			Issue *struct {
				ID string `json:"id"`
			} `json:"issue"`
		} `json:"repository"`
	}
	err := s.gql.Do(`query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) { issue(number: $number) { id } }
	}`, map[string]any{"owner": owner, "name": name, "number": n}, &issue)
	if err != nil {
		t.Errorf("delete %s#%d: read it: %v", s.repo, n, err)
		return
	}
	if issue.Repository.Issue == nil {
		return
	}
	err = s.gql.Do(`mutation($id: ID!) { deleteIssue(input: {issueId: $id}) { clientMutationId } }`,
		map[string]any{"id": issue.Repository.Issue.ID}, nil)
	if err != nil {
		t.Errorf("delete %s#%d: %v", s.repo, n, err)
	}
}

// dojo runs the gh-dojo binary in a git working copy of the sandbox, with a
// schema cache of its own.
type dojo struct {
	t   *testing.T
	dir string
	env []string
}

func newDojo(t *testing.T, repo string) *dojo {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", "https://github.com/" + repo + ".git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return name == "GH_REPO" || name == "GH_DOJO_CACHE_DIR"
	})
	env = append(env, "GH_DOJO_CACHE_DIR="+t.TempDir())
	return &dojo{t: t, dir: dir, env: env}
}

// run runs gh dojo with args and fails the test unless it exits with code.
func (d *dojo) run(code int, args ...string) []byte {
	d.t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = d.dir, d.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	got := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			d.t.Fatalf("gh dojo %s: %v", strings.Join(args, " "), err)
		}
		got = exit.ExitCode()
	}
	d.t.Logf("gh dojo %s → %d", strings.Join(args, " "), got)
	if got != code {
		d.t.Fatalf("gh dojo %s: exit code %d, want %d\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), got, code, stdout.Bytes(), stderr.Bytes())
	}
	return stdout.Bytes()
}

// json runs gh dojo with args and --json and decodes its output into v.
func (d *dojo) json(v any, code int, args ...string) {
	d.t.Helper()
	out := d.run(code, append(args, "--json")...)
	if err := json.Unmarshal(out, v); err != nil {
		d.t.Fatalf("gh dojo %s --json: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// listUntil runs list or next (args) until ok accepts its issues, for up to
// listWait: GitHub lists a board's items a few seconds behind the writes,
// while show reads the issue itself and is up to date at once. It returns
// the last issues read and whether ok accepted them.
func (d *dojo) listUntil(ok func([]listIssue) bool, args ...string) ([]listIssue, bool) {
	d.t.Helper()
	deadline := time.Now().Add(listWait)
	for {
		var out struct {
			Issues []listIssue `json:"issues"`
		}
		d.json(&out, 0, args...)
		if ok(out.Issues) {
			return out.Issues, true
		}
		if time.Now().After(deadline) {
			return out.Issues, false
		}
		d.t.Logf("gh dojo %s: %v, not there yet; reading again", strings.Join(args, " "), numbers(out.Issues))
		time.Sleep(3 * time.Second)
	}
}

// listWait is how long listUntil waits for the board's items to catch up.
const listWait = 30 * time.Second

type verbResult struct {
	Issues []struct {
		Number   int      `json:"number"`
		Outcome  string   `json:"outcome"`
		Warnings []string `json:"warnings"`
	} `json:"issues"`
	Summary struct {
		Total  int `json:"total"`
		Done   int `json:"done"`
		Failed int `json:"failed"`
	} `json:"summary"`
}

// verb runs a process verb and checks it reports every issue.
func (d *dojo) verb(code int, args ...string) verbResult {
	d.t.Helper()
	var r verbResult
	d.json(&r, code, args...)
	if len(r.Issues) == 0 {
		d.t.Fatalf("gh dojo %s: no issues in the result", strings.Join(args, " "))
	}
	return r
}

type card struct {
	Number      int     `json:"number"`
	State       string  `json:"state"`
	StateReason *string `json:"state_reason"`
	Board       struct {
		OnBoard bool    `json:"on_board"`
		Status  *string `json:"status"`
	} `json:"board"`
	Sprint *struct {
		Iteration *struct {
			Title     string  `json:"title"`
			StartDate string  `json:"start_date"`
			EndDate   *string `json:"end_date"`
		} `json:"iteration"`
	} `json:"sprint"`
	BlockedBy          []blocker `json:"blocked_by"`
	UnresolvedBlockers int       `json:"unresolved_blockers"`
	Comments           []comment `json:"comments"`
}

type blocker struct {
	Ref      string `json:"ref"`
	Resolved bool   `json:"resolved"`
}

// check is a line of doctor; Detail is left out so checks compare by name
// and result.
type check struct {
	Name   string `json:"name"`
	Result string `json:"result"`
}

type comment struct {
	Author string `json:"author"`
	Text   string `json:"text"`
}

func (d *dojo) show(n int) card {
	d.t.Helper()
	var out struct {
		Issues []card `json:"issues"`
	}
	d.json(&out, 0, "show", strconv.Itoa(n))
	if len(out.Issues) != 1 {
		d.t.Fatalf("show #%d: %d issues", n, len(out.Issues))
	}
	return out.Issues[0]
}

func (d *dojo) wantStatus(n int, status string) card {
	d.t.Helper()
	c := d.show(n)
	if !c.Board.OnBoard || c.Board.Status == nil || *c.Board.Status != status {
		d.t.Fatalf("#%d: on board %v, status %v, want %s", n, c.Board.OnBoard, c.Board.Status, status)
	}
	return c
}

// wantSprintToday checks issue n is in the sprint that covers today (UTC,
// as the config init writes has no timezone).
func (d *dojo) wantSprintToday(n int) {
	d.t.Helper()
	c := d.show(n)
	today := time.Now().UTC().Format(time.DateOnly)
	if c.Sprint == nil || c.Sprint.Iteration == nil {
		d.t.Fatalf("#%d has no sprint, want the one covering %s", n, today)
	}
	it := c.Sprint.Iteration
	if it.StartDate > today || it.EndDate == nil || *it.EndDate < today {
		d.t.Fatalf("#%d is in %s (%s – %v), want the sprint covering %s", n, it.Title, it.StartDate, it.EndDate, today)
	}
}

func hasComment(c card, text string) bool {
	return slices.ContainsFunc(c.Comments, func(cm comment) bool { return cm.Text == text })
}

func blockedBy(c card, ref string, resolved bool) bool {
	return slices.Contains(c.BlockedBy, blocker{Ref: ref, Resolved: resolved})
}

type listIssue struct {
	Number int     `json:"number"`
	Repo   string  `json:"repo"`
	Status *string `json:"status"`
}

func hasIssue(issues []listIssue, n int) bool {
	return slices.ContainsFunc(issues, func(i listIssue) bool { return i.Number == n })
}

func numbers(issues []listIssue) []int {
	out := make([]int, len(issues))
	for i, is := range issues {
		out[i] = is.Number
	}
	return out
}
