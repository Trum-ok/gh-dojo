package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/agent"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/spf13/cobra"
)

func init() { register(newAgentCmd) }

// maxDiffLines is how many changed lines --check prints per file.
const maxDiffLines = 20

func newAgentCmd() (*cobra.Command, error) {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Write this project's process into agent instructions",
	}
	install := &cobra.Command{
		Use:   "install",
		Short: "Write a skill or AGENTS.md section for agents from the config",
		Long: `Write this project's process, built from ` + config.FileName + `, into the
files agents read, so the rules are written once, in the config:

  claude     .claude/skills/gh-dojo/SKILL.md, a Claude Code skill
  agents-md  a section of AGENTS.md, which Codex and other agents read
  copilot    a section of .github/copilot-instructions.md

The instructions are in English: the lifecycle, each verb with when to use
it, what it needs, checks and does along the way, blockers, the session
link, reading and creating issues, exit codes and JSON, and the commands of
this gh-dojo. The markdown file [agent] notes names is appended as it is,
for what the config cannot say. Without --agent, the agents of [agent]
targets are written (by default claude).

The skill is gh-dojo's whole file: one that exists without gh-dojo's
marker, written by hand, is not overwritten (exit code 1). In AGENTS.md and
the Copilot file only the text between the gh-dojo:begin and gh-dojo:end
markers is gh-dojo's, and the rest stays as it is; a file without them gets
the section at its end.

A repeat with the same config changes nothing and says "up to date".
--check writes nothing and exits 1 when a file is missing or differs from
what install would write, showing the changed lines: run it in CI. Paths
are relative to the repository root, the directory that holds .github.
It never uses the network.`,
		Example: `  gh dojo agent install
  gh dojo agent install --agent claude,agents-md
  gh dojo agent install --check`,
		Args: cobra.NoArgs,
		RunE: runAgentInstall,
	}
	install.Flags().StringSlice("agent", nil, "write for `agent`: "+strings.Join(config.AgentTargets(), ", ")+
		"; repeatable, or comma-separated (default: [agent] targets)")
	install.Flags().Bool("check", false, "write nothing; exit 1 when a file is not up to date")
	cmd.AddCommand(install)
	return cmd, nil
}

// agentResult is what agent install did or found, and its JSON.
type agentResult struct {
	Check bool              `json:"check"`
	Files []agentFileResult `json:"files"`
}

type agentFileResult struct {
	Agent string `json:"agent"`
	Path  string `json:"path"` // relative to the repository root
	// State is agent.StateUpToDate, StateCreated or StateUpdated; with
	// --check, StateUpToDate, StateMissing or StateOutOfDate.
	State string   `json:"state"`
	Diff  []string `json:"diff"` // --check, out of date: the changed lines (agent.Diff)
}

func (r *agentResult) write(w io.Writer) {
	for _, f := range r.Files {
		fmt.Fprintf(w, "%s: %s\n", f.Path, strings.ReplaceAll(f.State, "_", " "))
		for _, l := range f.Diff {
			fmt.Fprintf(w, "  %s\n", l)
		}
	}
}

func runAgentInstall(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	agents, _ := cmd.Flags().GetStringSlice("agent")
	check, _ := cmd.Flags().GetBool("check")
	targets, err := agentTargets(cfg, agents)
	if err != nil {
		return err
	}
	root, err := repoRoot(cfg)
	if err != nil {
		return err
	}
	files, err := prepareAgentFiles(cfg, cmd.Root(), root, targets, check)
	if err != nil {
		return err
	}

	r := &agentResult{Check: check, Files: []agentFileResult{}}
	stale, wrote := 0, false
	for _, f := range files {
		res := agentFileResult{Agent: f.Agent, Path: f.Path, State: agent.StateUpToDate, Diff: []string{}}
		switch {
		case f.UpToDate():
		case check:
			stale++
			res.State = agent.StateMissing
			if f.Exists {
				res.State = agent.StateOutOfDate
				res.Diff = agent.Diff(f.Have, f.Want, maxDiffLines)
			}
		default:
			if err := f.Write(root); err != nil {
				// What was written before stays: a repeat finishes the rest.
				return emitWrite(cmd, r, r.write, err, wrote)
			}
			wrote = true
			res.State = agent.StateUpdated
			if !f.Exists {
				res.State = agent.StateCreated
			}
		}
		r.Files = append(r.Files, res)
	}
	var runErr error
	if stale > 0 {
		runErr = render.Usage("%d of %s not up to date with %s",
			stale, plural(len(files), "agent file", "agent files"), config.FileName).
			WithFix(agentInstallFix(agents))
	}
	return emitWrite(cmd, r, r.write, runErr, wrote)
}

