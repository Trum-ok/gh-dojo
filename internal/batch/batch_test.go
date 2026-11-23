package batch

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// The run is tested here on a backend in memory. cmd/gh-dojo's batch
// tests run the verbs and release on the GitHub backend's fixtures, with
// golden files of their output.

const shopConfig = `
version = 1

[board]
owner = "acme"
project = 3
repos = ["acme/shop"]

[fields]
status = "Status"

[statuses]
resolved = ["Done"]

[verbs.done]
when = "The change is released"
to = "Done"
from = ["Ready"]
`

var shop = issue.Repo{Owner: "acme", Name: "shop"}

func ref(n int) issue.Ref { return issue.Ref{Repo: shop, Number: n} }

func refs(ns ...int) []issue.Ref {
	out := make([]issue.Ref, len(ns))
	for i, n := range ns {
		out[i] = ref(n)
	}
	return out
}

// fakeBackend serves issues of acme/shop from memory and applies every
// op of a plan unless the issue has an error.
type fakeBackend struct {
	issues    map[int]workflow.Issue
	readErr   error         // fails every read
	issueErrs map[int]error // the read error of one issue
	applyErrs map[int]error // the Apply error of one issue; nothing confirmed
	cached    bool          // SchemaCached until Reset
	reads     [][]int       // the numbers of each ReadIssues call
	applied   []int         // the numbers of each Apply call
	resets    int
}

func (f *fakeBackend) ReadIssues(_ context.Context, _ config.Verb, refs []issue.Ref, _ workflow.Args) ([]Read, error) {
	var ns []int
	for _, r := range refs {
		ns = append(ns, r.Number)
	}
	f.reads = append(f.reads, ns)
	if f.readErr != nil {
		return nil, f.readErr
	}
	out := make([]Read, len(refs))
	for i, r := range refs {
		out[i].Ref = r
		is, ok := f.issues[r.Number]
		switch {
		case f.issueErrs[r.Number] != nil:
			out[i].Err = f.issueErrs[r.Number]
		case !ok:
			out[i].Err, out[i].Cause = render.NotFound("issue #%d not found in %s", r.Number, shop), issue.CauseMissing
		default:
			is.Number = r.Number
			out[i].Issue = is
		}
	}
	return out, nil
}

func (f *fakeBackend) Apply(_ context.Context, p *workflow.Plan) ([]workflow.Op, error) {
	f.applied = append(f.applied, p.Issue.Number)
	if err := f.applyErrs[p.Issue.Number]; err != nil {
		delete(f.applyErrs, p.Issue.Number) // fails once
		return nil, err
	}
	is := f.issues[p.Issue.Number]
	is.Status = p.Target
	f.issues[p.Issue.Number] = is
	return p.Ops, nil
}

func (f *fakeBackend) Reset() error { f.resets++; f.cached = false; return nil }

func (f *fakeBackend) SchemaCached() bool { return f.cached }

// ready is an issue on the board in status.
func ready(status string) workflow.Issue {
	return workflow.Issue{Title: "Fix it", OnBoard: true, Status: status}
}

func newRun(t *testing.T, be Backend, extra string) (*Run, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cfg, err := config.Parse([]byte(shopConfig+extra), ".github/dojo.toml")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := cfg.Verb("done")
	b := New(be, cfg, v, workflow.Args{}, shop)
	var out, errOut bytes.Buffer
	b.Stdout, b.Stderr = &out, &errOut
	return b, &out, &errOut
}

// wantError fails unless err has code and its text is want.
func wantError(t *testing.T, err error, code render.Code, want string) {
	t.Helper()
	e := render.FromError(err)
	if err == nil || e.Code != code || e.What != want {
		t.Errorf("summary = %d %q\nwant      %d %q", e.Code, e.What, code, want)
	}
}

// TestDo: one read for the batch, one write per issue with something to
// write, a confirmation line for each, and the summary counts the issue
// already in place as done but not written.
func TestDo(t *testing.T) {
	be := &fakeBackend{issues: map[int]workflow.Issue{1: ready("Ready"), 2: ready("Done"), 3: ready("Ready")}}
	b, out, _ := newRun(t, be, "")
	items, err := b.Do(context.Background(), refs(1, 2, 3), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Summary(items, false); err != nil {
		t.Errorf("summary = %v, want nil", err)
	}
	if want := "#1 Ready → Done\n#2 Done (unchanged)\n#3 Ready → Done\n3 of 3 done (2 written)\n"; out.String() != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", out, want)
	}
	if len(be.reads) != 1 || !slices.Equal(be.applied, []int{1, 3}) {
		t.Errorf("reads %v, applied %v; want one read, #1 and #3 applied", be.reads, be.applied)
	}
	if !items[0].Wrote() || items[1].Wrote() {
		t.Error("Wrote: want #1 written, #2 not")
	}
}

