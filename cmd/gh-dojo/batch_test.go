package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// Batches against the real backend: kanban's done has batch = true. The
// fixtures copy the recorded card of #3 for the other numbers.

// TestBatchRefused: one request reads the four cards; #9 is in place, #17
// is refused (exit 2, the refusal decides); #3 and #7 get a mutation each.
// The dry run reads the same and writes nothing.
func TestBatchRefused(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("batch_done_refused"))
	checkRuns(t, srv, before,
		runs{[]string{"done", "3", "7", "9", "17", "--dry-run"}, render.ExitDenied, 1, "batch_refused_dry_run"},
		runs{[]string{"done", "3", "7", "9", "17", "3"}, render.ExitDenied, 3, "batch_refused"},
	)
}

// TestBatchNetwork: the mutation of #7 gets a 502 while #3 and #9 are
// applied: exit 5. The re-run finds #3 and #9 done and sends only #7
// (exit 0); a repeat finds everything in place and writes nothing.
func TestBatchNetwork(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("batch_done_network"))
	checkRuns(t, srv, before,
		runs{[]string{"done", "3", "7", "9"}, render.ExitPartial, 4, "batch_network"},
		runs{[]string{"done", "3", "7", "9"}, render.ExitOK, 2, "batch_network_rerun"},
		runs{[]string{"done", "3", "7", "9"}, render.ExitOK, 1, "batch_network_repeat"},
	)
}

// TestBatchOtherRepo: issues of the default repository and of cli/cli are
// read in one request, as for one repository (spec F12: the requests of a
// verb do not grow). cli/cli#2 is not on the board, so done refuses it.
func TestBatchOtherRepo(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("batch_done_repos"))
	checkRuns(t, srv, before,
		runs{[]string{"done", "3", "cli/cli#2", "--dry-run"}, render.ExitDenied, 1, "batch_other_repo_dry_run"},
		runs{[]string{"done", "3", "cli/cli#2", "--dry-run", "--json"}, render.ExitDenied, 1, "batch_other_repo_dry_run_json"},
	)
}

// TestBatchStale: GitHub rejects the cached option ID for #3, whose close
// goes through. Once per run: the schema is re-read, then #3 and #7 in one
// request; #3 gets its status alone, #7 its status and close. Requests:
// cards, #3, schema, cards, #3, #7.
func TestBatchStale(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("batch_done_stale"))
	checkRuns(t, srv, before, runs{[]string{"done", "3", "7"}, render.ExitOK, 6, "batch_stale"})
}

// TestBatchChunks: 21 issues are read in two requests (20 and 1); all are
// done already, so nothing is written.
func TestBatchChunks(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("batch_done_chunks"))
	args := []string{"done"}
	for n := 101; n <= 121; n++ {
		args = append(args, fmt.Sprint(n))
	}
	checkRuns(t, srv, before, runs{args, render.ExitOK, 2, "batch_chunks"})
}

// TestBatchReadFails: the second request of 21 issues fails (and so does
// #21 read alone, see issue.ReadAll): the 20 cards read are planned, and
// #21 fails with the network error. A dry run, so nothing is written.
func TestBatchReadFails(t *testing.T) {
	srv, before := withWarmVerbApp(t, "../../internal/issue/testdata/graphql/cards_21_down.json")
	args := []string{"done", "--dry-run"}
	for n := 1; n <= 21; n++ {
		args = append(args, fmt.Sprint(n))
	}
	// 1 batch + 3 attempts of the second batch + 3 attempts of #21 alone.
	checkRuns(t, srv, before, runs{args, render.ExitDenied, 7, "batch_read_fails"})
}

