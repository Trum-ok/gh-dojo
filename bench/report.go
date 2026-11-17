package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

// report prints the tables of level 2 from results.jsonl files: per model and
// condition overall, then per task.
func report(files []string) error {
	all, broken, err := load("report", files)
	if err != nil {
		return err
	}
	session := map[string]bool{}
	for _, t := range tasks() {
		session[t.name] = len(t.steps) > 0
	}

	type key struct{ task, model, cond string }
	groups := map[key][]agentResult{}
	var models, taskNames []string
	for _, r := range all {
		// The overall rows are the one-message tasks: a session costs as
		// much as several of them and has tables of its own.
		if !session[r.Task] {
			groups[key{"", r.Model, r.Cond}] = append(groups[key{"", r.Model, r.Cond}], r)
		}
		groups[key{r.Task, r.Model, r.Cond}] = append(groups[key{r.Task, r.Model, r.Cond}], r)
		if !slices.Contains(models, r.Model) {
			models = append(models, r.Model)
		}
		if !slices.Contains(taskNames, r.Task) {
			taskNames = append(taskNames, r.Task)
		}
	}

	fmt.Println("# Level 2: agents, gh with CLAUDE.md vs gh dojo with its skill")
	fmt.Println()
	fmt.Printf("%d runs", len(all))
	if broken > 0 {
		fmt.Printf(" (%d more left out: the harness failed, not the agent)", broken)
	}
	fmt.Println(". Medians, with the range in brackets; tokens are all the input (cached or not) plus the output.")
	fmt.Println()
	header := "| Model | Condition | Runs | Success | Violations | Cost, $ | Tokens, k | Turns | Time, s | Tool calls | Tool errors | Refusals | Denied |"
	sep := "|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|"
	row := func(label string, rs []agentResult) string {
		ok, viol, errs, refusals, denied := 0, 0, 0, 0, 0
		var cost, tok, turns, secs, calls []float64
		for _, r := range rs {
			if r.OK {
				ok++
			}
			viol += len(r.Violations)
			errs += r.ToolErrors
			refusals += r.Refusals
			denied += r.Denials
			cost = append(cost, r.CostUSD)
			tok = append(tok, float64(r.InputTokens+r.CacheWrite+r.CacheRead+r.OutputTokens)/1000)
			turns = append(turns, float64(r.Turns))
			secs = append(secs, float64(r.DurationMS)/1000)
			calls = append(calls, float64(r.ToolCalls))
		}
		return fmt.Sprintf("| %s | %d | %d/%d | %d | %s | %s | %s | %s | %s | %d | %d | %d |", label, len(rs), ok, len(rs), viol,
			spread(cost, "%.3f"), spread(tok, "%.0f"), spread(turns, "%.0f"), spread(secs, "%.0f"), spread(calls, "%.0f"), errs, refusals, denied)
	}
	if slices.ContainsFunc(all, func(r agentResult) bool { return !session[r.Task] }) {
		fmt.Println("One-message tasks, all together:")
		fmt.Println()
		fmt.Println(header)
		fmt.Println(sep)
		for _, m := range models {
			for _, c := range allConditions {
				if rs := groups[key{"", m, c}]; len(rs) > 0 {
					fmt.Println(row(m+" | "+condNames[c], rs))
				}
			}
		}
	}
	for _, t := range taskNames {
		fmt.Printf("\n## %s\n\n", t)
		fmt.Println(header)
		fmt.Println(sep)
		for _, m := range models {
			for _, c := range allConditions {
				if rs := groups[key{t, m, c}]; len(rs) > 0 {
					fmt.Println(row(m+" | "+condNames[c], rs))
				}
			}
		}
		if session[t] {
			sessionSteps(models, func(m, c string) []agentResult { return groups[key{t, m, c}] })
		}
		var notes []string
		for _, m := range models {
			for _, c := range allConditions {
				for _, r := range groups[key{t, m, c}] {
					for _, p := range append(slices.Clone(r.Missing), r.Violations...) {
						notes = append(notes, fmt.Sprintf("%s, %s, run %d: %s", m, c, r.Run, p))
					}
				}
			}
		}
		if len(notes) > 0 {
			fmt.Println()
			for _, n := range notes {
				fmt.Println("- " + n)
			}
		}
	}
	return nil
}

