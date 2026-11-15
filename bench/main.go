package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// titlePrefix starts the title of every issue the benchmark creates; sweep
// deletes the issues with it.
const titlePrefix = "bench-"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func usage() error {
	return fmt.Errorf(`usage:
  go run ./bench ops     [flags]   level 1: board operations by plain gh and by gh dojo, no model
  go run ./bench agents  [flags]   level 2: agents with claude -p, conditions gh and dojo
  go run ./bench report  FILE...   tables from the results of agents (results.jsonl)
  go run ./bench chart   FILE...   the README's SVGs of the workday session, from the results of agents
  go run ./bench sweep   [flags]   delete the issues the benchmark left in the sandbox`)
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	fs := flag.NewFlagSet(args[0], flag.ExitOnError)
	repo := fs.String("repo", envOr("GH_DOJO_BENCH_REPO", "Trum-ok/gh-dojo-sandbox"), "the sandbox repository")
	project := fs.Int("project", 6, "the number of its kanban board, owned by the repository's owner")
	out := fs.String("out", "bench/out", "where results go")
	models := fs.String("models", "haiku,sonnet", "agents: comma-separated models for claude --model")
	runs := fs.Int("runs", 5, "agents: runs per task, condition and model")
	tasks := fs.String("tasks", "", "agents: comma-separated task names (default: all)")
	conds := fs.String("conds", strings.Join(allConditions, ","), "agents: comma-separated conditions")
	maxTurns := fs.Int("max-turns", 40, "agents: claude --max-turns")
	parallel := fs.Int("parallel", 4, "agents: runs at once (next always runs alone on the board)")
	fs.StringVar(&shellMode, "shell", "gh", "agents: what Bash may run: gh (gh, jq and a few readers) or any (any command, in Claude Code's sandbox: writes in the workspace, network to GitHub only)")
	repeat := fs.Int("repeat", 3, "ops: passes over the operations; the time is their median")
	img := fs.String("img", "docs/media", "chart: where the SVGs go")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if shellMode != "gh" && shellMode != "any" {
		return fmt.Errorf("-shell %q, want gh or any", shellMode)
	}
	switch args[0] {
	case "report":
		return report(fs.Args())
	case "chart":
		return chart(*img, fs.Args())
	}
	// Two benchmarks on one board delete each other's issues.
	unlock, err := lockBoard(*out, *repo, *project)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := newSandbox(*repo, *project)
	if err != nil {
		return err
	}
	switch args[0] {
	case "sweep":
		return s.sweep(titlePrefix)
	case "ops":
		return s.levelOne(*out, *repeat)
	case "agents":
		return s.levelTwo(levelTwoConfig{
			out: *out, models: split(*models), runs: *runs, tasks: split(*tasks), conds: split(*conds), maxTurns: *maxTurns,
			parallel: *parallel,
		})
	}
	return usage()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func median(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(vs))
	if n := len(s); n%2 == 1 {
		return s[n/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func stamp() string { return time.Now().UTC().Format("20060102-150405") }

// tokens estimates the tokens of a text from its characters, about four per
// token for the English, JSON and shell the benchmark deals in. Level 2
// reports the real counts of the API.
func tokens(chars int) int { return (chars + 3) / 4 }

func (s *sandbox) levelOne(outDir string, repeat int) error {
	if err := s.busy(); err != nil {
		return err
	}
	if err := s.sweep(titlePrefix); err != nil {
		return err
	}
	recipes, err := s.recipes()
	if err != nil {
		return err
	}
	version, _ := (&workspace{dir: ".", env: childEnv(os.TempDir())}).run("gh", "dojo", "--version")
	fmt.Printf("# Level 1: board operations, plain gh by recipes vs gh dojo\n\n%s, board %s/%d, %s\n\n",
		s.repo(), s.owner, s.project, strings.TrimSpace(version))

	ws := map[string]*workspace{}
	for _, c := range allConditions {
		w, err := newWorkspace(s, c)
		if err != nil {
			return err
		}
		defer w.remove()
		ws[c] = w
	}
	// Passes alternate the order of the conditions, so neither always runs
	// first on a warm or cold API.
	var results []opResult
	ms := map[string][]float64{} // op/cond → wall times of all passes
	for pass := 0; pass < max(repeat, 1); pass++ {
		tag := titlePrefix + stamp() + ": "
		conds := slices.Clone(allConditions)
		if pass%2 == 1 {
			slices.Reverse(conds)
		}
		for _, o := range s.ops() {
			got := map[string]opResult{}
			for _, c := range conds {
				r := s.runOp(ws[c], o, recipes, tag)
				got[c] = r
				if r.Err != "" {
					fmt.Fprintf(os.Stderr, "%s, %s: %s\n", o.name, c, r.Err)
				} else {
					ms[o.name+"/"+c] = append(ms[o.name+"/"+c], float64(r.MS)/1000)
				}
				if err := s.sweep(titlePrefix); err != nil {
					return err
				}
			}
			if pass == 0 {
				results = append(results, got[condGh], got[condDojo])
			} else {
				for _, c := range conds {
					if got[c].Err != "" {
						results = append(results, got[c])
					}
				}
			}
		}
	}

	fmt.Printf("Calls and tokens of one pass; time is the median of %d passes, in seconds, of the commands alone.\n\n", max(repeat, 1))
	fmt.Println("| Operation | gh: calls | gh: ≈tokens written | gh: ≈tokens read | gh: s | gh dojo: calls | gh dojo: ≈tokens written | gh dojo: ≈tokens read | gh dojo: s |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	var sum [2]opResult
	var sumS [2]float64
	for i := 0; i+1 < len(results) && results[i].Cond == condGh && results[i+1].Cond == condDojo; i += 2 {
		g, d := results[i], results[i+1]
		mark := func(r opResult, v int) string {
			if r.Err != "" {
				return "failed"
			}
			return fmt.Sprint(v)
		}
		gs, ds := median(ms[g.Op+"/"+condGh]), median(ms[d.Op+"/"+condDojo])
		fmt.Printf("| %s | %s | %s | %s | %.1f | %s | %s | %s | %.1f |\n", g.Op,
			mark(g, g.Calls), mark(g, tokens(g.InChars)), mark(g, tokens(g.OutChars)), gs,
			mark(d, d.Calls), mark(d, tokens(d.InChars)), mark(d, tokens(d.OutChars)), ds)
		for j, r := range []opResult{g, d} {
			sum[j].Calls += r.Calls
			sum[j].InChars += r.InChars
			sum[j].OutChars += r.OutChars
		}
		sumS[0] += gs
		sumS[1] += ds
	}
	fmt.Printf("| **all** | %d | %d | %d | %.1f | %d | %d | %d | %.1f |\n\n", sum[0].Calls, tokens(sum[0].InChars), tokens(sum[0].OutChars), sumS[0],
		sum[1].Calls, tokens(sum[1].InChars), tokens(sum[1].OutChars), sumS[1])

	fmt.Println("What each agent has in context before its first command:")
	fmt.Println()
	for _, c := range allConditions {
		files, err := ws[c].context()
		if err != nil {
			return err
		}
		var parts []string
		total := 0
		for _, p := range []string{"CLAUDE.md", ".claude/skills/gh-dojo/SKILL.md"} {
			if t, ok := files[p]; ok {
				n := len([]rune(t))
				total += n
				parts = append(parts, fmt.Sprintf("%s ≈%d", p, tokens(n)))
			}
		}
		fmt.Printf("- %s: ≈%d tokens (%s)\n", c, tokens(total), strings.Join(parts, ", "))
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(outDir, "ops-"+stamp()+".json")
	b, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("\nThe commands of each run: %s\n", path)
	for _, r := range results {
		if r.Err != "" {
			return fmt.Errorf("some operations failed, see above")
		}
	}
	return nil
}

// errNoLock is tryLock's answer where there is no flock.
var errNoLock = errors.New("the benchmark runs only on Unix: it locks the board with flock")

// lockBoard takes a lock on the board for this process, refusing when another
// benchmark run from this working copy holds it. The lock is in the results'
// directory, not the temporary one, which differs between sandboxes.
func lockBoard(outDir, repo string, project int) (func(), error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	name := fmt.Sprintf(".lock-%s-%d", strings.ReplaceAll(repo, "/", "_"), project)
	f, err := os.OpenFile(filepath.Join(outDir, name), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := tryLock(f); err != nil {
		f.Close()
		if errors.Is(err, errNoLock) {
			return nil, err
		}
		return nil, fmt.Errorf("another benchmark is running on %s board %d (lock %s): wait for it or stop it", repo, project, f.Name())
	}
	return func() { f.Close() }, nil
}