// TestBatchLookup: review adds and removes labels and assigns @me; with
// batch = true the label and viewer IDs are looked up once, in the request
// of both cards, and each issue gets its own mutation.
func TestBatchLookup(t *testing.T) {
	withConfig(t, strings.Replace(effectsDojoConfig, "labels = [\"+enhancement\", \"-dogfood\"]\n",
		"labels = [\"+enhancement\", \"-dogfood\"]\nbatch = true\n", 1))
	withSession(t, "abc")
	srv := withApp(t, boardFixture, verbFixture("batch_review"))
	if _, err := newApp().Schema(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkRuns(t, srv, srv.Requests(), runs{[]string{"review", "3", "7", "--comment", "PR #50"}, render.ExitOK, 3, "batch_review"})
}

// TestBatchOutcomes covers the exit code of each mix of outcomes with a
// fake backend: one read for the batch, one Apply per issue to write.
func TestBatchOutcomes(t *testing.T) {
	issues := func(statuses ...string) map[int]workflow.Issue {
		m := map[int]workflow.Issue{}
		for i, s := range statuses {
			n := i + 1
			m[n] = workflow.Issue{Number: n, Title: fmt.Sprintf("Issue %d", n), OnBoard: true, Status: s, Closed: s == "Done"}
		}
		return m
	}
	network := render.Network("GitHub is unavailable (HTTP 502); retry later").WithFix("gh auth status")
	tests := []struct {
		name      string
		args      []string
		issues    map[int]workflow.Issue
		errs      map[int]error
		confirmed map[int]int
		code      render.Code
		applied   int
	}{
		// Every issue applied or in place: exit 0 with a summary line.
		{"all_ok", []string{"done", "1", "2", "3"}, issues("Ready", "In progress", "Done"), nil, nil, render.ExitOK, 2},
		// A refusal next to issues applied: the refusal decides (2).
		{"refused", []string{"done", "1", "2", "3"}, issues("Ready", "Backlog", "Backlog"), nil, nil, render.ExitDenied, 1},
		// Refused and not found, nothing applied: 2 before 3.
		{"refused_not_found", []string{"done", "2", "9", "1"}, issues("Done", "Backlog"), nil, nil, render.ExitDenied, 0},
		// Not found only.
		{"not_found", []string{"done", "1", "9"}, issues("Done"), nil, nil, render.ExitNotFound, 0},
		// A network failure after another issue was applied: 5, re-run.
		{"network", []string{"done", "1", "2"}, issues("Ready", "Ready"), map[int]error{1: network}, nil, render.ExitPartial, 1},
		// A network failure, nothing else to write: 4.
		{"network_only", []string{"done", "1", "2"}, issues("Done", "Ready"), map[int]error{2: network}, nil, render.ExitNetwork, 0},
		// Network and a refusal next to an issue applied: 5; the re-run
		// then ends with the refusal.
		{"network_refused", []string{"done", "1", "2", "3"}, issues("Ready", "Ready", "Backlog"), map[int]error{2: network}, nil, render.ExitPartial, 1},
		// The same reason for several issues is said once.
		{"grouped", []string{"done", "1", "2", "3", "4"}, issues("Ready", "Ready", "Ready", "Ready"),
			map[int]error{1: network, 2: network, 4: network}, nil, render.ExitPartial, 1},
		// #1's status went through, then the write was lost: 5, naming
		// what was done and what is unconfirmed.
		{"partial", []string{"done", "1", "2"}, issues("Ready", "Ready"), map[int]error{1: network}, map[int]int{1: 1}, render.ExitPartial, 1},
		// Too many reasons for one line: the rest are only named.
		{"many", []string{"done", "1", "2", "3", "4", "5", "6", "7", "9"}, issues("Ready", "Backlog", "Waiting", "In review", "Triage", "Icebox", "Parked"),
			nil, nil, render.ExitDenied, 1},
		// A repeated number counts once; one left is the single-issue run.
		{"repeat_number", []string{"done", "1", "#1", "1"}, issues("Ready"), nil, nil, render.ExitOK, 1},
		{"dry_run", []string{"done", "1", "2", "--dry-run", "--comment", "v1.2"}, issues("Ready", "Done"), nil, nil, render.ExitOK, 0},
		{"dry_run_refused", []string{"done", "1", "2", "--dry-run"}, issues("Ready", "Backlog"), nil, nil, render.ExitDenied, 0},
		// A refusal and a network failure, nothing written: the refusal
		// decides (2), and the error still says a re-run retries #2.
		{"refused_network", []string{"done", "1", "2"}, issues("Backlog", "Ready"), map[int]error{2: network}, nil, render.ExitDenied, 0},
		// Not found and a network failure, nothing written: 4 before 3, a
		// re-run may finish #2.
		{"not_found_network", []string{"done", "1", "2", "9"}, issues("Done", "Ready"), map[int]error{2: network}, nil, render.ExitNetwork, 0},
		// Every issue done, one of them in place: "done (1 written)".
		{"in_place", []string{"done", "1", "2"}, issues("Ready", "Done"), nil, nil, render.ExitOK, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, verbsConfig)
			f := &fakeBackend{issues: tt.issues, applyErrs: tt.errs, confirmed: tt.confirmed}
			withBackend(t, f)
			code, out := runOut(tt.args...)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d\n%s", code, tt.code, out)
			}
			if f.reads != 1 || len(f.applied) != tt.applied {
				t.Errorf("reads = %d, applied = %d; want 1, %d", f.reads, len(f.applied), tt.applied)
			}
			testutil.Golden(t, "verbs/batch_"+tt.name, out)
		})
	}
}

