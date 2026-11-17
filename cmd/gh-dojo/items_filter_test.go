package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

// The gh-dojo board recorded twice at once (#94): read with GitHub's
// filter, and every item as before the filter (everyItem).
const (
	filteredDojoFixture = "testdata/graphql/filtered_dojo.json"
	everyDojoFixture    = "testdata/graphql/filtered_dojo_every.json"
)

// filteredRun is a run of list or next and the requests it takes with
// GitHub's filter, the schema already cached.
type filteredRun struct {
	args     []string
	requests int
}

// recordedRuns are list and next with each filter, on the recorded board.
var recordedRuns = []filteredRun{
	{[]string{"list"}, 1},
	{[]string{"list", "--json"}, 1},
	{[]string{"list", "--mine"}, 1},
	{[]string{"list", "--label", "type: dogfood"}, 1},
	{[]string{"list", "--label", "TYPE: ENHANCEMENT", "--label", "question", "--limit", "2"}, 1},
	{[]string{"list", "--status", "none", "--status", "Ready"}, 1},
	{[]string{"list", "--status", "Backlog", "--mine"}, 1},
	{[]string{"next"}, 1},
	{[]string{"next", "--all", "--json"}, 1},
	{[]string{"next", "--mine"}, 1},
	{[]string{"next", "--label", "question", "--limit", "1"}, 1},
	// No issue has both labels: read again unfiltered, as checkLabels
	// needs every issue's labels.
	{[]string{"next", "--label", "type: dogfood", "--label", "question"}, 2},
	{[]string{"list", "--label", "type: dogfod"}, 2},
}

// TestFilteredSameAsEvery: list and next print the same reading only the
// items GitHub's filter matches, with the counts of the rest, as reading
// every item; one request each after the schema.
func TestFilteredSameAsEvery(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, filteredDojoFixture)
	var filtered []string
	for i, r := range recordedRuns {
		before := srv.Requests()
		_, out := runOut(r.args...)
		filtered = append(filtered, string(out))
		want := r.requests
		if i == 0 {
			want++ // the schema, cold
		}
		if n := srv.Requests() - before; n != want {
			t.Errorf("%q: %d requests, want %d", r.args, n, want)
		}
	}
	everyItem(t)
	withApp(t, everyDojoFixture)
	var all bytes.Buffer
	for i, r := range recordedRuns {
		_, out := runOut(r.args...)
		if string(out) != filtered[i] {
			t.Errorf("%q: filtered prints\n%s\nevery item prints\n%s", r.args, filtered[i], out)
		}
		fmt.Fprintf(&all, "$ gh dojo %s\n%s\n", strings.Join(r.args, " "), out)
	}
	testutil.Golden(t, "list/filtered_recorded", all.Bytes())
}

// TestListFiltered: what list's filter leaves out is counted as when
// every item is read; when it cannot be read with a filter, every item
// is. Golden files list/<name> are shared with TestList where both read
// the same board.
func TestListFiltered(t *testing.T) {
	tests := []struct {
		name     string
		fixtures []string
		warm     bool
		args     []string
		code     render.Code
		requests int // the schema cold, unless warm
		golden   string
	}{
		// 2 issues read; counted: 1 that passes the filters but is closed
		// or Done, 3 that do not, a draft, 2 pull requests and an unseen
		// item. #2's labels were not all read, but GitHub matched bug:
		// no "not fully checked" line.
		{"counts", []string{boardFixture, "testdata/graphql/list_filtered.json"}, false,
			[]string{"--mine", "--label", "bug"}, render.ExitOK, 2, "filtered_counts"},
		// Icebox is not in the cached schema: the schema is read again,
		// then the items with a filter built from it.
		{"stale", []string{boardFixture, "testdata/graphql/list_filtered_stale.json"}, true,
			nil, render.ExitOK, 3, "filtered_stale"},
		// No issue has the label: every item is read to name it.
		{"label_typo", []string{boardFixture, "testdata/graphql/list_filtered_label.json", listLabelsFixture}, false,
			[]string{"--label", "bgu"}, render.ExitUsage, 3, "label_typo"},
		// A label the filter cannot spell, --all and a final status read
		// every item.
		{"unspelled", []string{boardFixture, listLabelsFixture}, false,
			[]string{"--label", "wont,fix"}, render.ExitUsage, 2, "filtered_unspelled"},
		{"all", []string{boardFixture, itemsTwoPagesFixture}, false, []string{"--all"}, render.ExitOK, 3, "all"},
		{"final_status", []string{boardFixture, itemsTwoPagesFixture}, false,
			[]string{"--status", "Done"}, render.ExitOK, 3, "final_status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			srv := withApp(t, tt.fixtures...)
			before := 0
			if tt.warm {
				before = warmSchema(t, srv)
			}
			code, out := runOut(append([]string{"list"}, tt.args...)...)
			if code != tt.code {
				t.Errorf("code = %d, want %d\n%s", code, tt.code, out)
			}
			testutil.Golden(t, "list/"+tt.golden, out)
			if n := srv.Requests() - before; n != tt.requests {
				t.Errorf("requests = %d, want %d", n, tt.requests)
			}
		})
	}
}

// TestListSprintFiltered: --sprint is a term of the filter, the
// iteration named by its title: the output of sprint_items.json read
// whole (TestSprintList), one request each.
func TestListSprintFiltered(t *testing.T) {
	srv, before := withSprintApp(t, time.Time{}, "testdata/graphql/sprint_filtered.json")
	for _, sprint := range []string{"current", "none"} {
		code, out := runOut("list", "--sprint", sprint)
		if code != render.ExitOK {
			t.Errorf("--sprint %s: exit code = %d\n%s", sprint, code, out)
		}
		if n := srv.Requests() - before; n != 1 {
			t.Errorf("--sprint %s: %d requests, want 1", sprint, n)
		}
		before = srv.Requests()
		testutil.Golden(t, "list/sprint_"+sprint, out)
	}
}

// TestNextFiltered: next reads each blocker with its place on the board,
// so none needs to be among the items read, and counts what the filter
// left out. One request each, the schema cached.
func TestNextFiltered(t *testing.T) {
	withConfig(t, dojoConfig)
	srv := withApp(t, boardFixture, "testdata/graphql/next_filtered.json")
	before := warmSchema(t, srv)
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"filtered", nil},
		{"filtered_all", []string{"--all", "--limit", "20"}},
	} {
		code, out := runOut(append([]string{"next"}, tt.args...)...)
		if code != render.ExitOK {
			t.Errorf("%q: exit code = %d\n%s", tt.args, code, out)
		}
		testutil.Golden(t, "next/"+tt.name, out)
	}
	code, out := runJSON(t, "next", "--all", "--json")
	if code != render.ExitOK {
		t.Errorf("--json: exit code = %d\n%s", code, out)
	}
	testutil.Golden(t, "json/next_filtered", out)
	if n := srv.Requests() - before; n != 3 {
		t.Errorf("%d requests, want 3", n)
	}
}
