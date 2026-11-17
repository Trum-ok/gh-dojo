package release

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

var (
	cli  = issue.Repo{Owner: "cli", Name: "cli"}
	dojo = issue.Repo{Owner: "Trum-ok", Name: "gh-dojo"}
)

func TestParseVersion(t *testing.T) {
	for _, s := range []string{"v1.2.3", "1.2.3", "v0.1.0-rc.1", "refs/tags/v2.0.0", "v1.0.0+build.5", "v1.0.0-alpha-1.x"} {
		if _, ok := ParseVersion(s); !ok {
			t.Errorf("ParseVersion(%q) failed", s)
		}
	}
	for _, s := range []string{"", "v1.2", "1.2.3.4", "v01.2.3", "release-1.2.3", "v1.2.3-", "nightly", "pkg/v1.2.3"} {
		if v, ok := ParseVersion(s); ok {
			t.Errorf("ParseVersion(%q) = %+v, want no version", s, v)
		}
	}
	// semver.org's precedence example, lowest first.
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "v2.0.0", "10.0.0"}
	for i := 1; i < len(order); i++ {
		a, _ := ParseVersion(order[i-1])
		b, _ := ParseVersion(order[i])
		if a.Compare(b) >= 0 || b.Compare(a) <= 0 {
			t.Errorf("%s should be below %s", order[i-1], order[i])
		}
	}
	a, _ := ParseVersion("v1.0.0+one")
	b, _ := ParseVersion("1.0.0")
	if a.Compare(b) != 0 {
		t.Error("build metadata should not count")
	}
}

func TestPrevious(t *testing.T) {
	tags := []Tag{
		{"v0.3.0-rc.1", "c7"}, {"v0.2.0", "c6"}, {"v0.2.0-rc.2", "c5"}, {"v0.2.0-rc.1", "c4"},
		{"nightly", "c9"}, {"v0.1.1", "c3"}, {"v0.1.0", "c2"}, {"v1.0.0-tree", ""}, {"v0.0.1", "c1"},
	}
	tests := []struct {
		head, commit, want string // want "" for none
	}{
		{"v0.2.0", "c6", "v0.1.1"},      // a release: the previous release, not its candidates
		{"v0.3.0", "c8", "v0.2.0"},      // a tag not read yet (pushed later): still by version
		{"v0.3.0-rc.1", "c7", "v0.2.0"}, // a prerelease: prereleases count too
		{"v0.2.0-rc.2", "c5", "v0.2.0-rc.1"},
		{"v0.2.0-rc.1", "c4", "v0.1.1"},
		{"HEAD", "c8", "v0.2.0"},    // not a version: the highest release
		{"main", "c6", "v0.1.1"},    // ... other than the one at the same commit
		{"v0.0.1", "c1", ""},        // the first release
		{"v1.0.0", "c10", "v0.2.0"}, // v1.0.0-tree points at no commit
		{"refs/tags/v0.1.1", "c3", "v0.1.0"},
	}
	for _, tt := range tests {
		got, ok := Previous(tags, tt.head, tt.commit)
		if !ok {
			got.Name = ""
		}
		if got.Name != tt.want {
			t.Errorf("Previous(%s) = %q, want %q", tt.head, got.Name, tt.want)
		}
	}
}

