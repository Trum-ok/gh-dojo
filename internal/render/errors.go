// Package render turns command results into the short, predictable output
// agents rely on: exit codes, error blocks, confirmation lines, warnings and
// truncation notes.
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

	"github.com/cli/go-gh/v2/pkg/api"
)

// Code is a process exit code. The values are part of the CLI contract.
type Code int

const (
	ExitOK       Code = 0 // everything applied
	ExitUsage    Code = 1 // bad arguments or config: fix per the error text
	ExitDenied   Code = 2 // a process check refused: ask a human, don't work around it
	ExitNotFound Code = 3 // issue or board not found: check the number
	ExitNetwork  Code = 4 // network, rate limit or auth: retry later or refresh auth
	ExitPartial  Code = 5 // partially applied: re-run the same command to finish
)

const (
	maxWhatLines = 3   // the error block is at most 5 lines: what (≤3), allowed, fix
	maxLineRunes = 200 // long lines are cut with "…"
	maxFixRunes  = 400 // a longer fix is dropped: a cut command would be wrong
	maxAllowed   = 20  // longer allowed lists end with "and N more"
)

// AuthFix is the fix command for missing or insufficient GitHub auth.
const AuthFix = "gh auth refresh -s project"

// InternalFix is the hint printed for internal errors. It has no side
// effects: an agent should not file issues on its own.
const InternalFix = "re-run the command; if it repeats, report it to the user"

// Error is a user-facing error: what is wrong, which values are allowed and
// which command fixes it. Build one with a constructor such as Usage or
// NotFound and refine it with WithAllowed, WithFix and WithCause.
type Error struct {
	Code    Code
	What    string   // what is wrong; one line, at most 3
	Allowed []string // optional: the values that would have been accepted
	Fix     string   // optional: a command to run next; never cut, dropped if too long
	Cause   error    // optional: underlying error, never printed
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

// Internal reports a bug in gh-dojo itself, such as a recovered panic
// (exit code 1). The message makes clear the arguments are not at fault.
func Internal(format string, args ...any) *Error {
	return New(ExitUsage, "internal error (not your arguments): "+format, args...).WithFix(InternalFix)
}

// WithAllowed sets the accepted values and returns e.
func (e *Error) WithAllowed(values ...string) *Error { e.Allowed = values; return e }

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
// value is put on one line. The fix line is never cut, since a cut command
// would be wrong; a fix longer than maxFixRunes is dropped instead.
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
		lines = append(lines, clip(l, maxLineRunes))
	}
	if len(e.Allowed) > 0 {
		lines = append(lines, clip("allowed: "+joinAllowed(e.Allowed), maxLineRunes*2))
	}
	if fix := oneLine(e.Fix); fix != "" && runeLen(fix) <= maxFixRunes {
		lines = append(lines, "fix: "+fix)
	}
	return strings.Join(lines, "\n")
}

func joinAllowed(values []string) string {
	shown := make([]string, 0, min(len(values), maxAllowed))
	for _, v := range values[:min(len(values), maxAllowed)] {
		shown = append(shown, oneLine(v))
	}
	s := strings.Join(shown, ", ")
	if n := len(values) - len(shown); n > 0 {
		s += fmt.Sprintf(" and %d more", n)
	}
	return s
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
		return Network("network error: %s", firstLine(err.Error())).WithCause(err)
	}
	return Usage("%s", firstLine(err.Error())).WithCause(err)
}

func fromHTTP(err *api.HTTPError) *Error {
	s := err.StatusCode
	msg := safeMessage(err.Message, http.StatusText(s))
	switch {
	case s == http.StatusNotFound:
		return NotFound("GitHub API: not found")
	case s == http.StatusUnauthorized:
		return Network("GitHub API: %s", msg).WithFix(AuthFix)
	case s == http.StatusTooManyRequests, s == http.StatusForbidden && isRateLimit(err):
		return rateLimited(err.Headers.Get("Retry-After"))
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
	if err.Headers.Get("X-RateLimit-Remaining") == "0" || err.Headers.Get("Retry-After") != "" {
		return true
	}
	msg := strings.ToLower(err.Message)
	return strings.Contains(msg, "rate limit") || strings.Contains(msg, "abuse detection")
}

func rateLimited(retryAfter string) *Error {
	if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs > 0 {
		return Network("GitHub API rate limit exceeded; retry in %ds", secs)
	}
	return Network("GitHub API rate limit exceeded; retry later")
}

// fromGraphQL maps GraphQL errors by type, looking at all of them: rate
// limits first, then missing scopes, then not found. Messages of other types
// are never printed, since they can carry queries, field names and node IDs.
func fromGraphQL(err *api.GraphQLError) *Error {
	has := func(types ...string) (api.GraphQLErrorItem, bool) {
		for _, item := range err.Errors {
			for _, t := range types {
				if item.Type == t {
					return item, true
				}
			}
		}
		return api.GraphQLErrorItem{}, false
	}
	if _, ok := has("RATE_LIMITED"); ok {
		return rateLimited("")
	}
	if item, ok := has("INSUFFICIENT_SCOPES"); ok {
		return Network("%s", safeMessage(item.Message, "GitHub token lacks a required scope")).WithFix(AuthFix)
	}
	if item, ok := has("FORBIDDEN"); ok {
		return Network("%s", safeMessage(item.Message, "GitHub API: access denied")).WithFix(AuthFix)
	}
	if item, ok := has("NOT_FOUND"); ok {
		return NotFound("%s", safeMessage(item.Message, "GitHub API: not found"))
	}
	return Usage("GitHub API rejected the request")
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
	if msg = firstLine(msg); msg == "" || hasNodeID(msg) {
		return fallback
	}
	return msg
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}
