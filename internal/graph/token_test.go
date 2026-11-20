package graph_test

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

func TestQueryHeaders(t *testing.T) {
	srv := graphtest.New(t, fixture("viewer_scopes"))
	var out viewer
	h, err := srv.Client().QueryHeaders(context.Background(), viewerQuery, nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	if out.Viewer.Login != "Trum-ok" {
		t.Errorf("login = %q", out.Viewer.Login)
	}
	scopes, listed := graph.Scopes(h)
	if want := []string{"gist", "project", "read:org", "repo"}; !listed || !slices.Equal(scopes, want) {
		t.Errorf("scopes = %q, %v; want %q", scopes, listed, want)
	}
}

// TestQueryHeadersOnError: the headers of a failed answer come back too.
func TestQueryHeadersOnError(t *testing.T) {
	srv := graphtest.New(t, fixture("bad_credentials"))
	h, err := srv.Client().QueryHeaders(context.Background(), viewerQuery, nil, nil)
	if render.FromError(err).Code != render.ExitNetwork {
		t.Errorf("err = %v, want an auth error", err)
	}
	if h == nil {
		t.Error("no headers from a 401 answer")
	}
}

func TestScopes(t *testing.T) {
	tests := []struct {
		header http.Header
		want   []string
		listed bool
	}{
		{http.Header{}, nil, false}, // fine-grained or app token
		{http.Header{"X-Oauth-Scopes": {""}}, nil, true},
		{http.Header{"X-Oauth-Scopes": {"repo,  read:project"}}, []string{"read:project", "repo"}, true},
	}
	for _, tt := range tests {
		got, listed := graph.Scopes(tt.header)
		if listed != tt.listed || !slices.Equal(got, tt.want) {
			t.Errorf("Scopes(%v) = %q, %v; want %q, %v", tt.header, got, listed, tt.want, tt.listed)
		}
	}
}

// TestCheckProjectScope: a classic token without project is refused from
// the scopes of any answer read, as its first board write would fail; a
// token with project, one whose scopes are not listed (fine-grained, app)
// and a client that has read nothing yet are not.
func TestCheckProjectScope(t *testing.T) {
	tests := []struct {
		fixture string
		refused bool
	}{
		{"viewer_read_project", true},
		{"viewer_scopes", false},
		{"retry_502", false}, // no X-OAuth-Scopes: a fine-grained or app token
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			c := graphtest.New(t, fixture(tt.fixture)).Client()
			if err := c.CheckProjectScope(); err != nil {
				t.Fatalf("before any request: %v", err)
			}
			if err := c.Query(context.Background(), viewerQuery, nil, nil); err != nil {
				t.Fatal(err)
			}
			err := c.CheckProjectScope()
			if !tt.refused {
				if err != nil {
					t.Errorf("err = %v, want nil", err)
				}
				return
			}
			e := render.FromError(err)
			if e.Code != render.ExitNetwork || e.What != "GitHub token is missing a required scope (project)" || e.Fix == "" {
				t.Errorf("err = %+v, want the missing-scope error with a fix", e)
			}
		})
	}
}

func TestTokenEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", graphtest.FakeToken)
	c := graph.New(graph.Options{Host: "github.com"})
	if got := c.TokenEnv(); got != "GH_TOKEN" {
		t.Errorf("TokenEnv = %q, want GH_TOKEN", got)
	}
	if got, want := c.AuthFix(), "set GH_TOKEN to a token with the project scope"; got != want {
		t.Errorf("AuthFix = %q, want %q", got, want)
	}
	if got := c.Host(); got != "github.com" {
		t.Errorf("Host = %q", got)
	}
	if c.Requests() != 0 {
		t.Errorf("requests = %d, want 0", c.Requests())
	}
}
