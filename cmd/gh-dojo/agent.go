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
	"github.com/Trum-ok/gh-dojo/internal/doctor"
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

The instructions are in English: to handle issues only through gh dojo,
the lifecycle, each verb with when to use it, what it needs, checks and
does along the way, blockers, reading, creating and linking issues, safe
repeats, exit codes and JSON. The markdown file [agent] notes names is
appended as it is, for what the config cannot say, and [agent] description
is added to the skill's description. Without --agent, the agents of
[agent] targets are written (by default claude).

The skill is gh-dojo's whole file: one that exists without gh-dojo's
marker, written by hand, is not overwritten (exit code 1; --check says
"hand written"). In AGENTS.md and the Copilot file only the lines from
gh-dojo:begin to gh-dojo:end are gh-dojo's, and the rest stays byte for
byte; a file without them gets the section at its end. Marker lines count
anywhere, code blocks included, so do not quote them in those files. A file
with CRLF line endings keeps them. A file is written whole through a
temporary file; a relative symbolic link is written where it points, which
must be inside the repository, and so must the notes; absolute links fail.

The marker names the gh-dojo version that wrote the text; a file that
differs only in that version is up to date and is not rewritten.

A repeat with the same config changes nothing and says "up to date".
--check writes nothing and exits 1 when a file is missing or differs from
what install would write, showing the changed lines and the version that
wrote it when another: run it in CI with a fixed gh-dojo version, since
the text depends on the version too. Paths
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
	Check bool            `json:"check"`
	Files []agentFileJSON `json:"files"`
}

type agentFileJSON struct {
	Agent string `json:"agent"`
	Path  string `json:"path"` // relative to the repository root
	// State is agent.StateUpToDate, StateCreated or StateUpdated; with
	// --check, StateUpToDate, StateMissing, StateOutOfDate or
	// StateHandWritten.
	State string   `json:"state"`
	Diff  []string `json:"diff"` // --check, out of date: the changed lines (agent.Diff)
	Fix   *string  `json:"fix"`  // --check, not up to date: what brings it up to date; else null
	// WrittenBy is the gh-dojo version the file's marker named before this
	// run (agent.File.WrittenBy); null when it named none or there was no
	// file.
	WrittenBy *string `json:"written_by"`
}

func (r *agentResult) write(w io.Writer) {
	for _, f := range r.Files {
		fmt.Fprintf(w, "%s: %s%s\n", f.Path, stateText(f.State), writtenByText(f.State, f.WrittenBy))
		for _, l := range f.Diff {
			fmt.Fprintf(w, "  %s\n", l)
		}
	}
}

// stateText is a file's state (agent.StateOutOfDate, say) as the text
// says it: "out of date".
func stateText(state string) string { return strings.ReplaceAll(state, "_", " ") }

