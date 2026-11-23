// Package render turns command results into the short, predictable output
// agents rely on: exit codes, error blocks (GitHub's errors mapped to
// them), confirmation lines, warnings and "showing N of M" notes. Its text
// helpers (text.go) join, count, clip and sanitize what every command
// prints.
package render

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// Code is a process exit code. The values are part of the CLI contract.
type Code int

const (
	ExitOK       Code = 0 // everything applied
	ExitUsage    Code = 1 // bad arguments or config: fix per the error text
	ExitDenied   Code = 2 // a process check refused: ask a human, don't work around it
	ExitNotFound Code = 3 // issue or board not found: check the number or the board config
	ExitNetwork  Code = 4 // network, rate limit or auth: retry later or refresh auth
	ExitPartial  Code = 5 // partially applied: re-run the same command to finish
)

// ExitCodes describes every exit code, in order, with what the agent
// should do next; the root help lists them.
var ExitCodes = []struct {
	Code          Code
	Meaning, Next string
}{
	{ExitOK, "everything applied", "go on"},
	{ExitUsage, "bad arguments or config", "fix them as the error says"},
	{ExitDenied, "a process check refused", "don't work around it; ask the user"},
	{ExitNotFound, "issue or board not found", "check the number (gh dojo list) or board.owner/project in .github/dojo.toml"},
	{ExitNetwork, "network, rate limit or auth", "retry later, or gh auth refresh -s project"},
	{ExitPartial, "partially applied", "re-run the same command; it finishes the rest"},
}

// MaxLineRunes is how long a line of the error block may be, "error: "
// included: a longer one is cut with "…".
const MaxLineRunes = 200

const (
	maxWhatLines = 3   // the error block is at most 5 lines: what (≤3), allowed, fix
	maxFixRunes  = 400 // a longer fix is dropped: a cut command would be wrong
	maxAllowed   = 20  // longer allowed lists end with "and N more", unless AllowedAll
)

// AuthFix is the fix command for missing or insufficient GitHub auth.
const AuthFix = "gh auth refresh -s project"

// InternalFix is the hint printed for internal errors. It has no side
// effects: an agent should not file issues on its own.
const InternalFix = "re-run the command; if it repeats, report it to the user"

// Error is a user-facing error: what is wrong, which values are allowed and
// which command fixes it. Build one with a constructor such as Usage or
// NotFound and refine it with WithAllowed (or WithAllowedAll), WithFix and
// WithCause.
type Error struct {
	Code    Code
	What    string   // what is wrong; one line, at most 3
	Allowed []string // optional: the values that would have been accepted
	// AllowedAll prints Allowed in full: it is a closed set of gh-dojo's
	// own names, such as config keys (see WithAllowedAll). Otherwise the
	// block cuts Allowed after maxAllowed values.
	AllowedAll bool
	Fix        string // optional: a command to run next; never cut, dropped if too long
	// Wait is how long to wait before re-running ("a few minutes", "60s"),
	// "" for at once: a rate limit sets it, and a partial report built
	// from the error keeps it next to its "re-run the same command".
	Wait  string
	Cause error // optional: underlying error, never printed
}

// New returns an Error with the given code and message.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, What: fmt.Sprintf(format, args...)}
}

// Usage reports bad arguments or config (exit code 1).
func Usage(format string, args ...any) *Error { return New(ExitUsage, format, args...) }

// Denied reports a refused process check (exit code 2).
func Denied(format string, args ...any) *Error { return New(ExitDenied, format, args...) }

// NotFound reports a missing issue, board or similar (exit code 3).
func NotFound(format string, args ...any) *Error { return New(ExitNotFound, format, args...) }

// Network reports a network, rate limit or auth failure (exit code 4).
func Network(format string, args ...any) *Error { return New(ExitNetwork, format, args...) }

// Partial reports a partially applied command (exit code 5). The message
// should say what was done and what was not.
func Partial(format string, args ...any) *Error { return New(ExitPartial, format, args...) }

// RerunStep is the next step after a partial write that stopped with e: its
// fix, then "re-run the same command". A rate limit (Wait) says to wait
// first, its fix already ends in "then re-run the same command", so that is
// not repeated.
func RerunStep(e *Error) string {
	const rerun = "re-run the same command"
	switch fix := strings.TrimSpace(e.Fix); {
	case e.Wait != "":
		return "wait " + e.Wait + ", then " + rerun
	case fix != "":
		return fix + ", then " + rerun
	}
	return rerun
}

// Internal reports a bug in gh-dojo itself, such as a recovered panic
// (exit code 1). The message makes clear the arguments are not at fault.
func Internal(format string, args ...any) *Error {
	return New(ExitUsage, "internal error (not your arguments): "+format, args...).WithFix(InternalFix)
}

