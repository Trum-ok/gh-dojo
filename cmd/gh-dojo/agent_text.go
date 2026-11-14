package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Trum-ok/gh-dojo/internal/agent"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/spf13/cobra"
)

// agentDoc is what agent install writes: this project's process as
// instructions for an agent, in English, built from the config (through
// describeProcess, so it says what describe says), followed by the
// project's [agent] notes as they are. It depends only on the config, the
// notes and this build's commands, never on the directory it runs in.
type agentDoc struct {
	cfg *config.Config
	// p is describe's process of cfg without what rules explains (the
	// blockers check and link, confirm, batch), so a verb's line does not
	// say it twice.
	p     *process
	cmds  []agentCommand // the built-in commands, for the command table
	notes string         // "" or markdown ending in a newline
}

type agentCommand struct{ name, short string }

// newAgentDoc collects the doc of cfg. root gives the commands this build
// has, so a command added later (next, release) shows up without a list
// to keep here.
func newAgentDoc(cfg *config.Config, root *cobra.Command, notes string) *agentDoc {
	plain := *cfg
	plain.Verbs = slices.Clone(cfg.Verbs)
	for i := range plain.Verbs {
		plain.Verbs[i].Blockers, plain.Verbs[i].Confirm, plain.Verbs[i].Batch = "", false, false
		plain.Verbs[i].SubIssues = ""
	}
	d := &agentDoc{cfg: cfg, p: describeProcess(&plain), notes: notes}
	var walk func(c *cobra.Command, prefix string)
	walk = func(c *cobra.Command, prefix string) {
		for _, sub := range c.Commands() {
			if !sub.IsAvailableCommand() || sub.GroupID == verbsGroup.ID || sub.Name() == "help" {
				continue
			}
			if sub.HasAvailableSubCommands() {
				walk(sub, prefix+sub.Name()+" ")
				continue
			}
			d.cmds = append(d.cmds, agentCommand{prefix + sub.Name(), sub.Short})
		}
	}
	walk(root, "")
	return d
}

// skill is the Claude Code skill: front matter, the generated marker, the
// instructions with a top-level heading, then the notes.
//
// Claude Code reads at most maxDescription characters of the description:
// a longer one, with [agent] description, is an error.
func (d *agentDoc) skill() (string, error) {
	desc := d.description()
	if n := utf8.RuneCountInString(desc); n > maxDescription {
		return "", render.Usage("%s: agent.description makes the skill's description %d characters, over %d",
			d.cfg.Display, n, maxDescription).WithFix("shorten description in [agent] of " + d.cfg.Display)
	}
	var b strings.Builder
	b.WriteString("---\nname: gh-dojo\ndescription: " + yamlString(desc) + "\n---\n\n")
	b.WriteString(agent.Generated(config.FileName) + "\n\n")
	b.WriteString(d.body(1))
	if d.notes != "" {
		b.WriteString("\n" + d.notes)
	}
	return b.String(), nil
}

// maxDescription is the longest skill description Claude Code takes.
const maxDescription = 1024

// section is the section of a shared file (AGENTS.md): the instructions one
// heading level down, then the notes; Prepare adds the markers.
func (d *agentDoc) section() string {
	s := d.body(2)
	if d.notes != "" {
		s += "\n" + d.notes
	}
	return s
}

// description tells an agent when to load the skill: whenever it deals
// with this project's issues; [agent] description adds to it.
func (d *agentDoc) description() string {
	whose := "this repository"
	if r := d.cfg.Board.Repos; len(r) > 0 {
		whose = strings.Join(r, " and ")
	}
	verbs := ""
	if names := d.cfg.VerbNames(); len(names) > 0 {
		verbs = " (" + strings.Join(names, ", ") + ")"
	}
	s := fmt.Sprintf("How to handle the issues of %s through gh dojo: the GitHub Projects board "+
		"(project %d of %s), its statuses and process verbs%s. Load it whenever you take, read, create, "+
		"comment on, block or move an issue of %s, finish work on one, or look for what to work on next.",
		whose, d.cfg.Board.Project, d.cfg.Board.Owner, verbs, whoseShort(d.cfg))
	if extra := d.cfg.Agent.Description; extra != "" {
		s += " " + extra
	}
	return s
}

func whoseShort(cfg *config.Config) string {
	if len(cfg.Board.Repos) > 1 {
		return "these repositories"
	}
	return "this repository"
}