func TestMatcher(t *testing.T) {
	m := NewMatcher([]string{"closes", "fixes", "refs", "part of"}, false)
	tests := []struct {
		msg  string
		want string
	}{
		{"Add release\n\nRefs #41", "#41"},
		{"fixes #3, #4 and acme/api#5; see #6", "#3 #4 acme/api#5"},
		{"Fixes: #7\nCloses #7\nREFS #8", "#7 #8"},
		{"Part of https://github.com/acme/api/issues/9", "acme/api#9"},
		{"Part\nof #10", ""},                         // a keyword on one line
		{"Refs #21\n#22 is next\nFixes\n#23", "#21"}, // a list ends with its line
		{"Refs #24,\n#25", "#24"},
		{"Fixes\t#26 and\t#27", "#26 #27"},
		{"Prefs #11, fixesx #12, refs#13, refs 14", ""},         // whole words, then a reference
		{"Refs https://github.com/Trum-ok/gh-dojo/pull/15", ""}, // a pull request's URL
		{"Merge pull request #16 from x/y", ""},
		{"Fixes #17.\nAlso refs #18 #19", "#17 #18 #19"},
		{"Closes Trum-ok/gh-dojo#20", "#20"},
		// Code is an example, not a link: a span, a fenced block.
		{"Parse `refs: #12, #13 and acme/api#3`\n\nRefs #51", "#51"},
		{"Refs `#12`", ""},
		{"Refs #30, `#31`", "#30"},
		{"Fixes ``a ` refs #32``, fixes #33", "#33"},
		{"Refs `#34\nstill code` refs #35", "#35"},                // a span may wrap a line
		{"Refs #36 `x\n\nRefs #37` and refs #38", "#36 #37 #38"},  // not past a paragraph: text
		{"Example:\n```\nFixes #40\n```\nFixes #41", "#41"},       // a fenced block
		{"```sh\nrefs #42\n~~~\nrefs #43\n````\nrefs #44", "#44"}, // closed by ``` or longer only
		{"~~~\nFixes #45\n~~~\nFixes #46", "#46"},
		{"    ```\nFixes #47\n```", "#47"}, // four spaces: not a fence
		{"```\nFixes #48", ""},             // a block open to the end
		{"Fixes #49 ``` and ```", "#49"},   // a span of three backticks, not a fence
		{"A lone ` refs #50", "#50"},       // a backtick nothing closes is text
		// Code ends a list as the backticks did; it is no separator.
		{"Refs `#12` #13", ""},
		{"Fixes `foo` #13", ""},
		{"Refs #30 `x` #31", "#30"},
		// An indented block, after a blank line, is code; within a
		// paragraph an indented line is text.
		{"Example:\n\n    Fixes #52\n\tRefs #53\n\n    refs #54\nFixes #55", "#55"},
		{"Refs #56,\n    #57\nwrapped:\n    fixes #58", "#56 #58"},
		{"    Fixes #59", ""}, // the first line, after nothing
		// The quoted title of a revert is the reverted commit's words (#77);
		// the revert's own words count.
		{`Revert "Fixes #60"`, ""},
		{"Revert \"Fixes #61\"\n\nThis reverts commit 1234567.\n\nRefs #62", "#62"},
		{`Revert "Revert "Fixes #63""`, ""},
		{`Reapply "Fixes #64"`, ""},
		{"Merge pull request #21 from acme/revert-20\n\nRevert \"Fixes #65\"", ""},     // a merge commit's body
		{"Revert (#22)\n\n* Revert \"Fixes #66\"\n\nThis reverts commit 1234567.", ""}, // a squash merge's list
		{`Revert "Fixes #67" and fixes #68`, "#68"},
		{`Revert "Fixes #69`, "#69"},         // no closing quote: no quote
		{`Do not revert "Fixes #70"`, "#70"}, // not a revert's title
	}
	for _, tt := range tests {
		var got []string
		for _, r := range m.Refs(tt.msg, dojo) {
			got = append(got, r.String(dojo))
		}
		if s := strings.Join(got, " "); s != tt.want {
			t.Errorf("Refs(%q) = %q, want %q", tt.msg, s, tt.want)
		}
	}
	if got := NewMatcher(nil, false).Refs("Fixes #1", dojo); got != nil {
		t.Errorf("no keywords: %v", got)
	}
	if got := NewMatcher([]string{"closes"}, false).Refs("Fixes #1", dojo); got != nil {
		t.Errorf("only closes: %v", got)
	}
}

