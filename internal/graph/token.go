package graph

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/cli/go-gh/v2/pkg/auth"
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
