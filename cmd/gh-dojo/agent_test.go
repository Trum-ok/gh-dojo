package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/doctor"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// withRepo writes toml as .github/dojo.toml of a new repository directory,
// plus files (slash path → content), and loads the config from there. It
// returns the directory.
func withRepo(t *testing.T, toml string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files = mapsWith(files, config.FileName, toml)
	for name, content := range files {
		writeRepoFile(t, dir, name, content)
	}
	saved := loadConfig
	t.Cleanup(func() { loadConfig = saved })
	loadConfig = func() (*config.Config, error) { return config.Load(dir) }
	return dir
}

func mapsWith(m map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for mk, mv := range m {
		out[mk] = mv
	}
	return out
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRepoFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const skillPath = ".claude/skills/gh-dojo/SKILL.md"

// agentConfig covers every rule the instructions explain: confirm, the
// blockers check (deny) and link, the sub_issues check (warn and deny),
// the label checks (from_labels and refuse_labels, with the verbs that
// add and remove their labels), batch, a sprint, labels.type_prefix, notes.
const agentConfig = `
version = 1
preset = "sprints"

[board]
owner = "acme"
project = 3
repos = ["acme/shop"]

[labels]
type_prefix = "type: "

[session]
env = ["CLAUDE_CODE_HOST_SESSION_ID"]
link = "claude://claude.ai/epitaxy/{id}"

[verbs.start]
blockers = "deny"

[verbs.ready]
sub_issues = "warn"

[verbs.done]
sub_issues = "deny"

[verbs.drop]
when = "The issue is not needed or a duplicate"
close = "not_planned"
require = ["comment"]
confirm = "user"

[verbs.approve]
when = "The user approved the change"
labels = ["+approved"]

[verbs.park]
when = "Work on the issue stops for now"
labels = ["+parked"]

[verbs.unpark]
when = "Work on the issue goes on"
labels = ["-parked"]

[verbs.ship]
when = "The approved change goes out"
to = "Done"
from_labels = ["approved"]
refuse_labels = ["parked"]

[agent]
targets = ["claude", "agents-md", "copilot"]
notes = "docs/agent-notes.md"
description = "Also load it before a release."
`

const agentNotes = "## Our rules\r\n\r\nRun `make test` before `ready`.\r\n\r\n\r\n"

// TestLabelChecksHelp: the help of a verb with label checks names the
// verbs that add the label it needs and remove the one it refuses.
func TestLabelChecksHelp(t *testing.T) {
	withConfig(t, agentConfig)
	code, out := runOut("ship", "--help")
	if code != render.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}
	testutil.Golden(t, "verbs/help_ship_labels", out)
}

// TestAgentInstall writes the files of every agent, shows them, and runs
// install and --check again: nothing changes.
func TestAgentInstall(t *testing.T) {
	dir := withRepo(t, agentConfig, map[string]string{
		"docs/agent-notes.md": agentNotes,
		"AGENTS.md":           "# Agents\n\nBuild with make.\n",
	})
	code, out := runOut("agent", "install")
	if code != render.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}
	testutil.Golden(t, "agent/install", out)
	testutil.Golden(t, "agent/skill", []byte(readRepoFile(t, dir, skillPath)))
	testutil.Golden(t, "agent/agents_md", []byte(readRepoFile(t, dir, "AGENTS.md")))
	if a, c := readRepoFile(t, dir, "AGENTS.md"), readRepoFile(t, dir, ".github/copilot-instructions.md"); !strings.HasSuffix(a, c) {
		t.Errorf("the Copilot file is not the AGENTS.md section alone:\n%s", c)
	}

	for name, args := range map[string][]string{"install_again": {"agent", "install"}, "check_again": {"agent", "install", "--check"}} {
		code, out := runOut(args...)
		if code != render.ExitOK {
			t.Errorf("%v: exit code = %d, want 0", args, code)
		}
		testutil.Golden(t, "agent/"+name, out)
	}
}

// TestAgentConfigs is the skill of other configs with no [agent]: the
// default target, no notes. kanban is this repository's preset; custom
// overrides it; no_preset has no statuses, so no lifecycle path; suffix
// tells how a commit names its issue; shipped goes from Backlog straight
// to Done, which skips no status of the lifecycle and is not for a release.
func TestAgentConfigs(t *testing.T) {
	for name, cfg := range map[string]string{"kanban": dojoConfig, "custom": customConfig, "no_preset": noPresetConfig,
		"milestone": milestoneDojoConfig, "suffix": dojoConfig + "\n[release]\nsuffix = true\n",
		"shipped": dojoConfig + "\n" + shippedVerb} {
		t.Run(name, func(t *testing.T) {
			dir := withRepo(t, cfg, nil)
			if code, out := runOut("agent", "install"); code != render.ExitOK {
				t.Fatalf("exit code = %d\n%s", code, out)
			}
			testutil.Golden(t, "agent/skill_"+name, []byte(readRepoFile(t, dir, skillPath)))
		})
	}
}

