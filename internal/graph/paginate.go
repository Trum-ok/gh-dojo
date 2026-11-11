package graph

import (
	"context"
	"errors"
	"maps"

	"github.com/Trum-ok/gh-dojo/internal/render"
)

// ErrParentNotFound is wrapped by the error Paginate returns when the
// connection's parent is missing (e.g. "user": null).
var ErrParentNotFound = errors.New("connection parent not found")

// PageInfo is the pageInfo object of a GraphQL connection.
type PageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// Connection is a GraphQL connection as GitHub returns it. Select it as
//
//	items(first: 100, after: $cursor) {
//	  totalCount
//	  pageInfo { hasNextPage endCursor }
//	  nodes { ... }
//	}
type Connection[T any] struct {
	TotalCount int      `json:"totalCount"`
	PageInfo   PageInfo `json:"pageInfo"`
	Nodes      []T      `json:"nodes"`
}

// Page is what Paginate collected: the nodes and the total the server
// reported, for "showing 50 of 73".
type Page[T any] struct {
	Nodes      []T
	TotalCount int
}

// Truncated reports whether fewer nodes were collected than exist.
func (p Page[T]) Truncated() bool { return len(p.Nodes) < p.TotalCount }

// Paginate runs query page by page and collects the nodes of one connection.
//
// The query must declare "$cursor: String" and pass it as "after: $cursor";
// Paginate sets vars["cursor"] (vars itself is not modified). R is the type
// of the response data and conn picks the connection out of it, returning
// nil when the connection's parent is missing: Paginate then fails with a
// not-found error (exit code 3) wrapping ErrParentNotFound, along with the
// nodes collected so far.
//
// limit > 0 stops once that many nodes are collected (one page may overshoot,
// the extra nodes are dropped); limit <= 0 collects everything. TotalCount
// comes from the server, so Page tells how much was left out.
//
//	type resp struct {
//		Node *struct {
//			Items graph.Connection[item] `json:"items"`
//		} `json:"node"`
//	}
//	page, err := graph.Paginate(ctx, c, q, vars, 200, func(r *resp) *graph.Connection[item] {
//		if r.Node == nil {
//			return nil
//		}
//		return &r.Node.Items
//	})
func Paginate[R, T any](ctx context.Context, q Querier, query string, vars map[string]any, limit int, conn func(*R) *Connection[T]) (Page[T], error) {
	var page Page[T]
	v := maps.Clone(vars)
	if v == nil {
		v = map[string]any{}
	}
	v["cursor"] = nil
	for {
		var r R
		if err := q.Query(ctx, query, v, &r); err != nil {
			return page, err
		}
		c := conn(&r)
		if c == nil {
			return page, render.NotFound("GitHub API: not found").WithCause(ErrParentNotFound)
		}
		page.TotalCount = c.TotalCount
		page.Nodes = append(page.Nodes, c.Nodes...)
		if limit > 0 && len(page.Nodes) >= limit {
			page.Nodes = page.Nodes[:limit]
			return page, nil
		}
		next := c.PageInfo.EndCursor
		if !c.PageInfo.HasNextPage || next == "" || next == v["cursor"] {
			return page, nil
		}
		v["cursor"] = next
	}
}
