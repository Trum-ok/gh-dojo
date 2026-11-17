package newissue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/effects"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

const shopConfig = `
version = 1

[board]
owner = "acme"
project = 3
repos = ["acme/shop"]

[fields]
status = "Status"

[statuses]
initial = "Backlog"
resolved = ["Done"]
`

var shop = issue.Repo{Owner: "acme", Name: "shop"}

func shopCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(shopConfig), ".github/dojo.toml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// fakeBoard serves a schema without the Status field, so every run fails
// on it as on a stale cached schema (board.IsStale).
type fakeBoard struct {
	cached          bool
	schemas, resets int
}

func (b *fakeBoard) Schema(context.Context) (*board.Schema, error) {
	b.schemas++
	return &board.Schema{Owner: "acme", Number: 3, ID: "PVT_1", Cached: b.cached}, nil
}

func (b *fakeBoard) SchemaCached() bool { return b.cached }

func (b *fakeBoard) ResetSchema() error { b.resets++; b.cached = false; return nil }

// noClient fails the test on any request.
type noClient struct{ t *testing.T }

func (c noClient) Query(context.Context, string, map[string]any, any) error {
	c.t.Error("unexpected query")
	return errors.New("no requests")
}

func (c noClient) Mutate(context.Context, string, map[string]any, any) error {
	c.t.Error("unexpected mutation")
	return errors.New("no requests")
}

func env(t *testing.T, b *fakeBoard) Env {
	return Env{
		Config: shopCfg(t), Client: noClient{t}, Board: b,
		Repo: func() (issue.Repo, error) { return shop, nil }, Host: func() string { return "github.com" },
	}
}

// TestRunStaleSchema: a cached schema GitHub's IDs no longer match is
// dropped and the run tried once more while nothing is written; a fresh
// one fails at once.
func TestRunStaleSchema(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		cached                  bool
		wantSchemas, wantResets int
	}{
		{"cached", true, 2, 1},
		{"fresh", false, 1, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := &fakeBoard{cached: tt.cached}
			is, err := Run(context.Background(), env(t, b), Options{Title: "Fix it"})
			if is != nil || !board.IsStale(err) {
				t.Errorf("Run = %v, %v; want nil and a stale-schema error", is, err)
			}
			if b.schemas != tt.wantSchemas || b.resets != tt.wantResets {
				t.Errorf("schemas %d, resets %d; want %d, %d", b.schemas, b.resets, tt.wantSchemas, tt.wantResets)
			}
		})
	}
}

// gqlErr is a GraphQL error with items at path, of type typ.
func gqlErr(path, typ, msg string) error {
	return &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Message: msg, Type: typ, Path: []any{path}}}}
}

