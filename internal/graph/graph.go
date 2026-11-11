package graph

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
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
	// MaxRetryWait caps the total pause between query retries: an agent
	// would rather get exit code 4 than wait a minute.
	MaxRetryWait = 10 * time.Second

	baseDelay      = time.Second
	defaultTimeout = 30 * time.Second
)

// LoginFix is the fix command when gh has no token at all.
const LoginFix = "gh auth login -s project"

// Querier runs read-only GraphQL queries.
type Querier interface {
	Query(ctx context.Context, query string, vars map[string]any, out any) error
}

// Doer runs GraphQL queries and mutations. *Client implements it; packages
// should accept the narrowest interface they need.
type Doer interface {
	Querier
	Mutate(ctx context.Context, mutation string, vars map[string]any, out any) error
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
	// Tests set one that returns at once.
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

	once        sync.Once
	gql         *api.GraphQLClient
	token       string
	tokenSource string // env var name, "oauth_token", "gh" or "" (Options.AuthToken)
	err         error
}

// New returns a Client. It never touches the network or gh config; auth
// problems surface from the first request as exit-code-4 errors.
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
		token, c.tokenSource = auth.TokenForHost(host)
	}
	if token == "" {
		c.err = render.Network("not logged in to GitHub (%s)", host).WithFix(LoginFix)
		return
	}
	c.token = token
	gql, err := api.NewGraphQLClient(api.ClientOptions{
		Host:      host,
		AuthToken: token,
		Transport: headerSink{c.opts.Transport}, // for QueryHeaders
		Timeout:   c.opts.Timeout,
	})
	if err != nil {
		c.err = render.Internal("GitHub client setup failed").WithCause(err)
		return
	}
	c.gql = gql
}

// Query sends a GraphQL query and decodes its "data" into out (a pointer to
// a struct with json tags, or nil). It is retried on 5xx responses and
// secondary rate limits with exponential backoff (honouring Retry-After), at
// most MaxAttempts sends and MaxRetryWait of pauses in total.
//
// Every error is a *render.Error with the right exit code and a short
// message that never contains raw GraphQL text, node IDs, the endpoint or
// the token. The original error is its Cause, so errors.As(err,
// new(*api.GraphQLError)) and IsStaleID still work. On a GraphQL error out
// may hold partial data.
//
// Name every operation and put it first in the document
// (query BoardSchema(...) {...} fragment ...): graphtest keys fixtures by it.
func (c *Client) Query(ctx context.Context, query string, vars map[string]any, out any) error {
	return c.do(ctx, query, vars, out, false)
}

// Mutate sends a GraphQL mutation once; it is never retried. When the
// outcome is unknown (5xx, timeout, dropped connection, garbled response)
// the error says the write may have been applied: the caller must re-read
// state before deciding to send it again.
func (c *Client) Mutate(ctx context.Context, mutation string, vars map[string]any, out any) error {
	return c.do(ctx, mutation, vars, out, true)
}

func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any, mutation bool) error {
	c.once.Do(c.init)
	if c.err != nil {
		return c.err
	}
	if err := ctx.Err(); err != nil {
		return c.mapError(err, false) // nothing sent yet
	}
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		c.requests.Add(1)
		err := c.gql.DoWithContext(ctx, query, vars, out)
		if err == nil {
			return nil
		}
		delay, retry := retryDelay(err, attempt)
		if mutation || !retry || attempt >= MaxAttempts || waited+delay > MaxRetryWait {
			return c.mapError(err, mutation)
		}
		if serr := c.opts.Sleep(ctx, delay); serr != nil {
			return c.mapError(serr, false) // canceled during backoff
		}
		waited += delay
	}
}

// retryDelay says whether a failed query attempt (1-based) may be retried
// and after which pause.
func retryDelay(err error, attempt int) (time.Duration, bool) {
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) || httpErr == nil {
		return 0, false
	}
	if httpErr.StatusCode < 500 && !isSecondaryRateLimit(httpErr) {
		return 0, false
	}
	delay := baseDelay << (attempt - 1)
	if s := httpErr.Headers.Get("Retry-After"); s != "" {
		if secs, perr := strconv.Atoi(strings.TrimSpace(s)); perr == nil && secs >= 0 {
			delay = max(delay, time.Duration(secs)*time.Second)
		}
	}
	return delay, true
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