// yamlString quotes s for YAML front matter: a JSON string is a valid
// double-quoted YAML scalar.
func yamlString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// mdWriter writes markdown blocks separated by blank lines; level is the
// level of the doc's own heading.
type mdWriter struct {
	b     strings.Builder
	level int
}

func (w *mdWriter) heading(depth int, text string) {
	w.block(strings.Repeat("#", w.level+depth) + " " + text)
}

func (w *mdWriter) block(text string) {
	if w.b.Len() > 0 {
		w.b.WriteString("\n")
	}
	w.b.WriteString(text + "\n")
}

func (w *mdWriter) para(format string, a ...any) { w.block(fmt.Sprintf(format, a...)) }

func (w *mdWriter) list(items []string) {
	if len(items) > 0 {
		w.block("- " + strings.Join(items, "\n- "))
	}
}

func code(s string) string { return "`" + s + "`" }

func codes(list []string) string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = code(s)
	}
	return strings.Join(out, ", ")
}

// body is the generated instructions; level is the level of their heading.
func (d *agentDoc) body(level int) string {
	w := &mdWriter{level: level}
	cfg := d.cfg
	w.heading(0, "Issues and the board: gh dojo")

	where := "in the current repository"
	if len(cfg.Board.Repos) > 0 {
		where = "of " + strings.Join(cfg.Board.Repos, ", ")
	}
	preset := ""
	if cfg.Preset != "" {
		preset = " (preset " + code(cfg.Preset) + ")"
	}
	w.para("This project tracks the issues %s on the GitHub Projects board of %s, project %d, with the process in %s%s. "+
		"Take, move, comment on, block and create issues **only** through `gh dojo`: never change a status, "+
		"a comment, a blocked-by link or an issue with `gh issue`, `gh project` or the API. For what gh dojo "+
		"cannot do, such as editing an issue's title, body, labels or assignees, use `gh issue` only when the "+
		"task needs it or the user asks.", where, cfg.Board.Owner, cfg.Board.Project, code(config.FileName), preset)
	w.para("Start with `gh dojo describe`: the verbs, when to use each, what they need and do along the way, " +
		"the lifecycle and the exit codes, read from the config without the network. `gh dojo <verb> --help` " +
		"has the details of one verb. Read them rather than guess. When the setup is broken (token, board, " +
		"config), `gh dojo doctor` names each problem with the command that fixes it.")

	d.repos(w)
	d.statuses(w)
	d.verbs(w)
	d.reading(w)
	d.writing(w)
	d.repeats(w)
	d.session(w)
	d.exitCodes(w)
	d.json(w)
	d.commands(w)
	return w.b.String()
}

// repos says how commands name issues, of the default repository and of
// others.
func (d *agentDoc) repos(w *mdWriter) {
	def := "the current repository"
	if len(d.cfg.Board.Repos) > 0 {
		def = code(d.cfg.Board.Repos[0])
	}
	newRepo := ""
	if d.hasCommand("new") {
		newRepo = " `gh dojo new --repo owner/repo` creates an issue in another repository of `board.repos`."
	}
	w.para("A bare `12` or `#12` is an issue of %s; an issue of any other repository is `owner/repo#12` or its "+
		"URL, wherever a command takes an issue: verbs and their batches, `show`, `comment`, both sides of "+
		"`block` and `unblock`, `--by` and `--blocked-by`.%s The text names an issue of %s `#12` and any other "+
		"`owner/repo#12`; the JSON always writes `owner/repo#12`, and each issue has its `repo`. Free text in "+
		"`--by` goes into the comment as is, so on an issue of another repository a `#12` in it links there.",
		def, newRepo, def)
}

func (d *agentDoc) verbs(w *mdWriter) {
	w.heading(1, "Verbs")
	if len(d.p.Verbs) == 0 {
		w.para("This project has no verbs: they are added under `[verbs]` in %s.", code(config.FileName))
		return
	}
	intro := "Run a verb as `gh dojo <verb> N` (`N`, `#N` or `owner/repo#N`); `--dry-run` prints its plan (status, links, " +
		"the comment it would post) without changing anything. A verb adds the issue to the board if it " +
		"is missing and refuses a closed issue unless noted. It confirms in one line"
	if ex := d.confirmExample(); ex != "" {
		intro += ", such as " + code(ex)
	}
	w.para("%s: there is no need to read the issue again.", intro)
	for i, pv := range d.p.Verbs {
		v := d.cfg.Verbs[i]
		w.heading(2, code(v.Name))
		w.para("**When:** %s", v.When)
		from, to := "any status", "status unchanged"
		if len(pv.From) > 0 {
			from = strings.Join(pv.From, ", ")
		}
		if pv.To != nil {
			to = *pv.To
		}
		line := code("gh dojo "+v.Name+" N") + ": " + from + " → " + to
		if facts := d.facts(v, pv); facts != "" {
			line += "; " + facts
		}
		w.para("%s.", line)
		w.list(d.rules(v))
	}
}

