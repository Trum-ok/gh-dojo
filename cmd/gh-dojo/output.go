package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/jq"
	"github.com/cli/go-gh/v2/pkg/jsonpretty"
	"github.com/cli/go-gh/v2/pkg/template"
	"github.com/cli/go-gh/v2/pkg/term"
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
// write (exit code 5), uses emitResult(cmd, res, res.write, err), and a
// command that writes uses emitWrite, which also says whether it wrote:
// the JSON says what was done, its "error" what was not.
//
// The JSON is an object with "schema_version" (schemaVersion) first and
// "error" second: null, or the error that ended the command (errorJSON),
// then the result's fields, snake_case and never a GitHub node ID. Every
// key is always there: lists are [] when empty, an absent value is null,
// never "".
//
// An error with no result to show (bad arguments, a refusal before
// anything was read, ...) is JSON too: run prints {"schema_version":1,
// "error":{...}} on stdout (writeErrorJSON). The error is also printed as
// text on stderr, in every mode, with the same exit code; warnings stay
// on stderr. docs/output.md ("JSON output") documents the schemas.

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
	f.Bool(jsonFlag, false, "print the result as JSON (schema_version 1), errors too")
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

// rawOutput reads the output flags from the arguments themselves, up to
// "--", for an error that comes before cobra parsed them or while it did
// (an unknown flag, a broken config). The last of a repeated flag wins,
// as in pflag.
func rawOutput(args []string) output {
	var o output
	value := func(i *int, a, long, short string) (string, bool) {
		switch {
		case a == "--"+long || a == "-"+short:
			if *i+1 < len(args) {
				*i++
				return args[*i], true
			}
			return "", false
		case strings.HasPrefix(a, "--"+long+"="):
			return strings.TrimPrefix(a, "--"+long+"="), true
		case strings.HasPrefix(a, "-"+short) && !strings.HasPrefix(a, "--"):
			return strings.TrimPrefix(strings.TrimPrefix(a, "-"+short), "="), true
		}
		return "", false
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if a == "--"+jsonFlag {
			o.json = true
			continue
		}
		if v, ok := strings.CutPrefix(a, "--"+jsonFlag+"="); ok {
			b, err := parseBool(v)
			o.json = b || err != nil // --json=number,title asks for JSON too
			continue
		}
		if v, ok := value(&i, a, jqFlag, "q"); ok {
			o.jq = v
		} else if v, ok := value(&i, a, templateFlag, "t"); ok {
			o.template = v
		}
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
		if err := checkJQ(o.jq); err != nil {
			return render.Usage("invalid --jq expression: %s", err).WithFix(help)
		}
	}
	if o.template != "" {
		if err := template.New(io.Discard, 80, false).Parse(o.template); err != nil {
			return render.Usage("invalid --template: %v", err).WithFix(help)
		}
	}
	return nil
}

// checkJQ parses and compiles expr as gh does, with go-gh's message: a
// parse error names the line and column. Its multi-line form, the
// expression with a caret under the column, is joined on one line, as an
// error line is trimmed.
func checkJQ(expr string) error {
	// No input: an expression that compiles fails on reading it.
	err := jq.Evaluate(strings.NewReader(""), io.Discard, expr)
	var syntax *json.SyntaxError
	if err == nil || errors.As(err, &syntax) {
		return nil
	}
	if head, _, multi := strings.Cut(err.Error(), "\n"); multi {
		if cause := errors.Unwrap(err); cause != nil {
			return fmt.Errorf("%s: %w", head, cause)
		}
		return errors.New(head)
	}
	return err
}

// jsonMode reports whether cmd prints JSON.
func jsonMode(cmd *cobra.Command) bool { return outputOf(cmd).json }

// emit prints the result of a command that read only: text(w) by default,
// else v as JSON, through --jq or --template when given. v must encode to
// a JSON object.
func emit(cmd *cobra.Command, v any, text func(w io.Writer)) error {
	return emitWrite(cmd, v, text, nil, false)
}