// TestVerbsOtherRepo (spec F12): an issue of another repository goes by
// owner/repo#N or its URL, alone or in a batch with the default
// repository's (acme/shop, where a bare number is), and every line names
// it so: the dry run, the confirmations, the summary, the warnings and
// the JSON. A bare --by number on such an issue is mentioned as
// acme/shop#12 in its comment.
func TestVerbsOtherRepo(t *testing.T) {
	shop := map[int]workflow.Issue{
		3:  {Number: 3, Title: "Shop three", OnBoard: true, Status: "Ready"},
		12: {Number: 12, Title: "Fix login", OnBoard: true, Status: "Ready"},
	}
	others := map[string]workflow.Issue{
		"other/repo#3": {Number: 3, Title: "Export rate limit", OnBoard: true, Status: "Ready"},
		"other/repo#4": {Number: 4, Title: "Export docs", OnBoard: true, Status: "Backlog"},
		"other/repo#5": {Number: 5, Title: "Export retries", OnBoard: true, Status: "In progress"},
		"other/repo#6": {Number: 6, Title: "Export auth", Closed: true, StateReason: "not_planned"},
	}
	for _, tt := range []struct {
		name    string
		args    []string
		code    render.Code
		applied int
	}{
		{"dry_run", []string{"done", "12", "other/repo#3", "--dry-run"}, render.ExitOK, 0},
		{"dry_run_json", []string{"done", "12", "other/repo#3", "--dry-run", "--json"}, render.ExitOK, 0},
		{"done", []string{"done", "12", "other/repo#3", "3"}, render.ExitOK, 3},
		// The summary names other/repo's issues in full, #12 is acme/shop's.
		{"refused", []string{"done", "other/repo#4", "12", "https://github.com/other/repo/issues/9", "other/repo#6"}, render.ExitDenied, 1},
		// 12 and acme/shop#12 are one issue: the single-issue run.
		{"same", []string{"done", "12", "acme/shop#12"}, render.ExitOK, 1},
		{"one", []string{"start", "other/repo#4", "--dry-run"}, render.ExitOK, 0},
		{"wait_by", []string{"wait", "other/repo#5", "--by", "12", "--dry-run"}, render.ExitOK, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, verbsConfig)
			f := &fakeBackend{issues: shop, others: others}
			withBackend(t, f)
			code, out := runOut(tt.args...)
			if code != tt.code || len(f.applied) != tt.applied || f.reads != 1 {
				t.Errorf("exit code = %d, applied %d, reads %d; want %d, %d, 1\n%s", code, len(f.applied), f.reads, tt.code, tt.applied, out)
			}
			testutil.Golden(t, "verbs/other_repo_"+tt.name, out)
		})
	}
}

// TestBatchConfirm: a batch verb with confirm = "user" refuses every issue
// that needs it without --yes, and --yes applies to all of them. An issue
// with nothing left to do needs no go-ahead.
func TestBatchConfirm(t *testing.T) {
	withConfig(t, verbsConfig+`
[verbs.drop]
when = "Not needed"
from = ["Backlog", "Done"]
close = "not_planned"
confirm = "user"
batch = true
`)
	is := map[int]workflow.Issue{
		1: {Number: 1, OnBoard: true, Status: "Backlog"},
		2: {Number: 2, OnBoard: true, Status: "Backlog"},
		3: {Number: 3, OnBoard: true, Status: "Done", Closed: true, StateReason: "not_planned"},
		4: {Number: 4, OnBoard: true, Status: "Ready"},
	}
	for _, tt := range []struct {
		name    string
		args    []string
		code    render.Code
		applied int
	}{
		{"confirm_no", []string{"drop", "1", "2", "3"}, render.ExitDenied, 0},
		{"confirm_yes", []string{"drop", "1", "2", "3", "--yes"}, render.ExitOK, 2},
		// A from refusal next to a missing go-ahead: both next steps.
		{"confirm_refused", []string{"drop", "1", "4"}, render.ExitDenied, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeBackend{issues: is}
			withBackend(t, f)
			code, out := runOut(tt.args...)
			if code != tt.code || len(f.applied) != tt.applied {
				t.Errorf("exit code = %d, applied %d; want %d, %d\n%s", code, len(f.applied), tt.code, tt.applied, out)
			}
			testutil.Golden(t, "verbs/batch_"+tt.name, out)
		})
	}
}

