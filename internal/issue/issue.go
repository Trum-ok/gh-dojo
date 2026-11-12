// Package issue reads an issue card — everything `show` prints and a verb
// checks — in one GraphQL request (principle 6): title, state, author,
// milestone, labels, assignees, body, the latest comments, blockers both
// ways with their statuses on the configured board, the item on that board
// with its field values, and the status history on that board.
//
//	card, err := issue.Read(ctx, client, issue.Request{
//		Repo: repo, Number: 42, Board: schema,
//		StatusField: cfg.StatusField(), StatusFieldID: statusField.ID,
//	})
//
// Lists are graph.Page values: the nodes read and GitHub's totalCount, for
// "showing 20 of 73". Node IDs (Card.ID, Item.ID) are for mutations only and
// must never be printed.
//
// What the card cannot know it says so: BoardUnknown (the issue may be on
// the board), StatusUnknown, HistoryTruncated and HistoryUntracked are not
// "no" answers.
package issue

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	// StatusFieldID is the node ID of that field on Board. The status is
	// matched by ID, so a field renamed on the board since Board was cached
	// still yields the right status; StatusField is then only the name the
	// value is looked up by first. Empty matches by StatusField alone (the
	// name a stale schema may get wrong).
	StatusFieldID string
	// Comments is how many of the latest comments to read: 0 means
	// DefaultComments; more than MaxComments reads MaxComments.
	Comments int
	// Lookup is read in the same request; its result is Card.Found. ReadAll
	// reads it once per request, and every card of that request gets it.
	Lookup Lookup
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
	// Milestone is the issue's milestone, nil when it has none.
	Milestone *Milestone

	Labels    graph.Page[string] // label names
	Assignees graph.Page[string] // logins
	// Comments are the latest ones, oldest first; TotalCount counts all.
	Comments graph.Page[Comment]
	// BlockedBy are the issues blocking this one, Blocking those it blocks,
	// open and closed alike (see UnresolvedBlockers).
	BlockedBy graph.Page[Link]
	Blocking  graph.Page[Link]

	// Item is the issue's item on Request.Board, nil when the issue is not
	// on that board or BoardUnknown (see OnBoard). An archived item is still
	// on the board (see Archived).
	Item *Item
	// BoardUnknown is set when Item is nil but the reader cannot tell that
	// the issue is not on Request.Board: the issue is an item of more
	// boards than the window read (20), or GitHub withheld some of its
	// project items or their projects (also Incomplete). Status is then
	// unknown, not unset: show "board membership unknown", and a verb must
	// not plan from "not on the board". BoardUnknownReason says why, as a
	// lowercase phrase for messages.
	BoardUnknown       bool
	BoardUnknownReason string

	// History is the status changes on Request.Board, oldest first, from the
	// issue's last 20 status changes on any board.
	//
	// HistoryTruncated says older status changes on Request.Board may
	// exist: the window is full, older events exist, and none of the events
	// read predates the item (when it does, everything older belongs to
	// other boards or to an earlier, removed item). An issue not on the
	// board counts as truncated whenever older events exist.
	//
	// GitHub records status changes (ProjectV2ItemStatusChangedEvent) for a
	// board's built-in Status field only: changes of other single-select
	// fields leave no event. When Request.StatusField names another field,
	// History is empty and HistoryUntracked is set: say "not tracked", not
	// "no changes".
	History          []StatusChange
	HistoryTruncated bool
	HistoryUntracked bool

	// Incomplete is set when GitHub withheld part of the card, e.g. a
	// project or blocker the token cannot see; the rest is accurate. Item is
	// then nil if the board item was among the hidden parts.
	Incomplete bool

	// Found is what Request.Lookup found; nil without a Lookup.
	Found *Found
}

// Closed reports whether the issue is closed.
func (c *Card) Closed() bool { return c.State == StateClosed }

// OnBoard reports whether the issue is an item of the configured board,
// archived or not (see Archived). An issue that is not needs
// addProjectV2ItemById before its status can be set. False is not "not on
// the board" when BoardUnknown is set.
func (c *Card) OnBoard() bool { return c.Item != nil }

// Archived reports whether the issue's item on the board is archived: on
// the board, with its status and values, but hidden from board views until
// unarchived (unarchiveProjectV2Item).
func (c *Card) Archived() bool { return c.Item != nil && c.Item.Archived }

// StatusUnknown reports whether the reader could not tell the issue's
// status on the board: BoardUnknown, or the item's Status could not be read
// (Item.StatusUnknown). Status() is "" then, but the issue may have one; a
// verb must not check `from` against it.
func (c *Card) StatusUnknown() bool {
	return c.BoardUnknown || c.Item != nil && c.Item.StatusUnknown
}

