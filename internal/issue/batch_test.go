package issue_test

import (
	"context"
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
		fmt.Fprintf(&b, "== #%d\n", r.Number)
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
