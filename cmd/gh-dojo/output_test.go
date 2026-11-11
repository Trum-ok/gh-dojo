package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/Trum-ok/gh-dojo/internal/graph/graphtest"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
)

// runJSON runs gh dojo with args and returns the exit code and a golden
// text: the code, stdout indented (it must be one JSON object with
// schema_version 1, or empty) and stderr as is.
func runJSON(t *testing.T, args ...string) (render.Code, []byte) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(newRootCmd, args, &stdout, &stderr)
	out := stdout.Bytes()
	if len(out) > 0 {
		checkSchema(t, out)
		var b bytes.Buffer
		if err := json.Indent(&b, out, "", "  "); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, out)
		}
		out = append(b.Bytes(), '\n')
	}
	return code, fmt.Appendf(nil, "code: %d\n--- stdout\n%s--- stderr\n%s", code, out, stderr.String())
}

// checkSchema fails unless out is one compact JSON object, ending with a
// newline, whose first field is "schema_version": 1.
func checkSchema(t *testing.T, out []byte) {
	t.Helper()
	if !bytes.HasPrefix(out, []byte(`{"schema_version":1,`)) || bytes.Count(out, []byte("\n")) != 1 || out[len(out)-1] != '\n' {
		t.Errorf("stdout is not one compact JSON object starting with schema_version 1:\n%s", out)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, out)
	}
}

// warmApp installs a recorded GitHub with the board schema cached.
func warmApp(t *testing.T, fixtures ...string) {
	t.Helper()
	withApp(t, append([]string{boardFixture}, fixtures...)...)
	if _, err := newApp().Schema(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestShowJSON: show --json for full and brief cards, several numbers with
// one missing (exit code 3, the error text on stderr), and blockers both
// ways.
func TestShowJSON(t *testing.T) {
	for _, tt := range []struct {
		name    string
		fixture string
		args    []string
		code    render.Code
	}{
		{"card_3", card3Fixture, []string{"3"}, render.ExitOK},
		{"card_13_brief", issueFixtures + "card_13.json", []string{"13", "--brief"}, render.ExitOK},
		{"blockers", "testdata/graphql/show_blockers.json", []string{"3"}, render.ExitOK},
		{"not_on_board", issueFixtures + "card_not_on_board.json", []string{"3"}, render.ExitOK},
		{"long_thread_3", "testdata/graphql/show_long_thread.json", []string{"42", "--comments", "3"}, render.ExitOK},
		{"many", issueFixtures + "cards_3_7_9999.json", []string{"3", "7", "9999", "--brief"}, render.ExitNotFound},
		{"not_found", issueFixtures + "card_not_found.json", []string{"9999"}, render.ExitNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			warmApp(t, tt.fixture)
			code, out := runJSON(t, append([]string{"show", "--json"}, tt.args...)...)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d\n%s", code, tt.code, out)
			}
			testutil.Golden(t, "json/show_"+tt.name, out)
		})
	}
}

// TestJQ: --jq filters the JSON as gh does, scalars printed raw.
func TestJQ(t *testing.T) {
	withConfig(t, dojoConfig)
	warmApp(t, issueFixtures+"cards_3_7_9999.json")
	var stdout, stderr bytes.Buffer
	code := run(newRootCmd, []string{"show", "3", "7", "9999", "--jq", ".issues[].number"}, &stdout, &stderr)
	// #9999 is missing: exit code 3, the error as text on stderr.
	if code != render.ExitNotFound || stdout.String() != "3\n7\n" || !strings.HasPrefix(stderr.String(), "error: issue #9999 not found") {
		t.Errorf("code %d, stdout %q, stderr %q; want 3, \"3\\n7\\n\", the error", code, stdout.String(), stderr.String())
	}
}

// TestTemplate: --template formats the JSON with a Go template.
func TestTemplate(t *testing.T) {
	withConfig(t, dojoConfig)
	warmApp(t, card3Fixture)
	var stdout, stderr bytes.Buffer
	tmpl := `{{range .issues}}#{{.number}} {{.board.status}}{{"\n"}}{{end}}`
	code := run(newRootCmd, []string{"show", "3", "--template", tmpl}, &stdout, &stderr)
	if code != render.ExitOK || stdout.String() != "#3 Ready\n" || stderr.Len() > 0 {
		t.Errorf("code %d, stdout %q, stderr %q; want 0, \"#3 Ready\\n\", none", code, stdout.String(), stderr.String())
	}
}

