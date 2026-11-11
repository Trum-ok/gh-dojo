package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// specExample is the example from docs/spec.md without the keys this
// version does not support (board.assignee, fields.sprint, blockers,
// iteration, batch, close, confirm); TestSpecExampleUnsupported puts some of
// them back.
const specExample = `
version = 1
preset = "kanban"            # base, overrides follow

[board]
owner = "acme"               # user or org is detected
project = 3
repos = ["acme/shop"]

[fields]
status = "Status"

[session]
env = ["CLAUDE_CODE_HOST_SESSION_ID"]
link = "claude://claude.ai/epitaxy/{id}"

[verbs.start]
when = "берёшь задачу в работу"
to = "In progress"
comment = "Сессия: {session}"

[verbs.wait]
when = "работа встала: ждём задачу или внешнее решение"
to = "Waiting"
require_one_of = ["by", "comment"]

[verbs.ready]
when = "сделано, проверено, закоммичено"
to = "Ready"

[verbs.done]
when = "выкачено на прод"
to = "Done"

[verbs.drop]
when = "задача не нужна или дубль"
require = ["comment"]
comment = "Не нужна: {comment}"
`

const presetOnly = `
preset = "kanban"

[board]
owner = "Trum-ok"
project = 1

[fields]
status = "Status"
`

// writeConfig creates dir/.github/dojo.toml and returns dir.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".github", "dojo.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustLoad(t *testing.T, content string) *Config {
	t.Helper()
	c, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