func TestParentRefused(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		field string
		named bool
		want  bool
	}{
		{"addSubIssue unprocessable", gqlErr(effects.AliasParent, "UNPROCESSABLE", "too many"), effects.AliasParent, false, true},
		{"addSubIssue forbidden", gqlErr(effects.AliasParent, "FORBIDDEN", "no access"), effects.AliasParent, false, true},
		{"input error without a type", gqlErr(effects.AliasParent, "", "invalid"), effects.AliasParent, false, true},
		{"another field", gqlErr(effects.AliasStatus, "UNPROCESSABLE", "no"), effects.AliasParent, false, false},
		{"another type", gqlErr(effects.AliasParent, "NOT_FOUND", "gone"), effects.AliasParent, false, false},
		{"createIssue naming the parent", gqlErr("createIssue", "UNPROCESSABLE", "Parent has too many Sub-issues"), "createIssue", true, true},
		{"createIssue naming something else", gqlErr("createIssue", "UNPROCESSABLE", "Labels are invalid"), "createIssue", true, false},
		{"lost answer", fmt.Errorf("%w: %w", graph.ErrUnknownOutcome, gqlErr(effects.AliasParent, "", "")), effects.AliasParent, false, false},
		{"not GraphQL", errors.New("connection reset"), effects.AliasParent, false, false},
		{"nil", nil, effects.AliasParent, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parentRefused(tt.err, tt.field, tt.named); got != tt.want {
				t.Errorf("parentRefused = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPartial: a run stopped after the issue existed is exit code 5 with a
// re-run as the fix, keeping a fix of its cause; a refused parent or a
// dependency cycle is exit code 1, since a re-run would fail alike.
func TestPartial(t *testing.T) {
	r := &Issue{Number: 15, Repo: shop, Def: shop}
	tests := []struct {
		name      string
		resumed   bool
		err       error
		code      render.Code
		what, fix string
	}{
		{
			"step", false, &stepError{step: "setting status Backlog failed", err: render.Network("GitHub did not answer")},
			render.ExitPartial, "#15 created, but setting status Backlog failed\nGitHub did not answer",
			"re-run the same gh dojo new command: it finds #15 and finishes it",
		},
		{
			"resumed with its own fix", true, render.Network("no scope").WithFix("gh auth refresh -s project"),
			render.ExitPartial, "#15 found (created earlier), but finishing it failed\nno scope",
			"gh auth refresh -s project; then re-run the same gh dojo new command",
		},
		{
			"parent refused", false, &stepError{step: "making it a sub-issue of #40 failed", err: gqlErr(effects.AliasParent, "UNPROCESSABLE", "Parent has the maximum number of sub-issues"), parent: true},
			render.ExitUsage, "#15 created, but making it a sub-issue of #40 failed; re-running will not help\nGitHub: Parent has the maximum number of sub-issues",
			"pass another --parent to make it a sub-issue (gh issue edit 15 -R acme/shop --parent <issue>), or leave it without one",
		},
		{
			"cycle", false, &stepError{step: "marking it blocked by #12 failed", err: gqlErr(effects.LinkAlias(0), "UNPROCESSABLE", "would create a cycle")},
			render.ExitUsage, "", "gh dojo show 15",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r.Resumed = tt.resumed
			e := render.FromError(r.partial(tt.err))
			if e.Code != tt.code || tt.what != "" && e.What != tt.what || e.Fix != tt.fix {
				t.Errorf("partial = %d %q, fix %q\nwant      %d %q, fix %q", e.Code, e.What, e.Fix, tt.code, tt.what, tt.fix)
			}
		})
	}

	// A FORBIDDEN refusal may be the token's: its fix comes first.
	e := render.FromError(r.partial(&stepError{step: "making it a sub-issue of acme/web#4 failed", err: gqlErr(effects.AliasParent, "FORBIDDEN", "no access"), parent: true}))
	if !strings.Contains(e.What, "re-running with the same token will not help") || !strings.HasPrefix(e.Fix, "if the token lacks access") {
		t.Errorf("forbidden parent = %q, fix %q", e.What, e.Fix)
	}
}

func TestWithMarker(t *testing.T) {
	const m = "<!-- gh-dojo:new:abc -->"
	if got := withMarker("", m); got != m {
		t.Errorf("empty body = %q", got)
	}
	if got := withMarker("Body.", m); got != "Body.\n\n"+m {
		t.Errorf("body = %q", got)
	}
}

// TestWrote: a created issue was written; a resumed one only when this
// run changed it; a dry run never.
func TestWrote(t *testing.T) {
	tests := []struct {
		name string
		r    Issue
		want bool
	}{
		{"created", Issue{Number: 15}, true},
		{"resumed, nothing to do", Issue{Number: 15, Resumed: true}, false},
		{"resumed, status set", Issue{Number: 15, Resumed: true, StatusSet: true}, true},
		{"resumed, assigned", Issue{Number: 15, Resumed: true, Assigned: []string{"octocat"}}, true},
		{"dry run", Issue{opts: Options{DryRun: true}, StatusSet: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Wrote(); got != tt.want {
				t.Errorf("Wrote = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestText: the confirmation and the dry run's plan name other
// repositories' issues in full and say what a resumed issue kept.
func TestText(t *testing.T) {
	other := issue.Repo{Owner: "acme", Name: "api"}
	parent := issue.Ref{Repo: shop, Number: 40}
	kept := issue.Ref{Repo: shop, Number: 41}
	r := &Issue{
		opts: Options{Title: "Fix it", Parent: &kept}, title: "Fix it", Number: 15, Resumed: true,
		Repo: other, Def: shop, Labels: []string{"bug", "area:cli"}, labelsMissing: []string{"area:cli"},
		Status: "Backlog", Parent: &parent, parentKept: &kept,
		LinkedFound: []*issue.Linked{{Ref: issue.Ref{Repo: shop, Number: 12}}},
	}
	if got, want := r.Confirm(), `acme/api#15 "Fix it" already created, labels bug, area:cli, Backlog (unchanged), already blocked by #12, sub-issue of #40 (kept, not #41)`; got != want {
		t.Errorf("Confirm =\n%s\nwant\n%s", got, want)
	}
	r.parentHidden = true
	want := `dry run: new acme/api#15 "Fix it"
  create: already created by an earlier run; finishes it
  labels: +area:cli (bug already in place)
  status: Backlog (unchanged)
  blocked by: #12 already linked
  sub-issue of: a parent not readable, kept, not #41`
	if got := r.DryRunText(); got != want {
		t.Errorf("DryRunText =\n%s\nwant\n%s", got, want)
	}
}
