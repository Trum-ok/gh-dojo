// Package workflow checks a verb against an issue and plans its effects
// without touching the network.
//
// The flow of a verb is: CheckArgs (no network, run before reading the
// issue), then Engine.Plan with the issue state a reader fetched, then
// Plan.Unconfirmed (a verb with confirm = "user" without --yes), then an
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
	// StateReason is why a closed issue was closed, as a config close
	// reason (config.CloseCompleted, ...); "" when open or unknown.
	StateReason string

	// Labels and Assignees are the issue's label names and assignee
	// logins, for the labels and assign effects. They may be windows of
	// the full lists (20 labels and 10 assignees in a card): see
	// planLabels for what a label beyond the window does.
	Labels    []string
	Assignees []string
	// Viewer is the login of the authenticated user, which assign = "@me"
	// (config.Me) stands for; the reader fills it for such a verb.
	Viewer string

	// OnBoard reports whether the issue is an item of the configured
	// project. Status is the item's status option name, "" when the item has
	// no status; it is ignored when OnBoard is false.
	OnBoard bool
	Status  string
	// Archived reports that the board item is archived: hidden from the
	// board's views. A verb unarchives it (see Unarchive). Ignored when
	// OnBoard is false.
	Archived bool

	// Comments are the issue's latest comments, oldest first (GraphQL
	// comments(last: N) returns them in this order), each with its body
	// verbatim and ViewerDidAuthor. They are only searched for gh-dojo
	// markers (see LastMarker): the most recent marker must be in the window
	// for a retried verb not to post its comment twice. The last 20 comments
	// are enough for an immediate retry; a marker outside the window only
	// means the comment is posted again.
	Comments []Comment

	// Ref is opaque to this package: the reader may keep the node IDs the
	// executor needs here. It is never printed.
	Ref any
}

// Comment is an issue comment.
type Comment struct {
	Body string
	// ViewerDidAuthor is GraphQL IssueComment.viewerDidAuthor: the comment
	// was written by the authenticated user. Markers in other users'
	// comments are ignored, so nobody else can suppress a verb's comment.
	ViewerDidAuthor bool
}

