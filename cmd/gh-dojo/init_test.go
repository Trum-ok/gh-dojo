package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// Recorded board fixtures init reads, relative to cmd/gh-dojo.
const (
	boardFixtures          = "../../internal/board/testdata/graphql/"
	initOrgBoardFixture    = boardFixtures + "org_board.json"
	boardNotFoundFixture   = boardFixtures + "board_not_found.json"
	ownerNotFoundFixture   = boardFixtures + "owner_not_found.json"
	userProjectsFixture    = boardFixtures + "projects_user.json"
	orgProjectsFixture     = boardFixtures + "projects_org.json"
	noOwnerProjectsFixture = boardFixtures + "projects_owner_not_found.json"
)

// withInit sets init up in a fresh repository root (a temp dir with .git,
// returned) as the current directory, with current as the current
// repository ("" = unknown) and the fixtures replayed. Without fixtures
// every request fails. requests counts the requests sent.
func withInit(t *testing.T, current string, fixtures ...string) (root string, requests func() int) {
	t.Helper()
	currentRepo := func() (string, error) {
		if current == "" {
			return "", errors.New("no git remote")
		}
		return current, nil
	}
	if len(fixtures) == 0 {
		var q countingClient
		saved := newApp
		t.Cleanup(func() { newApp = saved })
		newApp = func() *App { return &App{Client: &q, Loader: board.NewLoader(&q), CurrentRepo: currentRepo} }
		requests = func() int { return q.n }
	} else {
		srv := withApp(t, fixtures...)
		replay := newApp // withApp's; its cleanup restores the original
		newApp = func() *App {
			a := replay()
			a.CurrentRepo = currentRepo
			return a
		}
		requests = srv.Requests
	}
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	inDir(t, root)
	return root, requests
}

// inDir makes dir init's current directory.
func inDir(t *testing.T, dir string) {
	t.Helper()
	saved := workDir
	t.Cleanup(func() { workDir = saved })
	workDir = func() (string, error) { return dir, nil }
}

func configPath(root string) string { return filepath.Join(root, filepath.FromSlash(config.FileName)) }

