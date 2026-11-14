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
	// Brief: --brief, so history and comments are [] and body null in
	// every card.
	Brief  bool         `json:"brief"`
	Issues []showCard   `json:"issues"`
	Failed []showFailed `json:"failed"` // numbers not shown; the error on stderr names them too
	// Warnings are also printed on stderr.
	Warnings []string `json:"warnings"`
}

// showFailed is an issue show could not print: not found, a pull
// request, or not read.
type showFailed struct {
	Number int        `json:"number"`
	Repo   string     `json:"repo"`
	Error  *jsonError `json:"error"`
}

type showCard struct {
	Number      int        `json:"number"`
	Repo        string     `json:"repo"`
	URL         string     `json:"url"`
	Title       string     `json:"title"`
	State       string     `json:"state"`        // open or closed
	StateReason *string    `json:"state_reason"` // completed, not_planned, duplicate, reopened; null never closed
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
	// Parent is the issue this one is a sub-issue of, null for none.
	// SubIssues are its own; SubIssuesTotal counts every one, also those
	// not read; SubIssuesClosed and UnresolvedSubIssues count the ones read
	// that are closed and that are unresolved (as blockers are).
	Parent              *showLink  `json:"parent"`
	SubIssues           []showLink `json:"sub_issues"`
	SubIssuesTotal      int        `json:"sub_issues_total"`
	SubIssuesClosed     int        `json:"sub_issues_closed"`
	UnresolvedSubIssues int        `json:"unresolved_sub_issues"`

	// History and Comments are [] and Body null with --brief.
	History          []showChange  `json:"history"`
	HistoryTruncated bool          `json:"history_truncated"`
	HistoryUntracked bool          `json:"history_untracked"`
	Body             *showText     `json:"body"`
	Comments         []showComment `json:"comments"`
	CommentsTotal    int           `json:"comments_total"`
}

type showBoard struct {
	OnBoard bool `json:"on_board"` // false also when unknown_reason is set
	// UnknownReason says why board membership is unknown: the issue may be
	// on the board; null when known.
	UnknownReason *string `json:"unknown_reason"`
	Status        *string `json:"status"`         // null for none, not on the board, or unknown
	StatusUnknown bool    `json:"status_unknown"` // the status could not be read
	Archived      bool    `json:"archived"`
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
	// UnknownReason says why the iteration is not known; null when known.
	UnknownReason *string `json:"unknown_reason"`
}

// jsonIteration is a sprint iteration with its days, as JSON.
type jsonIteration struct {
	Title        string  `json:"title"`
	StartDate    string  `json:"start_date"` // YYYY-MM-DD
	EndDate      *string `json:"end_date"`   // the last day; null when not known
	DurationDays int     `json:"duration_days"`
}

// iterationJSON is it as JSON, nil for nil.
func iterationJSON(it *board.Iteration) *jsonIteration {
	if it == nil {
		return nil
	}
	out := &jsonIteration{Title: it.Title, StartDate: it.StartDate, DurationDays: it.Duration}
	if start, err := time.Parse(time.DateOnly, it.StartDate); err == nil && it.Duration > 0 {
		out.EndDate = orNull(start.AddDate(0, 0, it.Duration-1).Format(time.DateOnly))
	}
	return out
}

type jsonMilestone struct {
	Title string  `json:"title"`
	DueOn *string `json:"due_on"` // YYYY-MM-DD; null without a due date
}

type showLink struct {
	Ref    string `json:"ref"` // "owner/name#12", always in full
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	// OnBoard, Status and Archived are its place on the board; when
	// StatusUnknownReason is set they are not known.
	OnBoard             bool    `json:"on_board"`
	Status              *string `json:"status"` // null for none or not on the board
	Archived            bool    `json:"archived"`
	StatusUnknownReason *string `json:"status_unknown_reason"` // null when known
	// Resolved: closed, or in a status of statuses.resolved. A blocker
	// that is not resolved still blocks; a sub-issue that is not resolved
	// fails a sub_issues check.
	Resolved bool `json:"resolved"`
}