// writtenByText tells, after a file out of date, that another gh-dojo
// version wrote it, so a reader can tell an upgrade from a config change:
// " (written by gh dojo v0.3.0, this is v0.4.0)". "" otherwise.
func writtenByText(state string, writtenBy *string) string {
	if state != agent.StateOutOfDate || writtenBy == nil || *writtenBy == version {
		return ""
	}
	return fmt.Sprintf(" (written by gh dojo %s, this is %s)", *writtenBy, version)
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
	root, err := openRepoRoot(cfg)
	if err != nil {
		return err
	}
	defer root.Close()
	files, err := prepareAgentFiles(cfg, cmd.Root(), root, targets)
	if err != nil {
		return err
	}
	install := agentInstallFix(agents)
	if !check {
		for _, f := range files {
			if f.HandWritten {
				return render.Usage("%s exists and was not written by gh dojo agent install; "+
					"it would be overwritten", f.Path).WithFix(agent.HandWrittenFix(f.Target, config.FileName))
			}
		}
	}

	r := &agentResult{Check: check, Files: []agentFileJSON{}}
	stale, wrote, fix := 0, false, install
	for _, f := range files {
		res := agentFileJSON{Agent: f.Agent, Path: f.Path, State: agent.StateUpToDate, Diff: []string{}}
		if f.WrittenBy != "" {
			res.WrittenBy = &f.WrittenBy
		}
		switch {
		case f.UpToDate():
		case check:
			stale++
			res.State = f.State()
			res.Fix = &install
			switch res.State {
			case agent.StateOutOfDate:
				res.Diff = agent.Diff(f.Have, f.Want, maxDiffLines)
			case agent.StateHandWritten:
				hw := agent.HandWrittenFix(f.Target, config.FileName)
				res.Fix, fix = &hw, hw
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
			stale, render.Plural(len(files), "agent file", "agent files"), config.FileName).WithFix(fix)
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

// openRepoRoot opens the directory that holds the config's .github: every
// file agent install reads or writes is inside it.
func openRepoRoot(cfg *config.Config) (*os.Root, error) {
	if cfg.Path == "" {
		return nil, render.Internal("the config was not loaded from a file")
	}
	root, err := os.OpenRoot(filepath.Dir(filepath.Dir(cfg.Path)))
	if err != nil {
		return nil, render.Usage("cannot open the repository root: %v", errors.Unwrap(err)).WithCause(err)
	}
	return root, nil
}

// prepareAgentFiles reads the notes and every target, and works out what
// each should hold, before anything is written: a problem with one file
// leaves all of them as they are.
func prepareAgentFiles(cfg *config.Config, cmdRoot *cobra.Command, root *os.Root, targets []agent.Target) ([]*agent.File, error) {
	notes, err := readAgentNotes(cfg, root)
	if err != nil {
		return nil, err
	}
	doc := newAgentDoc(cfg, cmdRoot, notes)
	var files []*agent.File
	for _, t := range targets {
		body := doc.section()
		if !t.Section { // only the skill has a description, with its length limit
			if body, err = doc.skill(); err != nil {
				return nil, err
			}
		}
		f, err := agent.Prepare(root, t, body, config.FileName, version)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// checkAgentFiles is doctor's agent check, agent install --check without
// the diff, for a project that uses agent install: one that sets [agent]
// (then its targets count), or has a file of any agent with a gh-dojo
// marker (then those files count). Others get no line, and neither does
// targets = [].
//
// A file out of date or missing is a warning, not a problem: the text
// depends on the gh-dojo version too, and the installed one may differ
// from the one that wrote the file; --check in CI is the strict check. A
// hand-written skill under [agent], or a file install cannot handle, is a
// problem.
func checkAgentFiles(r *doctor.Report, cfg *config.Config, cmdRoot *cobra.Command) {
	if cfg.Path == "" || len(cfg.Agent.Targets) == 0 { // not read from a file: in tests
		return
	}
	root, err := openRepoRoot(cfg)
	if err != nil {
		r.AddProblem("agent", err)
		return
	}
	defer root.Close()
	targets := agent.Targets // without [agent]: any file agent install wrote
	if cfg.Agent.Set {
		if targets, err = agentTargets(cfg, nil); err != nil {
			r.AddProblem("agent", err)
			return
		}
	}
	files, err := prepareAgentFiles(cfg, cmdRoot, root, targets)
	if err != nil {
		if cfg.Agent.Set {
			r.AddProblem("agent", err)
		}
		return
	}
	if !cfg.Agent.Set {
		files = slices.DeleteFunc(files, func(f *agent.File) bool { return !f.Marked })
		if len(files) == 0 {
			return
		}
	}
	var ok, stale []string
	for _, f := range files {
		switch f.State() {
		case agent.StateUpToDate:
			ok = append(ok, f.Path)
		case agent.StateHandWritten:
			r.AddProblem("agent", render.Usage("%s was not written by gh dojo agent install", f.Path).
				WithFix(agent.HandWrittenFix(f.Target, config.FileName)))
			return
		default:
			by := ""
			if f.WrittenBy != "" && f.WrittenBy != version && f.State() == agent.StateOutOfDate {
				by = ", written by gh dojo " + f.WrittenBy
			}
			stale = append(stale, f.Path+" ("+stateText(f.State())+by+")")
		}
	}
	if len(stale) > 0 {
		r.AddWarn("agent", "gh dojo agent install", "not up to date with %s or this gh-dojo version: %s",
			config.FileName, strings.Join(stale, ", "))
		return
	}
	r.AddOK("agent", "%s: up to date", strings.Join(ok, ", "))
}

// readAgentNotes reads [agent] notes from root: "" without them, else the
// text with LF line endings, ending in one newline.
func readAgentNotes(cfg *config.Config, root *os.Root) (string, error) {
	name := cfg.Agent.Notes
	if name == "" {
		return "", nil
	}
	// The .git check of the config, again on where links lead.
	resolved, err := agent.Resolve(root, name)
	if err != nil {
		return "", render.Usage("%s: agent.notes: cannot read %s: %v", cfg.Display, name, err).WithCause(err)
	}
	if first, _, _ := strings.Cut(resolved, "/"); strings.EqualFold(first, ".git") {
		return "", render.Usage("%s: agent.notes: %s leads into .git (%s)", cfg.Display, name, resolved).
			WithFix("point notes at a markdown file of the repository, outside .git")
	}
	data, err := agent.ReadFile(root, resolved)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", render.Usage("%s: agent.notes: %s does not exist", cfg.Display, name).
			WithFix(fmt.Sprintf("create %s, or remove notes from [agent] in %s", name, cfg.Display))
	case err != nil:
		e := render.FromError(err)
		e.What = cfg.Display + ": agent.notes: " + e.What
		return "", e
	}
	text := strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), " \t\n")
	if text == "" {
		return "", nil
	}
	for line := range strings.Lines(text) {
		if agent.IsMarker(line, agent.MarkerPrefix) {
			return "", render.Usage("%s: agent.notes: %s has a gh-dojo marker line: %s",
				cfg.Display, name, strings.TrimSpace(line)).
				WithFix("remove the <!-- gh-dojo: ... --> lines from " + name)
		}
	}
	return text + "\n", nil
}
