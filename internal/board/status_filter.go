package board

import (
	"context"
	"slices"

	"github.com/Trum-ok/gh-dojo/internal/graph"
)

// ReadInStatus reads the issues of the board s whose statusField is one
// of statuses (open and closed, archived items left out), in board order,
// filtered by GitHub (ReadItemsWith with Filter): ceil(matching/100)
// requests, one as a rule. A status the filter has no checked spelling
// for (see FieldTerm) reads every item instead. Only issues in exactly one
// of statuses are returned, whatever the filter matched; items the token
// cannot see are left out. Errors are ReadItemsWith's.
func ReadInStatus(ctx context.Context, q graph.Querier, s *Schema, statusField string, statuses []string) ([]Item, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	var o ItemsOptions
	if t, ok := FieldTerm(statusField, statuses, false); ok {
		o.Filter = Filter{FilterIssues, t}.String()
	}
	items, err := ReadItemsWith(ctx, q, s, statusField, o)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, it := range items.Items {
		if it.Type == ContentIssue && slices.Contains(statuses, it.Status) {
			out = append(out, it)
		}
	}
	return out, nil
}
