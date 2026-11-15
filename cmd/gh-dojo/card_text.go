package main

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Text of issue cards for show: short, labelled lines an agent reads in one
// pass, with every cut saying what is hidden (principle 7). Node IDs are
// never printed.

const (
	// Unless --full, a description is cut at maxBodyRunes and the comments
	// shown share commentBudgetRunes: each gets an equal part, but at least
	// minCommentRunes and at most maxCommentRunes. With the default 5
	// comments that keeps a card near 2k tokens even in Cyrillic (about 2
	// characters a token).
	maxBodyRunes       = 2000
	commentBudgetRunes = 1500
	minCommentRunes    = 200
	maxCommentRunes    = 600
	// maxLinkTitleRunes cuts titles of linked issues.
	maxLinkTitleRunes = 80
	// maxBriefRefs is how many unresolved blockers --brief names.
	maxBriefRefs = 5
)

// projectAutomation are logins of GitHub's own project automation. The
// event's wasAutomated flag is unreliable (false for workflow changes), so
// the actor tells them apart too.
var projectAutomation = map[string]bool{"github-project-automation": true}

// cardMarkerRE matches the hidden marker gh-dojo ends its own comments and
// descriptions with: the grammar workflow's dedup looks for, on its own
// line after a blank one (or alone), at the very end. A marker quoted in
// text or left unclosed does not match.
var cardMarkerRE = regexp.MustCompile(`(?:^|\n\n)<!-- gh-dojo:[a-z][a-z0-9-]*:[0-9a-f]{8} -->[ \t\n]*$`)

// fenceRE matches a line that opens or closes a fenced code block.
var fenceRE = regexp.MustCompile("(?m)^ {0,3}(```|~~~)")

// bidiControls drops the Unicode embedding, override and isolate controls
// (U+202A–202E, U+2066–2069), which make text read other than it is.
var bidiControls = strings.NewReplacer(
	"\u202a", "", "\u202b", "", "\u202c", "", "\u202d", "", "\u202e", "",
	"\u2066", "", "\u2067", "", "\u2068", "", "\u2069", "",
)

// cardView renders cards with show's flags.
type cardView struct {
	statusField string // board name of the status field, left out of the field list
	// sprintField is the board name of the sprint field (fields.sprint),
	// shown on a line of its own; "" when not set. sprintFieldID is its
	// node ID, which finds the value even if the field was renamed.
	sprintField, sprintFieldID string
	resolved                   []string // statuses.resolved: an open blocker in these no longer blocks
	// def is the default repository: an issue outside it, the card's own
	// or a linked one, is named owner/repo#N.
	def      issue.Repo
	comments int // how many of the latest comments to print
	// bodyRunes cuts the description and commentBudget is shared by the
	// comments shown (see commentRunes); 0 prints them whole.
	bodyRunes, commentBudget int
	// full prints the status history a change per line and session links
	// whole (--full).
	full bool
	// sessionRE matches a session link of [session] link in a comment, its
	// ID the first group; nil when the link is the bare ID.
	sessionRE *regexp.Regexp
}

// sessionLinkRE matches links made from the session link template (with
// {id}), nil for the bare "{id}", which has nothing to tell an ID by.
func sessionLinkRE(link string) *regexp.Regexp {
	before, after, ok := strings.Cut(link, "{id}")
	if !ok || before == "" && after == "" {
		return nil
	}
	return regexp.MustCompile(regexp.QuoteMeta(before) + "([^\\s<>()\\[\\]`'\"]+)" + regexp.QuoteMeta(after))
}

// sessionTail is how many last characters of a session ID a shortened
// session link keeps.
const sessionTail = 8

// shortSessions shortens the session links in s to "session …<tail>":
// an agent reading the card needs no link to another agent's session, and
// an ID costs many tokens. --full keeps them.
func (v cardView) shortSessions(s string) string {
	if v.full || v.sessionRE == nil {
		return s
	}
	return v.sessionRE.ReplaceAllStringFunc(s, func(m string) string {
		id := []rune(v.sessionRE.FindStringSubmatch(m)[1])
		return "…" + string(id[max(0, len(id)-sessionTail):])
	})
}

