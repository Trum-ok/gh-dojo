package board_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

var (
	userBoard = board.Ref{Owner: "Trum-ok", Number: 5}
	orgBoard  = board.Ref{Owner: "github", Number: 4247}
	t0        = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
)

func fixture(name string) string { return "testdata/graphql/" + name + ".json" }

// clock is a settable test clock.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// newLoader returns a loader over the fixtures with a private cache dir and
// a test clock.
func newLoader(t *testing.T, fixtures ...string) (*board.Loader, *graphtest.Server, *clock) {
	t.Helper()
	srv := graphtest.New(t, fixtures...)
	clk := &clock{now: t0}
	l := board.NewLoader(srv.Client())
	l.Dir = t.TempDir()
	l.Now = clk.Now
	l.Host = "github.com"
	return l, srv, clk
}

func TestUserBoard(t *testing.T) {
	l, srv, _ := newLoader(t, fixture("user_board"))
	s, err := l.Load(context.Background(), userBoard)
	if err != nil {
		t.Fatal(err)
	}
	if s.OwnerType != board.OwnerUser || s.Cached || srv.Requests() != 1 {
		t.Errorf("owner type %q, cached %v, requests %d", s.OwnerType, s.Cached, srv.Requests())
	}
	testutil.Golden(t, "schema/user_board", summary(s))

	status, err := s.Field("Status")
	if err != nil {
		t.Fatal(err)
	}
	want := "Backlog, Ready, In progress, In review, Waiting, Done"
	if got := strings.Join(status.OptionNames(), ", "); got != want {
		t.Errorf("status options = %q, want board order %q", got, want)
	}
	opt, err := status.Option("In progress")
	if err != nil || opt.ID == "" {
		t.Errorf("Option(In progress) = %+v, %v", opt, err)
	}
}

func TestOrgBoard(t *testing.T) {
	l, _, _ := newLoader(t, fixture("org_board"))
	s, err := l.Load(context.Background(), orgBoard)
	if err != nil {
		t.Fatal(err)
	}
	if s.OwnerType != board.OwnerOrganization {
		t.Errorf("owner type = %q", s.OwnerType)
	}
	testutil.Golden(t, "schema/org_board", summary(s))
}

func TestIterationField(t *testing.T) {
	l, _, _ := newLoader(t, fixture("iteration_board"))
	s, err := l.Load(context.Background(), board.Ref{Owner: "acme", Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "schema/iteration_board", summary(s))
	sprint, err := s.Field("Sprint")
	if err != nil {
		t.Fatal(err)
	}
	if it := sprint.IterationAt(time.Date(2026, 11, 11, 23, 0, 0, 0, time.UTC)); it == nil || it.Title != "Sprint 12" {
		t.Errorf("IterationAt(2026-11-11) = %+v, want Sprint 12", it)
	}
	if it := sprint.IterationAt(time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)); it == nil || it.Title != "Sprint 11" || !it.Completed {
		t.Errorf("IterationAt(2026-10-30) = %+v, want completed Sprint 11", it)
	}
	if it := sprint.IterationAt(time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)); it != nil {
		t.Errorf("IterationAt(2027-06-01) = %+v, want nil", it)
	}
	if _, err := sprint.Iteration("Sprint 13"); err != nil {
		t.Error(err)
	}
	_, err = sprint.Iteration("Sprint 99")
	assertError(t, err, render.ExitUsage, "errors/unknown_iteration")
}

