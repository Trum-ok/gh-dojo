# gh-dojo

[![CI](https://github.com/Trum-ok/gh-dojo/actions/workflows/ci.yml/badge.svg)](https://github.com/Trum-ok/gh-dojo/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Trum-ok/gh-dojo)](https://github.com/Trum-ok/gh-dojo/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/Trum-ok/gh-dojo)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**An AI-native [GitHub CLI](https://cli.github.com/) extension for GitHub
Projects.** Your agent says what happened, `gh dojo start 42`, and gh-dojo
applies your board's process: the status, the comment, the blockers, the
sprint. The process lives in the repository, in `.github/dojo.toml`.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/media/demo-dark.gif">
  <img alt="The user asks an agent to take #42. The agent runs gh dojo start 42, wait 42 --by 12 and ready 42; each prints one line, and issue #42 moves on the board from Backlog to In progress, Waiting and Ready with a comment for each step" src="docs/media/demo-light.gif">
</picture>

## Why

With plain `gh`, an agent can set any board field to any value, and nothing
tells it the process. Agents invent it as they go: they forget to leave a
reason for waiting or move an issue from the wrong status. gh-dojo keeps the
process in the repository as data, and turns it into commands:

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/media/compare-dark.gif">
  <img alt="Told that #42 is stuck until #12 lands, an agent with gh writes GraphQL queries and mutations, carrying node IDs between five calls, and leaves no comment; an agent with gh dojo runs gh dojo wait 42 --by 12, which sets the status, links the blocker and comments in one line" src="docs/media/compare-light.gif">
</picture>

gh-dojo is not a planner: it does no sprint planning, standups or reports,
does not replace `gh issue` or `gh pr`, and does not change the board's own
settings.

## Benchmark

Claude Code did a day of board work in one session: eight messages, two of
them traps, the board graded after each. Against plain `gh` with a careful
`CLAUDE.md` (the process, the board's IDs and tested command recipes), gh dojo
took 37–44% less time and 33–44% less money, and got every step right:

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/media/bench-workday-dark.svg">
  <img alt="Running time of one agent session after each of eight messages, median of 5 sessions. Sonnet: 51 s with gh dojo against 80 s with gh and CLAUDE.md. Haiku: 66 s against 117 s. The gap widens with every message" src="docs/media/bench-workday-light.svg">
</picture>

The gap widens with every message: with gh the agent writes GraphQL and the
board's IDs, as above, into every command, three to four times the output
tokens, and rereads them all later. For a task of one message the two are
even. Preset kanban, 5 sessions per model; the tasks, numbers and limits are
in [docs/bench.md](docs/bench.md).

## Built for agents

- **Process verbs, not fields.** The agent says what happened (`start`,
  `wait`, `ready`); the verb knows the target status, the allowed starting
  statuses, the required arguments, the comment to leave and what else to do
  (sprint, labels, assignee, blocked-by links, closing).
- **One line out, errors that say what to do.** A write prints what was,
  what is now and what was done along the way, so the agent need not re-read
  the issue. An error names the allowed values and the command to run next,
  and each [exit code](docs/output.md#exit-codes) maps to one action: fix the
  arguments (1), ask a human (2), check the number (3), retry later (4),
  re-run the same command (5).
- **Safe retries.** Repeating a command never posts a second comment or
  creates a second issue, and finishes what a failed run left undone.
- **Self-describing.** `gh dojo describe` prints the project's verbs, when to
  use each, the lifecycle and the exit codes in a few lines; `gh dojo next`
  answers "what do I take now".
- **Instructions written from the config.** [`gh dojo agent install`](docs/commands.md#agent-install)
  writes a Claude Code skill, an `AGENTS.md` section or Copilot instructions,
  and `--check` in CI catches instructions that fell behind the config.
- **The human decides what is theirs.** A verb with
  [`confirm = "user"`](docs/configuration.md#checks) runs only after the agent
  asks the user, and [`[session]`](docs/configuration.md#session) links each
  comment to the agent session that wrote it.

## Install

```sh
gh extension install Trum-ok/gh-dojo
gh extension upgrade dojo
```

Prebuilt binaries for macOS, Linux and Windows (amd64 and arm64); to build
from source, see [CONTRIBUTING.md](CONTRIBUTING.md#install-from-source).
gh-dojo uses gh's authentication (or `GH_TOKEN`). Projects need the `project`
scope: `gh auth refresh -s project`. Boards of users and of organizations are
both supported; for an organization with SAML single sign-on, the token must
also be authorized for it.

## Quick start

1. Give the board's status field the [kanban preset](docs/configuration.md#kanban-preset)
   statuses: Backlog, In progress, Waiting, Ready, Done. To count work in
   sprints, the [sprints preset](docs/configuration.md#sprints-preset) uses
   the same statuses and an iteration field named Sprint.
2. Write `.github/dojo.toml` with [`init`](docs/commands.md#init), in the
   repository:

   ```sh
   gh dojo init                          # lists the owner's projects
   gh dojo init --owner acme --project 3 # writes the config, checks it against the board
   gh dojo init --owner acme --project 3 --preset sprints
   ```

3. Check the setup, read the process the way an agent will, and point your
   agent at it:

   ```sh
   gh dojo doctor          # config, token scope, board, repositories: ok or a fix
   gh dojo describe        # verbs, when to use them, lifecycle, exit codes
   gh dojo agent install   # a Claude Code skill; --agent agents-md,copilot for others
   ```

4. Work:

   ```sh
   gh dojo next            # issues to take: Backlog or no status, not blocked
   gh dojo list            # open issues on the board, grouped by status
   gh dojo show 42         # read an issue before working on it
   gh dojo start 42        # Backlog → In progress, with a comment; --show prints the card too
   gh dojo wait 42 --by 17 # In progress → Waiting, blocked by #17, "Waiting for #17."
   gh dojo ready 42        # → Ready: done and committed, not released
   gh dojo done 42 43      # → Done and closed: released
   ```

## Commands

| Command | What it does |
|---------|--------------|
| [`show`](docs/commands.md#show) | Read issues: board status, sprint, blockers, history, description, comments |
| [`list`](docs/commands.md#list) | One line per open issue on the board, grouped by status |
| [`next`](docs/commands.md#next) | Issues to take: initial status, no unresolved blockers, in board order |
| [`new`](docs/commands.md#new) | Create an issue with labels, milestone, blockers and parent, on the board, in the initial status |
| [`comment`](docs/commands.md#comment) | Comment without changing the status |
| [`block`, `unblock`](docs/commands.md#block-unblock) | Add or remove blocked-by links without changing the status |
| [`init`](docs/commands.md#init) | Write `.github/dojo.toml` for a board and check it |
| [`describe`](docs/commands.md#describe) | Print this project's process, for an agent's context |
| [`doctor`](docs/commands.md#doctor) | Check config, token, board and repositories, with fixes |
| [`agent install`](docs/commands.md#agent-install) | Write the process into a skill or `AGENTS.md` section for agents |
| [`<verb>`](docs/commands.md#process-verbs) | The project's process verbs, from the config (`start`, `wait`, …) |
| [`release`](docs/commands.md#release) | Apply a verb to the issues a release includes, found through pull requests and commits |

Every command prints its usage with `--help` and takes `--json`, `--jq` and
`--template` ([JSON output](docs/output.md#json-output)).

## Configuration

A preset gives the verbs; the config names the board and changes or adds
verbs key by key:

```toml
preset = "kanban"            # start, wait, ready, done

[board]
owner = "acme"               # user or org login that owns the project
project = 3                  # project number from its URL

[verbs.start]
blockers = "deny"            # refuse while a blocker is unresolved

[verbs.review]               # a new verb: gh dojo review 42 --by @alice
when = "A pull request is open and waits for review"
from = ["In progress"]
to = "In review"
require = ["by"]
comment = "Review by {by}."
```

Every key, the checks and effects of a verb, comment templates and the
presets: [docs/configuration.md](docs/configuration.md). Complete, commented
configs to start from are in [`examples/`](examples).

## Documentation

- [Commands](docs/commands.md): every command, its flags and output.
- [Configuration](docs/configuration.md): the config format, verbs, presets,
  a process of your own.
- [Output and exit codes](docs/output.md): text output, exit codes, several
  issues at once, `--json`.
- [Contributing](CONTRIBUTING.md): building, tests, golden files, releases.

## Status

gh-dojo tracks its own development with gh-dojo: its board is driven by
[`.github/dojo.toml`](.github/dojo.toml), and its agents read the skill
[`agent install`](docs/commands.md#agent-install) generated from it,
[`.claude/skills/gh-dojo/SKILL.md`](.claude/skills/gh-dojo/SKILL.md). The
config (`version = 1`) and the JSON output (`schema_version: 1`) are
versioned. See the [releases](https://github.com/Trum-ok/gh-dojo/releases);
what comes next, such as hooks for verbs or several agents on one board, is
still an [open question](https://github.com/Trum-ok/gh-dojo/issues?q=is%3Aopen+label%3Aquestion).

## License

[MIT](LICENSE)
