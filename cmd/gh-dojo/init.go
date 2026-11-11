package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

func init() { register(newInitCmd) }

// defaultPreset is the preset init writes without --preset.
const defaultPreset = "kanban"

// workDir returns the directory init starts from: the config goes to the
// root of its git repository. Tests replace it.
var workDir = os.Getwd

// initOpts are the flags of init.
type initOpts struct {
	preset  string
	owner   string
	project int
	repo    string
	force   bool
	dryRun  bool
}

func newInitCmd() (*cobra.Command, error) {
	var o initOpts
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write " + config.FileName + " for a board and check it against the board",
		Long: "Use it once per repository to point gh dojo at a board. Without --project it\n" +
			"lists the owner's projects (user or organization) and the command to run.\n" +
			"With --project it writes " + config.FileName + " at the repository root:\n" +
			"the board and the preset, which brings the verbs, statuses and fields. Then\n" +
			"it checks the config against the board and lists each status the board\n" +
			"lacks, with how to fix it; the file is kept either way.\n\n" +
			"--owner defaults to the owner of the current repository, and board.repos to\n" +
			"the current repository. An existing config is never overwritten without\n" +
			"--force. Format: " + config.DocsURL,
		Example: `  gh dojo init
  gh dojo init --owner acme --project 3
  gh dojo init --owner acme --project 3 --repo acme/shop --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd, o, cmd.Flags().Changed("project"))
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.preset, "preset", defaultPreset, "`name` of the preset to build on: "+strings.Join(config.Presets(), ", "))
	f.StringVar(&o.owner, "owner", "", "`login` of the user or organization that owns the project (default: the current repository's owner)")
	f.IntVar(&o.project, "project", 0, "project `number`, from its URL; without it the owner's projects are listed")
	f.StringVar(&o.repo, "repo", "", "default repository `owner/name` for board.repos (default: the current repository)")
	f.BoolVar(&o.force, "force", false, "overwrite an existing "+config.FileName)
	f.BoolVar(&o.dryRun, "dry-run", false, "print the file instead of writing it")
	return cmd, nil
}

// runInit lists the owner's projects (no --project) or writes the config
// and checks it against the board. Requests: ceil(projects/100) for the
// list, one schema read for the config.
func runInit(cmd *cobra.Command, o initOpts, projectSet bool) error {
	help := cmd.CommandPath() + " --help"
	if !slices.Contains(config.Presets(), o.preset) {
		return render.Usage("unknown preset %q", o.preset).WithAllowed(config.Presets()...).WithFix(help)
	}
	if projectSet && o.project <= 0 {
		return render.Usage("invalid --project %d: want the project number from its URL", o.project).
			WithFix(o.command(o.owner, "", ""))
	}
	if o.repo != "" {
		if _, err := issue.ParseRepo(o.repo); err != nil {
			return render.FromError(err).WithFix(help)
		}
	}
	a := newApp()
	current, currentErr := a.CurrentRepo()
	if o.owner == "" {
		if currentErr != nil {
			return render.Usage("--owner is required: the current repository is unknown: %v", currentErr).
				WithFix(o.command("<login>", "", "")).WithCause(currentErr)
		}
		o.owner, _, _ = strings.Cut(current, "/")
	}
	if err := (board.Ref{Owner: o.owner, Number: 1}).Validate(); err != nil {
		return render.FromError(err).WithFix(help)
	}
	if !projectSet {
		return listInitProjects(cmd, a, o, current)
	}
	repo := o.repo
	if repo == "" && currentErr == nil {
		if _, err := issue.ParseRepo(current); err == nil {
			repo = current
		}
	}
	return writeInitConfig(cmd, a, o, repo)
}

// command returns the init command line for these options with owner and
// project (left out when ""), then extra flags.
func (o initOpts) command(owner, project, extra string) string {
	parts := []string{"gh dojo init"}
	if o.preset != defaultPreset {
		parts = append(parts, "--preset "+o.preset)
	}
	if owner != "" {
		parts = append(parts, "--owner "+owner)
	}
	if project != "" {
		parts = append(parts, "--project "+project)
	}
	if o.repo != "" {
		parts = append(parts, "--repo "+o.repo)
	}
	if extra != "" {
		parts = append(parts, extra)
	}
	return strings.Join(parts, " ")
}

// listInitProjects prints the owner's projects, one line each, and fails
// with the command that writes the config: --project is required.
//
//	4  IT Stupino Fall 2026 Kanban  (0 items)
//	5  gh-dojo  (36 items)
func listInitProjects(cmd *cobra.Command, a *App, o initOpts, current string) error {
	ps, err := board.ListProjects(cmd.Context(), a.Client, o.owner)
	if err != nil {
		return err
	}
	kind := strings.ToLower(ps.OwnerType)
	if kind == "" {
		kind = "owner"
	}
	if len(ps.Projects) == 0 {
		return render.NotFound("%s %s has no projects this token can see", kind, ps.Owner).
			WithFix(fmt.Sprintf("gh project create --owner %s --title <title>", ps.Owner))
	}
	res := initProjects{Owner: ps.Owner, OwnerType: kind, Projects: []initProject{}}
	for _, p := range ps.Projects {
		res.Projects = append(res.Projects, initProject{Number: p.Number, Title: oneLineText(p.Title), Items: p.Items, Closed: p.Closed})
	}
	noun := "projects"
	if len(ps.Projects) == 1 {
		noun = "project"
	}
	project := "<number>"
	if p := pickProject(ps.Projects, current); p != nil {
		project = strconv.Itoa(p.Number)
		res.Suggested = &p.Number
	}
	res.Command = o.command(ps.Owner, project, "")
	return emitResult(cmd, res, res.write, render.Usage("choose a project: --project is required (%s %s has %d %s, listed above)",
		kind, ps.Owner, len(ps.Projects), noun).WithFix(res.Command))
}

// initProjects is the owner's projects init lists without --project, and
// its JSON. The command still exits 1: --project is required.
type initProjects struct {
	Owner     string        `json:"owner"`
	OwnerType string        `json:"owner_type"` // user or organization
	Projects  []initProject `json:"projects"`
	// Suggested is the project the command names, null when the choice is
	// not clear; Command writes the config ("<number>" when not clear).
	Suggested *int   `json:"suggested_project"`
	Command   string `json:"command"`
}

type initProject struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Items  int    `json:"items"`
	Closed bool   `json:"closed"`
}

// write prints one line per project:
//
//	4  IT Stupino Fall 2026 Kanban  (0 items)
//	5  gh-dojo  (36 items)
func (r initProjects) write(w io.Writer) {
	width := 0
	for _, p := range r.Projects {
		width = max(width, len(strconv.Itoa(p.Number)))
	}
	for _, p := range r.Projects {
		items := fmt.Sprintf("%d items", p.Items)
		if p.Items == 1 {
			items = "1 item"
		}
		if p.Closed {
			items += ", closed"
		}
		fmt.Fprintf(w, "%*d  %s  (%s)\n", width, p.Number, p.Title, items)
	}
}

// pickProject returns the project the fix command should name: the only
// open one, else the only open one titled like the current repository, or
// nil when the choice is not clear.
func pickProject(ps []board.Project, current string) *board.Project {
	var open, named []*board.Project
	_, name, _ := strings.Cut(current, "/")
	for i := range ps {
		p := &ps[i]
		if p.Closed {
			continue
		}
		open = append(open, p)
		if name != "" && strings.EqualFold(strings.TrimSpace(p.Title), name) {
			named = append(named, p)
		}
	}
	switch {
	case len(open) == 1:
		return open[0]
	case len(named) == 1:
		return named[0]
	}
	return nil
}

// writeInitConfig writes the config for board o.owner/o.project with
// board.repos = [repo], left out when repo is "" (prints it with
// --dry-run), and checks it against the board: one schema read. A
// missing board writes nothing; a mismatch keeps the file and lists each
// problem.
func writeInitConfig(cmd *cobra.Command, a *App, o initOpts, repo string) error {
	wd, err := workDir()
	if err != nil {
		return render.Usage("cannot get the current directory: %v", err).WithCause(err)
	}
	root, err := gitRoot(wd)
	if err != nil {
		return err
	}
	path := filepath.Join(root, filepath.FromSlash(config.FileName))
	display := config.FileName
	if rel, err := filepath.Rel(wd, path); err == nil {
		display = filepath.ToSlash(rel)
	}
	project := strconv.Itoa(o.project)
	if !o.force && !o.dryRun {
		if _, err := os.Lstat(path); err == nil {
			return render.Usage("%s already exists; init does not overwrite it", display).
				WithFix(o.command(o.owner, project, "--force"))
		}
	}

	s, err := a.Loader.Fetch(cmd.Context(), board.Ref{Owner: o.owner, Number: o.project})
	if err != nil {
		if e := render.FromError(err); e.Code == render.ExitNotFound && e.Fix != "" {
			return e.WithFix(o.command(o.owner, "", "")) // a missing board: list the projects
		}
		return err
	}
	data, err := initConfig(o.preset, repo, s)
	if err != nil {
		return render.Internal("cannot build the config").WithCause(err)
	}
	cfg, err := config.Parse(data, display)
	if err != nil {
		return render.Internal("the generated config does not load").WithCause(err)
	}

	effects := []string{"with preset " + o.preset}
	if repo != "" {
		effects = append(effects, "repo "+repo)
	} else {
		effects = append(effects, "no repos (the current repository is used)")
	}
	problems := checkInitConfig(cfg, s)
	if len(problems) == 0 {
		effects = append(effects, "matches the board")
	}
	res := initWritten{
		Path: display, Written: !o.dryRun, Board: s.Ref().String(), BoardTitle: oneLineText(s.Title),
		Preset: o.preset, Repos: []string{}, Matches: len(problems) == 0, Problems: []initProblemJSON{},
		Content: string(data),
	}
	if repo != "" {
		res.Repos = []string{repo}
	}
	for _, p := range problems {
		res.Problems = append(res.Problems, initProblemJSON{Problem: p.what, Fix: p.fix})
	}
	subject := fmt.Sprintf("wrote %s for board %s", display, s.Ref())
	var text func(io.Writer)
	if o.dryRun {
		// The file on stdout, so it can be redirected; the line on stderr.
		subject = fmt.Sprintf("dry run, not written: %s for board %s", display, s.Ref())
		text = func(w io.Writer) {
			_, _ = w.Write(data)
			fmt.Fprintln(cmd.ErrOrStderr(), render.Confirm(subject, s.Title, effects...))
		}
	} else {
		if err := writeFile(path, data); err != nil {
			return render.Usage("cannot write %s: %v", display, err).WithCause(err)
		}
		text = func(w io.Writer) { fmt.Fprintln(w, render.Confirm(subject, s.Title, effects...)) }
	}
	err = emit(cmd, res, text)
	if len(problems) == 0 {
		return err
	}
	if err != nil { // the mismatch decides the exit code
		render.Warn(cmd.ErrOrStderr(), "%s", render.FromError(err).What)
	}
	fix := "gh dojo list" // checks the config against the board again
	if o.dryRun {
		fix = o.command(o.owner, project, "")
	}
	return reportProblems(cmd.ErrOrStderr(), cfg, s, problems, o.dryRun, fix)
}

// initWritten is the config init wrote, or would write with --dry-run,
// and its JSON. With problems the command exits 1; the file is kept.
type initWritten struct {
	Path       string   `json:"path"`    // relative to the current directory
	Written    bool     `json:"written"` // false for --dry-run
	Board      string   `json:"board"`   // owner/number
	BoardTitle string   `json:"board_title"`
	Preset     string   `json:"preset"`
	Repos      []string `json:"repos"` // board.repos; none: the current repository is used
	// Matches says the config matches the board; else Problems lists what
	// does not, each with its fix (also on stderr).
	Matches  bool              `json:"matches_board"`
	Problems []initProblemJSON `json:"problems"`
	Content  string            `json:"content"` // the file's text
}

type initProblemJSON struct {
	Problem string `json:"problem"`
	Fix     string `json:"fix"`
}

// gitRoot returns dir or the nearest parent that contains .git.
func gitRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", render.Usage("cannot resolve directory %s: %v", dir, err).WithCause(err)
	}
	for d := abs; ; {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", render.Usage("not in a git repository: %s goes to the repository root", config.FileName).
				WithFix("cd into the repository's working copy and run gh dojo init again")
		}
		d = parent
	}
}

// writeFile writes data to path, creating its directory.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// initTemplate is the config init writes: the board and the preset, with
// nothing the preset provides already.
var initTemplate = template.Must(template.New("dojo.toml").Parse(`# gh-dojo: the board and the process verbs of this repository.
# Format and keys: {{.Docs}}
# Preset {{.Preset}} brings the verbs, statuses and fields; override any of
# them below, e.g. [verbs.start] when = "..." in your project's language.
# gh dojo --help shows the verbs this file gives.

version = {{.Version}}
preset = {{printf "%q" .Preset}}

[board]
owner = {{printf "%q" .Owner}}
project = {{.Project}}{{if .Title}}  # {{.Title}}{{end}}
{{if .Repo}}repos = [{{printf "%q" .Repo}}]  # the first one is the default repository
{{else}}# repos = ["owner/name"]  # unset: the current repository (GH_REPO, else the git remote)
{{end}}`))

// initConfig renders initTemplate for board s.
func initConfig(preset, repo string, s *board.Schema) ([]byte, error) {
	var b bytes.Buffer
	err := initTemplate.Execute(&b, map[string]any{
		"Docs":    config.DocsURL,
		"Version": config.Version,
		"Preset":  preset,
		"Owner":   s.Owner,
		"Project": s.Number,
		"Title":   oneLineText(s.Title),
		"Repo":    repo,
	})
	return b.Bytes(), err
}

// oneLineText makes text safe for one line of output or a TOML comment:
// control characters become spaces, runs of spaces one.
func oneLineText(text string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)), " ")
}

// initProblem is one mismatch between the written config and the board.
type initProblem struct {
	what, fix string
}

// checkInitConfig lists what board.Check rejects, one problem per missing
// field and per missing status with every key that names it. A rejection
// it cannot break down is one problem with Check's own text and fix.
func checkInitConfig(cfg *config.Config, s *board.Schema) []initProblem {
	checkErr := board.Check(cfg, s)
	if checkErr == nil {
		return nil
	}
	problems := boardProblems(cfg, s)
	if len(problems) == 0 {
		e := render.FromError(checkErr)
		what, _, _ := strings.Cut(e.What, "\n")
		problems = append(problems, initProblem{what: what, fix: e.Fix})
	}
	return problems
}

// boardProblems lists the fields and statuses the config names that the
// board lacks.
func boardProblems(cfg *config.Config, s *board.Schema) []initProblem {
	var problems []initProblem
	var status *board.Field
	for _, name := range cfg.FieldNames() {
		key := "fields." + name
		f, err := s.Field(cfg.Fields[name])
		switch {
		case err != nil:
			kind := "a field"
			switch name {
			case "status":
				kind = "a single-select field"
			case "sprint":
				kind = "an iteration field"
			}
			problems = append(problems, initProblem{
				what: fmt.Sprintf("board %s has no field %q (%s)%s", s.Ref(), cfg.Fields[name], key, suggestion(err)),
				fix: fmt.Sprintf("add %s %q on the board (%s), or set [fields] %s = \"…\" in %s",
					kind, cfg.Fields[name], s.URL, name, cfg.Display),
			})
		case name == "status" && f.Type != board.TypeSingleSelect:
			problems = append(problems, initProblem{
				what: fmt.Sprintf("field %q on board %s is %s, not single-select (%s)", f.Name, s.Ref(), f.Type, key),
				fix:  fmt.Sprintf("set [fields] %s = \"…\" in %s to a single-select field", name, cfg.Display),
			})
		case name == "status":
			status = f
		case name == "sprint" && f.Type != board.TypeIteration:
			problems = append(problems, initProblem{
				what: fmt.Sprintf("field %q on board %s is %s, not an iteration field (%s)", f.Name, s.Ref(), f.Type, key),
				fix:  fmt.Sprintf("set [fields] %s = \"…\" in %s to an iteration field", name, cfg.Display),
			})
		}
	}
	if status == nil {
		return problems
	}
	var order []string
	keys := map[string][]string{}
	errs := map[string]error{}
	for _, ref := range cfg.StatusRefs() {
		_, err := status.Option(ref.Status)
		if err == nil {
			continue
		}
		if _, seen := keys[ref.Status]; !seen {
			order = append(order, ref.Status)
			errs[ref.Status] = err
		}
		keys[ref.Status] = append(keys[ref.Status], ref.Key)
	}
	for _, name := range order {
		var overrides []string
		for _, key := range keys[name] {
			overrides = append(overrides, override(key))
		}
		problems = append(problems, initProblem{
			what: fmt.Sprintf("board %s has no status %q, used by %s%s",
				s.Ref(), name, strings.Join(keys[name], ", "), suggestion(errs[name])),
			fix: fmt.Sprintf("add %q to field %q on the board (%s), or override in %s: %s",
				name, status.Name, s.URL, cfg.Display, strings.Join(overrides, "; ")),
		})
	}
	return problems
}

// suggestion returns the "; did you mean ...?" part of a lookup error.
func suggestion(err error) string {
	what := render.FromError(err).What
	if i := strings.Index(what, "; did you mean"); i >= 0 {
		return what[i:]
	}
	return ""
}

// override shows how to set a config key in TOML: "verbs.wait.to" →
// `[verbs.wait] to = "…"`, "verbs.start.from" → `[verbs.start] from = […]`.
func override(key string) string {
	i := strings.LastIndex(key, ".")
	table, name := key[:i], key[i+1:]
	value := `"…"`
	if name == "from" || name == "final" || name == "resolved" {
		value = "[…]"
	}
	return fmt.Sprintf("[%s] %s = %s", table, name, value)
}

// reportProblems writes each problem with its fix and returns the error
// that ends init: exit code 1, the file kept.
func reportProblems(w io.Writer, cfg *config.Config, s *board.Schema, problems []initProblem, dryRun bool, fix string) error {
	for _, p := range problems {
		fmt.Fprintf(w, "problem: %s\n  fix: %s\n", p.what, p.fix)
	}
	noun := "problems"
	if len(problems) == 1 {
		noun = "problem"
	}
	what := fmt.Sprintf("%s does not match board %s: %d %s above; the file is kept, fix the board or the file",
		cfg.Display, s.Ref(), len(problems), noun)
	if dryRun {
		what = fmt.Sprintf("%s would not match board %s: %d %s above", cfg.Display, s.Ref(), len(problems), noun)
	}
	e := render.Usage("%s", what).WithFix(fix)
	// The board's statuses, or its single-select fields when the status
	// field is the problem.
	if f, err := s.Field(cfg.StatusField()); err == nil && f.Type == board.TypeSingleSelect {
		e = e.WithAllowed(f.OptionNames()...)
	} else {
		var fields []string
		for _, f := range s.Fields {
			if f.Type == board.TypeSingleSelect {
				fields = append(fields, f.Name)
			}
		}
		e = e.WithAllowed(fields...)
	}
	return e.WithCause(errors.Join(errInitMismatch, board.Check(cfg, s)))
}

// errInitMismatch marks the error of a config that does not match its
// board.
var errInitMismatch = errors.New("config does not match the board")
