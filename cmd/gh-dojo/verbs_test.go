package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/config"
	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
	"github.com/Trum-ok/gh-dojo/internal/workflow"
	"github.com/spf13/cobra"
)

// TestMain makes every test run without a config unless it sets one: the
// real loader would find the repository's own .github/dojo.toml.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gh-dojo-test")
	if err != nil {
		panic(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		panic(err)
	}
	loadConfig = func() (*config.Config, error) { return config.Load(dir) }
	getenv = func(string) string { return "" }
	newApp = noApp // see withApp
	code := m.Run()
	if stray := strayAppFailures(); len(stray) > 0 {
		for _, line := range stray {
			fmt.Fprintln(os.Stderr, "FAIL:", line)
		}
		code = 1
	}
	os.RemoveAll(dir)
	os.Exit(code)
}

const verbsConfig = `
version = 1
preset = "kanban"

[board]
owner = "acme"
project = 3

[session]
env = ["DOJO_SESSION"]
link = "claude://s/{id}"

[verbs.park]
when = "Put the issue back"
to = "Backlog"
comment = "Parked."
`

// withConfig makes the commands load toml as .github/dojo.toml.
func withConfig(t *testing.T, toml string) {
	t.Helper()
	saved := loadConfig
	t.Cleanup(func() { loadConfig = saved })
	loadConfig = func() (*config.Config, error) { return config.Parse([]byte(toml), ".github/dojo.toml") }
}

func withSession(t *testing.T, id string) {
	t.Helper()
	saved := getenv
	t.Cleanup(func() { getenv = saved })
	getenv = func(name string) string {
		if name == "DOJO_SESSION" {
			return id
		}
		return ""
	}
}

// fakeBackend serves issues from memory and records what it applies.
type fakeBackend struct {
	issues   map[int]workflow.Issue
	applyErr error
	reads    int
	applied  []*workflow.Plan
}

func (f *fakeBackend) ReadIssue(_ context.Context, n int) (workflow.Issue, error) {
	f.reads++
	is, ok := f.issues[n]
	if !ok {
		return workflow.Issue{}, render.NotFound("issue #%d not found in acme/shop", n)
	}
	return is, nil
}

func (f *fakeBackend) Apply(_ context.Context, p *workflow.Plan) ([]workflow.Op, error) {
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	f.applied = append(f.applied, p)
	return p.Ops, nil
}

func (f *fakeBackend) Reset() error { return nil }

func withBackend(t *testing.T, f *fakeBackend) {
	t.Helper()
	saved := newBackend
	t.Cleanup(func() { newBackend = saved })
	newBackend = func(*config.Config) (Backend, error) { return f, nil }
}

// runOut runs gh dojo with args and returns the exit code with stdout and
// stderr in one block, as the golden files hold it.
func runOut(args ...string) (render.Code, []byte) { return runRootOut(newRootCmd, args...) }

func runRootOut(newRoot func() (*cobra.Command, error), args ...string) (render.Code, []byte) {
	var stdout, stderr bytes.Buffer
	code := run(newRoot, args, &stdout, &stderr)
	return code, fmt.Appendf(nil, "code: %d\n--- stdout\n%s--- stderr\n%s", code, stdout.String(), stderr.String())
}

func issue42(status string, comments ...workflow.Comment) map[int]workflow.Issue {
	return map[int]workflow.Issue{42: {
		Number: 42, Title: "Fix login redirect", OnBoard: status != "", Status: status, Comments: comments,
	}}
}

func TestVerbs(t *testing.T) {
	present := workflow.Comment{ViewerDidAuthor: true, Body: "Started. Session: claude://s/abc\n\n" +
		workflow.Marker("start", "Started. Session: claude://s/abc")}
	tests := []struct {
		name    string
		args    []string
		issues  map[int]workflow.Issue
		session string
		code    render.Code
		reads   int // ReadIssue calls expected
		applied int // Apply calls expected
	}{
		{"start_dry_run", []string{"start", "42", "--dry-run"}, issue42("Backlog"), "abc", render.ExitOK, 1, 0},
		{"start_dry_run_hash", []string{"start", "#42", "--dry-run", "--comment", "on it"}, issue42(""), "abc", render.ExitOK, 1, 0},
		{"start", []string{"start", "42"}, issue42("Backlog"), "abc", render.ExitOK, 1, 1},
		{"start_repeat", []string{"start", "42"}, issue42("In progress", present), "abc", render.ExitOK, 1, 0},
		{"start_no_session", []string{"start", "42", "--comment", "on it"}, issue42("Backlog"), "", render.ExitOK, 1, 1},
		{"wait_by", []string{"wait", "42", "--by", "12"}, issue42("In progress"), "", render.ExitOK, 1, 1},
		{"ready_denied", []string{"ready", "42"}, issue42("Backlog"), "", render.ExitDenied, 1, 0},
		{"done_closed", []string{"done", "42"}, map[int]workflow.Issue{42: {Number: 42, Closed: true, OnBoard: true, Status: "Ready"}},
			"", render.ExitOK, 1, 1},
		{"ready_closed", []string{"ready", "42"}, map[int]workflow.Issue{42: {Number: 42, Closed: true}}, "", render.ExitDenied, 1, 0},
		{"wait_missing_args", []string{"wait", "42"}, issue42("In progress"), "", render.ExitUsage, 0, 0},
		{"start_unused_by", []string{"start", "42", "--by", "12"}, issue42("Backlog"), "", render.ExitUsage, 0, 0},
		{"park_unused_comment", []string{"park", "42", "--comment", "x"}, issue42("Done"), "", render.ExitUsage, 0, 0},
		{"not_found", []string{"start", "7"}, issue42("Backlog"), "", render.ExitNotFound, 1, 0},
		{"no_arg", []string{"start"}, nil, "", render.ExitUsage, 0, 0},
		{"bad_arg", []string{"start", "abc"}, nil, "", render.ExitUsage, 0, 0},
		{"zero_arg", []string{"start", "0"}, nil, "", render.ExitUsage, 0, 0},
		{"max_arg", []string{"start", "2147483647"}, nil, "", render.ExitNotFound, 1, 0},
		{"too_large_arg", []string{"start", "2147483648"}, nil, "", render.ExitUsage, 0, 0},
		{"start_not_on_board", []string{"start", "42"}, map[int]workflow.Issue{42: {Number: 42}}, "abc", render.ExitOK, 1, 1},
		{"two_args", []string{"start", "1", "2"}, nil, "", render.ExitUsage, 0, 0},
		{"unknown_flag", []string{"start", "42", "--nope"}, nil, "", render.ExitUsage, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, verbsConfig)
			withSession(t, tt.session)
			f := &fakeBackend{issues: tt.issues}
			withBackend(t, f)
			code, out := runOut(tt.args...)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d\n%s", code, tt.code, out)
			}
			if f.reads != tt.reads || len(f.applied) != tt.applied {
				t.Errorf("reads = %d, applied = %d; want %d, %d", f.reads, len(f.applied), tt.reads, tt.applied)
			}
			testutil.Golden(t, "verbs/"+tt.name, out)
		})
	}
}

