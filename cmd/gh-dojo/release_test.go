package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/release"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// releaseKeywords adds Refs to the keywords, as gh-dojo's own config does.
const releaseKeywords = `
[release]
keywords = ["closes", "fixes", "resolves", "refs"]
`

// TestReleaseNotReady: v0.2.0 since the previous version tag, v0.1.0 (two
// pages of tags), finds #3, #7, #9 and #17 in one page of commits, then
// runs done on #3 and #7 as gh dojo done 3 7 does (TestBatchRefused): both
// are written. #9, closed in Done, is done with: listed as already closed
// and left out. #17, in Backlog, is not ready for done: listed, not
// failed (exit 0). The dry run lists how each issue was found, then the
// plans.
func TestReleaseNotReady(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("release_dojo"), verbFixture("batch_done_refused"))
	withConfig(t, verbDojoConfig+releaseKeywords)
	checkRuns(t, srv, before,
		// tags (2 pages), commits, cards
		runs{[]string{"release", "v0.2.0", "--verb", "done", "--dry-run"}, render.ExitOK, 4, "release_dry_run"},
		// the same, then a mutation each for #3 and #7
		runs{[]string{"release", "v0.2.0", "--verb", "done"}, render.ExitOK, 6, "release"},
	)
}

// TestReleaseOtherRepo: --since a tag is compared; cli/cli#2 is found
// through the closing references of merged pull request #50, #3 through
// its message, and both are read in one request (TestBatchOtherRepo).
// cli/cli#2 is closed but not on the board: not "not ready", since no
// later release would move it, but refused (exit code 2).
func TestReleaseOtherRepo(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("release_repos"), verbFixture("batch_done_repos"))
	withConfig(t, verbDojoConfig+releaseKeywords)
	args := []string{"release", "v0.2.0", "--since", "v0.1.0", "--verb", "done", "--dry-run"}
	checkRuns(t, srv, before,
		runs{args, render.ExitDenied, 3, "release_other_repo"},
		runs{append(args, "--json"), render.ExitDenied, 3, "release_other_repo_json"},
	)
}

// TestReleaseFake runs release on the fake backend (acme/shop) with
// GitHub's default keywords: --since a commit reads the history; #5 is a
// pull request a message names, left out with a warning.
func TestReleaseFake(t *testing.T) {
	shop := func() *fakeBackend {
		return &fakeBackend{
			issues: map[int]workflow.Issue{
				42: {Number: 42, Title: "Fix login redirect", OnBoard: true, Status: "Ready"},
				43: {Number: 43, Title: "Speed up checkout", OnBoard: true, Status: "Done", Closed: true},
			},
			pulls: map[int]bool{5: true},
		}
	}
	tests := []struct {
		name    string
		config  string
		args    []string
		code    render.Code
		applied int
		change  func(map[int]workflow.Issue) // changes the issues of acme/shop
	}{
		{"fake_dry_run", verbsConfig, []string{"--dry-run"}, render.ExitOK, 0, nil},
		{"fake", verbsConfig, nil, render.ExitOK, 1, nil},
		{"fake_json", verbsConfig, []string{"--json"}, render.ExitOK, 1, nil},
		{"fake_comment", verbsConfig, []string{"--comment", "Released in v1.0.0."}, render.ExitOK, 1, nil},
		// No keywords: only pull requests count, and there are none.
		{"fake_no_keywords", verbsConfig + "[release]\nkeywords = []\n", nil, render.ExitOK, 0, nil},
		{"fake_no_keywords_json", verbsConfig + "[release]\nkeywords = []\n", []string{"--json"}, render.ExitOK, 0, nil},
		// A refusal other than from still fails the batch: the user's
		// go-ahead. #43, closed in Done, is left out and needs none.
		{"fake_confirm", verbsConfig + "[verbs.done]\nconfirm = \"user\"\n", nil, render.ExitDenied, 0, nil},
		// #42 in Ready is not ready for a done from Waiting: listed, exit 0.
		{"fake_not_ready", verbsConfig + "[verbs.done]\nfrom = [\"Waiting\"]\n", nil, render.ExitOK, 0, nil},
		{"fake_not_ready_json", verbsConfig + "[verbs.done]\nfrom = [\"Waiting\"]\n", []string{"--json"}, render.ExitOK, 0, nil},
		// #43, closed by "fixes #43" while in progress: done from Ready
		// refuses it, and so does release (exit code 2), since no later
		// release would move it. #42 is applied.
		{"fake_closed_in_progress", verbsConfig + "[verbs.done]\nfrom = [\"Ready\"]\n", nil, render.ExitDenied, 1,
			func(m map[int]workflow.Issue) {
				m[43] = workflow.Issue{Number: 43, Title: "Speed up checkout", OnBoard: true, Status: "In progress", Closed: true,
					StateReason: "completed"}
			}},
		// #43, closed in Done, is done with: listed as already closed and
		// left out, for a verb that works only on open issues too (ready,
		// which would refuse it), exit 0. #42 is already in Ready.
		{"fake_closed_open_only", verbsConfig, []string{"--verb", "ready"}, render.ExitOK, 0, nil},
		{"fake_closed_open_only_json", verbsConfig, []string{"--verb", "ready", "--json", "--dry-run"}, render.ExitOK, 0, nil},
		// #43 moved to Done by the board's "Item closed" workflow when a
		// commit closed it: it did not go through the process, so the verb
		// gets it as before: done repeats, ready refuses it (exit code 2).
		{"fake_closed_automated", verbsConfig, nil, render.ExitOK, 1, closedByAutomation},
		{"fake_closed_automated_open_only", verbsConfig, []string{"--verb", "ready"}, render.ExitDenied, 0, closedByAutomation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, tt.config)
			withApp(t, verbFixture("release_shop"))
			f := shop()
			if tt.change != nil {
				tt.change(f.issues)
			}
			withBackend(t, f)
			code, out := runOut(append([]string{"release", "HEAD", "--since", "abc1234", "--verb", "done"}, tt.args...)...)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d\n%s", code, tt.code, out)
			}
			if len(f.applied) != tt.applied {
				t.Errorf("applied %d plans, want %d", len(f.applied), tt.applied)
			}
			testutil.Golden(t, "release/"+tt.name, out)
		})
	}
	// No commits since the start: nothing to read.
	t.Run("no_commits", func(t *testing.T) {
		withConfig(t, verbsConfig)
		withApp(t, verbFixture("release_shop_empty"))
		f := shop()
		withBackend(t, f)
		code, out := runOut("release", "v1.0.0", "--since", "v0.9.0", "--verb", "done")
		if code != render.ExitOK || f.reads != 0 {
			t.Errorf("exit code = %d, reads = %d\n%s", code, f.reads, out)
		}
		testutil.Golden(t, "release/no_commits", out)
	})
}