// emitResult is emit for a command that read only and failed with runErr
// after reading something to show, such as show with a missing number:
// the JSON gets runErr as its "error", and runErr, whose exit code says
// what happened, is returned.
func emitResult(cmd *cobra.Command, v any, text func(w io.Writer), runErr error) error {
	return emitWrite(cmd, v, text, runErr, false)
}

// emitWrite is emitResult for a command that writes; wrote says it wrote
// something. --jq or --template failing on the data after a write is a
// warning naming what was applied, and the exit code stays the command's:
// never 1 after a write, which an agent could take for "nothing done".
func emitWrite(cmd *cobra.Command, v any, text func(w io.Writer), runErr error, wrote bool) error {
	o := outputOf(cmd)
	w := cmd.OutOrStdout()
	if !o.json {
		text(w)
		return runErr
	}
	data, err := encodeResult(v, runErr)
	if err == nil {
		err = o.write(w, data)
	}
	var fe *formatError
	switch {
	case err == nil && runErr == nil:
		return nil
	case err == nil:
		return reportedError{runErr} // its JSON is out: run prints only the text
	case !errors.As(err, &fe):
		return render.Internal("cannot encode the result as JSON").WithCause(err)
	case wrote:
		render.Warn(cmd.ErrOrStderr(), "%s applied; %s failed on its result: %v", cmd.Name(), fe.flag, fe.err)
		return runErr
	case runErr != nil:
		render.Warn(cmd.ErrOrStderr(), "%s", fe)
		return runErr
	}
	return render.Usage("%s", fe).WithFix(cmd.CommandPath() + " --help").WithCause(err)
}

// reportedError is a command's error already in the JSON it printed:
// run prints it on stderr only.
type reportedError struct{ error }

func (e reportedError) Unwrap() error { return e.error }

// formatError is --jq or --template failing on the JSON.
type formatError struct {
	flag string // --jq or --template
	err  error
}

func (e *formatError) Error() string { return e.flag + ": " + e.err.Error() }
func (e *formatError) Unwrap() error { return e.err }

// write prints the JSON data as o asks: through --jq or --template, else
// as is, indented (and colored, as gh does) on a terminal. The output is
// buffered: a filter that fails part way leaves w untouched.
func (o output) write(w io.Writer, data []byte) error {
	tty, width, color := terminal(w)
	var b bytes.Buffer
	switch {
	case o.jq != "" && o.template != "":
		return &formatError{"--jq", errors.New("--jq and --template cannot be combined")}
	case o.jq != "":
		indent := ""
		if tty {
			indent = "  "
		}
		if err := jq.EvaluateFormatted(bytes.NewReader(data), &b, o.jq, indent, color); err != nil {
			return &formatError{"--jq", err}
		}
	case o.template != "":
		t := template.New(&b, width, color)
		err := t.Parse(o.template)
		if err == nil {
			err = t.Execute(bytes.NewReader(data))
		}
		if err == nil {
			err = t.Flush()
		}
		if err != nil {
			return &formatError{"--template", err}
		}
	case tty:
		if err := jsonpretty.Format(&b, bytes.NewReader(data), "  ", color); err != nil {
			return err
		}
	default:
		b.Write(data)
		b.WriteByte('\n')
	}
	_, err := w.Write(b.Bytes())
	return err
}

// writeErrorJSON prints err on w as {"schema_version":1,"error":{...}},
// through --jq or --template as o asks. When the filter fails on it, w
// gets nothing: the error is on stderr anyway.
func writeErrorJSON(w io.Writer, o output, err error) {
	data, encErr := encodeResult(struct{}{}, err)
	if encErr == nil {
		_ = o.write(w, data)
	}
}

