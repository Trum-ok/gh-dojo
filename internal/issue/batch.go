package issue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// MaxBatch is how many issues ReadAll reads in one request; more take one
// request per MaxBatch issues.
const MaxBatch = 20

// Cause says why ReadAll did not read one issue.
type Cause string

const (
	CauseMissing     Cause = "missing"      // no issue with that number
	CausePullRequest Cause = "pull request" // the number is a pull request
	// CauseNoRepo: the issue's repository does not exist, or the token
	// cannot see it (see ReadRefs).
	CauseNoRepo Cause = "no repository"
)

// Result is one issue read by ReadAll: its card, or why it was not read.
type Result struct {
	Repo   Repo // as GitHub spells it when the card was read
	Number int
	Card   *Card // nil when Err is set
	// Err is a *render.Error about this issue alone (exit code 3), and
	// Cause its kind, so callers can group several.
	Err   error
	Cause Cause
}

// Ref is the issue the result is about.
func (r Result) Ref() Ref { return Ref{Repo: r.Repo, Number: r.Number} }

// ReadAll reads the cards of several issues of req.Repo (req.Number is
// ignored); see ReadRefs.
func ReadAll(ctx context.Context, q graph.Querier, req Request, numbers []int) ([]Result, error) {
	refs := make([]Ref, len(numbers))
	for i, n := range numbers {
		refs[i] = Ref{Repo: req.Repo, Number: n}
	}
	return ReadRefs(ctx, q, req, refs)
}