// TestDoSkipsFailed: an issue the verb refuses and one not found are
// skipped; the others are applied. The refusal decides the exit code (2)
// and its advice is the next step.
func TestDoSkipsFailed(t *testing.T) {
	be := &fakeBackend{issues: map[int]workflow.Issue{1: ready("Backlog"), 2: ready("Ready"), 4: ready("Backlog")}}
	b, out, _ := newRun(t, be, "")
	items, err := b.Do(context.Background(), refs(1, 2, 3, 4), false)
	if err != nil {
		t.Fatal(err)
	}
	wantError(t, b.Summary(items, false), render.ExitDenied,
		"1 of 4 done; #1, #4: done cannot move it from Backlog, it starts only from Ready; #3: issue #3 not found in acme/shop\n"+
			"this is a process rule: ask the user how to proceed")
	if out.String() != "#2 Ready → Done\n" || !slices.Equal(be.applied, []int{2}) {
		t.Errorf("stdout %q, applied %v; want #2 alone", out, be.applied)
	}
}

// TestDoStops: an error about the token stops the writes: the issues
// left with something to write are not attempted, those in place are
// still done, and a re-run is the next step (exit code 5 after a write).
func TestDoStops(t *testing.T) {
	denied := &api.HTTPError{StatusCode: 401, Message: "Bad credentials"}
	be := &fakeBackend{
		issues:    map[int]workflow.Issue{1: ready("Ready"), 2: ready("Ready"), 3: ready("Ready"), 4: ready("Done")},
		applyErrs: map[int]error{2: render.Network("GitHub rejected the token").WithFix("gh auth login").WithCause(denied)},
	}
	b, _, _ := newRun(t, be, "")
	items, err := b.Do(context.Background(), refs(1, 2, 3, 4), false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(be.applied, []int{1, 2}) || !items[2].Skipped || items[3].Err != nil {
		t.Errorf("applied %v, #3 skipped %v, #4 err %v; want #3 not attempted, #4 done", be.applied, items[2].Skipped, items[3].Err)
	}
	summary := b.Summary(items, false)
	wantError(t, summary, render.ExitPartial,
		"2 of 4 done (1 written); #2: GitHub rejected the token; #3: not attempted: GitHub rejected the token\n"+
			"re-run the same command to retry #2, #3: it skips the issues done")
	if fix := render.FromError(summary).Fix; fix != "gh auth login" {
		t.Errorf("fix = %q, want the token's", fix)
	}
}

// TestDoJSON: with JSON the run prints nothing on stdout; the command
// emits the items.
func TestDoJSON(t *testing.T) {
	be := &fakeBackend{issues: map[int]workflow.Issue{1: ready("Ready"), 2: ready("Ready")}}
	b, out, _ := newRun(t, be, "")
	b.JSON = true
	items, err := b.Do(context.Background(), refs(1, 2), false)
	if err != nil || b.Summary(items, false) != nil || out.Len() != 0 {
		t.Errorf("err %v, stdout %q; want nothing printed", err, out)
	}
}

// TestDoDryRun: the plans are printed, nothing is applied; the summary
// names only what cannot be planned.
func TestDoDryRun(t *testing.T) {
	be := &fakeBackend{issues: map[int]workflow.Issue{1: ready("Ready"), 2: ready("Backlog")}}
	b, out, _ := newRun(t, be, "")
	items, err := b.Do(context.Background(), refs(1, 2), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(be.applied) != 0 || !strings.HasPrefix(out.String(), "dry run: done #1") || strings.Contains(out.String(), "#2") {
		t.Errorf("applied %v, stdout:\n%s\nwant #1's plan alone", be.applied, out)
	}
	wantError(t, b.Summary(items, true), render.ExitDenied,
		"dry run: 1 of 2 can be applied; #2: done cannot move #2 from Backlog, it starts only from Ready\n"+
			"this is a process rule: ask the user how to proceed")
}

// TestDoReadFails: a read that fails them all fails the run, with nothing
// written.
func TestDoReadFails(t *testing.T) {
	be := &fakeBackend{readErr: render.Network("cannot reach GitHub")}
	b, _, _ := newRun(t, be, "")
	if items, err := b.Do(context.Background(), refs(1, 2), false); err == nil || items != nil || len(be.applied) != 0 {
		t.Errorf("items %v, err %v; want the read's error alone", items, err)
	}
}

// TestDoKeepBefore: Keep drops issues read before they are planned,
// Before the planned ones before anything is written; neither is in the
// result.
func TestDoKeepBefore(t *testing.T) {
	be := &fakeBackend{issues: map[int]workflow.Issue{1: ready("Ready"), 2: ready("Ready"), 3: ready("Ready")}}
	b, _, _ := newRun(t, be, "")
	b.Keep = func(r Read) bool { return r.Ref.Number != 1 }
	var planned []int
	b.Before = func(items []*Item) []*Item {
		for _, it := range items {
			planned = append(planned, it.Ref.Number)
		}
		return items[1:]
	}
	items, err := b.Do(context.Background(), refs(1, 2, 3), false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(planned, []int{2, 3}) || len(items) != 1 || items[0].Ref.Number != 3 || !slices.Equal(be.applied, []int{3}) {
		t.Errorf("planned %v, result %d items, applied %v; want #3 alone", planned, len(items), be.applied)
	}
}

// TestDoSubIssues: with sub_issues = "deny", a parent and its sub-issue in
// one run: the sub-issue is applied first, and the parent counts it as
// resolved. A sub-issue that then fails to be written refuses the parent.
func TestDoSubIssues(t *testing.T) {
	parent := ready("Ready")
	parent.UnresolvedSubIssues = []workflow.Blocker{{Ref: "#2", Place: "Ready"}}
	be := &fakeBackend{issues: map[int]workflow.Issue{1: parent, 2: ready("Ready")}}
	b, out, _ := newRun(t, be, `sub_issues = "deny"`+"\n")
	items, err := b.Do(context.Background(), refs(1, 2), false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(be.applied, []int{2, 1}) || b.Summary(items, false) != nil {
		t.Errorf("applied %v, stdout:\n%s\nwant #2, then #1", be.applied, out)
	}
	if items[0].Ref.Number != 1 {
		t.Error("the result is not in the order of the arguments")
	}

	be = &fakeBackend{issues: map[int]workflow.Issue{1: parent, 2: ready("Ready")},
		applyErrs: map[int]error{2: render.Usage("GitHub rejected the change")}}
	b, _, _ = newRun(t, be, `sub_issues = "deny"`+"\n")
	items, _ = b.Do(context.Background(), refs(1, 2), false)
	if !slices.Equal(be.applied, []int{2}) || render.FromError(items[0].Err).Code != render.ExitDenied ||
		!strings.Contains(items[0].Err.Error(), "unresolved sub-issues: #2 (failed in this run)") {
		t.Errorf("applied %v, parent's error %v; want it refused", be.applied, items[0].Err)
	}
}

// TestDoStale: a read that fails on the cached schema is retried once
// with a fresh one; a write GitHub refuses for a stale ID resets the
// schema, re-reads the issues not applied yet and goes on, once per run.
func TestDoStale(t *testing.T) {
	stale := render.Usage("no iteration for today").WithCause(board.ErrUnknownIteration)
	be := &fakeBackend{issues: map[int]workflow.Issue{1: ready("Ready"), 2: ready("Ready")},
		issueErrs: map[int]error{1: stale}, cached: true}
	b, _, _ := newRun(t, be, "")
	// The issue's error stays the same here, so the re-read fails alike.
	items, err := b.Do(context.Background(), refs(1, 2), false)
	if err != nil {
		t.Fatal(err)
	}
	if be.resets != 1 || len(be.reads) != 2 || !slices.Equal(be.reads[1], []int{1}) || items[0].Err == nil {
		t.Errorf("resets %d, reads %v, #1 err %v; want #1 re-read once", be.resets, be.reads, items[0].Err)
	}

	staleID := &api.GraphQLError{Errors: []api.GraphQLErrorItem{{Message: "The item does not belong to the project"}}}
	be = &fakeBackend{issues: map[int]workflow.Issue{1: ready("Ready"), 2: ready("Ready"), 3: ready("Ready")},
		applyErrs: map[int]error{2: staleID}}
	b, out, _ := newRun(t, be, "")
	items, err = b.Do(context.Background(), refs(1, 2, 3), false)
	if err != nil || b.Summary(items, false) != nil {
		t.Fatalf("err %v, summary %v", err, b.Summary(items, false))
	}
	if be.resets != 1 || !slices.Equal(be.reads[1], []int{2, 3}) || !slices.Equal(be.applied, []int{1, 2, 2, 3}) {
		t.Errorf("resets %d, reads %v, applied %v; want #2 and #3 re-read, #2 applied again", be.resets, be.reads, be.applied)
	}
	if !strings.HasSuffix(out.String(), "3 of 3 done\n") {
		t.Errorf("stdout:\n%s", out)
	}
}

// TestReadPlan: one issue is read and planned; a failure on the cached
// schema is retried once with a fresh one, any other failure is the
// item's.
func TestReadPlan(t *testing.T) {
	cfg, err := config.Parse([]byte(shopConfig), ".github/dojo.toml")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := cfg.Verb("done")
	be := &fakeBackend{issues: map[int]workflow.Issue{1: ready("Ready"), 2: ready("Backlog")}, cached: true}
	it, err := ReadPlan(context.Background(), be, workflow.New(cfg), v, ref(1), workflow.Args{})
	if err != nil || it.Writes == nil || it.Writes.First.Target != "Done" {
		t.Fatalf("ReadPlan = %+v, %v", it, err)
	}
	it, err = ReadPlan(context.Background(), be, workflow.New(cfg), v, ref(2), workflow.Args{})
	if err != nil || it.Writes != nil || it.Issue == nil || render.FromError(it.Err).Code != render.ExitDenied {
		t.Errorf("a refusal: %+v, %v; want the item's error, with the issue read", it, err)
	}
	if be.resets != 0 {
		t.Errorf("%d resets, want none for errors not about the schema", be.resets)
	}

	be.issueErrs = map[int]error{1: render.Usage("unknown option").WithCause(board.ErrUnknownOption)}
	if it, _ := ReadPlan(context.Background(), be, workflow.New(cfg), v, ref(1), workflow.Args{}); be.resets != 1 || len(be.reads) != 4 || it.Err == nil {
		t.Errorf("resets %d, reads %d; want one retry", be.resets, len(be.reads))
	}
}

// TestWrites: an error with every op confirmed is a warning; a write that
// applied part of the plan is exit code 5 naming what was done and what
// was not.
func TestWrites(t *testing.T) {
	plan := &workflow.Plan{Verb: "done", Issue: workflow.Issue{Number: 7, Status: "Ready", OnBoard: true}, Target: "Done",
		Ops: []workflow.Op{workflow.AddComment{Body: "Shipped.", Marker: "m"}, workflow.SetStatus{From: "Ready", To: "Done"}}}
	var warn bytes.Buffer
	w := &Writes{First: plan, Stderr: &warn}
	be := &onlyBackend{done: plan.Ops, err: render.Network("timeout")}
	if err := w.Apply(context.Background(), be, plan); err != nil || !strings.Contains(warn.String(), "after applying every change: timeout") {
		t.Errorf("err %v, warning %q", err, warn.String())
	}

	w = &Writes{First: plan}
	be = &onlyBackend{done: plan.Ops[:1], err: render.Network("GitHub's answer lacked an ID").WithCause(errors.Join(ErrNotSent, errors.New("no item ID")))}
	err := w.ApplyFresh(context.Background(), be, nil)
	wantError(t, err, render.ExitPartial,
		"#7 comment left; status not changed: GitHub's answer lacked an ID; re-run the same command")

	w = &Writes{First: plan}
	be = &onlyBackend{done: plan.Ops[:1], err: render.Network("timeout")}
	wantError(t, w.ApplyFresh(context.Background(), be, nil), render.ExitPartial,
		"#7 comment left; the rest is unconfirmed: timeout; re-run the same command")

	// #108: a rate limit names the wait, as the content limit does.
	limit := &api.HTTPError{StatusCode: 403, Message: "You have exceeded a secondary rate limit.", Headers: http.Header{"Retry-After": {"60"}},
		RequestURL: &url.URL{Scheme: "https", Host: "api.github.com", Path: "/graphql"}}
	w = &Writes{First: plan}
	be = &onlyBackend{done: plan.Ops[:1], err: limit}
	err = w.ApplyFresh(context.Background(), be, nil)
	wantError(t, err, render.ExitPartial,
		"#7 comment left; the rest is unconfirmed: GitHub API rate limit exceeded; wait 60s, then re-run the same command")
	if fix := render.FromError(err).Fix; fix != "wait 60s, then re-run the same command" {
		t.Errorf("fix = %q", fix)
	}
}

// onlyBackend answers every Apply with done and err.
type onlyBackend struct {
	done []workflow.Op
	err  error
}

func (onlyBackend) ReadIssues(context.Context, config.Verb, []issue.Ref, workflow.Args) ([]Read, error) {
	return nil, errors.New("no reads")
}

func (b *onlyBackend) Apply(context.Context, *workflow.Plan) ([]workflow.Op, error) {
	return b.done, b.err
}

func (onlyBackend) Reset() error { return nil }

// TestMissing: ops left to send compare by content, not kind. After a
// stale-ID retry the re-plan may add a label the first attempt did not
// (+b after +a): it is still sent.
func TestMissing(t *testing.T) {
	done := []workflow.Op{workflow.AddLabels{Names: []string{"a"}}, workflow.AddComment{Body: "x", Marker: "m"}}
	ops := []workflow.Op{
		workflow.AddLabels{Names: []string{"b"}},
		workflow.AddLabels{Names: []string{"a"}},
		workflow.AddComment{Body: "x", Marker: "m"},
		workflow.SetStatus{From: "Ready", To: "Done"},
	}
	got := missing(ops, done)
	want := []workflow.Op{workflow.AddLabels{Names: []string{"b"}}, workflow.SetStatus{From: "Ready", To: "Done"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("missing = %v, want %v", got, want)
	}
}

// TestGeneralReason: the issue's own number is worded away, another
// issue's or repository's stays.
func TestGeneralReason(t *testing.T) {
	other := issue.Ref{Repo: issue.Repo{Owner: "acme", Name: "api"}, Number: 9}
	for _, tt := range []struct {
		why  string
		ref  issue.Ref
		want string
	}{
		{"issue #9 not found in acme/shop", ref(9), "issue not found in acme/shop"},
		{"#9 is closed", ref(9), "it is closed"},
		{"#9 is blocked by #12", ref(9), "it is blocked by #12"},
		{"acme/api#9 is closed", other, "it is closed"},
		{"acme/api#9 is closed", ref(9), "acme/api#9 is closed"},
	} {
		if got := generalReason(tt.why, tt.ref); got != tt.want {
			t.Errorf("generalReason(%q) = %q, want %q", tt.why, got, tt.want)
		}
	}
}

// TestBatchCode: after a write, a failure a re-run may finish is 5;
// otherwise 2 comes first, then 1, 4, 3.
func TestBatchCode(t *testing.T) {
	fail := func(errs ...error) []*Item {
		var out []*Item
		for _, err := range errs {
			out = append(out, &Item{Err: err})
		}
		return out
	}
	network, denied, usage, missing := render.Network("x"), render.Denied("x"), render.Usage("x"), render.NotFound("x")
	for _, tt := range []struct {
		failed []*Item
		wrote  bool
		want   render.Code
	}{
		{fail(network, denied), true, render.ExitPartial},
		{fail(network, denied), false, render.ExitDenied},
		{fail(missing, usage), false, render.ExitUsage},
		{fail(missing, network), false, render.ExitNetwork},
		{fail(missing), true, render.ExitNotFound},
		{fail(errors.Join(graph.ErrUnknownOutcome, network)), true, render.ExitPartial},
	} {
		if got := batchCode(tt.failed, tt.wrote); got != tt.want {
			t.Errorf("batchCode(wrote %v) = %d, want %d", tt.wrote, got, tt.want)
		}
	}
}

// TestSummaryFits: many failures for many reasons: the first line stays
// within MaxSummaryRunes and names or counts every issue not done.
func TestSummaryFits(t *testing.T) {
	var items []*Item
	for n := 1; n <= 40; n++ {
		it := &Item{Ref: ref(n)}
		it.Fail(render.Usage("reason number %d, rather long to fill the line", n))
		items = append(items, it)
	}
	b := &Run{def: shop, Stdout: &bytes.Buffer{}}
	line, _, _ := strings.Cut(render.FromError(b.Summary(items, false)).What, "\n")
	if render.RuneLen(line) > MaxSummaryRunes {
		t.Errorf("%d runes, want at most %d:\n%s", render.RuneLen(line), MaxSummaryRunes, line)
	}
	if !strings.HasPrefix(line, "0 of 40 done; #1: ") || !strings.Contains(line, "; also not done: #") || !strings.HasSuffix(line, " more") {
		t.Errorf("line:\n%s", line)
	}
}
