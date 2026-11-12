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
	for _, sub := range m.re.FindAllStringSubmatch(message, -1) {
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
