package main

import (
	"strings"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/issue"
)

// The JSON of show: the cards printed, read with the same flags and cut
// the same way (--comments, --full), and the issues that failed.

// showResult is show's JSON.
type showResult struct {
	Issues []showCard   `json:"issues"`
	Failed []showFailed `json:"failed"` // numbers not shown; the error on stderr names them too
}

// showFailed is an issue show could not print: not found, a pull
// request, or not read.
type showFailed struct {
	Number int        `json:"number"`
	Error  *jsonError `json:"error"`
}

type showCard struct {
	Number      int        `json:"number"`
	Repo        string     `json:"repo"`
	URL         string     `json:"url"`
	Title       string     `json:"title"`
	State       string     `json:"state"`        // open or closed
	StateReason string     `json:"state_reason"` // completed, not_planned, duplicate, reopened; "" never closed
	Author      string     `json:"author"`       // "ghost" for a deleted account
	CreatedAt   *time.Time `json:"created_at"`
	ClosedAt    *time.Time `json:"closed_at"` // null while open
	// Incomplete: GitHub withheld part of the issue from the token.
	Incomplete bool `json:"incomplete"`

	Board     showBoard      `json:"board"`
	Sprint    *sprintJSON    `json:"sprint"` // null without fields.sprint
	Milestone *jsonMilestone `json:"milestone"`

	Labels         []string `json:"labels"`
	LabelsTotal    int      `json:"labels_total"`
	Assignees      []string `json:"assignees"`
	AssigneesTotal int      `json:"assignees_total"`

	BlockedBy []showLink `json:"blocked_by"`
	// BlockedByTotal counts every blocker, also those not read; Unresolved
	// counts the blockers read that still block.
	BlockedByTotal int        `json:"blocked_by_total"`
	Unresolved     int        `json:"unresolved_blockers"`
	Blocking       []showLink `json:"blocking"`
	BlockingTotal  int        `json:"blocking_total"`

	// History, Body and Comments are left out with --brief.
	History          *[]showChange  `json:"history,omitempty"`
	HistoryTruncated bool           `json:"history_truncated"`
	HistoryUntracked bool           `json:"history_untracked"`
	Body             *showText      `json:"body,omitempty"`
	Comments         *[]showComment `json:"comments,omitempty"`
	CommentsTotal    int            `json:"comments_total"`
}

type showBoard struct {
	OnBoard bool `json:"on_board"` // false also when unknown_reason is set
	// UnknownReason says why board membership is unknown: the issue may be
	// on the board; "" when known.
	UnknownReason string `json:"unknown_reason"`
	Status        string `json:"status"`         // "" for none, not on the board, or unknown
	StatusUnknown bool   `json:"status_unknown"` // the status could not be read
	Archived      bool   `json:"archived"`
	// Fields are the item's other set field values, sprint and status aside.
	Fields          []showField `json:"fields"`
	FieldsTruncated bool        `json:"fields_truncated"`
}

type showField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type sprintJSON struct {
	Field     string         `json:"field"`
	Iteration *jsonIteration `json:"iteration"` // null for none
	// UnknownReason says why the iteration is not known; "" when known.
	UnknownReason string `json:"unknown_reason"`
}

// jsonIteration is a sprint iteration with its days, as JSON.
type jsonIteration struct {
	Title        string `json:"title"`
	StartDate    string `json:"start_date"` // YYYY-MM-DD
	EndDate      string `json:"end_date"`   // the last day
	DurationDays int    `json:"duration_days"`
}

// iterationJSON is it as JSON, nil for nil.
func iterationJSON(it *board.Iteration) *jsonIteration {
	if it == nil {
		return nil
	}
	out := &jsonIteration{Title: it.Title, StartDate: it.StartDate, DurationDays: it.Duration}
	if start, err := time.Parse(time.DateOnly, it.StartDate); err == nil && it.Duration > 0 {
		out.EndDate = start.AddDate(0, 0, it.Duration-1).Format(time.DateOnly)
	}
	return out
}

type jsonMilestone struct {
	Title string `json:"title"`
	DueOn string `json:"due_on"` // YYYY-MM-DD; "" without a due date
}

type showLink struct {
	Ref    string `json:"ref"` // "#12", or "owner/name#12" in another repository
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	// OnBoard, Status and Archived are its place on the board; when
	// StatusUnknownReason is set they are not known.
	OnBoard             bool   `json:"on_board"`
	Status              string `json:"status"`
	Archived            bool   `json:"archived"`
	StatusUnknownReason string `json:"status_unknown_reason"`
	// Resolved: closed, or in a status of statuses.resolved. A blocker
	// that is not resolved still blocks.
	Resolved bool `json:"resolved"`
}

type showChange struct {
	At        *time.Time `json:"at"`
	From      string     `json:"from"` // "" for no status
	To        string     `json:"to"`
	Actor     string     `json:"actor"`
	Automated bool       `json:"automated"`
}

// showText is a description or comment body as show prints it: cut
// unless --full, the hidden gh-dojo marker dropped.
type showText struct {
	Text   string `json:"text"`
	Cut    bool   `json:"cut"`    // Text is the start only; --full gives all
	Length int    `json:"length"` // characters of the whole text
}

type showComment struct {
	Author    string     `json:"author"`
	CreatedAt *time.Time `json:"created_at"`
	showText
}

