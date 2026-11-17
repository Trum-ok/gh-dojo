package issue

import (
	"context"
	"errors"
	"fmt"

	"github.com/Trum-ok/gh-dojo/internal/graph"
	"github.com/Trum-ok/gh-dojo/internal/render"
)

// NotAssignable is the error, made by newError (a usage or a config
// error), for logins that cannot be assigned issues in repo, as where
// names them: the flag or the config key.
func NotAssignable(newError func(string, ...any) *render.Error, where string, logins []string, repo Repo) *render.Error {
	fix := fmt.Sprintf("check the login, or give %s access to %s", logins[0], repo)
	if len(logins) > 1 {
		fix = "check the logins, or give them access to " + repo.String()
	}
	return newError("%s: %s cannot be assigned issues in %s", where, render.JoinWords(logins, "and"), repo).WithFix(fix)
}

// assignableQuery reads who can be assigned issues in a repository, for an
// allowed list.
const assignableQuery = `query AssignableUsers($owner: String!, $repo: String!) {
  repository(owner: $owner, name: $repo) {
    assignableUsers(first: 100) { nodes { login } }
  }
}`

// ReadAssignable reads up to 100 logins that can be assigned in repo, for
// an allowed list.
func ReadAssignable(ctx context.Context, q graph.Querier, repo Repo) ([]string, error) {
	var resp struct {
		Repository *struct {
			AssignableUsers struct {
				Nodes []*struct {
					Login string `json:"login"`
				} `json:"nodes"`
			} `json:"assignableUsers"`
		} `json:"repository"`
	}
	if err := q.Query(ctx, assignableQuery, map[string]any{"owner": repo.Owner, "repo": repo.Name}, &resp); err != nil {
		return nil, err
	}
	if resp.Repository == nil {
		return nil, errors.New("no repository")
	}
	var logins []string
	for _, u := range resp.Repository.AssignableUsers.Nodes {
		if u != nil {
			logins = append(logins, u.Login)
		}
	}
	return logins, nil
}
