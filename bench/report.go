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
	if len(files) == 0 {
		return fmt.Errorf("report: name the results.jsonl files")
	}
	var all []agentResult
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for sc.Scan() {
			var r agentResult
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				fh.Close()
				return fmt.Errorf("%s: %w", f, err)
			}
			all = append(all, r)
		}
		fh.Close()
	}
	var broken int
	all = slices.DeleteFunc(all, func(r agentResult) bool {
		if r.HarnessError != "" {
			broken++
			return true
		}
		return false
	})

	type key struct{ task, model, cond string }
	groups := map[key][]agentResult{}
	var models, taskNames []string
	for _, r := range all {
		for _, k := range []key{{"", r.Model, r.Cond}, {r.Task, r.Model, r.Cond}} {
			groups[k] = append(groups[k], r)
		}
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
	header := "| Model | Condition | Runs | Success | Violations | Cost, $ | Tokens, k | Turns | Tool calls | Tool errors | Denied |"
	sep := "|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|"
	row := func(label string, rs []agentResult) string {
		ok, viol, errs, denied := 0, 0, 0, 0
		var cost, tok, turns, calls []float64
		for _, r := range rs {
			if r.OK {
				ok++
			}
			viol += len(r.Violations)
			errs += r.ToolErrors
			denied += r.Denials
			cost = append(cost, r.CostUSD)
			tok = append(tok, float64(r.InputTokens+r.CacheWrite+r.CacheRead+r.OutputTokens)/1000)
			turns = append(turns, float64(r.Turns))
			calls = append(calls, float64(r.ToolCalls))
		}
		return fmt.Sprintf("| %s | %d | %d/%d | %d | %s | %s | %s | %s | %d | %d |", label, len(rs), ok, len(rs), viol,
			spread(cost, "%.3f"), spread(tok, "%.0f"), spread(turns, "%.0f"), spread(calls, "%.0f"), errs, denied)
	}
	condName := map[string]string{condGh: "gh + CLAUDE.md", condDojo: "gh dojo + skill"}
	fmt.Println(header)
	fmt.Println(sep)
	for _, m := range models {
		for _, c := range allConditions {
			if rs := groups[key{"", m, c}]; len(rs) > 0 {
				fmt.Println(row(m+" | "+condName[c], rs))
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
					fmt.Println(row(m+" | "+condName[c], rs))
				}
			}
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

// spread formats the median of vs and their range.
func spread(vs []float64, format string) string {
	if len(vs) == 0 {
		return "–"
	}
	s := slices.Clone(vs)
	slices.Sort(s)
	var med float64
	if n := len(s); n%2 == 1 {
		med = s[n/2]
	} else {
		med = (s[n/2-1] + s[n/2]) / 2
	}
	m := fmt.Sprintf(format, med)
	if len(s) == 1 {
		return m
	}
	return fmt.Sprintf("%s [%s–%s]", m, strings.TrimSpace(fmt.Sprintf(format, s[0])), strings.TrimSpace(fmt.Sprintf(format, s[len(s)-1])))
}