// closedByAutomation makes #43 of acme/shop closed in Done by the board's
// workflows.
func closedByAutomation(m map[int]workflow.Issue) {
	is := m[43]
	is.StatusAutomated = true
	m[43] = is
}

// TestReleaseSuffix:with [release] suffix = true, #42 is found in the
// suffix of the squash commit of pull request #60, whose "(#60)" is left
// out with no warning; "(#5)", another pull request, is left out with
// one; #43, named by keyword and suffix in one commit, is found once, by
// keyword. Off, the default, only the keyword counts. With no keywords
// and no suffix in the messages, the head says where release looked.
func TestReleaseSuffix(t *testing.T) {
	suffix := verbsConfig + "[release]\nsuffix = true\n"
	tests := []struct {
		name, config, fixture string
		args                  []string
	}{
		{"suffix_dry_run", suffix, "release_shop_suffix", []string{"--dry-run"}},
		{"suffix_json", suffix, "release_shop_suffix", []string{"--dry-run", "--json"}},
		{"suffix_off", verbsConfig, "release_shop_suffix", []string{"--dry-run"}},
		{"suffix_none", verbsConfig + "[release]\nkeywords = []\nsuffix = true\n", "release_shop", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, tt.config)
			withApp(t, verbFixture(tt.fixture))
			withBackend(t, &fakeBackend{
				issues: map[int]workflow.Issue{
					42: {Number: 42, Title: "Fix login redirect", OnBoard: true, Status: "Ready"},
					43: {Number: 43, Title: "Speed up checkout", OnBoard: true, Status: "Ready"},
				},
				pulls: map[int]bool{5: true, 60: true},
			})
			code, out := runOut(append([]string{"release", "HEAD", "--since", "abc1234", "--verb", "done"}, tt.args...)...)
			if code != render.ExitOK {
				t.Errorf("exit code = %d, want 0\n%s", code, out)
			}
			testutil.Golden(t, "release/"+tt.name, out)
		})
	}
}

