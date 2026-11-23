package release

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Limits of one read of a release.
const (
	// MaxCommits is the most commits a release may have: 10 requests. A
	// longer range is refused before its commits are read; a later
	// --since narrows it.
	MaxCommits = 1000
	// MaxTags is how many of the newest tags are read (by their commit's
	// date) to find the previous release: 10 requests at most.
	MaxTags = 1000
	// PageSize is how many commits one request reads, each with its pull
	// requests and their closing issues.
	PageSize = 100
	// PullWindow is how many pull requests are read per commit, and
	// ClosingWindow how many closing issues per pull request; more are
	// left out with a warning.
	PullWindow    = 5
	ClosingWindow = 25
)

// Range is the commits of a release: those of Head that Since does not
// have.
type Range struct {
	Repo issue.Repo // as given
	Head string     // the <ref> of the release, as given
	// HeadCommit is the oid of Head's commit.
	HeadCommit string
	// Since is the start of the range as named: the previous release's tag
	// (Picked) or --since.
	Since  string
	Picked bool
	// SinceCommit is the oid of Since's commit.
	SinceCommit string
	// Compared says GitHub compared Head with Since, a tag or a branch:
	// the range is exact. Otherwise Since is a commit, and the range is
	// Head's history down to it (see Read).
	Compared bool
	// Commits are oldest first.
	Commits  []Commit
	Warnings []string
}

// Commit is a commit of the range with what links it to issues.
type Commit struct {
	OID     string
	Message string
	// Pulls are the merged pull requests of the release's repository that
	// the commit belongs to: its own (rebase), the one it squashes, the
	// one it merges.
	Pulls []Pull
}

// Pull is a merged pull request and the issues it closes: those its
// description names with a closing keyword and those linked to it on
// GitHub (closingIssuesReferences).
type Pull struct {
	Ref    issue.Ref
	Closes []issue.Ref
	// MergeCommit is the oid of the commit that merged the pull request;
	// "" when GitHub does not say. A commit belongs to every pull request
	// that merged a branch containing it, so Commit.Pulls tells which
	// pull requests a commit is in, MergeCommit which one it merged.
	MergeCommit string
}

// commitFragment is what a release reads of each commit.
var commitFragment = `fragment releaseCommit on Commit {
  oid
  message
  associatedPullRequests(first: ` + strconv.Itoa(PullWindow) + `) {
    totalCount
    nodes {
      number
      merged
      repository { nameWithOwner }
      mergeCommit { oid }
      closingIssuesReferences(first: ` + strconv.Itoa(ClosingWindow) + `) {
        totalCount
        nodes { number repository { nameWithOwner } }
      }
    }
  }
}`

type commitNode struct {
	OID     string `json:"oid"`
	Message string `json:"message"`
	Pulls   struct {
		TotalCount int         `json:"totalCount"`
		Nodes      []*pullNode `json:"nodes"`
	} `json:"associatedPullRequests"`
}

type pullNode struct {
	Number      int      `json:"number"`
	Merged      bool     `json:"merged"`
	Repository  repoNode `json:"repository"`
	MergeCommit *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	Closing struct {
		TotalCount int          `json:"totalCount"`
		Nodes      []*issueNode `json:"nodes"`
	} `json:"closingIssuesReferences"`
}

type issueNode struct {
	Number     int      `json:"number"`
	Repository repoNode `json:"repository"`
}

type repoNode struct {
	NameWithOwner string `json:"nameWithOwner"`
}

const tagsQuery = `query ReleaseTags($owner: String!, $repo: String!, $head: String!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    head: object(expression: $head) { oid }
    refs(refPrefix: "refs/tags/", first: 100, after: $cursor, orderBy: {field: TAG_COMMIT_DATE, direction: DESC}) {
      totalCount
      pageInfo { hasNextPage endCursor }
      nodes {
        name
        target { __typename oid ... on Tag { target { __typename oid } } }
      }
    }
  }
}`