// shippedVerb is a verb for an issue opened for work already shipped:
// Backlog → Done, as in this repository's own config.
const shippedVerb = `[verbs.shipped]
when = "the issue is for work already shipped in a release"
from = ["Backlog"]
to = "Done"
close = "completed"
require = ["comment"]
`

// TestReleaseFinishes: a verb only from the initial status finishes an
// issue nobody took, so describe, the skill and release do not point to
// release for it; with another status in from, they do.
func TestReleaseFinishes(t *testing.T) {
	cfg := &config.Config{Statuses: config.Statuses{Initial: "Backlog", Final: []string{"Done"}}}
	for _, c := range []struct {
		from []string
		want bool
	}{{[]string{"backlog"}, false}, {[]string{"Backlog", "Ready"}, true}, {[]string{"Ready"}, true}, {nil, true}} {
		v := config.Verb{Name: "v", From: c.from, To: "Done"}
		if got := releaseFinishes(cfg, v); got != c.want {
			t.Errorf("from %q: releaseFinishes = %v, want %v", c.from, got, c.want)
		}
	}
}

// TestAgentCheck changes a verb's when after install: --check fails with
// the changed lines, and install brings the files back up to date.
func TestAgentCheck(t *testing.T) {
	dir := withRepo(t, dojoConfig+"\n[agent]\ntargets = [\"claude\", \"agents-md\"]\n", nil)
	if code, out := runOut("agent", "install"); code != render.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	writeRepoFile(t, dir, config.FileName, dojoConfig+"\n[agent]\ntargets = [\"claude\", \"agents-md\"]\n"+
		"[verbs.ready]\nwhen = \"Merged into main\"\n")
	code, out := runOut("agent", "install", "--check")
	if code != render.ExitUsage {
		t.Errorf("exit code = %d, want 1", code)
	}
	testutil.Golden(t, "agent/check_stale", out)
	_, out = runOut("agent", "install", "--check", "--json")
	testutil.Golden(t, "json/agent_check_stale", out)

	code, out = runOut("agent", "install", "--agent", "agents-md", "--json")
	if code != render.ExitOK {
		t.Errorf("exit code = %d, want 0", code)
	}
	testutil.Golden(t, "json/agent_install", out)
	if code, out := runOut("agent", "install", "--check"); code != render.ExitUsage || !strings.Contains(string(out), "SKILL.md: out of date") {
		t.Errorf("only AGENTS.md should be updated:\n%s", out)
	}
}

