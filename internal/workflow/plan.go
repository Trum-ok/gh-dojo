package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Plan is what a verb does to one issue: the operations to apply, in order,
// and the effects found already in place.
type Plan struct {
	Verb   string
	Issue  Issue  // the state the plan was built from; Issue.Status is "" when not on the board
	Target string // the verb's target status; "" when it leaves the status alone

	// Ops are the operations to apply, in this order, each at most once
	// (AddBlockedBy once per blocking issue): AddToBoard or Unarchive,
	// Reopen, SetStatus, SetIteration, SetFields, SetMilestone, AddLabels,
	// RemoveLabels, Unassign, Assign, AddBlockedBy, AddComment, Close.
	// Empty when there is nothing to do.
	Ops []Op

	StatusUnchanged bool // the issue is already in Target; no SetStatus
	CommentPresent  bool // the same comment is the latest gh-dojo comment already; no AddComment
	// InPlace are the verb's effects found already in place, as the ops
	// that would have applied them: Reopen, SetIteration (From = To),
	// SetFields (the fields already set), SetMilestone (From = To),
	// AddLabels, RemoveLabels,
	// Assign, Unassign (the users not assigned, none for "all" with nobody
	// to remove; or Kept, the user the verb assigns), AddBlockedBy, and Close with the reason the issue was
	// closed with ("" when unknown). They are never applied; they give the "already" wording of
	// the dry run and the confirmation line.
	InPlace []Op

	// NeedsYes is set for a verb with confirm = "user" run without --yes
	// when the plan has something to apply; see Unconfirmed.
	NeedsYes bool

	// Warnings are for stderr, one line each, such as a missing agent
	// session. They never stop the verb.
	Warnings []string
}

// Op is one operation of a plan: AddToBoard, Unarchive, Reopen, SetStatus,
// SetIteration, SetFields, SetMilestone, AddLabels, RemoveLabels,
// Unassign, Assign, AddBlockedBy, AddComment or Close. Each kind has its
// own type, its own field in the mutation (see package effects) and its
// own words in the dry run, the confirmation line and a partial report.
type Op interface {
	// Describe is the op's line in a dry run.
	Describe() string
	// Effect is the op's part of the confirmation line once applied.
	Effect() string
	op()
}

// AddToBoard adds the issue to the project. It comes first: the other ops
// need the project item.
type AddToBoard struct{}

// Unarchive restores the issue's archived board item when a verb moves it
// to a status that is not final (see Engine.Plan). It comes first, like
// AddToBoard, which it replaces for an issue already on the board.
type Unarchive struct{}

// Reopen reopens a closed issue, before its status is set.
type Reopen struct{}

// SetStatus sets the status field. From is the status read, "" for none.
type SetStatus struct {
	From, To string
}

// AddLabels adds the labels Names to the issue.
type AddLabels struct {
	Names []string
}

// RemoveLabels removes the labels Names from the issue.
type RemoveLabels struct {
	Names []string
}

// Assign adds Login to the issue's assignees; the others stay.
type Assign struct {
	Login string
}

// Unassign removes Logins from the issue's assignees; the others stay.
// All says the verb removes every assignee but the one it assigns
// (unassign = "all"). It comes before Assign: an issue has at most 10
// assignees. Kept is only in Plan.InPlace: the verb does not remove the
// user it names because it assigns them too.
type Unassign struct {
	Logins []string
	All    bool
	Kept   bool
}

// AddBlockedBy marks the issue blocked by the issue Ref ("#12" or
// "owner/repo#12"): the blockers = "link" effect of --by.
type AddBlockedBy struct {
	Ref string
}

// AddComment posts a comment. Post Text, which ends with the marker.
type AddComment struct {
	Body   string // the rendered template, as shown in a dry run
	Marker string // see Marker
}