func TestSecondLoadWithinTTLUsesCache(t *testing.T) {
	l, srv, clk := newLoader(t, fixture("user_board"))
	ctx := context.Background()
	if _, err := l.Load(ctx, userBoard); err != nil {
		t.Fatal(err)
	}
	clk.now = t0.Add(59 * time.Minute)
	s, err := l.Load(ctx, userBoard)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Cached || srv.Requests() != 1 {
		t.Fatalf("second load: cached %v, requests %d; want cached and 1 request", s.Cached, srv.Requests())
	}
	if _, err := s.Field("Status"); err != nil {
		t.Errorf("cached schema lost fields: %v", err)
	}
	// Owner logins are case-insensitive: same cache entry.
	if _, err := l.Load(ctx, board.Ref{Owner: "trum-ok", Number: 5}); err != nil || srv.Requests() != 1 {
		t.Errorf("load with lowercase owner: err %v, requests %d", err, srv.Requests())
	}

	clk.now = t0.Add(time.Hour)
	if s, err = l.Load(ctx, userBoard); err != nil || s.Cached || srv.Requests() != 2 {
		t.Fatalf("after TTL: err %v, cached %v, requests %d; want a re-fetch", err, s != nil && s.Cached, srv.Requests())
	}
	if s, err = l.Load(ctx, userBoard); err != nil || !s.Cached || srv.Requests() != 2 {
		t.Fatalf("after re-fetch: err %v, requests %d; want cache", err, srv.Requests())
	}

	if err := l.Invalidate(userBoard); err != nil {
		t.Fatal(err)
	}
	if s, err = l.Load(ctx, userBoard); err != nil || s.Cached || srv.Requests() != 3 {
		t.Fatalf("after Invalidate: err %v, requests %d; want a re-fetch", err, srv.Requests())
	}
	if err := l.Invalidate(board.Ref{Owner: "nobody", Number: 1}); err != nil {
		t.Errorf("Invalidate of a missing entry: %v", err)
	}
}

func TestCacheMisses(t *testing.T) {
	tests := map[string]func(t *testing.T, path string, clk *clock){
		"corrupt file": func(t *testing.T, path string, _ *clock) {
			writeFile(t, path, "{not json")
		},
		"other version": func(t *testing.T, path string, _ *clock) {
			editCache(t, path, func(m map[string]any) { m["version"] = 999 })
		},
		"other board": func(t *testing.T, path string, _ *clock) {
			editCache(t, path, func(m map[string]any) { m["schema"].(map[string]any)["number"] = 6 })
		},
		"clock went back": func(t *testing.T, _ string, clk *clock) {
			clk.now = t0.Add(-time.Minute)
		},
	}
	for name, spoil := range tests {
		t.Run(name, func(t *testing.T) {
			l, srv, clk := newLoader(t, fixture("user_board"))
			ctx := context.Background()
			if _, err := l.Load(ctx, userBoard); err != nil {
				t.Fatal(err)
			}
			spoil(t, filepath.Join(l.Dir, cacheFile), clk)
			s, err := l.Load(ctx, userBoard)
			if err != nil || s.Cached || srv.Requests() != 2 {
				t.Errorf("err %v, requests %d; want a re-fetch", err, srv.Requests())
			}
		})
	}
}

const cacheFile = "board-github.com-trum-ok-5.json"

func TestCacheDirEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(board.CacheDirEnv, dir)
	t.Setenv("GH_HOST", "github.com")
	srv := graphtest.New(t, fixture("user_board"))
	l := board.NewLoader(srv.Client())
	if _, err := l.Load(context.Background(), userBoard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, cacheFile)); err != nil {
		t.Errorf("cache not written to %s: %v", board.CacheDirEnv, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("cache dir has %d entries, want 1 (no temp files left)", len(entries))
	}
}

func TestResolveRereadsStaleCacheOnce(t *testing.T) {
	l, srv, _ := newLoader(t, fixture("user_board"))
	ctx := context.Background()
	if _, err := l.Load(ctx, userBoard); err != nil {
		t.Fatal(err)
	}
	// The cached copy predates the "Waiting" status.
	editCache(t, filepath.Join(l.Dir, cacheFile), func(m map[string]any) {
		for _, f := range m["schema"].(map[string]any)["fields"].([]any) {
			f := f.(map[string]any)
			if f["name"] == "Status" {
				opts := f["options"].([]any)
				f["options"] = opts[:4]
			}
		}
	})
	lookup := func(s *board.Schema) error {
		f, err := s.Field("Status")
		if err != nil {
			return err
		}
		_, err = f.Option("Waiting")
		return err
	}
	if _, err := l.Resolve(ctx, userBoard, lookup); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if srv.Requests() != 2 {
		t.Errorf("requests = %d, want 2 (one re-read)", srv.Requests())
	}

	// A name that is wrong on the fresh board too: one re-read at most.
	_, err := l.Resolve(ctx, userBoard, func(s *board.Schema) error {
		f, _ := s.Field("Status")
		_, err := f.Option("In progres")
		return err
	})
	assertError(t, err, render.ExitUsage, "errors/unknown_option")
	if !errors.Is(err, board.ErrUnknownOption) || !board.IsUnknown(err) {
		t.Errorf("err %v is not ErrUnknownOption", err)
	}
	if srv.Requests() != 3 {
		t.Errorf("requests = %d, want 3", srv.Requests())
	}
}

