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
	m := NewMatcher([]string{"closes", "fixes", "refs", "part of"})
	tests := []struct {
		msg  string
		want string
	}{
		{"Add release\n\nRefs #41", "#41"},
		{"fixes #3, #4 and acme/api#5; see #6", "#3 #4 acme/api#5"},
		{"Fixes: #7\nCloses #7\nREFS #8", "#7 #8"},
		{"Part of https://github.com/acme/api/issues/9", "acme/api#9"},
		{"Part\nof #10", "#10"},
		{"Prefs #11, fixesx #12, refs#13, refs 14", ""},         // whole words, then a reference
		{"Refs https://github.com/Trum-ok/gh-dojo/pull/15", ""}, // a pull request's URL
		{"Merge pull request #16 from x/y", ""},
		{"Fixes #17.\nAlso refs #18 #19", "#17 #18 #19"},
		{"Closes Trum-ok/gh-dojo#20", "#20"},
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
	if got := NewMatcher(nil).Refs("Fixes #1", dojo); got != nil {
		t.Errorf("no keywords: %v", got)
	}
	if got := NewMatcher([]string{"closes"}).Refs("Fixes #1", dojo); got != nil {
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
	found, warnings := Collect(r, NewMatcher([]string{"fixes", "refs"}), "github.com")
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
	found, warnings := Collect(r, NewMatcher(keywords), "github.com")
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
		{"too_many", "v0.2.0", "v0.0.1", render.ExitUsage, 2},
		{"not_ancestor", "v0.2.0", "abc1234", render.ExitUsage, 2},
		{"repo_missing", "v0.2.0", "main", render.ExitNotFound, 1},
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
