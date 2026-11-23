package graphtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/graph"
)

// FakeToken is the token replay clients send. Tests assert it never shows
// up in output or errors.
const FakeToken = "ghp_graphtestFAKEtoken0000000000000000000"

const (
	recordEnv          = "GH_DOJO_RECORD"
	recordMutationsEnv = "GH_DOJO_RECORD_MUTATIONS"
)

// recording reports whether tests run in record mode (GH_DOJO_RECORD=1).
func recording() bool { return os.Getenv(recordEnv) != "" }

// File is the on-disk fixture format.
type File struct {
	// Comment says what the file is for and, if hand-crafted, where the
	// response shape comes from.
	Comment string `json:"comment,omitempty"`
	// Handcrafted files can't be recorded (errors, outages); tests using
	// them are skipped in record mode.
	Handcrafted  bool           `json:"handcrafted,omitempty"`
	Interactions []*Interaction `json:"interactions"`
}

// Interaction is one GraphQL request and its response.
type Interaction struct {
	// Operation is the operation name, e.g. "BoardSchema".
	Operation string `json:"operation"`
	// Query must match the request's query (whitespace and commas ignored),
	// so a changed query never replays a stale response. Required.
	Query string `json:"query"`
	// Variables must equal the request's variables. Omitted (null) matches
	// any variables.
	Variables json.RawMessage `json:"variables,omitempty"`
	// Repeat lets the interaction answer more than once. Off by default:
	// every request must be expected.
	Repeat bool `json:"repeat,omitempty"`
	// Status is the HTTP status; 0 means 200.
	Status int `json:"status,omitempty"`
	// Headers are extra response headers, e.g. Retry-After.
	Headers map[string]string `json:"headers,omitempty"`
	// Response is the response body; a JSON string when the Content-Type
	// header says it is not JSON.
	Response json.RawMessage `json:"response"`

	file int // index into Server.files
	used int
}

// Server is an http.RoundTripper that replays fixtures (or, in record mode,
// forwards to GitHub and writes them). Build one with New.
type Server struct {
	t      testing.TB
	files  []string
	loaded []*File
	record bool

	mu       sync.Mutex
	list     []*Interaction
	requests int
	sleeps   []time.Duration
	recorded []*Interaction
	token    string
	client   *graph.Client
	headers  map[string]string // SetHeader's, on every replayed answer
}

// SetHeader adds the response header name: value to every answer replayed
// from now on, over the interaction's own: a header recordings do not
// keep, such as the X-OAuth-Scopes of a classic token, without copying
// the fixtures.
func (s *Server) SetHeader(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.headers == nil {
		s.headers = map[string]string{}
	}
	s.headers[name] = value
}

// New returns a Server replaying the interactions of the given fixture
// files (paths relative to the test's package, e.g.
// "testdata/graphql/user_board.json"). Files are combined in order.
//
// Matching: a request matches an interaction with the same operation name,
// query and variables. Each interaction answers once, in file order; one
// with "repeat": true keeps answering after that (the last such match
// does). A request with no answer fails the test; so does an interaction
// never requested, at cleanup.
//
// In record mode (GH_DOJO_RECORD=1) requests go to GitHub with gh's real
// token and the responses are written back at cleanup: each interaction to
// the first file that already has one with the same operation name, else to
// the last file. Mutations are refused unless GH_DOJO_RECORD_MUTATIONS=1.
// Tests with a hand-crafted file are skipped.
func New(t testing.TB, files ...string) *Server {
	t.Helper()
	if len(files) == 0 {
		t.Fatal("graphtest.New: no fixture files")
	}
	s := &Server{t: t, files: files, record: recording()}
	for i, path := range files {
		f, err := readFile(path)
		if errors.Is(err, os.ErrNotExist) && s.record {
			f = &File{}
		} else if err != nil {
			t.Fatalf("graphtest: %v", err)
		}
		if f.Handcrafted && s.record {
			t.Skipf("graphtest: %s is hand-crafted; nothing to record", path)
		}
		for _, in := range f.Interactions {
			in.file = i
		}
		s.loaded = append(s.loaded, f)
		s.list = append(s.list, f.Interactions...)
	}
	t.Cleanup(s.finish)
	return s
}

func readFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	for i, in := range f.Interactions {
		if in.Query == "" {
			return nil, fmt.Errorf("%s: interaction %d has no query; copy the query the code sends", path, i)
		}
		if _, op, ok := operation(in.Query); !ok || op != in.Operation {
			return nil, fmt.Errorf("%s: interaction %d: operation %q does not match its query (want %q)", path, i, in.Operation, op)
		}
	}
	return &f, nil
}

// opRE finds the leading named operation of a document. graph's callers
// put the operation first and name it.
var opRE = regexp.MustCompile(`^\s*(?:#[^\n]*\n\s*)*(query|mutation|subscription)\s+([A-Za-z_][A-Za-z0-9_]*)`)

func operation(query string) (kind, name string, ok bool) {
	m := opRE.FindStringSubmatch(query)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// Client returns a graph client wired to the server: a fake token and
// instant retries when replaying, gh's token when recording. Each call
// returns the same client.
func (s *Server) Client() *graph.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		opts := graph.Options{Transport: s}
		if !s.record {
			opts.Host = "github.com"
			opts.AuthToken = FakeToken
			opts.Sleep = s.sleep
		}
		s.client = graph.New(opts)
	}
	return s.client
}

// Requests reports how many GraphQL requests reached the server.
func (s *Server) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// Sleeps reports the retry pauses the replay client asked for.
func (s *Server) Sleeps() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sleeps)
}

func (s *Server) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.sleeps = append(s.sleeps, d)
	s.mu.Unlock()
	return ctx.Err()
}

type request struct {
	Query     string          `json:"query"`
	Variables json.RawMessage `json:"variables"`
}

// RoundTrip implements http.RoundTripper.
func (s *Server) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	var r request
	if err := json.Unmarshal(body, &r); err != nil {
		s.t.Errorf("graphtest: request body is not GraphQL JSON: %v", err)
		return nil, err
	}
	kind, op, ok := operation(r.Query)
	if !ok {
		err := fmt.Errorf("query must start with a named operation: %.60q", r.Query)
		s.t.Errorf("graphtest: %v", err)
		return nil, err
	}
	vars := canonical(r.Variables)

	s.mu.Lock()
	s.requests++
	s.mu.Unlock()

	if s.record {
		return s.forward(req, body, kind, op, r.Query, vars)
	}
	in, err := s.match(op, r.Query, vars)
	if err != nil {
		s.t.Errorf("graphtest: %v", err)
		return nil, err
	}
	s.mu.Lock()
	headers := maps.Clone(in.Headers)
	if len(s.headers) > 0 && headers == nil {
		headers = map[string]string{}
	}
	maps.Copy(headers, s.headers)
	s.mu.Unlock()
	return response(req, in.Status, headers, in.Response), nil
}

func (s *Server) match(op, query, vars string) (*Interaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var repeat, used, differs *Interaction
	for _, in := range s.list {
		anyVars := len(in.Variables) == 0 || string(in.Variables) == "null"
		if in.Operation != op || !anyVars && canonical(in.Variables) != vars {
			continue
		}
		if normalize(in.Query) != normalize(query) {
			// Another query with the same name and variables, such as a
			// card read with a wider window by another verb.
			if differs == nil {
				differs = in
			}
			continue
		}
		if in.used == 0 {
			in.used++
			return in, nil
		}
		used = in
		if in.Repeat {
			repeat = in
		}
	}
	switch {
	case repeat != nil:
		repeat.used++
		return repeat, nil
	case used != nil:
		return nil, fmt.Errorf("request %s %s answered already by %s; add another interaction or \"repeat\": true",
			op, vars, s.files[used.file])
	case differs != nil:
		return nil, fmt.Errorf("%s in %s has a different query than the code sends; re-record (%s=1 go test -run %s) or update the hand-crafted fixture",
			op, s.files[differs.file], recordEnv, s.t.Name())
	}
	return nil, fmt.Errorf("unexpected request %s %s: no fixture in %s", op, vars, strings.Join(s.files, ", "))
}