// WithAllowed sets the accepted values and returns e. The error block
// lists the first maxAllowed of them, then "and N more": for values read
// from the board or a repository (statuses, labels, users, milestones),
// which can be many.
func (e *Error) WithAllowed(values ...string) *Error {
	e.Allowed, e.AllowedAll = values, false
	return e
}

// WithAllowedAll sets the accepted values, printed in full, and returns e:
// for a closed set of gh-dojo's own names an agent must type exactly, such
// as config keys, where a cut list would hide the right one.
func (e *Error) WithAllowedAll(values ...string) *Error {
	e.Allowed, e.AllowedAll = values, true
	return e
}

// WithFix sets the command that fixes the problem and returns e.
func (e *Error) WithFix(cmd string) *Error { e.Fix = cmd; return e }

// WithCause records the underlying error for errors.Is/As and returns e.
// The cause is never printed.
func (e *Error) WithCause(err error) *Error { e.Cause = err; return e }

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.What
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Text renders the error block: at most 5 lines, no trailing newline.
//
//	error: unknown status "Doing"
//	allowed: Backlog, In progress, Done
//	fix: gh dojo describe
//
// Blank lines of What are dropped, then it is cut to 3 lines; each allowed
// value is put on one line. The allowed line is cut after maxAllowed values
// and 2×MaxLineRunes runes, unless AllowedAll. The fix line is never cut,
// since a cut command would be wrong; a fix longer than maxFixRunes is
// dropped instead.
func (e *Error) Text() string {
	var what []string
	for l := range strings.SplitSeq(e.What, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			what = append(what, l)
		}
	}
	if len(what) == 0 {
		what = []string{"unknown error"}
	}
	if len(what) > maxWhatLines {
		what = what[:maxWhatLines]
	}
	lines := make([]string, 0, maxWhatLines+2)
	for i, l := range what {
		if i == 0 {
			l = "error: " + l
		}
		lines = append(lines, Clip(l, MaxLineRunes))
	}
	switch {
	case len(e.Allowed) == 0:
	case e.AllowedAll:
		lines = append(lines, "allowed: "+joinAllowed(e.Allowed, len(e.Allowed)))
	default:
		lines = append(lines, Clip("allowed: "+joinAllowed(e.Allowed, maxAllowed), MaxLineRunes*2))
	}
	if fix := SingleLine(e.Fix); fix != "" && RuneLen(fix) <= maxFixRunes {
		lines = append(lines, "fix: "+fix)
	}
	return strings.Join(lines, "\n")
}

// joinAllowed joins the first limit values, then "and N more" for the rest.
func joinAllowed(values []string, limit int) string {
	shown := make([]string, 0, min(len(values), limit))
	for _, v := range values[:min(len(values), limit)] {
		shown = append(shown, SingleLine(v))
	}
	s := strings.Join(shown, ", ")
	if n := len(values) - len(shown); n > 0 {
		s += fmt.Sprintf(" and %d more", n)
	}
	return s
}

// Reason is the first clause of an error's message, for a line that
// names several outcomes: a partial report, a batch summary.
func Reason(err error) string {
	what, _, _ := strings.Cut(FromError(err).What, "\n")
	what, _, _ = strings.Cut(what, ";")
	return strings.TrimSpace(what)
}

// WriteError prints err to w as a short error block and returns the exit
// code. A nil err prints nothing and returns ExitOK.
func WriteError(w io.Writer, err error) Code {
	if err == nil {
		return ExitOK
	}
	e := FromError(err)
	fmt.Fprintln(w, e.Text())
	return e.Code
}

// FromError converts any error into an *Error. An *Error keeps its code and
// fields, with the message of any wrapping (fmt.Errorf("ctx: %w", e)) kept as
// context; GitHub API and network errors get a matching code; anything else
// becomes a usage error (code 1) showing only the first line of its message.
//
// GitHub errors are found with errors.As, so they may be wrapped with %w.
// Wrapping with %v flattens them into plain text that is printed as is:
// always wrap API errors with %w so raw GraphQL text never reaches the output.
func FromError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		if e == nil { // a typed nil *Error returned as error
			return Internal("nil error value").WithCause(err)
		}
		if e != err {
			wrapped := *e
			wrapped.What = err.Error()
			return &wrapped
		}
		return e
	}
	var httpErr *api.HTTPError
	var gqlErr *api.GraphQLError
	var netErr net.Error
	switch {
	case errors.As(err, &httpErr) && httpErr != nil:
		return fromHTTP(httpErr).WithCause(err)
	case errors.As(err, &gqlErr) && gqlErr != nil:
		return fromGraphQL(gqlErr).WithCause(err)
	case errors.Is(err, context.Canceled):
		return Network("interrupted before finishing; re-run the command").WithCause(err)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr):
		return Network("network error: %s", FirstLine(err.Error())).WithCause(err)
	}
	return Usage("%s", FirstLine(err.Error())).WithCause(err)
}

