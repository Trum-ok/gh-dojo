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