// mapError turns a go-gh or transport error into a short *render.Error,
// starting from render's mapping and tightening it for GraphQL.
func (c *Client) mapError(err error, mutation bool) error {
	e := c.describe(err, mutation)
	switch e.Fix {
	case render.AuthFix:
		e.Fix = c.authFix()
	case SSOFix:
		e.Fix = c.ssoFix()
	}
	e.What = c.scrub(e.What)
	e.Fix = c.scrub(e.Fix)
	if e.Cause == ErrUnknownOutcome {
		return e.WithCause(errors.Join(ErrUnknownOutcome, err))
	}
	return e.WithCause(err)
}

func (c *Client) describe(err error, mutation bool) *render.Error {
	var httpErr *api.HTTPError
	var gqlErr *api.GraphQLError
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &httpErr) && httpErr != nil:
		switch s := httpErr.StatusCode; {
		case s == http.StatusUnauthorized:
			return render.Network("GitHub rejected the token (HTTP 401)").WithFix(render.AuthFix)
		case s >= 500 && mutation:
			return mayHaveApplied("HTTP " + strconv.Itoa(s))
		}
		return render.FromError(httpErr)
	case errors.As(err, &gqlErr) && gqlErr != nil:
		if unfinished(gqlErr) {
			if mutation {
				return mayHaveApplied("GitHub did not finish it")
			}
			return render.Network("GitHub did not finish the request (it may have timed out); retry later")
		}
		return describeGraphQL(gqlErr)
	case errors.As(err, &typeErr):
		return render.Internal("unexpected shape of a GitHub API response")
	case errors.As(err, &syntaxErr): // includes an empty or HTML 200 body
		if mutation {
			return mayHaveApplied("garbled response")
		}
		return render.Network("GitHub API returned an invalid response; retry later")
	case errors.Is(err, context.Canceled):
		if mutation {
			return mayHaveApplied("interrupted")
		}
		return render.FromError(context.Canceled)
	}
	reason := transportReason(err)
	if mutation {
		return mayHaveApplied(reason)
	}
	return render.Network("cannot reach GitHub (%s); retry later", reason)
}

// describeGraphQL maps GraphQL errors without printing their messages, which
// carry queries, field names and node IDs.
func describeGraphQL(err *api.GraphQLError) *render.Error {
	for _, item := range err.Errors {
		if item.Type == "" && item.Extensions["code"] != nil {
			// Schema validation ("Field 'x' doesn't exist", "Variable $y is
			// declared but not used"): gh-dojo sent a bad query.
			return render.Internal("GitHub rejected gh-dojo's GraphQL query")
		}
	}
	has := func(t string) (api.GraphQLErrorItem, bool) {
		for _, item := range err.Errors {
			if item.Type == t {
				return item, true
			}
		}
		return api.GraphQLErrorItem{}, false
	}
	if _, ok := has("RATE_LIMITED"); ok {
		return render.FromError(err)
	}
	if item, ok := has("INSUFFICIENT_SCOPES"); ok {
		return render.Network("GitHub token is missing a required scope%s", scopesHint(item.Message)).WithFix(render.AuthFix)
	}
	for _, item := range err.Errors {
		if samlFailure(item) {
			return render.Network("GitHub denied access: the organization enforces SAML single sign-on and the token is not authorized for it").
				WithFix(SSOFix)
		}
		if strings.Contains(strings.ToLower(item.Message), "oauth app access restrictions") {
			return render.Network("GitHub denied access: the organization restricts OAuth apps and has not approved GitHub CLI").
				WithFix(OAuthRestrictedFix)
		}
	}
	if _, ok := has("FORBIDDEN"); ok {
		return render.Network("GitHub denied access; the token may lack the project scope or access to the owner").WithFix(render.AuthFix)
	}
	if _, ok := has("NOT_FOUND"); ok {
		return render.NotFound("GitHub API: not found")
	}
	return render.New(render.ExitUsage, "GitHub API rejected the request")
}