// Close closes the issue with Reason, a config close reason
// (config.CloseCompleted, ...). It comes last, after the comment that
// explains it. Of is the original ("#17" or "owner/repo#17") a duplicate
// is linked to, for close = "duplicate"; "" otherwise.
type Close struct {
	Reason string
	Of     string
}

// SetIteration sets the sprint field (fields.sprint) to the iteration To,
// or clears it when To is empty. From is the iteration read, "" for none.
// Start is To's start date (YYYY-MM-DD), which tells iterations with the
// same title apart.
type SetIteration struct {
	From, To string
	Start    string
}

// SetFields sets board fields to the values of the verb's field effect,
// one change per field, in field name order.
type SetFields struct {
	Changes []FieldChange
}

// SetMilestone puts the issue in the open milestone titled To, or takes
// it out of its milestone for To "". From is the title of the milestone it
// had, "" for none.
type SetMilestone struct {
	From, To string
}

// FieldChange is one field of SetFields: its board name, the value read
// ("" for none) and the value to set, as the config writes it.
type FieldChange struct {
	Field    string
	From, To string
	// Value is the config value To comes from; the executor turns it into
	// the board's input (board.Schema.FieldInput).
	Value config.FieldValue
}

func (AddToBoard) Describe() string     { return "add to the board" }
func (AddToBoard) Effect() string       { return "added to the board" }
func (AddToBoard) op()                  {}
func (Unarchive) Describe() string      { return "unarchive on the board" }
func (Unarchive) Effect() string        { return "unarchived" }
func (Unarchive) op()                   {}
func (Reopen) Describe() string         { return "reopen" }
func (Reopen) Effect() string           { return "reopened" }
func (Reopen) op()                      {}
func (s SetStatus) Describe() string    { return "status: " + render.Transition(s.From, s.To) }
func (s SetStatus) Effect() string      { return render.Transition(s.From, s.To) }
func (SetStatus) op()                   {}
func (l AddLabels) Describe() string    { return "labels: " + strings.Join(l.signs(), " ") }
func (l AddLabels) Effect() string      { return "labels " + strings.Join(l.signs(), " ") }
func (AddLabels) op()                   {}
func (l RemoveLabels) Describe() string { return "labels: " + strings.Join(l.signs(), " ") }
func (l RemoveLabels) Effect() string   { return "labels " + strings.Join(l.signs(), " ") }
func (RemoveLabels) op()                {}
func (a Assign) Describe() string       { return "assign: " + a.Login }
func (a Assign) Effect() string         { return "assigned " + a.Login }
func (Assign) op()                      {}
func (u Unassign) Describe() string     { return "unassign: " + strings.Join(u.Logins, ", ") }
func (u Unassign) Effect() string       { return "unassigned " + render.JoinWords(u.Logins, "and") }
func (Unassign) op()                    {}
func (b AddBlockedBy) Describe() string { return "blocked by: " + b.Ref }
func (b AddBlockedBy) Effect() string   { return "blocked by " + b.Ref }
func (AddBlockedBy) op()                {}
func (c AddComment) Describe() string {
	return "comment: " + strconv.Quote(c.Body)
}
func (AddComment) Effect() string       { return "comment left" }
func (AddComment) op()                  {}
func (c Close) Describe() string        { return "close: as " + c.reason() }
func (c Close) Effect() string          { return "closed as " + c.reason() }
func (Close) op()                       {}
func (s SetIteration) Describe() string { return "sprint: " + valueTransition(s.From, s.To) }
func (s SetIteration) Effect() string {
	if s.From == s.To { // in place: "sprint unchanged (Sprint 11)"
		return "sprint unchanged (" + orNone(s.To) + ")"
	}
	return "sprint " + valueTransition(s.From, s.To)
}
func (SetIteration) op()             {}
func (f SetFields) Describe() string { return strings.Join(f.each(FieldChange.describe), ", ") }
func (f SetFields) Effect() string   { return strings.Join(f.each(FieldChange.effect), ", ") }
func (SetFields) op()                {}
func (m SetMilestone) Describe() string {
	return "milestone: " + valueTransition(m.From, m.To)
}
func (m SetMilestone) Effect() string {
	if m.From == m.To { // in place: "milestone unchanged (v1.2)"
		return "milestone unchanged (" + orNone(m.To) + ")"
	}
	return "milestone " + valueTransition(m.From, m.To)
}
func (SetMilestone) op() {}

