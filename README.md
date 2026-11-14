# gh-dojo

[![CI](https://github.com/Trum-ok/gh-dojo/actions/workflows/ci.yml/badge.svg)](https://github.com/Trum-ok/gh-dojo/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Trum-ok/gh-dojo)](https://github.com/Trum-ok/gh-dojo/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/Trum-ok/gh-dojo)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

A [GitHub CLI](https://cli.github.com/) extension that gives AI agents process
verbs over GitHub Projects v2. An agent runs `gh dojo start 42`, and gh-dojo
knows the board's fields, statuses and rules from the repository's
`.github/dojo.toml`.

```console
$ gh dojo start 42
#42 Backlog → In progress, comment left
```

**Why.** With plain `gh`, moving an issue on a Projects board means
`gh project item-edit` or GraphQL with project, field and option IDs. Agents
spend turns looking those up, read verbose output and invent the process as
they go: they forget to leave a reason for waiting or move an issue from the
wrong status. gh-dojo keeps the process in the repository as data, and turns
it into commands:

- **Process verbs, not fields.** The agent says what happened (`start`,
  `wait`, `ready`); the verb knows the target status, the allowed starting
  statuses, the required arguments, the comment to leave and what else to do
  (sprint, labels, assignee, blocked-by links, closing).
- **One-line confirmations** and **short errors** that name the allowed values
  and the command to run next, with [exit codes](#exit-codes) an agent can act on.
- **Safe retries.** Repeating a command never posts a second comment or
  creates a second issue, and finishes what a failed run left undone.
- **Self-describing.** `gh dojo describe` and `gh dojo --help` print this
  project's verbs, when to use each, the lifecycle and the exit codes.

**Status:** v0.1, the first public release. gh-dojo tracks its own
development with gh-dojo ([`.github/dojo.toml`](.github/dojo.toml)). The
config (`version = 1`) and the JSON output (`schema_version: 1`) are
versioned. See the [roadmap](#roadmap).

**Contents:** [Install](#install) · [Quick start](#quick-start) ·
[Commands](#commands) · [Configuration](#configuration) ·
[Output and exit codes](#output-and-exit-codes) · [Agents](#agents) ·
[Development](#development) · [Roadmap](#roadmap) · [License](#license)

## Install

From the first release (v0.1.0) on, install the prebuilt binary (macOS, Linux
and Windows; amd64 and arm64) and update it with gh:

```sh
gh extension install Trum-ok/gh-dojo
gh extension upgrade dojo
```

Before that, or to run the latest `main`, install from source; this needs Go
(the version in `go.mod`) and `gh`:

```sh
git clone https://github.com/Trum-ok/gh-dojo
cd gh-dojo
make install
gh dojo --version
```

`make install` builds into `~/.local/share/gh-dojo/gh-dojo` (override with
`INSTALL_DIR`, whose last element must be `gh-dojo`) and installs the extension
from there, so the installed `gh dojo` does not change when you switch
branches in the working copy. Re-run it to update. A different `gh dojo`
extension already installed is replaced, and put back if the install fails.
`gh dojo --version` shows the release tag, or the `git describe` of a source
build.

On Windows `make install` is not available. Build in the clone (its directory
must be named `gh-dojo`) from Git Bash and install it as a local extension;
gh runs local extensions through Git for Windows' `sh`, and they follow the
working copy, so rebuild after switching branches:

```sh
go build -ldflags "-X main.version=$(git describe --always --dirty)" -o gh-dojo.exe ./cmd/gh-dojo
gh extension install .
```

gh-dojo uses gh's authentication (or `GH_TOKEN`). Projects need the `project`
scope: `gh auth refresh -s project`. Boards of users and of organizations are
both supported; for an organization with SAML single sign-on, the token must
also be authorized for it.

## Quick start

1. Give the board's status field the [kanban preset](#kanban-preset)
   statuses: Backlog, In progress, Waiting, Ready, Done. To count work in
   sprints, the [sprints preset](#sprints-preset) uses the same statuses and
   an iteration field named Sprint.
2. Write `.github/dojo.toml` with [`init`](#init), in the repository:

   ```sh
   gh dojo init                          # lists the owner's projects
   gh dojo init --owner acme --project 3 # writes the config, checks it against the board
   gh dojo init --owner acme --project 3 --preset sprints
   ```

   or by hand (more in [`examples/`](examples)):

   ```toml
   preset = "kanban"

   [board]
   owner = "acme"          # user or org login that owns the project
   project = 3             # project number from its URL
   ```

3. Check the setup and read the process the way an agent will:

   ```sh
   gh dojo doctor          # config, token scope, board, repositories: ok or a fix
   gh dojo describe        # verbs, when to use them, lifecycle, exit codes
   ```

4. Work:

   ```sh
   gh dojo next            # issues to take: Backlog or no status, not blocked
   gh dojo list            # open issues on the board, grouped by status
   gh dojo show 42         # read an issue before working on it
   gh dojo start 42        # Backlog → In progress, with a comment
   gh dojo wait 42 --by 17 # In progress → Waiting, blocked by #17, "Waiting for #17."
   gh dojo ready 42        # → Ready: done and committed, not released
   gh dojo done 42 43      # → Done and closed: released
   ```

## Commands

| Command | What it does |
|---------|--------------|
| [`show`](#show) | Read issues: board status, sprint, blockers, history, description, comments |
| [`list`](#list) | One line per open issue on the board, grouped by status |
| [`next`](#next) | Issues to take: initial status, no unresolved blockers, in board order |
| [`new`](#new) | Create an issue with labels, milestone and blockers, on the board, in the initial status |
| [`comment`](#comment) | Comment without changing the status |
| [`block`, `unblock`](#block-unblock) | Add or remove blocked-by links without changing the status |
| [`init`](#init) | Write `.github/dojo.toml` for a board and check it |
| [`describe`](#describe) | Print this project's process, for an agent's context |
| [`doctor`](#doctor) | Check config, token, board and repositories, with fixes |
| [`agent install`](#agent-install) | Write the process into a skill or `AGENTS.md` section for agents |
| [`<verb>`](#process-verbs) | The project's process verbs, from the config (`start`, `wait`, …) |
| [`release`](#release) | Apply a verb to the issues a release includes, found through pull requests and commits |

Issues are written `42` or `#42` for the default repository (the first of
[`board.repos`](#board)), and `owner/repo#42` or the issue's URL for any
other: in a verb, a batch, `show`, `comment`, both sides of `block` and
`unblock`, `wait --by` and `new --blocked-by`. A repository outside
`board.repos` is accepted too, since the command names it; a verb adds its
issue to the board like any other. The text names an issue of the default
repository `#42` and any other `owner/repo#42`; the JSON names every issue
in full, `owner/repo#42` ([JSON output](#json-output)). A verb's comment
names issues as GitHub links them from the issue's own repository
(`acme/shop#12` on an issue of acme/api), but free text in `--by` goes in
as is, so a `#12` in it links to the issue's repository. Every command
prints its usage with `--help` and takes `--json`, `--jq` and `--template`
([JSON output](#json-output)). Flags go before a `--`: anything after it is
an argument.

### show

```sh
gh dojo show <N>... [--brief] [--comments N|all] [--full]
```

Reads issues before working on them: state, board status and fields, the
sprint (with [`fields.sprint`](#fields): `sprint: Sprint 12 (2026-11-09 to
2026-11-22)`), milestone, labels, assignees, blockers both ways with their
board status, status history on the board, the description and the latest
comments. Times are UTC. Unresolved blockers (see
[`statuses.resolved`](#statuses)) are listed first; a linked issue is shown
with its status, "on the board, no status", "not on the board", or "status
not read" and why, and a blocker whose status was not read counts as
unresolved.

Long content is folded, always with a note saying what is hidden: the last 5
comments are printed (`--comments N` for the last N, `--comments all` for up
to 100); the description is cut at 2000 characters and the comments shown
share 1500. `--full` prints them whole. `--brief` prints three lines per
issue: title; state, status, sprint, milestone, labels and assignees;
blockers and counts.

Several issues are read in one request (up to 20 issues per request), from
any repositories: `gh dojo show 42 acme/api#7`. A missing issue, a pull
request number or a missing repository is reported after the others are
printed, with exit code 3.

### list

```sh
gh dojo list [--status S]... [--label L]... [--mine] [--sprint current|TITLE|none] [--all] [--limit N]
```

One line per open issue, grouped by status in board order, issues without a
status first. Closed issues and issues in a final status
([`statuses.final`](#statuses)) are hidden and counted; `--all` shows them,
and so does `--status` naming a final status. `--status none` selects issues
without a status. Filters combine with AND, and the issues they leave out are
counted. Only issues are listed: draft issues and pull requests on the board
are counted. At most `--limit` issues are printed (default 50), with the
total.

`--sprint` needs [`fields.sprint`](#fields) and keeps the issues in one
iteration: `current` (the one covering today in [`board.timezone`](#board)),
a title, or `none` (no sprint). The first line names the iteration with its
days; an unknown title lists the iterations.

### next

```sh
gh dojo next [--label L]... [--mine] [--sprint current|TITLE|none] [--all] [--limit N]
```

What to work on next: the open issues in the initial status
([`statuses.initial`](#statuses)) or without a status, one line per issue as
in `list`, in the board's own order (issues without a status mixed with the
initial ones, no groups), leaving out the issues with an unresolved blocker.
A blocker is resolved when it is closed or on the board in a
[`statuses.resolved`](#statuses) status, archived or not: the rule of `show`
and the `blockers` check. A blocker not on the board, or whose place the
token cannot read, counts as unresolved. The blocked issues are counted
(`hidden: 2 blocked, use --all`); `--all` shows them too, each with its
blockers:

```text
$ gh dojo next
#34 Run the e2e tests on a sandbox board [enhancement]
hidden: 2 blocked, use --all
$ gh dojo next --all
#34 Run the e2e tests on a sandbox board [enhancement]
#41 Add the release command [enhancement] — blocked by #38 (In progress)
#42 Configure another flow without code [documentation] — blocked by #40 (In progress)
```

`--label`, `--mine` and `--sprint` filter as in [`list`](#list), and the
issues they leave out are counted. At most `--limit` issues are printed
(default 5), with the total. Closed issues and issues in other statuses are
not candidates; draft issues, pull requests and items the token cannot see in
the initial status or without one are counted as skipped. Blockers are read
with the board's items, so `next` usually takes as many requests as `list`;
when a candidate's open blocker is not among the board's items (not on the
board, or archived there), one more request reads where those blockers
stand. It needs `statuses.initial`: without it, it is a config error (exit
code 1).

### new

```sh
gh dojo new <title> [--type T] [--area A]... [--label L]... [--milestone M] [--blocked-by N]... [--repo owner/repo] [--body TEXT | --body-file FILE]
```

Creates an issue, labels it, adds it to the board and sets the initial status
([`statuses.initial`](#statuses)) in one command:

- `--type` takes one of [`labels.types`](#labels) (or any repo label outside
  the areas when that list is empty), `--area cli` adds the label
  `<labels.area_prefix>cli`, `--label` adds any existing label;
- `--milestone v1.2` puts the issue in the open milestone titled `v1.2`
  (ignoring case);
- `--blocked-by 17` marks it blocked by #17 (repeatable, or comma-separated;
  `owner/repo#17` or a URL for another repository);
- `--repo acme/api` creates it in another repository of
  [`board.repos`](#board) than the first: its labels, milestones and repeat
  check are that repository's, the issue is named `acme/api#15`, and a bare
  `--blocked-by` number is still in the default repository. A repository
  not in `board.repos` is refused (exit code 1, listing them);
- `--body-file -` reads the body from stdin. A title starting with `-` goes
  after `--`: `gh dojo new --type bug -- "-v is ignored"`.

An unknown label or milestone fails before anything is created, listing the
allowed values (exit code 1); so does a `--blocked-by` issue that does not
exist, or a pull request (exit code 3).

```console
$ gh dojo new "Fix login redirect" --type bug --area cli
#15 "Fix login redirect" created, labels bug, area:cli, no status → Backlog
```

A repeat is safe: the body ends with a hidden marker of the title, and a run
that finds it on one of the repository's 20 newest open issues written by
you finishes that issue (labels, milestone, board, status, blocked-by links)
instead of creating another; a different milestone it already has is kept,
with a warning. If a run fails after creating the issue, it exits with code
5: run the same command again.

### comment

```sh
gh dojo comment <N> [text] [--body-file FILE]
```

Comments on an issue without changing its status. The text comes from the
second argument or from `--body-file` (`-` reads stdin), exactly one of them.
Text starting with `-` goes after `--`: `gh dojo comment 42 -- "-1, still
broken"`. When the issue's last comment is yours with the same text, it is
not posted again.

### block, unblock

```sh
gh dojo block <N> --by <M>...
gh dojo unblock <N> --by <M>...
```

Marks issue N blocked by the issues `--by` names, or removes those links,
without changing its status. N and `--by` take `12`, `"#12"`, `owner/repo#12`
or the issue's URL; repeat `--by` or separate the issues with commas. A link already
there (`block`) or already gone (`unblock`) is not an error, and the line
says so. An issue that does not exist, or a pull request, fails before
anything is written (exit code 3); an issue cannot block itself, nor an
issue it already blocks: GitHub refuses cycles (exit code 1).

```console
$ gh dojo block 42 --by 17,18
#42 blocked by #18, already blocked by #17
$ gh dojo unblock 42 --by 17
#42 no longer blocked by #17
$ gh dojo block acme/api#7 --by 42
acme/api#7 blocked by #42
```

Verbs check and add the same links: see [`blockers`](#checks).

### init

```sh
gh dojo init [--owner LOGIN] [--project N] [--preset kanban|sprints] [--repo OWNER/NAME] [--force] [--dry-run]
```

Writes `.github/dojo.toml` at the repository root, without questions.
Without `--project` it lists the owner's projects (a user or an
organization; `--owner` defaults to the current repository's owner), one
line each, and exits with code 1 and the command to run:

```console
$ gh dojo init --owner acme
1  Roadmap  (12 items, closed)
3  Shop  (48 items)
error: choose a project: --project is required (organization acme has 2 projects, listed above)
fix: gh dojo init --owner acme --project 3
```

With `--project` it writes the board (`owner`, `project`, and `repos` set to
`--repo` or the current repository) and the preset (`kanban` by default),
nothing the preset already provides, then checks the file against the board.
Each status or field the board lacks is listed with its fix: add it on the
board, or override the keys that name it, e.g. `[verbs.wait] to = "…"`. The
file is kept, and the exit code is 1 until the board and the file match. An
existing config is not overwritten without `--force`; `--dry-run` prints the
file instead.

### describe

```sh
gh dojo describe
```

Prints the project's process in a few lines per verb, short enough for an
agent's context: when to use each verb, the arguments it needs or takes, what
it checks and does along the way (comment, sprint, labels, assignee, closing
or reopening, blocked-by links, the blockers check, closed issues, the
user's go-ahead), then the lifecycle (from → to), the initial, final and
resolved statuses, and the exit codes; with an initial status it points at
[`next`](#next) to find an issue. It reads only the config, never the
network; a missing or broken config is an error (exit code 1).
`gh dojo <verb> --help` has the details of one verb.

### doctor

```sh
gh dojo doctor
```

Checks the setup and prints one line per check, `ok`, `problem`, `warn`
(worth fixing, but not a problem), or `skip` when an earlier problem keeps
a check from running; each problem and warning is followed by the command
that fixes it:

```console
$ gh dojo doctor
ok       version  gh dojo v0.1.0
ok       config   .github/dojo.toml: preset sprints, 6 verbs
ok       token    github.com as octocat, with the project scope
ok       board    octocat/3 "Shop": field Status and 5 statuses match
ok       repo     octocat/shop (default): visible, issues enabled
ok       labels   octocat/shop: 4 area labels (area:…)
problem  sprint   Sprint has no iteration for today (2026-12-20, UTC)
                  fix: add one in the Sprint field settings: https://github.com/users/octocat/projects/3/settings
ok       verbs    octocat/shop: 2 labels exist, 1 assignee can be assigned
```

| Check | What it checks |
|-------|----------------|
| `version` | The installed gh-dojo version |
| `config` | `.github/dojo.toml` loads |
| `token` | gh is logged in, and the token has the `project` scope (from `GH_TOKEN`: "set GH_TOKEN to …") |
| `board` | The board exists, is open, and has every field and status the config names, each mismatch on its own line |
| `repo` | Each repository of [`board.repos`](#board), or the current one, is visible with issues enabled; a warning when `board.repos` spells one otherwise than GitHub does |
| `labels` | With [`labels.types`](#labels) or `labels.area_prefix` set, in each repository of the `repo` check: every type label exists (missing is a problem, since `new --type` fails on it, fixed by `gh label create`; spelled in another case is `warn`, since `new` matches labels regardless of case, fixed by `gh label edit`), and how many labels start with the area prefix. None is `warn` when `.github/dojo.toml` sets the prefix, since `new --area` finds nothing (create one, or set the prefix to `""`); with the prefix from a preset, which many teams never use, it is a note on the `ok` line. Past 100 labels the area count is a lower bound ("at least"), and none found is "could not confirm" rather than a warning |
| `sprint` | With [`fields.sprint`](#fields): an iteration covers today, a date in [`board.timezone`](#board) |
| `verbs` | When verbs have labels or assignees: the labels exist in the default repository, spelled exactly as there, and the users can be assigned |
| `agent` | With [`[agent]`](#agent) set (and `targets` not empty), or a file with a gh-dojo marker: the files [`agent install`](#agent-install) writes are up to date (`agent install --check`, without the diff). Out of date or missing is `warn`, since the text also depends on the gh-dojo version; a hand-written skill under `[agent]` is a problem |

It sends one request for the token and the repositories, one more for the
board when its schema is not cached, one for the labels when
`labels.types` or `labels.area_prefix` is set (plus a repository's labels
page by page when it has over 100 and a type is not found), and one for
the verbs' labels and assignees when a verb has them. The exit code is 0
when no check is a problem (warnings included), 4 when one failed on auth
or the network, else 1.

### agent install

```sh
gh dojo agent install                          # the agents of [agent] targets, by default claude
gh dojo agent install --agent claude,agents-md # these agents instead
gh dojo agent install --check                  # in CI: write nothing, fail when out of date
```

Writes the project's process, built from the config, into the files agents
read, so the rules are written once, in the config:

| Agent | File |
|-------|------|
| `claude` | `.claude/skills/gh-dojo/SKILL.md`: a Claude Code skill, with a `description` that loads it whenever the agent deals with the project's issues |
| `agents-md` | A section of `AGENTS.md`, which Codex and other agents read |
| `copilot` | A section of `.github/copilot-instructions.md` |

The instructions are in English and say what `describe` says, with what an
agent should do about it: use only `gh dojo` for issues; the lifecycle, the
longest way from the initial status to a final one along the verbs (a status
a verb can jump over in parentheses); the initial, final and resolved
statuses; each verb with its `when`, the statuses it moves between, its
arguments, checks and effects, and the rules behind them (ask the user before
`--yes`, don't work around a refused blockers check, what `--by` links,
several numbers at once); `show`, `next` and `list`; `new`, `comment`,
`block` and `unblock`; safe repeats; the session link; exit codes; JSON; and
a table of the commands of the gh-dojo that wrote them, so a command added in
a later version shows up there by itself. The skill's `description` names the
repository, the board and the verbs, and [`[agent] description`](#agent)
adds to it. The markdown file [`[agent] notes`](#agent) names is appended as
it is, in the project's language, for what the config cannot say.

The skill is gh-dojo's whole file, marked as generated after its front
matter; a skill without that marker, written by hand, is not overwritten
(exit code 1): move what is worth keeping into the notes and delete it. In
`AGENTS.md` and the Copilot file gh-dojo owns only the lines from
`<!-- gh-dojo:begin ... -->` to `<!-- gh-dojo:end -->` (leading spaces
allowed), and the rest of the file stays byte for byte; a file without the
markers gets the section at its end. Marker lines count anywhere, fenced code
blocks included, so don't quote them in those files. Markers out of order are
an error, and nothing is written. A file with CRLF line endings keeps them.

Every file is written whole, through a temporary file next to it that is
renamed over it, with the file's permissions. Reads and writes stay inside
the repository: only relative symbolic links that point inside it are
followed (an absolute link is an error), and the target of a linked file is
what gets written. `[agent] notes` cannot be under `.git`, also through a
link, or be one of the files agent install writes.

A repeat with the same config writes nothing and prints `up to date` for
each file. `--check` writes nothing either: a file that is missing, differs
from what install would write, or is a hand-written skill is `missing`,
`out of date` (with up to 20 changed lines) or `hand written`, and the exit
code is 1 with the fix: `gh dojo agent install`, or for a hand-written skill
moving it into the notes first. Paths are relative to the repository root,
the directory that holds `.github`; agent install never uses the network.

The files depend on the config, the notes and the gh-dojo version (its text
and its command list), not on where it runs. So pin the version that checks
them in CI, and run `agent install` with that version when you upgrade:

```yaml
- run: gh extension install Trum-ok/gh-dojo --pin v0.2.0
  env:
    GH_TOKEN: ${{ github.token }}
- run: gh dojo agent install --check
```

[`doctor`](#doctor) only warns about files out of date: the installed
gh-dojo may be a different version than the one in CI. In this repository
CI checks with the build of the branch, so after changing the config, the
notes, the generator or a command's short help, run `make agent`.

### Process verbs

```sh
gh dojo <verb> <N> [--comment TEXT] [--by N|TEXT] [--assignee LOGIN] [--yes] [--dry-run]
gh dojo <verb> <N>... [flags]     # a verb with batch = true, such as done
```

Verbs come from [`[verbs]`](#verbs) in the config, so each project has its
own; the [kanban](#kanban-preset) and [sprints](#sprints-preset) presets
define `start`, `wait`, `ready` and `done`. `gh dojo --help` lists them with
their `when` line and the lifecycle; `gh dojo <verb> --help` shows the
allowed starting statuses, the target status, the required arguments, the
other effects and the comment template.

A verb takes one issue number (several with [`batch = true`](#several-issues-at-once))
and, in one command:

1. checks that the issue is open (a verb that moves it to a final status,
   closes it or reopens it also runs on a closed one; one that closes and
   has no `to`, only on an issue closed with its reason), that its status is one
   of the verb's `from` statuses (no status, or not on the board, counts as
   [`statuses.initial`](#statuses)), that the required arguments are given,
   the user's go-ahead (`confirm`) and the issue's blockers (`blockers`);
2. adds the issue to the board if it is not there;
3. applies its [effects](#effects): reopens it, sets the status and the
   sprint, sets other board fields, adds and removes labels, assigns and
   unassigns users, marks it blocked by the `--by` issues, posts the rendered
   [comment template](#comment-templates) and closes it.

Each effect already in place is skipped: a status already set, labels
already there or gone, a comment that is already the latest gh-dojo comment,
an issue already closed (whatever its reason), and so on. Everything is
written in one GraphQL mutation (two when the issue is not on the board yet:
the status needs the new board item), so a verb on an issue on the board
costs one read and one write.

`--comment` and `--by` fill `{comment}` and `{by}` in the template; a verb
whose template does not use one of them rejects that flag. `--by` takes an
issue number (rendered `#12`) or free text; with
[`blockers = "link"`](#checks) issues are also marked as blocking this one.
`--assignee` (a login, or `@me`) exists only on a verb with
[`assign = "{assignee}"`](#effects), and is required there: the user to
assign, also `{assignee}` in the template. `--yes` exists only on a verb
with [`confirm = "user"`](#checks).
`--dry-run` prints the plan without changing anything:

```console
$ gh dojo wait 42 --by 17 --dry-run
dry run: wait #42 "Fix login redirect"
  status: In progress → Waiting
  blocked by: #17
  comment: "Waiting for #17."

$ gh dojo drop 42 --comment "Duplicate of #17."
error: drop needs the user's go-ahead for #42 (would be: comment left, closed as not planned)
this is the user's decision: ask the user, and re-run with --yes only if they agree

$ gh dojo drop 42 --comment "Duplicate of #17." --yes
#42 comment left, closed as not planned
```

The board's built-in workflows run after the verb and may change the status
it set: "Item closed" moves an issue a verb closed (to Done by default),
"Item reopened" moves one it reopened. Turn them off in the board's workflow
settings, or point them at the statuses your verbs set. A verb that closes
the issue and has no `to` skips the `from` check on an issue closed with the
verb's reason, so its repeat is not refused because a workflow moved the
issue; an issue closed with another reason it refuses, whatever its status.
A verb that
moves an issue closed as not planned or duplicate to a final status warns on
stderr and keeps the reason.

A verb with `batch = true` (the presets' `done`) takes several numbers:
`gh dojo done 12 13 14`. Each issue is checked and written on its own; see
[Several issues at once](#several-issues-at-once) for the summary and the
exit code. [`release`](#release) runs it on the issues of a release.

### release

```sh
gh dojo release <ref> --verb <verb> [--since <ref>] [--dry-run] [--yes] [--comment TEXT]
```

Once a release is tagged, runs a verb with [`batch = true`](#several-issues-at-once)
on the issues it includes, as `gh dojo done 12 13 14` would: same checks,
summary and exit codes.

**The range** is the commits of `<ref>` (a tag, branch or commit on GitHub;
no local checkout) since `--since`, or else since the previous release: the
highest version tag (`v1.2.3`, `1.2.3`) below `<ref>` when `<ref>` is one,
else the highest not on `<ref>`'s commit; one that is not in `<ref>`'s
history, like a `--since` newer than `<ref>`, is exit code 1, not an empty
release. Prereleases count only for a
prerelease, so `v0.2.0` starts from `v0.1.0`, past `v0.2.0-rc.1`: an issue
already done costs no write, a range too short misses issues. The first
release needs `--since` (exit code 1 without it): taking the whole history
would apply the verb to every issue any commit ever named. GitHub compares a
tag or branch with `<ref>` exactly; from a commit (a SHA, `HEAD~10`), which
the API cannot compare, `<ref>`'s history is read down to it, exact for a
linear history (with merge commits, a merged branch's commits older than the
start are missed, but their pull request is found through the merge
commit). At most 1000 commits; a longer range is exit code 1.

**An issue is in the release** when a merged pull request a commit belongs
to closes it (its closing keywords or linked issues, `closingIssuesReferences`;
squash, merge and rebase alike), or a commit message names it after a word
of [`[release] keywords`](#release-keywords), by default GitHub's closing
keywords: `Fixes #12`, `closes: acme/api#3`, `Resolves <issue URL>`, any
case; a list after a keyword counts whole up to the end of its line (`Fixes
#12, #13 and #14`), a mention without one (`see #12`) does not. `Refs #12` says a commit is about
an issue, which may be done only in part, so `refs` is not a default; a
project that commits without pull requests adds it, as this one does. A
number a message names that is a pull request is left out with a warning.

**Not ready.** An open issue whose status the verb's `from` does not allow,
such as one still in progress with a `Refs` commit when `done` starts only
from Ready, is listed as not ready and left out, not failed: work goes on
across releases, and a refusal (exit code 2) would end every such release.
Only a later release whose commits name it again picks it up; otherwise run
the verb on it (`gh dojo done 12`) once it is ready. A closed issue refused
by `from` (closed by a `Fixes #12` while in progress, say) is a refusal
(exit code 2): no release would move it. The verb's other checks (blockers,
`--yes`) fail the batch as well.

```console
$ gh dojo release v0.2.0 --verb done --dry-run
release v0.2.0: 5 commits since v0.1.0, 4 issues
not ready (done starts only from Ready): #17 (In progress)
  #3: commit 1111111
  #7: commit 1212121, commit a1a1a1a
  #9: PR #50
dry run: done #3 "Read the config"
  status: Ready → Done
  close: as completed
...
```

`--dry-run` lists how each issue was found (`PR #N`, `commit <sha>`) and its
plan. No issues is a line saying where release looked, exit code 0.
`--comment`, `--by` and `--yes` go to the verb, for every issue. A verb
without `batch = true` is exit code 1, a `<ref>` or `--since` GitHub does not
know exit code 3. Requests: one for `<ref>` and the start (one per 100
tags), one per 100 commits with their pull requests, then the batch's.

## Configuration

gh-dojo reads `.github/dojo.toml`, looking in the current directory and then
in each parent up to the repository root (the first directory with `.git`).
[`gh dojo init`](#init) writes one for a board. Unknown keys are errors, and
every error names the file and the key.

Complete, commented configs to start from:
[`examples/kanban.toml`](examples/kanban.toml),
[`examples/sprints.toml`](examples/sprints.toml) and, for a process of
your own without a preset, [`examples/review.toml`](examples/review.toml)
(see [Your own process](#your-own-process)). Every key, in one file:

```toml
version = 1                  # optional; 1 is the only version
preset = "kanban"            # optional: "kanban" or "sprints", verbs and defaults to build on

[board]
owner = "acme"
project = 3
repos = ["acme/shop"]        # the first one is the default repo
assignee = "@me"             # who `assign = true` assigns
timezone = "Europe/Moscow"   # the sprint's "today"; UTC when not set

[fields]                     # logical name = field name on the board
status = "Status"            # required (the presets set "Status")
sprint = "Sprint"            # optional: an iteration field (the sprints preset sets "Sprint")

[statuses]
initial = "Backlog"          # set by `new`; assumed for issues without a status
final = ["Done"]             # hidden by `list`, with a counter
resolved = ["Ready", "Done"] # a blocker in these no longer blocks

[labels]
types = ["bug", "feature"]   # labels `new --type` accepts; empty = any label without area_prefix
area_prefix = "area:"        # `new --area cli` → label "area:cli"

[session]                    # fills {session} in comment templates
env = ["CLAUDE_CODE_HOST_SESSION_ID"]   # the first set variable wins
link = "claude://claude.ai/epitaxy/{id}"

[agent]                      # gh dojo agent install
targets = ["claude", "agents-md"]       # claude (the default), agents-md, copilot
notes = "docs/agent-notes.md" # appended as is to the generated instructions
description = "Also load it before a release."  # added to the skill's description

[release]                    # gh dojo release
keywords = ["closes", "fixes", "resolves", "refs"]   # words before #12 in a commit message; default: GitHub's closing keywords

[verbs.start]                # overrides the preset's start key by key
when = "You begin working on the issue"   # one line; first line of --help
blockers = "deny"            # refuse while a blocker is unresolved (the preset warns)
comment = "Started. Session: {session}"

[verbs.review]               # a new verb: gh dojo review <N>
when = "A pull request is open and waits for review"
to = "In review"             # target status
from = ["In progress"]       # allowed current statuses; empty = any
labels = ["+in-review", "-blocked"]       # + adds a label, - removes it
assign = true                # board.assignee; or "@me", a login, or "{assignee}" (--assignee)
require_one_of = ["by", "comment"]  # --by or --comment must be given
comment = """
Review by {by}.

{comment}"""

[verbs.drop]
when = "The issue is not needed or a duplicate"
close = "not_planned"        # completed, not_planned or duplicate
require = ["comment"]        # without a template, --comment is the comment
confirm = "user"             # runs only with --yes, after asking the user

[verbs.revive]
when = "A closed issue is needed after all"
to = "Backlog"
reopen = true

[verbs.ship]
when = "The change is released"
to = "Done"
iteration = "by_status_date:Ready"  # the sprint in which it became Ready
batch = true                 # gh dojo ship 12 13 14

[verbs.plan]
when = "The issue is taken into the current sprint"
iteration = "current"        # or "clear"

[verbs.plan.field]           # board fields set to fixed values
Priority = "P1"              # single select: an option name
Estimate = 3                 # number; a text field takes a string, a date field 2026-11-30

[verbs.done]
close = false                # keep the issue open: false turns off the preset's close

[verbs.ready]
disabled = true              # drop a verb that the preset defines
```

### Top level

| Key       | Required | Meaning                                                      |
|-----------|----------|--------------------------------------------------------------|
| `version` | no       | Config version; `1` is the only one                          |
| `preset`  | no       | A built-in preset to build on: [`kanban`](#kanban-preset) or [`sprints`](#sprints-preset) |

### board

| Key        | Required | Meaning |
|------------|----------|---------|
| `owner`    | yes      | User or organization login that owns the project; which one is detected |
| `project`  | yes      | Project number from its URL, 1 or greater |
| `repos`    | no       | `owner/name` list; the first is the default repo for bare issue numbers and `new`, the others are reached with `owner/repo#N` and `new --repo`. Empty: the current repo (`GH_REPO`, else the git remote) |
| `assignee` | no       | Who a verb with `assign = true` assigns, or `unassign = true` removes: `"@me"` (the authenticated user) or a login |
| `timezone` | no       | An IANA time zone, like `"Europe/Moscow"`; UTC when not set. The [`iteration`](#effects) effect, `list --sprint current`, `next --sprint current` and `doctor` take today, and the day an issue entered a status, as dates in it |

### fields

Logical name = field name on the board.

| Key      | Required | Meaning |
|----------|----------|---------|
| `status` | yes      | The status field: a single-select field |
| `sprint` | no       | The sprint field: an iteration field. The [`iteration`](#effects) effect, `list --sprint`, `next --sprint`, the sprint line of `show` and `doctor` use it |

Other logical names are rejected; a verb sets any other board field by its
own name with the [`field`](#effects) effect. Logical names are lowercase:
`Status = "Status"` is an error suggesting `status`.

### statuses

| Key        | Meaning |
|------------|---------|
| `initial`  | Set by `new`; issues without a status or not on the board count as this one for `from`. `next` shows the issues in it, or without a status |
| `final`    | Statuses `list` hides, with a counter |
| `resolved` | Statuses in which an open blocker no longer blocks (`show`, the `blockers` check, `next`): its work is done though the issue stays open (kanban: Ready, Done). A closed blocker is always resolved |

### labels

| Key           | Meaning |
|---------------|---------|
| `types`       | Labels `new --type` accepts. Empty: any repo label that does not start with `area_prefix` |
| `area_prefix` | Prepended by `new --area`: `--area cli` → `area:cli`. Types may not start with it |

### session

Fills `{session}` in comment templates, so a comment links back to the agent
session that wrote it.

| Key    | Meaning |
|--------|---------|
| `env`  | Environment variables holding the session ID; the first one that is set wins |
| `link` | Link template; must contain `{id}`. Default: `{id}`, the bare ID |

When `env` is set but none of its variables is, `{session}` is empty and
gh-dojo warns; the verb still runs.

### agent

What [`agent install`](#agent-install) writes. A preset cannot set it.

| Key       | Meaning |
|-----------|---------|
| `targets` | The agents to write for: `claude`, `agents-md`, `copilot`. Default: `["claude"]`; `[]` writes nothing without `--agent` |
| `notes`   | A markdown file, relative to the repository root and written with `/`, appended as is after the generated instructions: what the config cannot say, in the project's language. Its headings land as written, so in an `AGENTS.md` section start them one level down. Not under `.git`, not one of the files agent install writes, and not a link out of the repository |
| `description` | One line added to the end of the skill's generated `description`, which tells Claude Code when to load the skill: say when else to load it. The whole description may be up to 1024 characters |

### release keywords

`[release]`: how [`gh dojo release`](#release) links a commit to issues besides the pull
requests it belongs to. A preset cannot set it.

| Key        | Meaning |
|------------|---------|
| `keywords` | Words that, followed by issues on the same line of a commit message (`Fixes #12`, `refs: #12, acme/api#3`), put those issues in the release; whole words, in any case, letters with single spaces or dashes (`"part of"`). Default: GitHub's closing keywords, `close`, `closes`, `closed`, `fix`, `fixes`, `fixed`, `resolve`, `resolves`, `resolved`; a list replaces them. `[]`: only pull requests count. With `refs`, keep the verb's `from` to Ready (the presets' `done` also starts from In progress): a `Refs` commit may be partial work, which is then [not ready](#release) rather than released |

### verbs

Each `[verbs.<name>]` table becomes `gh dojo <name>`. Verb names are
lowercase letters, digits and dashes and cannot shadow built-in or planned
commands (`show`, `list`, `new`, `comment`, `describe`, `init`, `doctor`,
`help`, `block`, `unblock`, `agent`, `next`, `release`).

| Key              | Kind   | Meaning |
|------------------|--------|---------|
| `when`           | —      | Required. One line saying when to use the verb; the first line of its `--help` |
| `from`           | check  | Statuses the verb may start from; empty or missing means any |
| `require`        | check  | Arguments that must all be given: `comment`, `by`, `assignee` |
| `require_one_of` | check  | Arguments of which at least one must be given |
| `blockers`       | check or effect | `"warn"` or `"deny"`: check the issue's unresolved blockers; `"link"`: `--by N` also marks it blocked by #N |
| `confirm`        | check  | `"user"`: the verb is the user's decision and runs only with `--yes` |
| `batch`          | —      | `true`: the verb takes several issue numbers at once |
| `to`             | effect | Target status |
| `iteration`      | effect | Set the sprint field: `"current"`, `"by_status_date:<status>"` or `"clear"`; needs `fields.sprint` |
| `field`          | effect | A table of other board fields to set: `[verbs.<name>.field]` with `Priority = "P1"`, `Estimate = 3` |
| `labels`         | effect | Labels to add (`"+name"`) and remove (`"-name"`): `["+in-review", "-blocked"]` |
| `assign`         | effect | Who to add to the assignees: `true` (`board.assignee`), `"@me"`, a login, or `"{assignee}"`: the user `--assignee` names when the verb runs |
| `unassign`       | effect | Who to remove from the assignees: `true` (`board.assignee`), `"@me"`, a login, or `"all"`: everyone but the user the verb assigns |
| `comment`        | effect | [Comment template](#comment-templates) |
| `close`          | effect | Close the issue with this reason: `completed`, `not_planned` or `duplicate` |
| `reopen`         | effect | `true` reopens a closed issue |
| `disabled`       | —      | `true` removes a verb the preset defines; it stands alone in its table |

A verb needs at least one effect: `to`, `comment`, `labels`, `assign`,
`unassign`, `close`, `reopen`, `iteration` or `field`. `comment` and `by` in
`require` and `require_one_of` must appear in the verb's comment template; a
verb that requires `comment` and has no template posts the `--comment` text
as is. `assignee` needs `assign = "{assignee}"`, which requires it anyway, so
it may only be repeated in `require`.
Keys from the design that are not implemented yet (`pre`, `post`) are
rejected with "not supported in this version".

#### Checks

- **from**: the issue's status must be one of them. It is skipped for an
  issue already in the verb's `to`, so a repeat is safe; so is `blockers`.
- **require**, **require_one_of**: without the arguments, exit code 1.
- **blockers** `"warn"` and `"deny"` look at the issue's unresolved
  blockers: open, and not in one of [`statuses.resolved`](#statuses); a
  blocker whose board status could not be read, or that was not read at all
  (hidden from the token; a verb reads up to 50, GitHub's limit), counts as
  unresolved. `"warn"` prints them on stderr and runs; `"deny"` refuses with
  exit code 2, listing them and asking you to ask the user. `--dry-run` warns
  or refuses the same way.
- **blockers** `"link"` is an effect: `--by` naming issues (`12`, `#12`,
  `owner/repo#12` or the issue's URL, comma-separated, rendered `#17, #18`)
  marks them as blocking this one, in the verb's one mutation; free text stays
  comment-only, and so does a pull request (with a warning). Each `--by`
  issue must exist (exit code 3), and cannot be the issue itself or an issue
  it already blocks (exit code 1); issues and text mixed are exit code 1 too.
  The verb's comment template must use `{by}`.
- **confirm** `"user"`: without `--yes` the verb exits with code 2 and asks
  you to ask the user; re-run it with `--yes` only once they agree. A repeat
  with nothing left to do needs no `--yes`. It is a signal, not a lock: an
  agent could add `--yes` itself, so tell agents to ask first (see
  [Agents](#agents)).

#### Effects

- **labels**: every entry starts with `+` or `-`; a name may not be both
  added and removed. Names must match the repository's labels exactly, case
  included: a label to add that is missing fails before anything is
  written, with the repository's labels as the allowed values (create it
  with `gh label create`; a name that differs only in case gets the spelling
  to use instead). A label to remove that the repository does not have is in
  place, unless the issue has it in another case. A verb that removes labels
  reads up to 100 of the issue's labels, GitHub's limit per issue.
  [`gh dojo doctor`](#doctor) checks every verb's labels.
- **assign**: adds one user and keeps the other assignees. `"@me"` is the
  authenticated user; a login must be able to be assigned issues in the
  repository, or the verb fails before writing (exit code 1), listing who
  can be. `assign = "{assignee}"` takes the user from `--assignee` when the
  verb runs, for a user that changes from issue to issue, such as a
  reviewer: `gh dojo review 24 --assignee octocat` (or `--assignee @me`).
  The flag is then required, checked the same way, and `{assignee}` in the
  comment template is the login as GitHub spells it. It is a flag of its
  own rather than `--by`, which names what an issue waits for; only a verb
  with `assign = "{assignee}"` takes it.
- **unassign**: removes users and keeps the others, in the same mutation:
  `"@me"` or a login removes that user (one not assigned is in place:
  `octocat already unassigned`), and `"all"` (in any case) removes everyone
  but the user the verb assigns, so `assign = true` with `unassign = "all"`
  hands the issue back to its author alone. `"@me"`, and `board.assignee =
  "@me"`, are whoever runs the verb: such a verb hands the issue back to its
  author only when the author (or the author's agent) runs it; run by the
  reviewer, it would keep the reviewer and remove the author. A verb never
  removes the user it assigns (the line says `octocat kept: the verb
  assigns them`), and cannot name one user in both keys. Users are removed
  before the verb assigns its own, since an issue has at most 10 assignees.
  Who to remove is always in the config: a verb cannot take it from the
  command line (`"all"` covers removing a reviewer named at run time), and
  a user named `all` cannot be named. [`gh dojo doctor`](#doctor) warns
  about a login of `unassign` that cannot be assigned: the verb removes
  current assignees, so such a login is likely a typo.
- **close**, **reopen**: a verb sets at most one of them. An issue already
  closed is not closed again, even with another reason; the line says
  `already closed as completed`. A verb that closes and has no `to` refuses
  an issue closed with another reason instead. `duplicate` marks the issue a duplicate
  without linking the original: name it in the comment.
- **iteration**: sets the [sprint field](#fields) in the same mutation as
  the status. Dates are in [`board.timezone`](#board) (UTC by default), and
  a completed iteration still covers its days.
  - `current` is the iteration covering today.
  - `clear` removes the value, and leaves an issue that is not on the board
    off it when that is the verb's only board effect.
  - `by_status_date:Ready` is the one covering the day the issue last
    entered Ready, from its status history on the board (changes from before
    the issue was added to the board are ignored), or today when the verb
    itself moves the issue to Ready. When the day is unknown (the history
    lags behind, or the entry is older than the last 20 status changes
    gh-dojo reads) or the issue never entered Ready (In progress straight to
    Done), the verb takes today's iteration and warns: `#42 never entered
    Ready; using today's sprint`. GitHub keeps a status history only for a
    board's built-in `Status` field, so `by_status_date` needs
    `fields.status = "Status"`; the config is refused otherwise.

  No iteration for today is an error (exit code 1) listing the iterations,
  with [`gh dojo doctor`](#doctor) to check; a cached board schema is read
  again first. A past day no iteration covers is an error too, unless the
  issue has a sprint already. An issue already in the target iteration (or
  without one, for `clear`) is left alone: `sprint unchanged (Sprint 11)`.
- **field**: `[verbs.<name>.field]` names board fields, each with a value:
  an option name for a single-select field, a string for a text field, a
  number for a number field, a date (`2026-11-30`, or the string
  `"2026-11-30"`) for a date field. The status and sprint fields are set
  with `to` and `iteration`, not here. Each field is its own write: a
  partial report names the fields set and those not set (`Priority not
  set`).
- **comment**: see [Comment templates](#comment-templates).

#### Turning preset keys off

A verb that overrides a preset verb sets only the keys it names. To turn
off a key the preset sets:

| Key | Off |
|-----|-----|
| `close` | `close = false` (kanban's `done` closes; this keeps issues open in Done) |
| `blockers` | `blockers = false` (no check, no link) |
| `confirm` | `confirm = false` |
| `assign` | `assign = false` |
| `unassign` | `unassign = false` |
| `reopen` | `reopen = false` |
| `batch` | `batch = false` |
| `iteration` | `iteration = ""` |
| `from` | `from = []` (any status) |
| `labels` | `labels = []` |
| a whole verb | `disabled = true`, alone in its table |

### Comment templates

Templates may use `{session}`, `{comment}` (the `--comment` text), `{by}`
(the `--by` value) and, in a verb with `assign = "{assignee}"`, `{assignee}`
(the login it assigns). A line whose placeholders all come out empty is
dropped, so `"Waiting for {by}."` disappears when `--by` is not given; runs
of blank template lines collapse, trailing spaces and leading and trailing
blank lines are cut, and indentation stays. A template that renders to
nothing posts no comment. There is no escape for a literal `{comment}`;
other braces such as `{}` are plain text. A verb whose template does not use
`{comment}` or `{by}` rejects `--comment` or `--by`.

Comments go to the issue as written, so write templates and `when` lines in
your project's language; the CLI itself speaks English.

### Checked against the board

Every command that talks to GitHub first checks the config against the
board: `fields.status` must name a single-select board field and
`fields.sprint` an iteration field, every status the config names
(`statuses.initial`, `statuses.final`, `statuses.resolved`, each verb's `to`,
`from` and `iteration = "by_status_date:<status>"`) must be one of its
options, and every value of a verb's `field` table must fit its board field.
A typo is an error with the board's statuses and a suggestion, not a silent
skip. The board schema is cached for an hour in `~/.cache/gh-dojo` (or
`$XDG_CACHE_HOME/gh-dojo`; override with `GH_DOJO_CACHE_DIR`); a mismatch
re-reads the board before it is reported, so a status just added on the
board is picked up.

### Presets

With `preset = "kanban"` or `preset = "sprints"` the file is applied on top
of the preset: `[fields]`, `[statuses]`, `[labels]` and `[session]` override
key by key; a verb with a preset's name overrides only the keys it sets; a
new name adds a verb; `disabled = true` removes a preset verb. Without
`preset` every verb is defined in the file. `when` lines and comments are in
English: override them to write them in your project's language, as
[`examples/kanban.toml`](examples/kanban.toml) and this repository's own
[`.github/dojo.toml`](.github/dojo.toml) do.

Both presets set `fields.status = "Status"`, `statuses.initial = "Backlog"`,
`statuses.final = ["Done"]`, `statuses.resolved = ["Ready", "Done"]` (a
blocker in Ready is done, so it no longer blocks, though its issue stays
open until Done), `labels.types = []` (any label without the area prefix is
a type) and `labels.area_prefix = "area:"`.

### Kanban preset

The board's status field must have these options (other options are fine):

| Status      | Meaning                                              |
|-------------|------------------------------------------------------|
| Backlog     | Not started (initial status of new issues)           |
| In progress | Being worked on                                      |
| Waiting     | Stuck on another issue or an outside decision        |
| Ready       | Done, verified and committed, but not released yet   |
| Done        | Released (hidden by `list`)                          |

| Verb    | Moves to    | From                    | Needs                 | Also                                   | Comment                      |
|---------|-------------|-------------------------|-----------------------|----------------------------------------|------------------------------|
| `start` | In progress | Backlog, Waiting, Ready |                       | warns about unresolved blockers        | session link, `--comment`    |
| `wait`  | Waiting     | In progress             | `--by` or `--comment` | `--by 17` marks it blocked by #17      | "Waiting for …", `--comment` |
| `ready` | Ready       | In progress, Waiting    |                       |                                        | `--comment`                  |
| `done`  | Done        | Ready, In progress      |                       | closes as completed; several issues    | `--comment`                  |

- `start` warns on stderr about blockers not yet Ready, Done or closed
  (`blockers = "warn"`) and still runs; set `blockers = "deny"` to refuse,
  or `false` to skip the check.
- `wait --by 17` also marks the issue blocked by #17, `--by 17,18` by both
  (`blockers = "link"`); `wait --by "the API team"` only comments.
- `done` closes the issue as completed (Done means released) and takes
  several issues at once (`batch = true`): `gh dojo done 12 13 14` after a
  release. An issue a pull request already closed stays closed and still
  moves to Done. To keep issues open in Done, set `close = false`.

Its source is [`internal/config/presets/kanban.toml`](internal/config/presets/kanban.toml).
The preset has no sprints: use the [sprints preset](#sprints-preset), or add
the iteration field and an `iteration` effect to the verbs you want:

```toml
preset = "kanban"

[board]
owner = "acme"
project = 3

[fields]
sprint = "Sprint"

[verbs.done]
iteration = "by_status_date:Ready"   # the sprint in which the work was finished
```

### Sprints preset

The kanban flow with work counted in sprints. The board needs the
[kanban statuses](#kanban-preset) in its built-in `Status` field, and an
iteration field named `Sprint` with an iteration for today (GitHub: add a
field of type Iteration; [`gh dojo doctor`](#doctor) checks today's). The
preset sets `fields.sprint = "Sprint"`; everything else is kanban's.

| Verb    | Moves to    | From                    | Sprint                         |
|---------|-------------|-------------------------|--------------------------------|
| `start` | In progress | Backlog, Waiting, Ready | today's                        |
| `wait`  | Waiting     | In progress             | kept                           |
| `ready` | Ready       | In progress, Waiting    | of the day it entered Ready    |
| `done`  | Done        | Ready, In progress      | of the day it entered Ready    |

An issue's sprint is the one it is worked in, and in the end the one in
which it became Ready: `start` takes it into today's sprint (an issue
carried over from the last sprint moves to the current one), `wait` leaves
the sprint alone, and `ready` sets the sprint of the day it entered Ready,
which is today when `ready` moves it there; a repeat of `ready` keeps that
sprint. `done` sets it again from the status history
(`iteration = "by_status_date:Ready"`), so an issue released in a later
sprint, or moved to Ready on the board by hand, still counts in the sprint
it was finished in. An issue that goes from In progress straight to Done
never entered Ready: `done` puts it in today's sprint and warns. "Today" is
a date in [`board.timezone`](#board), UTC by default.

Its source is [`internal/config/presets/sprints.toml`](internal/config/presets/sprints.toml).
Override on top of it like any preset; [`examples/sprints.toml`](examples/sprints.toml)
has more:

```toml
preset = "sprints"

[board]
owner = "acme"
project = 3
timezone = "Europe/Moscow"   # the team's "today" for sprints

[fields]
sprint = "Iteration"         # the board's iteration field has another name

[verbs.start]
blockers = "deny"            # refuse to start a blocked issue

[verbs.done]
from = ["Ready"]             # released only after ready
```

`ready` and `done` read the status history, which GitHub keeps only for a
board's built-in `Status` field: with another `fields.status`, the config
is refused until they set `iteration = "current"`.

### Your own process

The presets are a starting point, not a requirement: a process that is
neither kanban nor sprints is configured in `.github/dojo.toml` alone,
without changing gh-dojo. [`examples/review.toml`](examples/review.toml) is
a code review flow with no preset: an agent writes the change and opens a
pull request, a person reviews it, and approving or rejecting it is theirs
to decide.

| Verb              | Moves to          | From                                | Also |
|-------------------|-------------------|-------------------------------------|------|
| `start`           | In progress       | Backlog                             | refused while a blocker is unresolved; assigns the author |
| `hold`            | status unchanged  | In progress, Changes requested      | label `+blocked`; `--by 17` marks it blocked by #17 |
| `resume`          | status unchanged  | In progress, Changes requested      | label `-blocked`; refused while a blocker is unresolved |
| `review`          | In review         | In progress, Changes requested      | needs `--comment` (the pull request) and `--assignee` (the reviewer); labels; assigns the reviewer |
| `request-changes` | Changes requested | In review                           | needs `--comment`; labels; back to the author alone: the reviewer is unassigned |
| `approve`         | Approved          | In review                           | the user's decision (`--yes`); back to the author alone |
| `reject`          | status unchanged  | In progress, In review, Changes requested, Approved | the user's decision; closes as not planned |
| `merged`          | Merged            | Approved                            | closes as completed; several issues at once |

`reject` closes and has no `to`, so on an issue closed as not planned its
`from` is not checked: it runs as a repeat and posts its comment only if it
is missing. A merged issue, closed as completed, is refused whatever its
status, and gets no comment.
`merged` records a merge; it does not merge the pull request.

To write your own:

1. Put the statuses on the board's status field, and the labels the verbs
   add and remove in the repository (`gh label create`).
2. Without `preset`, only [`fields.status`](#fields) and the verbs are
   required. [`[statuses]`](#statuses) is optional, but without `initial`
   `new` sets no status and `next` refuses to run, and without `resolved`
   only a closed blocker is resolved. On top of a preset, set only what
   differs, add verbs, and drop the preset's with `disabled = true`.
3. A verb is a `when` line, [checks](#checks) (`from`, `require`,
   `require_one_of`, `blockers`, `confirm`) and [effects](#effects) (`to`,
   `labels`, `assign`, `unassign`, `comment`, `close`, `reopen`,
   `iteration`, `field`, the `blockers` link). A verb without `to` leaves
   the status alone, like `hold` above; one with `confirm = "user"` waits
   for a person.
4. Check it: [`gh dojo doctor`](#doctor) against the board and the
   repositories (statuses, the labels verbs add and remove, the users they
   assign and unassign by login, the type and area labels), `gh dojo
   describe` for the process
   as an agent reads it, `gh dojo <verb> N --dry-run` for a verb's plan on a
   real issue. Then [`gh dojo agent install`](#agent-install) writes the
   agent instructions from the same file.

What the config cannot say yet, and the example works around:

- On hold is the `blocked` label, not a state: no check reads labels, so
  `review`, `approve` and `merged` still run on an issue on hold. `resume`,
  `reject` and `merged` remove the label.
- `confirm = "user"` is the person the agent works for saying yes, not the
  reviewer: gh-dojo does not check that the pull request was approved.
- `request-changes` and `approve` hand the issue back by removing everyone
  but the author (`unassign = "all"`), not the reviewer by name: gh-dojo
  does not remember who `review` assigned, so someone else assigned by hand
  is removed too. The author is `@me`, whoever runs the verb: the author's
  agent runs them; a reviewer running them would remove the author.

## Output and exit codes

A successful write prints one line per issue: what was, what is now and what
was done along the way (`#42 Backlog → In progress, comment left`), so there
is no need to re-read the issue. Warnings go to stderr. An error is a few
lines on stderr: what is wrong, the allowed values and a command to fix it.

```console
$ gh dojo ready 42
error: ready cannot move #42 from Backlog; it starts only from In progress or Waiting
this is a process rule: ask the user how to proceed
```

### Exit codes

| Code | Meaning                     | What to do                                                   |
|------|-----------------------------|--------------------------------------------------------------|
| 0    | Everything applied          | Go on                                                        |
| 1    | Bad arguments or config     | Fix them as the error says                                   |
| 2    | A process check refused     | Don't work around it; ask a human                            |
| 3    | Issue or board not found    | Check the number (`gh dojo list`) or `board.owner`/`project` |
| 4    | Network, rate limit or auth | Retry later, or `gh auth refresh -s project`                 |
| 5    | Partially applied           | Re-run the same command; it finishes the rest                |

`gh dojo --help` and `gh dojo describe` print this table too. Retries are
safe for every command: verbs skip every effect already in place, `comment`
skips its own repeated text, `block` and `unblock` skip links already as
asked, and `new` resumes the issue it created. A partial write (code 5) names
what was done and what was not:

```console
$ gh dojo review 42
error: #42 labels +in-review, assigned octocat, comment left; status not changed: GitHub token is missing a required scope (project); gh auth refresh -s project, then re-run the same command
fix: gh auth refresh -s project
```

### Several issues at once

A verb with `batch = true` takes several issue numbers; other verbs take
exactly one and say which verbs take several. An issue given twice counts
once (`12` and `acme/shop#12` too, in the default repository acme/shop).
The cards are read in one request per 20 issues, from any repositories
(`gh dojo done 12 acme/api#3`), with the label, user and `--by` lookups once
in each (labels and users once per repository); then each issue is checked, planned and
written on its own, with its own mutation, so a lost answer or a refusal
touches that issue alone. An issue that fails is skipped and the others go
on. An error that would fail every write alike (the token rejected or missing
a scope, a rate limit, the run interrupted) stops the writes: the issues
left are reported as not attempted. `--yes` counts for every issue;
`--dry-run` prints the plan of each.

Each issue done prints its confirmation line; then a summary. When every
issue is done it is `3 of 3 done` on stdout, or `3 of 3 done (2 written)`
when some were already in place; otherwise an error names each issue not
done with its reason (issues failing alike share one entry), then what to do
next:

```console
$ gh dojo done 12 13 14 17 21
#12 Ready → Done, closed as completed
#13 In progress → Done, closed as completed
#14 Done (unchanged), already closed as completed
error: 3 of 5 done (2 written); #21: GitHub write failed (HTTP 502); #17: done cannot move #17 from Backlog, it starts only from Ready or In progress
re-run the same command to retry #21: it skips the issues done
this is a process rule: ask the user how to proceed
```

The run's exit code is the first that applies:

| Outcome                                                                                    | Code |
|--------------------------------------------------------------------------------------------|------|
| Every issue applied or already in place                                                    | 0    |
| Something was written, and an issue failed on the network or partway, or was not attempted | 5    |
| Otherwise, an issue was refused by a process check                                         | 2    |
| Otherwise, bad arguments for an issue (`--by` naming the issue itself)                     | 1    |
| Otherwise, network failures with nothing written                                           | 4    |
| Otherwise, an issue was not found                                                          | 3    |

Here it is 5: the re-run finishes #21 and then ends with the refusal of #17
(code 2). Whatever the code, the error names the issues a re-run may finish.
Bad arguments or flags, a config error and a label to add that the default
repository does not have fail the whole command, with nothing written (code
1); so does an issue `--by` names that does not exist (code 3). A label
missing in another repository fails only that repository's issues.

### JSON output

Every command takes `--json`, `--jq <expression>` (`-q`) and
`--template <template>` (`-t`), as gh does: `--jq` filters the JSON with jq
syntax, printing strings and numbers raw, and `--template` formats it with a
Go template (gh's functions included). Both imply `--json`, cannot be
combined, and are checked before the command runs, so a typo never follows a
write. `--json` takes no field list, unlike gh's: it prints every field, and
`--jq '{number, title}'` picks some. JSON is one compact line; indented and
colored when stdout is a terminal. Objects that `--jq` prints have their keys
sorted (as gojq prints them).

```console
$ gh dojo list --jq '.issues[].number'
21
22
$ gh dojo show 42 --jq '.issues[0].board.status'
In progress
$ gh dojo show 42 --jq '.issues[0].blocked_by[] | select(.resolved | not) | .ref'
acme/shop#17
$ gh dojo start 42 --dry-run --jq '.issues[0].planned[].op'
set_status
add_comment
$ gh dojo describe --jq '.verbs[].name'
start
wait
ready
done
$ gh dojo ready 42 --jq '.error.exit_code // 0'
2
```

An expression that fails only on the data (iterating `null`, say) prints
nothing: the output is buffered, so stdout never gets half of it. After a
command that only read, that is an error (exit code 1), as in gh. After a
write it is a warning that names what was applied (`warning: start applied;
--jq failed on its result: ...`) and the exit code stays the command's.

**Errors are JSON too.** With `--json`, `--jq` or `--template`, every output
has an `error`: `null` when the command succeeded, else the error that ended
it, with the same exit code:

```console
$ gh dojo list --limit 0 --json
{"schema_version":1,"error":{"exit_code":1,"message":"invalid --limit 0: want 1 or greater","allowed":[],"fix":"gh dojo list --help"}}
```

A command that failed with nothing to show (bad arguments or flags, a config
error, the network before anything was read) prints only `schema_version`
and `error`. One that read or did something prints its result with the
`error` next to it: a verb refused or failing once the issue was read, a
partial write (the result says what was applied, the error what was not),
`show` with a missing number, `doctor` with a problem, `init` with a
mismatch or without `--project`. `--jq` and `--template` apply to the error
object as well, so `.error.exit_code` works on every output. The error is
also printed as the usual short block on stderr, and warnings stay on stderr
(`list`, `show` and a verb's issues also carry them in `warnings`).

**Stability.** Every object has `"schema_version": 1` first and `"error"`
second. Field names are snake_case. Every key is always there: lists are
`[]` when empty, never `null`; an absent value (no status, no milestone, no
due date, no fix) is `null`, never `""`. Times are RFC 3339 in UTC, dates
`YYYY-MM-DD`. GitHub node IDs never appear. A reference to an issue is
always in full, `owner/name#12` (`ref`, `changed`, `unchanged`, `missed`,
`blocked_by`, `already_blocked_by`, the `issue` of `add_blocked_by`), and
every issue has its `repo`: the default repository as the config spells it,
others as GitHub does (also in the text). The short `#12` of the text is
only for reading. New fields may be added;
renaming or removing one, or changing its meaning, is a breaking change and
comes only with a new `schema_version` in a major release.

An error, top-level or inside a result, is `{exit_code, message, allowed,
fix}`: the fields of the error block, `fix` `null` when there is none.
`message` is whole; the block on stderr may cut it.

The schemas, by command, after `schema_version` and `error`:

| Command | Top-level fields |
|---------|------------------|
| `show` | `brief` (`--brief`: every card's `history` and `comments` are `[]`, `body` `null`), `issues`: cards; `failed`: `[{number, repo, error}]` for issues not shown; `warnings` |
| card | `number`, `repo`, `url`, `title`, `state` (`open`/`closed`), `state_reason` (`completed`, `not_planned`, `duplicate`, `reopened` or `null`), `author`, `created_at`, `closed_at`, `incomplete` (GitHub withheld part of it); `board`: `{on_board, unknown_reason, status, status_unknown, archived, fields: [{name, value}], fields_truncated}`; `sprint`: `null` without `fields.sprint`, else `{field, iteration: {title, start_date, end_date, duration_days} or null, unknown_reason}`; `milestone`: `{title, due_on}` or `null`; `labels`, `assignees` with `labels_total`, `assignees_total`; `blocked_by`, `blocking`: `[{ref, repo, number, title, state, on_board, status, archived, status_unknown_reason, resolved}]` with `blocked_by_total`, `blocking_total`, `unresolved_blockers`; `history`: `[{at, from, to, actor, automated}]` with `history_truncated`, `history_untracked`; `body`: `{text, cut, length}`; `comments`: `[{author, created_at, text, cut, length}]` with `comments_total` |
| `list` | `sprint_filter` (`null` without `--sprint`, `"none"` for issues without a sprint, `"iteration"` for those in `sprint`), `sprint` (the `--sprint` iteration or `null`), `issues`: `[{repo, number, title, state, status, labels, labels_total, assignees, assignees_total}]` in the printed order, cut to `--limit`; `matched` (before `--limit`), `unchecked`, `hidden`, `filtered`, `skipped`: `{draft_issues, pull_requests, unseen}`, `incomplete`, `warnings` |
| `next` | as `list`, each issue with `unresolved_blockers`: `[{ref, repo, number, on_board, status, archived, status_unknown_reason}]` (empty unless `--all`) and `unread_blockers` (blockers the token cannot read, unresolved); `matched`: issues to show before `--limit` (with `--all`, the blocked ones too); `hidden`: blocked issues left out (0 with `--all`); `incomplete`: GitHub withheld part of an issue to show or hide, or of its blockers |
| verbs | `verb`, `default_repo` (where a bare number is), `dry_run`, `issues`: `[{number, repo, title, outcome, planned, applied, in_place, needs_yes, warnings, error}]` in argument order (`title` `null` when the issue was not read), `summary`: `{total, done, written, failed, not_attempted}` |
| `release` | as verbs, with `release`: `{ref, commit, since, since_picked, since_commit, compared, commits}` (`since_picked`: the previous version tag, not `--since`; `compared`: GitHub compared the two, else the history was read down to a `--since` commit; commits are full SHAs), each issue with `found_by`: `[{pull_request, commit}]` (`pull_request` `owner/name#12` that closes it, with its first commit in the release, or `null`: the commit's message names it), `skipped`: `[{number, repo, ref, reason, status, found_by}]` (`reason` `pull_request`, or `not_ready` with the `status` the verb's `from` does not allow), and `warnings`; `issues` and `summary` count only the issues the verb ran on, the default repository's first |
| `new` | `number`, `repo`, `title`, `created` (`false`: resumed), `labels`, `status`, `status_set`, `unarchived`, `milestone` (`{title, due_on}` or `null`), `milestone_set`, `blocked_by`, `already_blocked_by` |
| `comment` | `number`, `repo`, `posted` (`false`: already the last comment) |
| `block`, `unblock` | `number`, `repo`, `action`, `changed`, `unchanged`, `missed` |
| `describe` | `config`, `preset`, `board`, `repos`, `verbs`: `[{name, when, from, to, requires, requires_one_of, optional, batch, checks, effects}]`, `statuses`: `{initial, final, resolved}`, `exit_codes`: `[{code, meaning, next}]` |
| `doctor` | `ok` (no check is a problem), `checks`: `[{name, result, detail, allowed, fix}]`, `result` one of `ok`, `problem`, `warn`, `skip` |
| `agent install` | `check`, `files`: `[{agent, path, state, diff, fix}]`; `state` is `created`, `updated` or `up_to_date`, with `--check` `up_to_date`, `missing`, `out_of_date` or `hand_written`; `diff` (`--check`, out of date) the changed lines as printed, else `[]`; `fix` (`--check`, not up to date) the command or step that brings it up to date, else `null` |
| `init` | without `--project` (exit code 1, the choice is yours): `owner`, `owner_type`, `projects`: `[{number, title, items, closed}]`, `suggested_project`, `command`; with it: `path`, `written`, `board`, `board_title`, `preset`, `repos`, `matches_board`, `problems`: `[{problem, fix}]`, `content` |
| `--version`, or `gh dojo --json` | `version` |

`show` cuts descriptions and comments as its text does (`--full`,
`--comments`). A verb's `outcome` is `planned` (dry run: something to
apply), `applied`, `unchanged` (every effect in place, also in a dry run),
`partial`, `failed` (nothing applied: refused, not found, needs `--yes`, or
the write failed; see its `error`), or `not_attempted` (an error that stops
every write came first; a re-run tries it). In `summary`, `done` counts
`applied` and `unchanged` issues (and `planned` ones in a dry run), `written`
the `applied` ones. A verb prints its result once the issue was read, or
found missing; an error before that (bad arguments, the network) is the
error object alone. `planned`, `applied` and `in_place` are lists of ops:
`{"op": ...}` with the fields of its kind, each always there:

| `op` | Fields |
|------|--------|
| `add_to_board`, `unarchive`, `reopen` | none |
| `set_status` | `from`, `to` (`null`: no status, or not on the board) |
| `set_iteration` | `from`, `to` (iteration titles, `null` for none), `start_date` (of `to`) |
| `set_fields` | `fields`: `[{field, from, to}]` |
| `add_labels`, `remove_labels` | `labels` |
| `assign` | `login` |
| `unassign` | `logins` (in `in_place`: the users not assigned; empty for `unassign = "all"` with nobody to remove), `kept` (`true` in `in_place` for a user it names and also assigns, who stays) |
| `add_blocked_by` | `issue` (`owner/name#12`) |
| `add_comment` | `body` (`null` in `in_place`: the comment was posted before) |
| `close` | `reason` (`completed`, `not_planned`, `duplicate`) |

## Agents

An agent learns the process from the project itself, so the rules live in
one place, the config:

- **`gh dojo describe`** prints the verbs, when to use each, what they need
  and do, the lifecycle and the exit codes in a few lines: the one command to
  read at the start of a session. `gh dojo <verb> --help` has the details of
  one verb, and the first line of each is its `when`.
- **`gh dojo next`** answers "what do I take now" in a few lines: open
  issues in the initial status, not blocked, in board order.
- **Errors say what to do next**, and the [exit codes](#exit-codes) map to
  one action each: fix the arguments (1), ask a human (2), check the number
  (3), retry later (4), re-run the same command (5).
- **`confirm = "user"`** marks a verb as the user's decision. The exit code 2
  is a signal, not a lock, so agents are told in their instructions
  ([`agent install`](#agent-install) writes it) to ask the user before
  adding `--yes`.
- **`[session]`** puts a link to the agent session into comments, so a human
  can trace who moved an issue and why.

Point your agent at gh-dojo with [`gh dojo agent install`](#agent-install):
it writes a Claude Code skill, an `AGENTS.md` section or Copilot
instructions from the config, which tell the agent to use `gh dojo` for
every issue and board change, read `gh dojo describe` first and
`gh dojo show N` before working on an issue, never work around exit code 2,
ask the user before `--yes`, and when and how to use each verb. Project
rules the config cannot express go into [`[agent] notes`](#agent), and
`agent install --check` in CI catches instructions that fell behind the
config. This repository's own skill,
[`.claude/skills/gh-dojo/SKILL.md`](.claude/skills/gh-dojo/SKILL.md), is
generated from [`.github/dojo.toml`](.github/dojo.toml) and the Russian
notes in [`.github/dojo-notes.md`](.github/dojo-notes.md).

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for building, tests, golden files,
GraphQL fixtures and releases. `go test ./...` loads every `toml` block in
this README and every file in [`examples/`](examples), so they stay valid
configs.

## Roadmap

- **v0.0 — self-hosting.** Config, the kanban preset, `show`, `list`, `new`,
  `comment` and verbs that set the status and leave a comment. gh-dojo's own
  issues live on its board and are handled only through `gh dojo`.
- **v0.1 — first public release (this one).** `describe`, `init`, `doctor`,
  `block` and `unblock`, `new --milestone` and `--blocked-by`; verb effects
  for labels, assign, close and reopen, iteration and fields, blocked-by
  links, the `blockers` and `confirm` checks; several issues per verb; the
  `sprints` preset; boards of users and organizations; `--json`, `--jq` and
  `--template`; `gh extension install` with prebuilt binaries for macOS,
  Linux and Windows.
- **v0.2 — agents and releases.** `agent install` (a skill and an
  `AGENTS.md` section generated from the config), `next` (issues ready to
  take), `release` (apply a verb to the issues a release includes), several
  repositories.
- **v0.3 — extensibility.** Hooks (`pre` and `post` for a verb) and
  sub-issues.

## License

[MIT](LICENSE)
