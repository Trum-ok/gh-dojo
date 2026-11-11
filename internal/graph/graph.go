package graph

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/auth"

	"github.com/Trum-ok/gh-dojo/internal/render"
)

const (
	// MaxAttempts is how many times a query is sent before giving up on
	// 5xx responses and secondary rate limits. Mutations are sent once.
	MaxAttempts = 3

	baseDelay      = time.Second
	maxRetryAfter  = 60 * time.Second
	defaultTimeout = 30 * time.Second
)

// LoginFix is the fix command when gh has no token at all.
const LoginFix = "gh auth login -s project"

// Doer runs one GraphQL operation. *Client implements it; packages that only
// read or write through GraphQL should accept a Doer.
type Doer interface {
	Do(ctx context.Context, query string, vars map[string]any, out any) error
}

// Options configure a Client. The zero value talks to the gh default host
// with gh's token (or GH_TOKEN), which is what commands want.
type Options struct {
	// Host is the GitHub host, e.g. "github.com". Default: gh's default host.
	Host string
	// AuthToken overrides the token gh would use. Tests set a fake one.
	AuthToken string
	// Transport overrides the HTTP transport. Tests set a graphtest server.
	Transport http.RoundTripper
	// Sleep waits between retries. Default: a timer that honours ctx.
	// Tests set a no-op so retries are instant.
	Sleep func(ctx context.Context, d time.Duration) error
	// Timeout limits each HTTP request. Default: 30s.
	Timeout time.Duration
}

// Client is a GraphQL client for the GitHub API with retries and errors
// mapped to internal/render. It is safe for concurrent use. Auth is resolved
// lazily on the first request, so building a Client is free.
type Client struct {
	opts     Options
	requests atomic.Int64

	once  sync.Once
	gql   *api.GraphQLClient
	token string
	err   error
}

// New returns a Client. It never touches the network or gh config; auth
// problems surface from the first Do as exit-code-4 errors.
func New(opts Options) *Client {
	if opts.Sleep == nil {
		opts.Sleep = sleep
	}
	if opts.Timeout == 0 {
		opts.Timeout = defaultTimeout
	}
	return &Client{opts: opts}
}

// Requests reports how many HTTP requests the client has sent, retries
// included. Tests use it to prove request budgets.
func (c *Client) Requests() int { return int(c.requests.Load()) }

func (c *Client) init() {
	host := c.opts.Host
	if host == "" {
		host, _ = auth.DefaultHost()
	}
	token := c.opts.AuthToken
	if token == "" {
		token, _ = auth.TokenForHost(host)
	}
	if token == "" {
		c.err = render.Network("not logged in to GitHub (%s)", host).WithFix(LoginFix)
		return
	}
	c.token = token
	gql, err := api.NewGraphQLClient(api.ClientOptions{
		Host:      host,
		AuthToken: token,
		Transport: c.opts.Transport,
		Timeout:   c.opts.Timeout,
	})
	if err != nil {
		c.err = render.Usage("GitHub client: %s", c.scrub(err.Error()))
		return
	}
	c.gql = gql
}

// Do sends one GraphQL operation and decodes its "data" into out (a pointer
// to a struct with json tags, or nil).
//
// The operation kind is read from the query text. Queries are retried on 5xx
// responses and secondary rate limits with exponential backoff, at most
// MaxAttempts sends in total. Mutations are never retried: after a failed
// mutation the caller must re-read state before deciding to send it again.
//
// Every error is a *render.Error with the right exit code and a short
// message; the original error is kept as its Cause, so errors.As(err,
// new(*api.GraphQLError)) still works. On a GraphQL error out may hold
// partial data. Use named operations (query BoardSchema(...)): the name keys
// the recorded test fixtures.
func (c *Client) Do(ctx context.Context, query string, vars map[string]any, out any) error {
	c.once.Do(c.init)
	if c.err != nil {
		return c.err
	}
	kind, _ := ParseOperation(query)
	mutation := kind == "mutation"
	for attempt := 1; ; attempt++ {
		c.requests.Add(1)
		err := c.gql.DoWithContext(ctx, query, vars, out)
		if err == nil {
			return nil
		}
		delay, retry := retryDelay(err, attempt)
		if !retry || mutation || attempt >= MaxAttempts {
			return c.mapError(err, mutation)
		}
		if serr := c.opts.Sleep(ctx, delay); serr != nil {
			return c.mapError(err, mutation)
		}
	}
}

// retryDelay says whether a failed attempt (1-based) may be retried and
// after which pause.
func retryDelay(err error, attempt int) (time.Duration, bool) {
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		return 0, false
	}
	backoff := baseDelay << (attempt - 1)
	switch {
	case httpErr.StatusCode >= 500:
		return backoff, true
	case isSecondaryRateLimit(httpErr):
		if s := httpErr.Headers.Get("Retry-After"); s != "" {
			secs, perr := strconv.Atoi(strings.TrimSpace(s))
			if perr != nil || secs < 0 || time.Duration(secs)*time.Second > maxRetryAfter {
				return 0, false
			}
			return max(time.Duration(secs)*time.Second, backoff), true
		}
		return backoff, true
	}
	return 0, false
}

