package issue

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// MaxBatch is how many issues ReadAll reads in one request; more take one
// request per MaxBatch issues.
const MaxBatch = 20

// Result is one issue read by ReadAll: its card, or why it was not read.
type Result struct {
	Number int
	Card   *Card // nil when Err is set
	// Err is a *render.Error about this issue alone: a missing issue or a
	// pull request number (exit code 3).
	Err error
}

// ReadAll reads the cards of several issues of req.Repo (req.Number is
// ignored) with the same windows as Read: one request per MaxBatch issues,
// aliasing the card query once per issue. A single number is read with
// Read, so it sends exactly the request Read sends.
//
// It returns one Result per number, in order. A missing issue or a pull
// request number fails only its own Result. A failure of a whole request
// (network, auth, a missing repository) is returned as err together with
// the results of the requests that succeeded before it.
func ReadAll(ctx context.Context, q graph.Querier, req Request, numbers []int) ([]Result, error) {
	for _, n := range numbers {
		if n <= 0 {
			return nil, render.Usage("invalid issue number %d: want 1 or greater", n)
		}
	}
	if len(numbers) == 1 {
		req.Number = numbers[0]
		card, err := Read(ctx, q, req)
		if err != nil && render.FromError(err).Code != render.ExitNotFound {
			return nil, err
		}
		return []Result{{Number: req.Number, Card: card, Err: err}}, nil
	}
	if req.Repo.Owner == "" || req.Repo.Name == "" {
		return nil, render.Internal("issue.ReadAll: no repository")
	}
	out := make([]Result, 0, len(numbers))
	for start := 0; start < len(numbers); start += MaxBatch {
		chunk := numbers[start:min(start+MaxBatch, len(numbers))]
		res, err := readBatch(ctx, q, req, chunk)
		if err != nil {
			return out, err
		}
		out = append(out, res...)
	}
	return out, nil
}

func readBatch(ctx context.Context, q graph.Querier, req Request, numbers []int) ([]Result, error) {
	vars := map[string]any{
		"owner": req.Repo.Owner, "repo": req.Repo.Name,
		"comments": commentsWindow(req.Comments), "status": req.StatusField,
	}
	for i, n := range numbers {
		vars[fmt.Sprintf("n%d", i)] = n
	}
	var resp struct {
		Repository map[string]*issueJSON `json:"repository"`
	}
	err := q.Query(ctx, batchQuery(len(numbers)), vars, &resp)
	var missing map[string]bool
	var hidden map[string][][]any
	if err != nil {
		var ok bool
		missing, hidden, ok = perIssueErrors(err)
		switch {
		case ok && resp.Repository != nil:
		case render.FromError(err).Code == render.ExitNotFound && resp.Repository == nil:
			return nil, render.NotFound("repository %s not found, or the token cannot see it", req.Repo).
				WithFix("gh repo view " + req.Repo.String()).WithCause(err)
		default:
			return nil, err
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
			out[i].Err = notFound(r)
		case node.Typename != "Issue":
			out[i].Err = render.NotFound("#%d in %s is a pull request, not an issue", n, req.Repo).
				WithFix(fmt.Sprintf("gh pr view %d -R %s", n, req.Repo))
		default:
			out[i].Card = node.card(r, hidden[alias])
			out[i].Card.Incomplete = len(hidden[alias]) > 0
		}
	}
	return out, nil
}

// perIssueErrors sorts the GraphQL errors of a batch by issue alias: a
// NOT_FOUND at repository.<alias> is a missing issue, a NOT_FOUND or
// FORBIDDEN below it a part of the card withheld from the token, kept as
// its path below the alias (list indexes as ints, as Read's nestedPaths).
// ok is false when any error is something else, which fails the whole
// request.
func perIssueErrors(err error) (missing map[string]bool, hidden map[string][][]any, ok bool) {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil || len(gqlErr.Errors) == 0 {
		return nil, nil, false
	}
	missing, hidden = map[string]bool{}, map[string][][]any{}
	for _, item := range gqlErr.Errors {
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

// batchQuery reads n issues: cardQuery's selection of issueOrPullRequest
// as a fragment, aliased once per issue.
func batchQuery(n int) string {
	var b strings.Builder
	b.WriteString("query IssueCards($owner: String!, $repo: String!, $comments: Int!, $status: String!")
	for i := range n {
		fmt.Fprintf(&b, ", $n%d: Int!", i)
	}
	b.WriteString(") {\n  repository(owner: $owner, name: $repo) {\n")
	for i := range n {
		fmt.Fprintf(&b, "    %s: issueOrPullRequest(number: $n%d) { ...card }\n", batchAlias(i), i)
	}
	b.WriteString("  }\n}\n\nfragment card on IssueOrPullRequest ")
	b.WriteString(cardSelection)
	b.WriteString("\n")
	b.WriteString(cardFragments)
	return b.String()
}

// cardSelection and cardFragments are cut from cardQuery, so a batch reads
// exactly what Read does.
var (
	cardSelection = selectionAfter(cardQuery, "issueOrPullRequest(number: $number)")
	cardFragments = cardQuery[strings.Index(cardQuery, "\nfragment ")+1:]
)

// selectionAfter returns the brace-delimited selection set that follows
// field in query, braces included.
func selectionAfter(query, field string) string {
	start := strings.Index(query, field)
	if start < 0 {
		panic("issue: " + field + " not in the card query")
	}
	open := start + strings.IndexByte(query[start:], '{')
	depth := 0
	for i := open; i < len(query); i++ {
		switch query[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return query[open : i+1]
			}
		}
	}
	panic("issue: unbalanced card query")
}
