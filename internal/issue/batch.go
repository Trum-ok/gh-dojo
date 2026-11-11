package issue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

// Result is one issue read by ReadAll: its card, or why it was not read.
type Result struct {
	Number int
	Card   *Card // nil when Err is set
	// Err is a *render.Error about this issue alone (exit code 3), and
	// Cause its kind, so callers can group several.
	Err   error
	Cause Cause
}

// ReadAll reads the cards of several issues of req.Repo (req.Number is
// ignored) with the same windows as Read: one request per MaxBatch issues,
// with the card selection aliased once per issue. A single number is read
// with Read, so it sends exactly the request Read sends.
//
// A Lookup is read once per request, at the top of the batch, and every
// card of the request gets what it found (Card.Found): the labels, users
// and linked issues a verb needs are the same for every issue it runs on.
//
// It returns one Result per number, in order. A missing issue or a pull
// request number fails only its own Result. When a batch request fails as
// a whole (a timeout, say), its issues are read once more one by one;
// the first of those reads to fail that way is returned as err, together
// with the results read before it. A missing repository is err at once.
func ReadAll(ctx context.Context, q graph.Querier, req Request, numbers []int) ([]Result, error) {
	for _, n := range numbers {
		if n <= 0 {
			return nil, render.Usage("invalid issue number %d: want 1 or greater", n)
		}
	}
	if req.Repo.Owner == "" || req.Repo.Name == "" {
		return nil, render.Internal("issue.ReadAll: no repository")
	}
	if len(numbers) == 1 {
		return readEach(ctx, q, req, numbers)
	}
	out := make([]Result, 0, len(numbers))
	for start := 0; start < len(numbers); start += MaxBatch {
		chunk := numbers[start:min(start+MaxBatch, len(numbers))]
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

// readEach reads numbers one request each with Read, stopping at the first
// error that is not about the issue alone.
func readEach(ctx context.Context, q graph.Querier, req Request, numbers []int) ([]Result, error) {
	out := make([]Result, 0, len(numbers))
	for _, n := range numbers {
		r := req
		r.Number = n
		card, err := Read(ctx, q, r)
		res := Result{Number: n, Card: card, Err: err}
		if err != nil {
			switch render.FromError(err).What {
			case notFound(r).What:
				res.Cause = CauseMissing
			case pullRequest(r).What:
				res.Cause = CausePullRequest
			default:
				return out, err
			}
		}
		out = append(out, res)
	}
	return out, nil
}

// errRepoMissing is the cause of the error for a missing repository.
var errRepoMissing = errors.New("repository not found")

func readBatch(ctx context.Context, q graph.Querier, req Request, numbers []int) ([]Result, error) {
	vars := map[string]any{
		"owner": req.Repo.Owner, "repo": req.Repo.Name,
		"comments": commentsWindow(req.Comments), "status": req.StatusField,
	}
	for i, n := range numbers {
		vars[fmt.Sprintf("n%d", i)] = n
	}
	query, extra := batchQuery(len(numbers), req.Lookup)
	for k, v := range extra {
		vars[k] = v
	}
	var resp batchResponse
	err := q.Query(ctx, query, vars, &resp)
	var missing map[string]bool
	var hidden map[string][][]any
	if err != nil {
		var ok bool
		missing, hidden, ok = perIssueErrors(err)
		switch {
		case ok && resp.Repository != nil:
		case render.FromError(err).Code == render.ExitNotFound && resp.Repository == nil:
			return nil, render.NotFound("repository %s not found, or the token cannot see it", req.Repo).
				WithFix("gh repo view " + req.Repo.String()).WithCause(errors.Join(errRepoMissing, err))
		default:
			return nil, err
		}
	}
	var found *Found
	if !req.Lookup.Empty() {
		if found, err = req.Lookup.found(resp.lookups, resp.Viewer, resp.data); err != nil {
			return nil, render.Internal("cannot decode GitHub's answer").WithCause(err)
		}
	}
	out := make([]Result, len(numbers))
	for i, n := range numbers {
		r := req
		r.Number = n
		alias := batchAlias(i)
		node := resp.Repository[alias]
		out[i].Number = n
		switch {
		case node == nil || missing[alias]:
			out[i].Err, out[i].Cause = notFound(r), CauseMissing
		case node.Typename != "Issue":
			out[i].Err, out[i].Cause = pullRequest(r), CausePullRequest
		default:
			out[i].Card = node.card(r, hidden[alias])
			out[i].Card.Incomplete = len(hidden[alias]) > 0
			out[i].Card.Found = found
		}
	}
	return out, nil
}

func pullRequest(req Request) *render.Error {
	return render.NotFound("#%d in %s is a pull request; gh dojo works with issues only", req.Number, req.Repo).
		WithFix(ListFix)
}

// batchResponse is the answer of batchQuery: the issues by alias, the
// lookups of the repository, the viewer, and the whole "data" object for
// the links of a Lookup.
type batchResponse struct {
	Repository map[string]*issueJSON // by alias; nil when there is none
	lookups    *repoJSON
	Viewer     *User
	data       map[string]json.RawMessage
}

func (r *batchResponse) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &r.data); err != nil {
		return err
	}
	if raw, ok := r.data["repository"]; ok {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		if fields != nil {
			// The aliases are the issues; the other fields are the
			// lookups, read from the same decoded object.
			r.Repository = map[string]*issueJSON{}
			r.lookups = &repoJSON{}
			if err := r.lookups.fromFields(fields); err != nil {
				return err
			}
		}
		for key, f := range fields {
			if !isBatchAlias(key) {
				continue
			}
			var node *issueJSON
			if err := json.Unmarshal(f, &node); err != nil {
				return err
			}
			r.Repository[key] = node
		}
	}
	if raw, ok := r.data["viewer"]; ok {
		if err := json.Unmarshal(raw, &r.Viewer); err != nil {
			return err
		}
	}
	return nil
}

