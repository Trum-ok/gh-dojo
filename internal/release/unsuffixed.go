package release

import (
	"slices"

	"github.com/Trum-ok/gh-dojo/internal/issue"
)

// Unsuffixed is an issue that commits of the range name only in a suffix,
// "fix: login limit (#12)", while [release] suffix is off, so the release
// leaves it out.
type Unsuffixed struct {
	Ref     issue.Ref
	Commits []string // the oids of the commits whose suffix names it, in order
}

// FindUnsuffixed lists the issues the suffixes of the commits of r name
// that the release leaves out because m does not read suffixes ([release]
// suffix is off), each once: the release says so rather than miss them
// silently. Nothing when m reads suffixes. Left out, as by Collect: the
// commits a later one reverts, a suffix's number that is a pull request of
// its commit (a squash merge's "(#PR)"), a URL on a host other than host,
// and the issues found, the release's anyway.
func FindUnsuffixed(r *Range, m *Matcher, found []Found, host string) []Unsuffixed {
	if m == nil || m.suffix {
		return nil
	}
	gone := Reverted(r)
	var out []Unsuffixed
	for i, c := range r.Commits {
		if gone[i] {
			continue
		}
		for _, ref := range suffix(c.Message, r.Repo) {
			if ref.CheckHost(host, "") != nil {
				continue
			}
			ref.Host = ""
			same := func(o issue.Ref) bool { return o.Same(ref) }
			if slices.ContainsFunc(c.Pulls, func(p Pull) bool { return same(p.Ref) }) ||
				slices.ContainsFunc(found, func(f Found) bool { return same(f.Ref) }) {
				continue
			}
			j := slices.IndexFunc(out, func(u Unsuffixed) bool { return same(u.Ref) })
			if j < 0 {
				j = len(out)
				out = append(out, Unsuffixed{Ref: ref})
			}
			out[j].Commits = append(out[j].Commits, c.OID) // suffix names each issue once
		}
	}
	return out
}
