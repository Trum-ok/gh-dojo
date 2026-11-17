package release

import (
	"regexp"
	"slices"
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
// With suffix ([release] suffix), it also finds the issues in parentheses
// at the end of the first line: "fix: login limit (#12)" (see Suffix).
//
// Code does not count, as Markdown does not link it: a reference in a code
// span ("`Refs #12`") or a fenced block (```) is an example of the syntax,
// not a link (see stripCode).
type Matcher struct {
	re     *regexp.Regexp // nil: no keywords, nothing matches
	suffix bool
}

// NewMatcher returns the matcher of keywords, and of the suffix if suffix.
func NewMatcher(keywords []string, suffix bool) *Matcher {
	if len(keywords) == 0 {
		return &Matcher{suffix: suffix}
	}
	alts := make([]string, len(keywords))
	for i, k := range keywords {
		alts[i] = strings.Join(strings.Fields(regexp.QuoteMeta(k)), `[ \t]+`)
	}
	// Spaces and tabs only: a keyword and its list stay on one line, so
	// "Refs #3" does not take the "#4" that starts the next one.
	list := refPattern + `(?:(?:[ \t]*,[ \t]*|[ \t]+)(?:and[ \t]+)?` + refPattern + `)*`
	return &Matcher{
		re:     regexp.MustCompile(`(?i)(?:^|[^\pL\pN_])(?:` + strings.Join(alts, "|") + `)(?:[ \t]*:[ \t]*|[ \t]+)(` + list + `)`),
		suffix: suffix,
	}
}

// suffixRE is what the parentheses of a suffix hold: issue references
// separated by commas, spaces around them allowed.
var suffixRE = regexp.MustCompile(`^[ \t]*` + refPattern + `(?:[ \t]*,[ \t]*` + refPattern + `)*[ \t]*$`)

// Suffix returns the issues the suffix of message names, in order, each
// once, a bare #12 in repo; nil without [release] suffix. The suffix is
// the groups of parentheses that end the first line, spaces after them
// allowed: "fix: login limit (#12)", "(#12, acme/api#3)", and both of
// "fix (#161) (#200)", a squash merge's "(#PR)" after a title that names
// its issue. A group holds only references separated by commas, and
// follows a space, another group or nothing; the groups are read from the
// end, up to one that is not such ("(#12, wip)", "()", "(12)"). So
// parentheses in the middle of the line ("(see #12) fix"), in the body,
// or followed by anything ("(#12).", the quote of `Revert "fix (#12)"`)
// do not count. Code does not count either.
func (m *Matcher) Suffix(message string, repo issue.Repo) []issue.Ref {
	if m == nil || !m.suffix {
		return nil
	}
	line, _, _ := strings.Cut(stripCode(message), "\n")
	var groups []string // from the end
	for {
		line = strings.TrimRight(line, " \t\r")
		open := strings.LastIndexByte(line, '(')
		if !strings.HasSuffix(line, ")") || open < 0 || !suffixRE.MatchString(line[open+1:len(line)-1]) {
			break
		}
		if open > 0 && !strings.ContainsRune(" \t)", rune(line[open-1])) {
			break // "f(#12)": a call, not a suffix
		}
		groups = append(groups, line[open+1:len(line)-1])
		line = line[:open]
	}
	var out []issue.Ref
	for _, g := range slices.Backward(groups) {
		for _, tok := range refRE.FindAllString(g, -1) {
			r, ok := issue.ParseRef(tok, repo)
			if ok && !r.Pull && !containsRef(out, r) {
				out = append(out, r)
			}
		}
	}
	return out
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

// stripCode masks the code of a message as Markdown reads it, so no
// reference in it matches: fenced blocks (a line opening with three or
// more ` or ~, up to a line closing with as many or more of the same, or
// the end), indented blocks (lines indented by four spaces or a tab, from
// one after a blank line, a fence or the start), then code spans (a run of backticks up to the next run of
// the same length, within a paragraph). A backtick run that nothing closes
// is text.
//
// Code becomes codeByte and keeps its line breaks: the text around it
// keeps its lines, and code ends a keyword's list as the backticks did,
// so "Refs `#12` #13" names nothing.
func stripCode(message string) string {
	lines := strings.SplitAfter(message, "\n")
	fence := ""                    // the run of ` or ~ of the open fenced block
	blank, indented := true, false // after a blank line; in an indented block
	for i, l := range lines {
		if fence != "" {
			if run, rest := fenceRun(l); strings.HasPrefix(run, fence) && strings.TrimSpace(rest) == "" {
				fence, blank = "", true // the closing fence; a block may follow
			}
			lines[i] = maskLine(l)
			continue
		}
		switch {
		case strings.TrimSpace(l) == "":
			blank = true
			continue
		case (blank || indented) && (strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "\t")):
			indented, blank = true, false
			lines[i] = maskLine(l)
			continue
		}
		indented, blank = false, false
		if fence, _ = fenceRun(l); fence != "" {
			lines[i] = maskLine(l)
		}
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
				s[j] = codeByte
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

// codeByte stands for each byte of code: neither a space, which would
// separate the references around it, nor a character of a keyword or a
// reference.
const codeByte = 0

// maskLine is l with every byte but its line break codeByte.
func maskLine(l string) string {
	body := strings.TrimRight(l, "\r\n")
	return strings.Repeat("\x00", len(body)) + l[len(body):]
}