// ItemID returns the board item node ID, "" when the issue is not on the
// board. Never print it.
func (c *Card) ItemID() string {
	if c.Item == nil {
		return ""
	}
	return c.Item.ID
}

// Status returns the issue's status on the board, "" when it has none, is
// not on the board, or StatusUnknown.
func (c *Card) Status() string {
	if c.Item == nil {
		return ""
	}
	return c.Item.Status
}

// Ref names the issue: its repository and number.
func (c *Card) Ref() Ref { return Ref{Repo: c.Repo, Number: c.Number} }

// UnresolvedBlockers returns the blockers that still block: open, and not
// in one of the resolved statuses (config statuses.resolved) on the board,
// or with a status that could not be read (Link.StatusUnknown). Blocker
// checks use this, never State alone: an open blocker in a resolved status
// does not block. When BlockedBy.Truncated(), more may exist.
func (c *Card) UnresolvedBlockers(resolved []string) []Link {
	var out []Link
	for _, l := range c.BlockedBy.Nodes {
		if !l.Resolved(resolved) {
			out = append(out, l)
		}
	}
	return out
}

// Milestone is an issue's milestone.
type Milestone struct {
	Title string
	DueOn time.Time // zero when it has no due date
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
	// OnBoard says the linked issue is an item of Request.Board, archived
	// or not. False is not "not on the board" when StatusUnknown is set.
	OnBoard  bool
	Archived bool
	// Status is the linked issue's status on Request.Board: "" when it has
	// none, is not on the board, or StatusUnknown.
	Status string
	// StatusUnknown says why the linked issue's place on the board could
	// not be read, as a lowercase phrase for messages ("on 12 projects,
	// only the first 10 read"); "" when OnBoard and Status are accurate.
	// The issue may then be on the board in any status: as a blocker it is
	// unresolved unless closed.
	StatusUnknown string
}

// Closed reports whether the linked issue is closed.
func (l Link) Closed() bool { return l.State == StateClosed }

// Resolved reports whether the linked issue, as a blocker, no longer
// blocks: it is closed, or its board status is one of resolved (config
// statuses.resolved), its work complete though the issue is still open.
func (l Link) Resolved(resolved []string) bool {
	return l.Closed() || l.StatusUnknown == "" && l.Status != "" && slices.Contains(resolved, l.Status)
}

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
	ID string
	// Archived items are still on the board; see Card.Archived.
	Archived bool
	// CreatedAt is when the issue was added to the board.
	CreatedAt time.Time
	// Status is the option name of the status field, "" when unset or
	// StatusUnknown. It is matched by Request.StatusFieldID.
	Status string
	// StatusUnknown is set when the status could not be read: GitHub
	// withheld the item's values, or the field (renamed since the schema was
	// cached) is not among the values read and ValuesTruncated.
	StatusUnknown bool
	// Values are the item's set values of single-select, iteration, text,
	// number and date fields, in GitHub's order. Unset fields and built-in
	// ones (Title, Labels, ...) are absent. ValuesTruncated says the item
	// has more values than were read (100).
	Values          []Value
	ValuesTruncated bool
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

