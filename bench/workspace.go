package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed conditions
var conditions embed.FS

// The two conditions the benchmark compares, both the way a team would set
// them up: plain gh with the process and recipes in CLAUDE.md, and gh dojo
// with its config and the skill agent install writes.
const (
	condGh   = "gh"
	condDojo = "dojo"
)

var allConditions = []string{condGh, condDojo}

// condNames are the conditions as the report and the chart name them.
var condNames = map[string]string{condGh: "gh + CLAUDE.md", condDojo: "gh dojo + skill"}

// workspace is an empty git repository whose origin is the sandbox, set up
// for one condition: what an agent's working directory would hold.
type workspace struct {
	dir  string
	cond string
	env  []string // for gh, gh dojo and claude run in it
}

// templateData fills the files of bench/conditions.
func (s *sandbox) templateData() map[string]any {
	return map[string]any{
		"Repo": s.repo(), "Owner": s.owner, "Name": s.name, "Project": s.project,
		"ProjectID": s.projectID, "StatusField": s.statusField, "Options": s.options,
	}
}

func (s *sandbox) render(name string) (string, error) {
	b, err := conditions.ReadFile("conditions/" + name)
	if err != nil {
		return "", err
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(b))
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := t.Execute(&out, s.templateData()); err != nil {
		return "", err
	}
	return out.String(), nil
}

func newWorkspace(s *sandbox, cond string) (*workspace, error) {
	dir, err := os.MkdirTemp("", "gh-dojo-bench-"+cond+"-")
	if err != nil {
		return nil, err
	}
	w := &workspace{dir: dir, cond: cond, env: childEnv(dir)}
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "https://github.com/" + s.repo() + ".git"},
	} {
		if _, err := w.run("git", args...); err != nil {
			return nil, err
		}
	}
	files := map[string]string{"CLAUDE.md": cond + ".md"}
	if cond == condDojo {
		files[".github/dojo.toml"] = "dojo.toml"
	}
	for path, name := range files {
		text, err := s.render(name)
		if err != nil {
			return nil, err
		}
		if err := w.write(path, text); err != nil {
			return nil, err
		}
	}
	if cond == condDojo {
		if out, err := w.run("gh", "dojo", "agent", "install"); err != nil {
			return nil, fmt.Errorf("gh dojo agent install: %w\n%s", err, out)
		}
	}
	return w, nil
}

func (w *workspace) write(path, text string) error {
	p := filepath.Join(w.dir, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(text), 0o644)
}

// context returns the files an agent of this condition has in its context
// from the start: CLAUDE.md, and the skill for gh dojo.
func (w *workspace) context() (map[string]string, error) {
	paths := []string{"CLAUDE.md"}
	if w.cond == condDojo {
		paths = append(paths, ".claude/skills/gh-dojo/SKILL.md")
	}
	out := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(w.dir, p))
		if err != nil {
			return nil, err
		}
		out[p] = string(b)
	}
	return out, nil
}

func (w *workspace) remove() { os.RemoveAll(w.dir) }

// run runs a command in the workspace and returns its stdout and stderr.
func (w *workspace) run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = w.dir, w.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// shell runs a command line the way an agent's Bash tool does.
func (w *workspace) shell(line string) (string, error) {
	return w.run("bash", "-c", line)
}

// childEnv is this process's environment without what ties a child to the
// session that runs the benchmark: an agent session's IDs (gh dojo would
// link them in comments), Claude Code's nesting markers and the desktop
// app's API endpoint, so a nested claude uses its own login.
func childEnv(dir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "CLAUDECODE" || k == "CLAUDE_PID" || k == "GH_REPO" || k == "ANTHROPIC_BASE_URL" ||
			strings.HasPrefix(k, "CLAUDE_CODE_") || strings.HasPrefix(k, "CLAUDE_AGENT_SDK") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GH_DOJO_CACHE_DIR="+filepath.Join(dir, ".cache"), "GH_PROMPT_DISABLED=1", "NO_COLOR=1")
}

func jsonBody(v any) io.Reader {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return bytes.NewReader(b)
}
