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
	// VarAssignee is the login of the user --assignee names, as GitHub
	// spells it; only a verb with assign = "{assignee}" may use it.
	VarAssignee = ArgAssignee
)

var (
	commentVars = []string{VarSession, VarComment, VarBy, VarAssignee}
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
//   - a blank template line that follows a blank output line is dropped, so
//     dropped parts leave one blank line, not several;
//   - trailing spaces of every output line are cut, and leading and
//     trailing blank lines are dropped. Leading indentation stays.
//
// There is no escape syntax: a literal "{comment}" cannot be written in a
// template. Braces that do not form {name}, such as "{}" or "{ x }", are
// plain text. Values are inserted as is and never expanded again.
//
// The result is "" when nothing is left; callers post no comment then.
func Render(tmpl string, vars map[string]string) string {
	var out []string
	for _, line := range strings.Split(tmpl, "\n") {
		matches := placeholderRE.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 && strings.TrimSpace(line) == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
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
		for _, l := range strings.Split(line, "\n") {
			out = append(out, strings.TrimRight(l, " \t\r"))
		}
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// Session says how to find the agent's session: the first non-empty
// environment variable in Env, formatted with Link.
type Session struct {
	Env  []string
	Link string // template with {id}; "{id}" when not configured
}

// Configured reports whether the config names any session variable.
// Callers warn about a missing session only when it is configured.
func (s Session) Configured() bool { return len(s.Env) > 0 }

// Resolve returns the session link for {session}, or "" and false when no
// variable in Env is set. Check Configured to tell "not configured" (no
// warning) from "configured but unset" (warn, do not fail). getenv is
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
