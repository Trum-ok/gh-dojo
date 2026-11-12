package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/config"
)

func TestSplice(t *testing.T) {
	const section = "<!-- gh-dojo:begin -->\nnew\n<!-- gh-dojo:end -->\n"
	tests := []struct{ name, have, want, err string }{
		{"empty", "", section, ""},
		{"blank", "\n\n", section, ""},
		{"append", "# Agents\n\nTabs.", "# Agents\n\nTabs.\n\n" + section, ""},
		{"append_trailing_newlines", "# Agents\n\n\n", "# Agents\n\n" + section, ""},
		{"replace", "a\n<!-- gh-dojo:begin: x -->\nold\n<!-- gh-dojo:end -->\nb\n", "a\n" + section + "b\n", ""},
		{"replace_last_line", "a\n<!-- gh-dojo:begin -->\nold\n<!-- gh-dojo:end -->", "a\n" + section, ""},
		{"two_begins", "<!-- gh-dojo:begin -->\n<!-- gh-dojo:begin -->\n<!-- gh-dojo:end -->\n", "", "two gh-dojo:begin markers"},
		{"end_first", "<!-- gh-dojo:end -->\n", "", "a gh-dojo:end marker without a gh-dojo:begin before it"},
		{"two_ends", "<!-- gh-dojo:begin -->\n<!-- gh-dojo:end -->\n<!-- gh-dojo:end -->\n", "",
			"a gh-dojo:end marker without a gh-dojo:begin before it"},
		{"no_end", "<!-- gh-dojo:begin -->\n", "", "a gh-dojo:begin marker without a gh-dojo:end after it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splice(tt.have, section)
			if tt.err != "" {
				if err == nil || err.Error() != tt.err {
					t.Fatalf("err = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("splice = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestPrepare(t *testing.T) {
	root := t.TempDir()
	agents, _ := TargetOf(config.AgentAgentsMD)
	write := func(name, s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// CRLF on disk, as a Windows checkout may have it, reads as LF.
	write("AGENTS.md", "# Agents\r\n\r\n"+Begin(config.FileName)+"\r\nbody\r\n"+End+"\r\n")
	f, err := Prepare(root, agents, "body\n", config.FileName, false)
	if err != nil {
		t.Fatal(err)
	}
	if !f.UpToDate() {
		t.Errorf("not up to date:\n%q\n%q", f.Have, f.Want)
	}

	skill, _ := TargetOf(config.AgentClaude)
	if _, err := Prepare(root, skill, "x\n", config.FileName, false); err != nil {
		t.Fatalf("a missing skill: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".claude/skills/gh-dojo"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(".claude/skills/gh-dojo/SKILL.md", "# Mine\n")
	if _, err := Prepare(root, skill, "x\n", config.FileName, false); err == nil || !strings.Contains(err.Error(), "not written by gh dojo agent install") {
		t.Errorf("a hand-written skill: err = %v", err)
	}
	if f, err := Prepare(root, skill, "x\n", config.FileName, true); err != nil || f.UpToDate() {
		t.Errorf("a hand-written skill, checked: %v, up to date %v", err, f != nil && f.UpToDate())
	}
	write(".claude/skills/gh-dojo/SKILL.md", "---\n---\n"+Generated(config.FileName)+"\nold\n")
	f, err = Prepare(root, skill, "new\n", config.FileName, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Write(root); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, ".claude/skills/gh-dojo/SKILL.md")); string(data) != "new\n" {
		t.Errorf("written: %q", data)
	}
}

func TestTargets(t *testing.T) {
	var agents []string
	for _, tg := range Targets {
		agents = append(agents, tg.Agent)
	}
	if !slices.Equal(agents, config.AgentTargets()) {
		t.Errorf("targets %v, config allows %v", agents, config.AgentTargets())
	}
}

func TestDiff(t *testing.T) {
	tests := []struct {
		name, have, want string
		max              int
		diff             []string
	}{
		{"equal", "a\nb\n", "a\nb\n", 10, nil},
		{"changed", "a\nb\nc\n", "a\nB\nc\n", 10, []string{"@@ line 2", "-b", "+B"}},
		{"added_at_end", "a\n", "a\nb\n", 10, []string{"@@ line 2", "+b"}},
		{"two_runs", "a\nb\nc\nd\n", "x\nb\nc\ny\n", 10, []string{"@@ line 1", "-a", "+x", "@@ line 4", "-d", "+y"}},
		{"cut", "a\nb\nc\n", "x\ny\nz\n", 2, []string{"@@ line 1", "-a", "-b", "… 4 more changed lines"}},
		{"from_empty", "", "a\n", 10, []string{"@@ line 1", "+a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Diff(tt.have, tt.want, tt.max); !slices.Equal(got, tt.diff) {
				t.Errorf("Diff = %q, want %q", got, tt.diff)
			}
		})
	}
}
