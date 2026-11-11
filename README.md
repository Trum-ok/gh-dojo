# gh-dojo

A [GitHub CLI](https://cli.github.com/) extension that gives AI agents process
verbs over GitHub Projects v2. An agent runs `gh dojo start 42`, and gh-dojo
knows the board's fields, statuses, sprint and rules from the repository's
`.github/dojo.toml`. No field or option IDs, one-line confirmations, short
errors with predictable exit codes.

**Status:** pre-v0.0, under active development. Nothing is usable yet.

## Install from source

Requires Go (see `go.mod`) and `gh`.

```sh
git clone https://github.com/Trum-ok/gh-dojo
cd gh-dojo
make install
gh dojo --help
```

`make install` builds into `~/.local/share/gh-dojo/gh-dojo` (override with
`INSTALL_DIR`, whose last element must be `gh-dojo`) and installs the extension
from there, so the installed `gh dojo` does not change when you switch
branches in the working copy. Re-run it to update. A different `gh dojo`
extension already installed is replaced, and put back if the install fails.
`gh dojo --version` shows the `git describe` of the installed build.

## Configuration

gh-dojo reads `.github/dojo.toml`, looking in the current directory and then
in each parent up to the repository root (the first directory with `.git`). There is no `gh dojo init` yet: create the file by hand. The
smallest working config uses the [kanban preset](#kanban-preset):

```toml
preset = "kanban"

[board]
owner = "acme"          # user or org login that owns the project
project = 3             # project number from its URL
```

A full example:

```toml
version = 1                  # optional; 1 is the only version
preset = "kanban"            # optional; verbs and defaults to build on

[board]
owner = "acme"
project = 3
repos = ["acme/shop"]        # the first one is the default repo
assignee = "@me"

[fields]                     # logical name = field name on the board
status = "Status"            # required (the kanban preset sets "Status")
sprint = "Sprint"

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

[verbs.done]
disabled = true              # drop a verb that the preset defines
```

Verb keys: `when` (required), `to`, `from`, `require` and `require_one_of`
(argument names: `comment`, `by`; each must appear in the verb's comment
template), `comment`, `disabled`. A verb needs `to` or `comment`.
`disabled = true` stands alone and only removes a verb the preset defines. Keys from the
design that are not implemented yet (`blockers`, `confirm`, `iteration`,
`field`, `assign`, `labels`, `close`, `reopen`, `batch`, `pre`, `post`) are
rejected with "not supported in this version", and any other unknown key is an
error too. Verb names are lowercase letters, digits and dashes and cannot
shadow built-in commands (`show`, `list`, `new`, `comment`, `describe`,
`init`, `doctor`, `help`, `block`, `unblock`, `agent`, `next`, `release`).

**Comment templates** may use `{session}`, `{comment}` (the `--comment`
text) and `{by}` (the `--by` value). A line whose placeholders all come out
empty is dropped, so `"Waiting for {by}."` disappears when `--by` is not
given; runs of blank template lines collapse, trailing spaces and leading and
trailing blank lines are cut, and indentation stays. A template that renders
to nothing posts no comment. There is no escape for a literal `{comment}`;
other braces such as `{}` are plain text. A verb whose template does not use
`{comment}` or `{by}` rejects `--comment` or `--by`. When `[session].env` is
set but none of its variables is, `{session}` is empty and gh-dojo warns.

**Presets.** With `preset = "kanban"` the file is applied on top of the
preset: `[fields]`, `[statuses]`, `[labels]` and `[session]` override key by
key; a verb with a preset's name overrides only the keys it sets; a new name
adds a verb; `disabled = true` removes a preset verb. Without `preset` every
verb is defined in the file.

### Kanban preset

The board's status field must have these options:

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

The preset also sets `fields.status = "Status"`, `labels.types = []` (any
label without the area prefix is a type) and `labels.area_prefix = "area:"`. Its source is
[`internal/config/presets/kanban.toml`](internal/config/presets/kanban.toml).
Override `when` and `comment` to write them in your project's language.

## Exit codes

| Code | Meaning                         |
|------|---------------------------------|
| 0    | Everything applied              |
| 1    | Bad arguments or config         |
| 2    | A process check refused         |
| 3    | Issue or board not found        |
| 4    | Network, rate limit or auth     |
| 5    | Partially applied; re-run it    |

## Development

```sh
make build test vet
make golden   # rewrite golden files after an intended output change (UPDATE_GOLDEN=1)
```
