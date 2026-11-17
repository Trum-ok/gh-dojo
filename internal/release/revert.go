package release

import (
	"regexp"
	"slices"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/issue"
)

// A revert is told by what git and GitHub write, never guessed from a
// title:
//
//   - git revert, and GitHub's Revert button, end the message with "This
//     reverts commit <sha>." (", reversing\nchanges made to <sha>." for a
//     merge commit, whose whole pull request it undoes);
//   - the pull request GitHub's Revert button opens says "Reverts
//     owner/name#20" in its description, which a squash merge may put in
//     the commit's message.
var (
	revertsCommitRE = regexp.MustCompile(`This reverts commit ([0-9a-fA-F]{7,40})\b(,\s+reversing\s+changes\s+made\s+to\b)?`)
	revertsPullRE   = regexp.MustCompile(`(?m)^[ \t]*Reverts[ \t]+(\S+)[ \t\r]*$`)
)

// revertTitleRE is the start of a line that quotes the title of the commit
// it reverts, as git writes it: `Revert "fix"`, `Reapply "fix"` for a
// revert of a revert (older git: `Revert "Revert "fix""`), also in a pull
// request's title, a merge commit's body and a squash merge's list of
// commits ("* Revert …").
var revertTitleRE = regexp.MustCompile(`(?m)^[ \t]*(?:[*-][ \t]+)?(?:Revert|Reapply) "`)

// maskRevertTitles masks the quoted titles of reverts in message, from the
// first quote of the line to its last: a quote is another commit's words,
// so `Revert "Fixes #12"` names no issue, nor does `Revert "fix (#12)"`
// end with a suffix. What follows the quote counts: the "(#21)" of a
// squash merge.
func maskRevertTitles(message string) string {
	locs := revertTitleRE.FindAllStringIndex(message, -1)
	if locs == nil {
		return message
	}
	b := []byte(message)
	for _, loc := range locs {
		open := loc[1] - 1
		end := len(b)
		if i := strings.IndexByte(message[open:], '\n'); i >= 0 {
			end = open + i
		}
		last := strings.LastIndexByte(message[open+1:end], '"')
		if last < 0 {
			continue // no closing quote: not a quote
		}
		for j := open; j <= open+1+last; j++ {
			b[j] = codeByte
		}
	}
	return string(b)
}

// reverts is what a commit's message says it reverts.
type reverts struct {
	commits []string // oids or their prefixes, lower case
	merges  []string // those of commits that are merge commits
	pulls   []issue.Ref
}

func parseReverts(message string, repo issue.Repo) reverts {
	var rv reverts
	for _, m := range revertsCommitRE.FindAllStringSubmatch(message, -1) {
		sha := strings.ToLower(m[1])
		rv.commits = append(rv.commits, sha)
		if m[2] != "" {
			rv.merges = append(rv.merges, sha)
		}
	}
	for _, m := range revertsPullRE.FindAllStringSubmatch(message, -1) {
		if ref, ok := issue.ParseRef(m[1], repo); ok {
			rv.pulls = append(rv.pulls, ref)
		}
	}
	return rv
}

// Reverted reports, for each commit of r, whether a later commit of the
// range reverts it, and that revert is not reverted itself:
//
//   - by its oid ("This reverts commit <sha>"); a merge commit so reverted
//     takes its pull requests with it;
//   - through its pull request ("Reverts #20", or a merge commit of it
//     reverted): every commit of that pull request in the range is
//     reverted.
//
// The commits are read newest first, so a revert of a revert ("Reapply
// …") brings the original back. A revert whose target is not in the range
// (released before) changes nothing here. No request: the messages and
// pull requests are those Read already has.
func Reverted(r *Range) []bool {
	n := len(r.Commits)
	dead := make([]bool, n)
	for i := n - 1; i >= 0; i-- {
		if dead[i] {
			continue // a reverted revert reverts nothing
		}
		rv := parseReverts(r.Commits[i].Message, r.Repo)
		pulls := slices.Clone(rv.pulls)
		for k := range i {
			c := r.Commits[k]
			if !slices.ContainsFunc(rv.commits, func(sha string) bool { return strings.HasPrefix(strings.ToLower(c.OID), sha) }) {
				continue
			}
			dead[k] = true
			if slices.ContainsFunc(rv.merges, func(sha string) bool { return strings.HasPrefix(strings.ToLower(c.OID), sha) }) {
				for _, p := range c.Pulls {
					pulls = append(pulls, p.Ref)
				}
			}
		}
		if len(pulls) == 0 {
			continue
		}
		for k := range i {
			if slices.ContainsFunc(r.Commits[k].Pulls, func(p Pull) bool { return slices.ContainsFunc(pulls, p.Ref.Same) }) {
				dead[k] = true
			}
		}
	}
	return dead
}