// Args are the verb arguments from the command line.
type Args struct {
	Comment    string
	HasComment bool // --comment was given, even if blank
	By         string
	HasBy      bool // --by was given, even if blank
	// Yes is --yes: the user agreed to a verb with confirm = "user".
	Yes bool
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
	return &Engine{Initial: c.Statuses.Initial, Final: c.Statuses.Final, Session: c.Session}
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
//   - --yes given to a verb without confirm = "user";
//   - an argument of require, or all of require_one_of, missing or blank.
//
// A missing --yes is not an argument error: Plan sets Plan.NeedsYes.
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
	if a.Yes && !v.Confirm {
		return render.Usage("%s does not take --yes: it needs no confirmation", v.Name).WithFix(Example(v, number))
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
//     status is final (Engine.Final), or the verb closes or reopens it;
//  3. an issue already in the target status gets no status change, and the
//     from check is skipped, so a repeat is safe;
//  4. otherwise the current status must be in from (exit code 2). An issue
//     not on the board or without a status counts as Engine.Initial.
//
// The plan adds the issue to the board when it is not there, reopens it,
// sets the target status, adds and removes labels, assigns, posts the
// rendered comment unless it renders empty or the same comment is already
// the verb's latest (see AlreadyPosted), and closes it. Each effect
// already in place is left out of Ops (StatusUnchanged, CommentPresent,
// InPlace): a closed issue is not closed again, whatever its reason. An
// archived item is unarchived only when the verb moves it to a status that
// is not final: the issue is live again. A repeat that changes nothing, a
// comment alone, or a move to a final status leaves it archived.
//
// A verb with confirm = "user" run without --yes gets NeedsYes when the
// plan is not empty: a repeat with nothing left to do needs no go-ahead.
// Call Plan.Unconfirmed before applying.
func (e *Engine) Plan(v config.Verb, is Issue, a Args) (*Plan, error) {
	if err := CheckArgs(v, is.Number, a); err != nil {
		return nil, err
	}
	if !is.OnBoard {
		is.Status = ""
		is.Archived = false
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
	moves := v.To != "" && !atTarget
	switch {
	case !is.OnBoard:
		p.Ops = append(p.Ops, AddToBoard{})
	case is.Archived && moves && !slices.Contains(e.Final, v.To):
		p.Ops = append(p.Ops, Unarchive{})
	}
	if v.Reopen {
		if is.Closed {
			p.Ops = append(p.Ops, Reopen{})
		} else {
			p.InPlace = append(p.InPlace, Reopen{})
		}
	}
	if v.To != "" {
		if atTarget {
			p.StatusUnchanged = true
		} else {
			p.Ops = append(p.Ops, SetStatus{From: is.Status, To: v.To})
		}
	}
	p.planLabels(v)
	if v.Assign != "" {
		login := v.Assign
		if login == config.Me {
			if login = is.Viewer; login == "" {
				return nil, render.Internal("%s assigns %s, but the authenticated user was not read", v.Name, config.Me)
			}
		}
		switch {
		case slices.ContainsFunc(is.Assignees, func(s string) bool { return strings.EqualFold(s, login) }):
			p.InPlace = append(p.InPlace, Assign{Login: login})
		default:
			// Not among the assignees read: assign, even if the list was
			// truncated; assigning someone twice changes nothing.
			p.Ops = append(p.Ops, Assign{Login: login})
		}
	}

	vars := map[string]string{
		config.VarComment: a.value(config.ArgComment),
		config.VarBy:      NormalizeBy(a.By),
	}
	noSession := false
	if v.Uses(config.VarSession) {
		link, ok := e.Session.Resolve(e.Getenv)
		vars[config.VarSession] = link
		noSession = !ok && e.Session.Configured()
	}
	body := config.Render(v.Comment, vars)
	switch {
	case body == "":
		// The template rendered empty: without a session it said nothing.
		if noSession {
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"no agent session: %s is not set, so no comment is left",
				join(e.Session.Env, "or")))
		}
	case AlreadyPosted(is.Comments, Marker(v.Name, body)):
		p.CommentPresent = true
	default:
		p.Ops = append(p.Ops, AddComment{Body: body, Marker: Marker(v.Name, body)})
		if noSession {
			p.Warnings = append(p.Warnings, fmt.Sprintf(
				"no agent session: %s is not set, so the comment has no session link",
				join(e.Session.Env, "or")))
		}
	}
	if v.Close != "" {
		if is.Closed {
			p.InPlace = append(p.InPlace, Close{Reason: is.StateReason})
		} else {
			p.Ops = append(p.Ops, Close{Reason: v.Close})
		}
	}
	p.NeedsYes = v.Confirm && !a.Yes && !p.Empty()
	return p, nil
}

// planLabels adds the label ops of v: labels to add that the issue lacks,
// labels to remove that it has. The rest are in place. Names compare
// without case, as GitHub's do. When the issue has more labels than were
// read, a label to add that was not seen is added (adding it twice
// changes nothing), and one to remove that was not seen counts as absent.
func (p *Plan) planLabels(v config.Verb) {
	has := func(name string) bool {
		return slices.ContainsFunc(p.Issue.Labels, func(l string) bool { return strings.EqualFold(l, name) })
	}
	var add, addInPlace, remove, removeInPlace []string
	for _, name := range v.AddLabels {
		if has(name) {
			addInPlace = append(addInPlace, name)
		} else {
			add = append(add, name)
		}
	}
	for _, name := range v.RemoveLabels {
		if has(name) {
			remove = append(remove, name)
		} else {
			removeInPlace = append(removeInPlace, name)
		}
	}
	if len(add) > 0 {
		p.Ops = append(p.Ops, AddLabels{Names: add})
	}
	if len(remove) > 0 {
		p.Ops = append(p.Ops, RemoveLabels{Names: remove})
	}
	if len(addInPlace) > 0 {
		p.InPlace = append(p.InPlace, AddLabels{Names: addInPlace})
	}
	if len(removeInPlace) > 0 {
		p.InPlace = append(p.InPlace, RemoveLabels{Names: removeInPlace})
	}
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
	// The same words as the dry run and confirmation line use.
	var from string
	switch {
	case !is.OnBoard:
		from = ": " + notOnBoard
	case is.Status == "":
		from = ": " + noStatus
	default:
		from = " from " + is.Status
	}
	if is.Status == "" && current != "" {
		from += ", which counts as " + current
	}
	// No fix line and no allowed line: a refusal ends with asking the user,
	// never with a command that works around the process.
	return render.Denied("%s cannot move #%d%s; it starts only from %s\n"+
		"this is a process rule: ask the user how to proceed",
		v.Name, is.Number, from, join(v.From, "or"))
}

// runsOnClosed reports whether v may run on a closed issue: its target
// status is final, or it closes (a repeat is safe) or reopens the issue.
func (e *Engine) runsOnClosed(v config.Verb) bool {
	return v.To != "" && slices.Contains(e.Final, v.To) || v.Close != "" || v.Reopen
}