// card renders the full card.
func (v cardView) card(c *issue.Card) string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	line("%s %s", c.Ref().String(v.def), singleLine(c.Title))
	line("state: %s, opened %s by %s", cardState(c), cardDay(c.CreatedAt), cardLogin(c.Author))
	line("board: %s", v.board(c, true))
	if v.sprintField != "" {
		line("sprint: %s", v.sprint(c))
	}
	// A line that would say "none" is left out.
	some := func(label, value string) {
		if value != "none" {
			line("%s: %s", label, value)
		}
	}
	some("milestone", cardMilestone(c.Milestone))
	some("labels", cardNames(c.Labels))
	some("assignees", cardNames(c.Assignees))
	v.writeBlockers(&b, c)
	writeBlocking(&b, c, v.def)
	some("parent", v.parent(c))
	v.writeSubIssues(&b, c)
	if c.OnBoard() || c.BoardUnknown && len(c.History) > 0 {
		writeHistory(&b, c, v.full)
	}
	if c.Incomplete {
		line("note: GitHub withheld part of this issue from the token (a project or linked issue it cannot see); the rest is accurate")
	}

	b.WriteString("\n")
	if body := cleanText(c.Body, true); body == "" {
		line("description: none")
	} else {
		line("description:")
		text, note := fold(body, v.bodyRunes)
		line("%s", indent(text))
		if note != "" {
			line("  %s", note)
		}
	}

	b.WriteString("\n")
	v.writeComments(&b, c)
	return b.String()
}

// brief renders three lines: title; state, status, milestone, labels,
// assignees; blockers and counts. A part that would say "none" is left out.
// A fourth line notes a partly hidden card.
func (v cardView) brief(c *issue.Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", c.Ref().String(v.def), singleLine(c.Title))
	fmt.Fprintf(&b, "%s · %s", cardState(c), v.board(c, false))
	if v.sprintField != "" {
		fmt.Fprintf(&b, " · sprint: %s", v.sprint(c))
	}
	if c.Milestone != nil {
		fmt.Fprintf(&b, " · milestone: %s", cardMilestone(c.Milestone))
	}
	if names := cardNames(c.Labels); names != "none" {
		fmt.Fprintf(&b, " · labels: %s", names)
	}
	if names := cardNames(c.Assignees); names != "none" {
		fmt.Fprintf(&b, " · assignees: %s", names)
	}
	b.WriteString("\n")

	var blocked string
	unresolved := c.UnresolvedBlockers(v.resolved)
	resolved := len(c.BlockedBy.Nodes) - len(unresolved)
	switch {
	case len(unresolved) > 0:
		refs := make([]string, 0, maxBriefRefs)
		for _, l := range unresolved[:min(len(unresolved), maxBriefRefs)] {
			refs = append(refs, linkRef(c, l, v.def))
		}
		blocked = fmt.Sprintf("blocked by %d unresolved (%s", len(unresolved), strings.Join(refs, ", "))
		if n := len(unresolved) - len(refs); n > 0 {
			blocked += fmt.Sprintf(" and %d more", n)
		}
		blocked += ")"
		if resolved > 0 {
			blocked += fmt.Sprintf("; %d resolved", resolved)
		}
	case resolved > 0:
		blocked = fmt.Sprintf("blocked by none unresolved (%d resolved)", resolved)
	}
	if note := render.Shown(len(c.BlockedBy.Nodes), c.BlockedBy.TotalCount, ""); note != "" {
		blocked = cmp.Or(blocked, "blocked by none read") + " (" + note + " blockers)"
	}
	var blocking string
	if c.Blocking.TotalCount > 0 || len(c.Blocking.Nodes) > 0 {
		closed := 0
		for _, l := range c.Blocking.Nodes {
			if l.Closed() {
				closed++
			}
		}
		blocking = fmt.Sprintf("blocking %d open", len(c.Blocking.Nodes)-closed)
		if closed > 0 {
			blocking += fmt.Sprintf(" (%d closed)", closed)
		}
		if note := render.Shown(len(c.Blocking.Nodes), c.Blocking.TotalCount, ""); note != "" {
			blocking += " (" + note + ")"
		}
	}
	var parts []string
	for _, p := range []string{blocked, blocking, strings.TrimPrefix(v.briefHierarchy(c), " · ")} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	parts = append(parts, fmt.Sprintf("comments: %d", c.Comments.TotalCount))
	fmt.Fprintf(&b, "%s\n", strings.Join(parts, " · "))
	if c.Incomplete {
		b.WriteString("note: GitHub withheld part of this issue from the token; the rest is accurate\n")
	}
	return b.String()
}