func (f SetFields) each(word func(FieldChange) string) []string {
	out := make([]string, len(f.Changes))
	for i, c := range f.Changes {
		out[i] = word(c)
	}
	return out
}

func (c FieldChange) describe() string { return "field: " + c.effect() }
func (c FieldChange) effect() string   { return c.Field + " " + valueTransition(c.From, c.To) }

// valueTransition is render.Transition for a field value: an empty value
// reads "none", not "no status" (render.Transition takes no word for it).
func valueTransition(from, to string) string { return render.Transition(orNone(from), orNone(to)) }

// orNone is a field value as output says it: "none" when empty.
func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func (l AddLabels) signs() []string    { return signed("+", l.Names) }
func (l RemoveLabels) signs() []string { return signed("-", l.Names) }

func signed(sign string, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = sign + n
	}
	return out
}

// reason is the close reason as output says it, with the original of a
// duplicate: "duplicate of #17".
func (c Close) reason() string {
	if c.Of != "" {
		return ReasonText(c.Reason) + " of " + c.Of
	}
	return ReasonText(c.Reason)
}

// ReasonText is a close reason as output says it: "completed", "not
// planned", "duplicate".
func ReasonText(reason string) string { return strings.ReplaceAll(reason, "_", " ") }

// Text is the comment body to post: Body, a blank line and Marker.
func (c AddComment) Text() string { return c.Body + "\n\n" + c.Marker }

// Wording shared by the dry run, the confirmation line and the from check.
const (
	noChanges  = "no changes"       // an empty list of effects
	noStatus   = "no status"        // as render.Transition says it; render exports no constant for it
	notOnBoard = "not on the board" // the "status" of an issue not on the board
)

// commentSkipped is the effect of a comment found already posted. It is never
// in Plan.Ops; it only gives the dry-run and confirmation wording.
type commentSkipped struct{}

func (commentSkipped) Describe() string { return "comment: already present, not posted again" }
func (commentSkipped) Effect() string   { return "comment already present" }
func (commentSkipped) op()              {}

// Empty reports whether the plan has nothing to apply.
func (p *Plan) Empty() bool { return len(p.Ops) == 0 }

// Unconfirmed refuses a plan that needs the user's go-ahead (NeedsYes)
// with exit code 2, naming what the verb would do; nil otherwise. A dry
// run shows the plan instead.
//
//	drop needs the user's go-ahead for #42 (would be: comment left, closed as not planned)
//	this is the user's decision: ask the user, and re-run with --yes only if they agree
func (p *Plan) Unconfirmed() error {
	if !p.NeedsYes {
		return nil
	}
	return render.Denied("%s needs the user's go-ahead for %s (would be: %s)\n"+
		"this is the user's decision: ask the user, and re-run with --yes only if they agree",
		p.Verb, p.Issue.Name(), strings.Join(p.lines(false), ", "))
}