func fromHTTP(err *api.HTTPError) *Error {
	s := err.StatusCode
	msg := safeMessage(err.Message, http.StatusText(s))
	switch {
	case s == http.StatusNotFound:
		return NotFound("GitHub API: not found")
	case s == http.StatusUnauthorized:
		return Network("GitHub API: %s", msg).WithFix(AuthFix)
	case (s == http.StatusTooManyRequests || s == http.StatusForbidden) && IsContentLimit(err.Message):
		return ContentLimited(err.Headers.Get("Retry-After"))
	case s == http.StatusTooManyRequests, s == http.StatusForbidden && isRateLimit(err):
		return rateLimited(err.Headers)
	case s == http.StatusForbidden:
		return Network("GitHub API: HTTP %d %s", s, msg).WithFix(AuthFix)
	case s >= 500:
		return Network("GitHub API unavailable (HTTP %d); retry later", s)
	default:
		return Usage("GitHub API: HTTP %d %s", s, msg)
	}
}

// isRateLimit tells a primary or secondary rate limit from a permission
// problem: both come as HTTP 403.
func isRateLimit(err *api.HTTPError) bool {
	if err.Headers.Get("X-RateLimit-Remaining") == "0" || IsSecondaryRateLimit(err) {
		return true
	}
	msg := strings.ToLower(err.Message)
	return strings.Contains(msg, "rate limit") || strings.Contains(msg, "abuse detection")
}

// IsSecondaryRateLimit reports an HTTP 403 or 429 for GitHub's secondary
// rate limit: a Retry-After header or a message saying so, with requests
// left. The primary limit (none left) resets in up to an hour, so only a
// secondary one is worth retrying after a pause.
func IsSecondaryRateLimit(err *api.HTTPError) bool {
	if err.StatusCode != http.StatusForbidden && err.StatusCode != http.StatusTooManyRequests {
		return false
	}
	if err.Headers.Get("X-RateLimit-Remaining") == "0" {
		return false
	}
	return err.Headers.Get("Retry-After") != "" ||
		strings.Contains(strings.ToLower(err.Message), "secondary rate limit")
}

// RetryAfter returns the seconds of a Retry-After header, 0 when it is
// absent, not a number or not positive.
func RetryAfter(header string) int {
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs > 0 {
		return secs
	}
	return 0
}

// now is the clock of rateLimited, replaced in tests.
var now = time.Now

// rateLimited reports a rate limit (primary or secondary) with how long to
// wait: the Retry-After header in h, else, for the primary limit (no requests
// left), the time until X-RateLimit-Reset, else a few minutes. h may be nil
// (a GraphQL answer carries no headers here).
func rateLimited(h http.Header) *Error {
	wait := contentLimitWait
	if secs := RetryAfter(h.Get("Retry-After")); secs > 0 {
		wait = strconv.Itoa(secs) + "s"
	} else if h.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil {
			if secs := reset - now().Unix(); secs > 0 {
				wait = formatWait(secs)
			}
		}
	}
	return waitThenRerun(Network("GitHub API rate limit exceeded"), wait)
}

// formatWait writes secs seconds as "45s", or in whole minutes (rounded up)
// from two minutes on.
func formatWait(secs int64) string {
	if secs < 120 {
		return strconv.FormatInt(secs, 10) + "s"
	}
	return strconv.FormatInt((secs+59)/60, 10) + "m"
}

// waitThenRerun sets the wait and the fix of a rate limit error e.
func waitThenRerun(e *Error, wait string) *Error {
	e.Wait = wait
	return e.WithFix("wait " + wait + ", then re-run the same command")
}

// contentLimitWait is how long to wait after a rate limit when GitHub does
// not say: it gives no Retry-After inside a GraphQL answer, and reports of
// the limit clearing range from a minute to several.
const contentLimitWait = "a few minutes"

// ContentLimited reports GitHub's secondary rate limit on creating content
// (issues, comments, pull requests): exit code 4, with how long to wait
// from retryAfter (seconds) when it is known. Mutations are never resent
// (see internal/graph), so the fix is a later re-run.
func ContentLimited(retryAfter string) *Error {
	wait := contentLimitWait
	if secs := RetryAfter(retryAfter); secs > 0 {
		wait = strconv.Itoa(secs) + "s"
	}
	return waitThenRerun(Network("GitHub's limit on creating issues and comments was hit"), wait)
}

