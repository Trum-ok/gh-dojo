package graph_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

const viewerQuery = `query Viewer { viewer { login } }`

const setStatus = `mutation SetStatus($project: ID!, $item: ID!, $field: ID!, $option: String!) {
  updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, value: {singleSelectOptionId: $option}}) {
    projectV2Item { id }
  }
}`

var setStatusVars = map[string]any{
	"project": "PVT_kwHOBFGd5c4BrMYF", "item": "PVTI_lAHOBFGd5c4BrMYFzhH6JM0x",
	"field": "PVTSSF_lAHOBFGd5c4BrMYFzhpgKzQ", "option": "47fc9ee4",
}

type viewer struct {
	Viewer struct {
		Login string `json:"login"`
	} `json:"viewer"`
}

func fixture(name string) string { return "testdata/graphql/" + name + ".json" }

var _ graph.Doer = (*graph.Client)(nil)

func TestRetryOn5xxThenSuccess(t *testing.T) {
	srv := graphtest.New(t, fixture("retry_502"))
	var out viewer
	if err := srv.Client().Query(context.Background(), viewerQuery, nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.Viewer.Login != "Trum-ok" {
		t.Errorf("login = %q", out.Viewer.Login)
	}
	if n := srv.Requests(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	if got, want := srv.Sleeps(), []time.Duration{time.Second}; !slices.Equal(got, want) {
		t.Errorf("sleeps = %v, want %v", got, want)
	}
}

func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	srv := graphtest.New(t, fixture("always_503"))
	c := srv.Client()
	err := c.Query(context.Background(), viewerQuery, nil, nil)
	assertError(t, err, render.ExitNetwork, "errors/always_503")
	if n := srv.Requests(); n != graph.MaxAttempts {
		t.Errorf("requests = %d, want %d", n, graph.MaxAttempts)
	}
	if n := c.Requests(); n != graph.MaxAttempts {
		t.Errorf("client.Requests() = %d, want %d", n, graph.MaxAttempts)
	}
	if got, want := srv.Sleeps(), []time.Duration{time.Second, 2 * time.Second}; !slices.Equal(got, want) {
		t.Errorf("sleeps = %v, want exponential %v", got, want)
	}
}

func TestRetryAfterOn503(t *testing.T) {
	srv := graphtest.New(t, fixture("retry_after_503"))
	if err := srv.Client().Query(context.Background(), viewerQuery, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := srv.Sleeps(), []time.Duration{3 * time.Second}; !slices.Equal(got, want) {
		t.Errorf("sleeps = %v, want %v", got, want)
	}
}

func TestRetryWaitIsCapped(t *testing.T) {
	srv := graphtest.New(t, fixture("retry_after_long"))
	err := srv.Client().Query(context.Background(), viewerQuery, nil, nil)
	assertError(t, err, render.ExitNetwork, "errors/always_503")
	if srv.Requests() != 2 {
		t.Errorf("requests = %d, want 2 (a second 8s pause would pass %v)", srv.Requests(), graph.MaxRetryWait)
	}
	var total time.Duration
	for _, d := range srv.Sleeps() {
		total += d
	}
	if total > graph.MaxRetryWait {
		t.Errorf("total wait %v exceeds %v", total, graph.MaxRetryWait)
	}
}

func TestSecondaryRateLimitHonoursRetryAfter(t *testing.T) {
	srv := graphtest.New(t, fixture("secondary_limit"))
	if err := srv.Client().Query(context.Background(), viewerQuery, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := srv.Sleeps(), []time.Duration{5 * time.Second}; !slices.Equal(got, want) {
		t.Errorf("sleeps = %v, want %v", got, want)
	}
}

func TestCancelDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := graph.New(graph.Options{
		Host: "github.com", AuthToken: graphtest.FakeToken,
		Transport: graphtest.New(t, fixture("always_503")),
		Sleep: func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		},
	})
	err := c.Query(ctx, viewerQuery, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want one wrapping context.Canceled", err)
	}
	assertError(t, err, render.ExitNetwork, "errors/canceled")
}

func TestCanceledContextSendsNothing(t *testing.T) {
	srv := graphtest.New(t, fixture("html_200"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := srv.Client().Query(ctx, viewerQuery, nil, nil)
	if !errors.Is(err, context.Canceled) || srv.Requests() != 0 {
		t.Errorf("err = %v, requests = %d; want canceled and no request", err, srv.Requests())
	}
	// Satisfy the fixture so the server's cleanup check passes.
	if err := srv.Client().Query(context.Background(), viewerQuery, nil, nil); err == nil {
		t.Error("html_200: want an error")
	}
}

func TestMutationIsNotRetried(t *testing.T) {
	srv := graphtest.New(t, fixture("mutation_502"))
	const m = `mutation AddComment($subject: ID!, $body: String!) {
  addComment(input: {subjectId: $subject, body: $body}) { clientMutationId }
}`
	err := srv.Client().Mutate(context.Background(), m, map[string]any{"subject": "I_kwDOexample", "body": "hello"}, nil)
	assertError(t, err, render.ExitNetwork, "errors/mutation_502")
	if !errors.Is(err, graph.ErrUnknownOutcome) {
		t.Error("a 502 on a mutation is not marked graph.ErrUnknownOutcome")
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want 1 (mutations are never retried)", n)
	}
	if s := srv.Sleeps(); len(s) != 0 {
		t.Errorf("sleeps = %v, want none", s)
	}
}

// TestContentLimit: GitHub's limit on creating content is exit code 4 with
// how long to wait, on a query and on a mutation, inside a 200 GraphQL
// answer and over HTTP. Nothing is resent, and a mutation it refused is
// not "may have been applied": GitHub refuses before writing.
func TestContentLimit(t *testing.T) {
	const addComment = `mutation AddComment($subject: ID!, $body: String!) {
  addComment(input: {subjectId: $subject, body: $body}) { clientMutationId }
}`
	vars := map[string]any{"subject": "I_kwDOexample", "body": "hello"}
	for _, tt := range []struct {
		fixture string
		run     func(c *graph.Client) error
	}{
		{"content_limit_query", func(c *graph.Client) error {
			return c.Query(context.Background(), viewerQuery, nil, nil)
		}},
		{"content_limit_mutation", func(c *graph.Client) error {
			return c.Mutate(context.Background(), addComment, vars, nil)
		}},
		{"content_limit_mutation_403", func(c *graph.Client) error {
			return c.Mutate(context.Background(), addComment, vars, nil)
		}},
	} {
		t.Run(tt.fixture, func(t *testing.T) {
			srv := graphtest.New(t, fixture(tt.fixture))
			err := tt.run(srv.Client())
			assertError(t, err, render.ExitNetwork, "errors/"+tt.fixture)
			if errors.Is(err, graph.ErrUnknownOutcome) {
				t.Error("a refused write is marked graph.ErrUnknownOutcome")
			}
			if !graph.AffectsAll(err) {
				t.Error("AffectsAll = false: a batch would go on into the same limit")
			}
			if render.FromError(err).Wait == "" {
				t.Error("Wait is empty: a partial report would say to re-run at once")
			}
			if n := srv.Requests(); n != 1 {
				t.Errorf("requests = %d, want 1", n)
			}
			if s := srv.Sleeps(); len(s) != 0 {
				t.Errorf("sleeps = %v, want none", s)
			}
		})
	}
}

func TestMutationWithGarbledResponse(t *testing.T) {
	srv := graphtest.New(t, fixture("set_status_html_200"))
	err := srv.Client().Mutate(context.Background(), setStatus, setStatusVars, nil)
	assertError(t, err, render.ExitNetwork, "errors/mutation_garbled")
	if !errors.Is(err, graph.ErrUnknownOutcome) {
		t.Error("a garbled mutation answer is not marked graph.ErrUnknownOutcome")
	}
}

func TestQueryWithNonJSON200(t *testing.T) {
	srv := graphtest.New(t, fixture("html_200"))
	err := srv.Client().Query(context.Background(), viewerQuery, nil, nil)
	assertError(t, err, render.ExitNetwork, "errors/html_200")
}

// failing is a transport that never reaches GitHub.
type failing struct{ err error }

func (f failing) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestTransportErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		mutation bool
	}{
		{"query_reset", syscall.ECONNRESET, false},
		{"query_timeout", timeoutErr{}, false},
		{"mutation_reset", syscall.ECONNRESET, true},
		{"mutation_timeout", timeoutErr{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := graph.New(graph.Options{Host: "github.com", AuthToken: graphtest.FakeToken, Transport: failing{tt.err}})
			var err error
			if tt.mutation {
				err = c.Mutate(context.Background(), setStatus, setStatusVars, nil)
			} else {
				err = c.Query(context.Background(), viewerQuery, nil, nil)
			}
			assertError(t, err, render.ExitNetwork, "errors/transport_"+tt.name)
			if errors.Is(err, graph.ErrUnknownOutcome) != tt.mutation {
				t.Errorf("errors.Is(err, ErrUnknownOutcome) = %v, want %v", !tt.mutation, tt.mutation)
			}
			text := render.FromError(err).Text()
			if strings.Contains(text, "api.github.com") || strings.Contains(text, "://") {
				t.Errorf("endpoint URL in output: %q", text)
			}
			if c.Requests() != 1 {
				t.Errorf("requests = %d, want 1", c.Requests())
			}
		})
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		fixture string
		code    render.Code
	}{
		{"primary_limit", render.ExitNetwork},
		{"graphql_rate_limited", render.ExitNetwork},
		{"bad_credentials", render.ExitNetwork},
		{"token_echo", render.ExitUsage},
		{"graphql_timeout", render.ExitNetwork},
		{"saml_failure", render.ExitNetwork},
		{"oauth_restricted", render.ExitNetwork},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			srv := graphtest.New(t, fixture(tt.fixture))
			err := srv.Client().Query(context.Background(), viewerQuery, nil, nil)
			assertError(t, err, tt.code, "errors/"+tt.fixture)
			if n := srv.Requests(); n != 1 {
				t.Errorf("requests = %d, want 1", n)
			}
		})
	}
}

