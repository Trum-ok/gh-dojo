package graph_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

const viewerQuery = `query Viewer { viewer { login } }`

type viewer struct {
	Viewer struct {
		Login string `json:"login"`
	} `json:"viewer"`
}

func fixture(name string) string { return "testdata/graphql/" + name + ".json" }

func TestRetryOn5xxThenSuccess(t *testing.T) {
	srv := graphtest.New(t, fixture("retry_502"))
	var out viewer
	if err := srv.Client().Do(context.Background(), viewerQuery, nil, &out); err != nil {
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
	err := c.Do(context.Background(), viewerQuery, nil, nil)
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

func TestMutationIsNotRetried(t *testing.T) {
	srv := graphtest.New(t, fixture("mutation_502"))
	const m = `mutation AddComment($subject: ID!, $body: String!) {
  addComment(input: {subjectId: $subject, body: $body}) { clientMutationId }
}`
	err := srv.Client().Do(context.Background(), m, map[string]any{"subject": "I_kwDOexample", "body": "hello"}, nil)
	assertError(t, err, render.ExitNetwork, "errors/mutation_502")
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want 1 (mutations are never retried)", n)
	}
	if s := srv.Sleeps(); len(s) != 0 {
		t.Errorf("sleeps = %v, want none", s)
	}
}

func TestSecondaryRateLimitHonoursRetryAfter(t *testing.T) {
	srv := graphtest.New(t, fixture("secondary_limit"))
	if err := srv.Client().Do(context.Background(), viewerQuery, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := srv.Sleeps(), []time.Duration{5 * time.Second}; !slices.Equal(got, want) {
		t.Errorf("sleeps = %v, want %v", got, want)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		fixture  string
		code     render.Code
		requests int
	}{
		{"primary_limit", render.ExitNetwork, 1},
		{"graphql_rate_limited", render.ExitNetwork, 1},
		{"insufficient_scopes", render.ExitNetwork, 1},
		{"bad_credentials", render.ExitNetwork, 1},
		{"token_echo", render.ExitUsage, 1},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			srv := graphtest.New(t, fixture(tt.fixture))
			err := srv.Client().Do(context.Background(), viewerQuery, nil, nil)
			assertError(t, err, tt.code, "errors/"+tt.fixture)
			if n := srv.Requests(); n != tt.requests {
				t.Errorf("requests = %d, want %d", n, tt.requests)
			}
		})
	}
}

func TestAuthErrorsSuggestRefresh(t *testing.T) {
	for _, name := range []string{"insufficient_scopes", "bad_credentials"} {
		srv := graphtest.New(t, fixture(name))
		err := srv.Client().Do(context.Background(), viewerQuery, nil, nil)
		if fix := render.FromError(err).Fix; fix != render.AuthFix {
			t.Errorf("%s: fix = %q, want %q", name, fix, render.AuthFix)
		}
	}
}

func TestGraphQLErrorKeptAsCause(t *testing.T) {
	srv := graphtest.New(t, fixture("insufficient_scopes"))
	err := srv.Client().Do(context.Background(), viewerQuery, nil, nil)
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr.Errors[0].Type != "INSUFFICIENT_SCOPES" {
		t.Errorf("errors.As(*api.GraphQLError) failed for %v", err)
	}
}

func TestNoTokenAsksToLogIn(t *testing.T) {
	for _, env := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		t.Setenv(env, "")
	}
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_PATH", "/nonexistent/gh")
	c := graph.New(graph.Options{Host: "github.com", Transport: unreachable{t}})
	err := c.Do(context.Background(), viewerQuery, nil, nil)
	assertError(t, err, render.ExitNetwork, "errors/no_token")
	if c.Requests() != 0 {
		t.Errorf("requests = %d, want 0", c.Requests())
	}
}

type unreachable struct{ t *testing.T }

func (u unreachable) RoundTrip(*http.Request) (*http.Response, error) {
	u.t.Error("unexpected HTTP request")
	return nil, errors.New("unreachable")
}

// TestTokenNeverInErrors runs every error fixture and checks neither the
// error text nor the rendered block contains the token.
func TestTokenNeverInErrors(t *testing.T) {
	for _, name := range []string{"always_503", "primary_limit", "graphql_rate_limited", "insufficient_scopes", "bad_credentials", "token_echo"} {
		srv := graphtest.New(t, fixture(name))
		err := srv.Client().Do(context.Background(), viewerQuery, nil, nil)
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

func TestParseOperation(t *testing.T) {
	tests := []struct{ doc, kind, name string }{
		{`query BoardSchema($owner: String!) { a }`, "query", "BoardSchema"},
		{"  # comment\n mutation SetStatus { a }", "mutation", "SetStatus"},
		{`{ viewer { login } }`, "query", ""},
		{`query { viewer { login } }`, "query", ""},
		{"fragment f on Issue { query mutation }\nmutation Add { a }", "mutation", "Add"},
		{`fragment f on X @dir(a: "}") { a } query Q { b }`, "query", "Q"},
		{``, "query", ""},
	}
	for _, tt := range tests {
		kind, name := graph.ParseOperation(tt.doc)
		if kind != tt.kind || name != tt.name {
			t.Errorf("ParseOperation(%q) = %q, %q; want %q, %q", tt.doc, kind, name, tt.kind, tt.name)
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
	testutil.Golden(t, golden, []byte(e.Text()+"\n"))
}