// readConfig returns the config file under root, "" when there is none.
func readConfig(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(configPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInitList(t *testing.T) {
	tests := []struct {
		name    string
		current string
		fixture string
		args    []string
		code    render.Code
	}{
		// The owner of the current repository; the fix names its board.
		{"list_user", "Trum-ok/gh-dojo", userProjectsFixture, nil, render.ExitUsage},
		// No clear pick among 15 open projects: the fix has a placeholder.
		{"list_org", "", orgProjectsFixture, []string{"--owner", "github", "--repo", "github/roadmap"}, render.ExitUsage},
		{"list_owner_not_found", "", noOwnerProjectsFixture, []string{"--owner", "no-such-user-xyz-123987"}, render.ExitNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, requests := withInit(t, tt.current, tt.fixture)
			code, out := runOut(append([]string{"init"}, tt.args...)...)
			if code != tt.code {
				t.Errorf("code = %d, want %d", code, tt.code)
			}
			testutil.Golden(t, "init/"+tt.name, out)
			if n := requests(); n != 1 {
				t.Errorf("requests = %d, want 1", n)
			}
			if got := readConfig(t, root); got != "" {
				t.Errorf("listing wrote a config:\n%s", got)
			}
		})
	}
}

func TestInitWrite(t *testing.T) {
	root, requests := withInit(t, "Trum-ok/gh-dojo", boardFixture)
	code, out := runOut("init", "--project", "5")
	if code != render.ExitOK {
		t.Errorf("code = %d, want 0", code)
	}
	testutil.Golden(t, "init/write", out)
	written := readConfig(t, root)
	testutil.Golden(t, "init/write_file", []byte(written))
	if n := requests(); n != 1 {
		t.Errorf("requests = %d, want 1 (the schema)", n)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("the written config does not load: %v", err)
	}
	if cfg.Board.Owner != "Trum-ok" || cfg.Board.Project != 5 || len(cfg.Board.Repos) != 1 ||
		cfg.Board.Repos[0] != "Trum-ok/gh-dojo" || cfg.Preset != "kanban" {
		t.Errorf("board %+v, preset %q", cfg.Board, cfg.Preset)
	}

	// Not again without --force: nothing is sent and the file stays.
	if err := os.WriteFile(configPath(root), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out = runOut("init", "--project", "5")
	if code != render.ExitUsage {
		t.Errorf("existing file: code = %d, want 1", code)
	}
	testutil.Golden(t, "init/exists", out)
	if got := readConfig(t, root); got != "# mine\n" {
		t.Errorf("existing file changed:\n%s", got)
	}
	if n := requests(); n != 1 {
		t.Errorf("existing file: requests = %d, want none after the first run", n-1)
	}
}

// TestInitSprints: --preset sprints on a board with the statuses and a
// Sprint iteration field writes a config that loads and matches; on the
// gh-dojo board, which has no iteration field, the file is kept with the
// missing field as its one problem.
func TestInitSprints(t *testing.T) {
	root, requests := withInit(t, "acme/shop", iterationBoardFixture)
	code, out := runOut("init", "--preset", "sprints", "--owner", "acme", "--project", "3")
	if code != render.ExitOK {
		t.Errorf("code = %d, want 0", code)
	}
	testutil.Golden(t, "init/sprints_write", out)
	testutil.Golden(t, "init/sprints_write_file", []byte(readConfig(t, root)))
	if n := requests(); n != 1 {
		t.Errorf("requests = %d, want 1 (the schema)", n)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("the written config does not load: %v", err)
	}
	if cfg.Preset != "sprints" || cfg.SprintField() != "Sprint" || cfg.Board.Owner != "acme" || cfg.Board.Project != 3 {
		t.Errorf("board %+v, preset %q, sprint field %q", cfg.Board, cfg.Preset, cfg.SprintField())
	}

	root, _ = withInit(t, "Trum-ok/gh-dojo", boardFixture)
	code, out = runOut("init", "--preset", "sprints", "--project", "5")
	if code != render.ExitUsage {
		t.Errorf("no sprint field: code = %d, want 1", code)
	}
	testutil.Golden(t, "init/sprints_no_sprint_field", out)
	if _, err := config.Load(root); err != nil {
		t.Errorf("the config is not kept or does not load: %v", err)
	}
}

func TestInitForce(t *testing.T) {
	// The schema cached by the first run is not used: init reads the board.
	root, requests := withInit(t, "Trum-ok/gh-dojo", boardFixture, boardFixture)
	if err := os.MkdirAll(filepath.Dir(configPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(root), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if code, out := runOut("init", "--project", "5", "--force"); code != render.ExitOK {
			t.Fatalf("code = %d:\n%s", code, out)
		}
	}
	testutil.Golden(t, "init/write_file", []byte(readConfig(t, root)))
	if n := requests(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestInitDryRun(t *testing.T) {
	root, _ := withInit(t, "", boardFixture)
	if err := os.MkdirAll(filepath.Dir(configPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(root), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No current repository: board.repos is left out. An existing file is
	// no reason to refuse a dry run, which writes nothing.
	code, out := runOut("init", "--owner", "Trum-ok", "--project", "5", "--dry-run")
	if code != render.ExitOK {
		t.Errorf("code = %d, want 0", code)
	}
	testutil.Golden(t, "init/dry_run", out)
	if got := readConfig(t, root); got != "# mine\n" {
		t.Errorf("dry run changed the file:\n%s", got)
	}
}

func TestInitSubdirectory(t *testing.T) {
	root, _ := withInit(t, "Trum-ok/gh-dojo", boardFixture)
	sub := filepath.Join(root, "cmd", "app")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	inDir(t, sub)
	code, out := runOut("init", "--project", "5")
	if code != render.ExitOK {
		t.Errorf("code = %d:\n%s", code, out)
	}
	testutil.Golden(t, "init/subdirectory", out)
	if _, err := config.Load(sub); err != nil {
		t.Errorf("the config is not found from %s: %v", sub, err)
	}
}

func TestInitMismatch(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		args    []string
	}{
		// An organization board with none of the kanban statuses.
		{"mismatch_org", initOrgBoardFixture, []string{"--owner", "github", "--project", "4247", "--repo", "acme/shop"}},
		// The gh-dojo board without Waiting.
		{"mismatch_waiting", editedBoard(t, "no_waiting", func(status map[string]any) {
			var kept []any
			for _, o := range status["options"].([]any) {
				if o.(map[string]any)["name"] != "Waiting" {
					kept = append(kept, o)
				}
			}
			status["options"] = kept
		}), []string{"--project", "5"}},
		// The gh-dojo board with Status renamed to Stage.
		{"mismatch_field", editedBoard(t, "stage", func(status map[string]any) {
			status["name"] = "Stage"
		}), []string{"--project", "5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, requests := withInit(t, "Trum-ok/gh-dojo", tt.fixture)
			code, out := runOut(append([]string{"init"}, tt.args...)...)
			if code != render.ExitUsage {
				t.Errorf("code = %d, want 1", code)
			}
			testutil.Golden(t, "init/"+tt.name, out)
			if n := requests(); n != 1 {
				t.Errorf("requests = %d, want 1", n)
			}
			if _, err := config.Load(root); err != nil {
				t.Errorf("the config is not kept or does not load: %v", err)
			}
		})
	}
}

func TestInitMismatchDryRun(t *testing.T) {
	root, _ := withInit(t, "", initOrgBoardFixture)
	code, out := runOut("init", "--owner", "github", "--project", "4247", "--dry-run")
	if code != render.ExitUsage {
		t.Errorf("code = %d, want 1", code)
	}
	testutil.Golden(t, "init/mismatch_dry_run", out)
	if got := readConfig(t, root); got != "" {
		t.Errorf("dry run wrote:\n%s", got)
	}
}

func TestInitNotFound(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		args    []string
	}{
		{"board_not_found", boardNotFoundFixture, []string{"--owner", "Trum-ok", "--project", "99"}},
		{"owner_not_found", ownerNotFoundFixture, []string{"--owner", "no-such-user-xyz-123987", "--project", "1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := withInit(t, "", tt.fixture)
			code, out := runOut(append([]string{"init"}, tt.args...)...)
			if code != render.ExitNotFound {
				t.Errorf("code = %d, want 3", code)
			}
			testutil.Golden(t, "init/"+tt.name, out)
			if got := readConfig(t, root); got != "" {
				t.Errorf("wrote a config for a missing board:\n%s", got)
			}
		})
	}
}

// TestInitUsage covers errors found before any request.
func TestInitUsage(t *testing.T) {
	tests := []struct {
		name    string
		current string
		args    []string
	}{
		{"no_owner", "", nil},
		{"bad_preset", "Trum-ok/gh-dojo", []string{"--preset", "scrum"}},
		{"bad_project", "Trum-ok/gh-dojo", []string{"--project", "0"}},
		{"bad_repo", "Trum-ok/gh-dojo", []string{"--project", "5", "--repo", "gh-dojo"}},
		{"bad_owner", "", []string{"--owner", "../etc", "--project", "5"}},
		{"extra_arg", "Trum-ok/gh-dojo", []string{"5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, requests := withInit(t, tt.current)
			code, out := runOut(append([]string{"init"}, tt.args...)...)
			if code != render.ExitUsage {
				t.Errorf("code = %d, want 1", code)
			}
			testutil.Golden(t, "init/"+tt.name, out)
			if n := requests(); n != 0 {
				t.Errorf("requests = %d, want 0", n)
			}
		})
	}
}

func TestInitNotARepository(t *testing.T) {
	_, requests := withInit(t, "Trum-ok/gh-dojo")
	inDir(t, t.TempDir()) // no .git here or above
	code, out := runOut("init", "--project", "5")
	if code != render.ExitUsage {
		t.Errorf("code = %d, want 1", code)
	}
	testutil.Golden(t, "init/not_a_repository", out)
	if n := requests(); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
}

func TestInitPresets(t *testing.T) {
	// Every preset makes a config that loads.
	for _, p := range config.Presets() {
		data, err := initConfig(p, "acme/shop", &boardSchemaStub)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := config.Parse(data, config.FileName); err != nil {
			t.Errorf("preset %s: %v\n%s", p, err, data)
		}
	}
}

// boardSchemaStub is a board for rendering the config template.
var boardSchemaStub = board.Schema{Owner: "acme", OwnerType: board.OwnerOrganization, Number: 3, Title: "Shop"}

// editedBoard writes boardFixture with its Status field changed by edit,
// marked hand-crafted, and returns its path.
func editedBoard(t *testing.T, name string, edit func(status map[string]any)) string {
	t.Helper()
	data, err := os.ReadFile(boardFixture)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	f["handcrafted"] = true
	in := f["interactions"].([]any)[0].(map[string]any)
	p := in["response"].(map[string]any)["data"].(map[string]any)["repositoryOwner"].(map[string]any)["projectV2"].(map[string]any)
	for _, n := range p["fields"].(map[string]any)["nodes"].([]any) {
		if field := n.(map[string]any); field["name"] == "Status" {
			edit(field)
		}
	}
	if data, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "user_board_"+name+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
