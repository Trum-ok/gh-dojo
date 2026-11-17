package render

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// JoinWords lists words as "a", "a or b", "a, b or c" with conj ("or",
// "and") before the last.
func JoinWords(words []string, conj string) string {
	if len(words) <= 1 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " " + conj + " " + words[len(words)-1]
}

// Plural counts n of a thing: "1 issue", "3 issues".
func Plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// RuneLen is the length of s in runes, the unit of all length limits.
func RuneLen(s string) int { return utf8.RuneCountInString(s) }

// Clip cuts s to at most n runes, ending with "…" when cut. It cuts between
// grapheme clusters, so an accented letter or emoji is never split.
func Clip(s string, n int) string {
	if RuneLen(s) <= n {
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

// bidiControls are the Unicode bidirectional controls: text from GitHub
// could use them to make what a terminal shows differ from what it says.
var bidiControls = strings.NewReplacer(
	"\u202a", "", "\u202b", "", "\u202c", "", "\u202d", "", "\u202e", "",
	"\u2066", "", "\u2067", "", "\u2068", "", "\u2069", "",
)

// Sanitize makes text from GitHub safe to print as several lines: line
// endings become "\n" and bidirectional controls are dropped.
func Sanitize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return bidiControls.Replace(s)
}

// SingleLine makes text safe to print on one line: bidirectional controls
// are dropped, other control characters count as spaces, and every run of
// spaces becomes one.
func SingleLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, bidiControls.Replace(s))
	return strings.Join(strings.Fields(s), " ")
}

// FirstLine is the first line of s, without surrounding spaces.
func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// HasPrefixFold reports whether s starts with prefix, ignoring case.
func HasPrefixFold(s, prefix string) bool {
	_, ok := CutPrefixFold(s, prefix)
	return ok
}

// CutPrefixFold returns s without prefix, matched ignoring case, and
// whether s started with it. It counts runes, not bytes: the case forms
// of a letter may differ in length.
func CutPrefixFold(s, prefix string) (string, bool) {
	rs, n := []rune(s), utf8.RuneCountInString(prefix)
	if len(rs) < n || !strings.EqualFold(string(rs[:n]), prefix) {
		return s, false
	}
	return string(rs[n:]), true
}
