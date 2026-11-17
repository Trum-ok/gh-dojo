// Package release finds the issues a release includes (see "release" in
// docs/commands.md): the commits of the release through the API, without
// a local git checkout, and for each one the merged pull requests it
// belongs to with the issues they close, plus the issues its message
// names after a keyword ([release] keywords) or in a suffix ([release]
// suffix). A commit reverted later in the range counts for nothing (see
// Reverted).
//
// Pull requests link a commit to its issues whatever the merge strategy:
// GitHub associates a squash commit, a merge commit and each rebased
// commit with the pull request they came from. The message is how a
// project that commits without pull requests links its commits.
package release

import (
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/issue"
)

// Found is an issue the release includes and how it was found.
type Found struct {
	Ref     issue.Ref
	Sources []Source // in the order of the commits, each once
}

// Source is how an issue was found: a merged pull request that closes it
// (Pull set, Commit its first commit in the range), or the message of
// Commit.
type Source struct {
	By     By
	Pull   *issue.Ref
	Commit string // the commit's oid
}

// By is what links a commit to an issue.
type By string

const (
	ByPull    By = "pull_request" // a merged pull request of the commit closes it
	ByKeyword By = "keyword"      // the message names it after a keyword
	BySuffix  By = "suffix"       // the message's suffix names it
)

// Collect lists the issues of the commits of r, each once, in the order
// they were found: through the pull requests of a commit first, then its
// message, keywords before the suffix (a commit that names an issue both
// ways is one source, by keyword). A reference by URL on a host other than
// host (gh's) is left out with a warning. A message's #12 may be a pull
// request, or no issue at all: the caller tells once it reads the issues;
// but a suffix's number that is a pull request of the commit itself, the
// "(#PR)" of a squash merge, is left out here, with no warning.
//
// A commit a later one of the range reverts (see Reverted) counts for
// nothing: reverted lists, as found would, the issues only such commits
// name or close, so the release can say why they are left out. An issue a
// commit that stays names is found, with only such commits as sources.
func Collect(r *Range, m *Matcher, host string) (found, reverted []Found, warnings []string) {
	var live, dead collector
	gone := Reverted(r)
	for i, c := range r.Commits {
		add := live.add
		if gone[i] {
			add = dead.add
		}
		for _, p := range c.Pulls {
			pull := p.Ref
			for _, ref := range p.Closes {
				add(ref, Source{By: ByPull, Pull: &pull, Commit: c.OID})
			}
		}
		named := func(refs []issue.Ref, by By) {
			for _, ref := range refs {
				if err := ref.CheckHost(host, ref.String(r.Repo)); err != nil {
					warnings = append(warnings, "commit "+Short(c.OID)+" names an issue on "+ref.Host+", not on "+host+": left out")
					continue
				}
				ref.Host = ""
				if by == BySuffix && slices.ContainsFunc(c.Pulls, func(p Pull) bool { return p.Ref.Same(ref) }) {
					continue // the commit's own pull request
				}
				add(ref, Source{By: by, Commit: c.OID})
			}
		}
		named(m.Refs(c.Message, r.Repo), ByKeyword)
		named(m.Suffix(c.Message, r.Repo), BySuffix)
	}
	for _, f := range dead.found {
		if _, ok := live.index[f.key()]; !ok {
			reverted = append(reverted, f)
		}
	}
	return live.found, reverted, warnings
}

// collector gathers issues, each once, with each way it was found once.
type collector struct {
	found []Found
	index map[string]int // by Found.key
}

func (f Found) key() string {
	return strings.ToLower(f.Ref.Repo.String()) + "#" + strconv.Itoa(f.Ref.Number)
}

func (c *collector) add(ref issue.Ref, s Source) {
	if c.index == nil {
		c.index = map[string]int{}
	}
	key := Found{Ref: ref}.key()
	i, ok := c.index[key]
	if !ok {
		i = len(c.found)
		c.index[key] = i
		c.found = append(c.found, Found{Ref: ref})
	}
	f := &c.found[i]
	for _, o := range f.Sources {
		if s.Pull != nil && o.Pull != nil && s.Pull.Same(*o.Pull) || s.Pull == nil && o.Pull == nil && o.Commit == s.Commit {
			return
		}
	}
	f.Sources = append(f.Sources, s)
}
