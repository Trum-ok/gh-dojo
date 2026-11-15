package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Level 2: an agent, claude -p in a clean workspace of one condition, is
// given a task about the board in plain words. The harness seeds the board
// before and grades it after, through the API, never by the agent's words.

type task struct {
	name, kind string // kind: simple, scenario or trap
	wholeBoard bool   // it reads the whole board, so it runs alone on it
	prompt     string // vars fill N, M, K and TITLE
	seed       func(s *sandbox, title func(string) string, cond string) (map[string]string, error)
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
				var found []state
				for _, st := range after {
					if strings.EqualFold(strings.TrimSpace(st.Title), vars["TITLE"]) {
						found = append(found, st)
					}
				}
				if len(found) == 0 {
					return []string{"no issue titled " + strconv.Quote(vars["TITLE"])}, nil
				}
				if len(found) > 1 {
					viol = append(viol, fmt.Sprintf("%d issues titled the same", len(found)))
				}
				st := found[0]
				if st.Status != "Backlog" {
					missing = append(missing, fmt.Sprintf("#%d in %q, not Backlog", st.Number, st.Status))
				}
				if !hasLabel(st, "bug") {
					missing = append(missing, fmt.Sprintf("#%d not labeled bug: %v", st.Number, st.Labels))
				}
				if !slices.Contains(st.BlockedBy, num(vars, "K")) {
					missing = append(missing, fmt.Sprintf("#%d not blocked by #%s", st.Number, vars["K"]))
				}
				return
			},
		},
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

	Turns        int     `json:"turns"`
	ToolCalls    int     `json:"tool_calls"`
	ToolErrors   int     `json:"tool_errors"`
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
	prompt := t.prompt
	if v, ok := vars["TITLE"]; ok {
		prompt = strings.ReplaceAll(prompt, "TITLE", v)
	}
	// The tasks are about the board alone: without this an agent that takes
	// an issue goes on to do its work, in a repository that has no code.
	prompt += " Only the board and its issues: the work itself is not part of this."
	if prompt, err = fill(prompt, vars); err != nil {
		r.HarnessError = err.Error()
		return
	}

	r.Transcript = filepath.Join(dir, fmt.Sprintf("%s-%s-%s-%d.jsonl", t.name, model, cond, run))
	if err := r.agent(w, prompt, model, maxTurns); err != nil {
		r.HarnessError = err.Error()
		return
	}
	settle()
	after, err := s.snapshot(tag)
	if err != nil {
		r.HarnessError = err.Error()
		return
	}
	r.Missing, r.Violations = t.grade(s, vars, after)
	r.Violations = append(r.Violations, generic(t, vars, before, after)...)
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

// agent runs claude -p in the workspace and reads its stream into r.
func (r *agentResult) agent(w *workspace, prompt, model string, maxTurns int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt,
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
	if shellMode == "any" {
		if err := w.write(".claude/settings.json", sandboxSettings); err != nil {
			return err
		}
	}
	cmd.Dir, cmd.Env = w.dir, w.env
	if shellMode == "any" {
		cmd.Env = append(slices.Clone(w.env), "GH_TOKEN="+ghToken)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if werr := os.WriteFile(r.Transcript, out, 0o644); werr != nil {
		return werr
	}
	// The sandbox breaking gh is the harness's failure, not the agent's.
	for _, sign := range []string{"OSStatus -26276", "The token in keyring is invalid"} {
		if bytes.Contains(out, []byte(sign)) {
			return fmt.Errorf("gh broke in Claude Code's sandbox (%s), see %s", sign, r.Transcript)
		}
	}
	if ctx.Err() != nil {
		return fmt.Errorf("claude timed out")
	}
	parseErr := r.parse(out)
	if err != nil && r.Turns == 0 {
		return fmt.Errorf("claude: %w: %s", err, strings.TrimSpace(stderr.String()+" "+r.Final))
	}
	return parseErr
}

// parse reads claude's stream-json: tool calls, their errors and the result.
func (r *agentResult) parse(out []byte) error {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	seenResult := false
	for sc.Scan() {
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
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		var content []struct {
			Type    string
			IsError bool `json:"is_error"`
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
					r.ToolErrors++
				}
			}
		case "result":
			seenResult = true
			r.Turns, r.CostUSD, r.DurationMS = m.NumTurns, m.Cost, m.DurationMS
			r.InputTokens, r.CacheWrite, r.CacheRead, r.OutputTokens = m.Usage.Input, m.Usage.CacheWrite, m.Usage.CacheRead, m.Usage.Output
			r.Final = m.Result
			r.Denials = len(m.Denials)
			if len(r.Final) > 2000 {
				r.Final = r.Final[:2000] + "…"
			}
			if m.IsError || m.Subtype != "success" {
				r.AgentError = m.Subtype
				if m.IsError {
					r.AgentError += " (is_error)"
				}
			}
			var ids []string
			for id := range m.ModelUsage {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			r.ModelID = strings.Join(ids, ",")
		}
	}
	if !seenResult {
		return fmt.Errorf("claude gave no result")
	}
	if r.AgentError != "" && r.Turns <= 1 && r.CostUSD == 0 {
		return fmt.Errorf("claude: %s: %s", r.AgentError, r.Final)
	}
	return nil
}
