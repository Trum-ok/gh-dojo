package issue_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/issue"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
)

func readAll(t *testing.T, file string, numbers ...int) ([]issue.Result, error) {
	t.Helper()
	srv := graphtest.New(t, fixture(file))
	res, err := issue.ReadAll(context.Background(), srv.Client(), issue.Request{
		Repo: dojo, Board: dojoBoard, StatusField: "Status",
	}, numbers)
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want exactly 1", n)
	}
	return res, err
}

func dumpResults(res []issue.Result) []byte {
	var b strings.Builder
	for _, r := range res {
		fmt.Fprintf(&b, "== %s\n", r.Ref().String(dojo))
		if r.Err != nil {
			e := render.FromError(r.Err)
			fmt.Fprintf(&b, "code %d\n%s\n", e.Code, e.Text())
			continue
		}
		b.Write(dump(r.Card))
	}
	return []byte(b.String())
}

// TestReadAllRecorded reads issues #3 and #7 of gh-dojo and the missing
// #9999 in one request: the missing one fails alone.
func TestReadAllRecorded(t *testing.T) {
	res, err := readAll(t, "cards_3_7_9999", 3, 7, 9999)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "batch/cards_3_7_9999", dumpResults(res))
	if len(res) != 3 || res[0].Card == nil || res[1].Card == nil || res[2].Err == nil {
		t.Fatalf("results %+v", res)
	}
	if res[0].Card.Number != 3 || res[1].Card.Number != 7 || !res[0].Card.OnBoard() {
		t.Errorf("cards %d, %d", res[0].Card.Number, res[1].Card.Number)
	}
	if render.FromError(res[2].Err).Code != render.ExitNotFound || res[2].Cause != issue.CauseMissing {
		t.Errorf("missing: %v", res[2].Err)
	}
}

