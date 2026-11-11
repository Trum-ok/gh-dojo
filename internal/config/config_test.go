package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// specExample is the example from docs/spec.md. Its drop verb is the
// spec's, as is: no comment template, so --comment is posted as is.
const specExample = `
version = 1
preset = "kanban"            # base, overrides follow

[board]
owner = "acme"               # user or org is detected
project = 3
repos = ["acme/shop"]
assignee = "@me"

[fields]
status = "Status"
sprint = "Sprint"

[session]
env = ["CLAUDE_CODE_HOST_SESSION_ID"]
link = "claude://claude.ai/epitaxy/{id}"

[verbs.start]
when = "берёшь задачу в работу"
to = "In progress"
blockers = "warn"
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
iteration = "by_status_date:Ready"
batch = true                 # several issues at once

[verbs.drop]
when = "задача не нужна или дубль"
close = "not_planned"
require = ["comment"]
confirm = "user"
`

const presetOnly = `
preset = "kanban"

[board]
owner = "Trum-ok"
project = 1

[fields]
status = "Status"
`

// sprintsOnly is the sprints preset with only [board], as init writes it.
const sprintsOnly = `
preset = "sprints"

[board]
owner = "acme"
project = 3
repos = ["acme/shop"]
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
	w("board: owner=%s project=%d repos=%v assignee=%q", c.Board.Owner, c.Board.Project, c.Board.Repos, c.Board.Assignee)
	if c.Board.Timezone != "" {
		w("board.timezone: %s (location %s, zone %q)", c.Board.Timezone, c.Board.Loc(), c.Board.Zone())
	}
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
		if len(v.AddLabels)+len(v.RemoveLabels) > 0 || v.Assign != "" || v.Close != "" || v.Reopen || v.Confirm {
			w("  labels: add=%q remove=%q assign: %q close: %q reopen: %v confirm: %v",
				v.AddLabels, v.RemoveLabels, v.Assign, v.Close, v.Reopen, v.Confirm)
		}
		if v.Iteration != "" || len(v.Fields) > 0 {
			var fields []string
			for _, f := range v.Fields {
				fields = append(fields, fmt.Sprintf("%s=%q number=%v date=%v", f.Field, f.Text, f.Number, f.Date))
			}
			w("  iteration: %q fields: %s", v.IterationRule(), strings.Join(fields, ", "))
		}
		if v.Blockers != "" {
			w("  blockers: %q", v.Blockers)
		}
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
		{"sprints_only", sprintsOnly},
		// Overrides on top of sprints: another iteration field, start
		// without the sprint and the blockers warning, done only from
		// Ready into today's sprint, keeping the issue open, and a verb of
		// its own.
		{"sprints_override", sprintsOnly + `
[fields]
sprint = "Iteration"

[verbs.start]
iteration = ""
blockers = false

[verbs.done]
from = ["Ready"]
iteration = "current"
close = false

[verbs.plan]
when = "The issue is taken into the current sprint"
iteration = "current"
`},
		{"blockers_off", presetOnly + "[verbs.wait]\nblockers = false\n"},
		{"blockers_deny", presetOnly + "[verbs.start]\nblockers = \"deny\"\n[verbs.wait]\nblockers = \"warn\"\n"},
		{"override_verb_key", presetOnly + `
[verbs.start]
when = "Take the issue"
comment = "Taken in {session}"
[verbs.done]
from = ["Ready"]
`},
		{"effects", strings.Replace(presetOnly, "project = 1\n", "project = 1\nassignee = \"alice\"\n", 1) + `
[verbs.review]
when = "A pull request is open and waits for review"
to = "In review"
labels = ["+in-review", "-blocked"]
assign = true

[verbs.take]
when = "Take the issue yourself"
assign = "@me"

[verbs.drop]
when = "The issue is not needed"
close = "not_planned"
require = ["comment"]
confirm = "user"

[verbs.revive]
when = "A closed issue is needed after all"
to = "Backlog"
reopen = true

[verbs.done]
close = "duplicate"
assign = false
`},
		// close = false and confirm = false turn a preset's values off:
		// done moves to Done and leaves the issue open.
		{"opt_out", presetOnly + `
[verbs.done]
close = false
confirm = false
`},
		{"timezone", strings.Replace(presetOnly, "project = 1\n", "project = 1\ntimezone = \"Europe/Moscow\"\n", 1)},
		{"sprint_effects", presetOnly + `sprint = "Sprint"

[verbs.plan]
when = "The issue is taken into the current sprint"
iteration = "current"

[verbs.triage]
when = "The issue is triaged"
to = "Backlog"
iteration = "clear"

