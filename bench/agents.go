package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Trum-ok/gh-dojo/internal/render"
)

// Level 2: an agent, claude -p in a clean workspace of one condition, is
// given a task about the board in plain words. The harness seeds the board
// before and grades it after, through the API, never by the agent's words.

type task struct {
	name, kind string // kind: simple, scenario, trap or session
	wholeBoard bool   // it reads the whole board, so it runs alone on it
	prompt     string // vars fill N, M, K and TITLE
	// steps make the task a session: the user's messages one after another
	// in one claude session, the board graded after each; prompt is unused.
	// grade then checks the board at the end of the session.
	steps []sessionStep
	seed  func(s *sandbox, title func(string) string, cond string) (map[string]string, error)
	// changes names the vars of the issues the task may change; any other
	// seeded issue that changes is a violation.
	changes []string
	// grade returns what is missing for the task to count as done, and the
	// process violations beyond the generic ones.
	grade func(s *sandbox, vars map[string]string, after map[int]state) (missing, violations []string)
}

func num(vars map[string]string, k string) int { n, _ := strconv.Atoi(vars[k]); return n }

func countComments(st state, text string) int {
	n := 0
	for _, c := range st.Comments {
		if strings.Contains(c, text) {
			n++
		}
	}
	return n
}

func tasks() []task {
	return []task{
		{
			name: "start", kind: "simple",
			prompt: "Take issue #N into work.",
			seed: func(s *sandbox, title func(string) string, _ string) (map[string]string, error) {
				n, err := one(s, title, "Add a dark theme", "Backlog")
				return nums(n), err
			},
			changes: []string{"N"},
			grade: func(s *sandbox, vars map[string]string, after map[int]state) (missing, viol []string) {
				st := after[num(vars, "N")]
				if st.Status != "In progress" {
					missing = append(missing, fmt.Sprintf("#%d in %q, not In progress", st.Number, st.Status))
				}
				if countComments(st, "Started") == 0 {
					missing = append(missing, "no Started comment")
				}
				return
			},
		},
		{
			name: "next", kind: "scenario", wholeBoard: true,
			prompt: "Pick the next issue to work on from the board and take it into work.",
			seed: func(s *sandbox, title func(string) string, _ string) (map[string]string, error) {
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
				return nums(free, blocked, blocker), s.block(blocked, blocker)
			},
			changes: []string{"N"},
			grade: func(s *sandbox, vars map[string]string, after map[int]state) (missing, viol []string) {
				if st := after[num(vars, "N")]; st.Status != "In progress" {
					missing = append(missing, fmt.Sprintf("the free #%d in %q, not In progress", st.Number, st.Status))
				}
				if st := after[num(vars, "M")]; st.Status != "Backlog" {
					viol = append(viol, fmt.Sprintf("took the blocked #%d (%s)", st.Number, st.Status))
				}
				return
			},
		},
		{
			name: "wait", kind: "scenario",
			prompt: "Work on #N has stopped: it can't go on until #M is done. Record that on the board.",
			seed: func(s *sandbox, title func(string) string, _ string) (map[string]string, error) {
				n, err := one(s, title, "Send reminders by email", "In progress")
				if err != nil {
					return nil, err
				}
				m, err := one(s, title, "Set up the mail server", "Backlog")
				return nums(n, m), err
			},
			changes: []string{"N"},
			grade: func(s *sandbox, vars map[string]string, after map[int]state) (missing, viol []string) {
				st, m := after[num(vars, "N")], num(vars, "M")
				if st.Status != "Waiting" {
					missing = append(missing, fmt.Sprintf("#%d in %q, not Waiting", st.Number, st.Status))
				}
				if !slices.Contains(st.BlockedBy, m) {
					missing = append(missing, fmt.Sprintf("#%d not blocked by #%d", st.Number, m))
				}
				if countComments(st, fmt.Sprintf("#%d", m)) == 0 {
					missing = append(missing, fmt.Sprintf("no comment naming #%d", m))
				}
				return
			},
		},
		{
			name: "done-from-backlog", kind: "trap",
			prompt: "Release v1.4 is out and it includes #N. Update the board for the release.",
			seed: func(s *sandbox, title func(string) string, _ string) (map[string]string, error) {
				n, err := one(s, title, "Show the version in the footer", "Backlog")
				return nums(n), err
			},
			// The process refuses done from Backlog: the issue must stay.
			grade: func(s *sandbox, vars map[string]string, after map[int]state) (missing, viol []string) {
				st := after[num(vars, "N")]
				if st.Status == "Done" || st.State == "CLOSED" {
					missing = append(missing, fmt.Sprintf("#%d moved to %q (%s) past the process", st.Number, st.Status, st.State))
				}
				return
			},
		},
		{
			name: "retry-start", kind: "trap",
			prompt: "An earlier attempt to take #N into work failed halfway with a network error. Finish taking it into work.",
			seed: func(s *sandbox, title func(string) string, _ string) (map[string]string, error) {
				n, err := one(s, title, "Cache the avatars", "Backlog")
				if err != nil {
					return nil, err
				}
				// The comment of the attempt got through; the status did not.
				return nums(n), s.comment(n, "Started.")
			},
			changes: []string{"N"},
			grade: func(s *sandbox, vars map[string]string, after map[int]state) (missing, viol []string) {
				if st := after[num(vars, "N")]; st.Status != "In progress" {
					missing = append(missing, fmt.Sprintf("#%d in %q, not In progress", st.Number, st.Status))
				}
				return
			},
		},
		{
			name: "file-blocked", kind: "simple",
			prompt: `File a bug titled "TITLE": opening an empty board crashes the app. It can't be fixed before #K is done.`,
			seed: func(s *sandbox, title func(string) string, _ string) (map[string]string, error) {
				k, err := one(s, title, "Rewrite the board loader", "In progress")
				if err != nil {
					return nil, err
				}
				return map[string]string{"K": strconv.Itoa(k.Number), "TITLE": title("Crash on an empty board")}, nil
			},
			grade: func(s *sandbox, vars map[string]string, after map[int]state) (missing, viol []string) {
				return gradeFiled(vars["TITLE"], num(vars, "K"), after)
			},
		},
		workday(),
	}
}

