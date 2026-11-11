# gh-dojo

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
  statuses, the required arguments and the comment to leave.
- **One-line confirmations** and **short errors** that name the allowed values
  and the command to run next, with [exit codes](#exit-codes) an agent can act on.
- **Safe retries.** Repeating a command never posts a second comment or
  creates a second issue.
- **Self-describing.** `gh dojo --help` lists this project's verbs and when to
  use them.

**Status:** v0.0, self-hosting: gh-dojo tracks its own development with
gh-dojo. The config format and output may still change before v0.1. See the
[roadmap](#roadmap).

## Install

There are no release builds yet (`gh extension install Trum-ok/gh-dojo`
comes with v0.1). Install from source; this needs Go (see `go.mod`) and `gh`:

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
`gh dojo --version` shows the `git describe` of the installed build.

gh-dojo uses gh's authentication (or `GH_TOKEN`). Projects need the `project`
scope: `gh auth refresh -s project`.

## Quick start

1. Make sure the board's status field has the [kanban preset](#kanban-preset)
   statuses: Backlog, In progress, Waiting, Ready, Done.
2. Create `.github/dojo.toml` in the repository root:

   ```toml
   preset = "kanban"

   [board]
   owner = "acme"          # user or org login that owns the project
   project = 3             # project number from its URL
   ```

3. Use it:

   ```sh
   gh dojo --help          # this project's verbs and when to use them
   gh dojo list            # open issues on the board, grouped by status
   gh dojo show 42         # read an issue before working on it
   gh dojo start 42        # Backlog → In progress, with a comment
   gh dojo wait 42 --by 17 # In progress → Waiting, "Waiting for #17."
   gh dojo ready 42        # → Ready: done and committed, not released
   gh dojo done 42         # → Done: released
   ```

## Commands

Issue numbers are written `42` or `#42` and refer to the default repository
(see [`board.repos`](#board)). Every command prints its usage with `--help`.

### show

```sh
gh dojo show <N>... [--brief] [--comments N|all] [--full]
```

Reads issues before working on them: state, board status and fields, labels,
assignees, blockers both ways (open first), status history on the board, the
description and the latest comments. Times are UTC.

Long content is folded, always with a note saying what is hidden: the last 5
comments are printed (`--comments N` for the last N, `--comments all` for up
to 100); the description is cut at 2000 characters and the comments shown
share 1500. `--full` prints them whole. `--brief` prints three lines per
issue: title; state, status, labels and assignees; blockers and counts.

Several numbers are read in one request (up to 20 issues per request). A
missing issue or a pull request number is reported after the others are
printed, with exit code 3.

### list

```sh
gh dojo list [--status S]... [--label L]... [--mine] [--all] [--limit N]
```

One line per open issue, grouped by status in board order, issues without a
status first. Closed issues and issues in a final status
([`statuses.final`](#statuses)) are hidden and counted; `--all` shows them,
and so does `--status` naming a final status. `--status none` selects issues
without a status. Filters combine with AND, and the issues they leave out are
counted. Only issues are listed: draft issues and pull requests on the board
are counted. At most `--limit` issues are printed (default 50), with the
total.

### new

```sh
gh dojo new <title> [--type T] [--area A]... [--label L]... [--body TEXT | --body-file FILE]
```

Creates an issue, labels it, adds it to the board and sets the initial status
([`statuses.initial`](#statuses)) in one command. `--type` takes one of
[`labels.types`](#labels) (or any repo label outside the areas when that list
is empty), `--area cli` adds the label `<labels.area_prefix>cli`, `--label`
adds any existing label. An unknown label fails before anything is created,
listing the allowed values. `--body-file -` reads the body from stdin. A
title starting with `-` goes after `--`: `gh dojo new -- "-v is ignored"`.

```console
$ gh dojo new "Fix login redirect" --type bug --area cli
#15 "Fix login redirect" created, labels bug, area:cli, no status → Backlog
```

A repeat is safe: the body ends with a hidden marker of the title, and a run
that finds it on one of the repository's 20 newest open issues written by
you finishes that issue (labels, board, status) instead of creating another.
If a run fails after creating the issue, it exits with code 5: run the same
command again.

### comment

```sh
gh dojo comment <N> [text] [--body-file FILE]
```

Comments on an issue without changing its status. The text comes from the
second argument or from `--body-file` (`-` reads stdin), exactly one of them.
Text starting with `-` goes after `--`: `gh dojo comment 42 -- "-1, still broken"`.
When the issue's last comment is yours with the same text, it is not posted
again.

### Process verbs

```sh
gh dojo <verb> <N> [--comment TEXT] [--by N|TEXT] [--dry-run]
```

Verbs come from [`[verbs]`](#verbs) in the config, so each project has its
own; the [kanban preset](#kanban-preset) defines `start`, `wait`, `ready` and
`done`. `gh dojo --help` lists them with their `when` line, and
`gh dojo <verb> --help` shows the allowed starting statuses, the target
status, the required arguments and the comment template.

A verb takes one issue number and, in one command:

1. checks that the issue is open, that its status is one of the verb's `from`
   statuses (no status, or not on the board, counts as
   [`statuses.initial`](#statuses)), and that the required arguments are
   given;
2. adds the issue to the board if it is not there;
3. sets the status to the verb's `to`; an issue already there keeps it;
4. posts the rendered [comment template](#comment-templates), unless it renders
   empty or is already the latest gh-dojo comment on the issue.

`--comment` and `--by` fill `{comment}` and `{by}` in the template; a verb
whose template does not use one of them rejects that flag. `--by` takes an issue number (`12` or
`#12`, rendered `#12`) or free text. `--dry-run` prints the plan without
changing anything:

```console
$ gh dojo wait 42 --by 17 --dry-run
dry run: wait #42 "Fix login redirect"
  status: In progress → Waiting
  comment: "Waiting for #17."
```

### Output and exit codes

A successful write prints one line per issue: what was, what is now and what
was done along the way (`#42 Backlog → In progress, comment left`), so there
is no need to re-read the issue. Warnings go to stderr. An error is a few
lines on stderr: what is wrong, the allowed values and a command to fix it.

```console
$ gh dojo ready 42
error: ready cannot move #42 from Backlog; it starts only from In progress or Waiting
this is a process rule: ask the user how to proceed
```

#### Exit codes

| Code | Meaning                     | What to do                                         |
|------|-----------------------------|----------------------------------------------------|
| 0    | Everything applied          | Go on                                              |
| 1    | Bad arguments or config     | Fix them as the error says                         |
| 2    | A process check refused     | Don't work around it; ask a human                  |
| 3    | Issue or board not found    | Check the number                                   |
| 4    | Network, rate limit or auth | Retry later, or `gh auth refresh -s project`       |
| 5    | Partially applied           | Re-run the same command; it finishes the rest      |

Retries are safe for every command: verbs keep a status that is already set
and skip a comment that is already the latest gh-dojo comment, `comment`
skips its own repeated text, and `new` resumes the issue it created.

## Configuration

gh-dojo reads `.github/dojo.toml`, looking in the current directory and then
in each parent up to the repository root (the first directory with `.git`).
There is no `gh dojo init` yet: create the file by hand. Unknown keys are
errors, and every error names the file and the key.

A full example:

```toml
version = 1                  # optional; 1 is the only version
preset = "kanban"            # optional; verbs and defaults to build on

[board]
owner = "acme"
project = 3
repos = ["acme/shop"]        # the first one is the default repo

[fields]                     # logical name = field name on the board
status = "Status"            # required (the kanban preset sets "Status")

[statuses]
initial = "Backlog"          # set by `new` and assumed for issues not on the board yet
final = ["Done"]             # hidden by `list`, with a counter

[labels]
types = ["bug", "feature"]   # labels `new --type` accepts; empty = any label without area_prefix
area_prefix = "area:"        # `new --area cli` → label "area:cli"

[session]                    # fills {session} in comment templates
env = ["CLAUDE_CODE_HOST_SESSION_ID"]   # the first set variable wins
link = "claude://claude.ai/epitaxy/{id}"

[verbs.start]                # becomes `gh dojo start <N>`
when = "You begin working on the issue"   # one line; first line of --help
to = "In progress"           # target status
from = ["Backlog", "Waiting", "Ready"]    # allowed current statuses; empty = any
comment = "Started. Session: {session}"   # comment template

[verbs.wait]
when = "Work is stuck: waiting for another issue or an outside decision"
to = "Waiting"
require_one_of = ["by", "comment"]        # --by or --comment must be given
comment = """
Waiting for {by}.

{comment}"""

[verbs.done]
disabled = true              # drop a verb that the preset defines
```

### Top level

| Key       | Required | Meaning                                                      |
|-----------|----------|--------------------------------------------------------------|
| `version` | no       | Config version; `1` is the only one                          |
| `preset`  | no       | A built-in preset to build on; `kanban` is the only one      |

### board

| Key       | Required | Meaning                                                                       |
|-----------|----------|-------------------------------------------------------------------------------|
| `owner`   | yes      | User or organization login that owns the project                              |
| `project` | yes      | Project number from its URL, 1 or greater                                     |
| `repos`   | no       | `owner/name` list; the first is the default repo for issue numbers and `new`. Empty: the current repo (`GH_REPO`, else the git remote) |

`assignee` (for the assign effect) comes in v0.1; until then it is rejected
with "not supported in this version".

### fields

Logical name = field name on the board. `status` is the only entry in this
version: it is required and must be a single-select field. Other logical
fields (such as `sprint`, for iteration effects) come in v0.1; until then
they are rejected with "not supported in this version".

### statuses

| Key       | Meaning                                                                                |
|-----------|----------------------------------------------------------------------------------------|
| `initial` | Set by `new`; issues without a status or not on the board count as this one for `from` |
| `final`   | Statuses `list` hides, with a counter                                                  |

### labels

| Key           | Meaning                                                                                          |
|---------------|--------------------------------------------------------------------------------------------------|
| `types`       | Labels `new --type` accepts. Empty: any repo label that does not start with `area_prefix`        |
| `area_prefix` | Prepended by `new --area`: `--area cli` → `area:cli`. Types may not start with it                |

### session

Fills `{session}` in comment templates, so a comment links back to the agent
session that wrote it.

| Key    | Meaning                                                                            |
|--------|------------------------------------------------------------------------------------|
| `env`  | Environment variables holding the session ID; the first one that is set wins       |
| `link` | Link template; must contain `{id}`. Default: `{id}`, the bare ID                    |

When `env` is set but none of its variables is, `{session}` is empty and
gh-dojo warns; the verb still runs.

### verbs

Each `[verbs.<name>]` table becomes `gh dojo <name>`.

| Key              | Meaning                                                                                          |
|------------------|--------------------------------------------------------------------------------------------------|
| `when`           | Required. One line saying when to use the verb; the first line of its `--help`                   |
| `to`             | Target status                                                                                    |
| `from`           | Statuses the verb may start from; empty or missing means any                                     |
| `require`        | Arguments that must all be given: `comment`, `by`                                                |
| `require_one_of` | Arguments of which at least one must be given                                                    |
| `comment`        | [Comment template](#comment-templates)                                                           |
| `disabled`       | `true` removes a verb the preset defines; it stands alone in its table                           |

A verb needs `to` or `comment`. Every argument in `require` and
`require_one_of` must appear in the verb's comment template. Verb names are
lowercase letters, digits and dashes and cannot shadow built-in or planned
commands (`show`, `list`, `new`, `comment`, `describe`, `init`, `doctor`,
`help`, `block`, `unblock`, `agent`, `next`, `release`). Keys from the design
that are not implemented yet (`blockers`, `confirm`, `iteration`, `field`,
`assign`, `labels`, `close`, `reopen`, `batch`, `pre`, `post`) are rejected
with "not supported in this version".

### Comment templates

Templates may use `{session}`, `{comment}` (the `--comment` text) and `{by}`
(the `--by` value). A line whose placeholders all come out empty is dropped,
so `"Waiting for {by}."` disappears when `--by` is not given; runs of blank
template lines collapse, trailing spaces and leading and trailing blank lines
are cut, and indentation stays. A template that renders to nothing posts no
comment. There is no escape for a literal `{comment}`; other braces such as
`{}` are plain text. A verb whose template does not use `{comment}` or `{by}`
rejects `--comment` or `--by`.

Comments go to the issue as written, so write templates and `when` lines in
your project's language; the CLI itself speaks English.

### Checked against the board

Every command that talks to GitHub first checks the config against the
board: `fields.status` must name a single-select board field, and every
status the config names (`statuses.initial`, `statuses.final`, each verb's
`to` and `from`) must be one of its options. A typo is an error with the board's statuses and a suggestion, not a silent
skip. The board schema is cached for an hour in `~/.cache/gh-dojo` (or
`$XDG_CACHE_HOME/gh-dojo`; override with `GH_DOJO_CACHE_DIR`); a mismatch
re-reads the board before it is reported, so a status just added on the
board is picked up.

### Presets

With `preset = "kanban"` the file is applied on top of the preset:
`[fields]`, `[statuses]`, `[labels]` and `[session]` override key by key; a
verb with a preset's name overrides only the keys it sets; a new name adds a
verb; `disabled = true` removes a preset verb. Without `preset` every verb is
defined in the file.

## Kanban preset

The board's status field must have these options (other options are fine):

| Status      | Meaning                                              |
|-------------|------------------------------------------------------|
| Backlog     | Not started (initial status of new issues)           |
| In progress | Being worked on                                      |
| Waiting     | Stuck on another issue or an outside decision        |
| Ready       | Done, verified and committed, but not released yet   |
| Done        | Released (hidden by `list`)                          |

| Verb    | Moves to    | From                     | Needs                 | Comment                          |
|---------|-------------|--------------------------|-----------------------|----------------------------------|
| `start` | In progress | Backlog, Waiting, Ready  |                       | session link, `--comment`        |
| `wait`  | Waiting     | In progress              | `--by` or `--comment` | "Waiting for …", `--comment`     |
| `ready` | Ready       | In progress, Waiting     |                       | `--comment`                      |
| `done`  | Done        | Ready, In progress       |                       | `--comment`                      |

The preset also sets `fields.status = "Status"`, `statuses.initial =
"Backlog"`, `statuses.final = ["Done"]`, `labels.types = []` (any label
without the area prefix is a type) and `labels.area_prefix = "area:"`. Its
source is [`internal/config/presets/kanban.toml`](internal/config/presets/kanban.toml).
Override `when` and `comment` to write them in your project's language; this
repository's own [`.github/dojo.toml`](.github/dojo.toml) does that.

## Roadmap

- **v0.0 — self-hosting (now).** Config, the kanban preset, `show`, `list`,
  `new`, `comment` and verbs that set the status and leave a comment.
  gh-dojo's own issues live on its board and are handled only through
  `gh dojo`.
- **v0.1 — first public release.** `describe`, `init`, `doctor`, `block` and
  `unblock`, more verb effects (blockers, iteration with sprint fields,
  assign, close, `confirm`), several issues per verb, a `sprints` preset,
  `--json`, and
  `gh extension install` with prebuilt binaries for macOS, Linux and Windows.
- **v0.2 — agents and releases.** `agent install` (an agent skill generated
  from the config), `next`, `release`, several repositories.
- **v0.3 — extensibility.** Hooks and sub-issues.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for building, tests, golden files and
GraphQL fixtures.
