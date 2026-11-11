package main

import (
	"bytes"
	"testing"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/Trum-ok/gh-dojo/internal/testutil"
	"github.com/spf13/cobra"
)

// testRoot is the real root plus commands that exercise error paths.
func testRoot() *cobra.Command {
	root := newRootCmd()
	root.AddCommand(
		&cobra.Command{
			Use: "boom",
			RunE: func(*cobra.Command, []string) error {
				panic("index out of range [3] with length 3\ngoroutine 1 [running]:\nmain.main()")
			},
		},
		&cobra.Command{
			Use:  "one <n>",
			Args: cobra.ExactArgs(1),
			RunE: func(*cobra.Command, []string) error { return nil },
		},
		&cobra.Command{
			Use:  "denied",
			RunE: func(*cobra.Command, []string) error { return render.Denied("ask the user first") },
		},
	)
	for _, c := range root.Commands() {
		wrapArgs(c)
	}
	return root
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(testRoot(), tt.args, &stdout, &stderr); code != tt.code {
				t.Errorf("exit code = %d, want %d", code, tt.code)
			}
			if stdout.Len() != 0 {
				t.Errorf("unexpected stdout: %q", stdout.String())
			}
			testutil.Golden(t, "run/"+tt.name, stderr.Bytes())
		})
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}} {
		var stdout, stderr bytes.Buffer
		if code := run(newRootCmd(), args, &stdout, &stderr); code != render.ExitOK {
			t.Fatalf("run(%q) = %d, stderr: %s", args, code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("run(%q) wrote to stderr: %q", args, stderr.String())
		}
		testutil.Golden(t, "help", stdout.Bytes())
	}
}
