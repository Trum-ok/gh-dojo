package release

import (
	"regexp"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/issue"
)

// refPattern is an issue reference in a commit message: #12,
// owner/repo#12 or an issue's URL. A bare number is not one: GitHub links
// only these.
const refPattern = `(?:(?:[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9._-]+)?#[0-9]+|https?://[^\s/]+/[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9._-]+/issues/[0-9]+)\b`

var refRE = regexp.MustCompile(refPattern)

// Matcher finds the issues a commit message links with a keyword
// ([release] keywords): "Fixes #12", "refs: #12, #13 and acme/api#4",
// "Part of https://github.com/acme/api/issues/4". A keyword is matched
// whole, regardless of case, followed by an optional colon and a list of
// references separated by commas, "and" or spaces, on the same line; a reference that does
// not follow a keyword ("see #12") does not count. GitHub closes only the
// first issue of "Fixes #12, #13"; a list is taken whole here, as a
// person reading it would.
//
// Code does not count, as Markdown does not link it: a reference in a code
// span ("`Refs #12`") or a fenced block (```) is an example of the syntax,
// not a link (see stripCode).
type Matcher struct {
	re *regexp.Regexp // nil: no keywords, nothing matches
}

// NewMatcher returns the matcher of keywords.
func NewMatcher(keywords []string) *Matcher {
	if len(keywords) == 0 {
		return &Matcher{}
	}
	alts := make([]string, len(keywords))
	for i, k := range keywords {
		alts[i] = strings.Join(strings.Fields(regexp.QuoteMeta(k)), `[ \t]+`)
	}
	// Spaces and tabs only: a keyword and its list stay on one line, so
	// "Refs #3" does not take the "#4" that starts the next one.
	list := refPattern + `(?:(?:[ \t]*,[ \t]*|[ \t]+)(?:and[ \t]+)?` + refPattern + `)*`
	return &Matcher{re: regexp.MustCompile(`(?i)(?:^|[^\pL\pN_])(?:` + strings.Join(alts, "|") + `)(?:[ \t]*:[ \t]*|[ \t]+)(` + list + `)`)}
}

// Refs returns the issues message links, in order, each once, a bare #12
// in repo (the repository of the commit). A URL keeps its host (see
// issue.Ref.CheckHost).
func (m *Matcher) Refs(message string, repo issue.Repo) []issue.Ref {
	if m == nil || m.re == nil {
		return nil
	}
	var out []issue.Ref
	for _, sub := range m.re.FindAllStringSubmatch(stripCode(message), -1) {
		for _, tok := range refRE.FindAllString(sub[1], -1) {
			r, ok := issue.ParseRef(tok, repo)
			if !ok || r.Pull {
				continue
			}
			if !containsRef(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
}

func containsRef(refs []issue.Ref, r issue.Ref) bool {
	for _, o := range refs {
		if o.Same(r) && strings.EqualFold(o.Host, r.Host) {
			return true
		}
	}
	return false
}

// stripCode blanks out the code of a message as Markdown reads it, so no
// reference in it matches: fenced blocks (a line opening with three or
// more ` or ~, up to a line closing with as many or more of the same, or
// the end), then code spans (a run of backticks up to the next run of the
// same length, within a paragraph). Code becomes spaces and keeps its line
// breaks, so the text around it keeps its lines. A backtick run that
// nothing closes is text.
func stripCode(message string) string {
	if !strings.ContainsAny(message, "`~") {
		return message
	}
	lines := strings.SplitAfter(message, "\n")
	fence := "" // the run of ` or ~ of the open fenced block
	for i, l := range lines {
		run, rest := fenceRun(l)
		switch {
		case fence == "" && run == "":
			continue
		case fence == "":
			fence = run
		case strings.HasPrefix(run, fence) && strings.TrimSpace(rest) == "":
			fence = "" // the closing fence
		}
		lines[i] = blankOut(l)
	}
	s := []byte(strings.Join(lines, ""))
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		n := runLen(s, i)
		end := spanEnd(s, i+n, n)
		if end < 0 {
			i += n
			continue
		}
		for j := i; j < end; j++ {
			if s[j] != '\n' {
				s[j] = ' '
			}
		}
		i = end
	}
	return string(s)
}

// fenceRun is the run of ` or ~ that line l starts with, after up to three
// spaces, when it can open or close a fenced block (three or more; no
// backtick after a backtick fence), and the rest of the line; "" when l
// is not a fence.
func fenceRun(l string) (run, rest string) {
	t := strings.TrimLeft(l, " ")
	if len(l)-len(t) > 3 || t == "" || t[0] != '`' && t[0] != '~' {
		return "", ""
	}
	n := runLen([]byte(t), 0)
	if n < 3 || t[0] == '`' && strings.Contains(t[n:], "`") {
		return "", ""
	}
	return t[:n], t[n:]
}

// runLen is how many bytes equal to s[i] start at i.
func runLen(s []byte, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == s[i] {
		n++
	}
	return n
}

// spanEnd is the index just past the run of exactly n backticks that
// closes a code span whose content starts at i; -1 when a blank line or
// the end comes first.
func spanEnd(s []byte, i, n int) int {
	for j := i; j < len(s); {
		switch {
		case s[j] == '`':
			k := runLen(s, j)
			if k == n {
				return j + k
			}
			j += k
		case s[j] == '\n' && blankLineAt(s, j+1):
			return -1
		default:
			j++
		}
	}
	return -1
}

// blankLineAt reports whether the line that starts at i is blank: only
// spaces, tabs or a carriage return before its line break or the end.
func blankLineAt(s []byte, i int) bool {
	for ; i < len(s) && s[i] != '\n'; i++ {
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\r' {
			return false
		}
	}
	return true
}

// blankOut is l with every byte but its line break a space.
func blankOut(l string) string {
	body := strings.TrimRight(l, "\r\n")
	return strings.Repeat(" ", len(body)) + l[len(body):]
}
