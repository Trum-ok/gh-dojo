package graphtest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTB records failures instead of failing the real test.
type fakeTB struct {
	testing.TB
	errors   []string
	cleanups []func()
}

func (f *fakeTB) Helper()           {}
func (f *fakeTB) Name() string      { return "TestFake" }
func (f *fakeTB) Failed() bool      { return len(f.errors) > 0 }
func (f *fakeTB) Cleanup(fn func()) { f.cleanups = append(f.cleanups, fn) }
func (f *fakeTB) Error(args ...any) { f.errors = append(f.errors, fmt.Sprint(args...)) }
func (f *fakeTB) Errorf(format string, a ...any) {
	f.errors = append(f.errors, fmt.Sprintf(format, a...))
}
func (f *fakeTB) Fatalf(format string, a ...any) { panic(fmt.Sprintf(format, a...)) }
func (f *fakeTB) Logf(string, ...any)            {}
func (f *fakeTB) Log(...any)                     {}
func (f *fakeTB) Skipf(format string, a ...any)  { panic("skip: " + fmt.Sprintf(format, a...)) }
func (f *fakeTB) finish() {
	for _, fn := range f.cleanups {
		fn()
	}
}

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const viewerFixture = `{"interactions": [
  {"operation": "Viewer", "variables": {"a": 1, "b": "x"}, "query": "query Viewer($a: Int, $b: String) { viewer { login } }",
   "response": {"data": {"viewer": {"login": "me"}}}}
]}`

const viewerQuery = "query Viewer($a: Int, $b: String) {\n  viewer {\n    login\n  }\n}"

func TestMatchIgnoresVariableOrderAndQueryWhitespace(t *testing.T) {
	t.Setenv(recordEnv, "")
	srv := New(t, writeFixture(t, viewerFixture))
	var out struct {
		Viewer struct{ Login string } `json:"viewer"`
	}
	if err := srv.Client().Query(context.Background(), viewerQuery, map[string]any{"b": "x", "a": 1}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Viewer.Login != "me" {
		t.Errorf("login = %q", out.Viewer.Login)
	}
}

func TestUnexpectedRequestFails(t *testing.T) {
	t.Setenv(recordEnv, "")
	ft := &fakeTB{}
	srv := New(ft, writeFixture(t, viewerFixture))
	err := srv.Client().Query(context.Background(), viewerQuery, map[string]any{"a": 2, "b": "x"}, nil)
	if err == nil {
		t.Fatal("want error for an unknown request")
	}
	ft.finish()
	if len(ft.errors) != 2 || !strings.Contains(ft.errors[0], `unexpected request Viewer {"a":2,"b":"x"}`) ||
		!strings.Contains(ft.errors[1], "never requested") {
		t.Errorf("errors = %q", ft.errors)
	}
}

func TestStaleQueryFails(t *testing.T) {
	t.Setenv(recordEnv, "")
	ft := &fakeTB{}
	srv := New(ft, writeFixture(t, viewerFixture))
	q := "query Viewer($a: Int, $b: String) { viewer { login name } }"
	if err := srv.Client().Query(context.Background(), q, map[string]any{"a": 1, "b": "x"}, nil); err == nil {
		t.Fatal("want error for a changed query")
	}
	if len(ft.errors) == 0 || !strings.Contains(ft.errors[0], "different query") {
		t.Errorf("errors = %q", ft.errors)
	}
}

const twoViewers = `{"interactions": [
  {"operation": "Viewer", "query": "query Viewer($a: Int, $b: String) { viewer { login } }", "response": {"data": {"viewer": {"login": "first"}}}},
  {"operation": "Viewer", "query": "query Viewer($a: Int, $b: String) { viewer { login } }", "REPEAT": true, "response": {"data": {"viewer": {"login": "second"}}}}
]}`

func answers(t *testing.T, srv *Server, n int) ([]string, error) {
	var got []string
	for range n {
		var out struct {
			Viewer struct{ Login string } `json:"viewer"`
		}
		if err := srv.Client().Query(context.Background(), viewerQuery, nil, &out); err != nil {
			return got, err
		}
		got = append(got, out.Viewer.Login)
	}
	return got, nil
}

func TestRepeatIsOptIn(t *testing.T) {
	t.Setenv(recordEnv, "")
	ft := &fakeTB{}
	srv := New(ft, writeFixture(t, strings.Replace(twoViewers, `"REPEAT": true, `, "", 1)))
	got, err := answers(t, srv, 3)
	if err == nil || strings.Join(got, ",") != "first,second" {
		t.Errorf("answers = %v, err = %v; want two answers and an error", got, err)
	}
	if len(ft.errors) != 1 || !strings.Contains(ft.errors[0], `"repeat": true`) {
		t.Errorf("errors = %q", ft.errors)
	}
}

func TestRepeatInOrderThenSticky(t *testing.T) {
	t.Setenv(recordEnv, "")
	srv := New(t, writeFixture(t, strings.Replace(twoViewers, "REPEAT", "repeat", 1)))
	got, err := answers(t, srv, 3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "first,second,second" {
		t.Errorf("answers = %v", got)
	}
	if srv.Requests() != 3 {
		t.Errorf("requests = %d", srv.Requests())
	}
}

func TestFixtureMustPinQuery(t *testing.T) {
	for name, body := range map[string]string{
		"no query":   `{"interactions": [{"operation": "Viewer", "response": {}}]}`,
		"wrong name": `{"interactions": [{"operation": "Viewer", "query": "query Other { a }", "response": {}}]}`,
		"anonymous":  `{"interactions": [{"operation": "Viewer", "query": "{ viewer { login } }", "response": {}}]}`,
	} {
		if _, err := readFile(writeFixture(t, body)); err == nil {
			t.Errorf("%s: readFile accepted it", name)
		}
	}
}

func TestCanceledRequest(t *testing.T) {
	t.Setenv(recordEnv, "")
	srv := New(t, writeFixture(t, `{"interactions": []}`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.github.com/graphql", strings.NewReader(`{"query":"query Viewer { a }"}`))
	if _, err := srv.RoundTrip(req); !errors.Is(err, context.Canceled) {
		t.Errorf("RoundTrip err = %v, want context.Canceled", err)
	}
	if srv.Requests() != 0 {
		t.Errorf("requests = %d, want 0", srv.Requests())
	}
}
