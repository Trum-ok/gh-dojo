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

	// Ops are the operations to apply, in this order: AddToBoard, SetStatus,
	// AddComment, each at most once. Empty when there is nothing to do.
	Ops []Op

	StatusUnchanged bool // the issue is already in Target; no SetStatus
	CommentPresent  bool // the same comment is the verb's latest already; no AddComment

	// Warnings are for stderr, one line each, such as a missing agent
	// session. They never stop the verb.
	Warnings []string
}

// Op is one operation of a plan: AddToBoard, SetStatus or AddComment.
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

// noChanges stands for an empty list of effects.
const noChanges = "no changes"

// commentSkipped is the effect of a comment found already posted. It is never
// in Plan.Ops; it only gives the dry-run and confirmation wording.
type commentSkipped struct{}

func (commentSkipped) Describe() string { return "comment: already present, not posted again" }
func (commentSkipped) Effect() string   { return "comment already present" }
func (commentSkipped) op()              {}

// Empty reports whether the plan has nothing to apply.
func (p *Plan) Empty() bool { return len(p.Ops) == 0 }

// lines returns one entry per effect, through pick, in a fixed order: board,
// status, comment. Effects already in place are included.
func (p *Plan) lines(pick func(Op) string) []string {
	var board, status, comment []string
	for _, op := range p.Ops {
		switch op.(type) {
		case AddToBoard:
			board = append(board, pick(op))
		case SetStatus:
			status = append(status, pick(op))
		case AddComment:
			comment = append(comment, pick(op))
		}
	}
	if p.StatusUnchanged {
		status = append(status, pick(SetStatus{From: p.Target, To: p.Target}))
	}
	if p.CommentPresent {
		comment = append(comment, pick(commentSkipped{}))
	}
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
	lines := p.lines(Op.Describe)
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
//	#42 In progress (unchanged), comment already present
func (p *Plan) Confirm() string {
	effects := p.lines(Op.Effect)
	if len(effects) == 0 {
		effects = []string{noChanges}
	}
	return render.Confirm(fmt.Sprintf("#%d", p.Issue.Number), "", effects...)
}

// markerRE matches the hidden marker gh-dojo appends to a verb's comment.
var markerRE = regexp.MustCompile(`<!-- gh-dojo:([a-z][a-z0-9-]*):([0-9a-f]{8,64}) -->`)

// Marker returns the hidden marker of a comment that verb posts with body:
// "<!-- gh-dojo:<verb>:<hash> -->", where hash is the first 8 hex digits of
// the SHA-256 of the verb name and the body. Equal bodies give equal markers.
func Marker(verb, body string) string {
	sum := sha256.Sum256([]byte(verb + "\n" + body))
	return fmt.Sprintf("<!-- gh-dojo:%s:%s -->", verb, hex.EncodeToString(sum[:4]))
}

// LastMarker returns the marker of the most recent comment that carries a
// marker of verb; comments are oldest first. A comment with several markers
// counts by its last one.
func LastMarker(comments []Comment, verb string) (string, bool) {
	for i := len(comments) - 1; i >= 0; i-- {
		ms := markerRE.FindAllStringSubmatch(comments[i].Body, -1)
		for j := len(ms) - 1; j >= 0; j-- {
			if ms[j][1] == verb {
				return ms[j][0], true
			}
		}
	}
	return "", false
}

// AlreadyPosted reports whether the verb's latest marked comment carries
// marker, so posting the comment again would duplicate it. An earlier comment
// with the same marker does not count: after a different comment of the verb
// the same text is posted again.
func AlreadyPosted(comments []Comment, verb, marker string) bool {
	last, ok := LastMarker(comments, verb)
	return ok && last == marker
}