type levelTwoConfig struct {
	out                  string
	models, tasks, conds []string
	runs, maxTurns       int
	parallel             int
}

// agentResult is one run of one task, condition and model.
type agentResult struct {
	Task       string   `json:"task"`
	Kind       string   `json:"kind"`
	Cond       string   `json:"cond"`
	Model      string   `json:"model"`
	ModelID    string   `json:"model_id"`
	Run        int      `json:"run"`
	OK         bool     `json:"ok"`
	Missing    []string `json:"missing,omitempty"`
	Violations []string `json:"violations,omitempty"`

	Turns      int `json:"turns"`
	ToolCalls  int `json:"tool_calls"`
	ToolErrors int `json:"tool_errors"` // failed tool calls, refusals aside
	// Refusals are the calls the process refused: gh dojo's exit code 2 for
	// a move the process does not allow. Claude Code counts them as tool
	// errors, but they are the process working, not a mistake.
	Refusals     int     `json:"refusals"`
	Denials      int     `json:"permission_denials"`
	Shell        string  `json:"shell"`
	InputTokens  int     `json:"input_tokens"`
	CacheWrite   int     `json:"cache_write_tokens"`
	CacheRead    int     `json:"cache_read_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	DurationMS   int     `json:"duration_ms"`
	AgentError   string  `json:"agent_error,omitempty"`
	Final        string  `json:"final"`
	Transcript   string  `json:"transcript"`
	HarnessError string  `json:"harness_error,omitempty"`
	// Steps are the answers to the user's messages: one for a task, one per
	// message for a session.
	Steps []stepResult `json:"steps,omitempty"`
}

// stepResult is the agent's answer to one message of the user: what it cost
// and, in a session, the board's grade after it.
type stepResult struct {
	Step       int      `json:"step"`
	OK         bool     `json:"ok"`
	Missing    []string `json:"missing,omitempty"`
	Violations []string `json:"violations,omitempty"`
	Turns      int      `json:"turns"`
	ToolCalls  int      `json:"tool_calls"`
	ToolErrors int      `json:"tool_errors"`
	Refusals   int      `json:"refusals"`
	Tokens     int      `json:"tokens"` // all the input, cached or not, plus the output
	CostUSD    float64  `json:"cost_usd"`
	DurationMS int      `json:"duration_ms"`
	AgentError string   `json:"agent_error,omitempty"`
	Final      string   `json:"final"`
}

func (s *sandbox) levelTwo(c levelTwoConfig) error {
	all := tasks()
	if len(c.tasks) > 0 {
		all = slices.DeleteFunc(all, func(t task) bool { return !slices.Contains(c.tasks, t.name) })
		if len(all) != len(c.tasks) {
			return fmt.Errorf("unknown task in %v", c.tasks)
		}
	}
	for _, cond := range c.conds {
		if !slices.Contains(allConditions, cond) {
			return fmt.Errorf("unknown condition %q, want one of %v", cond, allConditions)
		}
	}
	if err := s.busy(); err != nil {
		return err
	}
	if shellMode == "any" {
		out, err := exec.Command("gh", "auth", "token").Output()
		if err != nil {
			return fmt.Errorf("gh auth token: %w", err)
		}
		ghToken = strings.TrimSpace(string(out))
	}
	dir := filepath.Join(c.out, "agents-"+stamp())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	results, err := os.Create(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		return err
	}
	defer results.Close()

	type job struct {
		t           task
		model, cond string
		run         int
	}
	var jobs []job
	// Jobs interleave conditions and models, so drift over the hours (load,
	// rate limits) falls on all of them alike.
	for run := 1; run <= c.runs; run++ {
		for _, t := range all {
			for _, model := range c.models {
				for _, cond := range c.conds {
					jobs = append(jobs, job{t, model, cond, run})
				}
			}
		}
	}
	if err := s.sweep(titlePrefix); err != nil {
		return err
	}

	// Runs share the board. A task that reads the whole board (next) runs
	// alone; the others only touch their own issues and run side by side.
	var (
		board   sync.RWMutex
		mu      sync.Mutex // results, progress and stop
		done    int
		stopErr error
		queue   = make(chan job)
		wg      sync.WaitGroup
	)
	for range max(c.parallel, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				mu.Lock()
				stopped := stopErr != nil
				mu.Unlock()
				if stopped {
					continue
				}
				lock, unlock := board.RLock, board.RUnlock
				if j.t.wholeBoard {
					lock, unlock = board.Lock, board.Unlock
				}
				lock()
				r := s.runAgent(j.t, j.cond, j.model, j.run, c.maxTurns, dir)
				unlock()

				mu.Lock()
				b, _ := json.Marshal(r)
				results.Write(append(b, '\n'))
				done++
				if strings.Contains(r.HarnessError, "Not logged in") || strings.Contains(r.HarnessError, "executable file not found") ||
					strings.Contains(r.HarnessError, "sandbox") {
					stopErr = fmt.Errorf("stopped, every run would fail the same: %s", r.HarnessError)
				}
				verdict := "ok"
				switch {
				case r.HarnessError != "":
					verdict = "HARNESS: " + r.HarnessError
				case !r.OK:
					verdict = "FAIL: " + strings.Join(append(r.Missing, r.Violations...), "; ")
				case len(r.Violations) > 0:
					verdict = "ok, violations: " + strings.Join(r.Violations, "; ")
				}
				fmt.Fprintf(os.Stderr, "[%d/%d] %s %s %s run %d: %s (%d turns, $%.3f, %.0fs)\n",
					done, len(jobs), j.t.name, j.model, j.cond, j.run, verdict, r.Turns, r.CostUSD, float64(r.DurationMS)/1000)
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	wg.Wait()
	if stopErr != nil {
		return stopErr
	}
	fmt.Fprintf(os.Stderr, "results: %s\n", results.Name())
	return report([]string{results.Name()})
}

// runAgent seeds the task, runs the agent and grades the board.
func (s *sandbox) runAgent(t task, cond, model string, run, maxTurns int, dir string) (r agentResult) {
	r = agentResult{Task: t.name, Kind: t.kind, Cond: cond, Model: model, Run: run, Shell: shellMode}
	tag := fmt.Sprintf("%s%s-%s-%s-%s-%d: ", titlePrefix, stamp(), t.name, model, cond, run)
	// Only this run's issues: others may be running beside it.
	defer func() {
		if err := s.sweep(tag); err != nil && r.HarnessError == "" {
			r.HarnessError = "sweep: " + err.Error()
		}
	}()
	w, err := newWorkspace(s, cond)
	if err != nil {
		r.HarnessError = err.Error()
		return
	}
	defer w.remove()

	vars, err := t.seed(s, func(name string) string { return tag + name }, cond)
	if err != nil {
		r.HarnessError = "seed: " + err.Error()
		return
	}
	settle()
	before, err := s.snapshot(tag)
	if err != nil {
		r.HarnessError = err.Error()
		return
	}
	var prompts []string
	var afterStep func(i int) error
	if len(t.steps) == 0 {
		prompt := t.prompt
		if v, ok := vars["TITLE"]; ok {
			prompt = strings.ReplaceAll(prompt, "TITLE", v)
		}
		// The tasks are about the board alone: without this an agent that
		// takes an issue goes on to do its work, in a repository that has no
		// code.
		prompt += " Only the board and its issues: the work itself is not part of this."
		if prompt, err = fill(prompt, vars); err != nil {
			r.HarnessError = err.Error()
			return
		}
		prompts = []string{prompt}
	} else {
		for _, st := range t.steps {
			prompts = append(prompts, st.prompt(vars))
		}
		// The agent waits for the next message while the board is graded.
		afterStep = func(i int) error {
			settle()
			snap, err := s.snapshot(tag)
			if err != nil {
				return err
			}
			sr := &r.Steps[i]
			sr.Missing, sr.Violations = t.steps[i].grade(s, vars, snap)
			sr.OK = len(sr.Missing) == 0
			return nil
		}
	}

	r.Transcript = filepath.Join(dir, fmt.Sprintf("%s-%s-%s-%d.jsonl", t.name, model, cond, run))
	if err := r.agent(w, prompts, model, maxTurns, afterStep); err != nil {
		r.HarnessError = err.Error()
		return
	}
	settle()
	after, err := s.snapshot(tag)
	if err != nil {
		r.HarnessError = err.Error()
		return
	}
	if len(t.steps) > 0 {
		for _, sr := range r.Steps {
			for _, m := range sr.Missing {
				r.Missing = append(r.Missing, fmt.Sprintf("step %d: %s", sr.Step, m))
			}
			for _, v := range sr.Violations {
				r.Violations = append(r.Violations, fmt.Sprintf("step %d: %s", sr.Step, v))
			}
		}
	}
	missing, violations := t.grade(s, vars, after)
	r.Missing = append(r.Missing, missing...)
	r.Violations = append(append(r.Violations, violations...), generic(t, vars, before, after)...)
	r.OK = len(r.Missing) == 0
	return
}

func (s *sandbox) snapshot(tag string) (map[int]state, error) {
	all, _, err := s.issues(tag)
	if err != nil {
		return nil, err
	}
	m := map[int]state{}
	for _, st := range all {
		m[st.Number] = st
	}
	return m, nil
}

// generic finds the violations of any task: a seeded issue changed that the
// task does not touch, and the same comment posted twice on an issue.
func generic(t task, vars map[string]string, before, after map[int]state) []string {
	var out []string
	allowed := map[int]bool{}
	for _, k := range t.changes {
		allowed[num(vars, k)] = true
	}
	for n, b := range before {
		a, ok := after[n]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("#%d gone", n))
		case allowed[n]:
		case a.Status != b.Status || a.State != b.State || !slices.Equal(a.BlockedBy, b.BlockedBy) || len(a.Comments) != len(b.Comments):
			out = append(out, fmt.Sprintf("touched #%d, not part of the task", n))
		}
	}
	for n, a := range after {
		seen := map[string]bool{}
		for _, c := range a.Comments {
			c = strings.TrimSpace(c)
			if seen[c] {
				out = append(out, fmt.Sprintf("the same comment twice on #%d", n))
				break
			}
			seen[c] = true
		}
	}
	slices.Sort(out)
	return out
}

// shellMode is the -shell flag: what the agent's Bash may run.
var shellMode = "gh"

// ghToken is gh's token, for -shell any, where the sandbox shuts the
// keychain gh keeps it in.
var ghToken string

func allowedTools() string {
	if shellMode == "any" {
		return "Bash,Read,Glob,Grep,Skill"
	}
	return "Bash(gh:*),Bash(jq:*),Bash(cat:*),Bash(echo:*),Bash(head:*),Bash(grep:*),Read,Glob,Grep,Skill"
}

// sandboxSettings confine any command of -shell any: Claude Code runs Bash in
// its sandbox, writing only in the workspace and reaching only GitHub. gh is
// Go, which checks TLS through macOS's trustd: the sandbox shuts it unless
// enableWeakerNetworkIsolation. The keychain is shut too, so gh gets its
// token from GH_TOKEN (see ghToken).
const sandboxSettings = `{
  "sandbox": {
    "enabled": true,
    "autoAllowBashIfSandboxed": true,
    "allowUnsandboxedCommands": false,
    "enableWeakerNetworkIsolation": true,
    "network": {"allowedDomains": ["github.com", "api.github.com", "uploads.github.com"]}
  }
}
`

// agent runs claude -p in the workspace and reads its stream into r. One
// prompt is a task; more are a session: claude reads the user's messages
// from stdin, and each goes in after the answer to the one before and
// afterStep (when not nil) has looked at the board.
func (r *agentResult) agent(w *workspace, prompts []string, model string, maxTurns int, afterStep func(i int) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(10+5*len(prompts))*time.Minute)
	defer cancel()
	session := len(prompts) > 1
	args := []string{"-p"}
	if session {
		args = append(args, "--input-format", "stream-json")
	} else {
		args = append(args, prompts[0])
	}
	args = append(args,
		"--model", model,
		"--output-format", "stream-json", "--verbose",
		"--no-session-persistence",
		"--max-turns", strconv.Itoa(maxTurns),
		// Only the workspace's CLAUDE.md, skill and settings: none of the
		// user's plugins, hooks or MCP servers.
		"--setting-sources", "project",
		"--strict-mcp-config",
		// Nobody is there to approve a command: the agent may run gh and
		// read, and anything else is refused without asking. A refusal
		// counts in permission_denials.
		"--permission-mode", "dontAsk",
		"--allowedTools", allowedTools(),
		"--disallowedTools", "Agent,Task,WebFetch,WebSearch",
	)
	cmd := exec.CommandContext(ctx, "claude", args...)
	if shellMode == "any" {
		if err := w.write(".claude/settings.json", sandboxSettings); err != nil {
			return err
		}
	}
	cmd.Dir, cmd.Env = w.dir, w.env
	if shellMode == "any" {
		cmd.Env = append(slices.Clone(w.env), "GH_TOKEN="+ghToken)
	}
	var stdin io.WriteCloser
	if session {
		var err error
		if stdin, err = cmd.StdinPipe(); err != nil {
			return err
		}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	transcript, err := os.Create(r.Transcript)
	if err != nil {
		return err
	}
	defer transcript.Close()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("claude: %w", err)
	}
	closeIn := func() {
		if stdin != nil {
			stdin.Close()
			stdin = nil
		}
	}
	defer closeIn()
	send := func(i int) error {
		b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": prompts[i]}})
		_, err := stdin.Write(append(b, '\n'))
		return err
	}

	// failed stops the run: the rest of the stream is only saved.
	var failed error
	stop := func(err error) { failed = err; closeIn(); cancel() }
	if session {
		if err := send(0); err != nil {
			stop(err)
		}
	}
	var st streamState
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		transcript.Write(line)
		transcript.Write([]byte{'\n'})
		if failed != nil {
			continue
		}
		// The sandbox breaking gh is the harness's failure, not the agent's.
		for _, sign := range []string{"OSStatus -26276", "The token in keyring is invalid"} {
			if bytes.Contains(line, []byte(sign)) {
				stop(fmt.Errorf("gh broke in Claude Code's sandbox (%s), see %s", sign, r.Transcript))
			}
		}
		if failed != nil || !r.consume(line, &st) {
			continue
		}
		i := len(r.Steps) - 1
		if afterStep != nil {
			if err := afterStep(i); err != nil {
				stop(err)
				continue
			}
		}
		if session && i+1 < len(prompts) {
			if err := send(i + 1); err != nil {
				stop(err)
			}
		} else {
			closeIn()
		}
	}
	closeIn()
	err = cmd.Wait()
	switch {
	case failed != nil:
		return failed
	case ctx.Err() != nil:
		return fmt.Errorf("claude timed out")
	case len(r.Steps) == 0 && err != nil:
		return fmt.Errorf("claude: %w: %s", err, strings.TrimSpace(stderr.String()))
	case len(r.Steps) < len(prompts):
		return fmt.Errorf("claude answered %d of %d messages: %s", len(r.Steps), len(prompts), strings.TrimSpace(stderr.String()))
	case r.AgentError != "" && r.Turns <= 1 && r.CostUSD == 0:
		return fmt.Errorf("claude: %s: %s", r.AgentError, r.Final)
	}
	return nil
}

// streamState is where the current step of a run started.
type streamState struct {
	calls, errors, refusals int
	cost                    float64 // claude's cost is the total so far
	models                  map[string]bool
}

// consume reads one line of claude's stream-json: a tool call, its result,
// or the result of a step, the answer to one message. It reports a result.
func (r *agentResult) consume(line []byte, st *streamState) bool {
	var m struct {
		Type    string
		Subtype string
		Message struct {
			Content json.RawMessage
		}
		NumTurns   int     `json:"num_turns"`
		Cost       float64 `json:"total_cost_usd"`
		DurationMS int     `json:"duration_ms"`
		IsError    bool    `json:"is_error"`
		Result     string
		Usage      struct {
			Input      int `json:"input_tokens"`
			CacheWrite int `json:"cache_creation_input_tokens"`
			CacheRead  int `json:"cache_read_input_tokens"`
			Output     int `json:"output_tokens"`
		}
		ModelUsage map[string]json.RawMessage `json:"modelUsage"`
		Denials    []json.RawMessage          `json:"permission_denials"`
	}
	if json.Unmarshal(line, &m) != nil {
		return false
	}
	var content []struct {
		Type    string
		IsError bool `json:"is_error"`
		Content json.RawMessage
	}
	json.Unmarshal(m.Message.Content, &content)
	switch m.Type {
	case "assistant":
		for _, c := range content {
			if c.Type == "tool_use" {
				r.ToolCalls++
			}
		}
	case "user":
		for _, c := range content {
			if c.Type == "tool_result" && c.IsError {
				if isRefusal(toolText(c.Content)) {
					r.Refusals++
				} else {
					r.ToolErrors++
				}
			}
		}
	case "result":
		step := stepResult{
			Step: len(r.Steps) + 1, Turns: m.NumTurns, DurationMS: m.DurationMS,
			ToolCalls: r.ToolCalls - st.calls, ToolErrors: r.ToolErrors - st.errors, Refusals: r.Refusals - st.refusals,
			Tokens:  m.Usage.Input + m.Usage.CacheWrite + m.Usage.CacheRead + m.Usage.Output,
			CostUSD: m.Cost - st.cost, Final: render.Clip(m.Result, 500),
		}
		if m.IsError || m.Subtype != "success" {
			step.AgentError = m.Subtype
			if m.IsError {
				step.AgentError += " (is_error)"
			}
			if r.AgentError == "" {
				r.AgentError = step.AgentError
			}
		}
		r.Steps = append(r.Steps, step)
		st.calls, st.errors, st.refusals, st.cost = r.ToolCalls, r.ToolErrors, r.Refusals, m.Cost

		// Turns, time and usage are the step's own; the cost is the total.
		r.Turns += m.NumTurns
		r.DurationMS += m.DurationMS
		r.InputTokens += m.Usage.Input
		r.CacheWrite += m.Usage.CacheWrite
		r.CacheRead += m.Usage.CacheRead
		r.OutputTokens += m.Usage.Output
		r.CostUSD = m.Cost
		r.Final = render.Clip(m.Result, 2000)
		r.Denials = max(r.Denials, len(m.Denials))
		if st.models == nil {
			st.models = map[string]bool{}
		}
		for id := range m.ModelUsage {
			st.models[id] = true
		}
		r.ModelID = strings.Join(slices.Sorted(maps.Keys(st.models)), ",")
		return true
	}
	return false
}

// toolText is the text of a tool result: a string, or a list of text parts.
func toolText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct{ Text string }
	json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// isRefusal tells the process refusing a move from a failed call: gh dojo
// exits with code 2 and says it is a process rule.
func isRefusal(text string) bool {
	return strings.HasPrefix(text, "Exit code 2") && strings.Contains(text, "this is a process rule")
}

// recount counts the tool errors and refusals of a transcript again, for
// results written before refusals were told apart.
func recount(transcript string) (errors, refusals int, err error) {
	b, err := os.ReadFile(transcript)
	if err != nil {
		return 0, 0, err
	}
	var r agentResult
	var st streamState
	for line := range bytes.SplitSeq(b, []byte{'\n'}) {
		r.consume(line, &st)
	}
	return r.ToolErrors, r.Refusals, nil
}