// dump prints c in a stable, readable form for golden files.
func dump(c *Config) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("file: %s", c.Display)
	w("version: %d", c.Version)
	w("preset: %q", c.Preset)
	w("board: owner=%s project=%d repos=%v", c.Board.Owner, c.Board.Project, c.Board.Repos)
	for _, name := range c.FieldNames() {
		w("fields.%s: %q", name, c.Fields[name])
	}
	w("statuses: initial=%q final=%q resolved=%q", c.Statuses.Initial, c.Statuses.Final, c.Statuses.Resolved)
	w("labels: types=%q area_prefix=%q", c.Labels.Types, c.Labels.AreaPrefix)
	w("session: env=%q link=%q", c.Session.Env, c.Session.Link)
	for _, v := range c.Verbs {
		w("verb %s", v.Name)
		w("  when: %s", v.When)
		w("  to: %q", v.To)
		w("  from: %q", v.From)
		w("  require: %q require_one_of: %q", v.Require, v.RequireOneOf)
		w("  comment: %q", v.Comment)
	}
	w("status refs:")
	for _, r := range c.StatusRefs() {
		w("  %-12q %s", r.Status, c.KeyPath(r.Key))
	}
	return b.String()
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"spec_example", specExample},
		{"preset_only", presetOnly},
		{"override_verb_key", presetOnly + `
[verbs.start]
when = "Take the issue"
comment = "Taken in {session}"
[verbs.done]
from = ["Ready"]
`},
		{"add_verb", presetOnly + `
[verbs.review]
when = "A pull request is open and waits for review"
to = "In review"
from = ["In progress"]
require = ["comment"]
comment = "Review: {comment}"
`},
		{"disable_verb", presetOnly + `
[verbs.done]
disabled = true
[verbs.wait]
disabled = false
`},
		{"override_sections", `
preset = "kanban"
[board]
owner = "acme"
project = 3
[fields]
status = "Stage"
[statuses]
final = ["Done", "Won't do"]
resolved = []
[labels]
area_prefix = ""
types = ["bug", "feature"]
`},
		{"no_preset", `
version = 1
[board]
owner = "acme"
project = 7
[fields]
status = "Status"
[verbs.go]
when = "Go"
to = "Doing"
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := mustLoad(t, tt.content)
			testutil.Golden(t, "load/"+tt.name, []byte(dump(c)))
		})
	}
}

func TestLoadErrors(t *testing.T) {
	board := "[board]\nowner = \"acme\"\nproject = 3\n"
	tests := []struct {
		name    string
		content string
	}{
		{"syntax", "version = = 1\n"},
		{"type_mismatch", "[board]\nowner = \"acme\"\nproject = \"3\"\n"},
		{"unknown_top_key", "preset = \"kanban\"\n" + board + "[bord]\nowner = \"x\"\n"},
		{"unknown_board_key", "preset = \"kanban\"\n" + board + "ownr = \"x\"\n"},
		{"unknown_verb_key", presetOnly + "[verbs.start]\nwhen_ = \"x\"\n"},
		{"unsupported_key", presetOnly + "[verbs.done]\nbatch = true\n"},
		{"unsupported_hook", presetOnly + "[verbs.start.pre]\ncmd = \"./check\"\n"},
		{"unsupported_assignee", "preset = \"kanban\"\n" + board + "assignee = \"@me\"\n"},
		{"unsupported_field", presetOnly + "sprint = \"Sprint\"\n"},
		{"unsupported_field_first", presetOnly + "sprint = \"Sprint\"\n[verbs.start]\nwhen_ = \"x\"\n"},
		{"bad_version", "version = 2\npreset = \"kanban\"\n" + board},
		{"unknown_preset", "preset = \"scrum\"\n" + board},
		{"missing_owner", "preset = \"kanban\"\n[board]\nproject = 3\n"},
		{"missing_project", "preset = \"kanban\"\n[board]\nowner = \"acme\"\n"},
		{"bad_project", "preset = \"kanban\"\n[board]\nowner = \"acme\"\nproject = 0\n"},
		{"bad_repo", "preset = \"kanban\"\n" + board + "repos = [\"shop\"]\n"},
		{"missing_fields_status", board + "[fields]\n"},
		{"empty_fields_status", "preset = \"kanban\"\n" + board + "[fields]\nstatus = \"\"\n"},
		{"empty_field", strings.Replace(presetOnly, `"Status"`, `" "`, 1)},
		{"verb_without_when", board + "[fields]\nstatus = \"Status\"\n[verbs.go]\nto = \"Doing\"\n"},
		{"verb_multiline_when", presetOnly + "[verbs.go]\nwhen = \"\"\"\nline one\nline two\"\"\"\n"},
		{"verb_name_collision", presetOnly + "[verbs.list]\nwhen = \"x\"\n"},
		{"verb_name_invalid", presetOnly + "[verbs.Do_it]\nwhen = \"x\"\n"},
		{"bad_placeholder", presetOnly + "[verbs.start]\ncomment = \"Session {sesion}\"\n"},
		{"bad_require_arg", presetOnly + "[verbs.go]\nwhen = \"x\"\ncomment = \"{comment}\"\nrequire = [\"reason\"]\n"},
		{"empty_from", presetOnly + "[verbs.ready]\nfrom = [\"In progress\", \"\"]\n"},
		{"bad_link_placeholder", presetOnly + "[session]\nenv = [\"X\"]\nlink = \"https://x/{session}\"\n"},
		{"link_without_id", presetOnly + "[session]\nenv = [\"X\"]\nlink = \"https://x/\"\n"},
		{"bad_require_one_of", presetOnly + "[verbs.wait]\nrequire_one_of = [\"by\", \"why\"]\n"},
		{"repos_type", "preset = \"kanban\"\n" + board + "repos = \"acme/shop\"\n"},
		{"verb_not_table", presetOnly + "[verbs]\nstart = \"x\"\n"},
		{"no_effect", presetOnly + "[verbs.go]\nwhen = \"x\"\n"},
		{"require_unused", presetOnly + "[verbs.go]\nwhen = \"x\"\nto = \"Doing\"\nrequire = [\"comment\"]\n"},
		{"require_one_of_unused", presetOnly + "[verbs.wait]\ncomment = \"Waiting for {by}.\"\n"},
		{"require_duplicate", presetOnly + "[verbs.go]\nwhen = \"x\"\ncomment = \"{comment}\"\nrequire = [\"comment\", \"comment\"]\n"},
		{"require_overlap", presetOnly + "[verbs.wait]\nrequire = [\"comment\"]\n"},
		{"disabled_unknown_verb", presetOnly + "[verbs.drop]\ndisabled = true\n"},
		{"disabled_without_preset", board + "[fields]\nstatus = \"Status\"\n[verbs.old]\ndisabled = true\n"},
		{"disabled_with_keys", presetOnly + "[verbs.done]\ndisabled = true\nwhen = \"x\"\n"},
		{"disabled_bad_name", presetOnly + "[verbs.list]\ndisabled = true\n"},
		{"initial_spaces", presetOnly + "[statuses]\ninitial = \" Backlog\"\n"},
		{"final_duplicate", presetOnly + "[statuses]\nfinal = [\"Done\", \"Done\"]\n"},
		{"final_blank", presetOnly + "[statuses]\nfinal = [\"  \"]\n"},
		{"resolved_duplicate", presetOnly + "[statuses]\nresolved = [\"Ready\", \"Done\", \"Ready\"]\n"},
		{"resolved_blank", presetOnly + "[statuses]\nresolved = [\"Ready\", \"\"]\n"},
		{"resolved_not_list", presetOnly + "[statuses]\nresolved = \"Ready\"\n"},
		{"field_spaces", strings.Replace(presetOnly, `"Status"`, `"Status "`, 1)},
		{"env_spaces", presetOnly + "[session]\nenv = [\" X\"]\n"},
		{"env_duplicate", presetOnly + "[session]\nenv = [\"X\", \"X\"]\n"},
		{"to_spaces", presetOnly + "[verbs.start]\nto = \"In progress \"\n"},
		{"when_carriage_return", presetOnly + "[verbs.start]\nwhen = \"a\\rb\"\n"},
		{"repo_duplicate", "preset = \"kanban\"\n" + board + "repos = [\"acme/shop\", \"acme/shop\"]\n"},
		{"repo_git_suffix", "preset = \"kanban\"\n" + board + "repos = [\"acme/shop.git\"]\n"},
		{"types_area_prefix", presetOnly + "[labels]\ntypes = [\"bug\", \"area:cli\"]\n"},
		{"types_duplicate", presetOnly + "[labels]\ntypes = [\"bug\", \"bug\"]\n"},
		{"old_type_prefix", presetOnly + "[labels]\ntype_prefix = \"\"\n"},
		{"verbs_not_table", "verbs = 1\n" + presetOnly},
		{"field_not_string", strings.Replace(presetOnly, `"Status"`, `2`, 1)},
		{"unknown_session_key", presetOnly + "[session]\nenvs = [\"X\"]\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.content))
			checkError(t, "errors/"+tt.name, err)
		})
	}
}

func checkError(t *testing.T, golden string, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("got nil error")
	}
	var e *render.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %T is not *render.Error: %v", err, err)
	}
	if e.Code != render.ExitUsage {
		t.Errorf("code = %d, want %d", e.Code, render.ExitUsage)
	}
	text := e.Text() + "\n"
	if n := strings.Count(text, "\n"); n > 5 {
		t.Errorf("error block has %d lines, want at most 5", n)
	}
	testutil.Golden(t, golden, []byte(text))
}

func TestMissingFile(t *testing.T) {
	_, err := Load(t.TempDir())
	checkError(t, "errors/missing_file", err)
	if !errors.Is(err, ErrNoConfig) {
		t.Errorf("Load error %v is not ErrNoConfig", err)
	}
	if _, err := Parse([]byte("version = 2\n"), FileName); errors.Is(err, ErrNoConfig) {
		t.Errorf("a broken config is reported as ErrNoConfig: %v", err)
	}
}

func TestFindWalksUp(t *testing.T) {
	root := writeConfig(t, presetOnly)
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(root, ".github", "dojo.toml"))
	if got, _ := filepath.EvalSymlinks(c.Path); got != want || !filepath.IsAbs(c.Path) {
		t.Errorf("Path = %q, want %q", c.Path, want)
	}
	if c.Display != "../../.github/dojo.toml" {
		t.Errorf("Display = %q", c.Display)
	}
	// A directory named dojo.toml is not a config.
	if err := os.MkdirAll(filepath.Join(sub, ".github", "dojo.toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if path, err := Find(sub); err != nil || path != c.Path {
		t.Errorf("Find = %q, %v; want %q", path, err, c.Path)
	}
}

func TestPresetsValid(t *testing.T) {
	for _, name := range Presets() {
		t.Run(name, func(t *testing.T) {
			c, err := Parse([]byte(fmt.Sprintf("preset = %q\n[board]\nowner = \"o\"\nproject = 1\n", name)), FileName)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Verbs) == 0 {
				t.Error("preset has no verbs")
			}
			if c.Statuses.Initial == "" || len(c.Statuses.Final) == 0 {
				t.Error("preset must set statuses.initial and statuses.final")
			}
		})
	}
	if got := strings.Join(Presets(), ","); got != "kanban" {
		t.Errorf("Presets() = %s", got)
	}
}

func TestKanbanStatuses(t *testing.T) {
	c := mustLoad(t, presetOnly)
	seen := map[string]bool{}
	for _, r := range c.StatusRefs() {
		seen[r.Status] = true
	}
	for _, s := range []string{"Backlog", "In progress", "Waiting", "Ready", "Done"} {
		if !seen[s] {
			t.Errorf("kanban does not use status %q", s)
		}
		delete(seen, s)
	}
	if len(seen) > 0 {
		t.Errorf("kanban uses undocumented statuses %v", seen)
	}
}

func TestKanbanComments(t *testing.T) {
	c := mustLoad(t, presetOnly)
	tests := []struct {
		verb string
		vars map[string]string
		want string
	}{
		{"start", map[string]string{"session": "claude://s/1"}, "Started. Session: claude://s/1"},
		{"start", nil, ""},
		{"start", map[string]string{"comment": "Plan: x"}, "Plan: x"},
		{"start", map[string]string{"session": "S", "comment": "Plan: x"}, "Started. Session: S\n\nPlan: x"},
		{"wait", map[string]string{"by": "#12"}, "Waiting for #12."},
		{"wait", map[string]string{"comment": "Need a design decision"}, "Need a design decision"},
		{"wait", map[string]string{"by": "#12", "comment": "API first"}, "Waiting for #12.\n\nAPI first"},
		{"ready", nil, ""},
		{"done", map[string]string{"comment": "v0.1.0"}, "v0.1.0"},
	}
	for _, tt := range tests {
		v, ok := c.Verb(tt.verb)
		if !ok {
			t.Fatalf("no verb %s", tt.verb)
		}
		if got := Render(v.Comment, tt.vars); got != tt.want {
			t.Errorf("%s %v: got %q, want %q", tt.verb, tt.vars, got, tt.want)
		}
	}
}

func TestRender(t *testing.T) {
	vars := map[string]string{"a": "A", "b": "", "multi": "x\ny", "spaced": "p  \n\n\nq\n"}
	tests := []struct{ tmpl, want string }{
		{"", ""},
		{"plain text", "plain text"},
		{"{a} and {b}", "A and"},
		{"  indented {a}\n    more  \n", "  indented A\n    more"},
		{"\n  \n\t{a}", "\tA"},
		{"only {b}", ""},
		{"{missing}", ""},
		{"head\n\n{b}\n\n\ntail", "head\n\ntail"},
		{"\n\n{a}\n\n", "A"},
		{"json {} and { a } stay", "json {} and { a } stay"},
		{"{multi}!", "x\ny!"},
		{"{spaced}", "p\n\n\nq"},
		{"no {a} re-expansion of {a}", "no A re-expansion of A"},
	}
	for _, tt := range tests {
		if got := Render(tt.tmpl, vars); got != tt.want {
			t.Errorf("Render(%q) = %q, want %q", tt.tmpl, got, tt.want)
		}
	}
	if got := Render("{a}", map[string]string{"a": "{b}", "b": "B"}); got != "{b}" {
		t.Errorf("values must not be expanded again, got %q", got)
	}
}

func TestSessionResolve(t *testing.T) {
	env := map[string]string{"SECOND": " abc ", "EMPTY": ""}
	getenv := func(k string) string { return env[k] }
	tests := []struct {
		s      Session
		want   string
		wantOK bool
	}{
		{Session{}, "", false},
		{Session{Env: []string{"NONE", "EMPTY"}, Link: "x/{id}"}, "", false},
		{Session{Env: []string{"EMPTY", "SECOND"}, Link: "claude://s/{id}"}, "claude://s/abc", true},
		{Session{Env: []string{"SECOND"}}, "abc", true},
	}
	for _, tt := range tests {
		got, ok := tt.s.Resolve(getenv)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("%+v: got %q, %v; want %q, %v", tt.s, got, ok, tt.want, tt.wantOK)
		}
	}
	c := mustLoad(t, specExample)
	t.Setenv("CLAUDE_CODE_HOST_SESSION_ID", "s1")
	if got, ok := c.Session.Resolve(nil); !ok || got != "claude://claude.ai/epitaxy/s1" {
		t.Errorf("Resolve(nil) = %q, %v", got, ok)
	}
}

// TestSpecExampleUnsupported checks that the keys stripped from specExample
// are rejected, not silently accepted.
func TestSpecExampleUnsupported(t *testing.T) {
	tests := []struct{ after, add, want string }{
		{`repos = ["acme/shop"]` + "\n", `assignee = "@me"` + "\n", "board.assignee is not supported in this version"},
		{`status = "Status"` + "\n", `sprint = "Sprint"` + "\n", "fields.sprint is not supported in this version"},
		{"[verbs.done]\n", "batch = true\n", "verbs.done.batch is not supported in this version"},
		{"[verbs.wait]\n", "blockers = \"add\"\n", "verbs.wait.blockers is not supported in this version"},
	}
	for _, tt := range tests {
		if !strings.Contains(specExample, tt.after) {
			t.Fatalf("specExample has no %q", tt.after)
		}
		content := strings.Replace(specExample, tt.after, tt.after+tt.add, 1)
		_, err := Load(writeConfig(t, content))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("with %q: got %v, want %q", tt.add, err, tt.want)
		}
	}
}

// TestReadmeExamples keeps the README config examples loadable.
func TestReadmeExamples(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	blocks := strings.Split(string(data), "```toml\n")[1:]
	if len(blocks) < 2 {
		t.Fatalf("found %d toml blocks in README.md, want at least 2", len(blocks))
	}
	for i, b := range blocks {
		b, _, _ = strings.Cut(b, "```")
		if _, err := Parse([]byte(b), fmt.Sprintf("README.md block %d", i+1)); err != nil {
			t.Error(err)
		}
	}
}

func TestErrorfAndKeyPath(t *testing.T) {
	c := mustLoad(t, presetOnly+"[verbs.start]\nto = \"Doing\"\n")
	if got := c.KeyPath("verbs.start.to"); got != "verbs.start.to" {
		t.Errorf("repo key: %q", got)
	}
	if got := c.KeyPath("verbs.start.from"); got != "verbs.start.from (from preset kanban)" {
		t.Errorf("preset key: %q", got)
	}
	if got := c.KeyPath("board.owner"); got != "board.owner" {
		t.Errorf("board key: %q", got)
	}
	e := c.Errorf("%s: unknown status %q", c.KeyPath("verbs.wait.to"), "Waiting").WithAllowed("Backlog", "Done")
	testutil.Golden(t, "errors/board_check_example", []byte(e.Text()+"\n"))
}