// briefHierarchy is the parent and the sub-issues for --brief, each part
// only when the issue has it, starting with " · ":
//
//	· parent: #12 · sub-issues: 3 of 5 closed, 1 unresolved (#14)
func (v cardView) briefHierarchy(c *issue.Card) string {
	var s string
	switch {
	case c.Parent != nil:
		s += " · parent: " + linkRef(c, *c.Parent, v.def)
	case c.ParentHidden:
		s += " · parent: not readable"
	}
	if len(c.SubIssues.Nodes) == 0 && c.SubIssues.TotalCount == 0 {
		return s
	}
	unresolved := c.UnresolvedSubIssues(v.resolved)
	parts := subIssueCounts(c, len(unresolved))
	if len(unresolved) > 0 {
		refs := make([]string, 0, maxBriefRefs)
		for _, l := range unresolved[:min(len(unresolved), maxBriefRefs)] {
			refs = append(refs, linkRef(c, l, v.def))
		}
		if n := len(unresolved) - len(refs); n > 0 {
			refs = append(refs, fmt.Sprintf("%d more", n))
		}
		parts[len(parts)-1] += " (" + strings.Join(refs, ", ") + ")"
	}
	if note := render.Shown(len(c.SubIssues.Nodes), c.SubIssues.TotalCount, ""); note != "" {
		parts = append(parts, note)
	}
	return s + " · sub-issues: " + strings.Join(parts, ", ")
}

// board describes the issue's place on the board: status, other field
// values if fields, archived.
func (v cardView) board(c *issue.Card, fields bool) string {
	switch {
	case c.BoardUnknown && fields: // the full card's "board:" line
		return "unknown, may be on it (" + c.BoardUnknownReason + ")"
	case c.BoardUnknown:
		return "board status unknown (" + c.BoardUnknownReason + ")"
	case !c.OnBoard():
		return "not on the board (a verb adds it)"
	}
	parts := []string{"no status"}
	switch {
	case c.Item.StatusUnknown:
		parts[0] = "status unknown (GitHub did not return it)"
	case c.Item.Status != "":
		parts[0] = c.Item.Status
	}
	if fields {
		for _, val := range c.Item.Values {
			if val.Field != v.statusField && !v.isSprint(val) {
				parts = append(parts, singleLine(val.Field)+": "+singleLine(val.Text))
			}
		}
		if c.Item.ValuesTruncated {
			parts = append(parts, "more fields not read")
		}
	}
	s := strings.Join(parts, " · ")
	if c.Archived() {
		s += " (archived)"
	}
	return s
}

func cardState(c *issue.Card) string {
	reason := strings.ToLower(strings.ReplaceAll(c.StateReason, "_", " "))
	if !c.Closed() {
		if reason == "reopened" {
			return "open (reopened)"
		}
		return "open"
	}
	s := "closed"
	if reason != "" {
		s += " (" + reason + ")"
	}
	if !c.ClosedAt.IsZero() {
		s += " " + cardDay(c.ClosedAt)
	}
	return s
}

// cardNames joins names, "none" for no names, noting a cut list.
func cardNames(p graph.Page[string]) string {
	if len(p.Nodes) == 0 && p.TotalCount == 0 {
		return "none"
	}
	names := make([]string, len(p.Nodes))
	for i, n := range p.Nodes {
		names[i] = singleLine(n)
	}
	s := strings.Join(names, ", ")
	if note := render.Shown(len(p.Nodes), p.TotalCount, ""); note != "" {
		s += " (" + note + ")"
	}
	return s
}

// cardMilestone names a milestone with its due date, "none" for nil.
func cardMilestone(m *issue.Milestone) string {
	if m == nil {
		return "none"
	}
	s := singleLine(m.Title)
	if !m.DueOn.IsZero() {
		s += " (due " + cardDay(m.DueOn) + ")"
	}
	return s
}

