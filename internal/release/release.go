// Package release finds the issues a release includes (spec F11): the
// commits of the release through the API, without a local git checkout,
// and for each one the merged pull requests it belongs to with the issues
// they close, plus the issues its message names after a keyword
// ([release] keywords).
//
// Pull requests link a commit to its issues whatever the merge strategy:
// GitHub associates a squash commit, a merge commit and each rebased
// commit with the pull request they came from. The message is how a
// project that commits without pull requests links its commits.
package release

import (
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
	Pull   *issue.Ref
	Commit string // the commit's oid
}

// Collect lists the issues of the commits of r, each once, in the order
// they were found: through the pull requests of a commit first, then its
// message. A reference by URL on a host other than host (gh's) is left out
// with a warning. A message's #12 may be a pull request: the caller tells
// once it reads the issues.
func Collect(r *Range, m *Matcher, host string) (found []Found, warnings []string) {
	index := map[string]int{}
	add := func(ref issue.Ref, s Source) {
		key := strings.ToLower(ref.Repo.String()) + "#" + strconv.Itoa(ref.Number)
		i, ok := index[key]
		if !ok {
			i = len(found)
			index[key] = i
			found = append(found, Found{Ref: ref})
		}
		f := &found[i]
		for _, o := range f.Sources {
			if s.Pull != nil && o.Pull != nil && s.Pull.Same(*o.Pull) || s.Pull == nil && o.Pull == nil && o.Commit == s.Commit {
				return
			}
		}
		f.Sources = append(f.Sources, s)
	}
	for _, c := range r.Commits {
		for _, p := range c.Pulls {
			pull := p.Ref
			for _, ref := range p.Closes {
				add(ref, Source{Pull: &pull, Commit: c.OID})
			}
		}
		for _, ref := range m.Refs(c.Message, r.Repo) {
			if err := ref.CheckHost(host, ref.String(r.Repo)); err != nil {
				warnings = append(warnings, "commit "+Short(c.OID)+" names an issue on "+ref.Host+", not on "+host+": left out")
				continue
			}
			ref.Host = ""
			add(ref, Source{Commit: c.OID})
		}
	}
	return found, warnings
}
