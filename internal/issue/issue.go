// Package issue reads an issue card — everything `show` prints and a verb
// checks — in one GraphQL request (principle 6): title, state, author,
// labels, assignees, body, the latest comments, blockers both ways, the
// item on the configured board with its field values, and the status
// history on that board.
//
//	card, err := issue.Read(ctx, client, issue.Request{
//		Repo: repo, Number: 42, Board: schema, StatusField: cfg.StatusField(),
//	})
//
// Lists are graph.Page values: the nodes read and GitHub's totalCount, for
// "showing 20 of 73". Node IDs (Card.ID, Item.ID) are for mutations only and
// must never be printed.
package issue

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

const (
	// DefaultComments is how many of the latest comments Read fetches when
	// Request.Comments is 0. Verbs look for their markers in this window.
	DefaultComments = 20
	// MaxComments is the most comments one request can fetch (GitHub's page
	// size limit). Longer threads need pagination, which Read does not do.
	MaxComments = 100
)

// Issue states as GitHub spells them.
const (
	StateOpen   = "OPEN"
	StateClosed = "CLOSED"
)

// Repo is a GitHub repository.
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

var repoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9._-]+$`)

// ParseRepo parses "owner/name". A malformed value is a usage error.
func ParseRepo(s string) (Repo, error) {
	if !repoRE.MatchString(s) || strings.HasSuffix(s, ".git") {
		return Repo{}, render.Usage("invalid repository %q: want owner/name", s)
	}
	owner, name, _ := strings.Cut(s, "/")
	return Repo{Owner: owner, Name: name}, nil
}

// Request says which card to read.
type Request struct {
	Repo   Repo
	Number int
	// Board is the configured board; its item becomes Card.Item and its
	// status changes Card.History. Nil reads the card without board data.
	Board *board.Schema
	// StatusField is the board name of the status field
	// (config.StatusField()); it fills Item.Status.
	StatusField string
	// Comments is how many of the latest comments to read: 0 means
	// DefaultComments; more than MaxComments reads MaxComments.
	Comments int
}

// Card is an issue with everything gh-dojo shows and checks.
type Card struct {
	// ID is the issue node ID, for mutations (addComment,
	// addProjectV2ItemById). Never print it.
	ID     string
	Repo   Repo
	Number int
	Title  string
	State  string // StateOpen or StateClosed
	// StateReason is COMPLETED, NOT_PLANNED, DUPLICATE or REOPENED; "" for
	// an issue never closed.
	StateReason string
	URL         string
	Author      string // login; "" for a deleted account
	CreatedAt   time.Time
	ClosedAt    time.Time // zero while open
	Body        string

	Labels    graph.Page[string] // label names
	Assignees graph.Page[string] // logins
	// Comments are the latest ones, oldest first; TotalCount counts all.
	Comments graph.Page[Comment]
	// BlockedBy are the issues blocking this one, Blocking those it blocks,
	// open and closed alike (see OpenBlockers).
	BlockedBy graph.Page[Link]
	Blocking  graph.Page[Link]

	// Item is the issue's item on Request.Board, nil when the issue is not
	// on that board (see OnBoard).
	Item *Item
	// History is the status changes on Request.Board, oldest first, from the
	// issue's last 20 status changes on any board; HistoryTruncated says
	// older ones exist.
	History          []StatusChange
	HistoryTruncated bool

	// Incomplete is set when GitHub withheld part of the card, e.g. a
	// project or blocker the token cannot see; the rest is accurate. Item is
	// then nil if the board item was among the hidden parts.
	Incomplete bool
}

// Closed reports whether the issue is closed.
func (c *Card) Closed() bool { return c.State == StateClosed }

// OnBoard reports whether the issue is an item of the configured board. An
// issue that is not needs addProjectV2ItemById before its status can be set.
func (c *Card) OnBoard() bool { return c.Item != nil }

// ItemID returns the board item node ID, "" when the issue is not on the
// board. Never print it.
func (c *Card) ItemID() string {
	if c.Item == nil {
		return ""
	}
	return c.Item.ID
}

// Status returns the issue's status on the board, "" when it has none or is
// not on the board.
func (c *Card) Status() string {
	if c.Item == nil {
		return ""
	}
	return c.Item.Status
}

// OpenBlockers returns the open issues among BlockedBy. When
// BlockedBy.Truncated(), more may exist.
func (c *Card) OpenBlockers() []Link {
	var open []Link
	for _, l := range c.BlockedBy.Nodes {
		if !l.Closed() {
			open = append(open, l)
		}
	}
	return open
}

// Comment is an issue comment.
type Comment struct {
	ID        string // node ID, for updateIssueComment; never print it
	Author    string // login; "" for a deleted account
	CreatedAt time.Time
	// ViewerDidAuthor is true for comments written with the current token:
	// the only ones whose gh-dojo markers a verb should trust.
	ViewerDidAuthor bool
	Body            string
}

// Link is an issue that blocks or is blocked by the card.
type Link struct {
	Repo   Repo
	Number int
	Title  string
	State  string // StateOpen or StateClosed
}

// Closed reports whether the linked issue is closed.
func (l Link) Closed() bool { return l.State == StateClosed }

// Ref returns "#N" for an issue in repo, else "owner/name#N".
func (l Link) Ref(repo Repo) string {
	if l.Repo == repo || l.Repo == (Repo{}) {
		return "#" + strconv.Itoa(l.Number)
	}
	return l.Repo.String() + "#" + strconv.Itoa(l.Number)
}

// Item is the issue's item on the configured board.
type Item struct {
	// ID is the project item node ID, for updateProjectV2ItemFieldValue.
	// Never print it.
	ID       string
	Archived bool
	// Status is the option name of the status field, "" when unset.
	Status string
	// Values are the item's set values of single-select, iteration, text,
	// number and date fields, in GitHub's order. Unset fields and built-in
	// ones (Title, Labels, ...) are absent.
	Values []Value
}

// Value returns the item's value of the board field with this name.
func (it *Item) Value(field string) (Value, bool) {
	for _, v := range it.Values {
		if v.Field == field {
			return v, true
		}
	}
	return Value{}, false
}

// Value is a field value of a board item.
type Value struct {
	Field   string // board field name
	FieldID string // never print it
	Type    board.FieldType
	// Text is the value as shown: the option name, iteration title, text,
	// number, or date (YYYY-MM-DD).
	Text        string
	OptionID    string // single-select option ID
	IterationID string // iteration ID
	StartDate   string // iteration start, YYYY-MM-DD
	Duration    int    // iteration length in days
}

// StatusChange is one status change of the board item.
type StatusChange struct {
	At        time.Time
	From      string // "" when the item had no status
	To        string // "" when the status was cleared
	Actor     string // login; "" for a deleted account
	Automated bool   // set by a project workflow, not a person
}

// Read fetches the card in one GraphQL request.
//
// Errors are *render.Error: a missing repository or issue, or a pull
// request number, is exit code 3 with the repo in the message; network and
// auth problems keep graph's codes.
func Read(ctx context.Context, q graph.Querier, req Request) (*Card, error) {
	if req.Number <= 0 {
		return nil, render.Usage("invalid issue number %d: want 1 or greater", req.Number)
	}
	if req.Repo.Owner == "" || req.Repo.Name == "" {
		return nil, render.Internal("issue.Read: no repository")
	}
	n := req.Comments
	if n <= 0 {
		n = DefaultComments
	}
	n = min(n, MaxComments)
	vars := map[string]any{"owner": req.Repo.Owner, "repo": req.Repo.Name, "number": req.Number, "comments": n}

	var resp cardResponse
	err := q.Query(ctx, cardQuery, vars, &resp)
	var node *issueJSON
	if resp.Repository != nil {
		node = resp.Repository.IssueOrPullRequest
	}
	incomplete := false
	switch {
	case err == nil:
	case node != nil && node.ID != "" && onlyNested(err):
		incomplete = true // e.g. a project or blocker the token can't see
	case render.FromError(err).Code == render.ExitNotFound && resp.Repository == nil:
		return nil, render.NotFound("repository %s not found, or the token cannot see it", req.Repo).
			WithFix("gh repo view " + req.Repo.String()).WithCause(err)
	case render.FromError(err).Code == render.ExitNotFound:
		return nil, notFound(req).WithCause(err)
	default:
		return nil, err
	}
	switch {
	case node == nil:
		return nil, notFound(req)
	case node.Typename != "Issue":
		return nil, render.NotFound("#%d in %s is a pull request, not an issue", req.Number, req.Repo).
			WithFix(fmt.Sprintf("gh pr view %d -R %s", req.Number, req.Repo))
	}
	c := node.card(req)
	c.Incomplete = incomplete
	return c, nil
}

func notFound(req Request) *render.Error {
	return render.NotFound("issue #%d not found in %s", req.Number, req.Repo).
		WithFix("gh issue list -R " + req.Repo.String())
}

// onlyNested reports whether every GraphQL error points below the issue
// (repository.issueOrPullRequest.<field>...), so the issue itself was read.
func onlyNested(err error) bool {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil || len(gqlErr.Errors) == 0 {
		return false
	}
	for _, item := range gqlErr.Errors {
		if len(item.Path) < 3 || item.Path[0] != "repository" || item.Path[1] != "issueOrPullRequest" {
			return false
		}
		switch item.Type {
		case "NOT_FOUND", "FORBIDDEN":
		default:
			return false
		}
	}
	return true
}

func (n *issueJSON) card(req Request) *Card {
	c := &Card{
		ID:          n.ID,
		Repo:        req.Repo,
		Number:      n.Number,
		Title:       n.Title,
		State:       n.State,
		StateReason: deref(n.StateReason),
		URL:         n.URL,
		Author:      loginOf(n.Author),
		CreatedAt:   parseTime(n.CreatedAt),
		ClosedAt:    parseTime(deref(n.ClosedAt)),
		Body:        n.Body,
	}
	c.Labels.TotalCount = n.Labels.TotalCount
	for _, l := range n.Labels.Nodes {
		c.Labels.Nodes = append(c.Labels.Nodes, l.Name)
	}
	c.Assignees.TotalCount = n.Assignees.TotalCount
	for _, a := range n.Assignees.Nodes {
		c.Assignees.Nodes = append(c.Assignees.Nodes, a.Login)
	}
	c.Comments.TotalCount = n.Comments.TotalCount
	for _, cm := range n.Comments.Nodes {
		c.Comments.Nodes = append(c.Comments.Nodes, Comment{
			ID:              cm.ID,
			Author:          loginOf(cm.Author),
			CreatedAt:       parseTime(cm.CreatedAt),
			ViewerDidAuthor: cm.ViewerDidAuthor,
			Body:            cm.Body,
		})
	}
	c.BlockedBy = links(n.BlockedBy)
	c.Blocking = links(n.Blocking)

	if req.Board == nil {
		return c
	}
	for _, it := range n.ProjectItems.Nodes {
		if it == nil || it.Project == nil || it.Project.ID != req.Board.ID {
			continue
		}
		item := &Item{ID: it.ID, Archived: it.IsArchived}
		for _, v := range it.FieldValues.Nodes {
			if val, ok := v.value(); ok {
				item.Values = append(item.Values, val)
			}
		}
		if v, ok := item.Value(req.StatusField); ok && v.Type == board.TypeSingleSelect {
			item.Status = v.Text
		}
		c.Item = item
		break
	}
	for _, ev := range n.TimelineItems.Nodes {
		if ev == nil || ev.Project == nil || ev.Project.ID != req.Board.ID {
			continue
		}
		c.History = append(c.History, StatusChange{
			At:        parseTime(ev.CreatedAt),
			From:      ev.PreviousStatus,
			To:        ev.Status,
			Actor:     loginOf(ev.Actor),
			Automated: ev.WasAutomated,
		})
	}
	c.HistoryTruncated = n.TimelineItems.TotalCount > len(n.TimelineItems.Nodes)
	return c
}

func links(in conn[*linkJSON]) graph.Page[Link] {
	out := graph.Page[Link]{TotalCount: in.TotalCount}
	for _, l := range in.Nodes {
		if l == nil {
			continue // hidden from the token
		}
		link := Link{Number: l.Number, Title: l.Title, State: l.State}
		if l.Repository != nil {
			link.Repo, _ = ParseRepo(l.Repository.NameWithOwner)
		}
		out.Nodes = append(out.Nodes, link)
	}
	return out
}

func (v *valueJSON) value() (Value, bool) {
	if v == nil || v.Field == nil || v.Field.Name == "" {
		return Value{}, false
	}
	val := Value{Field: v.Field.Name, FieldID: v.Field.ID, Type: board.FieldType(v.Field.DataType)}
	if !val.Type.Writable() {
		return Value{}, false // the Title field: a text value the card has already
	}
	switch v.Typename {
	case "ProjectV2ItemFieldSingleSelectValue":
		val.Text, val.OptionID = v.Name, v.OptionID
	case "ProjectV2ItemFieldIterationValue":
		val.Text, val.IterationID, val.StartDate, val.Duration = v.Title, v.IterationID, v.StartDate, v.Duration
	case "ProjectV2ItemFieldTextValue":
		val.Text = v.Text
	case "ProjectV2ItemFieldNumberValue":
		if v.Number == nil {
			return Value{}, false
		}
		val.Text = strconv.FormatFloat(*v.Number, 'f', -1, 64)
	case "ProjectV2ItemFieldDateValue":
		val.Text = v.Date
	default:
		return Value{}, false
	}
	return val, true
}

func loginOf(l *login) string {
	if l == nil {
		return ""
	}
	return l.Login
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