// TestCollect: an issue once, with each way it was found once, in commit
// order; a URL on another host is left out.
func TestCollect(t *testing.T) {
	pull := func(n int, closes ...issue.Ref) Pull {
		return Pull{Ref: issue.Ref{Repo: dojo, Number: n}, Closes: closes}
	}
	ref := func(repo issue.Repo, n int) issue.Ref { return issue.Ref{Repo: repo, Number: n} }
	r := &Range{Repo: dojo, Commits: []Commit{
		{OID: "aaaaaaa1", Message: "Squash (#50)\n\nRefs #3", Pulls: []Pull{pull(50, ref(dojo, 3), ref(cli, 2))}},
		{OID: "bbbbbbb2", Message: "Rebased 1", Pulls: []Pull{pull(51, ref(dojo, 7))}},
		{OID: "ccccccc3", Message: "Rebased 2\n\nRefs #7", Pulls: []Pull{pull(51, ref(dojo, 7))}},
		{OID: "ddddddd4", Message: "Direct\n\nFixes #9, https://ghe.example.com/a/b/issues/1"},
		{OID: "eeeeeee5", Message: "No issue"},
	}}
	found, _, warnings := Collect(r, NewMatcher([]string{"fixes", "refs"}, false), "github.com")
	var b strings.Builder
	for _, f := range found {
		fmt.Fprintf(&b, "%s:", f.Ref.String(dojo))
		for _, s := range f.Sources {
			if s.Pull != nil {
				fmt.Fprintf(&b, " PR %s@%s", s.Pull.String(dojo), Short(s.Commit))
			} else {
				fmt.Fprintf(&b, " commit %s", Short(s.Commit))
			}
		}
		b.WriteString("\n")
	}
	want := "#3: PR #50@aaaaaaa commit aaaaaaa\ncli/cli#2: PR #50@aaaaaaa\n#7: PR #51@bbbbbbb commit ccccccc\n#9: commit ddddddd\n"
	if b.String() != want {
		t.Errorf("found:\n%s\nwant:\n%s", b.String(), want)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ghe.example.com") {
		t.Errorf("warnings = %q", warnings)
	}
}

func TestSuffix(t *testing.T) {
	m := NewMatcher(nil, true)
	tests := []struct {
		msg  string
		want string
	}{
		{"fix: login limit (#161)", "#161"},
		{"fix: login limit (#164, #181)", "#164 #181"},
		{"fix (#164,#181)", "#164 #181"},
		{"fix ( #12 , acme/api#3 )  \t", "#12 acme/api#3"}, // spaces inside and after
		{"fix (https://github.com/acme/api/issues/9)", "acme/api#9"},
		{"fix (#161) (#200)", "#161 #200"},  // a title's issue, then a squash merge's PR
		{"fix (#161)(#200)", "#161 #200"},   // groups need no space between
		{"fix (#12) (#13, #12)", "#12 #13"}, // each once
		{"(#5)", "#5"},                      // the whole line
		{"fix (#12)\r\n\nbody", "#12"},
		// Not a suffix.
		{"fix (#12).", ""},
		{"fix (see #12)", ""},
		{"fix (#12, wip)", ""},
		{"fix (#12 #13)", ""}, // commas only
		{"fix (#12 and #13)", ""},
		{"fix (12)", ""},
		{"fix ()", ""},
		{"fix f(#12)", ""},                  // a call
		{"(#12) fix", ""},                   // the middle of the line
		{"fix\n\nbody (#12)", ""},           // the body
		{`Revert "fix (#12)"`, ""},          // a revert ends with a quote
		{`Revert "fix (#12)" (#21)`, "#21"}, // ... the squash merge's "(#PR)" after it counts
		{`Revert "fix (#12) (#13)"`, ""},
		{"fix (#12) wip (#13)", "#13"}, // groups read up to one that is not
		{"fix (#12) (wip) (#13)", "#13"},
		{"fix `(#12)`", ""}, // code
		{"fix (`#12`)", ""},
		{"fix (acme/api/pull/3)", ""}, // not a reference
		{"fix (https://github.com/acme/api/pull/3)", ""},
	}
	for _, tt := range tests {
		var got []string
		for _, r := range m.Suffix(tt.msg, dojo) {
			got = append(got, r.String(dojo))
		}
		if s := strings.Join(got, " "); s != tt.want {
			t.Errorf("Suffix(%q) = %q, want %q", tt.msg, s, tt.want)
		}
	}
	if got := NewMatcher([]string{"refs"}, false).Suffix("fix (#1)", dojo); got != nil {
		t.Errorf("suffix off: %v", got)
	}
}

