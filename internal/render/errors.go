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
	maxAllowed   = 20  // longer allowed lists end with "and N more"
)

// AuthFix is the fix command for missing or insufficient GitHub auth.
const AuthFix = "gh auth refresh -s project"

// Error is a user-facing error: what is wrong, which values are allowed and
// which command fixes it. Build one with a constructor such as Usage or
// NotFound and refine it with WithAllowed, WithFix and WithCause.
type Error struct {
	Code    Code
	What    string   // what is wrong; one line, at most 3
	Allowed []string // optional: the values that would have been accepted
	Fix     string   // optional: a command to run next
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

// WithAllowed sets the accepted values and returns e.
func (e *Error) WithAllowed(values ...string) *Error { e.Allowed = values; return e }

// WithFix sets the command that fixes the problem and returns e.
func (e *Error) WithFix(cmd string) *Error { e.Fix = cmd; return e }

// WithCause records the underlying error for errors.Is/As and returns e.
// The cause is never printed.
func (e *Error) WithCause(err error) *Error { e.Cause = err; return e }

func (e *Error) Error() string { return e.What }

func (e *Error) Unwrap() error { return e.Cause }

// Text renders the error block: at most 5 lines, no trailing newline.
//
//	error: unknown status "Doing"
//	allowed: Backlog, In progress, Done
//	fix: gh dojo describe
func (e *Error) Text() string {
	what := strings.Split(strings.TrimSpace(e.What), "\n")
	if len(what) > maxWhatLines {
		what = what[:maxWhatLines]
	}
	lines := make([]string, 0, maxWhatLines+2)
	for i, l := range what {
		if i == 0 {
			l = "error: " + l
		}
		lines = append(lines, clip(strings.TrimRight(l, " \t\r"), maxLineRunes))
	}
	if len(e.Allowed) > 0 {
		lines = append(lines, clip("allowed: "+joinAllowed(e.Allowed), maxLineRunes*2))
	}
	if e.Fix != "" {
		lines = append(lines, clip("fix: "+oneLine(e.Fix), maxLineRunes))
	}
	return strings.Join(lines, "\n")
}

func joinAllowed(values []string) string {
	if len(values) <= maxAllowed {
		return strings.Join(values, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(values[:maxAllowed], ", "), len(values)-maxAllowed)
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
func FromError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
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
	case errors.As(err, &httpErr):
		return fromHTTP(httpErr).WithCause(err)
	case errors.As(err, &gqlErr):
		return fromGraphQL(gqlErr).WithCause(err)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr):
		return Network("network error: %s", firstLine(err.Error())).WithCause(err)
	}
	return Usage("%s", firstLine(err.Error())).WithCause(err)
}

func fromHTTP(err *api.HTTPError) *Error {
	msg := firstLine(err.Message)
	if msg == "" {
		msg = http.StatusText(err.StatusCode)
	}
	switch s := err.StatusCode; {
	case s == http.StatusNotFound:
		return NotFound("GitHub API: not found")
	case s == http.StatusUnauthorized:
		return Network("GitHub API: %s", msg).WithFix(AuthFix)
	case s == http.StatusForbidden, s == http.StatusTooManyRequests:
		if err.Headers.Get("X-RateLimit-Remaining") == "0" || s == http.StatusTooManyRequests {
			return Network("GitHub API rate limit exceeded; retry later")
		}
		return Network("GitHub API: HTTP %d %s", s, msg).WithFix(AuthFix)
	case s >= 500:
		return Network("GitHub API unavailable (HTTP %d); retry later", s)
	default:
		return Usage("GitHub API: HTTP %d %s", s, msg)
	}
}

func fromGraphQL(err *api.GraphQLError) *Error {
	if len(err.Errors) == 0 {
		return Network("GitHub API: empty GraphQL error")
	}
	item := err.Errors[0]
	msg := firstLine(item.Message)
	switch item.Type {
	case "NOT_FOUND":
		return NotFound("%s", msg)
	case "INSUFFICIENT_SCOPES", "FORBIDDEN":
		return Network("%s", msg).WithFix(AuthFix)
	case "RATE_LIMITED":
		return Network("GitHub API rate limit exceeded; retry later")
	default:
		return Usage("GitHub API: %s", msg)
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}
