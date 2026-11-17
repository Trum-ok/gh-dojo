package main

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/Trum-ok/gh-dojo/internal/render"
	"github.com/spf13/cobra"
)

// bodyFileFlag names the flag that reads text from a file, "-" for stdin.
const bodyFileFlag = "body-file"

// dryRunFlag names the flag of every command that writes to GitHub that
// prints its plan instead of writing.
const dryRunFlag = "dry-run"

// maxTextBytes caps text read from a file or stdin: GitHub rejects issue
// and comment bodies longer than 65536 characters anyway.
const maxTextBytes = 256 << 10

// readBodyFile returns the text of --body-file: the file's contents, or
// stdin for "-". Surrounding whitespace is trimmed.
func readBodyFile(cmd *cobra.Command, path string) (string, error) {
	help := cmd.CommandPath() + " --help"
	var r io.Reader
	what := path
	if path == "-" {
		r, what = cmd.InOrStdin(), "stdin"
	} else {
		f, err := os.Open(path)
		if err != nil {
			reason := "cannot read it"
			if errors.Is(err, os.ErrNotExist) {
				reason = "no such file"
			}
			return "", render.Usage("--%s %s: %s", bodyFileFlag, path, reason).WithFix(help).WithCause(err)
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, maxTextBytes+1))
	if err != nil {
		return "", render.Usage("--%s: cannot read %s: %v", bodyFileFlag, what, err).WithCause(err)
	}
	if len(data) > maxTextBytes {
		return "", render.Usage("--%s: %s is longer than %d KiB", bodyFileFlag, what, maxTextBytes>>10).WithFix(help)
	}
	return strings.TrimSpace(string(data)), nil
}