// lines returns one entry per effect, in a fixed order: board, reopen,
// status, sprint, fields (one entry per field), milestone, labels, assignees (who is
// removed, then who is assigned, as the mutation does), blocked-by link,
// comment, close. dry picks the dry-run wording
// (Describe), otherwise the confirmation wording (Effect). Effects already
// in place are included. The status change of an issue not on the board
// reads "not on the board → In progress"; in the confirmation that line
// alone stands for adding the issue to the board. Unarchiving is a board
// effect too, but always has its own line. Labels added and removed share
// one line; so do the blocked-by links added, and those already in place.
func (p *Plan) lines(dry bool) []string {
	pick := Op.Effect
	if dry {
		pick = Op.Describe
	}
	var board, unarchive, reopen, status, sprint, fields, milestone, assign, unassign, blocked, comment, closing []string
	var blockers, blockersInPlace []string // refs of AddBlockedBy, joined in one line each
	var labels, labelsInPlace []string     // +name and -name
	change := FieldChange.effect
	if dry {
		change = FieldChange.describe
	}
	for _, op := range p.Ops {
		switch op := op.(type) {
		case AddToBoard:
			board = append(board, pick(op))
		case Unarchive:
			unarchive = append(unarchive, pick(op))
		case Reopen:
			reopen = append(reopen, pick(op))
		case SetStatus:
			if !p.Issue.OnBoard {
				op.From = notOnBoard
			}
			status = append(status, pick(op))
		case SetIteration:
			sprint = append(sprint, pick(op))
		case SetFields:
			fields = append(fields, op.each(change)...)
		case SetMilestone:
			milestone = append(milestone, pick(op))
		case AddLabels:
			labels = append(labels, op.signs()...)
		case RemoveLabels:
			labels = append(labels, op.signs()...)
		case Assign:
			assign = append(assign, pick(op))
		case Unassign:
			unassign = append(unassign, pick(op))
		case AddBlockedBy:
			blockers = append(blockers, op.Ref)
		case AddComment:
			comment = append(comment, pick(op))
		case Close:
			closing = append(closing, pick(op))
		}
	}
	if !dry && len(status) > 0 {
		board = nil
	}
	if p.StatusUnchanged {
		status = append(status, pick(SetStatus{From: p.Target, To: p.Target}))
	}
	if p.CommentPresent {
		comment = append(comment, pick(commentSkipped{}))
	}
	// key prefixes an in-place effect in the dry run: "close: already closed".
	key := func(name, text string) string {
		if dry {
			return name + ": " + text
		}
		return text
	}
	for _, op := range p.InPlace {
		switch op := op.(type) {
		case Reopen:
			reopen = append(reopen, key("reopen", "already open"))
		case SetIteration:
			sprint = append(sprint, pick(op))
		case SetFields:
			fields = append(fields, op.each(change)...)
		case SetMilestone:
			milestone = append(milestone, pick(op))
		case AddLabels:
			labelsInPlace = append(labelsInPlace, op.signs()...)
		case RemoveLabels:
			labelsInPlace = append(labelsInPlace, op.signs()...)
		case Assign:
			assign = append(assign, key("assign", op.Login+" already assigned"))
		case Unassign:
			text := render.JoinWords(op.Logins, "and") + " already unassigned"
			switch {
			case op.Kept:
				text = render.JoinWords(op.Logins, "and") + " kept: the verb assigns them"
			case op.All:
				text = "no assignees"
				if slices.ContainsFunc(append(slices.Clone(p.Ops), p.InPlace...), func(o Op) bool { _, ok := o.(Assign); return ok }) {
					text = "no one else assigned"
				}
			}
			unassign = append(unassign, key("unassign", text))
		case AddBlockedBy:
			blockersInPlace = append(blockersInPlace, op.Ref)
		case Close:
			text := "already closed"
			if op.Reason != "" {
				text += " as " + ReasonText(op.Reason)
			}
			closing = append(closing, key("close", text))
		}
	}
	var label []string
	switch {
	case len(labels) > 0 && len(labelsInPlace) > 0 && dry:
		label = []string{"labels: " + strings.Join(labels, " ") +
			" (" + strings.Join(labelsInPlace, " ") + " already in place)"}
	case len(labels) > 0 && dry:
		label = []string{"labels: " + strings.Join(labels, " ")}
	case len(labels) > 0:
		label = []string{"labels " + strings.Join(labels, " ")}
	case len(labelsInPlace) > 0 && dry:
		label = []string{"labels: " + strings.Join(labelsInPlace, " ") + " already in place"}
	case len(labelsInPlace) > 0:
		label = []string{"labels unchanged"}
	}
	if len(blockers) > 0 {
		blocked = append(blocked, pick(AddBlockedBy{Ref: render.JoinWords(blockers, "and")}))
	}
	switch {
	case len(blockersInPlace) > 0 && dry:
		blocked = append(blocked, "blocked by: "+render.JoinWords(blockersInPlace, "and")+" already linked")
	case len(blockersInPlace) > 0:
		blocked = append(blocked, "already blocked by "+render.JoinWords(blockersInPlace, "and"))
	}
	board = append(board, unarchive...)
	var out []string
	for _, group := range [][]string{board, reopen, status, sprint, fields, milestone, label, unassign, assign, blocked, comment, closing} {
		out = append(out, group...)
	}
	return out
}

