// Bench measures what gh dojo saves an agent against plain gh (issue #50), on
// the sandbox the e2e tests use: the repository Trum-ok/gh-dojo-sandbox and
// its kanban board 6 (see e2e/doc.go). The harness seeds the board through
// the API before each run, reads it back the same way to grade it, and
// deletes the issues it created (titles "bench-…"; deleting needs admin
// rights). It refuses to start while the e2e tests' issues are there.
//
// Two conditions, both the way a team would set them up:
//
//   - gh: plain gh, with the process, the board's IDs and command recipes in
//     CLAUDE.md (conditions/gh.md);
//   - dojo: gh dojo with its config (conditions/dojo.toml) and the skill gh
//     dojo agent install writes from it.
//
// Level 1, no model: each board operation once by the recipes of gh.md and
// once by gh dojo, counting the commands, the characters written and read
// and the commands' wall time (the median of -repeat passes), and checking
// both leave the board as the operation asks:
//
//	go run ./bench ops
//
// Level 2: claude -p in a fresh workspace of each condition is given each
// task in plain words; the result is the board's state, not the agent's
// words. Tasks: start, next (pick the free issue past a blocked one), wait
// (on another issue), done-from-backlog (the process refuses it), retry-start
// (the comment of an earlier attempt is there already), file-blocked (a new
// bug blocked by an issue). And a session, workday: one claude session gets
// eight messages one after another, the board work of a day with two traps,
// and the board is graded after each; the report prints its running totals
// per step, to show where the skill, loaded once, pays off. A refusal of the
// process (gh dojo's exit code 2) is counted apart from the tool errors:
// Claude Code reports it as one, but it is the process working.
//
// It needs claude logged in (claude auth login) or ANTHROPIC_API_KEY, and
// costs real tokens; run it by hand, not in CI:
//
//	go run ./bench agents -shell any -models haiku,sonnet -runs 5
//	go run ./bench agents -shell any -models haiku,sonnet -runs 5 -tasks workday
//	go run ./bench report bench/out/agents-*/results.jsonl
//
// The README's chart of the workday, a light and a dark SVG in docs/media,
// is drawn from the results of workday runs:
//
//	go run ./bench chart bench/out/agents-*/results.jsonl
//
// With -shell any the agent's Bash runs any command in Claude Code's sandbox
// (writes in its workspace, network to GitHub only); the default -shell gh
// allows gh, jq and a few readers, and refuses without asking the shell
// plumbing plain gh needs (ITEM=$(gh …)), which biases against it. Runs go
// -parallel at a time, 4 by default, except next, which reads the whole
// board and runs alone on it; a lock in bench/out keeps a second benchmark
// off the board. Both conditions use the installed gh dojo. Results and
// transcripts go to bench/out.
package main
