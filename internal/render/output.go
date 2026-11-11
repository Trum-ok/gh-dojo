package render

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// MaxConfirmRunes is the length limit of a confirmation line.
const MaxConfirmRunes = 160

// minTitleRunes is the shortest cut title worth printing; below it the title
// is left out.
const minTitleRunes = 8

// Confirm builds the single confirmation line printed after a write: the
// subject, the optional title in quotes, then the comma-separated effects.
//
//	Confirm("#42", "", Transition("Backlog", "In progress"), "comment left")
//	// #42 Backlog → In progress, comment left
//	Confirm("#57", "Fix login redirect", "created", Transition("", "Backlog"))
//	// #57 "Fix login redirect" created, no status → Backlog
//
// Only the title is shortened to keep the line within MaxConfirmRunes, and
// left out if too little of it would fit. The subject and effects are never
// cut: they say what was done, so a line without a title may be longer.
func Confirm(subject, title string, effects ...string) string {
	head := oneLine(subject)
	tail := oneLine(strings.Join(nonEmpty(effects), ", "))
	if title = oneLine(title); title != "" {
		// room left for `"title"` between head and tail, with separators.
		room := MaxConfirmRunes - runeLen(head) - runeLen(tail) - 2
		if head != "" {
			room--
		}
		if tail != "" {
			room--
		}
		if room >= minTitleRunes || room >= runeLen(title) {
			head = strings.TrimSpace(head + ` "` + clip(title, room) + `"`)
		}
	}
	return strings.TrimSpace(head + " " + tail)
}

// Transition describes a status change for a confirmation line. An empty
// status reads "no status".
func Transition(from, to string) string {
	if from == "" {
		from = "no status"
	}
	if to == "" {
		to = "no status"
	}
	if from == to {
		return to + " (unchanged)"
	}
	return from + " → " + to
}

// Warn prints a one-line warning to w, which should be stderr.
func Warn(w io.Writer, format string, args ...any) {
	fmt.Fprintln(w, "warning: "+oneLine(fmt.Sprintf(format, args...)))
}

// Shown returns the note for a list cut to shown of total items, or "" when
// nothing is hidden. The optional hint tells how to see the rest.
//
//	Shown(50, 73, "use --limit 100")  // showing 50 of 73, use --limit 100
func Shown(shown, total int, hint string) string {
	if shown >= total {
		return ""
	}
	note := fmt.Sprintf("showing %d of %d", shown, total)
	if hint != "" {
		note += ", " + hint
	}
	return note
}

// Truncate keeps at most limit items (limit <= 0 keeps all) and returns the
// note from Shown. total is the full count, e.g. a GraphQL totalCount; it is
// raised to len(items) if smaller.
func Truncate[T any](items []T, limit, total int, hint string) ([]T, string) {
	total = max(total, len(items))
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, Shown(len(items), total, hint)
}

func nonEmpty(ss []string) []string {
	out := ss[:0:0]
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// oneLine collapses all whitespace runs, including newlines, to single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// runeLen is the length of s in runes, the unit of all length limits.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// clip cuts s to at most n runes, ending with "…" when cut. It cuts between
// grapheme clusters, so an accented letter or emoji is never split.
func clip(s string, n int) string {
	if runeLen(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		r := g.Runes()
		if used+len(r) > n-1 { // keep one rune for "…"
			break
		}
		b.WriteString(g.Str())
		used += len(r)
	}
	return strings.TrimRight(b.String(), " \t") + "…"
}