// TestBatchArgs: a verb without batch takes one number; a batch verb
// checks every argument before reading anything.
func TestBatchArgs(t *testing.T) {
	withConfig(t, verbsConfig)
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"not_batch", []string{"start", "1", "2"}},
		{"bad_number", []string{"done", "1", "x"}},
		{"no_number", []string{"done"}},
		{"bad_flag_arg", []string{"done", "1", "2", "--by", "3"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeBackend{}
			withBackend(t, f)
			code, out := runOut(tt.args...)
			if code != render.ExitUsage || f.reads != 0 {
				t.Errorf("exit code = %d, reads = %d; want %d, 0\n%s", code, f.reads, render.ExitUsage, out)
			}
			testutil.Golden(t, "verbs/batch_args_"+tt.name, out)
		})
	}
}

// staleReadBackend is fakeBackend whose cached schema lacks what the
// first read of some issues needs (say, today's iteration): those reads
// fail as stale until Reset.
type staleReadBackend struct {
	fakeBackend
	stale  map[int]bool
	resets int
}

func (f *staleReadBackend) SchemaCached() bool { return f.resets == 0 }

func (f *staleReadBackend) Reset() error { f.resets++; return nil }

func (f *staleReadBackend) ReadIssues(ctx context.Context, v config.Verb, refs []issue.Ref, a workflow.Args) ([]IssueRead, error) {
	out, err := f.fakeBackend.ReadIssues(ctx, v, refs, a)
	for i := range out {
		if f.stale[out[i].Ref.Number] && f.resets == 0 {
			out[i].Err = render.Usage("no iteration for today").WithCause(board.ErrUnknownIteration)
		}
	}
	return out, err
}

// TestBatchStaleRead: issues whose read failed on the cached schema are
// read once more with a fresh one, before anything is written, as
// readPlanFresh does for one issue: one read for the batch, one for the
// stale issues.
func TestBatchStaleRead(t *testing.T) {
	withConfig(t, verbsConfig)
	f := &staleReadBackend{stale: map[int]bool{2: true}}
	f.issues = map[int]workflow.Issue{
		1: {Number: 1, OnBoard: true, Status: "Ready"},
		2: {Number: 2, OnBoard: true, Status: "Ready"},
	}
	saved := newBackend
	t.Cleanup(func() { newBackend = saved })
	newBackend = func(*config.Config) (Backend, error) { return f, nil }
	code, out := runOut("done", "1", "2")
	if code != render.ExitOK || f.reads != 2 || f.resets != 1 || len(f.applied) != 2 {
		t.Errorf("exit code %d, reads %d, resets %d, applied %d; want 0, 2, 1, 2\n%s", code, f.reads, f.resets, len(f.applied), out)
	}
}

// TestBatchSummaryFits: 29 issues refused from as many statuses, the
// first a long one. The summary line names the issues that fit and counts
// the rest ("and N more"): no issue is lost to the error block's cut of a
// long line.
func TestBatchSummaryFits(t *testing.T) {
	withConfig(t, verbsConfig)
	is := map[int]workflow.Issue{1: {Number: 1, OnBoard: true, Status: "Ready"}}
	args := []string{"done", "1"}
	for n := 2; n <= 30; n++ {
		is[n] = workflow.Issue{Number: n, OnBoard: true, Status: fmt.Sprintf("Stage %d", n)}
		if n == 2 {
			is[n] = workflow.Issue{Number: n, OnBoard: true, Status: strings.Repeat("Waiting for a review ", 7)}
		}
		args = append(args, fmt.Sprint(n))
	}
	withBackend(t, &fakeBackend{issues: is})
	code, out := runOut(args...)
	if code != render.ExitDenied {
		t.Errorf("exit code = %d, want %d", code, render.ExitDenied)
	}
	checkSummaryCounts(t, string(out), 2, 30)
	testutil.Golden(t, "verbs/batch_summary_fits", out)
}