func TestVerbApplyError(t *testing.T) {
	withConfig(t, verbsConfig)
	withBackend(t, &fakeBackend{issues: issue42("Backlog"), applyErr: render.Partial("#42 status set, comment not posted")})
	if code, out := runOut("ready", "42", "--comment", "x"); code != render.ExitDenied {
		t.Errorf("ready from Backlog = %d, want %d\n%s", code, render.ExitDenied, out)
	}
	code, out := runOut("start", "42")
	if code != render.ExitPartial {
		t.Errorf("exit code = %d, want %d\n%s", code, render.ExitPartial, out)
	}
	testutil.Golden(t, "verbs/apply_partial", out)
}

func TestVerbHelp(t *testing.T) {
	withConfig(t, verbsConfig)
	for _, tt := range []struct {
		name string
		args [][]string
	}{
		{"root", [][]string{{"--help"}, {}, {"help"}}},
		{"start", [][]string{{"start", "--help"}, {"help", "start"}}},
		{"wait", [][]string{{"wait", "--help"}}},
		{"park", [][]string{{"park", "--help"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, args := range tt.args {
				code, out := runOut(args...)
				if code != render.ExitOK {
					t.Fatalf("run(%q) = %d\n%s", args, code, out)
				}
				testutil.Golden(t, "verbs/help_"+tt.name, out)
			}
		})
	}
}

// TestConfigErrorBuiltins checks unknown commands next to built-in ones: a
// typo still gets a suggestion, and a possible verb lists the built-ins
// with the reason no verbs are loaded.
func TestConfigErrorBuiltins(t *testing.T) {
	const broken = "version = 1\npreset = \"kanbn\"\n"
	for _, tt := range []struct {
		name, config string
		args         []string
	}{
		{"broken_typo_of_builtin", broken, []string{"bom"}},
		{"broken_verb_with_builtins", broken, []string{"start", "42"}},
		{"missing_verb_with_builtins", "", []string{"start", "42"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config != "" {
				withConfig(t, tt.config)
			}
			code, out := runRootOut(rootWith(testCmds), tt.args...)
			if code != render.ExitUsage {
				t.Errorf("exit code = %d, want %d\n%s", code, render.ExitUsage, out)
			}
			testutil.Golden(t, "config/"+tt.name, out)
		})
	}
}

// TestConfigErrors checks that a missing or broken config leaves --help and
// --version working, shows the problem in the root help and reports it for
// a command that may be a verb.
func TestConfigErrors(t *testing.T) {
	const broken = "version = 1\npreset = \"kanbn\"\n[board]\nowner = \"acme\"\nproject = 3\n"
	tests := []struct {
		name   string
		config string // "" means no config file
		args   []string
		code   render.Code
	}{
		{"missing_help", "", []string{"--help"}, render.ExitOK},
		{"missing_verb", "", []string{"start", "42"}, render.ExitUsage},
		{"broken_help", broken, []string{"--help"}, render.ExitOK},
		{"broken_version", broken, []string{"--version"}, render.ExitOK},
		{"broken_verb", broken, []string{"start", "42"}, render.ExitUsage},
		{"broken_verb_flag", broken, []string{"start", "42", "--dry-run"}, render.ExitUsage},
		{"broken_help_unknown", broken, []string{"help", "start"}, render.ExitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config != "" {
				withConfig(t, tt.config)
			}
			code, out := runOut(tt.args...)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d\n%s", code, tt.code, out)
			}
			testutil.Golden(t, "config/"+tt.name, out)
		})
	}
}