// SSOFix is the fix when an organization's SAML single sign-on blocks gh's
// token: refreshing it opens GitHub's authorization page, where the token
// is authorized for the organization's SSO too. A token from the
// environment gets another fix (see Client.ssoFix).
const SSOFix = "gh auth refresh -s project, and authorize the organization's SSO on the page it opens"

// OAuthRestrictedFix is the fix when an organization's OAuth app access
// restrictions block gh's token; personal access tokens are not subject to
// them.
const OAuthRestrictedFix = "ask an organization owner to approve GitHub CLI, or set GH_TOKEN to a personal access token with the project scope"

// samlFailure reports the error GitHub returns for an organization's
// resource when the token is not authorized for its SAML single sign-on:
// extensions.saml_failure, with a message about "SAML enforcement".
func samlFailure(item api.GraphQLErrorItem) bool {
	if v, ok := item.Extensions["saml_failure"].(bool); ok && v {
		return true
	}
	return strings.Contains(strings.ToLower(item.Message), "saml enforcement")
}

// ErrUnknownOutcome is in the chain of a Mutate error whose outcome is
// unknown (5xx, timeout, dropped connection, garbled response): the write
// may have been applied. Test for it with errors.Is; re-read state before
// sending the write again.
var ErrUnknownOutcome = errors.New("the write may have been applied")

// unfinished reports a GraphQL error that is GitHub's, not the query's: no
// type, no extensions.code and no path, or a message about a timeout
// ("Something went wrong while executing your query. This may be the
// result of a timeout..."). It is worth retrying, unlike a rejected query.
func unfinished(err *api.GraphQLError) bool {
	for _, item := range err.Errors {
		if item.Type != "" || item.Extensions["code"] != nil {
			return false
		}
		if len(item.Path) > 0 && !strings.Contains(strings.ToLower(item.Message), "timeout") {
			return false
		}
	}
	return len(err.Errors) > 0
}

func mayHaveApplied(reason string) *render.Error {
	return render.Network("GitHub write failed (%s); it may have been applied, re-run the command to check", reason).
		WithCause(ErrUnknownOutcome) // mapError keeps it next to the original error
}

// transportReason describes a failed round trip without the request URL.
func transportReason(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	msg := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if msg == "" || strings.Contains(msg, "://") {
		return "connection failed"
	}
	return msg
}

// authFix is the fix for a token GitHub rejects or that lacks a scope.
// gh auth refresh can't change a token set in the environment.
func (c *Client) authFix() string {
	if env := c.tokenEnv(); env != "" {
		return "set " + env + " to a token with the project scope"
	}
	return render.AuthFix
}

// ssoFix is the fix for a token an organization's SAML SSO blocks. A token
// from the environment is authorized on GitHub's token settings page.
func (c *Client) ssoFix() string {
	if env := c.tokenEnv(); env != "" {
		return "authorize " + env + " for the organization's SSO (Configure SSO in GitHub's token settings)"
	}
	return SSOFix
}

// tokenEnv names the environment variable the token came from, "" when
// it came from gh (its config or keyring) or Options.AuthToken.
func (c *Client) tokenEnv() string {
	switch c.tokenSource {
	case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN":
		return c.tokenSource
	}
	return ""
}

// IsStaleID reports whether GitHub rejected an ID gh-dojo sent: a node ID
// that no longer resolves, or an option or iteration ID that does not
// belong to the field. Cached board schemas cause these; callers should
// drop the cache, re-read and try once more.
func IsStaleID(err error) bool {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil {
		return false
	}
	for _, item := range gqlErr.Errors {
		msg := strings.ToLower(item.Message)
		switch {
		case item.Type == "NOT_FOUND" && strings.Contains(msg, "global id"):
			return true
		case strings.Contains(msg, "does not belong to"):
			return true
		case containsAny(msg, "optionid", "option id", "iterationid", "iteration id", "fieldid", "field id") &&
			containsAny(msg, "invalid", "not found", "does not exist", "doesn't exist", "could not resolve"):
			return true
		}
	}
	return false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
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
