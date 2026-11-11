package main

import (
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
	// maxBriefRefs is how many open blockers --brief names.
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
	comments    int    // how many of the latest comments to print
	// bodyRunes cuts the description and commentBudget is shared by the
	// comments shown (see commentRunes); 0 prints them whole.
	bodyRunes, commentBudget int
}

// card renders the full card.
func (v cardView) card(c *issue.Card) string {
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	line("#%d %s", c.Number, singleLine(c.Title))
	line("state: %s, opened %s by %s", cardState(c), cardDay(c.CreatedAt), cardLogin(c.Author))
	line("board: %s", v.board(c, true))
	line("labels: %s", cardNames(c.Labels))
	line("assignees: %s", cardNames(c.Assignees))
	writeLinks(&b, "blocked by", c.BlockedBy, c.Repo)
	writeLinks(&b, "blocking", c.Blocking, c.Repo)
	if c.OnBoard() || c.BoardUnknown && len(c.History) > 0 {
		writeHistory(&b, c)
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

// brief renders three lines: title; state, status, labels, assignees;
// blockers and counts. A fourth notes a partly hidden card.
func (v cardView) brief(c *issue.Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s\n", c.Number, singleLine(c.Title))
	fmt.Fprintf(&b, "%s · %s · labels: %s · assignees: %s\n", cardState(c), v.board(c, false), cardNames(c.Labels), cardNames(c.Assignees))

	var blocked string
	open := c.OpenBlockers()
	closed := len(c.BlockedBy.Nodes) - len(open)
	switch {
	case len(open) > 0:
		refs := make([]string, 0, maxBriefRefs)
		for _, l := range open[:min(len(open), maxBriefRefs)] {
			refs = append(refs, l.Ref(c.Repo))
		}
		blocked = fmt.Sprintf("blocked by %d open: %s", len(open), strings.Join(refs, ", "))
		if n := len(open) - len(refs); n > 0 {
			blocked += fmt.Sprintf(" and %d more", n)
		}
	case closed > 0:
		blocked = fmt.Sprintf("no open blockers (%d closed)", closed)
	default:
		blocked = "no blockers"
	}
	if note := render.Shown(len(c.BlockedBy.Nodes), c.BlockedBy.TotalCount, ""); note != "" {
		blocked += " (" + note + " blockers)"
	}
	blocking := "blocking none"
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
	fmt.Fprintf(&b, "%s · %s · comments: %d\n", blocked, blocking, c.Comments.TotalCount)
	if c.Incomplete {
		b.WriteString("note: GitHub withheld part of this issue from the token; the rest is accurate\n")
	}
	return b.String()
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
			if val.Field != v.statusField {
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

// writeLinks writes blockers one per line, open ones first.
func writeLinks(b *strings.Builder, label string, p graph.Page[issue.Link], repo issue.Repo) {
	var open, closed []issue.Link
	for _, l := range p.Nodes {
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
	if len(counts) == 0 && p.TotalCount == 0 {
		counts = []string{"none"}
	}
	if note := render.Shown(len(p.Nodes), p.TotalCount, ""); note != "" {
		counts = append(counts, note)
	}
	fmt.Fprintf(b, "%s: %s\n", label, strings.Join(counts, ", "))
	for _, l := range append(open, closed...) {
		fmt.Fprintf(b, "  %s %s", l.Ref(repo), clipRunes(singleLine(l.Title), maxLinkTitleRunes))
		if l.Closed() {
			b.WriteString(" (closed)")
		}
		b.WriteString("\n")
	}
}

// writeHistory writes the status changes on the board, oldest first.
func writeHistory(b *strings.Builder, c *issue.Card) {
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
	case c.HistoryTruncated:
		b.WriteString("status history (latest changes only; older ones are not read):\n")
	default:
		b.WriteString("status history:\n")
	}
	for _, h := range c.History {
		by := "by " + cardLogin(h.Actor)
		if h.Automated || projectAutomation[h.Actor] {
			by = "(automation)"
		}
		fmt.Fprintf(b, "  %s %s %s\n", cardMinute(h.At), render.Transition(h.From, h.To), by)
	}
}

// writeComments writes the last v.comments comments, oldest first.
func (v cardView) writeComments(b *strings.Builder, c *issue.Card) {
	nodes := c.Comments.Nodes
	total := max(c.Comments.TotalCount, len(nodes))
	if total == 0 {
		b.WriteString("comments: none\n")
		return
	}
	shown := nodes[len(nodes)-min(len(nodes), v.comments):]
	var hint string
	switch {
	case len(shown) == 0:
		hint = "--comments N prints the last N"
	case v.comments < issue.MaxComments:
		hint = fmt.Sprintf("--comments all prints up to %d", issue.MaxComments)
	default:
		hint = fmt.Sprintf("older ones: gh issue view %d -R %s --comments", c.Number, c.Repo)
	}
	if note := render.Shown(len(shown), total, hint); note != "" {
		fmt.Fprintf(b, "comments (%s):\n", note)
	} else {
		fmt.Fprintf(b, "comments (%d):\n", total)
	}
	bodies := make([]string, len(shown))
	lengths := make([]int, len(shown))
	for i, cm := range shown {
		bodies[i] = cleanText(cm.Body, cm.ViewerDidAuthor)
		if bodies[i] == "" {
			bodies[i] = "(empty)"
		}
		lengths[i] = utf8.RuneCountInString(bodies[i])
	}
	limit := v.commentLimit(lengths)
	for i, cm := range shown {
		fmt.Fprintf(b, "--- %s, %s\n", cardLogin(cm.Author), cardMinute(cm.CreatedAt))
		text, note := fold(bodies[i], limit)
		b.WriteString(indent(text) + "\n")
		if note != "" {
			b.WriteString("  " + note + "\n")
		}
	}
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