// writeBlockers writes the issues blocking this one, one per line:
// unresolved first, then resolved ones still open (their board status is
// in statuses.resolved), then closed ones. Only unresolved ones block.
func (v cardView) writeBlockers(b *strings.Builder, c *issue.Card) {
	var unresolved, done, closed []issue.Link
	for _, l := range c.BlockedBy.Nodes {
		switch {
		case l.Closed():
			closed = append(closed, l)
		case l.Resolved(v.resolved):
			done = append(done, l)
		default:
			unresolved = append(unresolved, l)
		}
	}
	var counts []string
	switch n := len(done) + len(closed); {
	case len(unresolved) > 0 && n > 0:
		counts = append(counts, fmt.Sprintf("%d unresolved, %d resolved", len(unresolved), n))
	case len(unresolved) > 0:
		counts = append(counts, fmt.Sprintf("%d unresolved", len(unresolved)))
	case n > 0:
		counts = append(counts, fmt.Sprintf("none unresolved (%d resolved)", n))
	}
	writeLinks(b, "blocked by", c.BlockedBy, counts)
	for _, l := range unresolved {
		writeLink(b, l, linkRef(c, l, v.def), linkPlace(l))
	}
	for _, l := range done {
		writeLink(b, l, linkRef(c, l, v.def), linkPlace(l)+", resolved")
	}
	for _, l := range closed {
		writeLink(b, l, linkRef(c, l, v.def), "closed")
	}
}

// writeBlocking writes the issues this one blocks, one per line, open ones
// first, named relative to def.
func writeBlocking(b *strings.Builder, c *issue.Card, def issue.Repo) {
	var open, closed []issue.Link
	for _, l := range c.Blocking.Nodes {
		if l.Closed() {
			closed = append(closed, l)
		} else {
			open = append(open, l)
		}
	}
	var counts []string
	if len(open) > 0 {
		counts = append(counts, fmt.Sprintf("%d open", len(open)))
	}
	if len(closed) > 0 {
		counts = append(counts, fmt.Sprintf("%d closed", len(closed)))
	}
	writeLinks(b, "blocking", c.Blocking, counts)
	for _, l := range open {
		writeLink(b, l, linkRef(c, l, def), linkPlace(l))
	}
	for _, l := range closed {
		writeLink(b, l, linkRef(c, l, def), "closed")
	}
}

// parent names the issue's parent with where it stands, "none" for none,
// "not readable" for one GitHub withheld from the token.
func (v cardView) parent(c *issue.Card) string {
	l := c.Parent
	switch {
	case l == nil && c.ParentHidden:
		return "not readable (GitHub withheld it from the token)"
	case l == nil:
		return "none"
	}
	about := "closed"
	if !l.Closed() {
		about = linkPlace(*l)
	}
	return linkRef(c, *l, v.def) + " " + clipRunes(singleLine(l.Title), maxLinkTitleRunes) + " (" + singleLine(about) + ")"
}

// writeSubIssues writes the issue's sub-issues, one per line, as
// writeBlockers writes blockers: unresolved first, then resolved ones
// still open, then closed ones. The header counts the closed ones and the
// unresolved ones, which a verb with sub_issues = "warn" or "deny" checks.
func (v cardView) writeSubIssues(b *strings.Builder, c *issue.Card) {
	var unresolved, done, closed []issue.Link
	for _, l := range c.SubIssues.Nodes {
		switch {
		case l.Closed():
			closed = append(closed, l)
		case l.Resolved(v.resolved):
			done = append(done, l)
		default:
			unresolved = append(unresolved, l)
		}
	}
	writeLinks(b, "sub-issues", c.SubIssues, subIssueCounts(c, len(unresolved)))
	for _, l := range unresolved {
		writeLink(b, l, linkRef(c, l, v.def), linkPlace(l))
	}
	for _, l := range done {
		writeLink(b, l, linkRef(c, l, v.def), linkPlace(l)+", resolved")
	}
	for _, l := range closed {
		writeLink(b, l, linkRef(c, l, v.def), "closed")
	}
}

// subIssueCounts says how many sub-issues are closed, of all of them
// ("17 of 55 closed", GitHub's count even when only 20 were read), and
// how many of those read are unresolved when any is open: "3 of 5
// closed, 1 unresolved", "17 of 55 closed, none unresolved of those
// read". None for an issue without sub-issues.
func subIssueCounts(c *issue.Card, unresolved int) []string {
	read, total := len(c.SubIssues.Nodes), max(c.SubIssues.TotalCount, len(c.SubIssues.Nodes))
	if total == 0 {
		return nil
	}
	counts := []string{fmt.Sprintf("%d of %d closed", c.SubIssuesClosed, total)}
	of := ""
	if read < total {
		of = " of those read"
	}
	switch {
	case unresolved > 0:
		counts = append(counts, fmt.Sprintf("%d unresolved%s", unresolved, of))
	case c.SubIssuesClosed < total:
		counts = append(counts, "none unresolved"+of)
	}
	return counts
}

