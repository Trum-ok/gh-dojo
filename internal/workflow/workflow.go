// Package workflow checks a verb against an issue and plans its effects
// without touching the network.
//
// The flow of a verb is: CheckArgs (no network, run before reading the
// issue), then Engine.Plan with the issue state a reader fetched, then an
// executor applies the Plan and prints Plan.Confirm. Plan.DryRun shows the
// plan instead of applying it.
//
// The plan has no IDs: an executor maps each Op to GraphQL with the board
// schema it caches, and finds the issue's own node IDs in Issue.Ref.
package workflow

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Issue is the state of an issue that a reader fetches before planning.
type Issue struct {
	Number int
	Title  string // shown in the dry run only
	Closed bool

	// OnBoard reports whether the issue is an item of the configured
	// project. Status is the item's status option name, "" when the item has
	// no status; it is ignored when OnBoard is false.
	OnBoard bool
	Status  string

	// Comments are the issue's latest comments, oldest first, bodies
	// verbatim (GraphQL comments(last: N) returns them in this order). They
	// are only searched for gh-dojo markers (see LastMarker): the most recent
	// marker of a verb must be in the window for a repeated verb not to post
	// its comment twice. A window of the last 20 comments is enough for an
	// immediate retry; an older marker only means the comment is posted again.
	Comments []Comment

	// Ref is opaque to this package: the reader may keep the node IDs the
	// executor needs here. It is never printed.
	Ref any
}

// Comment is an issue comment.
type Comment struct {
	Body string
}

// Args are the verb arguments from the command line.
type Args struct {
	Comment    string
	HasComment bool // --comment was given, even if blank
	By         string
	HasBy      bool // --by was given, even if blank
}

func (a Args) given(arg string) bool {
	switch arg {
	case config.ArgComment:
		return a.HasComment
	case config.ArgBy:
		return a.HasBy
	}
	return false
}

func (a Args) value(arg string) string {
	switch arg {
	case config.ArgComment:
		return strings.TrimSpace(a.Comment)
	case config.ArgBy:
		return strings.TrimSpace(a.By)
	}
	return ""
}

// Engine plans verbs with the rules of one config.
type Engine struct {
	// Verbs are all verbs of the config. Only used to suggest a verb that
	// leads to an allowed status when a from check fails.
	Verbs []config.Verb
	// Initial is the status assumed for an issue that is not on the board or
	// has no status (statuses.initial); may be "".
	Initial string
	// Final are the final statuses (statuses.final). A verb whose target is
	// one of them may run on a closed issue: GitHub closes an issue when a
	// PR that fixes it is merged, and the issue still has to reach Done.
	Final []string
	// Session fills {session} in comment templates.
	Session config.Session
	// Getenv reads the session variables; os.Getenv when nil.
	Getenv func(string) string
}

// New returns an Engine with the rules of c.
func New(c *config.Config) *Engine {
	return &Engine{Verbs: c.Verbs, Initial: c.Statuses.Initial, Final: c.Statuses.Final, Session: c.Session}
}

// byNumberRE matches an issue reference given to --by: "12" or "#12".
var byNumberRE = regexp.MustCompile(`^#?([1-9][0-9]*)$`)

// NormalizeBy turns an issue number given to --by ("12" or "#12") into "#12",
// which GitHub links; anything else is free text and only trimmed.
func NormalizeBy(s string) string {
	s = strings.TrimSpace(s)
	if m := byNumberRE.FindStringSubmatch(s); m != nil {
		return "#" + m[1]
	}
	return s
}

// CheckArgs validates the arguments of verb v for issue number (used in the
// example command of the error). It needs no issue state, so callers run it
// before reading the issue. Errors have exit code 1:
//
//   - --comment or --by given while the verb's comment template does not use it;
//   - an argument of require, or all of require_one_of, missing or blank.
func CheckArgs(v config.Verb, number int, a Args) error {
	var accepted []string
	for _, arg := range config.Args() {
		if v.Uses(arg) {
			accepted = append(accepted, flag(arg))
		}
	}
	for _, arg := range config.Args() {
		if !a.given(arg) || v.Uses(arg) {
			continue
		}
		if len(accepted) == 0 {
			return render.Usage("%s does not use %s; it takes no arguments besides the issue number", v.Name, flag(arg)).
				WithFix(Example(v, number))
		}
		return render.Usage("%s does not use %s", v.Name, flag(arg)).
			WithAllowed(accepted...).WithFix(Example(v, number))
	}

	var missing []string
	blank := false
	for _, arg := range v.Require {
		if a.value(arg) == "" {
			missing = append(missing, flag(arg))
			blank = blank || a.given(arg)
		}
	}
	var what string
	switch {
	case len(missing) > 0:
		what = fmt.Sprintf("%s needs %s", v.Name, join(missing, "and"))
	case len(v.RequireOneOf) > 0 && !slices.ContainsFunc(v.RequireOneOf, func(arg string) bool { return a.value(arg) != "" }):
		names := make([]string, len(v.RequireOneOf))
		for i, arg := range v.RequireOneOf {
			names[i] = flag(arg)
			blank = blank || a.given(arg)
		}
		if len(names) == 1 {
			what = fmt.Sprintf("%s needs %s", v.Name, names[0])
		} else {
			what = fmt.Sprintf("%s needs at least one of %s", v.Name, join(names, "or"))
		}
	default:
		return nil
	}
	if blank {
		what += " (blank values do not count)"
	}
	return render.Usage("%s", what).WithFix(Example(v, number))
}