const sinceQuery = `query ReleaseSince($owner: String!, $repo: String!, $head: String!, $since: String!, $sinceRef: String!) {
  repository(owner: $owner, name: $repo) {
    head: object(expression: $head) { oid ... on Commit { committedDate } }
    since: object(expression: $since) { oid ... on Commit { committedDate } }
    sinceRef: ref(qualifiedName: $sinceRef) { prefix name }
  }
}`

var compareQuery = `query ReleaseCompare($owner: String!, $repo: String!, $base: String!, $head: String!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    ref(qualifiedName: $base) {
      compare(headRef: $head) {
        status
        aheadBy
        behindBy
        commits(first: ` + strconv.Itoa(PageSize) + `, after: $cursor) {
          totalCount
          pageInfo { hasNextPage endCursor }
          nodes { ...releaseCommit }
        }
      }
    }
  }
}

` + commitFragment

var historyQuery = `query ReleaseHistory($owner: String!, $repo: String!, $head: GitObjectID!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    object(oid: $head) {
      ... on Commit {
        history(first: ` + strconv.Itoa(PageSize) + `, after: $cursor) {
          pageInfo { hasNextPage endCursor }
          nodes { ...releaseCommit }
        }
      }
    }
  }
}

` + commitFragment

type objectNode struct {
	OID string `json:"oid"`
	// CommittedDate is RFC 3339; "" when not read.
	CommittedDate string `json:"committedDate"`
}

// commitOf is the expression of ref's commit: a tag peeled to the commit
// it names.
func commitOf(ref string) string { return ref + "^{commit}" }

