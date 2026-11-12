package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/config"
)

func TestSplice(t *testing.T) {
	const section = "<!-- gh-dojo:begin -->\nnew\n<!-- gh-dojo:end -->\n"
	tests := []struct{ name, have, want, err string }{
		{"empty", "", section, ""},
		{"append", "# Agents\n\nTabs.", "# Agents\n\nTabs.\n\n" + section, ""},
		{"append_after_newline", "# Agents\n", "# Agents\n\n" + section, ""},
		{"append_kept_as_is", "# Agents\n\n\n", "# Agents\n\n\n" + section, ""},
		{"replace", "a\n<!-- gh-dojo:begin: x -->\nold\n<!-- gh-dojo:end -->\nb\n", "a\n" + section + "b\n", ""},
		{"replace_indented", "a\n  <!-- gh-dojo:begin: x -->\nold\n\t<!-- gh-dojo:end -->\nb\n", "a\n" + section + "b\n", ""},
		{"replace_last_line", "a\n<!-- gh-dojo:begin -->\nold\n<!-- gh-dojo:end -->", "a\n" + section, ""},
		{"two_begins", "<!-- gh-dojo:begin -->\n<!-- gh-dojo:begin -->\n<!-- gh-dojo:end -->\n", "", "two gh-dojo:begin markers"},
		{"end_first", "<!-- gh-dojo:end -->\n", "", "a gh-dojo:end marker without a gh-dojo:begin before it"},
		{"two_ends", "<!-- gh-dojo:begin -->\n<!-- gh-dojo:end -->\n<!-- gh-dojo:end -->\n", "",
			"a gh-dojo:end marker without a gh-dojo:begin before it"},
		{"no_end", "<!-- gh-dojo:begin -->\n", "", "a gh-dojo:begin marker without a gh-dojo:end after it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splice(tt.have, section, "\n")
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

// openRoot returns a new repository directory and its root.
func openRoot(t *testing.T) (string, *os.Root) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return dir, root
}

func writeFile(t *testing.T, dir, name, s string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func prepareWrite(t *testing.T, root *os.Root, agent, body string) *File {
	t.Helper()
	tg, _ := TargetOf(agent)
	f, err := Prepare(root, tg, body, config.FileName)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Write(root); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestWriteCRLF keeps a CRLF file in CRLF: the section is written with
// CRLF, the text around it byte for byte, and the result is up to date.
func TestWriteCRLF(t *testing.T) {
	dir, root := openRoot(t)
	writeFile(t, dir, "AGENTS.md", "# Agents\r\n\r\nTabs.  \r\n")
	prepareWrite(t, root, config.AgentAgentsMD, "new\nlines\n")
	want := "# Agents\r\n\r\nTabs.  \r\n\r\n" + Begin(config.FileName) + "\r\nnew\r\nlines\r\n" + End + "\r\n"
	if got := readFile(t, dir, "AGENTS.md"); got != want {
		t.Errorf("AGENTS.md = %q, want %q", got, want)
	}
	writeFile(t, dir, "AGENTS.md", "# Agents\r\n"+Begin("x")+"\r\nold\r\n"+End+"\r\n## Style\r\n")
	prepareWrite(t, root, config.AgentAgentsMD, "new\n")
	want = "# Agents\r\n" + Begin(config.FileName) + "\r\nnew\r\n" + End + "\r\n## Style\r\n"
	if got := readFile(t, dir, "AGENTS.md"); got != want {
		t.Errorf("AGENTS.md = %q, want %q", got, want)
	}
	tg, _ := TargetOf(config.AgentAgentsMD)
	if f, err := Prepare(root, tg, "new\n", config.FileName); err != nil || !f.UpToDate() {
		t.Errorf("after the write: %v, up to date %v", err, f != nil && f.UpToDate())
	}

	writeFile(t, dir, ".claude/skills/gh-dojo/SKILL.md", "---\r\n---\r\n"+Generated(config.FileName)+"\r\nold\r\n")
	prepareWrite(t, root, config.AgentClaude, "---\n---\n"+Generated(config.FileName)+"\nnew\n")
	if got := readFile(t, dir, ".claude/skills/gh-dojo/SKILL.md"); got != "---\r\n---\r\n"+Generated(config.FileName)+"\r\nnew\r\n" {
		t.Errorf("SKILL.md = %q", got)
	}
}

func TestPrepareSkill(t *testing.T) {
	dir, root := openRoot(t)
	skill, _ := TargetOf(config.AgentClaude)
	f, err := Prepare(root, skill, "x\n", config.FileName)
	if err != nil || f.State() != StateMissing || f.Marked {
		t.Fatalf("a missing skill: %v, %+v", err, f)
	}
	writeFile(t, dir, skill.Path, "# Mine\n")
	if f, err := Prepare(root, skill, "x\n", config.FileName); err != nil || f.State() != StateHandWritten || f.Marked {
		t.Errorf("a hand-written skill: %v, %+v", err, f)
	}
	writeFile(t, dir, skill.Path, "---\n---\n  "+Generated(config.FileName)+"\nold\n")
	f = prepareWrite(t, root, config.AgentClaude, "new\n")
	if !f.Marked || f.HandWritten || f.State() != StateOutOfDate {
		t.Errorf("a generated skill: %+v", f)
	}
	if got := readFile(t, dir, skill.Path); got != "new\n" {
		t.Errorf("written: %q", got)
	}
	// No temporary file is left behind.
	entries, err := os.ReadDir(filepath.Join(dir, ".claude/skills/gh-dojo"))
	if err != nil || len(entries) != 1 {
		t.Errorf("files next to the skill: %v %v", entries, err)
	}
}

func TestWriteKeepsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no Unix permissions")
	}
	dir, root := openRoot(t)
	writeFile(t, dir, "AGENTS.md", "x\n")
	// 0664: the umask (022 as a rule) would cut the group's write bit.
	if err := os.Chmod(filepath.Join(dir, "AGENTS.md"), 0o664); err != nil {
		t.Fatal(err)
	}
	prepareWrite(t, root, config.AgentAgentsMD, "new\n")
	if info, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil || info.Mode().Perm() != 0o664 {
		t.Errorf("mode after the write: %v %v", info.Mode(), err)
	}
}

// TestSymlinks writes through a link inside the repository, to where it
// points, and refuses links out of it, for reads and writes.
func TestSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	dir, root := openRoot(t)
	writeFile(t, dir, "docs/agents.md", "# Agents\n")
	if err := os.Symlink("docs/agents.md", filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	prepareWrite(t, root, config.AgentAgentsMD, "new\n")
	if info, err := os.Lstat(filepath.Join(dir, "AGENTS.md")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("AGENTS.md is no longer a link: %v", err)
	}
	if got := readFile(t, dir, "docs/agents.md"); !strings.Contains(got, "new\n") {
		t.Errorf("the link's target = %q", got)
	}

	outside := t.TempDir()
	writeFile(t, outside, "secret.md", "secret\n")
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(dir, "notes.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(root, "notes.md"); err == nil || !strings.Contains(err.Error(), "cannot read notes.md") {
		t.Errorf("a link out of the repository was read: %v", err)
	}

	skill, _ := TargetOf(config.AgentClaude)
	if err := os.MkdirAll(filepath.Join(dir, ".claude/skills/gh-dojo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "SKILL.md"), filepath.Join(dir, filepath.FromSlash(skill.Path))); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(root, skill, "x\n", config.FileName); err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Errorf("read through a link out of the repository: %v", err)
	}
	f := &File{Target: skill, Want: "x\n"}
	if err := f.Write(root); err == nil || !strings.Contains(err.Error(), "absolute symbolic link") {
		t.Errorf("wrote through an absolute link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "SKILL.md")); err == nil {
		t.Error("a file was written outside the repository")
	}

	// An absolute link is refused even when it points inside.
	copilot, _ := TargetOf(config.AgentCopilot)
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "docs/agents.md"), filepath.Join(dir, ".github/copilot-instructions.md")); err != nil {
		t.Fatal(err)
	}
	f = &File{Target: copilot, Want: "x\n"}
	if err := f.Write(root); err == nil || !strings.Contains(err.Error(), "absolute symbolic link") {
		t.Errorf("wrote through an absolute link inside: %v", err)
	}

	// A relative link out of the repository.
	if err := os.Remove(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../elsewhere.md", filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	agents, _ := TargetOf(config.AgentAgentsMD)
	f = &File{Target: agents, Want: "x\n"}
	if err := f.Write(root); err == nil || !strings.Contains(err.Error(), "outside the repository") {
		t.Errorf("wrote through a relative link out of the repository: %v", err)
	}
}

// TestResolve follows links in every element of a path.
func TestResolve(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	dir, root := openRoot(t)
	writeFile(t, dir, ".git/HEAD", "ref\n")
	writeFile(t, dir, "docs/a.md", "a\n")
	for link, to := range map[string]string{"sub/g": "../.git", "d": "docs", "d2": "d/../d"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, link)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(to, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{
		"sub/g/HEAD": ".git/HEAD", "d/a.md": "docs/a.md", "d2/a.md": "docs/a.md",
		"docs/new.md": "docs/new.md", "missing/x/y": "missing/x/y",
	} {
		if got, err := Resolve(root, name); err != nil || got != want {
			t.Errorf("Resolve(%s) = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestTargets(t *testing.T) {
	var agents []string
	for _, tg := range Targets {
		agents = append(agents, tg.Agent)
		if tg.Path != config.AgentFile(tg.Agent) {
			t.Errorf("%s: path %s, config says %s", tg.Agent, tg.Path, config.AgentFile(tg.Agent))
		}
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
		{"line_endings", "a\r\nb\r\n", "a\nb\n", 10, nil},
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
