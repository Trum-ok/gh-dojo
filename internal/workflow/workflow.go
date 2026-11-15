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
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Issue is the state of an issue that a reader fetches before planning.
type Issue struct {
	Number int
	// Display names the issue in every message and line of the plan:
	// "owner/repo#42" for an issue outside the default repository; ""
	// reads "#42" (see Name).
	Display string
	// DefaultRepo is the default repository, "owner/repo", for an issue
	// outside it: a bare number given to --by names an issue there, so the
	// comment names it as "owner/repo#12" (GitHub would read "#12" in the
	// issue's own repository). "" for an issue in the default repository.
	DefaultRepo string
	Title       string // shown in the dry run only
	Closed      bool
	// StateReason is why a closed issue was closed, as a config close
	// reason (config.CloseCompleted, ...); "" when open or unknown.
	StateReason string

	// Labels and Assignees are the issue's label names and assignee
	// logins, for the labels, assign and unassign effects and the
	// from_labels and refuse_labels checks. Labels may be a window of the
	// full list (a card reads 20 labels; 100, GitHub's limit per issue, for
	// a verb that removes or checks labels); Assignees has them all: a card reads 10, GitHub's
	// limit per issue. LabelsTruncated reports that the issue has labels
	// beyond Labels: see planLabels for what that does.
	Labels          []string
	LabelsTruncated bool
	Assignees       []string
	// MissingLabels are label names of the verb that do not exist in the
	// repository. A label to remove among them is in place: the issue
	// cannot have it. The reader refuses a label to add that is missing.
	MissingLabels []string
	// Viewer is the login of the authenticated user, which "@me"
	// (config.Me) stands for; the reader fills it for a verb that assigns
	// or unassigns "@me", in the config or in --assignee.
	Viewer string
	// AssignLogin is the login the verb assigns (from the config or
	// --assignee), as GitHub spells it; the reader fills it for a verb that
	// assigns a login. "" uses the spelling given.
	AssignLogin string

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

	// Sprint is the board's sprint field (fields.sprint) with the item's
	// iteration, for the iteration effect; nil when the config has none.
	Sprint *Sprint
	// History is the issue's status changes on the board, oldest first,
	// for iteration = "by_status_date:<status>". HistoryTruncated says
	// older changes may exist beyond the window read; HistoryUntracked
	// that GitHub keeps no history for the status field (it does only for
	// a board's built-in Status field). Changes before AddedAt belong to
	// an earlier item of the issue, since removed, and are ignored.
	History          []StatusChange
	HistoryTruncated bool
	HistoryUntracked bool
	// AddedAt is when the issue was added to the board (the item's
	// createdAt); zero when unknown or not on the board.
	AddedAt time.Time
	// Fields are the item's values of the board fields the verb's field
	// effect sets, by field name, as the board shows them (an option name,
	// a text, a number, a date); a field without a value, or whose value
	// was not read, is absent and gets set.
	Fields map[string]string
	// ReadAt is when the state was read. Its date in the board's time
	// zone (Engine.Location) is "today" for iteration = "current" and for
	// a verb that enters the status of by_status_date itself. Zero means
	// now.
	ReadAt time.Time

	// Unresolved are the issue's blockers that still block: open and not
	// in a resolved status (config statuses.resolved), or with a status
	// that could not be read. UnreadBlockers counts the blockers that were
	// not read at all (beyond the window, or withheld from the token); they
	// count as unresolved too. Only a verb with blockers = "warn" or
	// "deny" looks at them.
	Unresolved     []Blocker
	UnreadBlockers int
	// UnresolvedSubIssues are the issue's sub-issues whose work is not
	// done, by the rule of Unresolved; UnreadSubIssues counts those not
	// read, which count as unresolved too. Only a verb with sub_issues =
	// "warn" or "deny" looks at them.
	UnresolvedSubIssues []Blocker
	UnreadSubIssues     int

	// ByIssues are the issues --by names, in order, read for a verb with
	// blockers = "link" when --by is an issue reference or a
	// comma-separated list of them; empty otherwise, and then --by is only
	// text for the comment.
	ByIssues []ByIssue

	// Ref is opaque to this package: the reader may keep the node IDs the
	// executor needs here. It is never printed.
	Ref any
}