[verbs.triage.field]
Priority = "P1"
"Story points" = 3
Estimate = 2.5
Due = 2026-11-30
Start = "2026-11-12"

[verbs.done]
iteration = "by_status_date:Ready"
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
		{"unsupported_key", presetOnly + "[verbs.done]\npost = \"./notify\"\n"},
		{"batch_not_bool", presetOnly + "[verbs.done]\nbatch = \"yes\"\n"},
		{"unsupported_hook", presetOnly + "[verbs.start.pre]\ncmd = \"./check\"\n"},
		{"unsupported_field", presetOnly + "priority = \"Priority\"\n"},
		{"unsupported_field_first", presetOnly + "priority = \"Priority\"\n[verbs.start]\nwhen_ = \"x\"\n"},
		{"field_wrong_case", strings.Replace(presetOnly, `status = "Status"`, `Status = "Status"`, 1)},
		{"sprint_wrong_case", presetOnly + "Sprint = \"Sprint\"\n"},
		{"unsupported_field_table", presetOnly + "[fields.priority]\nname = \"Priority\"\n"},
		{"sprint_table", presetOnly + "[fields.sprint]\nname = \"Sprint\"\n"},
		{"iteration_without_sprint", presetOnly + "[verbs.done]\niteration = \"current\"\n"},
		{"iteration_bad", presetOnly + "sprint = \"Sprint\"\n[verbs.done]\niteration = \"next\"\n"},
		{"iteration_by_status_date_blank", presetOnly + "sprint = \"Sprint\"\n[verbs.done]\niteration = \"by_status_date:\"\n"},
		{"iteration_not_string", presetOnly + "sprint = \"Sprint\"\n[verbs.done]\niteration = true\n"},
		{"iteration_by_status_date_custom_status", strings.Replace(presetOnly, `status = "Status"`, `status = "Stage"`, 1) +
			"sprint = \"Sprint\"\n[verbs.done]\niteration = \"by_status_date:Ready\"\n"},
		{"timezone_unknown", strings.Replace(presetOnly, "project = 1\n", "project = 1\ntimezone = \"Moscow\"\n", 1)},
		{"timezone_local", strings.Replace(presetOnly, "project = 1\n", "project = 1\ntimezone = \"Local\"\n", 1)},
		{"timezone_blank", strings.Replace(presetOnly, "project = 1\n", "project = 1\ntimezone = \"\"\n", 1)},
		{"field_empty_table", presetOnly + "[verbs.done.field]\n"},
		{"field_not_table", presetOnly + "[verbs.done]\nfield = \"Priority\"\n"},
		{"field_bad_type", presetOnly + "[verbs.done.field]\nPriority = true\n"},
		{"field_datetime", presetOnly + "[verbs.done.field]\nDue = 2026-11-30T10:00:00Z\n"},
		{"field_blank_value", presetOnly + "[verbs.done.field]\n\"Story points\" = \" \"\n"},
		{"field_status", presetOnly + "[verbs.done.field]\nStatus = \"Done\"\n"},
		{"field_sprint", presetOnly + "sprint = \"Sprint\"\n[verbs.done.field]\nSprint = \"Sprint 12\"\n"},
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
		{"require_unused", presetOnly + "[verbs.go]\nwhen = \"x\"\nto = \"Doing\"\nrequire = [\"by\"]\n"},
		{"labels_no_sign", presetOnly + "[verbs.go]\nwhen = \"x\"\nlabels = [\"in-review\"]\n"},
		{"labels_add_and_remove", presetOnly + "[verbs.go]\nwhen = \"x\"\nlabels = [\"+bug\", \"-Bug\"]\n"},
		{"labels_blank_name", presetOnly + "[verbs.go]\nwhen = \"x\"\nlabels = [\"+\"]\n"},
		{"labels_spaces", presetOnly + "[verbs.go]\nwhen = \"x\"\nlabels = [\"- blocked\"]\n"},
		{"labels_not_list", presetOnly + "[verbs.go]\nwhen = \"x\"\nlabels = \"+bug\"\n"},
		{"assign_true_without_board", presetOnly + "[verbs.start]\nassign = true\n"},
		{"assign_at_login", presetOnly + "[verbs.start]\nassign = \"@alice\"\n"},
		{"assign_bad_login", presetOnly + "[verbs.start]\nassign = \"alice smith\"\n"},
		{"assign_wrong_type", presetOnly + "[verbs.start]\nassign = 1\n"},
		{"board_assignee_bad", "preset = \"kanban\"\n" + board + "assignee = \"me@x\"\n"},
		{"board_assignee_at", "preset = \"kanban\"\n" + board + "assignee = \"@Me\"\n"},
		{"close_bad_reason", presetOnly + "[verbs.done]\nclose = \"done\"\n"},
		{"close_and_reopen", presetOnly + "[verbs.done]\nreopen = true\n"},
		{"confirm_bad", presetOnly + "[verbs.done]\nconfirm = \"yes\"\n"},
		{"confirm_not_string", presetOnly + "[verbs.done]\nconfirm = true\n"},
		{"confirm_wrong_type", presetOnly + "[verbs.done]\nconfirm = 1\n"},
		{"close_true", presetOnly + "[verbs.done]\nclose = true\n"},
		{"blockers_bad", presetOnly + "[verbs.start]\nblockers = \"add\"\n"},
		{"blockers_true", presetOnly + "[verbs.start]\nblockers = true\n"},
		{"blockers_number", presetOnly + "[verbs.start]\nblockers = 1\n"},
		{"blockers_link_without_by", presetOnly + "[verbs.ready]\nblockers = \"link\"\n"},
		// kanban's wait links --by; a comment without {by} drops what it links.
		{"blockers_link_comment_override", presetOnly + "[verbs.wait]\ncomment = \"Stuck: {comment}\"\n"},
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
	if got := strings.Join(Presets(), ","); got != "kanban,sprints" {
		t.Errorf("Presets() = %s", got)
	}
}