// TestAgentVersion: the marker names the version that wrote the text. A
// newer version with the same text finds the files up to date and leaves
// them, marker included; once the text differs, --check and doctor name
// the version that wrote them.
func TestAgentVersion(t *testing.T) {
	saved := version
	t.Cleanup(func() { version = saved })
	conf := dojoConfig + "\n[agent]\ntargets = [\"claude\", \"agents-md\"]\n"
	dir := withRepo(t, conf, nil)
	version = "v0.3.0"
	if code, out := runOut("agent", "install"); code != render.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	skill := readRepoFile(t, dir, skillPath)
	if !strings.Contains(skill, "<!-- gh-dojo:generated by gh dojo agent install v0.3.0 from .github/dojo.toml:") ||
		!strings.Contains(readRepoFile(t, dir, "AGENTS.md"), "<!-- gh-dojo:begin: generated by gh dojo agent install v0.3.0 from") {
		t.Fatalf("the markers do not name v0.3.0:\n%s", skill)
	}

	version = "v0.4.0"
	for _, args := range [][]string{{"--check"}, nil} {
		code, out := runOut(append([]string{"agent", "install"}, args...)...)
		if code != render.ExitOK || strings.Count(string(out), ": up to date") != 2 {
			t.Errorf("agent install %v with the same text: exit code %d\n%s", args, code, out)
		}
	}
	if got := readRepoFile(t, dir, skillPath); got != skill {
		t.Errorf("the skill was rewritten for the version alone:\n%s", got)
	}
	_, out := runOut("agent", "install", "--check", "--json")
	if !strings.Contains(string(out), `"written_by":"v0.3.0"`) {
		t.Errorf("--json without written_by:\n%s", out)
	}

	writeRepoFile(t, dir, config.FileName, conf+"[verbs.ready]\nwhen = \"Merged into main\"\n")
	code, out := runOut("agent", "install", "--check")
	if code != render.ExitUsage || !strings.Contains(string(out),
		skillPath+": out of date (written by gh dojo v0.3.0, this is v0.4.0)\n") {
		t.Errorf("exit code = %d, want 1 and the version\n%s", code, out)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	root, err := newRootCmd()
	if err != nil {
		t.Fatal(err)
	}
	r := &doctor.Report{}
	checkAgentFiles(r, cfg, root)
	if got := r.Checks; len(got) != 1 || !strings.Contains(got[0].Detail, skillPath+" (out of date, written by gh dojo v0.3.0)") {
		t.Errorf("doctor: %+v", got)
	}
	if code, _ := runOut("agent", "install"); code != render.ExitOK ||
		!strings.Contains(readRepoFile(t, dir, skillPath), "agent install v0.4.0 from") {
		t.Errorf("install after the change: exit code %d, the marker does not name v0.4.0", code)
	}
}

// TestAgentSection keeps the text around the markers and replaces only
// what is between them.
func TestAgentSection(t *testing.T) {
	dir := withRepo(t, dojoConfig, map[string]string{
		"AGENTS.md": "# Agents\n\n<!-- gh-dojo:begin: old -->\nstale\n<!-- gh-dojo:end -->\n\n## Style\n\nTabs.\n",
	})
	if code, out := runOut("agent", "install", "--agent", "agents-md"); code != render.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	got := readRepoFile(t, dir, "AGENTS.md")
	if !strings.HasPrefix(got, "# Agents\n\n<!-- gh-dojo:begin: generated") || !strings.HasSuffix(got, "<!-- gh-dojo:end -->\n\n## Style\n\nTabs.\n") ||
		strings.Contains(got, "stale") {
		t.Errorf("AGENTS.md:\n%s", got)
	}
}

func TestAgentErrors(t *testing.T) {
	tests := []struct {
		name   string
		config string
		files  map[string]string
		args   []string
		code   render.Code
	}{
		{"missing_check", dojoConfig, nil, []string{"--check"}, render.ExitUsage},
		{"hand_written", dojoConfig, map[string]string{skillPath: "# My skill\n"}, nil, render.ExitUsage},
		{"hand_written_check", dojoConfig, map[string]string{skillPath: "# My skill\n"}, []string{"--check"}, render.ExitUsage},
		{"two_begins", dojoConfig, map[string]string{"AGENTS.md": "<!-- gh-dojo:begin -->\n<!-- gh-dojo:begin -->\n"},
			[]string{"--agent", "agents-md"}, render.ExitUsage},
		{"end_first", dojoConfig, map[string]string{"AGENTS.md": "<!-- gh-dojo:end -->\n<!-- gh-dojo:begin -->\n"},
			[]string{"--agent", "agents-md"}, render.ExitUsage},
		{"no_end", dojoConfig, map[string]string{"AGENTS.md": "x\n<!-- gh-dojo:begin -->\n"},
			[]string{"--agent", "agents-md"}, render.ExitUsage},
		{"unknown_agent", dojoConfig, nil, []string{"--agent", "claude,cursor"}, render.ExitUsage},
		{"no_targets", dojoConfig + "[agent]\ntargets = []\n", nil, nil, render.ExitUsage},
		{"notes_missing", dojoConfig + "[agent]\nnotes = \"notes.md\"\n", nil, nil, render.ExitUsage},
		{"notes_marker", dojoConfig + "[agent]\nnotes = \"notes.md\"\n",
			map[string]string{"notes.md": "x\n<!-- gh-dojo:end -->\n"}, nil, render.ExitUsage},
		{"args", dojoConfig, nil, []string{"extra"}, render.ExitUsage},
		{"description_long", dojoConfig + "[agent]\ndescription = \"" + strings.Repeat("Also now. ", 80) + "\"\n", nil, nil, render.ExitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := withRepo(t, tt.config, tt.files)
			code, out := runOut(append([]string{"agent", "install"}, tt.args...)...)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d", code, tt.code)
			}
			testutil.Golden(t, "agent/"+tt.name, out)
			for name, content := range tt.files { // nothing written
				if got := readRepoFile(t, dir, name); got != content {
					t.Errorf("%s changed:\n%s", name, got)
				}
			}
			if tt.files[skillPath] == "" {
				if _, err := os.Stat(filepath.Join(dir, skillPath)); err == nil {
					t.Errorf("%s written", skillPath)
				}
			}
		})
	}
}

// TestAgentNotesOutside refuses notes that link out of the repository.
func TestAgentNotesOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	dir := withRepo(t, dojoConfig+"[agent]\nnotes = \"notes.md\"\n", nil)
	outside := t.TempDir()
	writeRepoFile(t, outside, "secret.md", "secret\n")
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(dir, "notes.md")); err != nil {
		t.Fatal(err)
	}
	code, out := runOut("agent", "install")
	if code != render.ExitUsage {
		t.Errorf("exit code = %d, want 1", code)
	}
	testutil.Golden(t, "agent/notes_outside", out)
}