// Name is how messages name the issue: Display, else "#42".
func (is Issue) Name() string {
	if is.Display != "" {
		return is.Display
	}
	return "#" + strconv.Itoa(is.Number)
}

// arg is the issue as a command-line argument: "42", or "owner/repo#42".
func (is Issue) arg() string { return strings.TrimPrefix(is.Name(), "#") }

// Sprint is the board's sprint field and the item's value of it.
type Sprint struct {
	Field string // the board name of the iteration field
	// Iterations are the field's iterations. A completed one still covers
	// its days: an issue that entered Ready in a past sprint gets that
	// sprint.
	Iterations []board.Iteration
	// Current is the title of the item's iteration and CurrentID its ID;
	// both "" when the item has none.
	Current, CurrentID string
}

// StatusChange is one status change of the issue on the board.
type StatusChange struct {
	At time.Time
	To string // "" when the status was cleared
}

// Blocker is an unresolved blocker or sub-issue of an issue, as the
// blockers and sub_issues checks name it.
type Blocker struct {
	Ref   string // "#12", or "owner/repo#12" outside the default repository
	Place string // where it stands: "In progress", "not on the board", "status not read: ..."
}

func (b Blocker) String() string { return b.Ref + " (" + b.Place + ")" }

// ByIssue is an issue --by names for the blockers = "link" effect.
type ByIssue struct {
	Ref string // "#12", or "owner/repo#12" outside the default repository
	// Mention is how the comment names it, as GitHub links it from the
	// issue: "#12" in the issue's own repository, else "owner/repo#12";
	// "" reads Ref.
	Mention string
	// Blocks reports that it blocks the issue already: the link is in
	// place.
	Blocks bool
	// PullRequest says the number is a pull request: it is named in the
	// comment, but only issues can block, so it is not linked (a warning).
	PullRequest bool
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
	// Assignee is --assignee, the user a verb with assign = "{assignee}"
	// assigns: config.Me or a login.
	Assignee    string
	HasAssignee bool // --assignee was given, even if blank
	// Yes is --yes: the user agreed to a verb with confirm = "user".
	Yes bool
}

func (a Args) given(arg string) bool {
	switch arg {
	case config.ArgComment:
		return a.HasComment
	case config.ArgBy:
		return a.HasBy
	case config.ArgAssignee:
		return a.HasAssignee
	}
	return false
}