// sessionSteps prints a session's running totals after each message: the
// steps done right so far and the medians of cost, tokens, turns and time.
// Where the conditions' lines cross is where gh dojo's skill pays off.
func sessionSteps(models []string, runs func(model, cond string) []agentResult) {
	fmt.Println()
	fmt.Println("Running totals after each message: steps done right of all runs, then medians.")
	fmt.Println()
	fmt.Println("| Model | Condition | Step | Steps right | Cost, $ | Tokens, k | Turns | Time, s | Tool errors | Refusals |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, m := range models {
		for _, c := range allConditions {
			rs := runs(m, c)
			for i := range stepCount(rs) {
				ok, total, errs, refusals := 0, 0, 0, 0
				for _, r := range rs {
					if i >= len(r.Steps) {
						continue
					}
					for _, p := range r.Steps[:i+1] {
						errs += p.ToolErrors
						refusals += p.Refusals
						total++
						if p.OK {
							ok++
						}
					}
				}
				cost := runningTotals(rs, i, func(p stepResult) float64 { return p.CostUSD })
				tok := runningTotals(rs, i, func(p stepResult) float64 { return float64(p.Tokens) / 1000 })
				turns := runningTotals(rs, i, func(p stepResult) float64 { return float64(p.Turns) })
				secs := runningTotals(rs, i, stepSeconds)
				fmt.Printf("| %s | %s | %d | %d/%d | %s | %s | %s | %s | %d | %d |\n", m, condNames[c], i+1, ok, total,
					spread(cost, "%.3f"), spread(tok, "%.0f"), spread(turns, "%.0f"), spread(secs, "%.0f"), errs, refusals)
			}
		}
	}
}

// stepCount is how many messages the longest session of rs answered.
func stepCount(rs []agentResult) int {
	n := 0
	for _, r := range rs {
		n = max(n, len(r.Steps))
	}
	return n
}

// runningTotals is, for each run of rs that answered message i (from 0),
// the total of value over its steps up to that one: the report prints
// their median after each message, and the chart draws it.
func runningTotals(rs []agentResult, i int, value func(stepResult) float64) []float64 {
	var out []float64
	for _, r := range rs {
		if i >= len(r.Steps) {
			continue
		}
		t := 0.0
		for _, p := range r.Steps[:i+1] {
			t += value(p)
		}
		out = append(out, t)
	}
	return out
}

// stepSeconds is how long a step took, in seconds.
func stepSeconds(p stepResult) float64 { return float64(p.DurationMS) / 1000 }

// load reads results.jsonl files, leaving out the runs the harness failed:
// it returns how many.
func load(cmd string, files []string) (all []agentResult, broken int, err error) {
	if len(files) == 0 {
		return nil, 0, fmt.Errorf("%s: name the results.jsonl files", cmd)
	}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return nil, 0, err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for sc.Scan() {
			var r agentResult
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				fh.Close()
				return nil, 0, fmt.Errorf("%s: %w", f, err)
			}
			all = append(all, r)
		}
		fh.Close()
	}
	all = slices.DeleteFunc(all, func(r agentResult) bool {
		if r.HarnessError != "" {
			broken++
			return true
		}
		return false
	})
	// The transcripts are the source: results written before refusals were
	// told apart from errors count them again.
	for i, r := range all {
		if errs, refusals, err := recount(r.Transcript); err == nil {
			all[i].ToolErrors, all[i].Refusals = errs, refusals
		}
	}
	return all, broken, nil
}

// spread formats the median of vs and their range.
func spread(vs []float64, format string) string {
	if len(vs) == 0 {
		return "–"
	}
	m := fmt.Sprintf(format, median(vs))
	if len(vs) == 1 {
		return m
	}
	return fmt.Sprintf("%s [%s–%s]", m, strings.TrimSpace(fmt.Sprintf(format, slices.Min(vs))), strings.TrimSpace(fmt.Sprintf(format, slices.Max(vs))))
}
