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
	// Host is the host of a URL ("github.com"); "" for 12, #12 and
	// owner/name#12, which are on the host gh dojo talks to. See CheckHost.
	Host string
	// Pull says the URL names a pull request (.../pull/12).
	Pull bool
}

// refRE matches "12", "#12" and "owner/name#12"; urlRefRE an issue's or
// pull request's URL, "https://github.com/owner/name/issues/12", maybe
// with a query or a fragment ("#issuecomment-1"). Any host: GitHub
// Enterprise Server URLs name issues the same way, and CheckHost compares
// it with the client's.
var (
	refRE    = regexp.MustCompile(`^(?:([A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9._-]+)#|#)?([1-9][0-9]*)$`)
	urlRefRE = regexp.MustCompile(`^https?://([^/\s?#]+)/([A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9._-]+)/(issues|pull)/([1-9][0-9]*)/?(?:[?#]\S*)?$`)
)

// RefExamples shows how to write an issue reference.
const RefExamples = `12, "#12", owner/repo#12 or the issue's URL`

// ParseRef parses an issue reference, "12", "#12", "owner/name#12" or the
// issue's URL ("https://github.com/owner/name/issues/12", a query or
// fragment ignored), ignoring surrounding spaces; a bare number is in def.
// A pull request's URL parses too, with Pull set. ok is false for anything
// else, such as free text.
func ParseRef(s string, def Repo) (Ref, bool) {
	s = strings.TrimSpace(s)
	var host, repo, kind, num string
	if m := refRE.FindStringSubmatch(s); m != nil {
		repo, num = m[1], m[2]
	} else if m := urlRefRE.FindStringSubmatch(s); m != nil {
		host, repo, kind, num = m[1], m[2], m[3], m[4]
	} else {
		return Ref{}, false
	}
	n, err := strconv.ParseInt(num, 10, 32)
	if err != nil {
		return Ref{}, false
	}
	r := Ref{Repo: def, Number: int(n), Host: strings.ToLower(host), Pull: kind == "pull"}
	if repo != "" {
		parsed, err := ParseRepo(repo)
		if err != nil {
			return Ref{}, false
		}
		r.Repo = parsed
	}
	return r, true
}

// CheckHost refuses a URL on another host than host, the one gh dojo
// talks to: exit code 1, since its number would name another issue there.
// arg is the reference as given. "www.github.com" is github.com.
func (r Ref) CheckHost(host, arg string) error {
	if r.Host == "" || host == "" {
		return nil
	}
	norm := func(h string) string { return strings.TrimPrefix(strings.ToLower(h), "www.") }
	if norm(r.Host) == norm(host) {
		return nil
	}
	return render.Usage("%s: the URL is on %s, gh dojo talks to %s", arg, r.Host, host).
		WithFix("pass an issue of " + host + " (gh's default host: GH_HOST or gh auth login)")
}

// String returns "#12" for an issue in def, else "owner/name#12". Repos
// compare without case, as GitHub's names do.
func (r Ref) String(def Repo) string {
	if r.Repo == (Repo{}) || sameRepo(r.Repo, def) {
		return "#" + strconv.Itoa(r.Number)
	}
	return r.Repo.String() + "#" + strconv.Itoa(r.Number)
}

// Arg returns r as a command-line argument: "12" for an issue in def, else
// "owner/name#12" (a # inside a word needs no quotes in the shell).
func (r Ref) Arg(def Repo) string { return strings.TrimPrefix(r.String(def), "#") }

// In returns r with def as its repository when it names none, as a bare
// number does, or names def in another case: def is spelled as the config
// spells it.
func (r Ref) In(def Repo) Ref {
	if r.Repo == (Repo{}) || sameRepo(r.Repo, def) {
		r.Repo = def
	}
	return r
}

// canonical is repo as GitHub spells it, "owner/name" from an answer,
// when that names the same repository; else repo.
func canonical(repo Repo, spelled string) Repo {
	if r, err := ParseRepo(spelled); err == nil && sameRepo(r, repo) {
		return r
	}
	return repo
}

// Same reports whether r and o name the same issue.
func (r Ref) Same(o Ref) bool { return r.Number == o.Number && sameRepo(r.Repo, o.Repo) }

func sameRepo(a, b Repo) bool {
	return strings.EqualFold(a.Owner, b.Owner) && strings.EqualFold(a.Name, b.Name)
}

// BlockingWindow is how many of an issue's links are read each way for
// a link: the issues a linked issue blocks (Linked.Blocks) and those that
// block it (Linked.BlockedBy), and the blockers of the issue gh dojo block
// and unblock change (Target.BlockedBy). GitHub allows at most 50 blockers
// per issue, so a blockedBy list read with it is whole.
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
	blockedBy   []string // node IDs of the issues blocking it
}

// Blocks reports whether the linked issue blocks the issue with node ID
// id: the blocked-by link is in place.
func (l *Linked) Blocks(id string) bool { return id != "" && slices.Contains(l.blocking, id) }

// BlockedBy reports whether the issue with node ID id blocks the linked
// issue: making the linked issue block it would close a cycle. The list
// read is whole (see BlockingWindow).
func (l *Linked) BlockedBy(id string) bool { return id != "" && slices.Contains(l.blockedBy, id) }

