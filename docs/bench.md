# Benchmark: gh dojo against plain gh

What gh dojo saves an agent, measured on the sandbox board of the e2e tests (`Trum-ok/gh-dojo-sandbox`, board 6,
preset kanban). Both sides are set up the way a careful team would:

- **gh**: plain `gh`, with the process, the board's IDs and command recipes in `CLAUDE.md`
  ([bench/conditions/gh.md](../bench/conditions/gh.md));
- **gh dojo**: `gh dojo` with its config and the skill `gh dojo agent install` writes from it.

The harness and how to run it: [bench/doc.go](../bench/doc.go). Issue #50.

## Level 1: board operations, no model

Each operation done once by the recipes of the gh `CLAUDE.md` and once by `gh dojo`, on the same seeded state;
both ways are checked to leave the board as the operation asks. *Written* is the command lines an agent types,
*read* is what the commands print; tokens are estimated as characters / 4. *s* is the wall time of the commands
alone, without the agent's thinking between them: the median of 3 passes, which alternate the order of the two.

gh dojo v0.2.0-40-g797f9b1, 2026-11-15:

| Operation              | gh: calls | gh: ≈tokens written | gh: ≈tokens read | gh: s | gh dojo: calls | gh dojo: ≈tokens written | gh dojo: ≈tokens read | gh dojo: s |
|------------------------|----------:|--------------------:|-----------------:|------:|---------------:|-------------------------:|----------------------:|-----------:|
| show an issue          |         2 |                 201 |              118 |   3.2 |              1 |                        4 |                   119 |        1.9 |
| find the next issue    |         1 |                 206 |               85 |   1.4 |              1 |                        3 |                    23 |        1.4 |
| start                  |         3 |                 245 |               72 |   4.9 |              1 |                        4 |                    10 |        3.0 |
| wait for another issue |         5 |                 296 |               94 |   8.0 |              1 |                        6 |                    14 |        3.5 |
| ready                  |         2 |                 236 |               57 |   3.2 |              1 |                        4 |                     6 |        2.5 |
| done                   |         3 |                 245 |               80 |   4.9 |              1 |                        4 |                    10 |        2.9 |
| file an issue          |         3 |                 105 |               21 |   8.0 |              1 |                       30 |                    24 |        4.5 |
| **all**                |    **19** |            **1531** |          **525** |**33.7**|         **7** |                   **55** |               **205** |   **19.8** |

In context before the first command: gh ≈1260 tokens (`CLAUDE.md`), gh dojo ≈1630 (`CLAUDE.md` ≈50 and the skill
≈1590).

What it shows:

- Each operation is one call instead of two to five, and the agent writes almost nothing: with gh it copies a
  GraphQL query and the board's IDs into every command (≈1500 tokens for the seven), and must carry IDs from one
  command's output to the next.
- The commands alone take 40% less time (20 s against 34 s for the seven), though gh dojo checks the process on
  the way: one process and its own few requests instead of several `gh` runs. For an agent each saved call is also
  a saved turn of the model, seconds each, which level 2 measures.
- The skill costs ≈370 tokens more than the gh `CLAUDE.md` once per session; the seven operations save about
  1800. Where gh dojo should matter most is the process, which level 1 does not count: with gh the agent checks
  the allowed moves, the blockers and repeated comments itself. Level 2 measures that.

## Level 2: agents

`claude -p` in a fresh workspace of each condition gets a task in plain words, with "only the board and its issues"
added; the harness grades the board through the API afterwards. Six tasks: **start**; **next** (the first Backlog
issue is blocked, the agent must take the second); **wait** (on another issue: status, blocked-by link, comment);
**done-from-backlog** (a trap: "v1.4 is out and includes #N", but #N is in Backlog and the process refuses done from
there); **retry-start** (a trap: the `Started.` comment of a failed attempt is already there); **file-blocked** (a new
bug blocked by an issue). Besides each task's own checks, any change to an issue the task does not touch and any
comment posted twice count as violations.

The agent's Bash runs any command in Claude Code's sandbox (writes in its workspace, network to GitHub only); no
command was refused. 5 runs per task, condition and model, 4 at a time (next alone on the board), 2026-11-16, gh dojo
v0.2.0-40-g797f9b1, `claude-haiku-5-5` and `claude-sonnet-5-5`. Medians, with the range in brackets; tokens are all
the input, cached or not, plus the output; cost is at API prices (the runs used a subscription).

| Model  | Condition       | Success | Violations | Cost, $             | Tokens, k     | Turns     | Time, s      | Tool errors |
|--------|-----------------|--------:|-----------:|--------------------:|--------------:|----------:|-------------:|------------:|
| Sonnet | gh + CLAUDE.md  |   30/30 |          0 | 0.055 [0.050–0.063] |  66 [44–90]   | 3 [2–4]   | 14 [7–24]    |           0 |
| Sonnet | gh dojo + skill |   30/30 |          0 | 0.053 [0.049–0.066] |  86 [63–112]  | 5 [4–6]   | 10 [7–15]    |          13 |
| Haiku  | gh + CLAUDE.md  |   29/30 |          1 | 0.004 [0.003–0.007] |  93 [46–127]  | 4 [2–6]   | 16 [9–34]    |           1 |
| Haiku  | gh dojo + skill |   30/30 |          0 | 0.004 [0.003–0.006] |  94 [66–170]  | 5 [4–9]   | 15 [9–32]    |           9 |

Per task: `go run ./bench report bench/out/agents-*/results.jsonl`.

What it shows:

- **Against a careful CLAUDE.md, the gain is small.** With the process, the board's IDs and ready recipes written
  down, both models do these single-step tasks with plain gh almost as well. The one failure is the trap that
  matters: Haiku with gh, asked to update the board for a release, moved a Backlog issue through `In progress` to
  `Done` and closed it ("the table has no direct Backlog → Done move, so I went through In progress"). With gh dojo
  no run worked around the refusal: `done` exits 2 and the skill says not to work around it. gh dojo's own rules
  would allow the same detour (start, then done from In progress); what stopped the agents is the explicit refusal.
- **Time: Sonnet is 30% faster with gh dojo** (10 s against 14 s); Haiku is the same (15 s against 16 s).
- **Cost is even, and gh dojo takes one or two more turns.** Loading the skill is a tool call of its own, and every
  later turn rereads it, so tokens are higher with gh dojo though its commands are shorter. CLAUDE.md is in context
  from the start, at no turn.
- **Tool errors with gh dojo are mostly the process working:** 19 of the 22 are in the trap, `done` refusing with exit
  code 2 and `gh dojo release v1.4` for a tag that does not exist. The other 3 are a gap: one Sonnet run tried
  `gh dojo new … --dry-run`, which every verb has but `new` does not.

Limits of this run:

- The tasks are one step each; the gain of gh dojo should grow with longer chains of board work in one session,
  where the skill's cost is paid once. Not measured yet.
- The gh condition is the best case: a CLAUDE.md with IDs and tested recipes, which a team has to write and keep in
  step with the board. Without it, plain gh is not a fair comparison (decided in #50), so it was not run.
- Runs went 4 at a time, so an agent that read the whole board could see other runs' issues (one gh run listed the
  copies of its task from other runs, and left them alone). Only next was run alone on the board.
