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

// reportCmd is the fix suggested for internal errors.
const reportCmd = "gh issue create -R Trum-ok/gh-dojo"

func main() {
	os.Exit(int(run(newRootCmd(), os.Args[1:], os.Stdout, os.Stderr)))
}

// run executes root with args and returns the exit code. Errors and panics
// are printed to stderr as a short error block.
func run(root *cobra.Command, args []string, stdout, stderr io.Writer) (code render.Code) {
	defer func() {
		if r := recover(); r != nil {
			msg, _, _ := strings.Cut(fmt.Sprint(r), "\n") // never print a trace
			code = render.WriteError(stderr, render.Usage("internal error: %s", msg).WithFix(reportCmd))
		}
	}()
	if args == nil {
		args = []string{} // cobra reads os.Args for nil
	}
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	return render.WriteError(stderr, root.Execute())
}
