package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Level 1: each board operation done once with plain gh, by the recipes of
// the gh condition's CLAUDE.md, and once with gh dojo, on the same seeded
// state. It counts the commands an agent runs, the characters it writes in
// them and the characters it reads back, and checks that both ways leave the
// board in the state the operation asks for. No model is involved.

// recipe names the code blocks of conditions/gh.md, in order.
const (
	rState = iota
	rAdd
	rEdit
	rBlock
	rBoard
	nRecipes
)

// placeholder matches the words the recipes leave to the agent.
var placeholder = regexp.MustCompile(`\b(N_ID|M_ID|ITEM_ID|OPTION_ID|N|M|K)\b`)

// recipes returns the shell code blocks of the gh condition's CLAUDE.md.
func (s *sandbox) recipes() ([]string, error) {
	text, err := s.render("gh.md")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, part := range strings.Split(text, "```sh\n")[1:] {
		code, _, ok := strings.Cut(part, "```")
		if !ok {
			return nil, fmt.Errorf("gh.md: unclosed code block")
		}
		out = append(out, strings.TrimSpace(code))
	}
	if len(out) != nRecipes {
		return nil, fmt.Errorf("gh.md has %d recipes, want %d", len(out), nRecipes)
	}
	return out, nil
}

// step is one command an agent runs. Its line may hold placeholders, which
// take the values of vars; capture reads values for later steps from its
// output.
type step struct {
	line    string
	capture func(out string, vars map[string]string) error
}

type op struct {
	name string
	// seed puts the operation's issues on the board and returns their
	// numbers as vars N and M.
	seed func(s *sandbox, title func(string) string) (map[string]string, error)
	dojo []step
	gh   func(r []string) []step
	// check reads the board after the operation; nil for a read-only one,
	// whose output must name the issue the operation is after (want).
	check func(s *sandbox, vars map[string]string) error
	want  func(vars map[string]string, out string) error
}