// Example returns a command that runs v on issue number with placeholders
// for the arguments it requires: all of require, the first of
// require_one_of.
//
//	gh dojo wait 42 --by "<#N or name>"
func Example(v config.Verb, number int) string {
	parts := []string{"gh dojo", v.Name, strconv.Itoa(number)}
	args := slices.Clone(v.Require)
	if len(v.RequireOneOf) > 0 {
		args = append(args, v.RequireOneOf[0])
	}
	for _, arg := range args {
		parts = append(parts, flag(arg), placeholder(arg))
	}
	return strings.Join(parts, " ")
}

func placeholder(arg string) string {
	if arg == config.ArgBy {
		return `"<#N or name>"`
	}
	return `"<text>"`
}

func flag(arg string) string { return "--" + arg }

// join lists items as "a", "a or b", "a, b or c".
func join(items []string, conj string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + conj + " " + items[len(items)-1]
}

// Plan runs the checks of verb v against issue is and returns the effects to
// apply. In order:
//
//  1. CheckArgs (exit code 1);
//  2. a closed issue is refused (exit code 2), unless the verb's target
//     status is final (Engine.Final);
//  3. an issue already in the target status gets no status change, and the
//     from check is skipped, so a repeat is safe;
//  4. otherwise the current status must be in from (exit code 2). An issue
//     not on the board or without a status counts as Engine.Initial.
//
// The plan adds the issue to the board when it is not there, sets the
// target status, and posts the rendered comment unless it renders empty or
// the same comment is already the verb's latest (see AlreadyPosted).
func (e *Engine) Plan(v config.Verb, is Issue, a Args) (*Plan, error) {
	if err := CheckArgs(v, is.Number, a); err != nil {
		return nil, err
	}
	if !is.OnBoard {
		is.Status = ""
	}
	if is.Closed && !e.runsOnClosed(v) {
		return nil, render.Denied("#%d is closed; %s works only on open issues\n"+
			"this is a process rule: ask the user what to do with #%d", is.Number, v.Name, is.Number)
	}
	p := &Plan{Verb: v.Name, Issue: is, Target: v.To}
	atTarget := v.To != "" && is.Status == v.To
	if !atTarget {
		if err := e.checkFrom(v, is); err != nil {
			return nil, err
		}
	}
	if !is.OnBoard {
		p.Ops = append(p.Ops, AddToBoard{})
	}
	if v.To != "" {
		if atTarget {
			p.StatusUnchanged = true
		} else {
			p.Ops = append(p.Ops, SetStatus{From: is.Status, To: v.To})
		}
	}

	vars := map[string]string{
		config.VarComment: a.value(config.ArgComment),
		config.VarBy:      NormalizeBy(a.By),
	}
	if v.Uses(config.VarSession) {
		link, ok := e.Session.Resolve(e.Getenv)
		vars[config.VarSession] = link
		if !ok && e.Session.Configured() {
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"no agent session: %s is not set, so the comment has no session link",
				join(e.Session.Env, "or")))
		}
	}
	if body := config.Render(v.Comment, vars); body != "" {
		marker := Marker(v.Name, body)
		if AlreadyPosted(is.Comments, v.Name, marker) {
			p.CommentPresent = true
		} else {
			p.Ops = append(p.Ops, AddComment{Body: body, Marker: marker})
		}
	}
	return p, nil
}

// checkFrom refuses an issue whose status is not in v.From.
func (e *Engine) checkFrom(v config.Verb, is Issue) error {
	current := is.Status
	if current == "" {
		current = e.Initial
	}
	if len(v.From) == 0 || slices.Contains(v.From, current) {
		return nil
	}
	var what string
	switch {
	case current == "":
		what = fmt.Sprintf("%s cannot move #%d: it has no status", v.Name, is.Number)
	case !is.OnBoard:
		what = fmt.Sprintf("%s cannot move #%d from %s: it is not on the board, which counts as %s", v.Name, is.Number, current, current)
	case is.Status == "":
		what = fmt.Sprintf("%s cannot move #%d from %s: it has no status, which counts as %s", v.Name, is.Number, current, current)
	default:
		what = fmt.Sprintf("%s cannot move #%d from %s", v.Name, is.Number, current)
	}
	err := render.Denied("%s", what).WithAllowed(v.From...)
	if u, ok := e.leadsTo(v, current, is.Closed); ok {
		return err.WithFix(Example(u, is.Number))
	}
	err.What += "\nthis is a process rule: ask the user how to proceed"
	return err
}

// runsOnClosed reports whether v may run on a closed issue: its target
// status is final.
func (e *Engine) runsOnClosed(v config.Verb) bool {
	return v.To != "" && slices.Contains(e.Final, v.To)
}

// leadsTo finds the first other verb that moves an issue from current to a
// status v starts from, and that may run on it if it is closed.
func (e *Engine) leadsTo(v config.Verb, current string, closed bool) (config.Verb, bool) {
	if current == "" {
		return config.Verb{}, false
	}
	for _, u := range e.Verbs {
		if u.Name == v.Name || u.To == "" || u.To == current || !slices.Contains(v.From, u.To) {
			continue
		}
		if closed && !e.runsOnClosed(u) {
			continue
		}
		if len(u.From) == 0 || slices.Contains(u.From, current) {
			return u, true
		}
	}
	return config.Verb{}, false
}
