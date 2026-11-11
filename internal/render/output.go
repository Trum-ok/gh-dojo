package render

import (
	"fmt"
	"io"
	"strings"
)

// MaxConfirmRunes is the length limit of a confirmation line.
const MaxConfirmRunes = 160

// Confirm builds the single confirmation line printed after a write: the
// subject followed by comma-separated parts, cut to MaxConfirmRunes.
//
//	Confirm("#42", Transition("Backlog", "In progress"), "comment left")
//	// #42 Backlog → In progress, comment left
func Confirm(subject string, parts ...string) string {
	line := subject
	if rest := strings.Join(nonEmpty(parts), ", "); rest != "" {
		line += " " + rest
	}
	return clip(oneLine(line), MaxConfirmRunes)
}

// Transition describes a status change for a confirmation line.
func Transition(from, to string) string {
	switch {
	case from == to:
		return to + " (unchanged)"
	case from == "":
		return "no status → " + to
	default:
		return from + " → " + to
	}
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

// clip cuts s to at most n runes, ending with "…" when cut.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