func TestInsufficientScopes(t *testing.T) {
	srv := graphtest.New(t, fixture("insufficient_scopes"))
	_, err := graph.Paginate(context.Background(), srv.Client(), itemsQuery, boardVars, 0, items)
	assertError(t, err, render.ExitNetwork, "errors/insufficient_scopes")
	if fix := render.FromError(err).Fix; fix != render.AuthFix {
		t.Errorf("fix = %q, want %q", fix, render.AuthFix)
	}
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr.Errors[0].Type != "INSUFFICIENT_SCOPES" {
		t.Errorf("errors.As(*api.GraphQLError) failed for %v", err)
	}
}

// TestEnvTokenFix: gh auth refresh can't fix a token from GH_TOKEN.
func TestEnvTokenFix(t *testing.T) {
	t.Setenv("GH_TOKEN", graphtest.FakeToken)
	for _, name := range []string{"insufficient_scopes", "bad_credentials"} {
		srv := graphtest.New(t, fixture(name))
		c := graph.New(graph.Options{Host: "github.com", Transport: srv, Sleep: func(context.Context, time.Duration) error { return nil }})
		var err error
		if name == "insufficient_scopes" {
			_, err = graph.Paginate(context.Background(), c, itemsQuery, boardVars, 0, items)
		} else {
			err = c.Query(context.Background(), viewerQuery, nil, nil)
		}
		if fix, want := render.FromError(err).Fix, "set GH_TOKEN to a token with the project scope"; fix != want {
			t.Errorf("%s: fix = %q, want %q", name, fix, want)
		}
	}
}