// ReadRefs reads the cards of several issues (req.Number is ignored), each
// in the repository its ref names, req.Repo for a ref without one, with
// the same windows as Read: one request per MaxBatch issues, the card
// selection aliased once per issue below the field of its repository (see
// batchQuery). A single ref is read with Read, so it sends exactly the
// request Read sends.
//
// A Lookup is read once per request: labels and the assignable user once
// per repository, the viewer and the links once. Every card of the request
// gets what was found for its repository (Card.Found): the labels, users
// and linked issues a verb needs are the same for every issue of a
// repository it runs on.
//
// It returns one Result per ref, in order. A missing issue or a pull
// request number fails only its own Result; so does a missing repository
// other than req.Repo (CauseNoRepo), or req.Repo in a request that reads
// other repositories too: an issue of another repository may be a guess
// (a commit message's acme/api#3, see gh dojo release), and the issues of
// the others are still read. When a batch request fails as a whole (a
// timeout, say), its issues are read once more one by one; the first of
// those reads to fail that way is returned as err, together with the
// results read before it. A missing req.Repo in a request that reads no
// other repository is err at once: the config names it.
func ReadRefs(ctx context.Context, q graph.Querier, req Request, refs []Ref) ([]Result, error) {
	refs = slices.Clone(refs)
	for i, r := range refs {
		if r.Number <= 0 {
			return nil, invalidNumber(r.Number)
		}
		if r.Repo == (Repo{}) {
			refs[i].Repo = req.Repo
		}
		if refs[i].Repo.Owner == "" || refs[i].Repo.Name == "" {
			return nil, render.Internal("issue.ReadRefs: no repository")
		}
	}
	if len(refs) == 1 {
		return readEach(ctx, q, req, refs)
	}
	out := make([]Result, 0, len(refs))
	for start := 0; start < len(refs); start += MaxBatch {
		chunk := refs[start:min(start+MaxBatch, len(refs))]
		res, err := readBatch(ctx, q, req, chunk)
		if errors.Is(err, errRepoMissing) {
			return out, err
		}
		if err != nil {
			res, err = readEach(ctx, q, req, chunk)
		}
		out = append(out, res...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// readEach reads refs one request each with Read, stopping at the first
// error that is not about the issue alone.
func readEach(ctx context.Context, q graph.Querier, req Request, refs []Ref) ([]Result, error) {
	out := make([]Result, 0, len(refs))
	for _, ref := range refs {
		r := req
		r.Repo, r.Number = ref.Repo, ref.Number
		card, err := Read(ctx, q, r)
		res := Result{Repo: ref.Repo, Number: ref.Number, Card: card, Err: err}
		if card != nil {
			res.Repo = card.Repo // as GitHub spells it
		}
		switch {
		case err == nil:
		case errors.Is(err, errIssueMissing):
			res.Cause = CauseMissing
		case errors.Is(err, errPullRequest):
			res.Cause = CausePullRequest
		case errors.Is(err, errRepoMissing) && !r.Repo.Same(req.Repo):
			res.Cause = CauseNoRepo
		default:
			return out, err
		}
		out = append(out, res)
	}
	return out, nil
}

// The causes of the errors for a missing repository (repoNotFound), a
// missing issue (notFound) and a pull request number (pullRequest): what
// readEach and ReadRefs tell them apart by, never by their wording.
var (
	errRepoMissing  = errors.New("repository not found")
	errIssueMissing = errors.New("issue not found")
	errPullRequest  = errors.New("a pull request")
)

// repoNotFound is the error for a repository that does not exist or that
// the token cannot see; cause, which may be nil, is kept for errors.Is and
// As next to errRepoMissing.
func repoNotFound(repo Repo, cause error) *render.Error {
	return render.NotFound("repository %s not found, or the token cannot see it", repo).
		WithFix("gh repo view " + repo.String()).WithCause(errors.Join(errRepoMissing, cause))
}

// batchRepos groups refs by repository, in the order each repository
// first appears: the repository fields of batchQuery. Each group holds the
// indexes of its refs in refs, which number the issues' aliases.
func batchRepos(refs []Ref) (repos []Repo, groups [][]int) {
	for i, r := range refs {
		k := slices.IndexFunc(repos, func(o Repo) bool { return o.Same(r.Repo) })
		if k < 0 {
			repos = append(repos, r.Repo)
			groups = append(groups, nil)
			k = len(repos) - 1
		}
		groups[k] = append(groups[k], i)
	}
	return repos, groups
}

func readBatch(ctx context.Context, q graph.Querier, req Request, refs []Ref) ([]Result, error) {
	repos, groups := batchRepos(refs)
	vars := map[string]any{"comments": commentsWindow(req.Comments), "status": req.StatusField}
	for k, repo := range repos {
		vars[repoVar("owner", k)], vars[repoVar("repo", k)] = repo.Owner, repo.Name
	}
	for i, r := range refs {
		vars[fmt.Sprintf("n%d", i)] = r.Number
	}
	query, extra := batchQuery(len(refs), groups, req.Lookup)
	for k, v := range extra {
		vars[k] = v
	}
	var resp batchResponse
	err := q.Query(ctx, query, vars, &resp)
	var errs batchErrors
	if err != nil {
		var ok bool
		errs, ok = perIssueErrors(err)
		first := resp.Repos[repoAlias(0)]
		switch {
		case ok && (len(repos) > 1 || first != nil || !repos[0].Same(req.Repo)):
		case render.FromError(err).Code == render.ExitNotFound && len(repos) == 1 && first == nil:
			return nil, repoNotFound(repos[0], err)
		default:
			return nil, err
		}
	}
	out := make([]Result, len(refs))
	for k, group := range groups {
		block := resp.Repos[repoAlias(k)]
		var found *Found
		if block != nil && !req.Lookup.Empty() {
			if found, err = req.Lookup.found(block.lookups, resp.Viewer, resp.data); err != nil {
				return nil, render.Internal("cannot decode GitHub's answer").WithCause(err)
			}
		}
		for _, i := range group {
			r := req
			r.Repo, r.Number = refs[i].Repo, refs[i].Number
			alias := batchAlias(i)
			out[i].Repo, out[i].Number = r.Repo, r.Number
			var node *issueJSON
			if block != nil {
				node = block.issues[alias]
			}
			switch {
			case block == nil:
				out[i].Err, out[i].Cause = repoNotFound(r.Repo, nil), CauseNoRepo
			case node == nil || errs.missing[alias]:
				out[i].Err, out[i].Cause = notFound(r, nil), CauseMissing
			case node.Typename != "Issue":
				out[i].Err, out[i].Cause = pullRequest(r), CausePullRequest
			default:
				out[i].Card = node.card(r, errs.hidden[alias])
				out[i].Card.Incomplete = len(errs.hidden[alias]) > 0
				out[i].Card.Found = found
				out[i].Repo = out[i].Card.Repo
			}
		}
	}
	return out, nil
}

// pullRequest is the error for a number that is a pull request.
func pullRequest(req Request) *render.Error {
	return render.NotFound("#%d in %s is a pull request; gh dojo works with issues only", req.Number, req.Repo).
		WithFix(ListFix).WithCause(errPullRequest)
}

// batchResponse is the answer of batchQuery: the repository fields by
// alias (see repoAlias), each with its issues and lookups; the viewer; and
// the whole "data" object for the links of a Lookup. A repository field
// that is null or absent has no entry.
type batchResponse struct {
	Repos  map[string]*batchRepo
	Viewer *User
	data   map[string]json.RawMessage
}

// batchRepo is one repository field of a batch: the issues by alias and
// the lookups of the repository.
type batchRepo struct {
	issues  map[string]*issueJSON
	lookups *repoJSON
}

func (r *batchResponse) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &r.data); err != nil {
		return err
	}
	r.Repos = map[string]*batchRepo{}
	for key, raw := range r.data {
		if !isRepoAlias(key) {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		if fields == nil {
			continue
		}
		// The aliases are the issues; the other fields are the lookups,
		// read from the same decoded object.
		block := &batchRepo{issues: map[string]*issueJSON{}, lookups: &repoJSON{}}
		if err := block.lookups.fromFields(fields); err != nil {
			return err
		}
		for alias, f := range fields {
			if !isBatchAlias(alias) {
				continue
			}
			var node *issueJSON
			if err := json.Unmarshal(f, &node); err != nil {
				return err
			}
			block.issues[alias] = node
		}
		r.Repos[key] = block
	}
	if raw, ok := r.data["viewer"]; ok {
		if err := json.Unmarshal(raw, &r.Viewer); err != nil {
			return err
		}
	}
	return nil
}

// batchErrors are the GraphQL errors of a batch about one issue alone, by
// issue alias: missing issues, and the parts of a card withheld from the
// token, as paths below the alias (list indexes as ints, as Read's
// nestedPaths).
type batchErrors struct {
	missing map[string]bool
	hidden  map[string][][]any
}

// perIssueErrors sorts the GraphQL errors of a batch by issue alias: a
// NOT_FOUND at <repository>.<alias> is a missing issue, a NOT_FOUND or
// FORBIDDEN below it a part of the card withheld from the token. A
// NOT_FOUND at a repository field is a missing repository, whose field is
// null; a NOT_FOUND or FORBIDDEN at a link of the Lookup is that link's
// alone (Linked.Missing). ok is false when any error is something else,
// which fails the whole request.
func perIssueErrors(err error) (errs batchErrors, ok bool) {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil || len(gqlErr.Errors) == 0 {
		return batchErrors{}, false
	}
	errs = batchErrors{missing: map[string]bool{}, hidden: map[string][][]any{}}
	for _, item := range gqlErr.Errors {
		if isLinkPath(item.Path) && (item.Type == "NOT_FOUND" || item.Type == "FORBIDDEN") {
			continue
		}
		if len(item.Path) == 0 {
			return batchErrors{}, false
		}
		if repo, isString := item.Path[0].(string); !isString || !isRepoAlias(repo) {
			return batchErrors{}, false
		}
		if len(item.Path) == 1 {
			if item.Type != "NOT_FOUND" {
				return batchErrors{}, false
			}
			continue // a missing repository: its field is null
		}
		alias, isString := item.Path[1].(string)
		switch {
		case !isString:
			return batchErrors{}, false
		case len(item.Path) == 2 && item.Type == "NOT_FOUND":
			errs.missing[alias] = true
		case len(item.Path) > 2 && (item.Type == "NOT_FOUND" || item.Type == "FORBIDDEN"):
			errs.hidden[alias] = append(errs.hidden[alias], intIndexes(item.Path[2:]))
		default:
			return batchErrors{}, false
		}
	}
	return errs, true
}

// commentsWindow is how many comments Request.Comments n reads: 0 means
// DefaultComments, at most MaxComments.
func commentsWindow(n int) int {
	if n <= 0 {
		n = DefaultComments
	}
	return min(n, MaxComments)
}

func batchAlias(i int) string { return fmt.Sprintf("i%d", i) }

// isBatchAlias reports whether key is an issue alias of batchQuery.
func isBatchAlias(key string) bool { return isIndexed(key, "i") }

// repoAlias is the field of the k-th repository of a batch: the first is
// "repository", unaliased as in Read, so a batch of one repository reads
// as it always did; the others r1, r2...
func repoAlias(k int) string {
	if k == 0 {
		return "repository"
	}
	return fmt.Sprintf("r%d", k)
}

// isRepoAlias reports whether key is a repository field of batchQuery.
func isRepoAlias(key string) bool { return key == "repository" || isIndexed(key, "r") }

// repoVar is the variable of the k-th repository's owner or name (name
// "owner" or "repo"): $owner and $repo for the first, as in Read, then
// $owner1, $repo1...
func repoVar(name string, k int) string {
	if k == 0 {
		return name
	}
	return name + strconv.Itoa(k)
}

// isIndexed reports whether key is prefix followed by a number.
func isIndexed(key, prefix string) bool {
	n, ok := strings.CutPrefix(key, prefix)
	if !ok || n == "" {
		return false
	}
	_, err := strconv.Atoi(n)
	return err == nil
}

// batchQuery reads n issues: cardSelection as a fragment, aliased once per
// issue below the field of its repository, with the fields of l (see
// lookupQuery): those of a repository, such as labels, below each
// repository field, the viewer and the links once. groups are the issue
// indexes of each repository, in the order of the repository fields (see
// batchRepos). It returns l's variables too. One repository gives the
// query a batch has always sent.
//
//	query IssueCards($owner: String!, ..., $n0: Int!, $n1: Int!, $n2: Int!, $owner1: String!, $repo1: String!, $label0: String!) {
//	  repository(owner: $owner, name: $repo) {
//	    i0: issueOrPullRequest(number: $n0) { ...card }
//	    i2: issueOrPullRequest(number: $n2) { ...card }
//	    label0: label(name: $label0) { id name }
//	  }
//	  r1: repository(owner: $owner1, name: $repo1) {
//	    i1: issueOrPullRequest(number: $n1) { ...card }
//	    label0: label(name: $label0) { id name }
//	  }
//	}
func batchQuery(n int, groups [][]int, l Lookup) (string, map[string]any) {
	var p lookupParts
	if !l.Empty() {
		p = l.parts()
	}
	var b strings.Builder
	b.WriteString("query IssueCards($owner: String!, $repo: String!, $comments: Int!, $status: String!")
	for i := range n {
		fmt.Fprintf(&b, ", $n%d: Int!", i)
	}
	for k := 1; k < len(groups); k++ {
		fmt.Fprintf(&b, ", $%s: String!, $%s: String!", repoVar("owner", k), repoVar("repo", k))
	}
	b.WriteString(p.decls)
	b.WriteString(") {\n")
	for k, group := range groups {
		field := "repository"
		if k > 0 {
			field = repoAlias(k) + ": repository"
		}
		fmt.Fprintf(&b, "  %s(owner: $%s, name: $%s) {\n", field, repoVar("owner", k), repoVar("repo", k))
		for _, i := range group {
			fmt.Fprintf(&b, "    %s: issueOrPullRequest(number: $n%d) { ...card }\n", batchAlias(i), i)
		}
		if p.repo != "" {
			b.WriteString(strings.TrimPrefix(p.repo, "\n") + "\n")
		}
		b.WriteString("  }\n")
	}
	b.WriteString(p.top + "}\n\nfragment card on IssueOrPullRequest ")
	b.WriteString(l.widen(cardSelection))
	b.WriteString("\n\n")
	b.WriteString(cardFragments)
	return b.String(), p.vars
}