// perIssueErrors sorts the GraphQL errors of a batch by issue alias: a
// NOT_FOUND at repository.<alias> is a missing issue, a NOT_FOUND or
// FORBIDDEN below it a part of the card withheld from the token, kept as
// its path below the alias (list indexes as ints, as Read's nestedPaths).
// A NOT_FOUND or FORBIDDEN at a link of the Lookup is that link's alone
// (Linked.Missing). ok is false when any error is something else, which
// fails the whole request.
func perIssueErrors(err error) (missing map[string]bool, hidden map[string][][]any, ok bool) {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil || len(gqlErr.Errors) == 0 {
		return nil, nil, false
	}
	missing, hidden = map[string]bool{}, map[string][][]any{}
	for _, item := range gqlErr.Errors {
		if isLinkPath(item.Path) && (item.Type == "NOT_FOUND" || item.Type == "FORBIDDEN") {
			continue
		}
		if len(item.Path) < 2 || item.Path[0] != "repository" {
			return nil, nil, false
		}
		alias, isString := item.Path[1].(string)
		switch {
		case !isString:
			return nil, nil, false
		case len(item.Path) == 2 && item.Type == "NOT_FOUND":
			missing[alias] = true
		case len(item.Path) > 2 && (item.Type == "NOT_FOUND" || item.Type == "FORBIDDEN"):
			path := make([]any, 0, len(item.Path)-2)
			for _, p := range item.Path[2:] {
				if f, isNum := p.(float64); isNum {
					p = int(f)
				}
				path = append(path, p)
			}
			hidden[alias] = append(hidden[alias], path)
		default:
			return nil, nil, false
		}
	}
	return missing, hidden, true
}

func commentsWindow(n int) int {
	if n <= 0 {
		n = DefaultComments
	}
	return min(n, MaxComments)
}

func batchAlias(i int) string { return fmt.Sprintf("i%d", i) }

// isBatchAlias reports whether key is an issue alias of batchQuery.
func isBatchAlias(key string) bool {
	n, ok := strings.CutPrefix(key, "i")
	if !ok || n == "" {
		return false
	}
	_, err := strconv.Atoi(n)
	return err == nil
}

// batchQuery reads n issues: cardSelection as a fragment, aliased once per
// issue, with the fields of l once (see lookupQuery). It returns l's
// variables too.
//
//	query IssueCards($owner: String!, ..., $n0: Int!, $n1: Int!, $label0: String!) {
//	  repository(owner: $owner, name: $repo) {
//	    i0: issueOrPullRequest(number: $n0) { ...card }
//	    i1: issueOrPullRequest(number: $n1) { ...card }
//	    label0: label(name: $label0) { id name }
//	  }
//	}
func batchQuery(n int, l Lookup) (string, map[string]any) {
	var p lookupParts
	if !l.Empty() {
		p = l.parts()
	}
	var b strings.Builder
	b.WriteString("query IssueCards($owner: String!, $repo: String!, $comments: Int!, $status: String!")
	for i := range n {
		fmt.Fprintf(&b, ", $n%d: Int!", i)
	}
	b.WriteString(p.decls)
	b.WriteString(") {\n  repository(owner: $owner, name: $repo) {\n")
	for i := range n {
		fmt.Fprintf(&b, "    %s: issueOrPullRequest(number: $n%d) { ...card }\n", batchAlias(i), i)
	}
	if p.repo != "" {
		b.WriteString(strings.TrimPrefix(p.repo, "\n") + "\n")
	}
	b.WriteString("  }\n" + p.top + "}\n\nfragment card on IssueOrPullRequest ")
	b.WriteString(l.widen(cardSelection))
	b.WriteString("\n\n")
	b.WriteString(cardFragments)
	return b.String(), p.vars
}