func (a Args) value(arg string) string {
	switch arg {
	case config.ArgComment:
		return strings.TrimSpace(a.Comment)
	case config.ArgBy:
		return strings.TrimSpace(a.By)
	case config.ArgAssignee:
		return strings.TrimSpace(a.Assignee)
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
	// Location is the board's time zone (board.timezone): the sprint
	// effect's days are dates in it. UTC when nil.
	Location *time.Location
	// Verbs are the config's verbs: a refuse_labels refusal names the
	// verbs that remove the label. May be nil.
	Verbs []config.Verb
}

// New returns an Engine with the rules of c.
func New(c *config.Config) *Engine {
	return &Engine{Initial: c.Statuses.Initial, Final: c.Statuses.Final, Session: c.Session,
		Location: c.Board.Location, Verbs: c.Verbs}
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

// CheckArgs validates the arguments of verb v for the issues, given as
// command-line arguments ("42", "owner/repo#42"), all of them in the
// example command of the error. It needs no issue state,
// so callers run it before reading the issue. Errors have exit code 1:
//
//   - --comment or --by given while the verb's comment template does not use
//     it, --assignee given to a verb without assign = "{assignee}";
//   - --yes given to a verb without confirm = "user";
//   - an argument of require, or all of require_one_of, missing or blank;
//   - --assignee that is neither "@me" nor a GitHub login. Whether the user
//     can be assigned is the reader's to check.
//
// A missing --yes is not an argument error: Plan sets Plan.NeedsYes.
func CheckArgs(v config.Verb, a Args, issues ...string) error {
	var accepted []string
	for _, arg := range config.Args() {
		if v.Takes(arg) {
			accepted = append(accepted, flag(arg))
		}
	}
	for _, arg := range config.Args() {
		if !a.given(arg) || v.Takes(arg) {
			continue
		}
		if len(accepted) == 0 {
			return render.Usage("%s does not use %s; it takes no arguments besides the issue number", v.Name, flag(arg)).
				WithFix(Example(v, issues...))
		}
		return render.Usage("%s does not use %s", v.Name, flag(arg)).
			WithAllowed(accepted...).WithFix(Example(v, issues...))
	}
	if a.Yes && !v.Confirm {
		return render.Usage("%s does not take --yes: it needs no confirmation", v.Name).WithFix(Example(v, issues...))
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
		return checkAssignee(v, a, issues)
	}
	if blank {
		what += " (blank values do not count)"
	}
	return render.Usage("%s", what).WithFix(Example(v, issues...))
}

// checkAssignee refuses an --assignee that is neither config.Me nor a
// GitHub login, as the config refuses one in assign.
func checkAssignee(v config.Verb, a Args, issues []string) error {
	login := a.value(config.ArgAssignee)
	switch {
	case !v.AssignArg || login == "" || login == config.Me || config.IsLogin(login):
		return nil
	case strings.HasPrefix(login, "@"):
		return render.Usage("--%s %q: write the login without @, or %s for yourself", config.ArgAssignee, login, config.Me).
			WithFix(Example(v, issues...))
	}
	return render.Usage("--%s %q is not a GitHub login; pass a login, or %s for yourself", config.ArgAssignee, login, config.Me).
		WithFix(Example(v, issues...))
}

// Example returns a command that runs v on the issues, given as
// command-line arguments ("42", "owner/repo#42"), with placeholders for
// the arguments it requires: all of require, the first of require_one_of.
//
//	gh dojo wait 42 --by "<#N or name>"
func Example(v config.Verb, issues ...string) string {
	parts := append([]string{"gh dojo", v.Name}, issues...)
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
	switch arg {
	case config.ArgBy:
		return `"<#N or name>"`
	case config.ArgAssignee:
		return `"<login>"`
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
//     status is final (Engine.Final), or the verb closes or reopens it; a
//     verb without a target that closes runs on a closed issue only as a
//     repeat (see closedRepeat), and refuses one closed otherwise (exit
//     code 2);
//  3. an issue already in the target status gets no status change, and the
//     from check is skipped, so a repeat is safe; so is a repeat of a verb
//     without a target that closes the issue;
//  4. otherwise the current status must be in from (exit code 2). An issue
//     not on the board or without a status counts as Engine.Initial;
//  5. then, for a verb with from_labels or refuse_labels, an issue without
//     one of from_labels, or with one of refuse_labels, is refused (exit
//     code 2; see checkLabels). Like from, the check is skipped for an
//     issue already in the target status;
//  6. then, for a verb with blockers = "warn" or "deny", the issue's
//     unresolved blockers (Issue.Unresolved, Issue.UnreadBlockers) add a
//     warning or refuse it (exit code 2), skipped the same way;
//  7. then, for a verb with sub_issues = "warn" or "deny", the issue's
//     unresolved sub-issues (Issue.UnresolvedSubIssues,
//     Issue.UnreadSubIssues) do the same.
//
// Moving an issue closed as not planned or duplicate to a final status is
// allowed with a warning: the verb does not change why it was closed.
//
// The plan adds the issue to the board when it is not there (unless the
// verb's only board effect clears the sprint, which such an issue has
// none of), reopens it,
// sets the target status, sets the sprint (see planIteration; a day
// without an iteration is exit code 1) and the fields of the field effect,
// adds and removes labels, assigns and unassigns (see planAssign), marks
// the issue blocked by each of Issue.ByIssues (blockers = "link"; a pull
// request among them is only named in the comment, with a warning), posts
// the rendered comment unless it renders empty or the same comment is already
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
	if err := CheckArgs(v, a, is.arg()); err != nil {
		return nil, err
	}
	if !is.OnBoard {
		is.Status = ""
		is.Archived = false
	}
	if is.Closed && !e.runsOnClosed(v) {
		return nil, render.Denied("%s is closed; %s works only on open issues\n"+
			"this is a process rule: ask the user what to do with %s", is.Name(), v.Name, is.Name())
	}
	if v.To == "" && v.Close != "" && is.Closed && !closedRepeat(v, is) {
		return nil, render.Denied("%s is %s; %s closes as %s\n"+
			"this is a process rule: ask the user what to do with %s",
			is.Name(), closedText(is), v.Name, ReasonText(v.Close), is.Name())
	}
	p := &Plan{Verb: v.Name, Issue: is, Target: v.To}
	atTarget := v.To != "" && is.Status == v.To
	if !atTarget && !closedRepeat(v, is) {
		if err := e.checkFrom(v, is); err != nil {
			return nil, err
		}
	}
	if !atTarget && v.ChecksLabels() {
		if err := e.checkLabels(v, is); err != nil {
			return nil, err
		}
	}
	if !atTarget && v.ChecksBlockers() {
		if err := p.checkBlockers(v); err != nil {
			return nil, err
		}
	}
	if !atTarget && v.ChecksSubIssues() {
		if err := p.checkSubIssues(v); err != nil {
			return nil, err
		}
	}
	moves := v.To != "" && !atTarget
	if is.Closed && moves && slices.Contains(e.Final, v.To) &&
		(is.StateReason == config.CloseNotPlanned || is.StateReason == config.CloseDuplicate) {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s is closed as %s; moving to %s anyway",
			is.Name(), ReasonText(is.StateReason), v.To))
	}
	switch {
	case !is.OnBoard && !v.OnlyClearsSprint():
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
	if err := p.planIteration(v, atTarget, e.Location); err != nil {
		return nil, err
	}
	p.planFields(v)
	p.planLabels(v)
	assigned, err := p.planAssign(v, a)
	if err != nil {
		return nil, err
	}

	by := NormalizeBy(a.By)
	if is.DefaultRepo != "" && byNumberRE.MatchString(strings.TrimSpace(a.By)) {
		by = is.DefaultRepo + by // "#12" would name an issue of the issue's own repository
	}
	if v.Blockers == config.BlockersLink && len(is.ByIssues) > 0 {
		refs := make([]string, len(is.ByIssues))
		for i, b := range is.ByIssues {
			refs[i] = b.Ref
			if b.Mention != "" {
				refs[i] = b.Mention
			}
			switch {
			case b.PullRequest:
				p.Warnings = append(p.Warnings, b.Ref+" is a pull request: not linked")
			case b.Blocks:
				p.InPlace = append(p.InPlace, AddBlockedBy{Ref: b.Ref})
			default:
				p.Ops = append(p.Ops, AddBlockedBy{Ref: b.Ref})
			}
		}
		by = strings.Join(refs, ", ") // as GitHub links them: "#25, #26"
	}

	vars := map[string]string{
		config.VarComment:  a.value(config.ArgComment),
		config.VarBy:       by,
		config.VarAssignee: assigned,
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

// planAssign adds the assign and unassign ops of v and returns the login
// it assigns, "" for none. The user to assign is the config's, or
// --assignee for assign = "{assignee}"; "@me" (config.Me) is the viewer.
// One already among the assignees is in place. unassign removes the user
// it names (in place when not assigned) or, with "all", every assignee
// but the one the verb assigns: the verb never removes whom it assigns
// (a user it names is then in place, Kept). Unassign comes before Assign
// in Ops.
// GitHub allows 10 assignees per issue and the reader reads as many, so
// Issue.Assignees are all of them.
func (p *Plan) planAssign(v config.Verb, a Args) (string, error) {
	is := p.Issue
	viewer := func(does string) (string, error) {
		if is.Viewer == "" {
			return "", render.Internal("%s %s %s, but the authenticated user was not read", v.Name, does, config.Me)
		}
		return is.Viewer, nil
	}
	// assignee returns the issue's assignee login, as GitHub spells it.
	assignee := func(login string) (string, bool) {
		i := slices.IndexFunc(is.Assignees, func(s string) bool { return strings.EqualFold(s, login) })
		if i < 0 {
			return login, false
		}
		return is.Assignees[i], true
	}
	login := v.Assign
	if v.AssignArg {
		login = a.value(config.ArgAssignee)
	}
	switch {
	case login == config.Me:
		var err error
		if login, err = viewer("assigns"); err != nil {
			return "", err
		}
	case login != "" && is.AssignLogin != "":
		login = is.AssignLogin // as GitHub spells it
	}
	keep := func(s string) bool { return login != "" && strings.EqualFold(s, login) }
	switch u := v.Unassign; u {
	case "":
	case config.UnassignAll:
		var remove []string
		for _, s := range is.Assignees {
			if !keep(s) {
				remove = append(remove, s)
			}
		}
		if len(remove) > 0 {
			p.Ops = append(p.Ops, Unassign{Logins: remove, All: true})
		} else {
			p.InPlace = append(p.InPlace, Unassign{All: true})
		}
	default:
		if u == config.Me {
			var err error
			if u, err = viewer("unassigns"); err != nil {
				return "", err
			}
		}
		switch spelled, ok := assignee(u); {
		case keep(u):
			// The verb assigns this user too (--assignee, or "@me" and the
			// same person's login): assigning wins, and the line says so.
			p.InPlace = append(p.InPlace, Unassign{Logins: []string{login}, Kept: true})
		case ok:
			p.Ops = append(p.Ops, Unassign{Logins: []string{spelled}})
		default:
			p.InPlace = append(p.InPlace, Unassign{Logins: []string{u}})
		}
	}
	// After the unassign op: GitHub allows 10 assignees, so a full issue
	// needs the room before the user is added.
	if login != "" {
		if _, ok := assignee(login); ok {
			p.InPlace = append(p.InPlace, Assign{Login: login})
		} else {
			p.Ops = append(p.Ops, Assign{Login: login})
		}
	}
	return login, nil
}

// planLabels adds the label ops of v: labels to add that the issue lacks,
// labels to remove that it has. The rest are in place. Names compare
// exactly: the reader looked each one up as the repository spells it, and
// GitHub's names are unique regardless of case. A label to remove that
// does not exist in the repository (Issue.MissingLabels) is in place.
// When the issue has more labels than were read (Issue.LabelsTruncated),
// a label that was not seen is added or removed all the same: doing
// either twice changes nothing, and "unchanged" would be a guess.
func (p *Plan) planLabels(v config.Verb) {
	has := func(name string) bool { return slices.Contains(p.Issue.Labels, name) }
	var add, addInPlace, remove, removeInPlace []string
	for _, name := range v.AddLabels {
		if has(name) {
			addInPlace = append(addInPlace, name)
		} else {
			add = append(add, name)
		}
	}
	for _, name := range v.RemoveLabels {
		switch {
		case slices.Contains(p.Issue.MissingLabels, name):
			removeInPlace = append(removeInPlace, name)
		case has(name), p.Issue.LabelsTruncated:
			remove = append(remove, name)
		default:
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

// checkLabels refuses, with exit code 2, an issue that lacks a label of
// v.FromLabels or has one of v.RefuseLabels. Names match regardless of
// case: GitHub's label names are unique regardless of case. The reader
// reads all of the issue's labels for such a verb (GitHub allows 100 per
// issue). A refusal names the labels; one because of refuse_labels also
// names the verbs that remove each (see removers). Like checkBlockers, it
// has no fix line: it ends with asking the user.
//
//	review cannot move #42 while it has the label "blocked"; resume removes it
//	this is a process rule: ask the user how to proceed
//
//	resume cannot run on #42 without the label "blocked"
//	this is a process rule: ask the user how to proceed
func (e *Engine) checkLabels(v config.Verb, is Issue) error {
	// spelled is the issue's label name, "" when it lacks the label.
	spelled := func(name string) string {
		if i := slices.IndexFunc(is.Labels, func(l string) bool { return strings.EqualFold(l, name) }); i >= 0 {
			return is.Labels[i]
		}
		return ""
	}
	action := "move"
	if v.To == "" {
		action = "run on"
	}
	var missing []string
	for _, name := range v.FromLabels {
		if spelled(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return render.Denied("%s cannot %s %s without %s\n"+
			"this is a process rule: ask the user how to proceed", v.Name, action, is.Name(), labelsText(missing, "and"))
	}
	var found []string
	for _, name := range v.RefuseLabels {
		if s := spelled(name); s != "" {
			found = append(found, s) // as the issue spells it
		}
	}
	if len(found) == 0 {
		return nil
	}
	var hints []string
	for _, name := range found {
		if verbs := e.removers(v, is, name); len(verbs) > 0 {
			what := "it"
			if len(found) > 1 {
				what = strconv.Quote(name)
			}
			hints = append(hints, join(verbs, "or")+" removes "+what)
		}
	}
	hint := ""
	if len(hints) > 0 {
		hint = "; " + strings.Join(hints, ", ")
	}
	return render.Denied("%s cannot %s %s while it has %s%s\n"+
		"this is a process rule: ask the user how to proceed", v.Name, action, is.Name(), labelsText(found, "and"), hint)
}

// labelsText names labels in a refusal: `the label "blocked"`, `the labels
// "blocked" and "wip"`.
func labelsText(names []string, conj string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	if len(names) == 1 {
		return "the label " + quoted[0]
	}
	return "the labels " + join(quoted, conj)
}

// removers are the verbs of the config, other than v, that remove the
// label name and can run on is in its status (their from, as checkFrom
// reads it), without closing it: the way the process takes the label off
// so that v can run.
func (e *Engine) removers(v config.Verb, is Issue, name string) []string {
	current := is.Status
	if current == "" {
		current = e.Initial
	}
	var out []string
	for _, o := range e.Verbs {
		removes := slices.ContainsFunc(o.RemoveLabels, func(l string) bool { return strings.EqualFold(l, name) })
		if o.Name == v.Name || !removes || o.Close != "" || len(o.From) > 0 && !slices.Contains(o.From, current) {
			continue
		}
		out = append(out, o.Name)
	}
	return out
}

// maxBlockers is how many unresolved blockers or sub-issues a warning or
// refusal names.
const maxBlockers = 3

// checkBlockers runs the blockers check of v (blockers = "warn" or
// "deny") on p.Issue: with unresolved or unread blockers it adds a warning,
// or refuses with exit code 2. A refusal has no fix line: it ends with
// asking the user, never with a command that works around the process.
//
//	start cannot move #42 while it has unresolved blockers: #12 (In progress), #15 (not on the board)
//	this is a process rule: ask the user how to proceed
func (p *Plan) checkBlockers(v config.Verb) error {
	return p.checkLinks(v, v.Blockers, "blockers", p.Issue.Unresolved, p.Issue.UnreadBlockers)
}

// checkSubIssues runs the sub_issues check of v ("warn" or "deny") on
// p.Issue as checkBlockers runs the blockers check:
//
//	done cannot move #42 while it has unresolved sub-issues: #43 (In progress), #44 (not on the board)
//	this is a process rule: ask the user how to proceed
func (p *Plan) checkSubIssues(v config.Verb) error {
	return p.checkLinks(v, v.SubIssues, "sub-issues", p.Issue.UnresolvedSubIssues, p.Issue.UnreadSubIssues)
}

// checkLinks warns about (mode "warn") or refuses (mode "deny") the
// unresolved linked issues of p.Issue, what they are (blockers,
// sub-issues), and those not read (unread).
func (p *Plan) checkLinks(v config.Verb, mode, what string, unresolved []Blocker, unread int) error {
	is := p.Issue
	if len(unresolved) == 0 && unread == 0 {
		return nil
	}
	var names []string
	for i, b := range unresolved {
		if i == maxBlockers {
			names = append(names, fmt.Sprintf("%d more", len(unresolved)-maxBlockers))
			break
		}
		names = append(names, b.String())
	}
	if unread > 0 {
		names = append(names, fmt.Sprintf("%d not read (counted as unresolved)", unread))
	}
	list := strings.Join(names, ", ")
	if mode == config.BlockersWarn {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s has unresolved %s: %s", is.Name(), what, list))
		return nil
	}
	action := "move"
	if v.To == "" {
		action = "run on"
	}
	return render.Denied("%s cannot %s %s while it has unresolved %s: %s\n"+
		"this is a process rule: ask the user how to proceed", v.Name, action, is.Name(), what, list)
}

// closedRepeat reports whether v, a verb without a target that closes the
// issue, runs again on an issue it closed: the issue is closed with the
// verb's reason, or, when the reason was not read, the latest gh-dojo
// comment is the verb's. Then the from check is skipped: the board's "Item
// closed" workflow may have moved the item since the verb closed it, so its
// status says nothing about where the verb started. An issue closed
// otherwise, say as completed by a merged pull request, is no repeat: Plan
// refuses it before the from check, whatever its status, so no comment
// lands on an issue the verb would not close.
//
// The marker is looked for in the viewer's own comments only (see
// LastMarker): with the reason not read, another user's repeat of a verb
// someone else ran is no repeat and is refused. That is accepted: GitHub
// reads the reason of nearly every closed issue, and the refusal asks the
// user.
func closedRepeat(v config.Verb, is Issue) bool {
	if v.To != "" || v.Close == "" || !is.Closed {
		return false
	}
	if is.StateReason != "" {
		return is.StateReason == v.Close
	}
	last, ok := LastMarker(is.Comments) // see Marker
	return ok && strings.HasPrefix(last, "<!-- gh-dojo:"+v.Name+":")
}

// closedText says how a closed issue is closed: "closed as completed", or
// "closed" when the reason was not read.
func closedText(is Issue) string {
	if is.StateReason == "" {
		return "closed"
	}
	return "closed as " + ReasonText(is.StateReason)
}

// checkFrom refuses an issue whose status is not in v.From. A verb without
// a target moves nothing, so its refusal says where it runs, as
// checkBlockers does:
//
//	ready cannot move #42 from Backlog; it starts only from In progress or Waiting
//	hold cannot run on #42 in Backlog; it runs only in In progress or Changes requested
func (e *Engine) checkFrom(v config.Verb, is Issue) error {
	current := is.Status
	if current == "" {
		current = e.Initial
	}
	if len(v.From) == 0 || slices.Contains(v.From, current) {
		return nil
	}
	// The same words as the dry run and confirmation line use.
	action, in, allowed := "move", " from ", "it starts only from"
	if v.To == "" {
		action, in, allowed = "run on", " in ", "it runs only in"
	}
	var from string
	switch {
	case !is.OnBoard:
		from = ": " + notOnBoard
	case is.Status == "":
		from = ": " + noStatus
	default:
		from = in + is.Status
	}
	if is.Status == "" && current != "" {
		from += ", which counts as " + current
	}
	name := is.Name()
	if is.Closed {
		name += " (" + closedText(is) + ")"
	}
	// No fix line and no allowed line: a refusal ends with asking the user,
	// never with a command that works around the process.
	return render.Denied("%s cannot %s %s%s; %s %s\n"+
		"this is a process rule: ask the user how to proceed",
		v.Name, action, name, from, allowed, join(v.From, "or")).WithCause(ErrFrom)
}

// ErrFrom is the cause of a refusal because the issue's status is not one
// of the verb's from statuses: test for it with errors.Is. gh dojo release
// reads it as "not ready yet" rather than as a refusal.
var ErrFrom = errors.New("status not in from")

// runsOnClosed reports whether v may run on a closed issue: its target
// status is final, or it closes (a repeat is safe) or reopens the issue.
func (e *Engine) runsOnClosed(v config.Verb) bool {
	return v.To != "" && slices.Contains(e.Final, v.To) || v.Close != "" || v.Reopen
}
