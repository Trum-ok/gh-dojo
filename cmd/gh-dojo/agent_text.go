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
	cmds  []agentCommand // the built-in commands, for hasCommand
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
	w.para("Everything below is enough for everyday work; read `gh dojo <command> --help` only when a command " +
		"refuses or you need a flag not named here. When the setup is broken (token, board, config), " +
		"`gh dojo doctor` names each problem with the command that fixes it.")

	d.repos(w)
	d.statuses(w)
	d.verbs(w)
	d.reading(w)
	d.writing(w)
	d.repeats(w)
	d.exitCodes(w)
	d.json(w)
	return w.b.String()
}

// multiRepo reports whether the board takes issues from several
// repositories: only then does the doc explain at length how to name an
// issue of another one.
func (d *agentDoc) multiRepo() bool { return len(d.cfg.Board.Repos) > 1 }

// repos says how commands name issues, of the default repository and of
// others: in one sentence when the board has one repository.
func (d *agentDoc) repos(w *mdWriter) {
	def := "the current repository"
	if len(d.cfg.Board.Repos) > 0 {
		def = code(d.cfg.Board.Repos[0])
	}
	if !d.multiRepo() {
		w.para("A bare `12` or `#12` is an issue of %s; an issue of another repository is `owner/repo#12` or its URL.", def)
		return
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
	intro := "Run a verb as `gh dojo <verb> N`; `--dry-run` prints its plan (status, links, " +
		"the comment it would post) without changing anything. A verb adds the issue to the board if it " +
		"is missing and refuses a closed issue unless noted. It confirms in one line"
	if ex := d.confirmExample(); ex != "" {
		intro += ", such as " + code(ex)
	}
	w.para("%s: there is no need to read the issue again.", intro)
	if slices.ContainsFunc(d.cfg.Verbs, func(v config.Verb) bool { return v.Batch }) {
		w.para("`N…` takes several numbers at once: one that fails is skipped and named in the summary.")
	}
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
		n := " N"
		if v.Batch {
			n = " N…"
		}
		line := code("gh dojo "+v.Name+n) + ": " + from + " → " + to
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
			if strings.TrimSpace(strings.NewReplacer("{comment}", "", "{by}", "", "{session}", "", "{assignee}", "", "{milestone}", "").Replace(v.Comment)) != "" {
				ex += ", comment left"
			}
			return ex
		}
	}
	return ""
}

// facts is describe's line of the verb, with the whole comment template
// in place of its first characters.
// A template that is only {comment}, which is optional, says so instead:
// "comments only with --comment".
func (d *agentDoc) facts(v config.Verb, pv processVerb) string {
	pv.Effects = slices.Clone(pv.Effects)
	only := commentOnly(v)
	for i, e := range pv.Effects {
		if strings.HasPrefix(e, `comments "`) {
			pv.Effects[i] = `comments "` + singleLine(v.Comment) + `"`
			if only {
				pv.Effects[i] = "comments only with --" + config.ArgComment
			}
		}
	}
	if only {
		pv.Optional = slices.DeleteFunc(slices.Clone(pv.Optional), func(o string) bool { return o == "--"+config.ArgComment })
	}
	return pv.facts()
}

// commentOnly reports whether v's comment is only an optional {comment}.
func commentOnly(v config.Verb) bool {
	return strings.TrimSpace(v.Comment) == "{"+config.ArgComment+"}" && !slices.Contains(v.Require, config.ArgComment) &&
		!slices.Contains(v.RequireOneOf, config.ArgComment)
}

// rules says what an agent must do about the verb's checks and effects
// beyond describe's line: one sentence each.
func (d *agentDoc) rules(v config.Verb) []string {
	var r []string
	if v.Confirm {
		r = append(r, "It is the user's decision: without `--yes` it exits with code 2. Ask the user, and "+
			"run it again with `--yes` only if they agree; never add `--yes` on your own.")
	}
	// verbsFor names the verbs that add (remove false) or remove each
	// label: " `hold` adds `on-hold`."
	verbsFor := func(labels []string, remove bool) string {
		does := " adds "
		if remove {
			does = " removes "
		}
		s := ""
		for _, name := range labels {
			if verbs := labelVerbs(d.cfg, v, name, remove); len(verbs) > 0 {
				s += " " + joinWords(quoted(verbs), "or") + does + code(name) + "."
			}
		}
		return s
	}
	if len(v.FromLabels) > 0 {
		r = append(r, "It is refused (exit code 2) unless the issue has "+labelNames(v.FromLabels, "and", code)+
			": `gh dojo show N` lists its labels."+verbsFor(v.FromLabels, false)+
			" Never add a label another way to get it through; ask the user.")
	}
	if len(v.RefuseLabels) > 0 {
		r = append(r, "It is refused (exit code 2) while the issue has "+labelNames(v.RefuseLabels, "or", code)+"."+
			verbsFor(v.RefuseLabels, true)+" Never remove a label another way to get it through; ask the user.")
	}
	switch v.Blockers {
	case config.BlockersWarn:
		r = append(r, "With an unresolved blocker it warns on stderr and still runs: read `gh dojo show N` "+
			"and decide whether to take the issue, or ask the user.")
	case config.BlockersDeny:
		r = append(r, "It is refused (exit code 2) while a blocker is unresolved: `gh dojo show N` lists them. "+
			"Never move the status another way.")
	case config.BlockersLink:
		r = append(r, "`--by 12` also marks the issue blocked by #12; text (`--by \"the user's decision\"`) only "+
			"goes into the comment.")
	}
	switch v.SubIssues {
	case config.BlockersWarn:
		r = append(r, "With an unresolved sub-issue it warns on stderr and still runs: read `gh dojo show N` "+
			"and finish the sub-issues first, or ask the user.")
	case config.BlockersDeny:
		r = append(r, "It is refused (exit code 2) while a sub-issue is unresolved: `gh dojo show N` lists them. "+
			"Finish them first, or ask the user; never move the status another way.")
	}
	if v.ChecksSubIssues() && v.Batch && (v.Close != "" || slices.Contains(d.cfg.Statuses.Resolved, v.To)) {
		r = append(r, fmt.Sprintf("In one run with its sub-issues (`gh dojo %s 40 41 42`) the sub-issues it "+
			"moves count as resolved: they go first, and the parent is refused only if one of them is.", v.Name))
	}
	if v.AssignArg {
		r = append(r, fmt.Sprintf("`--%s <login>` (`@me` is you): ask the user whom to assign if you do not know.",
			config.ArgAssignee))
	}
	if v.MilestoneArg {
		r = append(r, fmt.Sprintf("`--%s <title>`, or a start only that title has (`--%s v1.2`): ask the user "+
			"which milestone if you do not know.", config.ArgMilestone, config.ArgMilestone))
	}
	if v.Batch {
		// A release finishes issues: only a verb that does is pointed there.
		finishes := v.Close != "" || slices.Contains(d.cfg.Statuses.Final, v.To)
		if finishes && d.hasCommand("release") && releaseLacks(v) == "" {
			r = append(r, d.releaseRule(v))
		}
	}
	return r
}

