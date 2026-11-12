package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/config"
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
// blockers check (deny) and link, batch, a sprint, labels.types, notes.
const agentConfig = `
version = 1
preset = "sprints"

[board]
owner = "acme"
project = 3
repos = ["acme/shop"]

[labels]
types = ["bug", "feature"]

[session]
env = ["CLAUDE_CODE_HOST_SESSION_ID"]
link = "claude://claude.ai/epitaxy/{id}"

[verbs.start]
blockers = "deny"

[verbs.drop]
when = "The issue is not needed or a duplicate"
close = "not_planned"
require = ["comment"]
confirm = "user"

[agent]
targets = ["claude", "agents-md", "copilot"]
notes = "docs/agent-notes.md"
`

const agentNotes = "## Our rules\r\n\r\nRun `make test` before `ready`.\r\n\r\n\r\n"

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

// TestAgentKanban is the skill of the kanban preset with no [agent]: the
// default target, no notes.
func TestAgentKanban(t *testing.T) {
	dir := withRepo(t, dojoConfig, nil)
	if code, out := runOut("agent", "install"); code != render.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	testutil.Golden(t, "agent/skill_kanban", []byte(readRepoFile(t, dir, skillPath)))
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

func TestAgentHelp(t *testing.T) {
	for name, args := range map[string][]string{"help_agent": {"agent"}, "help_install": {"agent", "install", "--help"}} {
		code, out := runOut(args...)
		if code != render.ExitOK {
			t.Errorf("%v: exit code = %d, want 0", args, code)
		}
		testutil.Golden(t, "agent/"+name, out)
	}
}

// TestDoctorAgent runs doctor's agent check alone: no line without [agent]
// or its files, ok when up to date, a problem with the fix otherwise.
func TestDoctorAgent(t *testing.T) {
	check := func() []doctorCheck {
		t.Helper()
		cfg, err := loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		root, err := newRootCmd()
		if err != nil {
			t.Fatal(err)
		}
		r := &doctorReport{}
		checkAgentFiles(r, cfg, root)
		return r.Checks
	}
	withRepo(t, dojoConfig, nil)
	if got := check(); len(got) != 0 {
		t.Errorf("checks without [agent] = %+v, want none", got)
	}
	withRepo(t, dojoConfig+"[agent]\ntargets = [\"claude\", \"agents-md\"]\n", map[string]string{"AGENTS.md": "# Agents\n"})
	got := check()
	if len(got) != 1 || got[0].Result != checkProblem || got[0].Fix == nil || *got[0].Fix != "gh dojo agent install" ||
		got[0].Detail != "not up to date with .github/dojo.toml: .claude/skills/gh-dojo/SKILL.md (missing), AGENTS.md (out of date)" {
		t.Errorf("stale: %+v", got)
	}
	runOut("agent", "install")
	if got := check(); len(got) != 1 || got[0].Result != checkOK || got[0].Detail != ".claude/skills/gh-dojo/SKILL.md, AGENTS.md: up to date" {
		t.Errorf("up to date: %+v", got)
	}
}
