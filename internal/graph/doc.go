// Package graph is the GitHub GraphQL client: auth through gh (or
// GH_TOKEN), retries for queries, errors mapped to internal/render exit
// codes, and pagination that keeps totalCount.
//
// Operations are raw GraphQL strings with a named operation first in the
// document, decoded into structs with json tags:
//
//	c := graph.New(graph.Options{})
//	var out struct {
//		Viewer struct{ Login string } `json:"viewer"`
//	}
//	err := c.Query(ctx, `query Viewer { viewer { login } }`, nil, &out)
//
// Query retries; Mutate never does. When IsStaleID(err) holds, an ID from
// the cached board schema is out of date: invalidate the cache, re-read and
// try once more.
//
// Tests use package graphtest for recorded responses.
package graph