func TestLookupErrors(t *testing.T) {
	l, _, _ := newLoader(t, fixture("user_board"))
	s, err := l.Load(context.Background(), userBoard)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Field("status")
	assertError(t, err, render.ExitUsage, "errors/unknown_field_case")
	if !errors.Is(err, board.ErrUnknownField) {
		t.Errorf("err %v is not ErrUnknownField", err)
	}
	_, err = s.Field("Sprint")
	assertError(t, err, render.ExitUsage, "errors/unknown_field")

	prio, _ := s.Field("Priority")
	_, err = prio.Option("P5")
	assertError(t, err, render.ExitUsage, "errors/unknown_option_ambiguous")

	est, _ := s.Field("Estimate")
	_, err = est.Option("1")
	assertError(t, err, render.ExitUsage, "errors/wrong_field_type")
	if !errors.Is(err, board.ErrFieldType) || !board.IsUnknown(err) || !board.IsStale(err) {
		t.Errorf("err %v is not ErrFieldType", err)
	}
}

func TestBoardNotFound(t *testing.T) {
	l, _, _ := newLoader(t, fixture("board_not_found"))
	_, err := l.Load(context.Background(), board.Ref{Owner: "Trum-ok", Number: 99})
	assertError(t, err, render.ExitNotFound, "errors/board_not_found")
}

// TestOrgBoardNotFound: GitHub answers the same for an organization's
// project that does not exist and one the token cannot see, so the error
// says both.
func TestOrgBoardNotFound(t *testing.T) {
	l, _, _ := newLoader(t, fixture("org_board_not_found"))
	_, err := l.Load(context.Background(), board.Ref{Owner: "github", Number: 1})
	assertError(t, err, render.ExitNotFound, "errors/org_board_not_found")
}

// TestOrgBoardAccessDenied: an organization that blocks the token (SAML
// SSO, OAuth app restrictions, a fine-grained token without access to its
// projects) is exit code 4 with a fix, and nothing is cached.
func TestOrgBoardAccessDenied(t *testing.T) {
	for _, name := range []string{"org_board_saml", "org_board_oauth_restricted", "org_board_forbidden"} {
		t.Run(name, func(t *testing.T) {
			l, _, _ := newLoader(t, fixture(name))
			_, err := l.Load(context.Background(), board.Ref{Owner: "acme", Number: 3})
			assertError(t, err, render.ExitNetwork, "errors/"+name)
			if entries, _ := os.ReadDir(l.Dir); len(entries) != 0 {
				t.Errorf("an error was cached: %v", entries)
			}
		})
	}
}

func TestOwnerNotFound(t *testing.T) {
	l, _, _ := newLoader(t, fixture("owner_not_found"))
	_, err := l.Load(context.Background(), board.Ref{Owner: "no-such-user-xyz-123987", Number: 1})
	assertError(t, err, render.ExitNotFound, "errors/owner_not_found")
}

func TestMissingProjectScope(t *testing.T) {
	l, srv, _ := newLoader(t, fixture("insufficient_scopes"))
	_, err := l.Load(context.Background(), userBoard)
	assertError(t, err, render.ExitNetwork, "errors/insufficient_scopes")
	if render.FromError(err).Fix != render.AuthFix {
		t.Errorf("fix = %q, want %q", render.FromError(err).Fix, render.AuthFix)
	}
	if strings.Contains(err.Error(), graphtest.FakeToken) {
		t.Error("token leaked into the error")
	}
	if srv.Requests() != 1 {
		t.Errorf("requests = %d, want 1", srv.Requests())
	}
	if entries, _ := os.ReadDir(l.Dir); len(entries) != 0 {
		t.Errorf("an error was cached: %v", entries)
	}
}

