# Benchmark: gh dojo against plain gh

What gh dojo saves an agent, measured on the sandbox board of the e2e tests (`Trum-ok/gh-dojo-sandbox`, board 6,
preset kanban). Both sides are set up the way a careful team would:

- **gh**: plain `gh`, with the process, the board's IDs and command recipes in `CLAUDE.md`
  ([bench/conditions/gh.md](../bench/conditions/gh.md));
- **gh dojo**: `gh dojo` with its config ([bench/conditions/dojo.toml](../bench/conditions/dojo.toml)) and the skill
  `gh dojo agent install` writes from it.

The harness and how to run it: [bench/doc.go](../bench/doc.go). Issue [#50](https://github.com/Trum-ok/gh-dojo/issues/50).

In short: for a task of one message gh dojo does as well as plain gh with a careful `CLAUDE.md`, at the same cost.
Over a session of eight messages it takes 37–44% less time and 33–44% less money, with every step right and no
process worked around ([a workday](#a-workday-where-it-pays)).

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

*all* is computed from the unrounded sums, so it may differ from the sum of the rows by rounding. In context before
the first command: gh ≈1260 tokens (`CLAUDE.md`), gh dojo ≈1630 (the skill ≈1586 and `CLAUDE.md` ≈47).

What it shows:

- Each operation is one call instead of two to five, and the agent writes almost nothing: with gh it copies a
  GraphQL query and the board's IDs into every command (≈1500 tokens for the seven), and must carry IDs from one
  command's output to the next.
- The commands alone take 40% less time (20 s against 34 s for the seven), though gh dojo checks the process on
  the way: one process and its own few requests instead of several `gh` runs. For an agent each saved call is also
  a saved turn of the model, seconds each, which level 2 measures.
- The skill and its `CLAUDE.md` cost ≈370 tokens more than the gh `CLAUDE.md` once per session; the seven
  operations save about 1800. Where gh dojo should matter most is the process, which level 1 does not count: with
  gh the agent checks the allowed moves, the blockers and repeated comments itself. Level 2 measures that.

## Level 2: agents

`claude -p` in a fresh workspace of each condition gets the user's words, and the harness grades the board through
the API, not the agent's answer. Any change to an issue the task does not touch and any comment posted twice count
as violations, besides each task's own checks.

The agent's Bash runs any command in Claude Code's sandbox (writes in its workspace, network to GitHub only); no
command was refused. 5 runs per task, condition and model, 4 at a time, gh dojo v0.2.0-40-g797f9b1,
`claude-haiku-5-5` and `claude-sonnet-5-5`. Medians, with the range in brackets; tokens are all the input, cached or
not, plus the output; cost is at API prices (the runs used a subscription). *Refusals* are the commands the process
refused, gh dojo's exit code 2: Claude Code reports them as failed tool calls, but they are the process working, so
they are counted apart from the *tool errors*.

Tables: `go run ./bench report bench/out/agents-*/results.jsonl`.

### One message: no harm

Six tasks of one message each: **start**; **next** (the first Backlog issue is blocked, the agent must take the
second; it ran alone on the board); **wait** (on another issue: status, blocked-by link, comment);
**done-from-backlog** (a trap: "v1.4 is out and includes #N", but #N is in Backlog and the process refuses done from
there); **retry-start** (a trap: the `Started.` comment of a failed attempt is already there); **file-blocked** (a new
bug blocked by an issue). 2026-11-16:

| Model  | Condition       | Success | Violations | Cost, $             | Tokens, k    | Turns   | Time, s    | Tool errors | Refusals |
|--------|-----------------|--------:|-----------:|--------------------:|-------------:|--------:|-----------:|------------:|---------:|
| Sonnet | gh + CLAUDE.md  |   30/30 |          0 | 0.055 [0.050–0.063] | 66 [44–90]   | 3 [2–4] | 14 [8–24]  |           0 |        0 |
| Sonnet | gh dojo + skill |   30/30 |          0 | 0.053 [0.049–0.066] | 86 [63–112]  | 5 [4–6] | 10 [7–15]  |           8 |        5 |
| Haiku  | gh + CLAUDE.md  |   29/30 |          1 | 0.004 [0.003–0.007] | 93 [46–127]  | 4 [2–6] | 16 [9–34]  |           1 |        0 |
| Haiku  | gh dojo + skill |   30/30 |          0 | 0.004 [0.003–0.006] | 94 [66–170]  | 5 [4–9] | 16 [9–33]  |           6 |        3 |

- **Success and cost are even; Sonnet is 30% faster with gh dojo** (10 s against 14 s), Haiku the same. With the
  process, the board's IDs and ready recipes written down, both models do one-step tasks with plain gh almost as
  well.
- **The one failure is the trap that matters:** Haiku with gh, asked to update the board for a release, moved a
  Backlog issue through `In progress` to `Done` and closed it ("the table has no direct Backlog → Done move, so I
  went through In progress"). With gh dojo no run worked around the refusal. gh dojo's own rules would allow the
  same detour (start, then done from In progress); what stopped the agents is the explicit refusal and the skill's
  rule not to work around it.
- **gh dojo takes one or two more turns, and more tokens:** loading the skill is a tool call of its own, which every
  later turn rereads. CLAUDE.md is in context from the start, at no turn. In one message this cost and what the
  short commands save come out even.
- **The tool errors with gh dojo are not misuse.** 11 of the 14 are in the done-from-backlog trap, looking for the
  release v1.4, which the sandbox does not have: `gh dojo release v1.4` 10 times (the agents reasonably took "v1.4
  is out" for the release command) and once the API for the tags. The other 3 were gaps of gh dojo, filed and
  fixed after these runs: `new … --dry-run`, which every verb had but `new` did not
  ([#60](https://github.com/Trum-ok/gh-dojo/issues/60)), and `new … --by N` twice, the flag of `wait` and `block`, where
  `new` wanted `--blocked-by` ([#61](https://github.com/Trum-ok/gh-dojo/issues/61)).

### A workday: where it pays

**workday** is one claude session that gets eight messages one after another, as a user gives them over a day, the
board graded after each: take an issue; it stops, waiting for another; file a bug blocked by it; the other is
finished and verified (Ready, not Done); is the blocked one free now (its blocker is Ready, so yes): take it; an
issue in Backlog "went out in today's release" (the trap of done-from-backlog); the one taken is released; finish
an interrupted start (the trap of retry-start). 2026-11-16:

| Model  | Condition       | Steps right | Violations | Cost, $             | Tokens, k     | Turns      | Time, s       | Tool errors | Refusals |
|--------|-----------------|------------:|-----------:|--------------------:|--------------:|-----------:|--------------:|------------:|---------:|
| Sonnet | gh + CLAUDE.md  |       40/40 |          0 | 0.162 [0.152–0.195] | 510 [486–540] | 21 [20–22] | 80 [77–85]    |           0 |        0 |
| Sonnet | gh dojo + skill |       40/40 |          0 | 0.108 [0.102–0.117] | 438 [409–476] | 20 [19–21] | 51 [50–55]    |           0 |        5 |
| Haiku  | gh + CLAUDE.md  |       39/40 |          0 | 0.017 [0.014–0.019] | 748 [593–873] | 27 [21–30] | 117 [95–132]  |           3 |        0 |
| Haiku  | gh dojo + skill |       40/40 |          0 | 0.010 [0.009–0.011] | 514 [484–571] | 21 [20–23] | 66 [58–71]    |           0 |        5 |

Running totals, the median cost and time of the session so far after each message (the report has them per step,
with tokens and turns):

| After message | Sonnet gh: $ | Sonnet gh dojo: $ | Sonnet gh: s | Sonnet gh dojo: s | Haiku gh: $ | Haiku gh dojo: $ | Haiku gh: s | Haiku gh dojo: s |
|--------------:|-------------:|------------------:|-------------:|------------------:|------------:|-----------------:|------------:|-----------------:|
|             1 |        0.054 |             0.049 |           11 |                 8 |       0.004 |            0.003 |          13 |               11 |
|             2 |        0.069 |             0.055 |           21 |                13 |       0.006 |            0.004 |          32 |               18 |
|             4 |        0.098 |             0.076 |           44 |                27 |       0.009 |            0.006 |          65 |               34 |
|             6 |        0.127 |             0.094 |           63 |                40 |       0.014 |            0.008 |          92 |               53 |
|             8 |        0.162 |             0.108 |           80 |                51 |       0.017 |            0.010 |         117 |               66 |

What it shows:

- **Over a session gh dojo takes 37–44% less time and 33–44% less money:** Sonnet $0.108 against $0.162 and 51 s
  against 80 s; Haiku $0.010 against $0.017 and 66 s against 117 s. The skill does not hold it back even at the
  first message, and the gap widens with each one: each message after the first costs Sonnet about $0.008 with gh
  dojo and $0.015 with gh.
- **Where it comes from: what the agent writes.** With gh the agent writes GraphQL queries and the board's IDs into
  its commands: 3 to 4 times the output tokens (Sonnet 4.5k against 1.4k, Haiku 10.3k against 2.7k), the dearest
  tokens, and every one of them stays in the context the later messages reread. With gh dojo the commands are a few
  words, and Haiku needs no extra turns to piece a recipe together (21 turns against 27).
- **Fewer mistakes.** Every step of every gh dojo session was right, with no tool errors; the 5 refusals are the
  trap, once a session, each followed by the agent asking the user. Haiku with gh had 3 tool errors, and once put
  the new bug on the board without a status: it chained the issue, the board and the blocked-by link in one
  command and left out setting `Backlog` (the process reads no status as Backlog, but it says to set it).
- **The gh agents remember the board's item IDs from earlier messages** and edit them without reading the issue
  again. That is cheaper, and right here; on a board others change during the day it would act on a stale state.
  gh dojo reads the board at each command.

### Limits

- The gh condition is the best case: a CLAUDE.md with IDs and tested recipes, which a team has to write and keep in
  step with the board. Without it, plain gh is not a fair comparison (decided in
  [#50](https://github.com/Trum-ok/gh-dojo/issues/50)), so it was not run.
- One process (preset kanban) and one session script, 5 runs each: the differences above are far beyond the ranges,
  but a single violation in 30 runs is not a rate.
- Runs went 4 at a time, so an agent that read the whole board could see other runs' issues (one gh run listed the
  copies of its task from other runs, and left them alone). Only next was run alone on the board.