// fill replaces the placeholders of a line with vars.
func fill(line string, vars map[string]string) (string, error) {
	var missing []string
	out := placeholder.ReplaceAllStringFunc(line, func(w string) string {
		v, ok := vars[w]
		if !ok {
			missing = append(missing, w)
			return w
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("no value for %v in %q", missing, line)
	}
	return out, nil
}

// captureState reads ITEM_ID and N_ID from the state recipe's output.
func captureState(out string, vars map[string]string) error {
	var v struct {
		ID   string
		Item struct{ ID string }
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return fmt.Errorf("state: %w: %s", err, out)
	}
	vars["N_ID"] = v.ID
	vars["ITEM_ID"] = v.Item.ID
	if v.Item.ID == "" {
		return fmt.Errorf("state: the issue is not on the board: %s", out)
	}
	return nil
}

func (s *sandbox) withOption(status string, line string) string {
	return strings.ReplaceAll(line, "OPTION_ID", s.options[status])
}

func wantStatus(s *sandbox, n string, status string) (state, error) {
	num, _ := strconv.Atoi(n)
	st, err := s.read(num)
	if err != nil {
		return st, err
	}
	if st.Status != status {
		return st, fmt.Errorf("#%d is in %q, want %q", num, st.Status, status)
	}
	return st, nil
}

func wantComment(st state, text string) error {
	if !slices.ContainsFunc(st.Comments, func(c string) bool { return strings.Contains(c, text) }) {
		return fmt.Errorf("#%d has no comment with %q: %q", st.Number, text, st.Comments)
	}
	return nil
}

func one(s *sandbox, title func(string) string, name, status string) (*issue, error) {
	return s.create(title(name), "", status, "enhancement")
}

func nums(is ...*issue) map[string]string {
	vars := map[string]string{}
	for i, k := range []string{"N", "M", "K"} {
		if i < len(is) {
			vars[k] = strconv.Itoa(is[i].Number)
		}
	}
	return vars
}

func (s *sandbox) ops() []op {
	return []op{
		{
			name: "show an issue",
			seed: func(s *sandbox, title func(string) string) (map[string]string, error) {
				m, err := one(s, title, "Export the board as CSV", "Backlog")
				if err != nil {
					return nil, err
				}
				n, err := one(s, title, "Import a CSV into the board", "In progress")
				if err != nil {
					return nil, err
				}
				if err := s.block(n, m); err != nil {
					return nil, err
				}
				for _, c := range []string{"Started.", "The parser is done; the mapping of columns is left."} {
					if err := s.comment(n, c); err != nil {
						return nil, err
					}
				}
				return nums(n, m), nil
			},
			dojo: []step{{line: "gh dojo show N"}},
			gh: func(r []string) []step {
				return []step{{line: "gh issue view N --comments"}, {line: r[rState]}}
			},
			want: func(vars map[string]string, out string) error {
				for _, w := range []string{vars["M"], "In progress", "mapping of columns"} {
					if !strings.Contains(out, w) {
						return fmt.Errorf("the output does not show %q", w)
					}
				}
				return nil
			},
		},
		{
			name: "find the next issue",
			seed: func(s *sandbox, title func(string) string) (map[string]string, error) {
				blocked, err := one(s, title, "Paginate the list", "Backlog")
				if err != nil {
					return nil, err
				}
				free, err := one(s, title, "Sort the list by date", "Backlog")
				if err != nil {
					return nil, err
				}
				blocker, err := one(s, title, "Speed up the list query", "In progress")
				if err != nil {
					return nil, err
				}
				if err := s.block(blocked, blocker); err != nil {
					return nil, err
				}
				return nums(free, blocked), nil
			},
			dojo: []step{{line: "gh dojo next"}},
			gh:   func(r []string) []step { return []step{{line: r[rBoard]}} },
			want: func(vars map[string]string, out string) error {
				if !strings.Contains(out, vars["N"]) {
					return fmt.Errorf("the output does not name #%s", vars["N"])
				}
				return nil
			},
		},
		{
			name: "start",
			seed: func(s *sandbox, title func(string) string) (map[string]string, error) {
				n, err := one(s, title, "Add a dark theme", "Backlog")
				return nums(n), err
			},
			dojo: []step{{line: "gh dojo start N"}},
			gh: func(r []string) []step {
				return []step{
					{line: r[rState], capture: captureState},
					{line: s.withOption("In progress", r[rEdit])},
					{line: `gh issue comment N --body "Started."`},
				}
			},
			check: func(s *sandbox, vars map[string]string) error {
				st, err := wantStatus(s, vars["N"], "In progress")
				if err != nil {
					return err
				}
				return wantComment(st, "Started.")
			},
		},
		{
			name: "wait for another issue",
			seed: func(s *sandbox, title func(string) string) (map[string]string, error) {
				n, err := one(s, title, "Send reminders by email", "In progress")
				if err != nil {
					return nil, err
				}
				m, err := one(s, title, "Set up the mail server", "Backlog")
				return nums(n, m), err
			},
			dojo: []step{{line: "gh dojo wait N --by M"}},
			gh: func(r []string) []step {
				return []step{
					{line: r[rState], capture: captureState},
					{line: "gh issue view M --json id --jq .id", capture: func(out string, vars map[string]string) error {
						vars["M_ID"] = strings.TrimSpace(out)
						return nil
					}},
					{line: r[rBlock]},
					{line: s.withOption("Waiting", r[rEdit])},
					{line: `gh issue comment N --body "Waiting for #M."`},
				}
			},
			check: func(s *sandbox, vars map[string]string) error {
				st, err := wantStatus(s, vars["N"], "Waiting")
				if err != nil {
					return err
				}
				m, _ := strconv.Atoi(vars["M"])
				if !slices.Contains(st.BlockedBy, m) {
					return fmt.Errorf("#%d is not blocked by #%d: %v", st.Number, m, st.BlockedBy)
				}
				return wantComment(st, "Waiting for #"+vars["M"])
			},
		},
		{
			name: "ready",
			seed: func(s *sandbox, title func(string) string) (map[string]string, error) {
				n, err := one(s, title, "Translate the settings page", "In progress")
				return nums(n), err
			},
			dojo: []step{{line: "gh dojo ready N"}},
			gh: func(r []string) []step {
				return []step{
					{line: r[rState], capture: captureState},
					{line: s.withOption("Ready", r[rEdit])},
				}
			},
			check: func(s *sandbox, vars map[string]string) error {
				_, err := wantStatus(s, vars["N"], "Ready")
				return err
			},
		},
		{
			name: "done",
			seed: func(s *sandbox, title func(string) string) (map[string]string, error) {
				n, err := one(s, title, "Show the version in the footer", "Ready")
				return nums(n), err
			},
			dojo: []step{{line: "gh dojo done N"}},
			gh: func(r []string) []step {
				return []step{
					{line: r[rState], capture: captureState},
					{line: s.withOption("Done", r[rEdit])},
					{line: "gh issue close N --reason completed"},
				}
			},
			check: func(s *sandbox, vars map[string]string) error {
				st, err := wantStatus(s, vars["N"], "Done")
				if err != nil {
					return err
				}
				if st.State != "CLOSED" || st.StateReason != "COMPLETED" {
					return fmt.Errorf("#%d is %s (%s), want closed as completed", st.Number, st.State, st.StateReason)
				}
				return nil
			},
		},
		{
			name: "file an issue",
			seed: func(s *sandbox, title func(string) string) (map[string]string, error) {
				return map[string]string{"TITLE": title("Crash on an empty board")}, nil
			},
			dojo: []step{{line: `gh dojo new "TITLE" --type bug --body "Opening an empty board crashes the app."`}},
			gh: func(r []string) []step {
				return []step{
					{line: `gh issue create --title "TITLE" --body "Opening an empty board crashes the app." --label bug`,
						capture: func(out string, vars map[string]string) error {
							url := strings.TrimSpace(out[strings.LastIndex(out, "https://"):])
							vars["N"] = url[strings.LastIndex(url, "/")+1:]
							return nil
						}},
					{line: r[rAdd], capture: func(out string, vars map[string]string) error {
						vars["ITEM_ID"] = strings.TrimSpace(out)
						return nil
					}},
					{line: s.withOption("Backlog", r[rEdit])},
				}
			},
			check: func(s *sandbox, vars map[string]string) error {
				all, _, err := s.issues(vars["TITLE"])
				if err != nil {
					return err
				}
				if len(all) != 1 {
					return fmt.Errorf("%d issues titled %q, want 1", len(all), vars["TITLE"])
				}
				if st := all[0]; st.Status != "Backlog" || !hasLabel(st, "bug") {
					return fmt.Errorf("#%d: status %q, labels %v; want Backlog, bug", st.Number, st.Status, st.Labels)
				}
				return nil
			},
		},
	}
}

// opResult is the measure of one way to do one operation.
type opResult struct {
	Op       string   `json:"op"`
	Cond     string   `json:"cond"`
	Calls    int      `json:"calls"`
	InChars  int      `json:"in_chars"`  // what the agent writes: the command lines
	OutChars int      `json:"out_chars"` // what it reads: stdout and stderr
	MS       int      `json:"ms"`        // wall time of the commands, without the agent's thinking
	Lines    []string `json:"lines"`
	Err      string   `json:"error,omitempty"`
}

// runOp seeds the operation, runs one condition's steps and checks the board.
func (s *sandbox) runOp(w *workspace, o op, recipes []string, tag string) opResult {
	r := opResult{Op: o.name, Cond: w.cond}
	title := func(name string) string { return tag + name }
	fail := func(err error) opResult { r.Err = err.Error(); return r }

	vars, err := o.seed(s, title)
	if err != nil {
		return fail(fmt.Errorf("seed: %w", err))
	}
	settle()
	steps := o.dojo
	if w.cond == condGh {
		steps = o.gh(recipes)
	}
	var all strings.Builder
	for _, st := range steps {
		line := st.line
		if v, ok := vars["TITLE"]; ok {
			line = strings.ReplaceAll(line, "TITLE", v)
		}
		line, err := fill(line, vars)
		if err != nil {
			return fail(err)
		}
		t0 := time.Now()
		out, err := w.shell(line)
		r.MS += int(time.Since(t0).Milliseconds())
		r.Calls++
		r.InChars += len([]rune(line))
		r.OutChars += len([]rune(out))
		r.Lines = append(r.Lines, line)
		all.WriteString(out)
		if err != nil {
			return fail(fmt.Errorf("%s: %w\n%s", line, err, out))
		}
		if st.capture != nil {
			if err := st.capture(out, vars); err != nil {
				return fail(err)
			}
		}
	}
	settle()
	if o.check != nil {
		if err := o.check(s, vars); err != nil {
			return fail(fmt.Errorf("check: %w", err))
		}
	}
	if o.want != nil {
		if err := o.want(vars, all.String()); err != nil {
			return fail(fmt.Errorf("check: %w", err))
		}
	}
	return r
}