func isSecondaryRateLimit(e *api.HTTPError) bool {
	if e.StatusCode != http.StatusForbidden && e.StatusCode != http.StatusTooManyRequests {
		return false
	}
	if e.Headers.Get("X-RateLimit-Remaining") == "0" {
		return false // primary limit: resets in up to an hour, retrying is pointless
	}
	return e.Headers.Get("Retry-After") != "" ||
		strings.Contains(strings.ToLower(e.Message), "secondary rate limit")
}

func isPrimaryRateLimit(e *api.HTTPError) bool {
	return (e.StatusCode == http.StatusForbidden || e.StatusCode == http.StatusTooManyRequests) &&
		e.Headers.Get("X-RateLimit-Remaining") == "0"
}

// mapError turns a go-gh or transport error into a short *render.Error.
// Raw GraphQL JSON and the token never reach the message.
func (c *Client) mapError(err error, mutation bool) error {
	e := c.describe(err, mutation)
	e.What = c.scrub(e.What)
	e.Fix = c.scrub(e.Fix)
	return e.WithCause(err)
}

func (c *Client) describe(err error, mutation bool) *render.Error {
	var httpErr *api.HTTPError
	var gqlErr *api.GraphQLError
	switch {
	case errors.As(err, &httpErr):
		switch s := httpErr.StatusCode; {
		case s == http.StatusUnauthorized:
			return render.Network("GitHub rejected the token (HTTP 401)").WithFix(render.AuthFix)
		case isPrimaryRateLimit(httpErr):
			return render.Network("GitHub API rate limit exceeded; retry later")
		case isSecondaryRateLimit(httpErr):
			return render.Network("GitHub secondary rate limit hit; retry in a minute")
		case s >= 500 && mutation:
			return render.Network("GitHub API error (HTTP %d) during a write; it may have been applied, re-run the command to check", s)
		case s >= 500:
			return render.Network("GitHub API unavailable (HTTP %d); retry later", s)
		}
	case errors.As(err, &gqlErr) && len(gqlErr.Errors) > 0:
		item := gqlErr.Errors[0]
		switch item.Type {
		case "INSUFFICIENT_SCOPES":
			return render.Network("GitHub token is missing a required scope%s", scopesHint(item.Message)).WithFix(render.AuthFix)
		case "RATE_LIMITED":
			return render.Network("GitHub API rate limit exceeded; retry later")
		}
	case errors.Is(err, context.Canceled):
		return render.Network("request canceled")
	}
	return render.FromError(err)
}

// scopesHint extracts "['read:project']" from GitHub's INSUFFICIENT_SCOPES
// message ("... requires one of the following scopes: ['read:project'], but
// your token has only been granted ...") as " (read:project)".
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
	scopes := strings.NewReplacer("'", "", `"`, "", " ", "").Replace(rest[:j])
	if scopes == "" {
		return ""
	}
	return " (" + strings.ReplaceAll(scopes, ",", " or ") + ")"
}

func (c *Client) scrub(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[token]")
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ParseOperation returns the kind ("query", "mutation" or "subscription")
// and the name of the first operation in a GraphQL document. A shorthand
// query ("{ viewer { login } }") has kind "query" and an empty name.
// Fragment definitions before the operation are skipped.
func ParseOperation(doc string) (kind, name string) {
	depth := 0
	for s := skipIgnored(doc); s != ""; s = skipIgnored(s) {
		switch c := s[0]; {
		case c == '{':
			if depth == 0 {
				return "query", "" // shorthand query
			}
			depth++
			s = s[1:]
		case c == '}':
			depth--
			s = s[1:]
		case c == '"':
			s = skipString(s)
		case depth == 0 && isNameStart(c):
			word := leadingName(s)
			s = s[len(word):]
			switch word {
			case "query", "mutation", "subscription":
				return word, leadingName(skipIgnored(s))
			case "fragment": // skip "name on Type @directives" up to the body
				i := strings.IndexByte(s, '{')
				if i < 0 {
					return "query", ""
				}
				s, depth = s[i+1:], 1
			}
		default:
			s = s[1:]
		}
	}
	return "query", ""
}

func isNameStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func skipString(s string) string {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return s[i+1:]
		}
	}
	return ""
}

func skipIgnored(s string) string {
	for s != "" {
		switch s[0] {
		case ' ', '\t', '\n', '\r', ',':
			s = s[1:]
		case '#':
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
			} else {
				s = ""
			}
		default:
			return s
		}
	}
	return s
}

func leadingName(s string) string {
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return s[:i]
		}
	}
	return s
}