// confirmExample is a confirmation line of the first verb that moves an
// issue between named statuses, "" when none does.
func (d *agentDoc) confirmExample() string {
	for _, v := range d.cfg.Verbs {
		if v.To != "" && len(v.From) > 0 && v.From[0] != v.To {
			ex := "#42 " + v.From[0] + " → " + v.To
			// A template with text of its own comments every time.
			if strings.TrimSpace(strings.NewReplacer("{comment}", "", "{by}", "", "{session}", "", "{assignee}", "").Replace(v.Comment)) != "" {
				ex += ", comment left"
			}
			return ex
		}
	}
	return ""
}

// facts is describe's line of the verb, with the whole comment template
// in place of its first characters.
func (d *agentDoc) facts(v config.Verb, pv processVerb) string {
	pv.Effects = slices.Clone(pv.Effects)
	for i, e := range pv.Effects {
		if strings.HasPrefix(e, `comments "`) {
			pv.Effects[i] = `comments "` + singleLine(v.Comment) + `"`
		}
	}
	return pv.facts()
}

// rules says what an agent must do about the verb's checks and effects
// beyond describe's line: one sentence each.
func (d *agentDoc) rules(v config.Verb) []string {
	var r []string
	if v.Confirm {
		r = append(r, "It is the user's decision: without `--yes` it exits with code 2. Ask the user, and "+
			"run it again with `--yes` only if they agree; never add `--yes` on your own.")
	}
	switch v.Blockers {
	case config.BlockersWarn:
		r = append(r, "With an unresolved blocker it warns on stderr and still runs: read `gh dojo show N` "+
			"and decide whether to take the issue, or ask the user.")
	case config.BlockersDeny:
		r = append(r, "It is refused (exit code 2) while a blocker is unresolved: `gh dojo show N` lists them. "+
			"Never move the status another way.")
	case config.BlockersLink:
		next := d.verbsFrom(v.To)
		s := "`--by 12` (also `#12`, `owner/repo#12` or an issue's URL; several comma-separated, " +
			"`--by 12,13`) also marks the issue blocked by #12. Text (`--by \"the user's decision\"`) only goes " +
			"into the comment; issues mixed with text are an error (exit code 1); a pull request is named " +
			"in the comment without a link."
		if v.To != "" && len(next) > 0 {
			s += " Once the blocker is resolved, go on with " + strings.Join(quoted(next), " or ") + "."
		}
		r = append(r, s)
	}
	switch v.SubIssues {
	case config.BlockersWarn:
		r = append(r, "With an unresolved sub-issue it warns on stderr and still runs: read `gh dojo show N` "+
			"and finish the sub-issues first, or ask the user.")
	case config.BlockersDeny:
		r = append(r, "It is refused (exit code 2) while a sub-issue is unresolved: `gh dojo show N` lists them. "+
			"Finish them first, or ask the user; never move the status another way.")
	}
	if v.AssignArg {
		r = append(r, fmt.Sprintf("`--%s <login>` names the user to assign (`--%s @me` is you); one who cannot "+
			"be assigned issues in the repository is exit code 1, with the list of who can. Ask the user "+
			"whom to assign if you do not know.", config.ArgAssignee, config.ArgAssignee))
	}
	if v.Batch {
		r = append(r, fmt.Sprintf("It takes several numbers at once, `gh dojo %s 12 13 14`: each issue is "+
			"checked and written on its own, one that fails is skipped, and a summary names each issue not done. "+
			"Running the same command again touches only what is left.", v.Name))
		if d.hasCommand("release") {
			r = append(r, d.releaseRule(v))
		}
	}
	if strings.TrimSpace(v.Comment) == "{"+config.ArgComment+"}" && !slices.Contains(v.Require, config.ArgComment) {
		r = append(r, "It comments only when given `--comment`.")
	}
	if v.Close != "" {
		r = append(r, "An issue closed already stays closed as it is.")
	}
	return r
}

