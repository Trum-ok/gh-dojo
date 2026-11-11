package issue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Ref names an issue on the command line: a number in the default
// repository, or in another one.
type Ref struct {
	Repo   Repo
	Number int
}

// refRE matches "12", "#12" and "owner/name#12".
var refRE = regexp.MustCompile(`^(?:([A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9._-]+)#|#)?([1-9][0-9]*)$`)

// RefExamples shows how to write an issue reference.
const RefExamples = `12, "#12" or owner/repo#12`

// ParseRef parses an issue reference, "12", "#12" or "owner/name#12",
// ignoring surrounding spaces; a bare number is in def. ok is false for
// anything else, such as free text.
func ParseRef(s string, def Repo) (Ref, bool) {
	m := refRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Ref{}, false
	}
	n, err := strconv.ParseInt(m[2], 10, 32)
	if err != nil {
		return Ref{}, false
	}
	r := Ref{Repo: def, Number: int(n)}
	if m[1] != "" {
		repo, err := ParseRepo(m[1])
		if err != nil {
			return Ref{}, false
		}
		r.Repo = repo
	}
	return r, true
}

// String returns "#12" for an issue in def, else "owner/name#12". Repos
// compare without case, as GitHub's names do.
func (r Ref) String(def Repo) string {
	if r.Repo == (Repo{}) || sameRepo(r.Repo, def) {
		return "#" + strconv.Itoa(r.Number)
	}
	return r.Repo.String() + "#" + strconv.Itoa(r.Number)
}

// Same reports whether r and o name the same issue.
func (r Ref) Same(o Ref) bool { return r.Number == o.Number && sameRepo(r.Repo, o.Repo) }

func sameRepo(a, b Repo) bool {
	return strings.EqualFold(a.Owner, b.Owner) && strings.EqualFold(a.Name, b.Name)
}

// BlockingWindow is how many of a linked issue's blocked issues are read
// (Linked.Blocks): GitHub allows at most 50 per issue, so the list is
// whole.
const BlockingWindow = 50

// Linked is an issue read by its Ref to link it as a blocker.
type Linked struct {
	Ref Ref
	// Missing is set when there is no such issue or repository, or the
	// token cannot see it; PullRequest when the number is a pull request.
	// The other fields are empty then.
	Missing     bool
	PullRequest bool
	ID          string // node ID, for addBlockedBy; never print it
	Title       string
	State       string
	blocking    []string // node IDs of the issues it blocks
}

// Blocks reports whether the linked issue blocks the issue with node ID
// id: the blocked-by link is in place.
func (l *Linked) Blocks(id string) bool { return id != "" && slices.Contains(l.blocking, id) }

// Links are issues read by Ref in the request of another query, each
// under its own alias at the top of the document, so a command reads its
// blockers along with what else it needs (principle 6). A Lookup carries
// them for a card; gh dojo new and block add them to their own queries.
//
//	query X(..., $link0Owner: String!, $link0Repo: String!, $link0Number: Int!) {
//	  ...
//	  link0: repository(owner: $link0Owner, name: $link0Repo) {
//	    issueOrPullRequest(number: $link0Number) { __typename ... on Issue { id title state blocking(first: 50) { nodes { id } } } }
//	  }
//	}
type Links []Ref

// linkPrefix starts the alias of every link field.
const linkPrefix = "link"

func linkAlias(i int) string { return fmt.Sprintf("%s%d", linkPrefix, i) }

// Decls returns the variable declarations of the links, each starting
// with ", ".
func (ls Links) Decls() string {
	var b strings.Builder
	for i := range ls {
		a := linkAlias(i)
		fmt.Fprintf(&b, ", $%sOwner: String!, $%sRepo: String!, $%sNumber: Int!", a, a, a)
	}
	return b.String()
}