// TestListJSON: list --json cut by --limit, the counts of what is left
// out, and a warning that stays on stderr.
func TestListJSON(t *testing.T) {
	for _, tt := range []struct {
		name     string
		fixtures []string
		args     []string
	}{
		{"limit", []string{boardFixture, itemsTwoPagesFixture}, []string{"--limit", "2"}},
		{"mine_label", []string{boardFixture, itemsTwoPagesFixture}, []string{"--mine", "--label", "BUG"}},
		{"board_changed", []string{boardFixture, listChangedFixture}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, dojoConfig)
			withApp(t, tt.fixtures...)
			code, out := runJSON(t, append([]string{"list", "--json"}, tt.args...)...)
			if code != render.ExitOK {
				t.Errorf("exit code = %d, want 0\n%s", code, out)
			}
			testutil.Golden(t, "json/list_"+tt.name, out)
		})
	}
}

// TestListJQ: --jq '.issues[].number' prints one number a line; a warning
// stays on stderr, out of the filtered output.
func TestListJQ(t *testing.T) {
	withConfig(t, dojoConfig)
	withApp(t, boardFixture, listChangedFixture)
	var stdout, stderr bytes.Buffer
	code := run(newRootCmd, []string{"list", "--jq", ".issues[].number"}, &stdout, &stderr)
	if code != render.ExitOK {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if _, err := strconv.Atoi(line); err != nil {
			t.Errorf("stdout line %q is not a number:\n%s", line, stdout.String())
		}
	}
	if !strings.HasPrefix(stderr.String(), "warning: the board changed") {
		t.Errorf("stderr = %q, want the warning", stderr.String())
	}
}

// checkJSONRuns is checkRuns with --json added to each command, golden
// files under json/.
func checkJSONRuns(t *testing.T, srv *graphtest.Server, before int, steps ...runs) {
	t.Helper()
	for _, r := range steps {
		code, out := runJSON(t, append(slices.Clone(r.args), "--json")...)
		if code != r.code {
			t.Errorf("%q: exit code = %d, want %d\n%s", r.args, code, r.code, out)
		}
		if n := srv.Requests() - before; n != r.requests {
			t.Errorf("%q: %d requests, want %d", r.args, n, r.requests)
		}
		before = srv.Requests()
		testutil.Golden(t, "json/"+r.golden, out)
	}
}

