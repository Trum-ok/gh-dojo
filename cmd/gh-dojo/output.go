package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/cli/go-gh/v2/pkg/jq"
	"github.com/cli/go-gh/v2/pkg/template"
	"github.com/itchyny/gojq"
	"github.com/spf13/cobra"

	"github.com/Trum-ok/gh-dojo/internal/render"
)

// JSON output: every command takes --json, --jq and --template (root
// persistent flags, added by addOutputFlags). A command builds its result
// as a typed value and hands it to emit with the function that prints it
// as text, so both come from the same data:
//
//	res := buildResult(...)
//	return emit(cmd, res, res.write)
//
// A command that did part of its work and failed, such as a partial
// write (exit code 5), uses emitResult(cmd, res, res.write, err): the
// JSON says what was done, the error what was not.
//
// The JSON is an object with "schema_version" (schemaVersion) first, then
// the result's fields, snake_case and never a GitHub node ID. Lists are
// [] when empty, never null. Errors stay text on stderr, as in gh: stdout
// gets JSON only for what the command produced, the same data it prints
// as text, so an error with nothing done leaves stdout empty, while a
// partial write (exit code 5) prints what was applied. Warnings stay on
// stderr. README ("JSON output") documents the schemas.

// schemaVersion is the version of every JSON schema. It changes only for
// a breaking change, in a major release; adding a field is not one.
const schemaVersion = 1

// Output flags, on the root and inherited by every command.
const (
	jsonFlag     = "json"
	jqFlag       = "jq"
	templateFlag = "template"
)

// addOutputFlags adds --json, --jq and --template to root, inherited by
// every command, and checks --jq and --template before any command runs,
// so a bad expression never follows a write.
func addOutputFlags(root *cobra.Command) {
	f := root.PersistentFlags()
	f.Bool(jsonFlag, false, "print the result as JSON (schema_version 1); errors stay text on stderr")
	f.StringP(jqFlag, "q", "", "filter the JSON with a jq `expression` (implies --json)")
	f.StringP(templateFlag, "t", "", "format the JSON with a Go `template` (implies --json)")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		return checkOutput(cmd, outputOf(cmd))
	}
}

// output is the output mode of a command run.
type output struct {
	json     bool   // --json, or implied by --jq or --template
	jq       string // --jq expression
	template string // --template
}

// outputOf reads the output flags of cmd.
func outputOf(cmd *cobra.Command) output {
	var o output
	f := cmd.Flags()
	if fl := f.Lookup(jqFlag); fl != nil {
		o.jq = fl.Value.String()
	}
	if fl := f.Lookup(templateFlag); fl != nil {
		o.template = fl.Value.String()
	}
	if fl := f.Lookup(jsonFlag); fl != nil {
		o.json = fl.Value.String() == "true"
	}
	o.json = o.json || o.jq != "" || o.template != ""
	return o
}

// checkOutput refuses --jq with --template and an expression or template
// that does not parse.
func checkOutput(cmd *cobra.Command, o output) error {
	help := cmd.CommandPath() + " --help"
	if o.jq != "" && o.template != "" {
		return render.Usage("--jq and --template cannot be combined; use one").WithFix(help)
	}
	if o.jq != "" {
		q, err := gojq.Parse(o.jq)
		if err == nil {
			_, err = gojq.Compile(q, gojq.WithEnvironLoader(os.Environ))
		}
		if err != nil {
			return render.Usage("invalid --jq expression: %v", err).WithFix(help)
		}
	}
	if o.template != "" {
		if err := template.New(io.Discard, 80, false).Parse(o.template); err != nil {
			return render.Usage("invalid --template: %v", err).WithFix(help)
		}
	}
	return nil
}

// jsonMode reports whether cmd prints JSON.
func jsonMode(cmd *cobra.Command) bool { return outputOf(cmd).json }

// emit prints a command's result: text(w) by default, else v as JSON,
// through --jq or --template when given. v must encode to a JSON object.
func emit(cmd *cobra.Command, v any, text func(w io.Writer)) error {
	o := outputOf(cmd)
	w := cmd.OutOrStdout()
	if !o.json {
		text(w)
		return nil
	}
	data, err := encodeResult(v)
	if err != nil {
		return render.Internal("cannot encode the result as JSON").WithCause(err)
	}
	switch {
	case o.jq != "":
		indent := ""
		if isTerminal(w) {
			indent = "  "
		}
		if err := jq.EvaluateFormatted(bytes.NewReader(data), w, o.jq, indent, false); err != nil {
			return render.Usage("--jq: %v", err).WithFix(cmd.CommandPath() + " --help")
		}
	case o.template != "":
		t := template.New(w, 80, false)
		if err := t.Parse(o.template); err != nil {
			return render.Usage("invalid --template: %v", err).WithFix(cmd.CommandPath() + " --help")
		}
		if err := t.Execute(bytes.NewReader(data)); err != nil {
			return render.Usage("--template: %v", err).WithFix(cmd.CommandPath() + " --help")
		}
		if err := t.Flush(); err != nil {
			return err
		}
	default:
		if isTerminal(w) {
			var b bytes.Buffer
			if err := json.Indent(&b, data, "", "  "); err == nil {
				data = b.Bytes()
			}
		}
		if _, err := fmt.Fprintf(w, "%s\n", data); err != nil {
			return err
		}
	}
	return nil
}

// emitResult is emit for a command that also failed with runErr, such as
// a partial write: it returns runErr, whose exit code says what happened,
// when there is one; a failing --jq or --template is then a warning.
func emitResult(cmd *cobra.Command, v any, text func(w io.Writer), runErr error) error {
	err := emit(cmd, v, text)
	if runErr == nil {
		return err
	}
	if err != nil {
		render.Warn(cmd.ErrOrStderr(), "%s", render.FromError(err).What)
	}
	return runErr
}

// encodeResult encodes v as a JSON object with "schema_version" as its
// first field. HTML characters are kept as they are (no <).
func encodeResult(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	data := bytes.TrimSpace(b.Bytes())
	if len(data) < 2 || data[0] != '{' {
		return nil, fmt.Errorf("result is not a JSON object: %.20s", data)
	}
	head := fmt.Sprintf(`{"schema_version":%d`, schemaVersion)
	if string(data) == "{}" {
		return []byte(head + "}"), nil
	}
	return append([]byte(head+","), data[1:]...), nil
}

// isTerminal reports whether w is a terminal: JSON is indented for a
// person, compact for a program.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// jsonError is an error inside a JSON result: an issue a command could
// not show or apply, or what stopped a partial write.
type jsonError struct {
	ExitCode int      `json:"exit_code"`
	Message  string   `json:"message"`
	Allowed  []string `json:"allowed"`
	Fix      string   `json:"fix"`
}

// errorJSON is err as a jsonError; nil for nil.
func errorJSON(err error) *jsonError {
	if err == nil {
		return nil
	}
	e := render.FromError(err)
	return &jsonError{ExitCode: int(e.Code), Message: e.What, Allowed: orEmpty(e.Allowed), Fix: e.Fix}
}

// orEmpty returns s, or an empty slice for nil: JSON lists are [], never
// null, so jq's .x[] works on them.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// jsonTime is t in UTC for JSON, nil (null) for the zero time.
func jsonTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