// encodeResult encodes v as a JSON object with "schema_version" as its
// first field and "error" (runErr as an errorJSON, null for nil) as its
// second. HTML characters are kept as they are (no <).
func encodeResult(v any, runErr error) ([]byte, error) {
	data, err := marshal(v)
	if err != nil {
		return nil, err
	}
	if len(data) < 2 || data[0] != '{' {
		return nil, fmt.Errorf("result is not a JSON object: %.20s", data)
	}
	e, err := marshal(toErrorJSON(runErr))
	if err != nil {
		return nil, err
	}
	head := fmt.Sprintf(`{"schema_version":%d,"error":%s`, schemaVersion, e)
	if string(data) == "{}" {
		return []byte(head + "}"), nil
	}
	return append([]byte(head+","), data[1:]...), nil
}

// marshal is json.Marshal without HTML escaping.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(b.Bytes()), nil
}

// isTerminal reports whether w is a terminal: JSON is indented for a
// person, compact for a program.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(f)
}

// terminal reports whether w is a terminal and, as gh reads them, its
// width (80 otherwise) and whether to color.
func terminal(w io.Writer) (tty bool, width int, color bool) {
	if !isTerminal(w) {
		return false, 80, false
	}
	t := term.FromEnv()
	width = 80
	if tw, _, err := t.Size(); err == nil && tw > 0 {
		width = tw
	}
	return true, width, t.IsColorEnabled()
}

// errorJSON is an error in JSON: what ended a command, an issue a command
// could not show or apply, or what stopped a partial write. The fields
// are those of the error block on stderr.
type errorJSON struct {
	ExitCode int      `json:"exit_code"`
	Message  string   `json:"message"`
	Allowed  []string `json:"allowed"`
	Fix      *string  `json:"fix"` // null for none
}

// toErrorJSON is err as an errorJSON; nil for nil.
func toErrorJSON(err error) *errorJSON {
	if err == nil {
		return nil
	}
	e := render.FromError(err)
	return &errorJSON{ExitCode: int(e.Code), Message: e.What, Allowed: render.OrEmpty(e.Allowed), Fix: render.OrNull(strings.TrimSpace(e.Fix))}
}

// jsonTime is t in UTC for JSON, nil (null) for the zero time.
func jsonTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// versionResult is the JSON of gh dojo --version, and of gh dojo --json.
type versionResult struct {
	Version string `json:"version"`
}

// jsonFieldsRE matches what gh's --json takes: field names, comma-separated.
var jsonFieldsRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(,[a-zA-Z][a-zA-Z0-9_]*)*$`)

// jsonFields returns the field list given to --json as gh takes it
// (--json number,title, or --json=number,title), "" for none. single is
// set for one name after --json, which may be an argument of the command
// instead (new --json Title).
func jsonFields(args []string) (fields string, single bool) {
	for i, a := range args {
		if a == "--" {
			break
		}
		if v, ok := strings.CutPrefix(a, "--"+jsonFlag+"="); ok {
			if _, err := parseBool(v); err != nil && jsonFieldsRE.MatchString(v) {
				return v, false
			}
			continue
		}
		if a == "--"+jsonFlag && i+1 < len(args) && jsonFieldsRE.MatchString(args[i+1]) {
			next := args[i+1]
			return next, !strings.Contains(next, ",")
		}
	}
	return "", false
}

// parseBool is strconv.ParseBool, as pflag reads a bool flag.
func parseBool(s string) (bool, error) {
	switch s {
	case "1", "t", "T", "true", "TRUE", "True":
		return true, nil
	case "0", "f", "F", "false", "FALSE", "False":
		return false, nil
	}
	return false, fmt.Errorf("not a bool: %q", s)
}

// jsonFieldsError is the error for fields given to --json, as gh takes
// them: gh dojo prints every field, and --jq picks some.
func jsonFieldsError(cmd *cobra.Command, fields string) error {
	pick := "{" + strings.ReplaceAll(fields, ",", ", ") + "}"
	return render.Usage("--json takes no fields, got %q: it prints them all; pick some with --jq, like --jq '%s'", fields, pick).
		WithFix(cmd.CommandPath() + " --help")
}