// result is show's JSON for the cards read, the ones that failed and
// those left unread by readErr.
func (v cardView) result(cards []*issue.Card, brief bool, failed []issue.Result, unread []int, readErr error) showResult {
	out := showResult{Issues: []showCard{}, Failed: []showFailed{}}
	for _, c := range cards {
		out.Issues = append(out.Issues, v.cardJSON(c, brief))
	}
	for _, n := range unread {
		out.Failed = append(out.Failed, showFailed{Number: n, Error: errorJSON(readErr)})
	}
	for _, r := range failed {
		out.Failed = append(out.Failed, showFailed{Number: r.Number, Error: errorJSON(r.Err)})
	}
	return out
}

// cardJSON is one card as JSON; brief leaves out the history, the
// description and the comments.
func (v cardView) cardJSON(c *issue.Card, brief bool) showCard {
	out := showCard{
		Number:           c.Number,
		Repo:             c.Repo.String(),
		URL:              c.URL,
		Title:            singleLine(c.Title),
		State:            strings.ToLower(c.State),
		StateReason:      strings.ToLower(c.StateReason),
		Author:           cardLogin(c.Author),
		CreatedAt:        jsonTime(c.CreatedAt),
		ClosedAt:         jsonTime(c.ClosedAt),
		Incomplete:       c.Incomplete,
		Board:            v.boardJSON(c),
		Labels:           orEmpty(c.Labels.Nodes),
		LabelsTotal:      max(c.Labels.TotalCount, len(c.Labels.Nodes)),
		Assignees:        orEmpty(c.Assignees.Nodes),
		AssigneesTotal:   max(c.Assignees.TotalCount, len(c.Assignees.Nodes)),
		BlockedBy:        v.linksJSON(c, c.BlockedBy.Nodes),
		BlockedByTotal:   max(c.BlockedBy.TotalCount, len(c.BlockedBy.Nodes)),
		Unresolved:       len(c.UnresolvedBlockers(v.resolved)),
		Blocking:         v.linksJSON(c, c.Blocking.Nodes),
		BlockingTotal:    max(c.Blocking.TotalCount, len(c.Blocking.Nodes)),
		HistoryTruncated: c.HistoryTruncated,
		HistoryUntracked: c.HistoryUntracked,
		CommentsTotal:    max(c.Comments.TotalCount, len(c.Comments.Nodes)),
	}
	if v.sprintField != "" {
		it, unknown := v.sprintOf(c)
		out.Sprint = &sprintJSON{Field: v.sprintField, Iteration: iterationJSON(it), UnknownReason: unknown}
	}
	if m := c.Milestone; m != nil {
		out.Milestone = &jsonMilestone{Title: singleLine(m.Title)}
		if !m.DueOn.IsZero() {
			out.Milestone.DueOn = cardDay(m.DueOn)
		}
	}
	if brief {
		return out
	}
	history := []showChange{}
	if c.OnBoard() || c.BoardUnknown {
		for _, h := range c.History {
			history = append(history, showChange{
				At: jsonTime(h.At), From: h.From, To: h.To, Actor: cardLogin(h.Actor),
				Automated: h.Automated || projectAutomation[h.Actor],
			})
		}
	}
	out.History = &history
	body := cleanText(c.Body, true)
	out.Body = textJSON(body, v.bodyRunes)
	comments := []showComment{}
	shown, bodies, limit := v.shownComments(c)
	for i, cm := range shown {
		text := bodies[i]
		if text == "(empty)" && cleanText(cm.Body, cm.ViewerDidAuthor) == "" {
			text = ""
		}
		comments = append(comments, showComment{
			Author: cardLogin(cm.Author), CreatedAt: jsonTime(cm.CreatedAt), showText: *textJSON(text, limit),
		})
	}
	out.Comments = &comments
	return out
}

// textJSON is text cut at limit runes (0: whole) as show cuts it.
func textJSON(text string, limit int) *showText {
	cut, note := fold(text, limit)
	return &showText{Text: cut, Cut: note != "", Length: runes(text)}
}

// boardJSON is the card's place on the board.
func (v cardView) boardJSON(c *issue.Card) showBoard {
	b := showBoard{
		OnBoard:       c.OnBoard(),
		UnknownReason: c.BoardUnknownReason,
		Status:        c.Status(),
		StatusUnknown: c.StatusUnknown(),
		Archived:      c.Archived(),
		Fields:        []showField{},
	}
	if c.BoardUnknown && b.UnknownReason == "" {
		b.UnknownReason = "GitHub did not return all of its projects"
	}
	if c.Item != nil {
		for _, val := range c.Item.Values {
			if val.Field != v.statusField && !v.isSprint(val) {
				b.Fields = append(b.Fields, showField{Name: singleLine(val.Field), Value: singleLine(val.Text)})
			}
		}
		b.FieldsTruncated = c.Item.ValuesTruncated
	}
	return b
}

// linksJSON is linked issues in GitHub's order.
func (v cardView) linksJSON(c *issue.Card, links []issue.Link) []showLink {
	out := []showLink{}
	for _, l := range links {
		repo := l.Repo
		if repo == (issue.Repo{}) {
			repo = c.Repo
		}
		out = append(out, showLink{
			Ref: l.Ref(c.Repo), Repo: repo.String(), Number: l.Number, Title: singleLine(l.Title),
			State: strings.ToLower(l.State), OnBoard: l.OnBoard, Status: l.Status, Archived: l.Archived,
			StatusUnknownReason: l.StatusUnknown, Resolved: l.Resolved(v.resolved),
		})
	}
	return out
}