// IsContentLimit reports whether msg, an error message from GitHub, is
// about its secondary rate limit on creating content. GitHub gives this
// limit no type of its own: in a GraphQL answer (HTTP 200) it is an error
// of the generic type UNPROCESSABLE with the message "was submitted too
// quickly", over HTTP a 403 "You have exceeded a secondary rate limit and
// have been temporarily blocked from content creation". So the text is
// the only sign; the phrases are GitHub's own.
func IsContentLimit(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "submitted too quickly") || strings.Contains(msg, "content creation")
}

// GraphQLLimit returns the error for a rate limit among the GraphQL errors
// of err, nil when none is: RATE_LIMITED (the primary limit), a content
// creation limit (IsContentLimit), or a secondary rate limit reported by
// its message.
func GraphQLLimit(err *api.GraphQLError) *Error {
	if err == nil {
		return nil
	}
	for _, item := range err.Errors {
		if item.Type == "RATE_LIMITED" {
			return rateLimited(nil)
		}
	}
	for _, item := range err.Errors {
		msg := strings.ToLower(item.Message)
		switch {
		case IsContentLimit(msg):
			return ContentLimited("")
		case strings.Contains(msg, "secondary rate limit"), strings.Contains(msg, "abuse detection"):
			return rateLimited(nil)
		}
	}
	return nil
}

// MapGraphQL maps GraphQL errors by type, looking at all of them: rate
// limits first (GraphQLLimit), then missing scopes, then other, then
// access denied, then not found. Anything else is "GitHub API rejected the
// request". GitHub's messages are never printed, since they can carry
// queries, field names and node IDs; only the scope names are taken from
// them. other, when set, is the caller's own cases (nil: none of them).
func MapGraphQL(err *api.GraphQLError, other func(*api.GraphQLError) *Error) *Error {
	if e := GraphQLLimit(err); e != nil {
		return e
	}
	if item, ok := firstOfType(err, "INSUFFICIENT_SCOPES"); ok {
		return Network("GitHub token is missing a required scope%s", scopesHint(item.Message)).WithFix(AuthFix)
	}
	if other != nil {
		if e := other(err); e != nil {
			return e
		}
	}
	if _, ok := firstOfType(err, "FORBIDDEN"); ok {
		return Network("GitHub denied access; the token may lack the project scope or access to the owner").WithFix(AuthFix)
	}
	if _, ok := firstOfType(err, "NOT_FOUND"); ok {
		return NotFound("GitHub API: not found")
	}
	return Usage("GitHub API rejected the request")
}

func firstOfType(err *api.GraphQLError, t string) (api.GraphQLErrorItem, bool) {
	for _, item := range err.Errors {
		if item.Type == t {
			return item, true
		}
	}
	return api.GraphQLErrorItem{}, false
}

// fromGraphQL maps a GraphQL error that did not come through
// internal/graph's client, which adds its own cases (only tests and fakes
// send such errors), in the same words.
func fromGraphQL(err *api.GraphQLError) *Error {
	return MapGraphQL(err, nil)
}

// scopesHint extracts "['read:project']" from GitHub's INSUFFICIENT_SCOPES
// message ("... requires one of the following scopes: ['read:project'], but
// your token has only been granted ...") as " (read:project)", or "" when
// it cannot tell.
func scopesHint(msg string) string {
	const marker = "following scopes: ["
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(marker):]
	j := strings.IndexByte(rest, ']')
	if j < 0 {
		return ""
	}
	var scopes []string
	for s := range strings.SplitSeq(rest[:j], ",") {
		s = strings.Trim(strings.TrimSpace(s), `'"`)
		if s == "" || strings.ContainsAny(s, " \t") || len(s) > 40 {
			return ""
		}
		scopes = append(scopes, s)
	}
	if len(scopes) == 0 {
		return ""
	}
	return " (" + strings.Join(scopes, " or ") + ")"
}

var (
	// nodeID matches GitHub global node IDs such as PVTF_lADOBn… or I_kwDO…;
	// the suffix check in hasNodeID tells them from words like NOT_FOUND.
	nodeID = regexp.MustCompile(`\b[A-Z]{1,8}_[A-Za-z0-9_-]{6,}`)
	// legacyID matches old base64 node IDs such as MDU6SXNzdWUx….
	legacyID = regexp.MustCompile(`\bMD[A-Za-z0-9+/]{10,}={0,2}`)
)

func hasNodeID(s string) bool {
	if legacyID.MatchString(s) {
		return true
	}
	for _, m := range nodeID.FindAllString(s, -1) {
		_, suffix, _ := strings.Cut(m, "_")
		if strings.ContainsAny(suffix, "abcdefghijklmnopqrstuvwxyz0123456789") {
			return true
		}
	}
	return false
}

// safeMessage returns the first line of an API message, or fallback when it
// is empty or mentions a node ID: agents never see IDs.
func safeMessage(msg, fallback string) string {
	if msg = FirstLine(msg); msg == "" || hasNodeID(msg) {
		return fallback
	}
	return msg
}
