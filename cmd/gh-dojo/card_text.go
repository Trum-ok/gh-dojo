package main

import (
	"fmt"
	"regexp"
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
	// maxBodyRunes and maxCommentRunes cut long texts unless --full.
	maxBodyRunes    = 4000
	maxCommentRunes = 1500
	// maxLinkTitleRunes cuts titles of linked issues.
	maxLinkTitleRunes = 80
	// maxBriefRefs is how many open blockers --brief names.
	maxBriefRefs = 5
)

// projectAutomation are logins of GitHub's own project automation. The
// event's wasAutomated flag is unreliable (false for workflow changes), so
// the actor tells them apart too.
var projectAutomation = map[string]bool{"github-project-automation": true}

// cardMarkerRE matches the hidden markers gh dojo puts in comments.
var cardMarkerRE = regexp.MustCompile(`(?s)<!--\s*gh-dojo:.*?-->`)

// cardView renders cards with show's flags.
type cardView struct {
	statusField string // board name of the status field, left out of the field list
	comments    int    // how many of the latest comments to print
	// bodyRunes and commentRunes cut longer texts; 0 prints them whole.
	bodyRunes, commentRunes int
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
	if body := cleanText(c.Body); body == "" {
		line("description: none")
	} else {
		line("description:")
		text, note := fold(body, v.bodyRunes)
		line("%s", text)
		if note != "" {
			line("%s", note)
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
	fmt.Fprintf(&b, "%s · blocking %d · comments: %d\n", blocked, c.Blocking.TotalCount, c.Comments.TotalCount)
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

// list joins names, "none" for no names, noting a cut list.
func cardNames(p graph.Page[string]) string {
	if len(p.Nodes) == 0 && p.TotalCount == 0 {
		return "none"
	}
	s := strings.Join(p.Nodes, ", ")
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
	for _, cm := range shown {
		fmt.Fprintf(b, "--- %s, %s\n", cardLogin(cm.Author), cardMinute(cm.CreatedAt))
		body := cleanText(cm.Body)
		if body == "" {
			body = "(empty)"
		}
		text, note := fold(body, v.commentRunes)
		b.WriteString(text + "\n")
		if note != "" {
			b.WriteString(note + "\n")
		}
	}
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

// clean drops gh dojo markers and Windows line ends and trims the text.
func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = cardMarkerRE.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func singleLine(s string) string { return strings.Join(strings.Fields(s), " ") }

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