// linkPlace says where an open linked issue stands on the board: its
// status, "on the board, no status", "not on the board", or why its status
// was not read; ", archived" for an archived item.
func linkPlace(l issue.Link) string {
	var s string
	switch {
	case l.StatusUnknown != "":
		s = "status not read: " + l.StatusUnknown
	case !l.OnBoard:
		return "not on the board"
	case l.Status == "":
		s = "on the board, no status"
	default:
		s = l.Status
	}
	if l.Archived {
		s += ", archived"
	}
	return s
}

// writeLinks writes the header line of linked issues: label, counts, and a
// note when not all were read; nothing when there are none.
func writeLinks(b *strings.Builder, label string, p graph.Page[issue.Link], counts []string) {
	if len(counts) == 0 && p.TotalCount == 0 && len(p.Nodes) == 0 {
		return
	}
	if note := render.Shown(len(p.Nodes), p.TotalCount, ""); note != "" {
		counts = append(counts, note)
	}
	fmt.Fprintf(b, "%s: %s\n", label, strings.Join(counts, ", "))
}

// writeLink writes a linked issue, named ref, on its own line, with what
// to say about it (its board status, closed) in parentheses unless empty.
func writeLink(b *strings.Builder, l issue.Link, ref, about string) {
	fmt.Fprintf(b, "  %s %s", ref, clipRunes(singleLine(l.Title), maxLinkTitleRunes))
	if about = singleLine(about); about != "" {
		fmt.Fprintf(b, " (%s)", about)
	}
	b.WriteString("\n")
}

// writeHistory writes the status changes on the board, oldest first: in
// one line with days unless full, which writes a change per line with its
// time.
func writeHistory(b *strings.Builder, c *issue.Card, full bool) {
	switch {
	case c.HistoryUntracked:
		b.WriteString("status history: not tracked (GitHub records changes of the built-in Status field only)\n")
		return
	case len(c.History) == 0 && !c.HistoryTruncated:
		b.WriteString("status history: none\n")
		return
	case len(c.History) == 0:
		b.WriteString("status history: none among the latest changes read; older ones are not read\n")
		return
	}
	head := "status history"
	if c.HistoryTruncated {
		head += " (latest changes only; older ones are not read)"
	}
	automated := func(h issue.StatusChange) bool { return h.Automated || projectAutomation[h.Actor] }
	if full {
		b.WriteString(head + ":\n")
		for _, h := range c.History {
			by := "by " + cardLogin(h.Actor)
			if automated(h) {
				by = "(automation)"
			}
			fmt.Fprintf(b, "  %s %s %s\n", cardMinute(h.At), render.Transition(h.From, h.To), by)
		}
		return
	}
	// One chain: "no status → Backlog (automation, 2026-11-14) → In
	// progress (alice, 2026-11-14)"; a change that does not start where
	// the last one ended starts a new chain after "; ".
	name := func(s string) string { return cmp.Or(s, "no status") }
	var chain strings.Builder
	last := ""
	for i, h := range c.History {
		if from := name(h.From); i == 0 || from != last {
			if i > 0 {
				chain.WriteString("; ")
			}
			chain.WriteString(from)
		}
		who := cardLogin(h.Actor)
		if automated(h) {
			who = "automation"
		}
		last = name(h.To)
		fmt.Fprintf(&chain, " → %s (%s, %s)", last, who, cardDay(h.At))
	}
	fmt.Fprintf(b, "%s: %s\n", head, singleLine(chain.String()))
}

// writeComments writes the last v.comments comments, oldest first.
func (v cardView) writeComments(b *strings.Builder, c *issue.Card) {
	nodes := c.Comments.Nodes
	total := max(c.Comments.TotalCount, len(nodes))
	if total == 0 {
		b.WriteString("comments: none\n")
		return
	}
	shown, bodies, limit := v.shownComments(c)
	var hint string
	switch {
	case len(shown) == 0:
		hint = "--comments N prints the last N"
	case v.comments < issue.MaxComments:
		hint = fmt.Sprintf("--comments all prints up to %d", issue.MaxComments)
	default:
		hint = fmt.Sprintf("older ones are beyond gh dojo's %d-comment limit: gh issue view %d -R %s --comments", issue.MaxComments, c.Number, c.Repo)
	}
	if note := render.Shown(len(shown), total, hint); note != "" {
		fmt.Fprintf(b, "comments (%s):\n", note)
	} else {
		fmt.Fprintf(b, "comments (%d):\n", total)
	}
	for i, cm := range shown {
		fmt.Fprintf(b, "--- %s, %s\n", cardLogin(cm.Author), cardMinute(cm.CreatedAt))
		text, note := fold(bodies[i], limit)
		b.WriteString(indent(text) + "\n")
		if note != "" {
			b.WriteString("  " + note + "\n")
		}
	}
}

