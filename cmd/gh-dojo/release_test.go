package main

import (
	"strings"
	"testing"

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
// runs done on them as gh dojo done 3 7 9 17 does (TestBatchRefused): #9
// is in place, #3 and #7 are written. #17, in Backlog, is not ready for
// done: listed, not failed (exit 0). The dry run lists how each issue
// was found, then the plans.
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
// cli/cli#2 is not on the board, so not ready for done.
func TestReleaseOtherRepo(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("release_repos"), verbFixture("batch_done_repos"))
	withConfig(t, verbDojoConfig+releaseKeywords)
	args := []string{"release", "v0.2.0", "--since", "v0.1.0", "--verb", "done", "--dry-run"}
	checkRuns(t, srv, before,
		runs{args, render.ExitOK, 3, "release_other_repo"},
		runs{append(args, "--json"), render.ExitOK, 3, "release_other_repo_json"},
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
	}{
		{"fake_dry_run", verbsConfig, []string{"--dry-run"}, render.ExitOK, 0},
		{"fake", verbsConfig, nil, render.ExitOK, 1},
		{"fake_json", verbsConfig, []string{"--json"}, render.ExitOK, 1},
		{"fake_comment", verbsConfig, []string{"--comment", "Released in v1.0.0."}, render.ExitOK, 2},
		// No keywords: only pull requests count, and there are none.
		{"fake_no_keywords", verbsConfig + "[release]\nkeywords = []\n", nil, render.ExitOK, 0},
		{"fake_no_keywords_json", verbsConfig + "[release]\nkeywords = []\n", []string{"--json"}, render.ExitOK, 0},
		// A refusal other than from still fails the batch: the user's
		// go-ahead. #43 is in place and needs none.
		{"fake_confirm", verbsConfig + "[verbs.done]\nconfirm = \"user\"\n", nil, render.ExitDenied, 0},
		// #42 in Ready is not ready for a done from Waiting: listed, exit 0.
		{"fake_not_ready", verbsConfig + "[verbs.done]\nfrom = [\"Waiting\"]\n", nil, render.ExitOK, 0},
		{"fake_not_ready_json", verbsConfig + "[verbs.done]\nfrom = [\"Waiting\"]\n", []string{"--json"}, render.ExitOK, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, tt.config)
			withApp(t, verbFixture("release_shop"))
			f := shop()
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
		{"not_batch", []string{"release", "v0.2.0", "--verb", "start"}},
		{"empty_since", []string{"release", "v0.2.0", "--verb", "done", "--since", ""}},
		{"yes", []string{"release", "v0.2.0", "--verb", "done", "--yes"}},
		{"by", []string{"release", "v0.2.0", "--verb", "done", "--by", "12"}},
		{"help", []string{"release", "--help"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, verbsConfig)
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