// releaseRule says how gh dojo release applies batch verb v to the
// issues of a release, with the keywords of the config.
func (d *agentDoc) releaseRule(v config.Verb) string {
	found := "the merged pull requests of its commits close"
	if k := d.cfg.Release.Keywords; len(k) > 0 {
		found += ", or its commit messages name after " + strings.Join(quoted(k), ", ")
	}
	return fmt.Sprintf("For a release, `gh dojo release <tag> --verb %s --dry-run` lists the issues the release "+
		"includes (those %s), how each was found and its plan; without `--dry-run` it runs `%s` on them as one "+
		"batch. An open issue whose status it does not start from is listed as not ready and left out, not "+
		"failed; only a later release whose commits name it again picks it up, so once it is ready run `gh dojo "+
		"%s N` on it. A closed issue it does not start from is refused (exit code 2): ask the user. The start is "+
		"the previous version tag; the first release needs `--since <tag or commit>`.%s",
		v.Name, found, v.Name, v.Name, d.messageRule())
}

// messageRule says, when commit messages count for a release, how release
// reads their numbers and how to write an example of one; "" without
// keywords.
func (d *agentDoc) messageRule() string {
	if len(d.cfg.Release.Keywords) == 0 {
		return ""
	}
	return " A number only commit messages name that is no issue (a pull request, a missing issue, or one of a " +
		"repository outside `board.repos` not found or hidden from the token) is left out with a warning, not " +
		"failed; `--exclude N` leaves out any issue found by mistake. In a commit message, put an example of an " +
		"issue reference in backticks (`` `Fixes #12` ``): code does not count."
}

// verbsFrom lists the verbs that can start from status s.
func (d *agentDoc) verbsFrom(s string) []string {
	var names []string
	for _, v := range d.cfg.Verbs {
		if slices.Contains(v.From, s) {
			names = append(names, v.Name)
		}
	}
	return names
}

func (d *agentDoc) statuses(w *mdWriter) {
	st := d.cfg.Statuses
	var items []string
	if st.Initial != "" {
		items = append(items, fmt.Sprintf("%s is the initial status: `gh dojo new` puts new issues there, and an "+
			"issue without a status or not on the board counts as %s.", code(st.Initial), code(st.Initial)))
	}
	if len(st.Final) > 0 {
		items = append(items, fmt.Sprintf("Final: %s. `gh dojo list` hides issues there unless `--all`.", codes(st.Final)))
	}
	if len(st.Resolved) > 0 {
		items = append(items, fmt.Sprintf("A blocker is resolved, no longer blocking, when it is closed or its board "+
			"status is %s: its work is done though the issue may be open.", strings.Join(quoted(st.Resolved), " or ")))
	} else {
		items = append(items, "A blocker is resolved, no longer blocking, only when it is closed.")
	}
	items = append(items, "A blocker whose status could not be read (`status not read: …`) counts as unresolved.")
	if slices.ContainsFunc(d.cfg.Verbs, config.Verb.ChecksSubIssues) {
		items = append(items, "A sub-issue is resolved, or not, by the same rule.")
	}
	w.heading(1, "Lifecycle")
	if path, verbs, skip := d.lifecycle(); len(path) > 1 {
		steps := make([]string, len(path))
		for i, st := range path {
			steps[i] = code(st)
			if skip[i] {
				steps[i] = "(" + steps[i] + ")"
			}
		}
		line := strings.Join(steps, " → ") + ", through " + codes(verbs) + "."
		if slices.Contains(skip, true) {
			line += " A status in parentheses may be skipped."
		}
		w.para("%s Each verb below says which statuses it moves from and to.", line)
	}
	w.list(items)
}

// maxLifecycleSteps bounds the search for the lifecycle path.
const maxLifecycleSteps = 100_000