// TestSSOFix: gh's own token is authorized for an organization's SAML SSO
// by refreshing it; a token from the environment on GitHub's token page.
func TestSSOFix(t *testing.T) {
	srv := graphtest.New(t, fixture("saml_failure"))
	err := srv.Client().Query(context.Background(), viewerQuery, nil, nil)
	if fix := render.FromError(err).Fix; fix != graph.SSOFix {
		t.Errorf("fix = %q, want %q", fix, graph.SSOFix)
	}

	t.Setenv("GH_TOKEN", graphtest.FakeToken)
	srv = graphtest.New(t, fixture("saml_failure"))
	c := graph.New(graph.Options{Host: "github.com", Transport: srv, Sleep: func(context.Context, time.Duration) error { return nil }})
	err = c.Query(context.Background(), viewerQuery, nil, nil)
	want := "authorize GH_TOKEN for the organization's SSO (Configure SSO in GitHub's token settings)"
	if fix := render.FromError(err).Fix; fix != want {
		t.Errorf("GH_TOKEN: fix = %q, want %q", fix, want)
	}
}

func TestBadCredentialsSuggestsRefresh(t *testing.T) {
	srv := graphtest.New(t, fixture("bad_credentials"))
	err := srv.Client().Query(context.Background(), viewerQuery, nil, nil)
	if fix := render.FromError(err).Fix; fix != render.AuthFix {
		t.Errorf("fix = %q, want %q", fix, render.AuthFix)
	}
}