// DryRun renders the plan for --dry-run: a header, then one indented line
// per effect, including those already in place, and a last line when the
// verb needs --yes.
//
//	dry run: start #42 "Fix login redirect"
//	  status: Backlog → In progress
//	  comment: "Started. Session: claude://claude.ai/epitaxy/abc"
func (p *Plan) DryRun() string {
	var b strings.Builder
	b.WriteString(render.Confirm(fmt.Sprintf("dry run: %s %s", p.Verb, p.Issue.Name()), p.Issue.Title))
	lines := p.lines(true)
	if len(lines) == 0 {
		lines = []string{noChanges}
	}
	if p.NeedsYes {
		lines = append(lines, "needs --yes: this is the user's decision, ask the user first")
	}
	for _, l := range lines {
		b.WriteString("\n  " + l)
	}
	return b.String()
}

// Confirm is the confirmation line of a fully applied plan:
//
//	#42 Backlog → In progress, comment left
//	#42 not on the board → In progress, comment left
//	#42 In progress (unchanged), comment already present
//	#42 Ready → Done, comment left, closed as completed
//	#42 comment already present, already closed as not planned
func (p *Plan) Confirm() string {
	effects := p.lines(false)
	if len(effects) == 0 {
		effects = []string{noChanges}
	}
	return render.Confirm(p.Issue.Name(), "", effects...)
}

// markerRE matches the hidden marker gh-dojo puts at the end of a verb's
// comment, after a blank line (see AddComment.Text). A marker anywhere else,
// such as one quoted in the middle of a comment, does not count.
var markerRE = regexp.MustCompile(`\r?\n\r?\n(<!-- gh-dojo:[a-z][a-z0-9-]*:[0-9a-f]{8} -->)\s*$`)

// Marker returns the hidden marker of a comment that verb posts with body:
// "<!-- gh-dojo:<verb>:<hash> -->", where hash is the first 8 hex digits of
// the SHA-256 of the verb name and the body. Equal bodies give equal markers.
func Marker(verb, body string) string {
	sum := sha256.Sum256([]byte(verb + "\n" + body))
	return fmt.Sprintf("<!-- gh-dojo:%s:%s -->", verb, hex.EncodeToString(sum[:4]))
}

// LastMarker returns the marker of the most recent gh-dojo comment, of any
// verb; comments are oldest first. Only comments the viewer wrote count, and
// only a marker that ends the comment (see markerRE).
func LastMarker(comments []Comment) (string, bool) {
	for i := len(comments) - 1; i >= 0; i-- {
		if !comments[i].ViewerDidAuthor {
			continue
		}
		if m := markerRE.FindStringSubmatch(comments[i].Body); m != nil {
			return m[1], true
		}
	}
	return "", false
}

// AlreadyPosted reports whether the most recent gh-dojo comment on the issue
// carries marker, so posting the comment again would duplicate it. This
// makes a retry after a partial failure safe, while a new cycle still
// comments: after start, wait and start again, the second start's comment is
// posted even if it equals the first.
func AlreadyPosted(comments []Comment, marker string) bool {
	last, ok := LastMarker(comments)
	return ok && last == marker
}
