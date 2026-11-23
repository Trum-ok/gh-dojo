// Command gh-dojo is a GitHub CLI extension that gives AI agents process
// verbs over GitHub Projects v2. Run it as `gh dojo`.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/spf13/cobra"
)

// version is set at build time with -ldflags "-X main.version=..." (see the
// Makefile); plain go build leaves "dev".
var version = "dev"

func main() {
	// Ctrl-C and SIGTERM cancel the context of the command instead of
	// killing the process, so it reports what it did (render.FromError,
	// graph.AffectsAll: the writes stop, the rest is not attempted). Once
	// the context is cancelled the handler is removed, so a second signal
	// kills the process as usual.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := runContext(ctx, newRootCmd, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(int(code))
}

// run builds the root command with newRoot, executes it with args and returns
// the exit code. Errors, including those of building the commands, are
// printed to stderr as a short error block; with --json, --jq or
// --template they are also printed on stdout as JSON (see report).
//
// A panic while building or running the commands becomes the same short
// internal error. Only panics on this goroutine are caught: a panic in a
// goroutine started by a command still crashes the process with a trace, so
// such goroutines must recover themselves and return an error.
func run(newRoot func() (*cobra.Command, error), args []string, stdout, stderr io.Writer) render.Code {
	return runContext(context.Background(), newRoot, args, stdout, stderr)
}

// runContext is run with the context the commands get as cmd.Context():
// cancelling it interrupts the run.
func runContext(ctx context.Context, newRoot func() (*cobra.Command, error), args []string, stdout, stderr io.Writer) (code render.Code) {
	o := rawOutput(args) // until cobra parsed the flags
	defer func() {
		if r := recover(); r != nil {
			msg, _, _ := strings.Cut(fmt.Sprint(r), "\n") // never print a trace
			code = report(stdout, stderr, o, render.Internal("%s", msg))
		}
	}()
	root, err := newRoot()
	if err != nil {
		return report(stdout, stderr, o, err)
	}
	if args == nil {
		args = []string{} // cobra reads os.Args for nil
	}
	if fields, single := jsonFields(args); fields != "" && !single {
		// gh's --json number,title: refused before the command runs, or
		// the list would be read as an argument.
		cmd, _, ferr := root.Find(args)
		if ferr != nil {
			cmd = root
		}
		return report(stdout, stderr, o, jsonFieldsError(cmd, fields))
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	cmd, err := root.ExecuteContextC(ctx)
	if cmd != nil {
		if co := outputOf(cmd); co.json {
			o = co
		}
		// --json number: one field, taken as an argument the command
		// rejected. new and comment take text: only an error naming it
		// is about it. At the root it is more likely a mistyped command.
		if fields, _ := jsonFields(args); fields != "" && err != nil && cmd.HasParent() && !isCommand(cmd.Root(), fields) {
			if e := render.FromError(err); e.Code == render.ExitUsage &&
				(strings.Contains(e.What, fields) || strings.Contains(e.What, "takes no arguments")) {
				err = jsonFieldsError(cmd, fields)
			}
		}
	}
	return report(stdout, stderr, o, err)
}

// report prints err, if any, and returns the exit code: as text on stderr,
// and in JSON mode (o) also on stdout as {"schema_version":1,"error":...},
// unless the command printed its JSON with the error in it already.
func report(stdout, stderr io.Writer, o output, err error) render.Code {
	if err == nil {
		return render.ExitOK
	}
	var done reportedError
	if o.json && !errors.As(err, &done) {
		writeErrorJSON(stdout, o, err)
	}
	return render.WriteError(stderr, err)
}

// isCommand reports whether name is a command of root (gh dojo --json
// start 3).
func isCommand(root *cobra.Command, name string) bool {
	c, _, err := root.Find([]string{name})
	return err == nil && c != root
}
