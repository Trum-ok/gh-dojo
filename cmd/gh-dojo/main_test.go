package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
	"github.com/spf13/cobra"
)

// testCmds are commands that exercise error paths.
func testCmds() ([]*cobra.Command, error) {
	board := &cobra.Command{Use: "board", Short: "Board commands"}
	board.AddCommand(&cobra.Command{
		Use:   "sync",
		Short: "Sync the board",
		RunE:  func(*cobra.Command, []string) error { return nil },
	})
	return []*cobra.Command{
		{
			Use: "boom",
			RunE: func(*cobra.Command, []string) error {
				panic("index out of range [3] with length 3\ngoroutine 1 [running]:\nmain.main()")
			},
		},
		{
			Use:  "one <n>",
			Args: cobra.ExactArgs(1),
			RunE: func(*cobra.Command, []string) error { return nil },
		},
		{
			Use:  "denied",
			RunE: func(*cobra.Command, []string) error { return render.Denied("ask the user first") },
		},
		board,
	}, nil
}

// rootWith returns a root constructor with the registered commands plus ctors.
func rootWith(ctors ...constructor) func() (*cobra.Command, error) {
	return func() (*cobra.Command, error) {
		return buildRoot(append(append([]constructor{}, constructors...), ctors...))
	}
}

func cmds(cs ...*cobra.Command) constructor {
	return func() ([]*cobra.Command, error) { return cs, nil }
}

func TestRun(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code render.Code
	}{
		{"panic", []string{"boom"}, render.ExitUsage},
		{"unknown_command", []string{"bom"}, render.ExitUsage},
		{"unknown_flag", []string{"one", "--nope", "1"}, render.ExitUsage},
		{"bad_args", []string{"one"}, render.ExitUsage},
		{"render_error", []string{"denied"}, render.ExitDenied},
		{"ok", []string{"one", "1"}, render.ExitOK},
		{"unknown_subcommand", []string{"board", "synk"}, render.ExitUsage},
		{"help_unknown", []string{"help", "bogus"}, render.ExitUsage},
		{"help_unknown_subcommand", []string{"help", "board", "bogus"}, render.ExitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(rootWith(testCmds), tt.args, &stdout, &stderr); code != tt.code {
				t.Errorf("exit code = %d, want %d", code, tt.code)
			}
			if stdout.Len() != 0 {
				t.Errorf("unexpected stdout: %q", stdout.String())
			}
			testutil.Golden(t, "run/"+tt.name, stderr.Bytes())
		})
	}
}

// TestBuildErrors checks that failures while building the commands are short
// errors, not crashes or silently missing commands.
func TestBuildErrors(t *testing.T) {
	builtin := func() *cobra.Command {
		return &cobra.Command{Use: "show", RunE: func(*cobra.Command, []string) error { return nil }}
	}
	tests := []struct {
		name string
		ctor constructor
		code render.Code
	}{
		{"constructor_panic", func() ([]*cobra.Command, error) { panic("nil map write\ngoroutine 1") }, render.ExitUsage},
		{"constructor_error", func() ([]*cobra.Command, error) {
			return nil, errors.New(".github/dojo.toml:3: unknown status \"Doing\"\nsecond line")
		}, render.ExitUsage},
		{"constructor_render_error", func() ([]*cobra.Command, error) {
			return nil, render.NotFound("project 7 not found for acme")
		}, render.ExitNotFound},
		{"constructor_nil", cmds(nil), render.ExitUsage},
		{"duplicate_name", func() ([]*cobra.Command, error) { return []*cobra.Command{builtin(), builtin()}, nil }, render.ExitUsage},
		{"duplicate_alias", cmds(builtin(), &cobra.Command{Use: "view", Aliases: []string{"show"}}), render.ExitUsage},
		{"duplicate_help", cmds(&cobra.Command{Use: "help"}), render.ExitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(rootWith(tt.ctor), []string{"--help"}, &stdout, &stderr); code != tt.code {
				t.Errorf("exit code = %d, want %d", code, tt.code)
			}
			if stdout.Len() != 0 {
				t.Errorf("unexpected stdout: %q", stdout.String())
			}
			testutil.Golden(t, "build/"+tt.name, stderr.Bytes())
		})
	}
}

func TestRegister(t *testing.T) {
	saved := constructors
	t.Cleanup(func() { constructors = saved })
	constructors = nil

	register(func() (*cobra.Command, error) {
		return &cobra.Command{Use: "a", Run: func(*cobra.Command, []string) {}}, nil
	})
	registerAll(func() ([]*cobra.Command, error) {
		return []*cobra.Command{{Use: "b", Run: func(*cobra.Command, []string) {}}}, nil
	})
	root, err := newRootCmd()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		if c, _, err := root.Find([]string{name}); err != nil || c.Name() != name {
			t.Errorf("command %q not registered", name)
		}
	}

	register(func() (*cobra.Command, error) { return nil, errors.New("broken config") })
	if _, err := newRootCmd(); err == nil || err.Error() != "broken config" {
		t.Errorf("newRootCmd() error = %v, want broken config", err)
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"help"}} {
		var stdout, stderr bytes.Buffer
		if code := run(newRootCmd, args, &stdout, &stderr); code != render.ExitOK {
			t.Fatalf("run(%q) = %d, stderr: %s", args, code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("run(%q) wrote to stderr: %q", args, stderr.String())
		}
		testutil.Golden(t, "help", stdout.Bytes())
	}
}

// TestGroupHelp checks that a command group without RunE prints its help.
func TestGroupHelp(t *testing.T) {
	for _, args := range [][]string{{"board"}, {"help", "board"}, {"board", "--help"}} {
		var stdout, stderr bytes.Buffer
		if code := run(rootWith(testCmds), args, &stdout, &stderr); code != render.ExitOK {
			t.Fatalf("run(%q) = %d, stderr: %s", args, code, stderr.String())
		}
		testutil.Golden(t, "group_help", stdout.Bytes())
	}
}

func TestVersion(t *testing.T) {
	saved := version
	t.Cleanup(func() { version = saved })
	for _, v := range []string{"dev", "v0.1.0-3-gabc1234-dirty"} {
		version = v
		var stdout, stderr bytes.Buffer
		if code := run(newRootCmd, []string{"--version"}, &stdout, &stderr); code != render.ExitOK {
			t.Fatalf("--version = %d, stderr: %s", code, stderr.String())
		}
		if want := "gh dojo version " + v + "\n"; stdout.String() != want {
			t.Errorf("--version printed %q, want %q", stdout.String(), want)
		}
	}
}
