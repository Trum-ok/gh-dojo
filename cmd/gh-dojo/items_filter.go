package main

import (
	"slices"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/board"
	"github.com/Trum-ok/gh-dojo/internal/config"
)

// itemsFilter is how list and next read the board without its archive
// (#94): only the items filter matches, those they may show, and in the
// same request the counts of the items GitHub's filters match, to count
// what the filter left out (principle 7). A nil itemsFilter reads every
// item and counts them all locally.
type itemsFilter struct {
	filter board.Filter
	counts []string       // the filters counted, each once
	index  map[string]int // what a count is ("issues") → its index in counts
}

// readEveryItem makes list and next read every item, as list --all does:
// tests of how they sort a board's items out set it.
var readEveryItem = false

// newItemsFilter reads the items read matches and counts, in scope, every
// item, the issues, the draft issues and the pull requests, for
// leftOut.addSkipped.
func newItemsFilter(scope board.Filter, read board.Filter) *itemsFilter {
	q := &itemsFilter{filter: read, index: map[string]int{}}
	q.count("all", scope)
	q.count("issues", scope.And(board.FilterIssues))
	q.count("drafts", scope.And(board.FilterDrafts))
	q.count("pulls", scope.And(board.FilterPulls))
	return q
}

// count adds the count of the items f matches as name.
func (q *itemsFilter) count(name string, f board.Filter) {
	s := f.String()
	i := slices.Index(q.counts, s)
	if i < 0 {
		q.counts = append(q.counts, s)
		i = len(q.counts) - 1
	}
	q.index[name] = i
}

// get is the count name as read with items; ok is false when it was not
// counted.
func (q *itemsFilter) get(items *board.Items, name string) (n int, ok bool) {
	i, ok := q.index[name]
	if !ok || i >= len(items.Counts) {
		return 0, false
	}
	return items.Counts[i], true
}

// options are what ReadItemsWith reads for q.
func (q *itemsFilter) options(o *board.ItemsOptions) {
	o.Filter, o.Counts = q.filter.String(), q.counts
}

// unknownLabel reports whether no issue on the board has every --label:
// a typo checkLabels names only from every issue's labels, so the board
// is read again unfiltered.
func (q *itemsFilter) unknownLabel(items *board.Items) bool {
	n, ok := q.get(items, "labeled")
	return ok && n == 0
}

// userTerms are the filters list and next share, --mine, --sprint and
// --label, as filter terms, and the --label ones alone; ok is false when
// the filter cannot express one.
func (o *listOpts) userTerms(s *board.Schema, cfg *config.Config) (terms, labels board.Filter, ok bool) {
	if o.mine {
		terms = append(terms, board.FilterMine)
	}
	if o.sprint != "" {
		t, ok := sprintTerm(s, cfg, o.sprintIt)
		if !ok {
			return nil, nil, false
		}
		terms = append(terms, t)
	}
	for _, l := range o.labels {
		t, ok := board.LabelTerm(l)
		if !ok {
			return nil, nil, false
		}
		labels = append(labels, t)
	}
	return append(terms, labels...), labels, true
}

// sprintTerm matches the items in iteration it of the sprint field, or
// without one for nil. GitHub names an iteration by its title, so a title
// two iterations share is not ok.
func sprintTerm(s *board.Schema, cfg *config.Config, it *board.Iteration) (string, bool) {
	if it == nil {
		return board.NoValue(cfg.SprintField())
	}
	f, err := s.Field(cfg.SprintField())
	if err != nil {
		return "", false
	}
	same := 0
	for _, i := range f.Iterations {
		if strings.EqualFold(i.Title, it.Title) {
			same++
		}
	}
	if same != 1 {
		return "", false
	}
	return board.FieldTerm(cfg.SprintField(), []string{it.Title}, false)
}

// statusTerm matches the items in one of the statuses want, "" for no
// status, of the status field with options. GitHub has no OR of a value
// with no value, so with "" it is the negation of the other options; ""
// for a term that matches every item.
func statusTerm(field string, options, want []string) (string, bool) {
	if !slices.Contains(want, "") {
		return board.FieldTerm(field, want, false)
	}
	var others []string
	for _, o := range options {
		if !slices.Contains(want, o) {
			others = append(others, o)
		}
	}
	if len(others) == 0 {
		return "", true
	}
	return board.FieldTerm(field, others, true)
}

// addSkipped counts the draft issues, pull requests and items the token
// cannot see that q's filter left out of the read.
func (c *leftOut) addSkipped(q *itemsFilter, items *board.Items) {
	all, _ := q.get(items, "all")
	issues, _ := q.get(items, "issues")
	drafts, _ := q.get(items, "drafts")
	pulls, _ := q.get(items, "pulls")
	c.drafts = max(c.drafts, drafts)
	c.pulls = max(c.pulls, pulls)
	c.unseen += max(0, all-issues-drafts-pulls)
}
