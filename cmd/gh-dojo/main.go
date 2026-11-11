// Command gh-dojo is a GitHub CLI extension that gives AI agents process
// verbs over GitHub Projects v2. Run it as `gh dojo`.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/spf13/cobra"
)

// version is set at build time with -ldflags "-X main.version=..." (see the
// Makefile); plain go build leaves "dev".
var version = "dev"

func main() {
	os.Exit(int(run(newRootCmd, os.Args[1:], os.Stdout, os.Stderr)))
}

// run builds the root command with newRoot, executes it with args and returns
// the exit code. Errors, including those of building the commands, are
// printed to stderr as a short error block.
//
// A panic while building or running the commands becomes the same short
// internal error. Only panics on this goroutine are caught: a panic in a
// goroutine started by a command still crashes the process with a trace, so
// such goroutines must recover themselves and return an error.
func run(newRoot func() (*cobra.Command, error), args []string, stdout, stderr io.Writer) (code render.Code) {
	defer func() {
		if r := recover(); r != nil {
			msg, _, _ := strings.Cut(fmt.Sprint(r), "\n") // never print a trace
			code = render.WriteError(stderr, render.Internal("%s", msg))
		}
	}()
	root, err := newRoot()
	if err != nil {
		return render.WriteError(stderr, err)
	}
	if args == nil {
		args = []string{} // cobra reads os.Args for nil
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	return render.WriteError(stderr, root.Execute())
}