// releaseRule points to gh dojo release for batch verb v: its details
// are in release --help, needed only when a release is made.
func (d *agentDoc) releaseRule(v config.Verb) string {
	return fmt.Sprintf("For a release, `gh dojo release <tag> --verb %s --dry-run` lists the issues the release "+
		"includes and its plan; read `gh dojo release --help` before you run it.", v.Name)
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
	var items []string
	if d.hasCommand("next") {
		items = append(items, d.nextItem())
	}
	items = append(items,
		"`gh dojo show N` before you work on an issue: its status, "+sprint+"milestone, labels, blockers both "+
			"ways (unresolved first, each with its board status), its parent and sub-issues (the same way), "+
			"status history, description and the last 5 comments; a line left out means none. Several numbers, "+
			"`gh dojo show 12 13`, print three lines per issue: read the one you take alone. `--comments all` prints "+
			"the whole thread (up to 100), `--full` everything uncut.",
		"`gh dojo list`: an overview of the board, when the user asks for one: the open issues grouped by status; "+
			"closed issues and final statuses are hidden and counted"+d.foldedList()+". Filters: "+listFilters+
			", `--all`, `--limit 100`.")
	w.heading(1, "Reading")
	w.list(items)
}

// foldedList says which statuses list prints as one line of numbers (see
// list.folded), "" when none.
func (d *agentDoc) foldedList() string {
	var folded []string
	for _, st := range d.cfg.Statuses.Resolved {
		if !slices.Contains(d.cfg.Statuses.Final, st) {
			folded = append(folded, st)
		}
	}
	if len(folded) == 0 {
		return ""
	}
	is := " is one line of numbers"
	if len(folded) > 1 {
		is = " are one line of numbers each"
	}
	return "; " + strings.Join(quoted(folded), " and ") + is + ", `--status " + statusArg(folded[0]) +
		"` lists them with titles"
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
	typ := "<label>"
	if t := cfg.Labels.Types; len(t) > 0 {
		typ = "<" + strings.Join(t, "|") + ">"
	}
	status := ""
	if cfg.Statuses.Initial != "" {
		status = " in " + code(cfg.Statuses.Initial)
	}
	w.heading(1, "Creating and linking issues")
	w.list([]string{
		"`gh dojo new \"<title>\" --type " + typ + " --body \"…\"`: the issue, its labels, the board" + status +
			", in one command; a long body: `--body-file <file>`, or `--body-file -` for stdin. More labels, a " +
			"milestone, blockers or a parent: `gh dojo new --help`.",
		"`gh dojo comment N \"text\"`: a comment without changing the status.",
		"`gh dojo block N --by M` / `gh dojo unblock N --by M`: add or remove a blocked-by link without changing " +
			"the status. An existing issue becomes a sub-issue with `gh issue edit N --parent M`.",
	})
}

func (d *agentDoc) repeats(w *mdWriter) {
	w.heading(1, "Repeats are safe")
	w.para("Run the same command again after an error: a verb does not set a status that is already set, nor post " +
		"a comment that is already the latest from gh-dojo; `comment` does not post its last comment again; " +
		"`block` and `unblock` skip links that are already as asked; `new` with the same title finishes the " +
		"issue it created instead of creating another.")
}

// exitCodes says what to do on an error in a few sentences: the error's
// own fix: line covers the rest.
func (d *agentDoc) exitCodes(w *mdWriter) {
	w.heading(1, "Exit codes")
	w.para("On an error, read its `error:`, `allowed:` and `fix:` lines and do what `fix:` says. Exit code 2 is a " +
		"process check refusing: never work around it (do not move the status another way); ask the user. On 4 " +
		"(network, rate limit or auth) and 5 (partially applied), run the same command again, on 4 a bit later.")
}

func (d *agentDoc) json(w *mdWriter) {
	w.heading(1, "JSON")
	w.para("Every command takes `--json`, `--jq` and `--template`, as gh does, for picking values out; " +
		"for everyday work the text is shorter.")
}