// shownComments returns the last v.comments comments of c, oldest first,
// their cleaned bodies ("(empty)" for an empty one) and where to cut them
// (see commentLimit).
func (v cardView) shownComments(c *issue.Card) (shown []issue.Comment, bodies []string, limit int) {
	nodes := c.Comments.Nodes
	shown = nodes[len(nodes)-min(len(nodes), v.comments):]
	bodies = make([]string, len(shown))
	lengths := make([]int, len(shown))
	for i, cm := range shown {
		bodies[i] = v.shortSessions(cleanText(cm.Body, cm.ViewerDidAuthor))
		if bodies[i] == "" {
			bodies[i] = "(empty)"
		}
		lengths[i] = utf8.RuneCountInString(bodies[i])
	}
	return shown, bodies, v.commentLimit(lengths)
}

// commentLimit is where comments of these lengths are cut so that together
// they fit v.commentBudget: short ones stay whole and the longer ones share
// what is left equally, each within minCommentRunes..maxCommentRunes. 0
// means no cut.
func (v cardView) commentLimit(lengths []int) int {
	if v.commentBudget <= 0 || len(lengths) == 0 {
		return 0
	}
	sorted := slices.Clone(lengths)
	slices.Sort(sorted)
	left := v.commentBudget
	for i, n := range sorted {
		share := left / (len(sorted) - i)
		if n > share {
			return min(maxCommentRunes, max(minCommentRunes, share))
		}
		left -= n
	}
	return 0 // everything fits
}

// fold cuts s to at most limit runes (0: no limit), preferring a line
// end, and returns the note saying so ("" when s is whole).
func fold(s string, limit int) (string, string) {
	total := utf8.RuneCountInString(s)
	if limit <= 0 || total <= limit {
		return s, ""
	}
	cut := []rune(s)[:limit]
	if i := lastRune(cut, '\n'); i >= limit/2 {
		cut = cut[:i]
	} else if i := lastRune(cut, ' '); i >= limit/2 {
		cut = cut[:i]
	}
	text := strings.TrimRight(string(cut), " \t\n")
	return text + " …", fmt.Sprintf("[cut: %d of %d characters shown; --full prints all]",
		utf8.RuneCountInString(text), total)
}

func lastRune(rs []rune, r rune) int {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i] == r {
			return i
		}
	}
	return -1
}

// sanitize makes user text safe to print: every line end becomes \n
// (a lone \r too) and bidi controls are dropped.
func sanitize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return bidiControls.Replace(s)
}

// cleanText sanitizes a description or comment and trims it. With marker
// set (descriptions, and comments the viewer wrote: the only ones gh-dojo
// posts) it also drops the gh-dojo marker that ends the text, unless the
// marker is inside a code block left open.
func cleanText(s string, marker bool) string {
	s = sanitize(s)
	if loc := cardMarkerRE.FindStringIndex(s); marker && loc != nil {
		if len(fenceRE.FindAllStringIndex(s[:loc[0]], -1))%2 == 0 {
			s = s[:loc[0]]
		}
	}
	return strings.Trim(s, " \t\n")
}

// indent prefixes every non-empty line, so a description or comment cannot
// pass for the card's own lines ("comments (...):", "--- author, date").
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = "  " + l
		}
	}
	return strings.Join(lines, "\n")
}

func singleLine(s string) string { return strings.Join(strings.Fields(sanitize(s)), " ") }

// clip cuts s to n runes, ending with "…" when cut.
func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimRight(string([]rune(s)[:n-1]), " ") + "…"
}

// login names an account; a deleted one is "ghost", as on GitHub.
func cardLogin(s string) string {
	if s == "" {
		return "ghost"
	}
	return s
}

func cardDay(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.UTC().Format("2006-01-02")
}

func cardMinute(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.UTC().Format("2006-01-02 15:04")
}