// agentInstallFix is the command that brings the files checked up to date.
func agentInstallFix(agents []string) string {
	if len(agents) == 0 {
		return "gh dojo agent install"
	}
	return "gh dojo agent install --agent " + strings.Join(agents, ",")
}

// agentTargets returns the targets of the --agent values, else of [agent]
// targets, in order and each once.
func agentTargets(cfg *config.Config, agents []string) ([]agent.Target, error) {
	if len(agents) == 0 {
		agents = cfg.Agent.Targets
		if len(agents) == 0 {
			return nil, render.Usage("%s: agent.targets is empty: no agent to write for", cfg.Display).
				WithAllowed(config.AgentTargets()...).
				WithFix("gh dojo agent install --agent " + config.AgentClaude)
		}
	}
	var targets []agent.Target
	for _, a := range agents {
		t, ok := agent.TargetOf(a)
		if !ok {
			return nil, render.Usage("unknown agent %q for --agent", a).
				WithAllowed(config.AgentTargets()...).
				WithFix("gh dojo agent install --help")
		}
		if !slices.Contains(targets, t) {
			targets = append(targets, t)
		}
	}
	return targets, nil
}

// repoRoot is the directory that holds the config's .github.
func repoRoot(cfg *config.Config) (string, error) {
	if cfg.Path == "" {
		return "", render.Internal("the config was not loaded from a file")
	}
	return filepath.Dir(filepath.Dir(cfg.Path)), nil
}

// prepareAgentFiles reads the notes and every target, and works out what
// each should hold, before anything is written: a problem with one file
// leaves all of them as they are.
func prepareAgentFiles(cfg *config.Config, cmdRoot *cobra.Command, root string, targets []agent.Target, check bool) ([]*agent.File, error) {
	notes, err := readAgentNotes(cfg, root)
	if err != nil {
		return nil, err
	}
	doc := newAgentDoc(cfg, cmdRoot, notes)
	var files []*agent.File
	for _, t := range targets {
		body := doc.section()
		if !t.Section {
			body = doc.skill()
		}
		f, err := agent.Prepare(root, t, body, config.FileName, check)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// checkAgentFiles is doctor's agent check: agent install --check on the
// targets of [agent], without the diff. A project that sets no [agent] and
// has none of their files gets no line: it does not use agent install.
func checkAgentFiles(r *doctorReport, cfg *config.Config, cmdRoot *cobra.Command) {
	root, err := repoRoot(cfg)
	if err != nil { // a config not read from a file, in tests
		return
	}
	targets, err := agentTargets(cfg, nil)
	if err != nil {
		r.problem("agent", err)
		return
	}
	if !cfg.Agent.Set && !slices.ContainsFunc(targets, func(t agent.Target) bool {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(t.Path)))
		return err == nil
	}) {
		return
	}
	files, err := prepareAgentFiles(cfg, cmdRoot, root, targets, true)
	if err != nil {
		r.problem("agent", err)
		return
	}
	var ok, stale []string
	for _, f := range files {
		switch {
		case f.UpToDate():
			ok = append(ok, f.Path)
		case f.Exists:
			stale = append(stale, f.Path+" (out of date)")
		default:
			stale = append(stale, f.Path+" (missing)")
		}
	}
	if len(stale) > 0 {
		r.problem("agent", render.Usage("not up to date with %s: %s", config.FileName, strings.Join(stale, ", ")).
			WithFix("gh dojo agent install"))
		return
	}
	r.ok("agent", "%s: up to date", strings.Join(ok, ", "))
}

// readAgentNotes reads [agent] notes: "" without them, else the text with
// LF line endings, ending in one newline.
func readAgentNotes(cfg *config.Config, root string) (string, error) {
	name := cfg.Agent.Notes
	if name == "" {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", render.Usage("%s: agent.notes: %s does not exist", cfg.Display, name).
			WithFix(fmt.Sprintf("create %s, or remove notes from [agent] in %s", name, cfg.Display))
	case err != nil:
		return "", render.Usage("%s: agent.notes: cannot read %s: %v", cfg.Display, name, errors.Unwrap(err)).WithCause(err)
	}
	text := strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), " \t\n")
	if text == "" {
		return "", nil
	}
	for line := range strings.Lines(text) {
		if strings.HasPrefix(line, "<!-- gh-dojo:") {
			return "", render.Usage("%s: agent.notes: %s has a gh-dojo marker line: %s",
				cfg.Display, name, strings.TrimSpace(line)).
				WithFix("remove the <!-- gh-dojo: ... --> lines from " + name)
		}
	}
	return text + "\n", nil
}