type showChange struct {
	At        *time.Time `json:"at"`
	From      *string    `json:"from"` // null for no status
	To        *string    `json:"to"`   // null for no status
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
func (v cardView) result(cards []*issue.Card, brief bool, failed []issue.Result, unread []issue.Ref, readErr error) showResult {
	out := showResult{Brief: brief, Issues: []showCard{}, Failed: []showFailed{}, Warnings: []string{}}
	for _, c := range cards {
		out.Issues = append(out.Issues, v.cardJSON(c, brief))
	}
	for _, r := range unread {
		out.Failed = append(out.Failed, showFailed{Number: r.Number, Repo: r.Repo.String(), Error: errorJSON(readErr)})
	}
	for _, r := range failed {
		out.Failed = append(out.Failed, showFailed{Number: r.Number, Repo: r.Repo.String(), Error: errorJSON(r.Err)})
	}
	return out
}

// cardJSON is one card as JSON; brief leaves out the history, the
// description and the comments.
func (v cardView) cardJSON(c *issue.Card, brief bool) showCard {
	out := showCard{
		Number:              c.Number,
		Repo:                c.Repo.String(),
		URL:                 c.URL,
		Title:               singleLine(c.Title),
		State:               strings.ToLower(c.State),
		StateReason:         orNull(strings.ToLower(c.StateReason)),
		Author:              cardLogin(c.Author),
		CreatedAt:           jsonTime(c.CreatedAt),
		ClosedAt:            jsonTime(c.ClosedAt),
		Incomplete:          c.Incomplete,
		Board:               v.boardJSON(c),
		Labels:              orEmpty(c.Labels.Nodes),
		LabelsTotal:         max(c.Labels.TotalCount, len(c.Labels.Nodes)),
		Assignees:           orEmpty(c.Assignees.Nodes),
		AssigneesTotal:      max(c.Assignees.TotalCount, len(c.Assignees.Nodes)),
		BlockedBy:           v.linksJSON(c, c.BlockedBy.Nodes),
		BlockedByTotal:      max(c.BlockedBy.TotalCount, len(c.BlockedBy.Nodes)),
		Unresolved:          len(c.UnresolvedBlockers(v.resolved)),
		Blocking:            v.linksJSON(c, c.Blocking.Nodes),
		BlockingTotal:       max(c.Blocking.TotalCount, len(c.Blocking.Nodes)),
		SubIssues:           v.linksJSON(c, c.SubIssues.Nodes),
		SubIssuesTotal:      max(c.SubIssues.TotalCount, len(c.SubIssues.Nodes)),
		UnresolvedSubIssues: len(c.UnresolvedSubIssues(v.resolved)),
		HistoryTruncated:    c.HistoryTruncated,
		HistoryUntracked:    c.HistoryUntracked,
		History:             []showChange{},
		Comments:            []showComment{},
		CommentsTotal:       max(c.Comments.TotalCount, len(c.Comments.Nodes)),
	}
	if c.Parent != nil {
		p := v.linkJSON(c, *c.Parent)
		out.Parent = &p
	}
	for _, l := range c.SubIssues.Nodes {
		if l.Closed() {
			out.SubIssuesClosed++
		}
	}
	if v.sprintField != "" {
		it, unknown := v.sprintOf(c)
		out.Sprint = &sprintJSON{Field: v.sprintField, Iteration: iterationJSON(it), UnknownReason: orNull(unknown)}
	}
	if m := c.Milestone; m != nil {
		out.Milestone = &jsonMilestone{Title: singleLine(m.Title)}
		if !m.DueOn.IsZero() {
			out.Milestone.DueOn = orNull(cardDay(m.DueOn))
		}
	}
	if brief {
		return out
	}
	if c.OnBoard() || c.BoardUnknown {
		for _, h := range c.History {
			out.History = append(out.History, showChange{
				At: jsonTime(h.At), From: orNull(h.From), To: orNull(h.To), Actor: cardLogin(h.Actor),
				Automated: h.Automated || projectAutomation[h.Actor],
			})
		}
	}
	body := cleanText(c.Body, true)
	out.Body = textJSON(body, v.bodyRunes)
	shown, bodies, limit := v.shownComments(c)
	for i, cm := range shown {
		text := bodies[i]
		if text == "(empty)" && cleanText(cm.Body, cm.ViewerDidAuthor) == "" {
			text = ""
		}
		out.Comments = append(out.Comments, showComment{
			Author: cardLogin(cm.Author), CreatedAt: jsonTime(cm.CreatedAt), showText: *textJSON(text, limit),
		})
	}
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
		UnknownReason: orNull(c.BoardUnknownReason),
		Status:        orNull(c.Status()),
		StatusUnknown: c.StatusUnknown(),
		Archived:      c.Archived(),
		Fields:        []showField{},
	}
	if c.BoardUnknown && b.UnknownReason == nil {
		b.UnknownReason = orNull("GitHub did not return all of its projects")
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
		out = append(out, v.linkJSON(c, l))
	}
	return out
}

// linkJSON is a linked issue of the card.
func (v cardView) linkJSON(c *issue.Card, l issue.Link) showLink {
	// The default repository as the config spells it.
	repo := issue.Ref{Repo: l.Repo, Number: l.Number}.In(c.Repo).In(v.def).Repo
	return showLink{
		Ref: fullRef(issue.Ref{Repo: repo, Number: l.Number}), Repo: repo.String(), Number: l.Number, Title: singleLine(l.Title),
		State: strings.ToLower(l.State), OnBoard: l.OnBoard, Status: orNull(l.Status), Archived: l.Archived,
		StatusUnknownReason: orNull(l.StatusUnknown), Resolved: l.Resolved(v.resolved),
	}
}