func TestInvalidRef(t *testing.T) {
	l := board.NewLoader(nil)
	l.Dir = t.TempDir()
	for _, ref := range []board.Ref{{Owner: "", Number: 1}, {Owner: "../etc", Number: 1}, {Owner: "acme", Number: 0}, {Owner: "a b", Number: 1}} {
		_, err := l.Load(context.Background(), ref)
		if render.FromError(err).Code != render.ExitUsage {
			t.Errorf("Load(%+v) = %v, want a usage error", ref, err)
		}
	}
}

func assertError(t *testing.T, err error, code render.Code, golden string) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	e := render.FromError(err)
	if e.Code != code {
		t.Errorf("code = %d, want %d (%v)", e.Code, code, err)
	}
	testutil.Golden(t, golden, []byte(e.Text()+"\n"))
}

// summary renders a schema for golden files, one line per field.
func summary(s *board.Schema) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "board %s/%d %s %q %s %s closed=%v\n", s.Owner, s.Number, s.OwnerType, s.Title, s.ID, s.URL, s.Closed)
	for _, f := range s.Fields {
		fmt.Fprintf(&b, "  %s [%s] %s", f.Name, f.Type, f.ID)
		for _, o := range f.Options {
			fmt.Fprintf(&b, "\n    option %q %s", o.Name, o.ID)
		}
		for _, it := range f.Iterations {
			fmt.Fprintf(&b, "\n    iteration %q %s %s +%dd completed=%v", it.Title, it.ID, it.StartDate, it.Duration, it.Completed)
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func editCache(t *testing.T, path string, edit func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	data, _ = json.Marshal(m)
	writeFile(t, path, string(data))
}

func TestEMULogin(t *testing.T) {
	if err := (board.Ref{Owner: "jdoe_acme", Number: 1}).Validate(); err != nil {
		t.Errorf("EMU login rejected: %v", err)
	}
}

func TestCacheKeyIncludesHost(t *testing.T) {
	l, srv, _ := newLoader(t, fixture("user_board"))
	ctx := context.Background()
	if _, err := l.Load(ctx, userBoard); err != nil {
		t.Fatal(err)
	}
	l.Host = "ghe.example.com"
	if s, err := l.Load(ctx, userBoard); err != nil || s.Cached || srv.Requests() != 2 {
		t.Errorf("other host: err %v, requests %d; want its own cache entry", err, srv.Requests())
	}
	for _, name := range []string{cacheFile, "board-ghe.example.com-trum-ok-5.json"} {
		if _, err := os.Stat(filepath.Join(l.Dir, name)); err != nil {
			t.Error(err)
		}
	}
}

func TestStaleTempFilesRemoved(t *testing.T) {
	l, _, _ := newLoader(t, fixture("user_board"))
	old := filepath.Join(l.Dir, ".board-123.tmp")
	fresh := filepath.Join(l.Dir, ".board-456.tmp")
	writeFile(t, old, "partial")
	writeFile(t, fresh, "in progress")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Load(context.Background(), userBoard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old temp file kept: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh temp file removed: %v", err)
	}
}

const setStatus = `mutation SetStatus($project: ID!, $item: ID!, $field: ID!, $option: String!) {
  updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, value: {singleSelectOptionId: $option}}) {
    projectV2Item { id }
  }
}`

// TestResolveRetriesStaleIDOnce is the flow verbs use: resolve the option
// from the cached schema, write; if GitHub rejects the cached ID, re-read
// the board and write again.
func TestResolveRetriesStaleIDOnce(t *testing.T) {
	l, srv, _ := newLoader(t, fixture("user_board"), fixture("set_status_stale_then_ok"))
	ctx := context.Background()
	if _, err := l.Load(ctx, userBoard); err != nil {
		t.Fatal(err)
	}
	writes := 0
	_, err := l.Resolve(ctx, userBoard, func(s *board.Schema) error {
		status, err := s.Field("Status")
		if err != nil {
			return err
		}
		opt, err := status.Option("In progress")
		if err != nil {
			return err
		}
		writes++
		return srv.Client().Mutate(ctx, setStatus, map[string]any{
			"project": s.ID, "item": "PVTI_lAHOBFGd5c4BrMYFzhH6JM0", "field": status.ID, "option": opt.ID,
		}, nil)
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if writes != 2 || srv.Requests() != 4 {
		t.Errorf("writes = %d, requests = %d; want 2 writes, 4 requests (read, write, re-read, write)", writes, srv.Requests())
	}
}