// TestReleaseReverted (#77): #43, fixed by a commit git revert reverted
// later in the range, and #44, closed by pull request #60 that the pull
// request of GitHub's Revert button reverted, are listed as reverted and
// left out, not read; #42 is released. The head, a revert of an earlier
// release's `Fixes #41`, names nothing: its title is a quote. With no
// keywords only #44 is found, reverted: no issues, nothing read.
func TestReleaseReverted(t *testing.T) {
	noKeywords := verbsConfig + "[release]\nkeywords = []\n"
	tests := []struct {
		name, config   string
		args           []string
		reads, applied int
	}{
		{"revert_dry_run", verbsConfig, []string{"--dry-run"}, 1, 0},
		{"revert", verbsConfig, nil, 1, 1},
		{"revert_json", verbsConfig, []string{"--dry-run", "--json"}, 1, 0},
		// done needs no go-ahead: --yes is a warning, once for the batch.
		{"revert_yes", verbsConfig, []string{"--yes"}, 1, 1},
		{"revert_yes_json", verbsConfig, []string{"--yes", "--dry-run", "--json"}, 1, 0},
		{"revert_all", noKeywords, nil, 0, 0},
		{"revert_all_json", noKeywords, []string{"--json"}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, tt.config)
			withApp(t, verbFixture("release_shop_revert"))
			f := &fakeBackend{
				issues: map[int]workflow.Issue{
					41: {Number: 41, Title: "Sign out", OnBoard: true, Status: "Ready"},
					42: {Number: 42, Title: "Fix login redirect", OnBoard: true, Status: "Ready"},
					43: {Number: 43, Title: "Speed up checkout", OnBoard: true, Status: "Ready"},
					44: {Number: 44, Title: "Cache the cart", OnBoard: true, Status: "Ready"},
				},
				pulls: map[int]bool{60: true, 61: true},
			}
			withBackend(t, f)
			code, out := runOut(append([]string{"release", "HEAD", "--since", "abc1234", "--verb", "done"}, tt.args...)...)
			if code != render.ExitOK {
				t.Errorf("exit code = %d, want 0\n%s", code, out)
			}
			if f.reads != tt.reads || len(f.applied) != tt.applied {
				t.Errorf("reads = %d, applied %d plans, want %d and %d", f.reads, len(f.applied), tt.reads, tt.applied)
			}
			testutil.Golden(t, "release/"+tt.name, out)
		})
	}
}

// TestReleaseGuesses: a commit message's numbers that are no issue, #99
// (no such issue) and acme/api#3 (no such repository), are left out with
// a warning, not failed; #44 in a code span does not count. acme/api#7
// and #98, which merged pull request #50 closes (#98 its message names
// too), fail the batch as any issue (exit code 3), until --exclude leaves
// them out; an --exclude the release does not include is a warning. A
// repository of board.repos not found, and any other error, fail the
// batch even for an issue only a message names.
func TestReleaseGuesses(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		code    render.Code
		applied int
		repos   string // board.repos, when set
		denied  bool   // the token is denied acme/api (SAML), not told it is missing
	}{
		// #98, which pull request #50 closes and a message names, is not
		// found: that fails the batch too.
		{"guesses", []string{"--dry-run"}, render.ExitNotFound, 0, "", false},
		{"guesses_exclude", []string{"--exclude", "acme/api#7,98", "--exclude", "#43,77"}, render.ExitOK, 1, "", false},
		{"guesses_exclude_json", []string{"--exclude", "acme/api#7,98", "--dry-run", "--json"}, render.ExitOK, 0, "", false},
		{"guesses_exclude_all", []string{"--exclude", "acme/api#7,42,43,98"}, render.ExitOK, 0, "", false},
		// Nothing left to read.
		{"guesses_exclude_every", []string{"--exclude", "acme/api#7,42,43,98,99,acme/api#3"}, render.ExitOK, 0, "", false},
		{"guesses_exclude_bad", []string{"--exclude", "acme"}, render.ExitUsage, 0, "", false},
		// acme/api is one of the project's repositories: not found, it is
		// the token's access almost surely, and fails the batch.
		{"guesses_own_repo", []string{"--exclude", "acme/api#7,98", "--dry-run"}, render.ExitNotFound, 0,
			`["acme/shop", "acme/api"]`, false},
		// An error other than a missing issue or repository is not left
		// out: a token GitHub denies fails the batch as a network error.
		{"guesses_denied", []string{"--exclude", "acme/api#7,98", "--dry-run"}, render.ExitNetwork, 0, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := verbsConfig
			if tt.repos != "" {
				cfg = strings.Replace(cfg, "project = 3\n", "project = 3\nrepos = "+tt.repos+"\n", 1)
			}
			withConfig(t, cfg)
			if tt.code != render.ExitUsage {
				withApp(t, verbFixture("release_shop_guesses"))
			}
			f := &fakeBackend{
				issues: map[int]workflow.Issue{
					42: {Number: 42, Title: "Fix login redirect", OnBoard: true, Status: "Ready"},
					43: {Number: 43, Title: "Speed up checkout", OnBoard: true, Status: "Done", Closed: true},
				},
				gone: map[string]bool{"acme/api": true},
			}
			if tt.denied {
				f.gone = nil
				f.readErrs = map[string]error{"acme/api": render.Network(
					"GitHub denied access: the organization enforces SAML single sign-on and the token is not authorized for it")}
			}
			withBackend(t, f)
			code, out := runOut(append([]string{"release", "HEAD", "--since", "abc1234", "--verb", "done"}, tt.args...)...)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d\n%s", code, tt.code, out)
			}
			if len(f.applied) != tt.applied {
				t.Errorf("applied %d plans, want %d", len(f.applied), tt.applied)
			}
			testutil.Golden(t, "release/"+tt.name, out)
		})
	}
}