// checkSummaryCounts checks the error line of out: short enough not to be
// cut, and every issue from lo to hi named in it or counted in an "and N
// more".
func checkSummaryCounts(t *testing.T, out string, lo, hi int) {
	t.Helper()
	_, line, _ := strings.Cut(out, "error: ")
	line, _, _ = strings.Cut(line, "\n")
	// The error block cuts a line at 200 runes; the summary stays within
	// maxSummaryRunes, so the cut never hides an issue.
	if utf8.RuneCountInString(line) > maxSummaryRunes {
		t.Errorf("summary line over %d runes, the error block may cut it:\n%s", maxSummaryRunes, line)
	}
	named := map[int]bool{}
	for _, m := range regexp.MustCompile(`#(\d+)`).FindAllStringSubmatch(line, -1) {
		n, _ := strconv.Atoi(m[1])
		named[n] = true
	}
	counted := 0
	for _, m := range regexp.MustCompile(`and (\d+) more`).FindAllStringSubmatch(line, -1) {
		n, _ := strconv.Atoi(m[1])
		counted += n
	}
	shown := 0
	for n := lo; n <= hi; n++ {
		if named[n] {
			shown++
		}
	}
	if shown+counted != hi-lo+1 {
		t.Errorf("summary names %d and counts %d issues, want %d in all:\n%s", shown, counted, hi-lo+1, line)
	}
}

// TestBatchStopsOnAuth: an error that would fail every write alike (the
// token rejected, a rate limit, the run interrupted) stops the batch: the
// issues after it are not attempted, grouped in one entry, while an issue
// already in place still needs no write and is done.
func TestBatchStopsOnAuth(t *testing.T) {
	withConfig(t, verbsConfig)
	is := map[int]workflow.Issue{
		1: {Number: 1, OnBoard: true, Status: "Ready"},
		2: {Number: 2, OnBoard: true, Status: "Ready"},
		3: {Number: 3, OnBoard: true, Status: "Done", Closed: true, StateReason: "completed"},
		4: {Number: 4, OnBoard: true, Status: "Ready"},
	}
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"auth", render.Network("GitHub rejected the token (HTTP 401)").WithFix(render.AuthFix).
			WithCause(&api.HTTPError{StatusCode: http.StatusUnauthorized})},
		{"rate_limit", render.Network("GitHub API rate limit exceeded; retry later").
			WithCause(&api.GraphQLError{Errors: []api.GraphQLErrorItem{{Type: "RATE_LIMITED"}}})},
		{"interrupted", render.Network("GitHub write failed (interrupted); it may have been applied, re-run the command to check").
			WithCause(errors.Join(graph.ErrUnknownOutcome, context.Canceled))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeBackend{issues: is, applyErrs: map[int]error{1: tt.err}}
			withBackend(t, f)
			code, out := runOut("done", "1", "2", "3", "4")
			if code != render.ExitNetwork || f.attempts != 1 {
				t.Errorf("exit code = %d, Apply calls = %d; want %d, 1\n%s", code, f.attempts, render.ExitNetwork, out)
			}
			testutil.Golden(t, "verbs/batch_stop_"+tt.name, out)
		})
	}
}

// TestBatchStaleOnce: the stale-schema retry is spent once per run,
// whichever step hits it: after the read of #2 was retried with a fresh
// schema, a stale ID on the write of #1 is not retried again.
func TestBatchStaleOnce(t *testing.T) {
	withConfig(t, verbsConfig)
	f := &staleReadBackend{stale: map[int]bool{2: true}}
	f.issues = map[int]workflow.Issue{
		1: {Number: 1, OnBoard: true, Status: "Ready"},
		2: {Number: 2, OnBoard: true, Status: "Ready"},
	}
	f.applyErrs = map[int]error{1: render.Usage("unknown option").WithCause(board.ErrUnknownOption)}
	saved := newBackend
	t.Cleanup(func() { newBackend = saved })
	newBackend = func(*config.Config) (Backend, error) { return f, nil }
	code, out := runOut("done", "1", "2")
	if code != render.ExitUsage || f.resets != 1 || f.reads != 2 {
		t.Errorf("exit code %d, resets %d, reads %d; want 1, 1, 2\n%s", code, f.resets, f.reads, out)
	}
}
