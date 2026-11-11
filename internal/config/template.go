package config

import (
	"os"
	"regexp"
	"slices"
	"strings"
)

// Placeholders of verb comment templates.
const (
	VarSession = "session" // agent session link, see Session.Resolve
	VarComment = ArgComment
	VarBy      = ArgBy
)

var (
	commentVars = []string{VarSession, VarComment, VarBy}
	linkVars    = []string{"id"}

	// placeholderRE matches {name}. Other braces, such as "{}" or "{ x }",
	// are plain text.
	placeholderRE = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// checkTemplate returns the first placeholder of tmpl not in allowed.
func checkTemplate(tmpl string, allowed []string) (string, bool) {
	for _, m := range placeholderRE.FindAllStringSubmatch(tmpl, -1) {
		if !slices.Contains(allowed, m[1]) {
			return m[1], false
		}
	}
	return "", true
}

// Render fills a comment template. Each {name} is replaced with vars[name]
// (missing names count as empty). Then:
//
//   - a line with placeholders that all came out empty is dropped, so an
//     optional part disappears with its surrounding text: with by unset,
//     "Waiting for {by}." vanishes;
//   - runs of blank lines collapse into one, and leading and trailing blank
//     lines and spaces are trimmed.
//
// The result is "" when nothing is left; callers post no comment then.
func Render(tmpl string, vars map[string]string) string {
	var out []string
	for _, line := range strings.Split(tmpl, "\n") {
		matches := placeholderRE.FindAllStringSubmatch(line, -1)
		empty := len(matches) > 0
		for _, m := range matches {
			if strings.TrimSpace(vars[m[1]]) != "" {
				empty = false
			}
		}
		if empty {
			continue
		}
		line = placeholderRE.ReplaceAllStringFunc(line, func(s string) string {
			return vars[s[1:len(s)-1]]
		})
		if strings.TrimSpace(line) == "" && (len(out) == 0 || out[len(out)-1] == "") {
			continue
		}
		out = append(out, strings.TrimRight(line, " \t\r"))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// Session says how to find the agent's session: the first non-empty
// environment variable in Env, formatted with Link.
type Session struct {
	Env  []string
	Link string // template with {id}; "{id}" when not configured
}

// Resolve returns the session link for {session}, or "" and false when no
// variable in Env is set; callers warn then, they do not fail. getenv is
// os.Getenv when nil.
func (s Session) Resolve(getenv func(string) string) (string, bool) {
	if getenv == nil {
		getenv = os.Getenv
	}
	for _, name := range s.Env {
		if id := strings.TrimSpace(getenv(name)); id != "" {
			link := s.Link
			if link == "" {
				link = "{id}"
			}
			return strings.ReplaceAll(link, "{id}", id), true
		}
	}
	return "", false
}