// TestVerbJSON: a verb's plan, what it applied and what was in place, for
// one issue: applied, unchanged on a repeat, a partial write (exit 5, the
// JSON says what applied and the error stays text on stderr), and dry
// runs, which give the plan.
func TestVerbJSON(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		srv, before := withWarmVerbApp(t, verbFixture("start_ok"))
		checkJSONRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitOK, 2, "verb_start"})
	})
	t.Run("start_not_on_board", func(t *testing.T) {
		srv, before := withWarmVerbApp(t, verbFixture("start_not_on_board"))
		checkJSONRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitOK, 3, "verb_start_not_on_board"})
	})
	t.Run("start_repeat", func(t *testing.T) {
		srv, before := withWarmVerbApp(t, verbFixture("start_repeat"))
		checkJSONRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitOK, 1, "verb_start_repeat"})
	})
	t.Run("start_partial", func(t *testing.T) {
		srv, before := withWarmVerbApp(t, verbFixture("start_partial"))
		checkJSONRuns(t, srv, before,
			runs{[]string{"start", "3"}, render.ExitPartial, 2, "verb_start_partial"},
			runs{[]string{"start", "3"}, render.ExitOK, 2, "verb_start_partial_rerun"},
		)
	})
	t.Run("start_502", func(t *testing.T) { // nothing confirmed: no JSON
		srv, before := withWarmVerbApp(t, verbFixture("start_502"))
		checkJSONRuns(t, srv, before, runs{[]string{"start", "3"}, render.ExitNetwork, 2, "verb_start_502"})
	})
	t.Run("start_dry_run", func(t *testing.T) {
		srv, before := withWarmVerbApp(t, verbFixture("card_3_start"))
		checkJSONRuns(t, srv, before, runs{[]string{"start", "3", "--dry-run"}, render.ExitOK, 1, "verb_start_dry_run"})
	})
	t.Run("wait_link", func(t *testing.T) {
		srv, before := withWarmVerbApp(t, verbFixture("wait_link"))
		checkJSONRuns(t, srv, before,
			runs{[]string{"wait", "3", "--by", "26"}, render.ExitOK, 2, "verb_wait_link"},
			runs{[]string{"wait", "3", "--by", "#26"}, render.ExitOK, 1, "verb_wait_link_repeat"},
		)
	})
	t.Run("review", func(t *testing.T) {
		srv, before := withEffectsApp(t, verbFixture("review_ok"))
		checkJSONRuns(t, srv, before,
			runs{[]string{"review", "3", "--comment", "PR #50"}, render.ExitOK, 2, "verb_review"},
			runs{[]string{"review", "3", "--comment", "PR #50"}, render.ExitOK, 1, "verb_review_repeat"},
		)
	})
	t.Run("drop", func(t *testing.T) {
		srv, before := withEffectsApp(t, verbFixture("drop_ok"))
		checkJSONRuns(t, srv, before,
			runs{[]string{"drop", "3", "--comment", "Duplicate of #2."}, render.ExitDenied, 1, "verb_drop_unconfirmed"},
			runs{[]string{"drop", "3", "--comment", "Duplicate of #2.", "--yes"}, render.ExitOK, 2, "verb_drop"},
			runs{[]string{"drop", "3", "--comment", "Duplicate of #2.", "--yes"}, render.ExitOK, 1, "verb_drop_repeat"},
			runs{[]string{"drop", "3", "--comment", "Duplicate of #2."}, render.ExitOK, 1, "verb_drop_repeat"},
		)
	})
	t.Run("drop_dry_run", func(t *testing.T) {
		srv, before := withEffectsApp(t, verbFixture("card_3"))
		checkJSONRuns(t, srv, before,
			runs{[]string{"drop", "3", "--comment", "Duplicate of #2.", "--dry-run"}, render.ExitOK, 1, "verb_drop_dry_run"})
	})
	t.Run("sprint_done_partial", func(t *testing.T) {
		srv, before := withSprintApp(t, time.Time{}, verbFixture("sprint_done_partial"))
		checkJSONRuns(t, srv, before,
			runs{[]string{"done", "42"}, render.ExitPartial, 2, "verb_sprint_done_partial"},
			runs{[]string{"done", "42"}, render.ExitOK, 2, "verb_sprint_done_partial_rerun"},
		)
	})
	t.Run("sprints_start_dry_run", func(t *testing.T) { // a warning: on stderr and in the JSON
		withConfig(t, sprintsPresetConfig)
		srv := withApp(t, iterationBoardFixture, verbFixture("sprints_start_card"))
		before := warmSchema(t, srv)
		checkJSONRuns(t, srv, before,
			runs{[]string{"start", "43", "--comment", "Card form first", "--dry-run"}, render.ExitOK, 1, "verb_sprints_start_dry_run"},
			// Refused: an error before anything is written leaves stdout empty.
			runs{[]string{"done", "43", "--dry-run"}, render.ExitDenied, 1, "verb_sprints_done_waiting"},
		)
	})
}

// TestBatchJSON: a verb on several issues reports each one, also those
// refused or failed, then exits with the batch's code; the summary error
// stays text on stderr.
func TestBatchJSON(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		srv, before := withWarmVerbApp(t, verbFixture("batch_done_refused"))
		checkJSONRuns(t, srv, before,
			runs{[]string{"done", "3", "7", "9", "17", "--dry-run"}, render.ExitDenied, 1, "batch_refused_dry_run"},
			runs{[]string{"done", "3", "7", "9", "17", "3"}, render.ExitDenied, 3, "batch_refused"},
		)
	})
	t.Run("network", func(t *testing.T) {
		srv, before := withWarmVerbApp(t, verbFixture("batch_done_network"))
		checkJSONRuns(t, srv, before,
			runs{[]string{"done", "3", "7", "9"}, render.ExitPartial, 4, "batch_network"},
			runs{[]string{"done", "3", "7", "9"}, render.ExitOK, 2, "batch_network_rerun"},
			runs{[]string{"done", "3", "7", "9"}, render.ExitOK, 1, "batch_network_repeat"},
		)
	})
}

