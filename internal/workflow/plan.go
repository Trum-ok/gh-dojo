package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Plan is what a verb does to one issue: the operations to apply, in order,
// and the effects found already in place.
type Plan struct {
	Verb   string
	Issue  Issue  // the state the plan was built from; Issue.Status is "" when not on the board
	Target string // the verb's target status; "" when it leaves the status alone

	// Ops are the operations to apply, in this order: AddToBoard or
	// Unarchive, SetStatus, AddComment, each at most once. Empty when there
	// is nothing to do.
	Ops []Op

	StatusUnchanged bool // the issue is already in Target; no SetStatus
	CommentPresent  bool // the same comment is the latest gh-dojo comment already; no AddComment

	// Warnings are for stderr, one line each, such as a missing agent
	// session. They never stop the verb.
	Warnings []string
}

// Op is one operation of a plan: AddToBoard, Unarchive, SetStatus or
// AddComment.
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

// Unarchive restores the issue's archived board item. It comes first, like
// AddToBoard, which it replaces for an issue already on the board.
type Unarchive struct{}

// SetStatus sets the status field. From is the status read, "" for none.
type SetStatus struct {
	From, To string
}

// AddComment posts a comment. Post Text, which ends with the marker.
type AddComment struct {
	Body   string // the rendered template, as shown in a dry run
	Marker string // see Marker
}

func (AddToBoard) Describe() string  { return "add to the board" }
func (AddToBoard) Effect() string    { return "added to the board" }
func (AddToBoard) op()               {}
func (Unarchive) Describe() string   { return "unarchive on the board" }
func (Unarchive) Effect() string     { return "unarchived" }
func (Unarchive) op()                {}
func (s SetStatus) Describe() string { return "status: " + render.Transition(s.From, s.To) }
func (s SetStatus) Effect() string   { return render.Transition(s.From, s.To) }
func (SetStatus) op()                {}
func (c AddComment) Describe() string {
	return "comment: " + strconv.Quote(c.Body)
}
func (AddComment) Effect() string { return "comment left" }
func (AddComment) op()            {}

// Text is the comment body to post: Body, a blank line and Marker.
func (c AddComment) Text() string { return c.Body + "\n\n" + c.Marker }

// Wording shared by the dry run, the confirmation line and the from check.
const (
	noChanges  = "no changes"       // an empty list of effects
	noStatus   = "no status"        // as render.Transition says it
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

// lines returns one entry per effect, through pick, in a fixed order: board,
// status, comment. Effects already in place are included. The status change
// of an issue not on the board reads "not on the board → In progress"; with
// short set, that line alone stands for adding the issue to the board.
// Unarchiving is a board effect too, but always has its own line.
func (p *Plan) lines(pick func(Op) string, short bool) []string {
	var board, unarchive, status, comment []string
	for _, op := range p.Ops {
		switch op := op.(type) {
		case AddToBoard:
			board = append(board, pick(op))
		case Unarchive:
			unarchive = append(unarchive, pick(op))
		case SetStatus:
			if !p.Issue.OnBoard {
				op.From = notOnBoard
			}
			status = append(status, pick(op))
		case AddComment:
			comment = append(comment, pick(op))
		}
	}
	if short && len(status) > 0 {
		board = nil
	}
	if p.StatusUnchanged {
		status = append(status, pick(SetStatus{From: p.Target, To: p.Target}))
	}
	if p.CommentPresent {
		comment = append(comment, pick(commentSkipped{}))
	}
	board = append(board, unarchive...)
	return append(append(board, status...), comment...)
}

// DryRun renders the plan for --dry-run: a header, then one indented line
// per effect, including those already in place.
//
//	dry run: start #42 "Fix login redirect"
//	  status: Backlog → In progress
//	  comment: "Started. Session: claude://claude.ai/epitaxy/abc"
func (p *Plan) DryRun() string {
	var b strings.Builder
	b.WriteString(render.Confirm(fmt.Sprintf("dry run: %s #%d", p.Verb, p.Issue.Number), p.Issue.Title))
	lines := p.lines(Op.Describe, false)
	if len(lines) == 0 {
		lines = []string{noChanges}
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
func (p *Plan) Confirm() string {
	effects := p.lines(Op.Effect, true)
	if len(effects) == 0 {
		effects = []string{noChanges}
	}
	return render.Confirm(fmt.Sprintf("#%d", p.Issue.Number), "", effects...)
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