// Read reads the commits of release head (a tag, a branch, a commit or
// HEAD) in repo, with their pull requests: those since the previous
// release (see Previous) when since is "", else since --since.
//
// When the start of the range is a tag or a branch, GitHub compares the
// two (Ref.compare): the commits of head that it does not have, whatever
// the merge strategy. A commit (a SHA, HEAD~3) cannot be compared through
// the API, so head's history is read, newest first as git log reads it,
// down to that commit: exact for a linear history (squash or rebase
// merges); with merge commits, a merged branch's commits older than since
// are left out, though their pull request is found through the merge
// commit.
//
// Requests: one to find head and the start (the tags take one per 100),
// then one per PageSize commits. Errors: head or since not found is exit
// code 3; no previous release, since not in head's history and a range of
// more than MaxCommits are exit code 1.
func Read(ctx context.Context, q graph.Querier, repo issue.Repo, head, since string) (*Range, error) {
	r := &Range{Repo: repo, Head: head, Since: since}
	vars := map[string]any{"owner": repo.Owner, "repo": repo.Name, "head": commitOf(head)}
	base := "" // the qualified ref to compare with; "" walks the history
	if since == "" {
		tags, headCommit, total, err := readTags(ctx, q, vars, r)
		if err != nil {
			return nil, err
		}
		r.HeadCommit = headCommit
		prev, ok := Previous(tags, head, headCommit)
		if !ok {
			return nil, noPrevious(r, len(tags), total)
		}
		r.Since, r.SinceCommit, r.Picked, base = prev.Name, prev.Commit, true, "refs/tags/"+prev.Name
	} else {
		var resp struct {
			Repository *struct {
				Head     *objectNode `json:"head"`
				Since    *objectNode `json:"since"`
				SinceRef *struct {
					Prefix string `json:"prefix"`
					Name   string `json:"name"`
				} `json:"sinceRef"`
			} `json:"repository"`
		}
		v := map[string]any{"owner": repo.Owner, "repo": repo.Name, "head": commitOf(head), "since": commitOf(since), "sinceRef": since}
		if err := q.Query(ctx, sinceQuery, v, &resp); err != nil {
			return nil, err
		}
		rp := resp.Repository
		switch {
		case rp == nil:
			return nil, repoMissing(repo)
		case rp.Head == nil:
			return nil, refNotFound(ctx, q, repo, head, "")
		case rp.Since == nil:
			return nil, refNotFound(ctx, q, repo, since, "--since ")
		}
		r.HeadCommit, r.SinceCommit = rp.Head.OID, rp.Since.OID
		// A start newer than the head would walk 1000 commits for nothing:
		// the dates tell at once. GitHub's committer dates are RFC 3339 in
		// UTC, so they compare as strings.
		if r.HeadCommit != r.SinceCommit && rp.Since.CommittedDate > rp.Head.CommittedDate && rp.Head.CommittedDate != "" {
			return nil, notInHistory(r, "is newer than")
		}
		if rp.SinceRef != nil {
			base = rp.SinceRef.Prefix + rp.SinceRef.Name
		}
	}
	var err error
	if base != "" {
		r.Compared = true
		err = r.compare(ctx, q, base)
	} else {
		err = r.walk(ctx, q)
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// tagNode is a tag as ReleaseTags reads it: its target is a commit, or
// an annotated tag naming one.
type tagNode struct {
	Name   string `json:"name"`
	Target *struct {
		objectType
		Target *objectType `json:"target"`
	} `json:"target"`
}

type objectType struct {
	Typename string `json:"__typename"`
	OID      string `json:"oid"`
}

// readTags reads up to MaxTags tags, the newest first, head's commit and
// how many tags there are. The tags are read in the order of their
// commits' dates, which need not be their versions' order (a backport
// tagged after a newer release), so all of them are read, not only until
// a previous release turns up.
func readTags(ctx context.Context, q graph.Querier, vars map[string]any, r *Range) ([]Tag, string, int, error) {
	type resp struct {
		Repository *struct {
			Head *objectNode                `json:"head"`
			Refs graph.Connection[*tagNode] `json:"refs"`
		} `json:"repository"`
	}
	headCommit := ""
	var missing error
	noHead := false
	page, err := graph.Paginate(ctx, q, tagsQuery, vars, MaxTags, func(x *resp) *graph.Connection[*tagNode] {
		switch {
		case x.Repository == nil:
			missing = repoMissing(r.Repo)
			return nil
		case x.Repository.Head == nil:
			noHead = true
			return nil
		}
		headCommit = x.Repository.Head.OID
		return &x.Repository.Refs
	})
	if noHead {
		return nil, "", 0, refNotFound(ctx, q, r.Repo, r.Head, "")
	}
	if missing != nil {
		return nil, "", 0, missing
	}
	if err != nil {
		return nil, "", 0, err
	}
	var tags []Tag
	for _, n := range page.Nodes {
		if n == nil || n.Target == nil {
			continue
		}
		t := Tag{Name: n.Name}
		switch tg := n.Target; {
		case tg.Typename == "Commit":
			t.Commit = tg.OID
		case tg.Typename == "Tag" && tg.Target != nil && tg.Target.Typename == "Commit":
			t.Commit = tg.Target.OID
		}
		tags = append(tags, t)
	}
	return tags, headCommit, page.TotalCount, nil
}

// compare reads the commits of r.HeadCommit that base, a qualified ref,
// does not have: the first page tells how many, and a range of more than
// MaxCommits is refused before the rest is read.
func (r *Range) compare(ctx context.Context, q graph.Querier, base string) error {
	vars := map[string]any{"owner": r.Repo.Owner, "repo": r.Repo.Name, "base": base, "head": r.HeadCommit, "cursor": nil}
	for {
		var resp struct {
			Repository *struct {
				Ref *struct {
					Compare *struct {
						Status  string                        `json:"status"`
						Commits graph.Connection[*commitNode] `json:"commits"`
					} `json:"compare"`
				} `json:"ref"`
			} `json:"repository"`
		}
		if err := q.Query(ctx, compareQuery, vars, &resp); err != nil {
			return err
		}
		rp := resp.Repository
		switch {
		case rp == nil:
			return repoMissing(r.Repo)
		case rp.Ref == nil || rp.Ref.Compare == nil:
			return refMissing(r.Repo, r.Since, sinceFlag(r))
		}
		// BEHIND: the start has commits the head lacks, all of the head's
		// included; DIVERGED: each has its own. Either way the start is not
		// in the head's history, and the commits "since" it would be a
		// guess (none, for BEHIND).
		switch rp.Ref.Compare.Status {
		case "BEHIND":
			return notInHistory(r, "is newer than")
		case "DIVERGED":
			return notInHistory(r, "is not in the history of")
		}
		c := rp.Ref.Compare.Commits
		if c.TotalCount > MaxCommits {
			return tooMany(r, fmt.Sprintf("%d commits", c.TotalCount))
		}
		r.add(c.Nodes)
		next := c.PageInfo.EndCursor
		if !c.PageInfo.HasNextPage || next == "" || next == vars["cursor"] {
			return nil
		}
		vars["cursor"] = next
	}
}

// walk reads r.HeadCommit's history, newest first, down to r.SinceCommit,
// and keeps the commits before it, oldest first.
func (r *Range) walk(ctx context.Context, q graph.Querier) error {
	vars := map[string]any{"owner": r.Repo.Owner, "repo": r.Repo.Name, "head": r.HeadCommit, "cursor": nil}
	var commits []*commitNode
	for {
		var resp struct {
			Repository *struct {
				Object *struct {
					History *graph.Connection[*commitNode] `json:"history"`
				} `json:"object"`
			} `json:"repository"`
		}
		if err := q.Query(ctx, historyQuery, vars, &resp); err != nil {
			return err
		}
		rp := resp.Repository
		switch {
		case rp == nil:
			return repoMissing(r.Repo)
		case rp.Object == nil || rp.Object.History == nil:
			return refMissing(r.Repo, r.Head, "")
		}
		h := rp.Object.History
		for _, c := range h.Nodes {
			if c == nil {
				continue
			}
			if c.OID == r.SinceCommit {
				slices.Reverse(commits)
				r.add(commits)
				return nil
			}
			if len(commits) == MaxCommits {
				return tooMany(r, fmt.Sprintf("more than %d commits", MaxCommits))
			}
			commits = append(commits, c)
		}
		next := h.PageInfo.EndCursor
		if !h.PageInfo.HasNextPage || next == "" || next == vars["cursor"] {
			return notInHistory(r, "is not in the history of")
		}
		vars["cursor"] = next
	}
}

// add keeps commits, with their merged pull requests of r.Repo, warning
// about lists cut by the windows.
func (r *Range) add(nodes []*commitNode) {
	for _, n := range nodes {
		if n == nil {
			continue
		}
		c := Commit{OID: n.OID, Message: n.Message}
		if n.Pulls.TotalCount > len(n.Pulls.Nodes) {
			r.Warnings = append(r.Warnings, fmt.Sprintf("commit %s belongs to %d pull requests; read the first %d",
				Short(n.OID), n.Pulls.TotalCount, len(n.Pulls.Nodes)))
		}
		for _, p := range n.Pulls.Nodes {
			if p == nil || !p.Merged {
				continue
			}
			pr, err := issue.ParseRepo(p.Repository.NameWithOwner)
			if err != nil || !pr.Same(r.Repo) {
				continue // a fork's pull request
			}
			pull := Pull{Ref: issue.Ref{Repo: r.Repo, Number: p.Number}}
			if p.MergeCommit != nil {
				pull.MergeCommit = p.MergeCommit.OID
			}
			for _, i := range p.Closing.Nodes {
				if i == nil {
					continue
				}
				ir, err := issue.ParseRepo(i.Repository.NameWithOwner)
				if err != nil {
					continue
				}
				pull.Closes = append(pull.Closes, issue.Ref{Repo: ir, Number: i.Number})
			}
			if p.Closing.TotalCount > len(p.Closing.Nodes) {
				w := fmt.Sprintf("pull request %s closes %d issues; read the first %d",
					pull.Ref.String(r.Repo), p.Closing.TotalCount, len(p.Closing.Nodes))
				if !slices.Contains(r.Warnings, w) {
					r.Warnings = append(r.Warnings, w)
				}
			}
			c.Pulls = append(c.Pulls, pull)
		}
		r.Commits = append(r.Commits, c)
	}
}

// Short is a commit's oid as output names it: its first 7 characters.
func Short(oid string) string { return oid[:min(7, len(oid))] }

func sinceFlag(r *Range) string {
	if r.Picked {
		return "tag "
	}
	return "--since "
}

func repoMissing(repo issue.Repo) error {
	return render.NotFound("repository %s not found, or the token cannot see it", repo).
		WithFix("check board.repos in .github/dojo.toml, or gh auth status")
}

const refQuery = `query ReleaseRef($owner: String!, $repo: String!, $ref: String!) {
  repository(owner: $owner, name: $repo) {
    object(expression: $ref) { oid }
  }
}`

// refNotFound is the error for ref, named by flag ("--since "), that GitHub
// did not find in repo. A ref like origin/main is a remote-tracking branch
// of a local checkout, which GitHub knows only as main (#68): when ref has
// a "/" and the name after the first one is found, the fix names it. Only
// a failed read pays this request; if it fails as well, the error is the
// plain one.
func refNotFound(ctx context.Context, q graph.Querier, repo issue.Repo, ref, flag string) error {
	remote, name, ok := strings.Cut(ref, "/")
	if !ok || remote == "" || name == "" {
		return refMissing(repo, ref, flag)
	}
	var resp struct {
		Repository *struct {
			Object *objectNode `json:"object"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": repo.Owner, "repo": repo.Name, "ref": commitOf(name)}
	if err := q.Query(ctx, refQuery, vars, &resp); err != nil || resp.Repository == nil || resp.Repository.Object == nil {
		return refMissing(repo, ref, flag)
	}
	return refMissing(repo, ref, flag).
		WithFix(fmt.Sprintf("if %s is a git remote of your checkout, GitHub names it %s: pass %s%s", remote, name, flag, name))
}

// refMissing is the error for ref, named by flag ("--since "), that is no
// tag, branch or commit of repo.
func refMissing(repo issue.Repo, ref, flag string) *render.Error {
	return render.NotFound("%s%s: no tag, branch or commit of %s", flag, ref, repo).
		WithFix(fmt.Sprintf("pass a tag, a branch or a commit of %s (push it first)", repo))
}

// noPrevious is the error of a first release: no version tag before it
// among the read ones of total tags. The whole history is not taken
// instead: on a project that starts using gh-dojo it would apply the verb
// to every issue a commit ever named.
func noPrevious(r *Range, read, total int) error {
	more := ""
	if read < total {
		more = fmt.Sprintf(" among its %d newest tags of %d", read, total)
	}
	return render.Usage("no release before %s: %s has no version tag (v1.2.3) before it%s", r.Head, r.Repo, more).
		WithFix("name where the release starts: gh dojo release " + r.Head + " --since <tag, branch or commit> --verb <verb>")
}

func tooMany(r *Range, n string) error {
	or := ""
	if !r.Compared {
		or = ", or --since is not in its history"
	}
	return render.Usage("%s..%s has %s, more than gh dojo release reads (%d)%s", r.Since, r.Head, n, MaxCommits, or).
		WithFix("pass a later --since, a tag, branch or commit closer to " + r.Head)
}

// notInHistory is the error for a start of the range that how relates to
// the head: "is newer than", "is not in the history of". A start picked
// from the tags is named as such.
func notInHistory(r *Range, how string) error {
	start := "--since " + r.Since
	if r.Picked {
		start = "the previous release " + r.Since + " (the highest version tag)"
	}
	return render.Usage("%s %s %s", start, how, r.Head).
		WithFix("pass --since, a tag, branch or commit that " + r.Head + " includes")
}