// TestBatchOutcomesJSON: an auth error stops the writes, so the issues
// after it are not_attempted, while an issue in place is done without a
// write; with every issue done, written counts those that took a write.
func TestBatchOutcomesJSON(t *testing.T) {
	withConfig(t, verbsConfig)
	is := map[int]workflow.Issue{
		1: {Number: 1, OnBoard: true, Status: "Ready"},
		2: {Number: 2, OnBoard: true, Status: "Ready"},
		3: {Number: 3, OnBoard: true, Status: "Done", Closed: true, StateReason: "completed"},
		4: {Number: 4, OnBoard: true, Status: "Ready"},
	}
	auth := render.Network("GitHub rejected the token (HTTP 401)").WithFix(render.AuthFix).
		WithCause(&api.HTTPError{StatusCode: http.StatusUnauthorized})
	for _, tt := range []struct {
		name string
		args []string
		errs map[int]error
		code render.Code
	}{
		{"stop_auth", []string{"done", "1", "2", "3", "4"}, map[int]error{1: auth}, render.ExitNetwork},
		{"in_place", []string{"done", "2", "3"}, nil, render.ExitOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withBackend(t, &fakeBackend{issues: is, applyErrs: tt.errs})
			code, out := runJSON(t, append(tt.args, "--json")...)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d\n%s", code, tt.code, out)
			}
			testutil.Golden(t, "json/batch_"+tt.name, out)
		})
	}
}

// TestWriteJSON: comment, new, block and unblock, also a partial write:
// exit 5, the JSON saying what was done, the error text on stderr.
func TestWriteJSON(t *testing.T) {
	t.Run("comment", func(t *testing.T) {
		withConfig(t, dojoConfig)
		srv := withApp(t, boardFixture, card3Fixture, commentAddFixture)
		checkJSONRuns(t, srv, warmSchema(t, srv),
			runs{[]string{"comment", "3", "  Reproduced on main.\n"}, render.ExitOK, 2, "comment"})
	})
	t.Run("comment_repeat", func(t *testing.T) {
		withConfig(t, dojoConfig)
		srv := withApp(t, boardFixture, commentPostedCard)
		checkJSONRuns(t, srv, warmSchema(t, srv),
			runs{[]string{"comment", "#3", "Reproduced on main."}, render.ExitOK, 1, "comment_repeat"})
	})
	t.Run("new", func(t *testing.T) {
		withConfig(t, dojoConfig)
		srv := withApp(t, boardFixture, newSuccessFixture)
		checkJSONRuns(t, srv, warmSchema(t, srv), runs{newFixArgs, render.ExitOK, 4, "new"})
	})
	t.Run("new_partial", func(t *testing.T) {
		withConfig(t, dojoConfig)
		srv := withApp(t, boardFixture, newStatusFails)
		checkJSONRuns(t, srv, srv.Requests(), runs{newFixArgs, render.ExitPartial, 5, "new_partial"})
		srv = withApp(t, boardFixture, newRerunFixture)
		checkJSONRuns(t, srv, srv.Requests(), runs{newFixArgs, render.ExitOK, 3, "new_rerun"})
	})
	t.Run("block", func(t *testing.T) {
		srv, before := withLinksApp(t, verbFixture("links_block"))
		checkJSONRuns(t, srv, before,
			runs{[]string{"block", "3", "--by", "2,26"}, render.ExitOK, 2, "block"},
			runs{[]string{"block", "#3", "--by", "2,", "--by", "https://github.com/Trum-ok/gh-dojo/issues/26"}, render.ExitOK, 1, "block_repeat"},
		)
	})
	t.Run("block_partial", func(t *testing.T) {
		srv, before := withLinksApp(t, verbFixture("links_block_partial"))
		checkJSONRuns(t, srv, before,
			runs{[]string{"block", "3", "--by", "2", "--by", "26"}, render.ExitPartial, 2, "block_partial"},
			runs{[]string{"block", "3", "--by", "2", "--by", "26"}, render.ExitOK, 2, "block_partial_rerun"},
		)
	})
	t.Run("unblock", func(t *testing.T) {
		srv, before := withLinksApp(t, verbFixture("links_unblock"))
		checkJSONRuns(t, srv, before,
			runs{[]string{"unblock", "3", "--by", "2", "--by", "26"}, render.ExitOK, 2, "unblock"},
			runs{[]string{"unblock", "3", "--by", "2", "--by", "26"}, render.ExitOK, 1, "unblock_repeat"},
		)
	})
}