// TestPresetStatuses keeps the presets on the statuses README.md
// documents for their boards.
func TestPresetStatuses(t *testing.T) {
	for _, tt := range []struct{ name, content string }{{"kanban", presetOnly}, {"sprints", sprintsOnly}} {
		c := mustLoad(t, tt.content)
		seen := map[string]bool{}
		for _, r := range c.StatusRefs() {
			seen[r.Status] = true
		}
		for _, s := range []string{"Backlog", "In progress", "Waiting", "Ready", "Done"} {
			if !seen[s] {
				t.Errorf("%s does not use status %q", tt.name, s)
			}
			delete(seen, s)
		}
		if len(seen) > 0 {
			t.Errorf("%s uses undocumented statuses %v", tt.name, seen)
		}
	}
}

// TestSprintsPreset: sprints is kanban's flow with the sprint field set by
// start, ready and done, and start warning about blockers; the rest of
// each verb, the statuses and labels are kanban's.
func TestSprintsPreset(t *testing.T) {
	kanban, sprints := mustLoad(t, presetOnly), mustLoad(t, sprintsOnly)
	if got := sprints.SprintField(); got != "Sprint" {
		t.Errorf("fields.sprint = %q, want Sprint", got)
	}
	if !reflect.DeepEqual(kanban.Statuses, sprints.Statuses) || !reflect.DeepEqual(kanban.Labels, sprints.Labels) {
		t.Error("sprints statuses or labels differ from kanban's")
	}
	want := map[string]struct{ iteration, blockers string }{
		"start": {"current", "warn"},
		"wait":  {"", "link"},
		"ready": {"by_status_date:Ready", ""},
		"done":  {"by_status_date:Ready", ""},
	}
	if len(sprints.Verbs) != len(want) {
		t.Errorf("sprints has %d verbs, want %d", len(sprints.Verbs), len(want))
	}
	for name, w := range want {
		k, _ := kanban.Verb(name)
		v, ok := sprints.Verb(name)
		if !ok {
			t.Fatalf("sprints has no verb %s", name)
		}
		if v.IterationRule() != w.iteration || string(v.Blockers) != w.blockers {
			t.Errorf("%s: iteration %q, blockers %q; want %q, %q", name, v.IterationRule(), v.Blockers, w.iteration, w.blockers)
		}
		if v.When != k.When || v.To != k.To || v.Comment != k.Comment || v.Close != k.Close ||
			!reflect.DeepEqual(v.RequireOneOf, k.RequireOneOf) || !reflect.DeepEqual(v.From, k.From) {
			t.Errorf("%s differs from kanban's beyond the sprint:\nkanban  %+v\nsprints %+v", name, k, v)
		}
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

// TestBatch: batch = true is the verb's own key, inherited from the
// preset (kanban's done) and turned off with false; other verbs take one
// number.
func TestBatch(t *testing.T) {
	c := mustLoad(t, specExample)
	for _, v := range c.Verbs {
		if v.Batch != (v.Name == "done") {
			t.Errorf("%s: Batch = %v", v.Name, v.Batch)
		}
	}
	c = mustLoad(t, presetOnly+"[verbs.done]\nbatch = false\n[verbs.ready]\nbatch = true\n")
	for _, v := range c.Verbs {
		if v.Batch != (v.Name == "ready") {
			t.Errorf("%s: Batch = %v", v.Name, v.Batch)
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
