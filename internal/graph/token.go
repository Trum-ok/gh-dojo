package graph

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/cli/go-gh/v2/pkg/auth"

	"github.com/Trum-ok/gh-dojo/internal/render"
)

// ScopeProject is the token scope gh-dojo needs to write to boards;
// ScopeReadProject is enough to read them.
const (
	ScopeProject     = "project"
	ScopeReadProject = "read:project"
)

// QueryHeaders is Query that also returns the HTTP headers of the response
// it decoded (of the last attempt), nil when no response came. GitHub lists
// a classic token's scopes in X-OAuth-Scopes (see Scopes).
func (c *Client) QueryHeaders(ctx context.Context, query string, vars map[string]any, out any) (http.Header, error) {
	var h http.Header
	err := c.do(context.WithValue(ctx, headersKey{}, &h), query, vars, out, false)
	return h, err
}

// Scopes returns the token scopes GitHub listed in response headers h.
// listed is false when GitHub listed none: fine-grained and app tokens
// have permissions instead of scopes.
func Scopes(h http.Header) (scopes []string, listed bool) {
	v, ok := h[http.CanonicalHeaderKey("X-OAuth-Scopes")]
	if !ok {
		return nil, false
	}
	for s := range strings.SplitSeq(strings.Join(v, ","), ",") {
		if s = strings.TrimSpace(s); s != "" {
			scopes = append(scopes, s)
		}
	}
	slices.Sort(scopes)
	return scopes, true
}

// ScopeChecker tells ahead of a write whether the token may write to
// boards. *Client implements it; a Doer that does not (a test fake) is not
// checked.
type ScopeChecker interface {
	// CheckProjectScope returns the error a write to a board would fail
	// with, nil when it may succeed or the client cannot tell.
	CheckProjectScope() error
}

// CheckProjectScope returns an error when the last response that listed the
// token's scopes listed no project scope: a classic token that can read
// boards (read:project, or a public one) but whose first write to one would
// fail with INSUFFICIENT_SCOPES, maybe after other writes of the same
// mutation took effect (#65). It is the same error, with the same fix, as
// that write's. nil when no response listed scopes: fine-grained and app
// tokens have permissions instead, and their writes are not checked ahead.
// The scopes come from the responses the command reads anyway, so the check
// costs no request.
func (c *Client) CheckProjectScope() error {
	c.scopesMu.Lock()
	scopes, listed := c.scopes, c.listed
	c.scopesMu.Unlock()
	if !listed || slices.Contains(scopes, ScopeProject) {
		return nil
	}
	c.once.Do(c.init)
	return render.Network("GitHub token is missing a required scope (%s)", ScopeProject).WithFix(c.authFix())
}

// noteScopes remembers the token's scopes from response headers h, if they
// list them; nil h (no response) changes nothing.
func (c *Client) noteScopes(h http.Header) {
	if h == nil {
		return
	}
	scopes, listed := Scopes(h)
	if !listed {
		return
	}
	c.scopesMu.Lock()
	c.scopes, c.listed = scopes, true
	c.scopesMu.Unlock()
}

// Host returns the GitHub host the client talks to.
func (c *Client) Host() string {
	if c.opts.Host != "" {
		return c.opts.Host
	}
	host, _ := auth.DefaultHost()
	return host
}

// TokenEnv names the environment variable the token comes from (GH_TOKEN,
// GITHUB_TOKEN, ...), "" when it comes from gh or Options.AuthToken. The
// fixes of auth errors depend on it: gh auth refresh cannot change it.
func (c *Client) TokenEnv() string {
	c.once.Do(c.init)
	return c.tokenEnv()
}

// AuthFix is the command that gives the token the project scope: gh auth
// refresh, or setting the environment variable the token came from, which
// gh auth refresh cannot change.
func (c *Client) AuthFix() string {
	c.once.Do(c.init)
	return c.authFix()
}

// headersKey holds the *http.Header that headerSink fills.
type headersKey struct{}

// headerSink is the transport under go-gh's: it copies the response
// headers into the request context's *http.Header, if any.
type headerSink struct{ next http.RoundTripper }

func (s headerSink) RoundTrip(req *http.Request) (*http.Response, error) {
	next := s.next
	if next == nil {
		next = http.DefaultTransport // as go-gh does without a transport
	}
	resp, err := next.RoundTrip(req)
	if p, ok := req.Context().Value(headersKey{}).(*http.Header); ok && resp != nil {
		*p = resp.Header.Clone()
	}
	return resp, err
}
