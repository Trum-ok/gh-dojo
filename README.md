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
  use them, the lifecycle (the statuses each verb moves an issue from and to;
  the initial, final and resolved statuses) and the exit codes with what to do
  next.

**Status:** v0.0, self-hosting: gh-dojo tracks its own development with
gh-dojo. The config format and output may still change before v0.1. See the
[roadmap](#roadmap).

## Install

After the first release, install the prebuilt binary (macOS, Linux and
Windows; amd64 and arm64) and update it with gh:

```sh
gh extension install Trum-ok/gh-dojo
gh extension upgrade dojo
```

Until then, or to run the latest `main`, install from source; this needs Go
(see `go.mod`) and `gh`:

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

1. Make sure the board's status field has the [kanban preset](#kanban-preset)
   statuses: Backlog, In progress, Waiting, Ready, Done.
2. Create `.github/dojo.toml` in the repository root with [`init`](#init):

   ```sh
   gh dojo init                          # lists the owner's projects
   gh dojo init --owner acme --project 3 # writes the config, checks the board
   ```

   or by hand:

   ```toml
   preset = "kanban"

   [board]
   owner = "acme"          # user or org login that owns the project
   project = 3             # project number from its URL
   ```

3. Use it:

   ```sh
   gh dojo --help          # verbs, lifecycle and exit codes of this project
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

Reads issues before working on them: state, board status and fields,
milestone, labels, assignees, blockers both ways with their board status,
status history on the board, the description and the latest comments. Times
are UTC. A blocker is resolved, no longer blocking, when it is closed or its
board status is in [`statuses.resolved`](#statuses) (kanban: Ready, Done);
unresolved blockers are listed first, and `--brief` counts only them as
blocking. A linked issue is shown with its status, "on the board, no
status", "not on the board", or "status not read" and why (it is on more
projects than are read, or GitHub withheld them); a blocker whose status was
not read counts as unresolved.

Long content is folded, always with a note saying what is hidden: the last 5
comments are printed (`--comments N` for the last N, `--comments all` for up
to 100); the description is cut at 2000 characters and the comments shown
share 1500. `--full` prints them whole. `--brief` prints three lines per
issue: title; state, status, milestone, labels and assignees; blockers and
counts.

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

### init

```sh
gh dojo init [--owner LOGIN] [--project N] [--preset kanban] [--repo OWNER/NAME] [--force] [--dry-run]
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

With `--project` it writes the board (`owner`, `project`, and `repos` set
to `--repo` or the current repository) and the preset, nothing the preset
already provides, then checks the file against the board. Each status the
board lacks is listed with its fix: add the status on the board, or override
the keys that name it, e.g. `[verbs.wait] to = "…"`. The file is kept, and
the exit code is 1 until the board and the file match. An existing config is
not overwritten without `--force`; `--dry-run` prints the file instead.

### describe

```sh
gh dojo describe
```

Prints the project's process in a few lines per verb, short enough for an
agent's context: when to use each verb, the arguments it needs or takes, what
it checks and does along the way (the comment it posts, labels, assignee,
closing or reopening, whether it runs on closed issues or needs the user's
go-ahead), then the lifecycle (from → to), the initial, final and
resolved statuses, and the exit codes. It reads only the config, never the
network; a missing or broken config is printed as an error (exit code 1).

### doctor

```sh
gh dojo doctor
```

Checks the setup and prints one line per check, `ok`, `problem`, or `skip`
when an earlier problem keeps a check from running; each problem is
followed by the command that fixes it:

```console
$ gh dojo doctor
ok       version  gh dojo v0.1.0
ok       config   .github/dojo.toml: preset kanban, 4 verbs
problem  token    github.com as octocat: the token lacks the project scope (has: read:org, repo)
                  fix: gh auth refresh -s project
ok       board    octocat/3 "Shop": field Status and 5 statuses match
ok       repo     octocat/shop (default): visible, issues enabled
ok       verbs    octocat/shop: 2 labels exist, 1 assignee can be assigned
```

It checks that the config loads, that gh is logged in with a token that has
the `project` scope (a token from `GH_TOKEN` gets "set GH_TOKEN to …" as
the fix), that the board exists, is open and has every field and status the
config names (each mismatch on its own line), and that every repository of
[`board.repos`](#board), or the current one, is visible with issues
enabled, and that the labels verbs add or remove exist in the default
repository spelled exactly as there and the users they assign can be
assigned (a missing label to remove is reported too: it never removes
anything). An organization whose SAML single sign-on or OAuth app
restrictions block the token gets the same fix as any other command. It
sends one request, plus one for the board when its schema is not
cached, plus one for the verbs' labels and assignees when a verb has them. The exit code is 0 when every check is ok, 4 when one failed on auth
or the network, else 1.

### Process verbs

```sh
gh dojo <verb> <N> [--comment TEXT] [--by N|TEXT] [--yes] [--dry-run]
```

Verbs come from [`[verbs]`](#verbs) in the config, so each project has its
own; the [kanban preset](#kanban-preset) defines `start`, `wait`, `ready` and
`done`. `gh dojo --help` lists them with their `when` line and, under
Lifecycle, the statuses each one moves an issue from and to, with the
initial, final and resolved statuses, and which verbs close or reopen the
issue or ask the user first; `gh dojo <verb> --help` shows the allowed
starting statuses, the target status, the required arguments, the other
effects and the comment template; [`gh dojo describe`](#describe) sums up all
of them.

A verb takes one issue number and, in one command:

1. checks that the issue is open (a verb that moves it to a final status,
   closes it or reopens it also runs on a closed one), that its status is one
   of the verb's `from` statuses (no status, or not on the board, counts as
   [`statuses.initial`](#statuses)), and that the required arguments are
   given;
2. adds the issue to the board if it is not there;
3. reopens it (`reopen`), sets the status to the verb's `to`, adds and removes
   `labels`, adds the `assign` user, posts the rendered
   [comment template](#comment-templates) and closes it (`close`). Each
   effect already in place is skipped: a status already set, labels already
   there or gone, a user already assigned, a comment that is already the
   latest gh-dojo comment, an issue already closed (whatever its reason) or
   open.

Everything is written in one GraphQL mutation (two when the issue is not on
the board yet: the status needs the new board item). Label and user IDs are
read with the issue, so a verb on an issue on the board costs one read and
one write.

The board's built-in workflows run after the verb and may change the status
it set: "Item closed" moves an issue a verb closed (to Done by default),
"Item reopened" moves one it reopened. Turn them off in the board's
workflow settings, or point them at the statuses your verbs set. A verb
that closes the issue and has no `to` skips the `from` check on an issue
already closed, so its repeat is not refused because a workflow moved the
issue. A verb that moves an issue closed as not planned or duplicate to a
final status warns on stderr (`closed as not planned; moving to Done
anyway`) and keeps the reason.

A verb with `confirm = "user"` is the user's decision: without `--yes` it
exits with code 2 and asks you to ask the user; re-run it with `--yes` only
once they agree. A repeat with nothing left to do needs no `--yes`. Other
verbs reject `--yes`.

`--comment` and `--by` fill `{comment}` and `{by}` in the template; a verb
whose template does not use one of them rejects that flag. `--by` takes an issue number (`12` or
`#12`, rendered `#12`) or free text. `--dry-run` prints the plan without
changing anything:

```console
$ gh dojo wait 42 --by 17 --dry-run
dry run: wait #42 "Fix login redirect"
  status: In progress → Waiting
  comment: "Waiting for #17."

$ gh dojo drop 42 --comment "Duplicate of #17."
error: drop needs the user's go-ahead for #42 (would be: comment left, closed as not planned)
this is the user's decision: ask the user, and re-run with --yes only if they agree

$ gh dojo drop 42 --comment "Duplicate of #17." --yes
#42 comment left, closed as not planned
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

| Code | Meaning                     | What to do                                                   |
|------|-----------------------------|--------------------------------------------------------------|
| 0    | Everything applied          | Go on                                                        |
| 1    | Bad arguments or config     | Fix them as the error says                                   |
| 2    | A process check refused     | Don't work around it; ask a human                            |
| 3    | Issue or board not found    | Check the number (`gh dojo list`) or `board.owner`/`project` |
| 4    | Network, rate limit or auth | Retry later, or `gh auth refresh -s project`                 |
| 5    | Partially applied           | Re-run the same command; it finishes the rest                |

`gh dojo --help` prints this table too. Retries are safe for every command:
verbs skip every effect already in place (a status already set, a comment
that is already the latest gh-dojo comment, an issue already closed, ...),
`comment` skips its own repeated text, and `new` resumes the issue it
created. A partial write (code 5) names what was done and what was not:

```console
$ gh dojo review 42
error: #42 labels +in-review, assigned octocat, comment left; status not changed: GitHub token is missing a required scope (project); gh auth refresh -s project, then re-run the same command
fix: gh auth refresh -s project
```

## Configuration

gh-dojo reads `.github/dojo.toml`, looking in the current directory and then
in each parent up to the repository root (the first directory with `.git`).
[`gh dojo init`](#init) writes one for a board; this section documents
every key. Unknown keys are errors, and every error names the file and the
key.

A full example:

```toml
version = 1                  # optional; 1 is the only version
preset = "kanban"            # optional; verbs and defaults to build on

[board]
owner = "acme"
project = 3
repos = ["acme/shop"]        # the first one is the default repo
assignee = "@me"             # who `assign = true` assigns

[fields]                     # logical name = field name on the board
status = "Status"            # required (the kanban preset sets "Status")

[statuses]
initial = "Backlog"          # set by `new` and assumed for issues not on the board yet
final = ["Done"]             # hidden by `list`, with a counter
resolved = ["Ready", "Done"] # a blocker in these no longer blocks

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

[verbs.review]
when = "A pull request is open and waits for review"
to = "In review"
labels = ["+in-review", "-blocked"]       # + adds a label, - removes it
assign = true                # board.assignee; or "@me", or a login

[verbs.drop]
when = "The issue is not needed or a duplicate"
close = "not_planned"        # completed, not_planned or duplicate
require = ["comment"]        # without a template, --comment is the comment
confirm = "user"             # runs only with --yes, after asking the user

[verbs.revive]
when = "A closed issue is needed after all"
to = "Backlog"
reopen = true

[verbs.done]
close = false                # keep the issue open: false turns off the preset's close

[verbs.ready]
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
| `owner`   | yes      | User or organization login that owns the project; which one is detected       |
| `project` | yes      | Project number from its URL, 1 or greater                                     |
| `repos`   | no       | `owner/name` list; the first is the default repo for issue numbers and `new`. Empty: the current repo (`GH_REPO`, else the git remote) |
| `assignee` | no      | Who a verb with `assign = true` assigns: `"@me"` (the authenticated user) or a login |

### fields

Logical name = field name on the board. `status` is the only entry in this
version: it is required and must be a single-select field. Other logical
fields (such as `sprint`, for iteration effects) come in v0.1; until then
they are rejected with "not supported in this version". The logical name is
lowercase: `Status = "Status"` is an error suggesting `status`.

### statuses

| Key        | Meaning                                                                                                                                               |
|------------|-------------------------------------------------------------------------------------------------------------------------------------------------------|
| `initial`  | Set by `new`; issues without a status or not on the board count as this one for `from`                                                                |
| `final`    | Statuses `list` hides, with a counter                                                                                                                 |
| `resolved` | Statuses in which an open blocker no longer blocks: its work is done though the issue stays open (kanban: Ready). A closed blocker is always resolved |

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
| `labels`         | Labels to add (`"+name"`) and remove (`"-name"`): `["+in-review", "-blocked"]`                   |
| `assign`         | Who to add to the assignees: `true` (`board.assignee`), `"@me"`, a login; `false` turns a preset's off |
| `close`          | Close the issue with this reason: `completed`, `not_planned` or `duplicate`; `false` turns a preset's off |
| `reopen`         | `true` reopens a closed issue                                                                    |
| `confirm`        | `"user"`: the verb is the user's decision and runs only with `--yes`; `false` turns a preset's off |
| `disabled`       | `true` removes a verb the preset defines; it stands alone in its table                           |

A verb needs at least one effect: `to`, `comment`, `labels`, `assign`,
`close` or `reopen`. Every argument in `require` and `require_one_of` must
appear in the verb's comment template; a verb that requires `comment` and
has no template posts the `--comment` text as is. Verb names are
lowercase letters, digits and dashes and cannot shadow built-in or planned
commands (`show`, `list`, `new`, `comment`, `describe`, `init`, `doctor`,
`help`, `block`, `unblock`, `agent`, `next`, `release`). Keys from the design
that are not implemented yet (`blockers`, `iteration`, `field`, `batch`,
`pre`, `post`) are rejected with "not supported in this version".

Effects:

- **labels**: every entry starts with `+` or `-`; a name may not be both
  added and removed. Names must match the repository's labels exactly, case
  included: a label to add that is missing fails before anything is
  written, with the repository's labels as the allowed values (create it
  with `gh label create`; a name that differs only in case gets the
  spelling to use instead). A label to remove that the repository does not
  have is in place, unless the issue has it in another case. Labels the
  issue already has (or lacks) are skipped; a verb that removes labels
  reads up to 100 of the issue's labels, GitHub's limit per issue.
  [`gh dojo doctor`](#doctor) checks every verb's labels.
- **assign**: adds one user and keeps the other assignees. `"@me"` is the
  authenticated user; a login must be able to be assigned issues in the
  repository, or the verb fails before writing, listing who can be.
- **close** / **reopen**: a verb sets at most one of them. An issue already
  closed is not closed again, even with another reason; the line says
  `already closed as completed`. `duplicate` marks the issue a duplicate
  without linking the original: name it in the comment. A verb that closes
  or reopens runs on closed issues. `close = false` turns off a preset's
  close, such as kanban's `done`.
- **confirm**: `confirm = "user"` is a signal, not a lock: an agent could add
  `--yes` itself, so tell agents (in a skill or AGENTS.md) to ask first.
  `confirm = false` turns off a preset's.

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
status the config names (`statuses.initial`, `statuses.final`,
`statuses.resolved`, each verb's `to` and `from`) must be one of its
options. A typo is an error with the board's statuses and a suggestion, not
a silent skip. The board schema is cached for an hour in `~/.cache/gh-dojo` (or
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

`done` also closes the issue as completed: Done means released. An issue a
pull request already closed stays closed; `done` still moves it to Done.
To keep issues open in Done, set `close = false` under `[verbs.done]`.

The preset also sets `fields.status = "Status"`, `statuses.initial =
"Backlog"`, `statuses.final = ["Done"]`, `statuses.resolved = ["Ready",
"Done"]` (a blocker in Ready is done, so it no longer blocks, though its
issue stays open until Done), `labels.types = []` (any label
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
  `unblock`, more verb effects (labels, assign, close and reopen, `confirm`;
  blockers, iteration with sprint fields), several issues per verb, a `sprints` preset,
  `--json`, and
  `gh extension install` with prebuilt binaries for macOS, Linux and Windows.
- **v0.2 — agents and releases.** `agent install` (an agent skill generated
  from the config), `next`, `release`, several repositories.
- **v0.3 — extensibility.** Hooks and sub-issues.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for building, tests, golden files and
GraphQL fixtures.
