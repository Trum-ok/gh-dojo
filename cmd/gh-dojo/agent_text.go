package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/agent"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
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
	// blockers and sub-issues checks, the link, confirm, release), so a
	// verb's line does not say it twice.
	p     *describeResult
	cmds  []string // the built-in commands, for hasCommand
	notes string   // "" or markdown ending in a newline
}

// newAgentDoc collects the doc of cfg. root gives the commands this build
// has, so a command added later (next, release) shows up without a list
// to keep here.
func newAgentDoc(cfg *config.Config, root *cobra.Command, notes string) *agentDoc {
	plain := *cfg
	plain.Verbs = slices.Clone(cfg.Verbs)
	for i := range plain.Verbs {
		plain.Verbs[i].Blockers, plain.Verbs[i].Confirm, plain.Verbs[i].SubIssues = "", false, ""
	}
	d := &agentDoc{cfg: cfg, p: describeProcess(&plain), notes: notes}
	for i := range d.p.Verbs {
		d.p.Verbs[i].release = false
	}
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
			d.cmds = append(d.cmds, prefix+sub.Name())
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
	if n := render.RuneLen(desc); n > maxDescription {
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

// code marks s as code: `s`.
func code(s string) string { return "`" + s + "`" }

// codeEach marks each of list as code.
func codeEach(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = code(s)
	}
	return out
}

// codes marks each of list as code and joins them with ", ".
func codes(list []string) string { return strings.Join(codeEach(list), ", ") }

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
	d.errors(w)
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
	intro := "Run a verb as `gh dojo <verb> N`; `--dry-run` prints its plan without changing anything, " +
		"and `--show` also prints the issue's card, as `gh dojo show N` does. A verb adds the issue to the board if it " +
		"is missing and refuses a closed issue unless noted. It confirms in one line"
	if ex := d.confirmExample(); ex != "" {
		intro += ", such as " + code(ex)
	}
	w.para("%s: there is no need to read the issue again.", intro)
	w.para("`N…` takes several numbers at once: one that fails is skipped and named in the summary.")
	// One paragraph per verb, not a heading and a When line: the skill is
	// loaded into every session.
	for i, pv := range d.p.Verbs {
		v := d.cfg.Verbs[i]
		from, to := "any status", "status unchanged"
		if len(pv.From) > 0 {
			from = strings.Join(pv.From, ", ")
		}
		if pv.To != nil {
			to = *pv.To
		}
		line := "**" + code(v.Name) + "** (" + v.When + "): " + code("gh dojo "+v.Name+" N…") + ", " + from + " → " + to
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
			if v.CommentsAlways() {
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
func (d *agentDoc) facts(v config.Verb, pv processVerbJSON) string {
	pv.Effects = slices.Clone(pv.Effects)
	only := commentOnly(v)
	for i, e := range pv.Effects {
		if strings.HasPrefix(e, `comments "`) {
			pv.Effects[i] = `comments "` + render.SingleLine(v.Comment) + `"`
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
				s += " " + render.JoinWords(codeEach(verbs), "or") + does + code(name) + "."
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
		r = append(r, "With an unresolved blocker it warns on stderr and still runs: read the card (`--show`) "+
			"and decide whether to keep the issue, or ask the user.")
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
	if v.ChecksSubIssues() && (v.Close != "" || slices.Contains(d.cfg.Statuses.Resolved, v.To)) {
		r = append(r, fmt.Sprintf("In one run with its sub-issues (`gh dojo %s 40 41 42`) the sub-issues it "+
			"moves count as resolved: they go first, and the parent is refused only if one of them is.", v.Name))
	}
	if v.AssignArg {
		r = append(r, fmt.Sprintf("`--%s <login>` (`@me` is you): ask the user whom to assign if you do not know.",
			config.ArgAssignee))
	}
	if v.MilestoneArg {
		r = append(r, fmt.Sprintf("`--%s <title>`, or a start only that title has (`--%s v1.2`): ask the user "+
			"which milestone if you do not know. `--%s %s` takes the issue out of its milestone.",
			config.ArgMilestone, config.ArgMilestone, config.ArgMilestone, workflow.NoMilestone))
	}
	if v.ClosesDuplicate() {
		r = append(r, fmt.Sprintf("`--%s 17` names the original: the issue is closed as its duplicate and "+
			"GitHub links the two.", config.ArgOf))
	}
	if releaseFinishes(d.cfg, v) && d.hasCommand("release") {
		r = append(r, d.releaseRule(v))
	}
	return r
}

// releaseRule points to gh dojo release for verb v: its details
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
	unread := "; one whose status could not be read (`status not read: …`) still blocks."
	if len(st.Resolved) > 0 {
		items = append(items, fmt.Sprintf("A blocker is resolved, no longer blocking, when it is closed or in %s, "+
			"though it may be open%s", strings.Join(codeEach(st.Resolved), " or "), unread))
	} else {
		items = append(items, "A blocker is resolved, no longer blocking, only when it is closed"+unread)
	}
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
		w.para("%s", line)
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
		"`gh dojo show N` before you work on an issue"+d.takeNote()+": its status, "+sprint+"blockers both ways, "+
			"parent and sub-issues, history, description and the last "+strconv.Itoa(defaultShownComments)+
			" comments; `--comments all` prints the whole thread, `--full` everything uncut (a verb takes both too). "+
			"Several numbers print three lines per issue.",
		"`gh dojo list`: an overview of the board, when the user asks for one: the open issues grouped by status; "+
			"closed issues and final statuses are hidden and counted"+d.foldedList()+". Filters: "+listFilters+
			", `--all`, `--limit 100`.")
	// Measured on a sandbox board: items and items(query:) catch up 1.5 to
	// 3.5 s after a write, the issue itself at once (#54).
	lists := "`gh dojo list`"
	if d.hasCommand("next") {
		lists = "`gh dojo next` and " + lists
	}
	items = append(items, lists+" read the board a few seconds behind writes: an issue just created or moved may "+
		"be missing or in its old status. To check one issue, `gh dojo show N`.")
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
	return "; " + strings.Join(codeEach(folded), " and ") + is + ", `--status " + statusArg(folded[0]) +
		"` lists them with titles"
}

// hasCommand reports whether this build has the built-in command name.
func (d *agentDoc) hasCommand(name string) bool {
	return slices.Contains(d.cmds, name)
}

// takeVerb is the verb that takes an issue: the first one from the
// initial status, "" when none.
func (d *agentDoc) takeVerb() string {
	if d.cfg.Statuses.Initial == "" {
		return ""
	}
	if take := d.verbsFrom(d.cfg.Statuses.Initial); len(take) > 0 {
		return take[0]
	}
	return ""
}

// takeNote points show's reader at the take verb's --show: taking an
// issue and reading it is one command (#57).
func (d *agentDoc) takeNote() string {
	take := d.takeVerb()
	if take == "" {
		return ""
	}
	return " (to take it as well, " + code("gh dojo "+take+" N --show") + " prints the same card)"
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
		"board order, " + strconv.Itoa(defaultNextLimit) + " by default (`--limit N`). The blocked ones are hidden and counted; `--all` shows them " +
		"with their blockers. Filters: " + filters + "."
	if take := d.takeVerb(); take != "" {
		s += " Once you pick one, " + code("gh dojo "+take+" N --show") + ": it takes the issue and prints its card."
	}
	return s
}

func (d *agentDoc) writing(w *mdWriter) {
	cfg := d.cfg
	// The types are the repository's labels, not the config's: the skill
	// names the prefix and leaves the list to new's error, which stays true.
	typ, types := "<label>", ""
	if p := cfg.Labels.TypePrefix; p != "" {
		typ = "<type>"
		types = " `--type bug` adds the label " + code(p+"bug") + "; the types are the repository's labels starting with " +
			code(p) + ", and an unknown one fails listing them."
	}
	status := ""
	if cfg.Statuses.Initial != "" {
		status = " in " + code(cfg.Statuses.Initial)
	}
	// new's other flags are named here, so that one of them does not cost
	// a read of new --help, the longest one.
	more := "`--label <label>`, `--assignee @me`, `--milestone <title>`, `--blocked-by N`, `--parent N`"
	if cfg.Labels.AreaPrefix != "" {
		more = "`--area <area>`, " + more
	}
	w.heading(1, "Creating and linking issues")
	items := []string{
		"`gh dojo new \"<title>\" --type " + typ + " --body \"…\"`: the issue, its labels, the board" + status +
			", in one command; a long body: `--body-file <file>`, or `--body-file -` for stdin. Also " + more +
			", each checked before anything is created." + types,
		"`gh dojo comment N \"text\"`: a comment without changing the status.",
		"`gh dojo block N --by M` / `gh dojo unblock N --by M`: add or remove a blocked-by link without changing " +
			"the status. An existing issue becomes a sub-issue with `gh issue edit N --parent M`.",
	}
	switch kw := keywordExample(cfg.Release.Keywords); {
	case !d.hasCommand("release"):
	case cfg.Release.Suffix:
		items = append(items, "A commit for an issue names it in parentheses at the end of its first line, "+
			"`fix: login limit (#12)`, several as `(#12, #13)`: that is how `gh dojo release` finds the issues "+
			"a release includes.")
	case kw != "":
		// Off by default, so an agent used to suffixes elsewhere writes
		// them here too (#93).
		items = append(items, "A commit pushed without a pull request names its issue after a keyword in its "+
			"message, as "+code(kw+" #12")+"; parentheses at the end of the first line, `(#12)`, do not count here: that is how `gh dojo release` "+
			"finds the issues a release includes.")
	}
	w.list(items)
}

// errors says what to do on an error, and that a repeat is safe, in a few
// sentences: the error's own fix: line covers the rest. JSON is left to
// --help: for everyday work the text is shorter.
func (d *agentDoc) errors(w *mdWriter) {
	w.heading(1, "Errors and repeats")
	w.para("On an error, do what its `fix:` line says. Exit code 2 is a process check refusing: never work " +
		"around it (do not move the status another way); ask the user. On 4 (network, rate limit or auth) and 5 " +
		"(partially applied), run the same command again, on 4 a bit later. A repeat is safe: what is already " +
		"in place (a status, the latest comment, a link) is skipped, and `new` with the same title finishes the " +
		"issue it created.")
}