// TestAgentNotesGit refuses notes that lead into .git through a link
// inside the repository, which the config check cannot see.
func TestAgentNotesGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	dir := withRepo(t, dojoConfig+"[agent]\nnotes = \"sub/g/HEAD\"\n", map[string]string{".git/HEAD": "ref\n"})
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../.git", filepath.Join(dir, "sub/g")); err != nil {
		t.Fatal(err)
	}
	code, out := runOut("agent", "install")
	if code != render.ExitUsage {
		t.Errorf("exit code = %d, want 1", code)
	}
	testutil.Golden(t, "agent/notes_git", out)
}

// TestAgentDescriptionNoSkill: a description too long for the skill is
// not an error while no skill is written.
func TestAgentDescriptionNoSkill(t *testing.T) {
	dir := withRepo(t, dojoConfig+"[agent]\ntargets = [\"agents-md\"]\ndescription = \""+strings.Repeat("Also now. ", 120)+"\"\n", nil)
	if code, out := runOut("agent", "install"); code != render.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if strings.Contains(readRepoFile(t, dir, "AGENTS.md"), "Also now.") {
		t.Error("the description went into AGENTS.md")
	}
}

func TestAgentHelp(t *testing.T) {
	for name, args := range map[string][]string{"help_agent": {"agent"}, "help_install": {"agent", "install", "--help"}} {
		code, out := runOut(args...)
		if code != render.ExitOK {
			t.Errorf("%v: exit code = %d, want 0", args, code)
		}
		testutil.Golden(t, "agent/"+name, out)
	}
}

// TestDoctorAgent runs doctor's agent check alone. A project uses agent
// install when it sets [agent] or has a file with a gh-dojo marker; others,
// and targets = [], get no line. Out of date is a warning (the text depends
// on the gh-dojo version too); a hand-written skill under [agent] is a
// problem with the same fix as install's.
func TestDoctorAgent(t *testing.T) {
	check := func() []doctor.Check {
		t.Helper()
		cfg, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		root, err := newRootCmd()
		if err != nil {
			t.Fatal(err)
		}
		r := &doctor.Report{}
		checkAgentFiles(r, cfg, root)
		if err := r.Err(); err != nil && r.Checks[0].Result != doctor.ResultProblem {
			t.Errorf("a %s is an error: %v", r.Checks[0].Result, err)
		}
		return r.Checks
	}
	none := func(name string) {
		t.Helper()
		if got := check(); len(got) != 0 {
			t.Errorf("%s: checks = %+v, want none", name, got)
		}
	}
	withRepo(t, dojoConfig, nil)
	none("no [agent], no files")
	withRepo(t, dojoConfig, map[string]string{skillPath: "# Mine\n", "AGENTS.md": "# Agents\n"})
	none("no [agent], files without markers")
	withRepo(t, dojoConfig+"[agent]\ntargets = []\n", map[string]string{skillPath: "# Mine\n"})
	none("targets = []")

	withRepo(t, dojoConfig+"[agent]\ntargets = [\"claude\", \"agents-md\"]\n", map[string]string{"AGENTS.md": "# Agents\n"})
	got := check()
	if len(got) != 1 || got[0].Result != doctor.ResultWarn || got[0].Fix == nil || *got[0].Fix != "gh dojo agent install" ||
		got[0].Detail != "not up to date with .github/dojo.toml or this gh-dojo version: "+
			".claude/skills/gh-dojo/SKILL.md (missing), AGENTS.md (out of date)" {
		t.Errorf("stale: %+v", got)
	}
	runOut("agent", "install")
	if got := check(); len(got) != 1 || got[0].Result != doctor.ResultOK || got[0].Detail != ".claude/skills/gh-dojo/SKILL.md, AGENTS.md: up to date" {
		t.Errorf("up to date: %+v", got)
	}

	// Without [agent], only the marked file counts: AGENTS.md here.
	dir := withRepo(t, dojoConfig, map[string]string{skillPath: "# Mine\n"})
	runOut("agent", "install", "--agent", "agents-md")
	writeRepoFile(t, dir, "AGENTS.md", strings.Replace(readRepoFile(t, dir, "AGENTS.md"), "Verbs", "Words", 1))
	if got := check(); len(got) != 1 || got[0].Result != doctor.ResultWarn || !strings.HasSuffix(got[0].Detail, ": AGENTS.md (out of date)") {
		t.Errorf("marked AGENTS.md: %+v", got)
	}

	withRepo(t, dojoConfig+"[agent]\ntargets = [\"claude\"]\n", map[string]string{skillPath: "# Mine\n"})
	got = check()
	if len(got) != 1 || got[0].Result != doctor.ResultProblem || got[0].Fix == nil || !strings.HasPrefix(*got[0].Fix, "move what is worth keeping") {
		t.Errorf("hand-written: %+v", got)
	}
}
