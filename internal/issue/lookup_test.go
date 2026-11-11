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

// TestReadAllLookup reads #3 and #7 with the lookups of a verb in one
// request: the lookups are read once, and every card gets them. A link
// GitHub cannot find fails neither the batch nor a card.
func TestReadAllLookup(t *testing.T) {
	srv := graphtest.New(t, fixture("cards_3_7_lookup"))
	req := dojoStatus
	req.Lookup = issue.Lookup{
		Labels: []string{"enhancement", "no-such-label"}, User: "trum-ok", Viewer: true,
		Links: issue.Links{{Repo: dojo, Number: 2}, {Repo: dojo, Number: 9999}},
	}
	res, err := issue.ReadAll(context.Background(), srv.Client(), req, []int{3, 7})
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want exactly 1", n)
	}
	if len(res) != 2 || res[0].Card == nil || res[1].Card == nil {
		t.Fatalf("results %+v", res)
	}
	for _, r := range res {
		f := r.Card.Found
		switch {
		case f == nil:
			t.Fatalf("#%d: no Found", r.Number)
		case len(f.Labels) != 1 || !strings.HasPrefix(f.Labels["enhancement"], "LA_"):
			t.Errorf("#%d: labels = %v, want enhancement only", r.Number, f.Labels)
		case f.User == nil || f.User.Login != "Trum-ok" || f.Viewer == nil || f.Viewer.Login != "Trum-ok":
			t.Errorf("#%d: user %+v, viewer %+v", r.Number, f.User, f.Viewer)
		case len(f.Links) != 2 || f.Links[0].Missing || !f.Links[1].Missing:
			t.Errorf("#%d: links %+v", r.Number, f.Links)
		}
	}
	// #2 blocks both #3 and #7: the link is read once, the check is per card.
	if l := res[0].Card.Found.Links[0]; !l.Blocks(res[0].Card.ID) || !l.Blocks(res[1].Card.ID) || l.Blocks("I_other") {
		t.Errorf("#2 blocks #3: %v, #7: %v", l.Blocks(res[0].Card.ID), l.Blocks(res[1].Card.ID))
	}
}

// TestReadAllLookupQuery: the batch query reads the lookups once, below
// the aliased issues, and widens the labels and blockers windows.
func TestReadAllLookupQuery(t *testing.T) {
	q := &firstQuery{}
	req := dojoStatus
	req.Lookup = issue.Lookup{Labels: []string{"blocked"}, AllLabels: true, AllBlockers: true, Viewer: true}
	_, _ = issue.ReadAll(context.Background(), q, req, []int{1, 2})
	for _, want := range []string{"i1: issueOrPullRequest(number: $n1)", "label0: label(name: $label0)", "viewer { id login }", "labels(first: 100)", "blockedBy(first: 50)"} {
		if strings.Count(q.query, want) != 1 {
			t.Errorf("query has %d of %q, want 1:\n%s", strings.Count(q.query, want), want, q.query)
		}
	}
}

// queryRecorder is a graph.Querier that keeps the query and fails it.
type queryRecorder struct{ query string }

func (q *queryRecorder) Query(_ context.Context, query string, _ map[string]any, _ any) error {
	q.query = query
	return errors.New("not sent")
}

// firstQuery is a graph.Querier that keeps the first query and fails
// every one.
type firstQuery struct{ query string }

func (q *firstQuery) Query(_ context.Context, query string, _ map[string]any, _ any) error {
	if q.query == "" {
		q.query = query
	}
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