// TestReleaseArgs: mistakes found before any request.
func TestReleaseArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no_ref", []string{"release", "--verb", "done"}},
		{"two_refs", []string{"release", "v0.2.0", "v0.3.0", "--verb", "done"}},
		{"ref_spaces", []string{"release", "v0.2.0 ", "--verb", "done"}},
		{"no_verb", []string{"release", "v0.2.0"}},
		{"no_verb_json", []string{"release", "v0.2.0", "--json"}},
		{"unknown_verb", []string{"release", "v0.2.0", "--verb", "ship"}},
		{"verb_needs_assignee", []string{"release", "v0.2.0", "--verb", "hand"}},
		{"empty_since", []string{"release", "v0.2.0", "--verb", "done", "--since", ""}},
		{"by", []string{"release", "v0.2.0", "--verb", "done", "--by", "12"}},
		{"help", []string{"release", "--help"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// hand needs --assignee, which release does not take.
			withConfig(t, verbsConfig+"[verbs.hand]\nwhen = \"Hand it over\"\nassign = \"{assignee}\"\n")
			code, out := runOut(tt.args...)
			want := render.ExitUsage
			if tt.name == "help" {
				want = render.ExitOK
			}
			if code != want {
				t.Errorf("exit code = %d, want %d\n%s", code, want, out)
			}
			testutil.Golden(t, "release/"+tt.name, out)
		})
	}
}

// TestReleaseNoConfig: release needs the config for its verb.
func TestReleaseNoConfig(t *testing.T) {
	code, out := runOut("release", "v0.2.0", "--verb", "done")
	if code != render.ExitUsage || !strings.Contains(string(out), "dojo.toml") {
		t.Errorf("exit code = %d\n%s", code, out)
	}
}

// TestReleaseSkipPlace: a skipped issue's place in the JSON is its status
// or null and whether it is on the board, both null for the reasons that
// read no place; the text names "no status" and "not on the board".
func TestReleaseSkipPlace(t *testing.T) {
	r := &releaseRun{rng: &release.Range{}, skipped: []releaseSkipped{
		{found: release.Found{Ref: issue.Ref{Number: 1}}, reason: skipNotReady, onBoard: true, status: "Backlog"},
		{found: release.Found{Ref: issue.Ref{Number: 2}}, reason: skipNotReady, onBoard: true},
		{found: release.Found{Ref: issue.Ref{Number: 3}}, reason: skipNotReady},
		{found: release.Found{Ref: issue.Ref{Number: 4}}, reason: skipExcluded},
	}}
	type place struct {
		OnBoard *bool
		Status  *string
	}
	yes, no := true, false
	want := []place{{&yes, render.OrNull("Backlog")}, {&yes, nil}, {&no, nil}, {nil, nil}}
	for i, s := range r.json("done", "", false, nil, nil).Skipped {
		if got := (place{s.OnBoard, s.Status}); !reflect.DeepEqual(got, want[i]) {
			t.Errorf("#%d: on_board, status = %v, want %v", s.Number, got, want[i])
		}
	}
	v := config.Verb{Name: "done", To: "Done", From: []string{"Ready"}}
	want2 := "not ready (done starts only from Ready): #1 (Backlog), #2 (no status), #3 (not on the board)"
	if got := r.notReadyLine(v); got != want2 {
		t.Errorf("notReadyLine = %q, want %q", got, want2)
	}
}