// TestSetupJSON: describe, doctor (a problem: exit 1 with the checks as
// JSON) and init (the projects listed, a config written, a dry run, and a
// mismatch with its problems).
func TestSetupJSON(t *testing.T) {
	t.Run("describe", func(t *testing.T) {
		withConfig(t, effectsDojoConfig)
		code, out := runJSON(t, "describe", "--json")
		if code != render.ExitOK {
			t.Errorf("exit code = %d, want 0", code)
		}
		testutil.Golden(t, "json/describe", out)
	})
	t.Run("doctor", func(t *testing.T) {
		for _, tt := range []struct {
			name     string
			config   string
			fixtures []string
			code     render.Code
		}{
			{"ok", dojoConfig, []string{doctorFixtures + "ok.json", boardFixture}, render.ExitOK},
			{"verbs", effectsDojoConfig, []string{doctorFixtures + "ok.json", boardFixture, doctorFixtures + "verbs.json"}, render.ExitUsage},
		} {
			withConfig(t, tt.config)
			withApp(t, tt.fixtures...)
			code, out := runJSON(t, "doctor", "--json")
			if code != tt.code {
				t.Errorf("%s: exit code = %d, want %d", tt.name, code, tt.code)
			}
			testutil.Golden(t, "json/doctor_"+tt.name, out)
		}
	})
	t.Run("init_list", func(t *testing.T) {
		withInit(t, "Trum-ok/gh-dojo", userProjectsFixture)
		code, out := runJSON(t, "init", "--json")
		if code != render.ExitUsage {
			t.Errorf("exit code = %d, want 1", code)
		}
		testutil.Golden(t, "json/init_list", out)
	})
	t.Run("init_write", func(t *testing.T) {
		withInit(t, "Trum-ok/gh-dojo", boardFixture)
		code, out := runJSON(t, "init", "--project", "5", "--json")
		if code != render.ExitOK {
			t.Errorf("exit code = %d, want 0", code)
		}
		testutil.Golden(t, "json/init_write", out)
	})
	t.Run("init_mismatch_dry_run", func(t *testing.T) {
		root, _ := withInit(t, "", initOrgBoardFixture)
		code, out := runJSON(t, "init", "--owner", "github", "--project", "4247", "--dry-run", "--json")
		if code != render.ExitUsage {
			t.Errorf("exit code = %d, want 1", code)
		}
		testutil.Golden(t, "json/init_mismatch_dry_run", out)
		if got := readConfig(t, root); got != "" {
			t.Errorf("dry run wrote:\n%s", got)
		}
	})
}

// TestJQRuntimeError: an expression that parses but fails on the data is
// an error after the command ran (exit 1, as in gh); next to the
// command's own error it is a warning and the command's code stays.
func TestJQRuntimeError(t *testing.T) {
	withConfig(t, dojoConfig)
	warmApp(t, issueFixtures+"cards_3_7_9999.json", card3Fixture)
	var stdout, stderr bytes.Buffer
	code := run(newRootCmd, []string{"show", "3", "7", "9999", "--jq", ".issues[].nope[]"}, &stdout, &stderr)
	if code != render.ExitNotFound || !strings.HasPrefix(stderr.String(), "warning: --jq: cannot iterate over: null") {
		t.Errorf("code %d, stderr %q; want 3 and the --jq warning first", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = run(newRootCmd, []string{"show", "3", "--jq", ".issues[].nope[]"}, &stdout, &stderr)
	if code != render.ExitUsage || !strings.HasPrefix(stderr.String(), "error: --jq: cannot iterate over: null") {
		t.Errorf("code %d, stderr %q; want 1 and the --jq error", code, stderr.String())
	}
}

// TestOutputFlags: --jq and --template are checked before the command
// runs, so a bad one never follows a write; they do not combine.
func TestOutputFlags(t *testing.T) {
	withConfig(t, dojoConfig)
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"bad_jq", []string{"show", "3", "--jq", ".issues["}},
		{"unknown_jq_function", []string{"show", "3", "--jq", "nosuchfn(1)"}},
		{"bad_template", []string{"list", "--template", "{{.issues"}},
		{"jq_and_template", []string{"list", "--jq", ".", "--template", "{{.}}"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runJSON(t, tt.args...) // newApp is noApp: no request may be sent
			if code != render.ExitUsage {
				t.Errorf("exit code = %d, want 1", code)
			}
			testutil.Golden(t, "json/flags_"+tt.name, out)
		})
	}
}