func (s *Server) forward(req *http.Request, body []byte, kind, op, query, vars string) (*http.Response, error) {
	if kind == "mutation" && os.Getenv(recordMutationsEnv) == "" {
		err := fmt.Errorf("refusing to send mutation %s to GitHub while recording; set %s=1 to allow", op, recordMutationsEnv)
		s.t.Error("graphtest: " + err.Error())
		return nil, err
	}
	if _, tok, ok := strings.Cut(req.Header.Get("Authorization"), " "); ok {
		s.mu.Lock()
		s.token = tok
		s.mu.Unlock()
	}
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	resp, err := http.DefaultTransport.RoundTrip(out)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	in := &Interaction{Operation: op, Query: query, Variables: json.RawMessage(vars), Response: data}
	if !json.Valid(data) {
		in.Response, _ = json.Marshal(string(data))
	}
	if resp.StatusCode != http.StatusOK {
		in.Status = resp.StatusCode
		for _, h := range []string{"Content-Type", "Retry-After", "X-RateLimit-Remaining", "X-RateLimit-Reset"} {
			if v := resp.Header.Get(h); v != "" {
				if in.Headers == nil {
					in.Headers = map[string]string{}
				}
				in.Headers[h] = v
			}
		}
	}
	s.mu.Lock()
	s.recorded = append(s.recorded, in)
	s.mu.Unlock()
	return response(req, in.Status, in.Headers, data), nil
}

func response(req *http.Request, status int, headers map[string]string, body []byte) *http.Response {
	if status == 0 {
		status = http.StatusOK
	}
	h := http.Header{"Content-Type": {"application/json; charset=utf-8"}}
	for k, v := range headers {
		h.Set(k, v)
	}
	// A non-JSON body (e.g. an HTML error page) is stored as a JSON string.
	if !strings.Contains(h.Get("Content-Type"), "json") {
		var text string
		if json.Unmarshal(body, &text) == nil {
			body = []byte(text)
		}
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

func (s *Server) finish() {
	if s.record {
		s.write()
		return
	}
	for _, in := range s.list {
		if in.used == 0 {
			s.t.Errorf("graphtest: fixture %s %s in %s was never requested",
				in.Operation, canonical(in.Variables), s.files[in.file])
		}
	}
}

// write saves recorded interactions, grouped by target file. Identical
// consecutive answers to the same query collapse into one with "repeat":
// true; mutations never collapse. A "repeat" flag already in the file is
// kept.
func (s *Server) write() {
	if s.t.Failed() {
		s.t.Log("graphtest: test failed; fixtures not written")
		return
	}
	byFile := map[int][]*Interaction{}
	for _, in := range s.recorded {
		target := s.target(in.Operation)
		prev := byFile[target]
		if old := lastWithKey(s.loaded[target].Interactions, in); old != nil && old.Repeat {
			in.Repeat = true
		}
		kind, _, _ := operation(in.Query)
		if dup := lastWithKey(prev, in); kind != "mutation" && dup != nil && dup.Status == in.Status && bytes.Equal(dup.Response, in.Response) {
			dup.Repeat = true
			continue
		}
		byFile[target] = append(prev, in)
	}
	for i, list := range byFile {
		f := &File{Comment: s.loaded[i].Comment, Interactions: list}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(f); err != nil {
			s.t.Errorf("graphtest: encode %s: %v", s.files[i], err)
			continue
		}
		if s.token != "" && bytes.Contains(buf.Bytes(), []byte(s.token)) {
			s.t.Errorf("graphtest: recorded response for %s contains the token; not written", s.files[i])
			continue
		}
		if err := os.WriteFile(s.files[i], buf.Bytes(), 0o644); err != nil {
			s.t.Errorf("graphtest: %v", err)
			continue
		}
		s.t.Logf("graphtest: recorded %d interaction(s) to %s", len(list), s.files[i])
	}
}

func (s *Server) target(op string) int {
	for i, f := range s.loaded {
		for _, in := range f.Interactions {
			if in.Operation == op {
				return i
			}
		}
	}
	return len(s.files) - 1
}

func lastWithKey(list []*Interaction, in *Interaction) *Interaction {
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Operation == in.Operation && canonical(list[i].Variables) == canonical(in.Variables) {
			return list[i]
		}
	}
	return nil
}

// canonical re-encodes JSON with sorted keys and no spaces; null and empty
// input become "{}".
func canonical(raw json.RawMessage) string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil || v == nil {
		return "{}"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSpace(buf.String())
}

func normalize(query string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', ',':
			return -1
		}
		return r
	}, query)
}