// lifecycle is the longest way through the statuses, as describe's
// from → to lines draw them: from the initial status, along verbs with a
// from list and a target, preferably ending in a final status. skip marks
// the statuses on it that a verb jumps over. Verbs from any status are
// left out: they lead anywhere. Nil without an initial status.
func (d *agentDoc) lifecycle() (path, verbs []string, skip []bool) {
	cfg := d.cfg
	start := cfg.Statuses.Initial
	if start == "" {
		return nil, nil, nil
	}
	type edge struct{ to, verb string }
	next := map[string][]edge{}
	for _, v := range cfg.Verbs {
		for _, f := range v.From {
			if v.To != "" && f != v.To {
				next[f] = append(next[f], edge{v.To, v.Name})
			}
		}
	}
	final := func(p []string) bool { return slices.Contains(cfg.Statuses.Final, p[len(p)-1]) }
	better := func(p []string) bool {
		switch {
		case path == nil:
			return true
		case final(p) != final(path):
			return final(p)
		}
		return len(p) > len(path)
	}
	steps := 0
	cur, curVerbs := []string{start}, []string{}
	var walk func()
	walk = func() {
		if steps++; steps > maxLifecycleSteps {
			return
		}
		if better(cur) {
			path, verbs = slices.Clone(cur), slices.Clone(curVerbs)
		}
		for _, e := range next[cur[len(cur)-1]] {
			if slices.Contains(cur, e.to) {
				continue
			}
			cur, curVerbs = append(cur, e.to), append(curVerbs, e.verb)
			walk()
			cur, curVerbs = cur[:len(cur)-1], curVerbs[:len(curVerbs)-1]
		}
	}
	walk()
	skip = make([]bool, len(path))
	for a := range path { // a verb from path[a] to path[b] skips what is between
		for _, e := range next[path[a]] {
			for i := a + 1; i < slices.Index(path, e.to); i++ {
				skip[i] = true
			}
		}
	}
	return path, slices.Compact(verbs), skip
}

func quoted(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = code(s)
	}
	return out
}

func (d *agentDoc) reading(w *mdWriter) {
	sprint := ""
	if d.cfg.SprintField() != "" {
		sprint = "sprint, "
	}
	listFilters := "`--status \"<status>\"`, `--status none`, `--mine`, `--label <label>`"
	if d.cfg.SprintField() != "" {
		listFilters += ", `--sprint current`"
	}
	items := []string{
		"`gh dojo show N` before you work on an issue: its status, " + sprint + "milestone, labels, blockers both " +
			"ways (unresolved first, each with its board status), its parent and sub-issues (the same way), " +
			"status history, description and the last 5 " +
			"comments. `gh dojo show 12 13 --brief` prints three lines per issue, `--comments all` the whole " +
			"thread (up to 100), `--full` everything uncut.",
	}
	if d.hasCommand("next") {
		items = append(items, d.nextItem())
	}
	items = append(items, "`gh dojo list`: the open issues on the board, grouped by status; closed issues and "+
		"final statuses are hidden and counted. Filters: "+listFilters+", `--all`, `--limit 100`.")
	w.heading(1, "Reading")
	w.list(items)
}

// hasCommand reports whether this build has the built-in command name.
func (d *agentDoc) hasCommand(name string) bool {
	return slices.ContainsFunc(d.cmds, func(c agentCommand) bool { return c.name == name })
}

// nextItem explains gh dojo next, when this build has it.
func (d *agentDoc) nextItem() string {
	cfg := d.cfg
	where := "without a status"
	if s := cfg.Statuses.Initial; s != "" {
		where = "in " + code(s) + " or without a status"
	}
	filters := "`--mine`, `--label <label>`"
	if cfg.SprintField() != "" {
		filters += ", `--sprint current`"
	}
	s := "`gh dojo next`: what to take next, the open issues " + where + ", with no unresolved blocker, in " +
		"board order, 5 by default (`--limit N`). The blocked ones are hidden and counted; `--all` shows them " +
		"with their blockers. Filters: " + filters + ". Once you pick one, `gh dojo show N`"
	if take := d.verbsFrom(cfg.Statuses.Initial); cfg.Statuses.Initial != "" && len(take) > 0 {
		s += ", then " + code("gh dojo "+take[0]+" N")
	}
	return s + "."
}