// Links are issues read by Ref in the request of another query, each
// under its own alias at the top of the document, so a command reads its
// blockers along with what else it needs (principle 6). A Lookup carries
// them for a card; gh dojo new and block add them to their own queries.
//
//	query X(..., $link0Owner: String!, $link0Repo: String!, $link0Number: Int!) {
//	  ...
//	  link0: repository(owner: $link0Owner, name: $link0Repo) {
//	    issueOrPullRequest(number: $link0Number) { __typename ... on Issue { id title state
//	      blocking(first: 50) { nodes { id } } blockedBy(first: 50) { nodes { id } } } }
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
// indented by two spaces. nameWithOwner gives the repository as GitHub
// spells it, whatever case the ref was written in.
func (ls Links) Fields() string {
	var b strings.Builder
	for i := range ls {
		a := linkAlias(i)
		fmt.Fprintf(&b, "  %s: repository(owner: $%sOwner, name: $%sRepo) {\n    nameWithOwner\n"+
			"    issueOrPullRequest(number: $%sNumber) { __typename ... on Issue { id title state "+
			"blocking(first: %d) { nodes { id } } blockedBy(first: %d) { nodes { id } } } }\n  }\n",
			a, a, a, a, BlockingWindow, BlockingWindow)
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
// order, each Ref's repository as GitHub spells it. A link whose field is
// null or absent is Missing.
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
			NameWithOwner string `json:"nameWithOwner"`
			Issue         *struct {
				Typename  string  `json:"__typename"`
				ID        string  `json:"id"`
				Title     string  `json:"title"`
				State     string  `json:"state"`
				Blocking  idNodes `json:"blocking"`
				BlockedBy idNodes `json:"blockedBy"`
			} `json:"issueOrPullRequest"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		if v != nil {
			l.Ref.Repo = canonical(l.Ref.Repo, v.NameWithOwner)
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
		l.blocking, l.blockedBy = v.Issue.Blocking.ids(), v.Issue.BlockedBy.ids()
	}
	return out, nil
}

// idNodes is a connection read as { nodes { id } }.
type idNodes struct {
	Nodes []*struct {
		ID string `json:"id"`
	} `json:"nodes"`
}

func (c idNodes) ids() []string {
	var out []string
	for _, n := range c.Nodes {
		if n != nil && n.ID != "" {
			out = append(out, n.ID)
		}
	}
	return out
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

// LinksError is the error for linked issues that cannot be blocked by,
// with exit code 3: missing ones and pull requests, all named at once by
// what (such as "--by"); nil when there are none. def is the default
// repository, for the refs and the message. "gh dojo list" is the fix
// only when every issue named is in def: it lists def's board, which
// cannot help with another repository's numbers.
//
//	--by #98, #99: no such issue in Trum-ok/gh-dojo, or the token cannot see it; #40: a pull request, not an issue; only issues can block
func LinksError(what string, links []*Linked, def Repo) error {
	var missing, prs []string
	where := " in " + def.String() // all missing ones are in def
	fix := ListFix
	for _, l := range links {
		if !l.Missing && !l.PullRequest {
			continue
		}
		if !sameRepo(l.Ref.Repo, def) {
			fix = ""
		}
		if l.PullRequest {
			prs = append(prs, l.Ref.String(def))
			continue
		}
		missing = append(missing, l.Ref.String(def))
		if !sameRepo(l.Ref.Repo, def) {
			where = ""
		}
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("%s: no such issue%s, or the token cannot see it", strings.Join(missing, ", "), where))
	}
	switch len(prs) {
	case 0:
	case 1:
		parts = append(parts, prs[0]+": a pull request, not an issue; only issues can block")
	default:
		parts = append(parts, strings.Join(prs, ", ")+": pull requests, not issues; only issues can block")
	}
	if len(parts) == 0 {
		return nil
	}
	e := render.NotFound("%s %s", what, strings.Join(parts, "; "))
	if fix != "" {
		e = e.WithFix(fix)
	}
	return e
}

// Target is the issue gh dojo block and unblock change.
type Target struct {
	Repo      Repo
	Number    int
	ID        string // node ID; never print it
	Title     string
	blockedBy []string // node IDs of its blockers, all of them (see BlockingWindow)
}

// BlockedBy reports whether the linked issue blocks the target: the
// blocked-by link is in place. The target's blockers are read whole, so
// unlike Linked.Blocks the answer holds however many issues l blocks.
func (t *Target) BlockedBy(l *Linked) bool { return l.ID != "" && slices.Contains(t.blockedBy, l.ID) }

// linksHead and linksTarget are the parts of the query ReadLinks sends.
const (
	linksHead   = `query IssueLinks($owner: String!, $repo: String!, $number: Int!`
	linksTarget = `) {
  repository(owner: $owner, name: $repo) {
    nameWithOwner
    issueOrPullRequest(number: $number) { __typename ... on Issue { id title blockedBy(first: 50) { nodes { id } } } }
  }
`
)

// ReadLinks reads issue number of repo with its blockers, and the issues
// refs name (see Links), in one request: what gh dojo block and unblock
// need. Target.BlockedBy tells whether a link is in place, Linked.BlockedBy
// whether adding it would close a cycle. A missing repository or issue, or a pull request number,
// is exit code 3; so are missing or pull request refs (see LinksError,
// with what naming them, and their refs written relative to def, the
// default repository). The links come back in the order of refs.
func ReadLinks(ctx context.Context, q graph.Querier, repo Repo, number int, def Repo, refs []Ref, what string) (*Target, []*Linked, error) {
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
			NameWithOwner string `json:"nameWithOwner"`
			Issue         *struct {
				Typename  string  `json:"__typename"`
				ID        string  `json:"id"`
				Title     string  `json:"title"`
				BlockedBy idNodes `json:"blockedBy"`
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
	if err := LinksError(what, links, def); err != nil {
		return nil, nil, err
	}
	is := resp.Repository.Issue
	t := &Target{Repo: canonical(repo, resp.Repository.NameWithOwner), Number: number, ID: is.ID, Title: is.Title,
		blockedBy: is.BlockedBy.ids()}
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
