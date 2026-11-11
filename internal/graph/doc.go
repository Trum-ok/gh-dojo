// Package graph is the GitHub GraphQL client: auth through gh (or
// GH_TOKEN), retries for queries, errors mapped to internal/render exit
// codes, and pagination that keeps totalCount.
//
// Queries are raw GraphQL strings with a named operation, decoded into
// structs with json tags:
//
//	c := graph.New(graph.Options{})
//	var out struct {
//		Viewer struct{ Login string } `json:"viewer"`
//	}
//	err := c.Do(ctx, `query Viewer { viewer { login } }`, nil, &out)
//
// Tests use package graphtest for recorded responses.
package graph
