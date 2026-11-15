# Configuration

gh-dojo reads `.github/dojo.toml`, looking in the current directory and then
in each parent up to the repository root (the first directory with `.git`).
[`gh dojo init`](commands.md#init) writes one for a board. Unknown keys are errors, and
every error names the file and the key.

Complete, commented configs to start from:
[`examples/kanban.toml`](../examples/kanban.toml),
[`examples/sprints.toml`](../examples/sprints.toml) and, for a process of
your own without a preset, [`examples/review.toml`](../examples/review.toml)
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

[verbs.schedule]
when = "The issue moves to another release"
milestone = "{milestone}"    # an open milestone from --milestone; or a title such as "v1.2"
batch = true

[verbs.done]
close = false                # keep the issue open: false turns off the preset's close

[verbs.ready]
disabled = true              # drop a verb that the preset defines
```

## Top level

| Key       | Required | Meaning                                                      |
|-----------|----------|--------------------------------------------------------------|
| `version` | no       | Config version; `1` is the only one                          |
| `preset`  | no       | A built-in preset to build on: [`kanban`](#kanban-preset) or [`sprints`](#sprints-preset) |

## board

| Key        | Required | Meaning |
|------------|----------|---------|
| `owner`    | yes      | User or organization login that owns the project; which one is detected |
| `project`  | yes      | Project number from its URL, 1 or greater |
| `repos`    | no       | `owner/name` list; the first is the default repo for bare issue numbers and `new`, the others are reached with `owner/repo#N` and `new --repo`. Empty: the current repo (`GH_REPO`, else the git remote) |
| `assignee` | no       | Who a verb with `assign = true` assigns, or `unassign = true` removes: `"@me"` (the authenticated user) or a login |
| `timezone` | no       | An IANA time zone, like `"Europe/Moscow"`; UTC when not set. The [`iteration`](#effects) effect, `list --sprint current`, `next --sprint current` and `doctor` take today, and the day an issue entered a status, as dates in it |

## fields

Logical name = field name on the board.

| Key      | Required | Meaning |
|----------|----------|---------|
| `status` | yes      | The status field: a single-select field |
| `sprint` | no       | The sprint field: an iteration field. The [`iteration`](#effects) effect, `list --sprint`, `next --sprint`, the sprint line of `show` and `doctor` use it |

Other logical names are rejected; a verb sets any other board field by its
own name with the [`field`](#effects) effect. Logical names are lowercase:
`Status = "Status"` is an error suggesting `status`.

## statuses

| Key        | Meaning |
|------------|---------|
| `initial`  | Set by `new`; issues without a status or not on the board count as this one for `from`. `next` shows the issues in it, or without a status |
| `final`    | Statuses `list` hides, with a counter |
| `resolved` | Statuses in which an open blocker no longer blocks (`show`, the `blockers` check, `next`): its work is done though the issue stays open (kanban: Ready, Done). A closed blocker is always resolved. The same rule resolves a sub-issue (`show`, the `sub_issues` check) |

## labels

| Key           | Meaning |
|---------------|---------|
| `types`       | Labels `new --type` accepts. Empty: any repo label that does not start with `area_prefix` |
| `area_prefix` | Prepended by `new --area`: `--area cli` → `area:cli`. Types may not start with it |

## session

Fills `{session}` in comment templates, so a comment links back to the agent
session that wrote it.

| Key    | Meaning |
|--------|---------|
| `env`  | Environment variables holding the session ID; the first one that is set wins |
| `link` | Link template; must contain `{id}`. Default: `{id}`, the bare ID |

When `env` is set but none of its variables is, `{session}` is empty and
gh-dojo warns; the verb still runs.

## agent

What [`agent install`](commands.md#agent-install) writes. A preset cannot set it.

| Key       | Meaning |
|-----------|---------|
| `targets` | The agents to write for: `claude`, `agents-md`, `copilot`. Default: `["claude"]`; `[]` writes nothing without `--agent` |
| `notes`   | A markdown file, relative to the repository root and written with `/`, appended as is after the generated instructions: what the config cannot say, in the project's language. Its headings land as written, so in an `AGENTS.md` section start them one level down. Not under `.git`, not one of the files agent install writes, and not a link out of the repository |
| `description` | One line added to the end of the skill's generated `description`, which tells Claude Code when to load the skill: say when else to load it. The whole description may be up to 1024 characters |

## release keywords

`[release]`: how [`gh dojo release`](commands.md#release) links a commit to issues besides the pull
requests it belongs to. A preset cannot set it.

| Key        | Meaning |
|------------|---------|
| `keywords` | Words that, followed by issues on the same line of a commit message (`Fixes #12`, `refs: #12, acme/api#3`), put those issues in the release; whole words, in any case, letters with single spaces or dashes (`"part of"`). Default: GitHub's closing keywords, `close`, `closes`, `closed`, `fix`, `fixes`, `fixed`, `resolve`, `resolves`, `resolved`; a list replaces them. `[]`: only pull requests count. With `refs`, keep the verb's `from` to Ready (the presets' `done` also starts from In progress): a `Refs` commit may be partial work, which is then [not ready](commands.md#release) rather than released |

## verbs

Each `[verbs.<name>]` table becomes `gh dojo <name>`. Verb names are
lowercase letters, digits and dashes and cannot shadow built-in or planned
commands (`show`, `list`, `new`, `comment`, `describe`, `init`, `doctor`,
`help`, `block`, `unblock`, `agent`, `next`, `release`).

| Key              | Kind   | Meaning |
|------------------|--------|---------|
| `when`           | —      | Required. One line saying when to use the verb; the first line of its `--help` |
| `from`           | check  | Statuses the verb may start from; empty or missing means any |
| `from_labels`    | check  | Labels the issue must all have: `["blocked"]` |
| `refuse_labels`  | check  | Labels of which the issue may have none: `["blocked"]` |
| `require`        | check  | Arguments that must all be given: `comment`, `by`, `assignee`, `milestone` |
| `require_one_of` | check  | Arguments of which at least one must be given |
| `blockers`       | check or effect | `"warn"` or `"deny"`: check the issue's unresolved blockers; `"link"`: `--by N` also marks it blocked by #N |
| `sub_issues`     | check  | `"warn"` or `"deny"`: check the issue's unresolved [sub-issues](commands.md#show) |
| `confirm`        | check  | `"user"`: the verb is the user's decision and runs only with `--yes` |
| `batch`          | —      | `true`: the verb takes several issue numbers at once |
| `to`             | effect | Target status |
| `iteration`      | effect | Set the sprint field: `"current"`, `"by_status_date:<status>"` or `"clear"`; needs `fields.sprint` |
| `field`          | effect | A table of other board fields to set: `[verbs.<name>.field]` with `Priority = "P1"`, `Estimate = 3` |
| `labels`         | effect | Labels to add (`"+name"`) and remove (`"-name"`): `["+in-review", "-blocked"]` |
| `assign`         | effect | Who to add to the assignees: `true` (`board.assignee`), `"@me"`, a login, or `"{assignee}"`: the user `--assignee` names when the verb runs |
| `unassign`       | effect | Who to remove from the assignees: `true` (`board.assignee`), `"@me"`, a login, or `"all"`: everyone but the user the verb assigns |
| `milestone`      | effect | The open milestone to put the issue in: a title (`"v1.2"`, or a start only one title has), or `"{milestone}"`: the one `--milestone` names when the verb runs |
| `comment`        | effect | [Comment template](#comment-templates) |
| `close`          | effect | Close the issue with this reason: `completed`, `not_planned` or `duplicate` |
| `reopen`         | effect | `true` reopens a closed issue |
| `disabled`       | —      | `true` removes a verb the preset defines; it stands alone in its table |

A verb needs at least one effect: `to`, `comment`, `labels`, `assign`,
`unassign`, `milestone`, `close`, `reopen`, `iteration` or `field`. `comment` and `by` in
`require` and `require_one_of` must appear in the verb's comment template; a
verb that requires `comment` and has no template posts the `--comment` text
as is. `assignee` needs `assign = "{assignee}"`, which requires it anyway, so
it may only be repeated in `require`; so does `milestone`, with
`milestone = "{milestone}"`.
Keys from the design that are not implemented yet (`pre`, `post`) are
rejected with "not supported in this version".

### Checks

- **from**: the issue's status must be one of them. It is skipped for an
  issue already in the verb's `to`, so a repeat is safe; so are
  `from_labels`, `refuse_labels`, `blockers` and `sub_issues`.
- **from_labels**, **refuse_labels**: the issue must have every label of
  `from_labels` and none of `refuse_labels`, or the verb refuses with exit
  code 2, naming the labels and the verbs that add one it needs or remove
  one it refuses (those that can run on the issue as it stands, in its
  status and open or closed, without closing it). This is how a
  state kept in a label, such as on hold, holds back other verbs (see
  [`examples/review.toml`](../examples/review.toml)). Names are written
  without `+` or `-` and match regardless of case, as GitHub's label names
  do; a verb reads up to 100 of the issue's labels for them, GitHub's limit
  per issue; should GitHub report more, a checked label not among those
  read refuses the verb as unknown. A label may not be
  in both lists, nor in the verb's own `labels`: one it requires and adds,
  or refuses and removes, never changes anything, and one it refuses and
  adds, or requires and removes, would refuse the verb's own re-run after
  a partly applied run, which must finish the rest. Checked after `from`,
  before `blockers`; skipped, like `from`, for an issue already in the
  verb's `to`, but not for the repeat of a verb without `to` that closes
  the issue; `--dry-run` refuses the same way.
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
- **sub_issues** `"warn"` and `"deny"` look at the issue's unresolved
  sub-issues by the rule of blockers: a sub-issue is resolved when it is
  closed or in one of [`statuses.resolved`](#statuses), so a parent can move
  on while its sub-issues wait in Ready for a release; one whose board
  status could not be read, or that was not read at all (hidden from the
  token; a verb reads up to 100, GitHub's limit), counts as unresolved.
  `"warn"` prints them on stderr and runs; `"deny"` refuses with exit code
  2. Set it on the verb that finishes an issue, `sub_issues = "deny"` under
  `[verbs.done]`, say; no preset sets it. Blockers are checked first.
  In a batch (`done 40 41 42`, or a release) whose verb closes the issues
  or moves them to a resolved status, the sub-issues the same run moves
  count as resolved: they are planned and written before their parent (so
  the confirmation lines follow that order; the JSON keeps the order of
  the arguments), and the parent is refused if one of them is refused or
  fails to be written.
- **confirm** `"user"`: without `--yes` the verb exits with code 2 and asks
  you to ask the user; re-run it with `--yes` only once they agree. A repeat
  with nothing left to do needs no `--yes`. It is a signal, not a lock: an
  agent could add `--yes` itself, so tell agents to ask first (the instructions
  [`agent install`](commands.md#agent-install) writes say so).

### Effects

- **labels**: every entry starts with `+` or `-`; a name may not be both
  added and removed. Names must match the repository's labels exactly, case
  included: a label to add that is missing fails before anything is
  written, with the repository's labels as the allowed values (create it
  with `gh label create`; a name that differs only in case gets the spelling
  to use instead). A label to remove that the repository does not have is in
  place, unless the issue has it in another case. A verb that removes or checks labels
  reads up to 100 of the issue's labels, GitHub's limit per issue.
  [`gh dojo doctor`](commands.md#doctor) checks every verb's labels.
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
  a user named `all` cannot be named. [`gh dojo doctor`](commands.md#doctor) warns
  about a login of `unassign` that cannot be assigned: the verb removes
  current assignees, so such a login is likely a typo.
- **milestone**: puts the issue in an open milestone of its repository, in
  the same mutation as the other effects; the milestone it had, if any, is
  replaced. A title matches the milestone with that title regardless of
  case, or else the only one whose title starts with it, so `"v0.3"` names
  `v0.3 — extensibility`. A start several titles share is exit code 1
  listing them, and no match is exit code 1 listing the open milestones,
  with `gh api repos/<owner>/<repo>/milestones -f title=…` to create one;
  either fails before anything is written. `milestone = "{milestone}"`
  takes the milestone from `--milestone` when the verb runs, for one that
  changes from issue to issue, such as moving an issue to the next
  release: `gh dojo plan 46 --milestone v0.3`. The flag is then required,
  and `{milestone}` in the comment template is the title as the repository
  spells it; only such a verb takes it. Each issue's milestone is matched in
  its own repository, so in a batch an `owner/repo#N` whose repository has
  no such milestone fails alone. An issue already in the milestone keeps
  it: `milestone unchanged (v0.3 — extensibility)`. The open milestones come
  with the issue's read (page by page past 100). A verb cannot clear a
  milestone. [`gh dojo doctor`](commands.md#doctor) checks that each title
  of the config matches one open milestone of the default repository;
  [`release`](commands.md#release) cannot run a verb with
  `milestone = "{milestone}"`, which needs `--milestone`.
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
  with [`gh dojo doctor`](commands.md#doctor) to check; a cached board schema is read
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

### Turning preset keys off

A verb that overrides a preset verb sets only the keys it names. To turn
off a key the preset sets:

| Key | Off |
|-----|-----|
| `close` | `close = false` (kanban's `done` closes; this keeps issues open in Done) |
| `blockers` | `blockers = false` (no check, no link) |
| `sub_issues` | `sub_issues = false` |
| `confirm` | `confirm = false` |
| `assign` | `assign = false` |
| `unassign` | `unassign = false` |
| `milestone` | `milestone = false` |
| `reopen` | `reopen = false` |
| `batch` | `batch = false` |
| `iteration` | `iteration = ""` |
| `from` | `from = []` (any status) |
| `from_labels`, `refuse_labels` | `from_labels = []`, `refuse_labels = []` |
| `labels` | `labels = []` |
| a whole verb | `disabled = true`, alone in its table |

## Comment templates

Templates may use `{session}`, `{comment}` (the `--comment` text), `{by}`
(the `--by` value), in a verb with `assign = "{assignee}"`, `{assignee}`
(the login it assigns), and in a verb with `milestone = "{milestone}"`,
`{milestone}` (the title of the milestone it sets). A line whose placeholders all come out empty is
dropped, so `"Waiting for {by}."` disappears when `--by` is not given; runs
of blank template lines collapse, trailing spaces and leading and trailing
blank lines are cut, and indentation stays. A template that renders to
nothing posts no comment. There is no escape for a literal `{comment}`;
other braces such as `{}` are plain text. A verb whose template does not use
`{comment}` or `{by}` rejects `--comment` or `--by`.

Comments go to the issue as written, so write templates and `when` lines in
your project's language; the CLI itself speaks English.

## Checked against the board

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

## Presets

With `preset = "kanban"` or `preset = "sprints"` the file is applied on top
of the preset: `[fields]`, `[statuses]`, `[labels]` and `[session]` override
key by key; a verb with a preset's name overrides only the keys it sets; a
new name adds a verb; `disabled = true` removes a preset verb. Without
`preset` every verb is defined in the file. `when` lines and comments are in
English: override them to write them in your project's language, as
[`examples/kanban.toml`](../examples/kanban.toml) and this repository's own
[`.github/dojo.toml`](../.github/dojo.toml) do.

Both presets set `fields.status = "Status"`, `statuses.initial = "Backlog"`,
`statuses.final = ["Done"]`, `statuses.resolved = ["Ready", "Done"]` (a
blocker in Ready is done, so it no longer blocks, though its issue stays
open until Done), `labels.types = []` (any label without the area prefix is
a type) and `labels.area_prefix = "area:"`.

## Kanban preset

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

Its source is [`internal/config/presets/kanban.toml`](../internal/config/presets/kanban.toml).
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

## Sprints preset

The kanban flow with work counted in sprints. The board needs the
[kanban statuses](#kanban-preset) in its built-in `Status` field, and an
iteration field named `Sprint` with an iteration for today (GitHub: add a
field of type Iteration; [`gh dojo doctor`](commands.md#doctor) checks today's). The
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

Its source is [`internal/config/presets/sprints.toml`](../internal/config/presets/sprints.toml).
Override on top of it like any preset; [`examples/sprints.toml`](../examples/sprints.toml)
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

## Your own process

The presets are a starting point, not a requirement: a process that is
neither kanban nor sprints is configured in `.github/dojo.toml` alone,
without changing gh-dojo. [`examples/review.toml`](../examples/review.toml) is
a code review flow with no preset: an agent writes the change and opens a
pull request, a person reviews it, and approving or rejecting it is theirs
to decide.

| Verb              | Moves to          | From                                | Also |
|-------------------|-------------------|-------------------------------------|------|
| `start`           | In progress       | Backlog                             | refused while a blocker is unresolved; assigns the author |
| `hold`            | status unchanged  | In progress, Changes requested      | label `+blocked`; `--by 17` marks it blocked by #17 |
| `resume`          | status unchanged  | In progress, Changes requested, Approved | label `-blocked`; refused while a blocker is unresolved |
| `review`          | In review         | In progress, Changes requested      | refused on hold; needs `--comment` (the pull request) and `--assignee` (the reviewer); labels; assigns the reviewer |
| `request-changes` | Changes requested | In review                           | needs `--comment`; labels; back to the author alone: the reviewer is unassigned |
| `approve`         | Approved          | In review                           | refused on hold; the user's decision (`--yes`); back to the author alone |
| `reject`          | status unchanged  | In progress, In review, Changes requested, Approved | the user's decision; closes as not planned, on hold or not |
| `merged`          | Merged            | Approved                            | refused on hold; closes as completed; several issues at once |

On hold is the label `blocked`, not a status, so a held issue keeps its
status and `resume` leaves it there: `refuse_labels = ["blocked"]` keeps
`review`, `approve` and `merged` off it. `hold` and `resume` check no
label, so a repeat of either finishes what a partly applied run left; on
an issue not on hold, `resume` removes nothing. `resume` also runs in
Approved, so an approved issue labelled `blocked` by hand has a way back
to `merged` other than `reject`; the refusals name it.

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
3. A verb is a `when` line, [checks](#checks) (`from`, `from_labels`,
   `refuse_labels`, `require`, `require_one_of`, `blockers`, `sub_issues`,
   `confirm`) and [effects](#effects) (`to`,
   `labels`, `assign`, `unassign`, `comment`, `close`, `reopen`,
   `iteration`, `field`, the `blockers` link). A verb without `to` leaves
   the status alone, like `hold` above; one with `confirm = "user"` waits
   for a person.
4. Check it: [`gh dojo doctor`](commands.md#doctor) against the board and the
   repositories (statuses, the labels verbs add, remove and check, the users they
   assign and unassign by login, the type and area labels), `gh dojo
   describe` for the process
   as an agent reads it, `gh dojo <verb> N --dry-run` for a verb's plan on a
   real issue. Then [`gh dojo agent install`](commands.md#agent-install) writes the
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
