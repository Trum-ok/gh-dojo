package issue_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/issue"
)

// TestReadLookup reads issue #3 of the gh-dojo repo with the lookups of a
// verb that adds an existing and a missing label, assigns Trum-ok and
// "@me": all in the card's one request.
func TestReadLookup(t *testing.T) {
	srv := graphtest.New(t, fixture("card_3_lookup"))
	req := dojoStatus
	req.Number = 3
	req.Lookup = issue.Lookup{Labels: []string{"enhancement", "no-such-label"}, User: "trum-ok", Viewer: true}
	card, err := issue.Read(context.Background(), srv.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want exactly 1", n)
	}
	f := card.Found
	switch {
	case f == nil:
		t.Fatal("no Found")
	case len(f.Labels) != 1 || !strings.HasPrefix(f.Labels["enhancement"], "LA_"):
		t.Errorf("labels = %v, want enhancement only", f.Labels)
	case f.User == nil || f.User.Login != "Trum-ok" || f.User.ID == "":
		t.Errorf("user = %+v, want Trum-ok", f.User)
	case f.Viewer == nil || f.Viewer.Login != "Trum-ok" || f.Viewer.ID != f.User.ID:
		t.Errorf("viewer = %+v, want Trum-ok", f.Viewer)
	}
	if card.Number != 3 || card.Status() != "Ready" {
		t.Errorf("card #%d in %q", card.Number, card.Status())
	}
}

// TestReadLookupEmpty: without a lookup the card query is the plain one,
// so recorded card fixtures keep matching, and Found is nil.
func TestReadLookupEmpty(t *testing.T) {
	card, err := read(t, "card_3", 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if card.Found != nil {
		t.Errorf("Found = %+v, want nil", card.Found)
	}
}

// TestReadAllLookup: a lookup goes with a single issue only.
func TestReadAllLookup(t *testing.T) {
	req := dojoStatus
	req.Lookup = issue.Lookup{Viewer: true}
	if _, err := issue.ReadAll(context.Background(), nil, req, []int{1, 2}); err == nil {
		t.Error("ReadAll of two issues with a lookup: want an error")
	}
}

// queryRecorder is a graph.Querier that keeps the query and fails it.
type queryRecorder struct{ query string }

func (q *queryRecorder) Query(_ context.Context, query string, _ map[string]any, _ any) error {
	q.query = query
	return errors.New("not sent")
}

// TestReadLookupAllLabels: a verb that removes labels reads up to 100 of
// the issue's labels, so one beyond the card's 20 is not taken for absent.
func TestReadLookupAllLabels(t *testing.T) {
	for _, tt := range []struct {
		lookup issue.Lookup
		want   string
	}{
		{issue.Lookup{Labels: []string{"blocked"}, AllLabels: true}, "labels(first: 100)"},
		{issue.Lookup{Labels: []string{"blocked"}}, "labels(first: 20)"},
	} {
		q := &queryRecorder{}
		req := dojoStatus
		req.Number = 3
		req.Lookup = tt.lookup
		_, _ = issue.Read(context.Background(), q, req)
		if n := strings.Count(q.query, "labels(first: "); n != 1 || !strings.Contains(q.query, tt.want) {
			t.Errorf("AllLabels=%v: the query has %d label windows, want one %s", tt.lookup.AllLabels, n, tt.want)
		}
	}
}
