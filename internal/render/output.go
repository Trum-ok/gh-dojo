package render

import (
	"fmt"
	"io"
	"strings"
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
	head := SingleLine(subject)
	tail := SingleLine(strings.Join(nonEmpty(effects), ", "))
	if title = SingleLine(title); title != "" {
		// room left for `"title"` between head and tail, with separators.
		room := MaxConfirmRunes - RuneLen(head) - RuneLen(tail) - 2
		if head != "" {
			room--
		}
		if tail != "" {
			room--
		}
		if room >= minTitleRunes || room >= RuneLen(title) {
			head = strings.TrimSpace(head + ` "` + Clip(title, room) + `"`)
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
	fmt.Fprintln(w, "warning: "+SingleLine(fmt.Sprintf(format, args...)))
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

// OrEmpty returns s, or an empty slice for nil: JSON lists are [], never
// null.
func OrEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// OrNull returns &s, or nil (null) for "": an absent value is null in
// JSON, never "".
func OrNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
