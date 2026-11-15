package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/render"
)

// TestVerbShow: --show prints the card the verb read, with no request
// more than the verb alone sends (#57). After a write the card is the one
// from before it, and says so; with nothing written, a dry run or a
// refusal it is the issue as it is.
func TestVerbShow(t *testing.T) {
	for _, tt := range []struct {
		name     string
		fixture  string
		args     []string
		code     render.Code
		requests int
	}{
		// Taken: the confirmation line, then the card from before.
		{"start_show", "start_ok", []string{"start", "3", "--show"}, render.ExitOK, 2},
		// In place already: nothing written, the card as it is.
		{"start_show_repeat", "start_repeat", []string{"start", "3", "--show"}, render.ExitOK, 1},
		// The plan, then the card.
		{"start_show_dry_run", "card_3_start", []string{"start", "3", "--show", "--dry-run"}, render.ExitOK, 1},
		// Refused: the error on stderr, the card on stdout.
		{"wait_show_refused", "card_3", []string{"wait", "3", "--by", "the API team", "--show"}, render.ExitDenied, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, before := withWarmVerbApp(t, verbFixture(tt.fixture))
			checkRuns(t, srv, before, runs{tt.args, tt.code, tt.requests, tt.name})
		})
	}
}

// TestVerbShowPartial: after a partial write the error is on stderr and
// the card from before on stdout; the re-run prints it with the comment
// the first run left.
func TestVerbShowPartial(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("start_partial"))
	checkRuns(t, srv, before,
		runs{[]string{"start", "3", "--show"}, render.ExitPartial, 2, "start_show_partial"},
		runs{[]string{"start", "3", "--show"}, render.ExitOK, 2, "start_show_partial_rerun"},
	)
}

// TestVerbShowJSON: --show puts the card into the issue's JSON as show
// --json has it; without the flag card is null.
func TestVerbShowJSON(t *testing.T) {
	srv, before := withWarmVerbApp(t, verbFixture("start_ok"))
	checkRuns(t, srv, before, runs{[]string{"start", "3", "--show", "--json"}, render.ExitOK, 2, "start_show_json"})

	srv, before = withWarmVerbApp(t, verbFixture("card_3_start"))
	var stdout, stderr bytes.Buffer
	if code := run(newRootCmd, []string{"start", "3", "--dry-run", "--json"}, &stdout, &stderr); code != render.ExitOK {
		t.Fatalf("exit code = %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if n := srv.Requests() - before; n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
	var res struct {
		Issues []map[string]json.RawMessage `json:"issues"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil || len(res.Issues) != 1 {
		t.Fatalf("%v\n%s", err, stdout.String())
	}
	if card, ok := res.Issues[0]["card"]; !ok || string(card) != "null" {
		t.Errorf("card = %s without --show, want null:\n%s", card, stdout.String())
	}
}

// TestVerbShowSeveral: --show is for one issue; a batch of several is
// refused before anything is read.
func TestVerbShowSeveral(t *testing.T) {
	srv, before := withWarmVerbApp(t)
	checkRuns(t, srv, before, runs{[]string{"done", "3", "4", "--show"}, render.ExitUsage, 0, "done_show_several"})
}