// TestCollectSuffix: a suffix links a commit as a keyword does; the
// commit's own pull request is left out with no warning, another one is
// the caller's to tell; a keyword and the suffix of one commit are one
// source, by keyword.
func TestCollectSuffix(t *testing.T) {
	squash := Pull{Ref: issue.Ref{Repo: dojo, Number: 200}}
	r := &Range{Repo: dojo, Commits: []Commit{
		{OID: "aaaaaaa1", Message: "fix: login limit (#161) (#200)", Pulls: []Pull{squash}},
		{OID: "bbbbbbb2", Message: "fix: retry (#164, #181)\n\nRefs #164"},
		{OID: "ccccccc3", Message: "chore (#150)"}, // a pull request of another commit, or no issue: read later
		{OID: "ddddddd4", Message: "docs (https://ghe.example.com/a/b/issues/1)"},
	}}
	found, _, warnings := Collect(r, NewMatcher([]string{"refs"}, true), "github.com")
	var b strings.Builder
	for _, f := range found {
		fmt.Fprintf(&b, "%s:", f.Ref.String(dojo))
		for _, s := range f.Sources {
			fmt.Fprintf(&b, " %s %s", s.By, Short(s.Commit))
		}
		b.WriteString("\n")
	}
	want := "#161: suffix aaaaaaa\n#164: keyword bbbbbbb\n#181: suffix bbbbbbb\n#150: suffix ccccccc\n"
	if b.String() != want {
		t.Errorf("found:\n%s\nwant:\n%s", b.String(), want)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ghe.example.com") {
		t.Errorf("warnings = %q", warnings)
	}
}

