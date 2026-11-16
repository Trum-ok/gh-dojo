package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// repo is a repository doctor checks, with what the Doctor query found.
type repo struct {
	Repo    issue.Repo
	Default bool
	Current bool // not from board.repos: GH_REPO or the git remote
	Found   *struct {
		NameWithOwner    string `json:"nameWithOwner"`
		HasIssuesEnabled bool   `json:"hasIssuesEnabled"`
	}
	Err error // GitHub denied reading it, e.g. its organization's SAML SSO
}

// usable reports whether the repo check found rp usable.
func (rp *repo) usable() bool {
	return rp.Err == nil && rp.Found != nil && rp.Found.HasIssuesEnabled
}

// listRepos lists the repositories to check: board.repos, else the
// current repository. An error means there is no repository to use.
func listRepos(cfg *config.Config, current func() (issue.Repo, error)) ([]*repo, error) {
	if cfg == nil {
		return nil, nil
	}
	if len(cfg.Board.Repos) == 0 {
		r, err := current()
		if err != nil {
			return nil, err
		}
		return []*repo{{Repo: r, Default: true, Current: true}}, nil
	}
	var repos []*repo
	for i, name := range cfg.Board.Repos {
		r, err := issue.ParseRepo(name)
		if err != nil {
			return nil, err
		}
		repos = append(repos, &repo{Repo: r, Default: i == 0})
	}
	return repos, nil
}

// tokenQuery is the Doctor query: the viewer and each repository, aliased
// repo0, repo1... GitHub lists the token's scopes in the response headers.
func tokenQuery(repos []*repo) (string, map[string]any) {
	if len(repos) == 0 {
		return "query Doctor { viewer { login } }", nil
	}
	params := make([]string, len(repos))
	fields := make([]string, len(repos))
	vars := map[string]any{}
	for i, r := range repos {
		params[i] = fmt.Sprintf("$owner%d: String!, $name%d: String!", i, i)
		fields[i] = fmt.Sprintf("  repo%d: repository(owner: $owner%d, name: $name%d) { nameWithOwner hasIssuesEnabled }", i, i, i)
		vars[fmt.Sprintf("owner%d", i)] = r.Repo.Owner
		vars[fmt.Sprintf("name%d", i)] = r.Repo.Name
	}
	return "query Doctor(" + strings.Join(params, ", ") + ") {\n  viewer { login }\n" +
		strings.Join(fields, "\n") + "\n}", vars
}

// checkToken sends the Doctor query, checks the token's scopes and fills
// what it found about repos. It reports whether GitHub answered, so the
// checks that need it can run, and the authenticated user's login ("" when
// not read).
func checkToken(ctx context.Context, r *Report, q graph.Querier, repos []*repo) (bool, string) {
	c, ok := q.(TokenClient)
	if !ok {
		r.AddSkip("token", "this GitHub client cannot report token scopes")
		return true, ""
	}
	query, vars := tokenQuery(repos)
	var out map[string]json.RawMessage
	h, err := c.QueryHeaders(ctx, query, vars, &out)
	failed, partial := repoErrors(err)
	if err != nil && !partial {
		r.AddProblem("token", err)
		return false, ""
	}
	var viewer struct{ Login string }
	_ = json.Unmarshal(out["viewer"], &viewer)
	for i, rp := range repos {
		_ = json.Unmarshal(out[fmt.Sprintf("repo%d", i)], &rp.Found) // null: not found
		if failed[i] {
			// SAML SSO or another denial: the client mapped it, with the
			// fix for where the token comes from.
			rp.Err = err
		}
	}

	who := c.Host() + " as " + viewer.Login
	if src := c.TokenEnv(); src != "" {
		who += " (token from " + src + ")"
	}
	scopes, listed := graph.Scopes(h)
	switch {
	case !listed:
		r.AddOK("token", "%s; no scopes listed (a fine-grained or app token): the board check shows its access", who)
	case slices.Contains(scopes, graph.ScopeProject):
		r.AddOK("token", "%s, with the project scope", who)
	case slices.Contains(scopes, graph.ScopeReadProject):
		r.AddProblem("token", render.Network("%s: the token has read:project but not project, so commands that change the board fail", who).
			WithFix(c.AuthFix()))
	default:
		has := "none"
		if len(scopes) > 0 {
			has = strings.Join(scopes, ", ")
		}
		r.AddProblem("token", render.Network("%s: the token lacks the project scope (has: %s)", who, has).WithFix(c.AuthFix()))
	}
	return true, viewer.Login
}

// repoErrors reads a GraphQL answer whose errors are all about repository
// fields (repo0, repo1...): the viewer and the other repositories are in
// place. partial is false for any other error. failed has the index of
// each repository with an error other than NOT_FOUND, such as an
// organization's SAML SSO denying the token; a NOT_FOUND one is null in
// the data, which reads as not visible.
func repoErrors(err error) (failed map[int]bool, partial bool) {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || len(gqlErr.Errors) == 0 {
		return nil, false
	}
	failed = map[int]bool{}
	for _, item := range gqlErr.Errors {
		var i int
		if len(item.Path) == 0 {
			return nil, false
		}
		alias, _ := item.Path[0].(string)
		if _, serr := fmt.Sscanf(alias, "repo%d", &i); serr != nil || alias != fmt.Sprintf("repo%d", i) {
			return nil, false
		}
		if item.Type != "NOT_FOUND" {
			failed[i] = true
		}
	}
	return failed, true
}

// checkRepo reports what the Doctor query found about one repository.
func checkRepo(r *Report, cfg *config.Config, rp *repo) {
	name := rp.Repo.String()
	if rp.Default {
		name += " (default)"
	}
	switch {
	case rp.Err != nil:
		r.AddProblem("repo", named(name, rp.Err))
	case rp.Found == nil && rp.Current:
		r.AddProblem("repo", render.Usage("%s, the current repository, is not found or not visible to this token", name).
			WithFix(`add repos = ["owner/name"] under [board] in `+cfg.Display))
	case rp.Found == nil:
		r.AddProblem("repo", render.Usage("%s is not found or not visible to this token", name).
			WithFix("edit board.repos in "+cfg.Display))
	case !rp.Found.HasIssuesEnabled:
		r.AddProblem("repo", render.Usage("%s has issues disabled", name).
			WithFix("gh repo edit "+rp.Found.NameWithOwner+" --enable-issues"))
	case rp.Current:
		r.AddOK("repo", "%s, the current repository: visible, issues enabled", name)
	case rp.Found.NameWithOwner != "" && rp.Found.NameWithOwner != rp.Repo.String():
		// GitHub matches names without case, so it works; output names
		// the default repository as the config spells it.
		r.AddWarn("repo", fmt.Sprintf("write it as %q in board.repos in %s", rp.Found.NameWithOwner, cfg.Display),
			"%s: visible, issues enabled; GitHub spells it %s", name, rp.Found.NameWithOwner)
	default:
		r.AddOK("repo", "%s: visible, issues enabled", name)
	}
}