func TestInvalidQueryIsInternal(t *testing.T) {
	srv := graphtest.New(t, fixture("invalid_query"))
	err := srv.Client().Query(context.Background(), `query Invalid { viewer { foo } }`, nil, nil)
	assertError(t, err, render.ExitUsage, "errors/invalid_query")
	if text := render.FromError(err).Text(); strings.Contains(text, "foo") || !strings.Contains(text, "internal error") {
		t.Errorf("want a generic internal error, got %q", text)
	}
}

func TestStaleIDs(t *testing.T) {
	tests := []struct {
		fixture string
		code    render.Code
		run     func(c *graph.Client) error
	}{
		{"node_not_found", render.ExitNotFound, func(c *graph.Client) error {
			return c.Query(context.Background(), `query Node($id: ID!) { node(id: $id) { id } }`,
				map[string]any{"id": "PVTI_lAHOBFGd5c4BrMYFzhH6JM0x"}, nil)
		}},
		{"set_status_stale_item", render.ExitNotFound, func(c *graph.Client) error {
			return c.Mutate(context.Background(), setStatus, setStatusVars, nil)
		}},
		{"set_status_stale_option", render.ExitUsage, func(c *graph.Client) error {
			return c.Mutate(context.Background(), setStatus, setStatusVars, nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			srv := graphtest.New(t, fixture(tt.fixture))
			err := tt.run(srv.Client())
			assertError(t, err, tt.code, "errors/"+tt.fixture)
			if !graph.IsStaleID(err) {
				t.Errorf("IsStaleID(%v) = false", err)
			}
		})
	}
	for _, err := range []error{
		errors.New("plain"),
		&api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "NOT_FOUND", Message: "Could not resolve to an Issue with the number of 999."}}},
		&api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "FORBIDDEN", Message: "Resource not accessible"}}},
	} {
		if graph.IsStaleID(err) {
			t.Errorf("IsStaleID(%v) = true", err)
		}
	}
}

// TestCycle: GitHub refusing a blocked-by link as a dependency cycle is
// exit code 1 with a message that says so, without GitHub's text.
func TestCycle(t *testing.T) {
	srv := graphtest.New(t, fixture("add_blocked_by_cycle"))
	err := srv.Client().Mutate(context.Background(),
		"mutation Block($issue: ID!, $link0: ID!) {\n  link0: addBlockedBy(input: {issueId: $issue, blockingIssueId: $link0}) { issue { id } }\n}",
		map[string]any{"issue": "I_1", "link0": "I_2"}, nil)
	assertError(t, err, render.ExitUsage, "errors/add_blocked_by_cycle")
	if !graph.IsCycle(err) {
		t.Errorf("IsCycle(%v) = false", err)
	}
	for _, err := range []error{
		nil,
		errors.New("a cycle"),
		&api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "NOT_FOUND", Message: "Could not resolve to an Issue with the number of 999."}}},
	} {
		if graph.IsCycle(err) {
			t.Errorf("IsCycle(%v) = true", err)
		}
	}
}