func (d *agentDoc) writing(w *mdWriter) {
	cfg := d.cfg
	typ, example := "<label>", "--label bug"
	if t := cfg.Labels.Types; len(t) > 0 {
		typ, example = "<"+strings.Join(t, "|")+">", "--type "+t[0]
	}
	status := ""
	if cfg.Statuses.Initial != "" {
		status = " in " + code(cfg.Statuses.Initial)
	}
	area := ""
	if p := cfg.Labels.AreaPrefix; p != "" {
		area = " `--area X` adds the label " + code(p+"X") + "."
	}
	w.heading(1, "Creating and linking issues")
	w.list([]string{
		"`gh dojo new \"<title>\" --type " + typ + " --body \"…\"`: the issue, its labels, the board" + status +
			", in one command. A long body: `--body-file <file>`, or `--body-file -` for stdin. More labels: " +
			"`--label X`." + area + " `--milestone \"<title>\"`: an open milestone (an unknown one is exit code 1 " +
			"with the list). `--blocked-by 12`: blocked from the start (repeat it, or comma-separate; an issue " +
			"that does not exist is exit code 3, and nothing is created). A title starting with `-` goes after " +
			"`--`, and every flag before it: `gh dojo new " + example + " -- \"-v is ignored\"`.",
		"`gh dojo comment N \"text\"`, or `gh dojo comment N --body-file -`: a comment without changing the status.",
		"`gh dojo block N --by M` marks N blocked by M without changing the status; `gh dojo unblock N --by M` " +
			"removes that. `--by` repeats or takes a comma-separated list; another repository's issue is " +
			"`owner/repo#12` or its URL. A link already there (or already gone) is not an error: the line says " +
			"`already`. A pull request cannot block (exit code 3), nor can an issue N blocks (a cycle, exit code 1).",
	})
}

func (d *agentDoc) repeats(w *mdWriter) {
	w.heading(1, "Repeats are safe")
	w.para("Run the same command again after an error: a verb does not set a status that is already set, nor post " +
		"a comment that is already the latest from gh-dojo; `comment` does not post its last comment again; " +
		"`block` and `unblock` skip links that are already as asked; `new` with the same title finishes the " +
		"issue it created instead of creating another.")
}

func (d *agentDoc) session(w *mdWriter) {
	env := d.cfg.Session.Env
	if len(env) == 0 {
		return
	}
	uses := false
	for _, v := range d.cfg.Verbs {
		uses = uses || strings.Contains(v.Comment, "{session}")
	}
	if !uses {
		return
	}
	w.heading(1, "Session")
	w.para("`{session}` in a comment is a link to your agent session, from %s (the first one set). When none is "+
		"set the verb warns and still runs.", codes(env))
}

func (d *agentDoc) exitCodes(w *mdWriter) {
	w.heading(1, "Exit codes")
	var b strings.Builder
	b.WriteString("| Code | Meaning | What to do |\n|------|---------|------------|")
	for _, c := range render.ExitCodes {
		fmt.Fprintf(&b, "\n| %d | %s | %s |", c.Code, c.Meaning, c.Next)
	}
	w.block(b.String())
	w.para("On an error, read its `error:`, `allowed:` and `fix:` lines. Never work around exit code 2: do not " +
		"move the status another way, ask the user. On 4 and 5, run the same command again (on 4, a bit later).")
	if slices.ContainsFunc(d.cfg.Verbs, func(v config.Verb) bool { return v.Batch }) {
		w.para("A verb on several issues exits 0 when all are done, 5 when some were written and running it " +
			"again may finish the rest, else with the code of the failed ones (2 first, then 1, 4, 3).")
	}
}

func (d *agentDoc) json(w *mdWriter) {
	w.heading(1, "JSON")
	status := "<status>"
	if len(d.cfg.Statuses.Resolved) > 0 {
		status = d.cfg.Statuses.Resolved[0]
	}
	w.para("Every command takes `--json`, `--jq '<expression>'` and `--template`, as gh does; an error is then " +
		"JSON too (`error` with `exit_code`, `message`, `allowed`, `fix`), with the same exit code. Use it to " +
		"pick values out of the output; for everyday work the text is shorter.")
	w.block("```sh\n" +
		"gh dojo list --jq '.issues[] | select(.status == \"" + status + "\") | .number'\n" +
		"gh dojo show 42 --jq '.issues[0].blocked_by[] | select(.resolved | not) | .ref'\n" +
		"```")
}

func (d *agentDoc) commands(w *mdWriter) {
	w.heading(1, "Commands")
	var b strings.Builder
	b.WriteString("| Command | What it does |\n|---------|--------------|")
	for _, c := range d.cmds {
		fmt.Fprintf(&b, "\n| `gh dojo %s` | %s |", c.name, c.short)
	}
	if len(d.p.Verbs) > 0 {
		fmt.Fprintf(&b, "\n| `gh dojo <verb>` | The verbs above: %s |", strings.Join(d.cfg.VerbNames(), ", "))
	}
	w.block(b.String())
}