// ValueByID returns the item's value of the board field with this node ID:
// unlike a name, an ID survives renaming the field.
func (it *Item) ValueByID(fieldID string) (Value, bool) {
	for _, v := range it.Values {
		if v.FieldID == fieldID {
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
	vars := map[string]any{
		"owner": req.Repo.Owner, "repo": req.Repo.Name, "number": req.Number,
		"comments": n, "status": req.StatusField,
	}
	query, extra := lookupQuery(req.Lookup)
	for k, v := range extra {
		vars[k] = v
	}

	var resp cardResponse
	err := q.Query(ctx, query, vars, &resp)
	var node *issueJSON
	if resp.Repository != nil {
		node = resp.Repository.IssueOrPullRequest
	}
	var hidden [][]any // paths below the issue that GitHub withheld
	switch {
	case err == nil:
	case node != nil && node.ID != "" && onlyNested(err):
		hidden = nestedPaths(err) // e.g. a project or blocker the token can't see
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
		return nil, pullRequest(req)
	}
	c := node.card(req, hidden)
	c.Incomplete = len(hidden) > 0
	if !req.Lookup.Empty() {
		if c.Found, err = req.Lookup.found(resp.Repository, resp.Viewer, resp.data); err != nil {
			return nil, render.Internal("cannot decode GitHub's answer").WithCause(err)
		}
	}
	return c, nil
}

// ListFix is the fix line for a number that is not an issue: the board
// lists open issues by status and counts the closed and final ones, so it
// leads to the right number without leaving gh dojo.
const ListFix = "gh dojo list"

func notFound(req Request) *render.Error {
	return render.NotFound("issue #%d not found in %s", req.Number, req.Repo).
		WithFix(ListFix)
}

// onlyNested reports whether every GraphQL error points below the issue
// (repository.issueOrPullRequest.<field>...), so the issue itself was read,
// or at a link of the Lookup (see OnlyLinkErrors), which only that link
// misses.
func onlyNested(err error) bool {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil || len(gqlErr.Errors) == 0 {
		return false
	}
	for _, item := range gqlErr.Errors {
		if isLinkPath(item.Path) && (item.Type == "NOT_FOUND" || item.Type == "FORBIDDEN") {
			continue
		}
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

// nestedPaths returns the error paths below the issue, without the leading
// repository.issueOrPullRequest; list indexes are ints.
func nestedPaths(err error) [][]any {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil {
		return nil
	}
	var paths [][]any
	for _, item := range gqlErr.Errors {
		if len(item.Path) < 3 || item.Path[0] != "repository" || item.Path[1] != "issueOrPullRequest" {
			continue
		}
		path := make([]any, 0, len(item.Path)-2)
		for _, p := range item.Path[2:] {
			if f, ok := p.(float64); ok {
				p = int(f)
			}
			path = append(path, p)
		}
		paths = append(paths, path)
	}
	return paths
}

// below returns the paths of hidden at or below prefix, without prefix.
func below(hidden [][]any, prefix ...any) [][]any {
	var out [][]any
	for _, path := range hidden {
		if withheld([][]any{path}, prefix...) {
			out = append(out, path[len(prefix):])
		}
	}
	return out
}

// withheld reports whether GitHub withheld something at or below prefix.
func withheld(hidden [][]any, prefix ...any) bool {
	for _, path := range hidden {
		if len(path) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if path[i] != p {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func (n *issueJSON) card(req Request, hidden [][]any) *Card {
	repo := req.Repo
	if m := urlRefRE.FindStringSubmatch(n.URL); m != nil {
		repo = canonical(repo, m[2]) // as GitHub spells it
	}
	c := &Card{
		ID:          n.ID,
		Repo:        repo,
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
	if m := n.Milestone; m != nil {
		c.Milestone = &Milestone{Title: m.Title, DueOn: parseTime(deref(m.DueOn))}
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
	renamed := false // the status field's name now finds another field, or none
	if req.Board != nil {
		c.Item, c.BoardUnknownReason, renamed = n.boardItem(req, hidden)
		c.BoardUnknown = c.BoardUnknownReason != ""
	}
	c.BlockedBy = links(n.BlockedBy, req, below(hidden, "blockedBy"), renamed)
	c.Blocking = links(n.Blocking, req, below(hidden, "blocking"), renamed)

	if req.Board == nil {
		return c
	}
	if req.StatusField != builtinStatus {
		c.HistoryUntracked = true
		return c
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
	c.HistoryTruncated = n.historyTruncated(c.Item)
	return c
}

// builtinStatus is the name of a board's built-in status field, the only
// one GitHub records ProjectV2ItemStatusChangedEvent for.
const builtinStatus = "Status"

// historyTruncated reports whether status changes of the board may precede
// the timeline window: older events exist (of any board) and the oldest
// event read is not older than the item, so older ones may be the item's.
func (n *issueJSON) historyTruncated(item *Item) bool {
	tl := n.TimelineItems
	if tl.TotalCount <= len(tl.Nodes) {
		return false
	}
	if item == nil || item.CreatedAt.IsZero() {
		return true
	}
	for _, ev := range tl.Nodes { // oldest first
		if ev == nil {
			continue
		}
		at := parseTime(ev.CreatedAt)
		return at.IsZero() || !at.Before(item.CreatedAt)
	}
	return true
}

// boardItem finds the issue's item on req.Board. When there is none it
// returns why the issue may still be on the board, "" when it is not.
// renamed reports that the status field's name (req.StatusField) no
// longer finds the status field by its ID (see itemJSON.status).
func (n *issueJSON) boardItem(req Request, hidden [][]any) (item *Item, unknown string, renamed bool) {
	items := n.ProjectItems
	unseen := false // an item whose project GitHub withheld
	for i, it := range items.Nodes {
		if it == nil || it.Project == nil {
			unseen = true
			continue
		}
		if it.Project.ID != req.Board.ID {
			continue
		}
		item = &Item{ID: it.ID, Archived: it.IsArchived, CreatedAt: parseTime(it.CreatedAt)}
		for _, v := range it.FieldValues.Nodes {
			if val, ok := v.value(); ok {
				item.Values = append(item.Values, val)
			}
		}
		item.ValuesTruncated = it.FieldValues.TotalCount > len(it.FieldValues.Nodes)
		item.Status, item.StatusUnknown, renamed = it.status(req, item, withheld(hidden, "projectItems", "nodes", i))
		return item, "", renamed
	}
	switch {
	case unseen || items.Nodes == nil && withheld(hidden, "projectItems"):
		return nil, "GitHub withheld some of the issue's projects from this token", false
	case items.TotalCount > len(items.Nodes):
		return nil, fmt.Sprintf("the issue is on %d projects and only the first %d were read", items.TotalCount, len(items.Nodes)), false
	}
	return nil, "", false
}

// status returns the item's status option name, or unknown when it cannot
// be read. The value looked up by name counts only if its field is the
// configured one (by ID); otherwise the field may have been renamed, and
// its ID is looked up among the values read. renamed reports that the
// lookup by name found another field, or nothing while the lookup by ID
// found the status: the name no longer finds the status field.
func (it *itemJSON) status(req Request, item *Item, hidden bool) (status string, unknown, renamed bool) {
	match := func(v Value) bool {
		if v.Type != board.TypeSingleSelect {
			return false
		}
		if req.StatusFieldID != "" {
			return v.FieldID == req.StatusFieldID
		}
		return v.Field == req.StatusField
	}
	v, byName := it.Status.value()
	if byName && match(v) {
		return v.Text, false, false
	}
	renamed = byName && req.StatusFieldID != "" // the name found another field
	for _, v := range item.Values {
		if match(v) {
			return v.Text, false, true
		}
	}
	if hidden || item.ValuesTruncated && req.StatusFieldID != "" {
		return "", true, renamed
	}
	return "", false, renamed
}

// links converts linked issues. hidden are the withheld paths below the
// list (nodes.<i>...); renamed is boardItem's: the status field's name no
// longer finds the status field.
func links(in conn[*linkJSON], req Request, hidden [][]any, renamed bool) graph.Page[Link] {
	out := graph.Page[Link]{TotalCount: in.TotalCount}
	for i, l := range in.Nodes {
		if l == nil {
			continue // hidden from the token
		}
		link := Link{Number: l.Number, Title: l.Title, State: l.State}
		if l.Repository != nil {
			link.Repo, _ = ParseRepo(l.Repository.NameWithOwner)
		}
		l.board(req, &link, below(hidden, "nodes", i, "projectItems"), renamed)
		out.Nodes = append(out.Nodes, link)
	}
	return out
}

// Reasons a linked issue's status is unknown (Link.StatusUnknown).
const (
	linkNoBoard  = "the board was not read"
	linkWithheld = "GitHub withheld its projects from the token"
	linkRenamed  = "the board's status field was renamed since it was cached"
)

// board fills the linked issue's place on req.Board: OnBoard, Archived,
// Status, or StatusUnknown when it cannot be told. hidden are the withheld
// paths below its projectItems. As for the card, a value whose field is
// not the configured status field (by ID, when known) does not count.
func (l *linkJSON) board(req Request, link *Link, hidden [][]any, renamed bool) {
	if req.Board == nil {
		link.StatusUnknown = linkNoBoard
		return
	}
	items := l.ProjectItems
	unseen := items.Nodes == nil && len(hidden) > 0 // the whole list withheld
	for j, it := range items.Nodes {
		if it == nil || it.Project == nil {
			unseen = true
			continue
		}
		if it.Project.ID != req.Board.ID {
			continue
		}
		link.OnBoard, link.Archived = true, it.IsArchived
		switch st := it.Status; {
		case st == nil && withheld(hidden, "nodes", j):
			link.StatusUnknown = linkWithheld
		case st == nil && renamed:
			link.StatusUnknown = linkRenamed
		case st == nil:
			// on the board without a status
		case req.StatusFieldID != "" && (st.Field == nil || st.Field.ID != req.StatusFieldID):
			link.StatusUnknown = linkRenamed
		default:
			link.Status = st.Name
		}
		return
	}
	switch {
	case unseen:
		link.StatusUnknown = linkWithheld
	case items.TotalCount > len(items.Nodes):
		link.StatusUnknown = fmt.Sprintf("on %d projects, only the first %d read", items.TotalCount, len(items.Nodes))
	}
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