func TestNoTokenAsksToLogIn(t *testing.T) {
	for _, env := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		t.Setenv(env, "")
	}
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_PATH", "/nonexistent/gh")
	c := graph.New(graph.Options{Host: "github.com", Transport: failing{errors.New("unreachable")}})
	err := c.Query(context.Background(), viewerQuery, nil, nil)
	assertError(t, err, render.ExitNetwork, "errors/no_token")
	if c.Requests() != 0 {
		t.Errorf("requests = %d, want 0", c.Requests())
	}
}

// TestTokenNeverInErrors runs every error fixture and checks neither the
// error text nor the rendered block contains the token.
func TestTokenNeverInErrors(t *testing.T) {
	for _, name := range []string{"always_503", "primary_limit", "graphql_rate_limited", "bad_credentials", "token_echo", "html_200"} {
		srv := graphtest.New(t, fixture(name))
		err := srv.Client().Query(context.Background(), viewerQuery, nil, nil)
		if err == nil {
			t.Fatalf("%s: want error", name)
		}
		var buf bytes.Buffer
		render.WriteError(&buf, err)
		for _, s := range []string{err.Error(), buf.String(), render.FromError(err).Text()} {
			if strings.Contains(s, graphtest.FakeToken) {
				t.Errorf("%s: token leaked: %q", name, s)
			}
		}
	}
}

func assertError(t *testing.T, err error, code render.Code, golden string) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	var e *render.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %T is not a *render.Error: %v", err, err)
	}
	if e.Code != code {
		t.Errorf("code = %d, want %d (%v)", e.Code, code, err)
	}
	text := e.Text()
	for _, bad := range []string{"PVT", "global id", "doesn't exist", "Could not resolve", graphtest.FakeToken} {
		if strings.Contains(text, bad) {
			t.Errorf("raw GraphQL text or ID %q in output: %q", bad, text)
		}
	}
	testutil.Golden(t, golden, []byte(text+"\n"))
}

// TestAffectsAll tells errors that would fail every issue of a batch alike
// (the token, a rate limit, the run interrupted) from errors of one
// request (a lost answer, a missing node, a stale ID).
func TestAffectsAll(t *testing.T) {
	gql := func(typ, msg string) error {
		return &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: typ, Message: msg}}}
	}
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"401", &api.HTTPError{StatusCode: http.StatusUnauthorized}, true},
		{"403", &api.HTTPError{StatusCode: http.StatusForbidden}, true},
		{"429", &api.HTTPError{StatusCode: http.StatusTooManyRequests}, true},
		{"rate_limited", gql("RATE_LIMITED", ""), true},
		{"content_limit", gql("UNPROCESSABLE", "was submitted too quickly"), true},
		{"unprocessable", gql("UNPROCESSABLE", "Title can't be blank"), false},
		{"scopes", gql("INSUFFICIENT_SCOPES", ""), true},
		{"forbidden", gql("FORBIDDEN", ""), true},
		{"saml", gql("", "Resource protected by organization SAML enforcement."), true},
		{"canceled", render.Network("interrupted").WithCause(errors.Join(graph.ErrUnknownOutcome, context.Canceled)), true},
		{"502", &api.HTTPError{StatusCode: http.StatusBadGateway}, false},
		{"not_found", gql("NOT_FOUND", ""), false},
		{"stale", gql("", "Could not resolve to a node with the global id of 'PVTI_x'"), false},
		{"timeout", context.DeadlineExceeded, false},
	} {
		if got := graph.AffectsAll(tt.err); got != tt.want {
			t.Errorf("%s: AffectsAll = %v, want %v", tt.name, got, tt.want)
		}
	}
}