// Fields returns the top-level fields that read the links, one per line,
// indented by two spaces.
func (ls Links) Fields() string {
	var b strings.Builder
	for i := range ls {
		a := linkAlias(i)
		fmt.Fprintf(&b, "  %s: repository(owner: $%sOwner, name: $%sRepo) {\n"+
			"    issueOrPullRequest(number: $%sNumber) { __typename ... on Issue { id title state "+
			"blocking(first: %d) { nodes { id } } } }\n  }\n", a, a, a, a, BlockingWindow)
	}
	return b.String()
}

// AddVars adds the links' variables to vars.
func (ls Links) AddVars(vars map[string]any) {
	for i, r := range ls {
		a := linkAlias(i)
		vars[a+"Owner"], vars[a+"Repo"], vars[a+"Number"] = r.Repo.Owner, r.Repo.Name, r.Number
	}
}

// Decode returns the links read from the "data" object of a response, in
// order. A link whose field is null or absent is Missing.
func (ls Links) Decode(data map[string]json.RawMessage) ([]*Linked, error) {
	out := make([]*Linked, len(ls))
	for i, r := range ls {
		l := &Linked{Ref: r, Missing: true}
		out[i] = l
		raw, ok := data[linkAlias(i)]
		if !ok {
			continue
		}
		var v *struct {
			Issue *struct {
				Typename string `json:"__typename"`
				ID       string `json:"id"`
				Title    string `json:"title"`
				State    string `json:"state"`
				Blocking struct {
					Nodes []*struct {
						ID string `json:"id"`
					} `json:"nodes"`
				} `json:"blocking"`
			} `json:"issueOrPullRequest"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if v == nil || v.Issue == nil {
			continue
		}
		l.Missing = false
		if v.Issue.Typename != "Issue" {
			l.PullRequest = true
			continue
		}
		l.ID, l.Title, l.State = v.Issue.ID, v.Issue.Title, v.Issue.State
		for _, n := range v.Issue.Blocking.Nodes {
			if n != nil {
				l.blocking = append(l.blocking, n.ID)
			}
		}
	}
	return out, nil
}

// isLinkPath reports whether a GraphQL error path points at or below a
// link field.
func isLinkPath(path []any) bool {
	if len(path) == 0 {
		return false
	}
	s, ok := path[0].(string)
	if !ok || !strings.HasPrefix(s, linkPrefix) {
		return false
	}
	_, err := strconv.Atoi(strings.TrimPrefix(s, linkPrefix))
	return err == nil
}

// OnlyLinkErrors reports whether err is a GraphQL error all of whose
// items are NOT_FOUND or FORBIDDEN at link fields: a linked issue or
// repository that does not exist or that the token cannot see. Decode
// marks those links Missing, and the rest of the response is good.
func OnlyLinkErrors(err error) bool {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil || len(gqlErr.Errors) == 0 {
		return false
	}
	for _, item := range gqlErr.Errors {
		if !isLinkPath(item.Path) || item.Type != "NOT_FOUND" && item.Type != "FORBIDDEN" {
			return false
		}
	}
	return true
}

// LinksError is the error for linked issues that cannot be blocked by:
// missing ones (exit code 3, "gh dojo list" as the fix) or pull requests
// (exit code 3), named by what (such as "--by"); nil when there are none.
// def is the default repository, for the refs and the message.
//
//	--by #98, #99: no such issue in Trum-ok/gh-dojo, or the token cannot see it
func LinksError(what string, links []*Linked, def Repo) error {
	var missing, prs []string
	where := " in " + def.String() // all missing ones are in def
	for _, l := range links {
		switch {
		case l.Missing:
			missing = append(missing, l.Ref.String(def))
			if !sameRepo(l.Ref.Repo, def) {
				where = ""
			}
		case l.PullRequest:
			prs = append(prs, l.Ref.String(def))
		}
	}
	switch {
	case len(missing) > 0:
		return render.NotFound("%s %s: no such issue%s, or the token cannot see it",
			what, strings.Join(missing, ", "), where).WithFix(ListFix)
	case len(prs) > 0:
		return render.NotFound("%s %s: a pull request, not an issue; only issues can block", what, strings.Join(prs, ", ")).
			WithFix(ListFix)
	}
	return nil
}

// Target is the issue gh dojo block and unblock change.
type Target struct {
	Repo   Repo
	Number int
	ID     string // node ID; never print it
	Title  string
}

// linksHead and linksTarget are the parts of the query ReadLinks sends.
const (
	linksHead   = `query IssueLinks($owner: String!, $repo: String!, $number: Int!`
	linksTarget = `) {
  repository(owner: $owner, name: $repo) {
    issueOrPullRequest(number: $number) { __typename ... on Issue { id title } }
  }
`
)

// ReadLinks reads issue number of repo and the issues refs name, with
// whether each already blocks it, in one request: what gh dojo block and
// unblock need. A missing repository or issue, or a pull request number,
// is exit code 3; so are missing or pull request refs (see LinksError,
// with what naming them). The links come back in the order of refs.
func ReadLinks(ctx context.Context, q graph.Querier, repo Repo, number int, refs []Ref, what string) (*Target, []*Linked, error) {
	if number <= 0 {
		return nil, nil, render.Usage("invalid issue number %d: want 1 or greater", number)
	}
	ls := Links(refs)
	query := linksHead + ls.Decls() + linksTarget + ls.Fields() + "}"
	vars := map[string]any{"owner": repo.Owner, "repo": repo.Name, "number": number}
	ls.AddVars(vars)

	var data map[string]json.RawMessage
	err := q.Query(ctx, query, vars, &data)
	var resp struct {
		Repository *struct {
			Issue *struct {
				Typename string `json:"__typename"`
				ID       string `json:"id"`
				Title    string `json:"title"`
			} `json:"issueOrPullRequest"`
		} `json:"repository"`
	}
	if raw, ok := data["repository"]; ok {
		if uerr := json.Unmarshal(raw, &resp.Repository); uerr != nil {
			return nil, nil, render.Internal("cannot decode GitHub's answer").WithCause(uerr)
		}
	}
	req := Request{Repo: repo, Number: number}
	switch {
	case err == nil, resp.Repository != nil && resp.Repository.Issue != nil && OnlyLinkErrors(err):
	case resp.Repository == nil && render.FromError(err).Code == render.ExitNotFound:
		return nil, nil, render.NotFound("repository %s not found, or the token cannot see it", repo).
			WithFix("gh repo view " + repo.String()).WithCause(err)
	case render.FromError(err).Code == render.ExitNotFound && onlyTargetOrLinks(err):
		return nil, nil, notFound(req).WithCause(err)
	default:
		return nil, nil, err
	}
	switch {
	case resp.Repository == nil || resp.Repository.Issue == nil:
		return nil, nil, notFound(req)
	case resp.Repository.Issue.Typename != "Issue":
		return nil, nil, pullRequest(req)
	}
	links, derr := ls.Decode(data)
	if derr != nil {
		return nil, nil, render.Internal("cannot decode GitHub's answer").WithCause(derr)
	}
	if err := LinksError(what, links, repo); err != nil {
		return nil, nil, err
	}
	t := &Target{Repo: repo, Number: number, ID: resp.Repository.Issue.ID, Title: resp.Repository.Issue.Title}
	return t, links, nil
}

// onlyTargetOrLinks reports whether every GraphQL error of err is a
// NOT_FOUND of the target issue or of a link: the target is missing, and
// maybe some links too.
func onlyTargetOrLinks(err error) bool {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || gqlErr == nil {
		return false
	}
	for _, item := range gqlErr.Errors {
		target := len(item.Path) == 2 && item.Path[0] == "repository" && item.Path[1] == "issueOrPullRequest"
		if item.Type != "NOT_FOUND" || !target && !isLinkPath(item.Path) {
			return false
		}
	}
	return true
}