// TestReadRefsRepos reads issues of three repositories in one request:
// gh-dojo #3, cli/cli #2 (an issue) and #1 (a pull request), and #1 of a
// repository that does not exist, which fails alone. The bare #3 is in
// the default repository.
func TestReadRefsRepos(t *testing.T) {
	srv := graphtest.New(t, fixture("cards_repos"))
	cli := issue.Repo{Owner: "cli", Name: "cli"}
	gone := issue.Repo{Owner: "Trum-ok", Name: "no-such-repo-xyz"}
	res, err := issue.ReadRefs(context.Background(), srv.Client(), issue.Request{
		Repo: dojo, Board: dojoBoard, StatusField: "Status",
	}, []issue.Ref{{Number: 3}, {Repo: cli, Number: 2}, {Repo: gone, Number: 1}, {Repo: cli, Number: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.Requests(); n != 1 {
		t.Errorf("requests = %d, want exactly 1", n)
	}
	testutil.Golden(t, "batch/cards_repos", dumpResults(res))
	if len(res) != 4 || res[0].Card == nil || res[1].Card == nil {
		t.Fatalf("results %+v", res)
	}
	if res[0].Card.Repo != dojo || res[0].Card.Number != 3 || res[1].Card.Repo != cli || res[1].Card.Number != 2 {
		t.Errorf("cards %v, %v", res[0].Card.Ref(), res[1].Card.Ref())
	}
	if res[2].Cause != issue.CauseNoRepo || res[2].Repo != gone || !strings.Contains(res[2].Err.Error(), "no-such-repo-xyz not found") {
		t.Errorf("missing repository: %+v", res[2])
	}
	if res[3].Cause != issue.CausePullRequest || res[3].Repo != cli {
		t.Errorf("pull request: %+v", res[3])
	}
}

// cannedQuerier answers every query with data, as GitHub's "data" object.
type cannedQuerier struct{ data string }

func (q cannedQuerier) Query(_ context.Context, _ string, _ map[string]any, out any) error {
	return json.Unmarshal([]byte(q.data), out)
}

// TestReadRefsLookupPerRepo: labels are looked up in each repository of a
// batch, and each card gets its own repository's: "blocked" exists in
// gh-dojo and not in acme/api. A repository written in another case is
// named as GitHub spells it, from the issue's URL.
func TestReadRefsLookupPerRepo(t *testing.T) {
	card := func(n int, url string) string {
		return fmt.Sprintf(`{"__typename": "Issue", "id": "I_%d", "number": %d, "title": "T", "state": "OPEN", "url": %q,
			"labels": {"totalCount": 0, "nodes": []}, "assignees": {"totalCount": 0, "nodes": []},
			"comments": {"totalCount": 0, "nodes": []}, "blockedBy": {"totalCount": 0, "nodes": []},
			"blocking": {"totalCount": 0, "nodes": []}, "projectItems": {"totalCount": 0, "nodes": []},
			"timelineItems": {"totalCount": 0, "nodes": []}}`, n, n, url)
	}
	q := cannedQuerier{`{
		"repository": {"i0": ` + card(3, "https://github.com/Trum-ok/gh-dojo/issues/3") + `, "label0": {"id": "LA_dojo", "name": "blocked"}},
		"r1": {"i1": ` + card(7, "https://github.com/acme/api/issues/7") + `, "i2": ` + card(8, "https://github.com/acme/api/issues/8") + `, "label0": null}
	}`}
	req := dojoStatus
	req.Lookup = issue.Lookup{Labels: []string{"blocked"}}
	res, err := issue.ReadRefs(context.Background(), q, req,
		[]issue.Ref{{Number: 3}, {Repo: issue.Repo{Owner: "ACME", Name: "Api"}, Number: 7}, {Repo: issue.Repo{Owner: "acme", Name: "API"}, Number: 8}})
	if err != nil {
		t.Fatal(err)
	}
	if got := res[0].Card.Found.Labels["blocked"]; got != "LA_dojo" {
		t.Errorf("gh-dojo#3: label blocked = %q, want LA_dojo", got)
	}
	for _, r := range res[1:] {
		if _, ok := r.Card.Found.Labels["blocked"]; ok {
			t.Errorf("%s: label blocked found, want missing in acme/api", r.Card.Ref().String(dojo))
		}
		if r.Card.Repo.String() != "acme/api" || r.Repo.String() != "acme/api" {
			t.Errorf("repo = %s, result %s; want acme/api as GitHub spells it", r.Card.Repo, r.Repo)
		}
	}
}

// TestReadRefsQuery: one repository reads as a batch always did; issues
// of another one go below its own aliased repository field, with the
// lookups of a repository repeated there and the others once.
func TestReadRefsQuery(t *testing.T) {
	q := &firstQuery{}
	req := dojoStatus
	req.Lookup = issue.Lookup{Labels: []string{"blocked"}, Viewer: true}
	acme := issue.Repo{Owner: "acme", Name: "api"}
	_, _ = issue.ReadRefs(context.Background(), q, req, []issue.Ref{{Number: 1}, {Repo: acme, Number: 7}, {Repo: dojo, Number: 2}})
	for _, want := range []string{
		"$n2: Int!, $owner1: String!, $repo1: String!, $label0: String!",
		"  repository(owner: $owner, name: $repo) {\n    i0: issueOrPullRequest(number: $n0) { ...card }\n    i2: issueOrPullRequest(number: $n2) { ...card }\n    label0:",
		"  r1: repository(owner: $owner1, name: $repo1) {\n    i1: issueOrPullRequest(number: $n1) { ...card }\n    label0:",
	} {
		if strings.Count(q.query, want) != 1 {
			t.Errorf("query has %d of %q, want 1:\n%s", strings.Count(q.query, want), want, q.query)
		}
	}
	if strings.Count(q.query, "viewer { id login }") != 1 {
		t.Errorf("viewer read %d times:\n%s", strings.Count(q.query, "viewer { id login }"), q.query)
	}
	if q.vars["owner1"] != "acme" || q.vars["repo1"] != "api" || q.vars["owner"] != "Trum-ok" || q.vars["n1"] != 7 {
		t.Errorf("vars = %v", q.vars)
	}
}

// TestReadAllMixed covers a pull request number and a card GitHub withheld
// part of, in one batch.
func TestReadAllMixed(t *testing.T) {
	res, err := readAll(t, "cards_mixed", 42, 15)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, "batch/mixed", dumpResults(res))
	if res[0].Card == nil || !res[0].Card.Incomplete || res[0].Card.OnBoard() || !res[0].Card.BoardUnknown {
		t.Errorf("partial card %+v", res[0])
	}
	if res[1].Err == nil || res[1].Cause != issue.CausePullRequest || !strings.Contains(res[1].Err.Error(), "pull request") {
		t.Errorf("pull request: %v (cause %q)", res[1].Err, res[1].Cause)
	}
}

func numbers(from, to int) []int {
	var ns []int
	for n := from; n <= to; n++ {
		ns = append(ns, n)
	}
	return ns
}

// TestReadAllChunks reads 21 issues in two batches; the second times out
// as a whole, so its issue is read again on its own.
func TestReadAllChunks(t *testing.T) {
	srv := graphtest.New(t, fixture("cards_21_fallback"))
	res, err := issue.ReadAll(context.Background(), srv.Client(), issue.Request{
		Repo: dojo, Board: dojoBoard, StatusField: "Status",
	}, numbers(1, 21))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 21 {
		t.Fatalf("%d results, want 21", len(res))
	}
	for i, r := range res {
		if r.Err != nil || r.Card == nil || r.Card.Number != i+1 || r.Number != i+1 {
			t.Errorf("result %d: %+v", i, r)
		}
	}
	if n := srv.Requests(); n != 3 {
		t.Errorf("requests = %d, want 3: two batches, then #21 alone", n)
	}
}

// TestReadAllLaterChunkFails: the second batch and the single read after
// it fail; the first batch's cards are returned with the error.
func TestReadAllLaterChunkFails(t *testing.T) {
	srv := graphtest.New(t, fixture("cards_21_down"))
	res, err := issue.ReadAll(context.Background(), srv.Client(), issue.Request{
		Repo: dojo, Board: dojoBoard, StatusField: "Status",
	}, numbers(1, 21))
	if e := render.FromError(err); err == nil || e.Code != render.ExitNetwork {
		t.Fatalf("err = %v, want a network error", err)
	}
	if len(res) != 20 || res[19].Card == nil || res[19].Number != 20 {
		t.Errorf("%d results before the failure, want 20", len(res))
	}
	// 1 batch + 3 attempts of the second batch + 3 attempts of #21 alone.
	if n := srv.Requests(); n != 7 {
		t.Errorf("requests = %d, want 7", n)
	}
}

func TestReadAllRepoNotFound(t *testing.T) {
	srv := graphtest.New(t, fixture("cards_repo_not_found"))
	_, err := issue.ReadAll(context.Background(), srv.Client(), issue.Request{
		Repo: issue.Repo{Owner: "Trum-ok", Name: "no-such-repo-xyz"}, Board: dojoBoard, StatusField: "Status",
	}, []int{1, 2})
	e := render.FromError(err)
	if e.Code != render.ExitNotFound || !strings.Contains(e.What, "repository") {
		t.Errorf("err = %v (code %d)", err, e.Code)
	}
}

// TestReadRefsOtherRepoNotFound: a missing repository other than the
// default one fails only its issues (CauseNoRepo), whether it is read
// alone, one issue (Read's request) or several.
func TestReadRefsOtherRepoNotFound(t *testing.T) {
	gone := issue.Repo{Owner: "Trum-ok", Name: "no-such-repo-xyz"}
	for _, tt := range []struct {
		file    string
		numbers []int
	}{
		{"card_repo_not_found", []int{1}},
		{"cards_repo_not_found", []int{1, 2}},
	} {
		srv := graphtest.New(t, fixture(tt.file))
		var refs []issue.Ref
		for _, n := range tt.numbers {
			refs = append(refs, issue.Ref{Repo: gone, Number: n})
		}
		res, err := issue.ReadRefs(context.Background(), srv.Client(), issue.Request{
			Repo: dojo, Board: dojoBoard, StatusField: "Status",
		}, refs)
		if err != nil || len(res) != len(refs) {
			t.Fatalf("%s: res %+v, err %v", tt.file, res, err)
		}
		for _, r := range res {
			if r.Cause != issue.CauseNoRepo || !strings.Contains(r.Err.Error(), "no-such-repo-xyz not found, or the token cannot see it") {
				t.Errorf("%s: %+v", tt.file, r)
			}
		}
	}
}

// TestReadAllSingle sends exactly Read's request for one number.
func TestReadAllSingle(t *testing.T) {
	res, err := readAll(t, "card_not_found", 9999)
	if err != nil || len(res) != 1 || render.FromError(res[0].Err).Code != render.ExitNotFound {
		t.Errorf("res %+v, err %v", res, err)
	}
	res, err = readAll(t, "card_3", 3)
	if err != nil || len(res) != 1 || res[0].Card == nil || res[0].Card.Status() != "Ready" {
		t.Errorf("res %+v, err %v", res, err)
	}
	if _, err := readAll(t, "card_forbidden", 3); render.FromError(err).Code != render.ExitNetwork {
		t.Errorf("forbidden: %v", err)
	}
}

func TestReadAllBadNumber(t *testing.T) {
	if _, err := issue.ReadAll(context.Background(), nil, issue.Request{Repo: dojo}, []int{3, 0}); render.FromError(err).Code != render.ExitUsage {
		t.Errorf("err = %v", err)
	}
}