// TestCollectReverted: a commit a later one of the range reverts counts
// for nothing (#77), whether the revert names it by oid (git revert), its
// merge commit ("reversing changes made to") or its pull request ("Reverts
// #20", GitHub's Revert button); a revert of a revert brings it back. Its
// issues are listed as reverted, unless a commit that stays names them.
func TestCollectReverted(t *testing.T) {
	pull := func(n int, closes ...int) Pull {
		p := Pull{Ref: issue.Ref{Repo: dojo, Number: n}}
		for _, c := range closes {
			p.Closes = append(p.Closes, issue.Ref{Repo: dojo, Number: c})
		}
		return p
	}
	oid := func(s string) string { return strings.Repeat(s, 20) } // 40 hex digits
	reverts := func(s string) string { return "\n\nThis reverts commit " + oid(s) + "." }
	r := &Range{Repo: dojo, Commits: []Commit{
		// A commit and its revert.
		{OID: oid("a1"), Message: "Fix login\n\nFixes #1"},
		{OID: oid("a2"), Message: `Revert "Fix login"` + reverts("a1")},
		// A commit, its revert and the revert's revert.
		{OID: oid("b1"), Message: "Fixes #2"},
		{OID: oid("b2"), Message: `Revert "Fixes #2"` + reverts("b1")},
		{OID: oid("b3"), Message: `Reapply "Fixes #2"` + reverts("b2")},
		// A squash merge, and that of the pull request GitHub's Revert
		// button opens.
		{OID: oid("c1"), Message: "Fix cart (#20)", Pulls: []Pull{pull(20, 3)}},
		{OID: oid("c2"), Message: "Revert \"Fix cart\" (#21)\n\nReverts Trum-ok/gh-dojo#20", Pulls: []Pull{pull(21)}},
		// A merge commit reverted: its branch's commits too.
		{OID: oid("d1"), Message: "Refs #4", Pulls: []Pull{pull(30, 4)}},
		{OID: oid("d2"), Message: "Merge pull request #30 from acme/cart", Pulls: []Pull{pull(30, 4)}},
		{OID: oid("d3"), Message: `Revert "Merge pull request #30 from acme/cart"` + "\n\nThis reverts commit " + oid("d2") +
			", reversing\nchanges made to " + oid("00") + "."},
		// One of a rebased pull request's commits reverted: the pull
		// request stays, through the other.
		{OID: oid("e1"), Message: "Rebased 1", Pulls: []Pull{pull(40, 5)}},
		{OID: oid("e2"), Message: "Rebased 2", Pulls: []Pull{pull(40, 5)}},
		{OID: oid("e3"), Message: `Revert "Rebased 1"` + reverts("e1")},
		// Another commit names the issue: it stays, found through that one.
		{OID: oid("f1"), Message: "Fixes #6"},
		{OID: oid("f2"), Message: "Refs #6"},
		{OID: oid("f3"), Message: `Revert "Fixes #6"` + reverts("f1")},
		// A revert of a commit of an earlier release, by a short oid.
		{OID: oid("11"), Message: "Revert \"Fixes #7\"\n\nThis reverts commit 9999999."},
		// A revert pull request reverted: the pull request it reverted
		// stays.
		{OID: oid("21"), Message: "Fix search (#50)", Pulls: []Pull{pull(50, 8)}},
		{OID: oid("22"), Message: "Revert \"Fix search\" (#51)\n\nReverts #50", Pulls: []Pull{pull(51)}},
		{OID: oid("23"), Message: "Revert \"Revert \"Fix search\"\" (#52)\n\nReverts Trum-ok/gh-dojo#51", Pulls: []Pull{pull(52)}},
	}}
	found, reverted, warnings := Collect(r, NewMatcher([]string{"fixes", "refs"}, false), "github.com")
	dump := func(fs []Found) string {
		var b strings.Builder
		for _, f := range fs {
			fmt.Fprintf(&b, "%s:", f.Ref.String(dojo))
			for _, s := range f.Sources {
				if s.Pull != nil {
					fmt.Fprintf(&b, " PR %s@%s", s.Pull.String(dojo), Short(s.Commit))
				} else {
					fmt.Fprintf(&b, " commit %s", Short(s.Commit))
				}
			}
			b.WriteString("\n")
		}
		return b.String()
	}
	if got, want := dump(found), "#2: commit b1b1b1b\n#5: PR #40@e2e2e2e\n#6: commit f2f2f2f\n#8: PR #50@2121212\n"; got != want {
		t.Errorf("found:\n%s\nwant:\n%s", got, want)
	}
	if got, want := dump(reverted), "#1: commit a1a1a1a\n#3: PR #20@c1c1c1c\n#4: PR #30@d1d1d1d commit d1d1d1d\n"; got != want {
		t.Errorf("reverted:\n%s\nwant:\n%s", got, want)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q", warnings)
	}
}

// dumpRange prints r and what Collect finds in it, for golden files.
func dumpRange(r *Range, keywords []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "repo %s, head %s (%s), since %s (%s), picked %v, compared %v, %d commits\n",
		r.Repo, r.Head, Short(r.HeadCommit), r.Since, Short(r.SinceCommit), r.Picked, r.Compared, len(r.Commits))
	for _, c := range r.Commits {
		head, _, _ := strings.Cut(c.Message, "\n")
		fmt.Fprintf(&b, "  %s %q", Short(c.OID), head)
		for _, p := range c.Pulls {
			var closes []string
			for _, ref := range p.Closes {
				closes = append(closes, ref.String(r.Repo))
			}
			fmt.Fprintf(&b, " PR %s closes [%s]", p.Ref.String(r.Repo), strings.Join(closes, " "))
		}
		b.WriteString("\n")
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	found, _, warnings := Collect(r, NewMatcher(keywords, false), "github.com")
	for _, f := range found {
		var src []string
		for _, s := range f.Sources {
			if s.Pull != nil {
				src = append(src, "PR "+s.Pull.String(r.Repo))
			} else {
				src = append(src, "commit "+Short(s.Commit))
			}
		}
		fmt.Fprintf(&b, "found %s: %s\n", f.Ref.String(r.Repo), strings.Join(src, ", "))
	}
	for _, w := range warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	return []byte(b.String())
}

// TestReadCompare: cli/cli's v2.39.2 since v2.39.1, recorded: squash
// merges (#8229 closes #8100, #8359 closes #8336), a merge commit whose
// branch commits belong to #8384 too (closes #8358), and a pull request
// that closes nothing. GitHub compares the tags: two requests.
func TestReadCompare(t *testing.T) {
	srv := graphtest.New(t, "testdata/graphql/compare_cli.json")
	r, err := Read(context.Background(), srv.Client(), cli, "v2.39.2", "v2.39.1")
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "read/compare_cli", dumpRange(r, config.DefaultKeywords()))
	if n := srv.Requests(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

// TestReadHistory: gh-dojo's first commits, recorded: --since a commit
// reads the history of the head down to it, oldest first.
func TestReadHistory(t *testing.T) {
	srv := graphtest.New(t, "testdata/graphql/history_dojo.json")
	r, err := Read(context.Background(), srv.Client(), dojo, "ef93eeb", "4c40894")
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "read/history_dojo", dumpRange(r, append(config.DefaultKeywords(), "refs")))
	if n := srv.Requests(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

// TestReadTags: without --since the previous version tag is picked from
// the tags (hand-crafted: gh-dojo has none yet), then compared. The
// second page of commits is read; a commit's pull request list and a pull
// request's closing list are cut, with warnings.
func TestReadTags(t *testing.T) {
	srv := graphtest.New(t, "testdata/graphql/tags.json")
	r, err := Read(context.Background(), srv.Client(), dojo, "v0.2.0", "")
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "read/tags", dumpRange(r, []string{"fixes", "refs"}))
	if n := srv.Requests(); n != 4 {
		t.Errorf("requests = %d, want 4 (2 pages of tags, 2 of commits)", n)
	}
}

func TestReadErrors(t *testing.T) {
	tests := []struct {
		name, head, since string
		code              render.Code
		requests          int
	}{
		{"no_previous", "v0.0.1", "", render.ExitUsage, 1},
		{"head_missing", "v9.9.9", "", render.ExitNotFound, 1},
		{"since_missing", "v0.2.0", "v0.0.9", render.ExitNotFound, 1},
		// A remote-tracking name of a local checkout (#68): the fix names
		// the ref GitHub knows, at the cost of one more request.
		{"remote_head", "origin/main", "", render.ExitNotFound, 2},
		{"remote_since", "v0.2.0", "origin/v0.1.0", render.ExitNotFound, 2},
		{"slash_missing", "feature/gone", "", render.ExitNotFound, 2},
		{"too_many", "v0.2.0", "v0.0.1", render.ExitUsage, 2},
		{"not_ancestor", "v0.2.0", "abc1234", render.ExitUsage, 2},
		{"repo_missing", "v0.2.0", "main", render.ExitNotFound, 1},
		// A start newer than the head: by the commits' dates, before any
		// history is read; by GitHub's comparison for a ref.
		{"since_newer", "v0.2.0", "abc1234", render.ExitUsage, 1},
		{"since_behind", "v0.2.0", "main", render.ExitUsage, 2},
		// The highest version tag of another branch: not "no issues".
		{"picked_diverged", "HEAD", "", render.ExitUsage, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := graphtest.New(t, "testdata/graphql/errors_"+tt.name+".json")
			_, err := Read(context.Background(), srv.Client(), dojo, tt.head, tt.since)
			var e *render.Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v, want a render error", err)
			}
			if e.Code != tt.code {
				t.Errorf("code = %d, want %d: %v", e.Code, tt.code, err)
			}
			testutil.Golden(t, "errors/"+tt.name, []byte(e.Text()+"\n"))
			if n := srv.Requests(); n != tt.requests {
				t.Errorf("requests = %d, want %d", n, tt.requests)
			}
		})
	}
}
