# Commands

| Command | What it does |
|---------|--------------|
| [`show`](#show) | Read issues: board status, sprint, blockers, history, description, comments |
| [`list`](#list) | One line per open issue on the board, grouped by status |
| [`next`](#next) | Issues to take: initial status, no unresolved blockers, in board order |
| [`new`](#new) | Create an issue with labels, assignees, milestone, blockers and parent, on the board, in the initial status |
| [`comment`](#comment) | Comment without changing the status |
| [`block`, `unblock`](#block-unblock) | Add or remove blocked-by links without changing the status |
| [`init`](#init) | Write `.github/dojo.toml` for a board and check it |
| [`describe`](#describe) | Print this project's process, for an agent's context |
| [`doctor`](#doctor) | Check config, token, board and repositories, with fixes |
| [`agent install`](#agent-install) | Write the process into a skill, an `AGENTS.md` section or Copilot instructions for agents |
| [`<verb>`](#process-verbs) | The project's process verbs, from the config (`start`, `wait`, …) |
| [`release`](#release) | Apply a verb to the issues a release includes, found through pull requests and commits |

Issues are written `42` or `#42` for the default repository (the first of
[`board.repos`](configuration.md#board)), and `owner/repo#42` or the issue's URL for any
other: in a verb, a batch, `show`, `comment`, both sides of `block` and
`unblock`, `wait --by`, a verb's `--of`, `new --blocked-by` and `--parent`,
and `release --exclude`. A repository outside
`board.repos` is accepted too, since the command names it; a verb adds its
issue to the board like any other.

Output names issues this way:

- the text: an issue of the default repository `#42`, any other
  `owner/repo#42`;
- the JSON: every issue in full, `owner/repo#42`
  ([JSON output](output.md#json-output));
- a verb's comment: as GitHub links them from the issue's own repository
  (`acme/shop#12` on an issue of acme/api), but free text in `--by` goes in
  as is, so a `#12` in it links to the issue's repository.

Every command prints its usage with `--help` and takes `--json`, `--jq` and
`--template` ([JSON output](output.md#json-output)). Flags go before a `--`:
anything after it is an argument.

## show

```sh
gh dojo show <N>... [--brief] [--comments N|all] [--full]
```

Reads issues before working on them:

- state, board status and fields, the sprint (with
  [`fields.sprint`](configuration.md#fields): `sprint: Sprint 12
  (2026-11-09 to 2026-11-22)`), milestone, labels, assignees;
- blockers both ways, the parent issue and the sub-issues ([GitHub's
  sub-issues](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/adding-sub-issues)),
  each with its board status;
- status history on the board, the description and the latest comments.

A line left out means none: a card without a milestone, labels, assignees,
blockers, blocked issues, a parent or sub-issues has no line for them.
Times are UTC.

Unresolved blockers ([the rule](configuration.md#statuses)) are listed
first; a linked issue is shown with its status, "on the board, no status",
"not on the board", or "status not read" and why. Sub-issues follow the same
rule and order, under a header such as `sub-issues: 3 of 5 closed, 1
unresolved`: the unresolved ones are what a verb with
[`sub_issues`](configuration.md#checks) checks. The first 20 sub-issues are
read, with a note when there are more; the closed count is GitHub's, of
all of them (`17 of 55 closed`), and counts an issue closed for any
reason, not planned too. A parent the token cannot read is `parent: not
readable`, not left out.

Long content is folded, always with a note saying what is hidden:

- the last 5 comments are printed (`--comments N` for the last N,
  `--comments all` for up to 100);
- the description is cut at 2000 characters and the comments shown share
  1500;
- the status history is one line with days (`status history: no status →
  Backlog (automation, 2026-11-14) → In progress (alice, 2026-11-14)`);
- a [session link](configuration.md#session) in a comment keeps the last 8
  characters of its ID (`Taken. Session: …491bf84f`).

`--full` prints them all whole, the history a change per line with its time.

`--brief` prints three lines per issue (four when GitHub hid part of it):

1. title;
2. state, status, sprint, milestone, labels and assignees;
3. blockers, the parent and the sub-issues (when it has them) and counts.

It takes neither `--comments` nor `--full` (exit code 1).

Several issues are read in one request (up to 20 issues per request, up to 4
such requests at once), from any repositories: `gh dojo show 42 acme/api#7`. In text they are printed as
with `--brief`, since an agent usually takes one of them and reads it alone;
`--comments N` or `--full` prints their whole cards, and `--json` has whole
cards unless `--brief`. A missing issue, a pull
request number or a missing repository is reported after the others are
printed, with exit code 3.

## list

```sh
gh dojo list [--status S]... [--label L]... [--mine] [--sprint current|TITLE|none] [--all] [--limit N]
```

One line per open issue, grouped by status in board order, issues without a
status first.

- Closed issues and issues in a final status
  ([`statuses.final`](configuration.md#statuses)) are hidden and counted;
  `--all` shows them, and so does `--status` naming a final status.
- `--status` matches the board's statuses in any case (`--status ready`);
  `--status none` selects issues without a status.
- Filters combine with AND, and the issues they leave out are counted.
- Only issues are listed: draft issues and pull requests on the board are
  counted.
- At most `--limit` issues are printed (default 50), with the total.

`list` asks GitHub only for the issues it may show and for how many items
it leaves out, so closed and final issues piling up do not slow it down;
`--all`, and `--status` naming a final status, read every item. So does a
`--label` that no issue has, to suggest the label meant.

GitHub lists a board's items a few seconds behind writes (1.5 to 3.5 s
measured, [#54](https://github.com/Trum-ok/gh-dojo/issues/54)), filtered or
not: an issue `new` or a verb has just created or moved may be missing from
`list` and `next`, or show its old status. `show` reads the issue itself and
is up to date at once, so check one issue with `show`.

Issues in a status that is resolved but not final
([`statuses.resolved`](configuration.md#statuses), such as Ready: done and waiting for a
release) pile up until a release, so their group is one line of numbers:
`Ready (6; titles: --status Ready): #34 #46 #51 #52 #53 #55`. `--status`
naming that status, or `--all`, lists them with their titles. `--json` always
has every issue.

`--sprint` needs [`fields.sprint`](configuration.md#fields) and keeps the issues in one
iteration: `current` (the one covering today in [`board.timezone`](configuration.md#board)),
a title, or `none` (no sprint). The first line names the iteration with its
days; an unknown title lists the iterations.

## next

```sh
gh dojo next [--label L]... [--mine] [--sprint current|TITLE|none] [--all] [--limit N]
```

What to work on next: the open issues in the initial status
([`statuses.initial`](configuration.md#statuses)) or without a status, one line per issue as
in `list`, in the board's own order (issues without a status mixed with the
initial ones, no groups), leaving out the issues with an unresolved blocker.

Blockers are resolved by the rule of `show` and the `blockers` check
([`statuses.resolved`](configuration.md#statuses)). The blocked issues are
counted (`hidden: 2 blocked, use --all`); `--all` shows them too, each with
its blockers:

```text
$ gh dojo next
#34 Run the e2e tests on a sandbox board [enhancement]
hidden: 2 blocked, use --all
$ gh dojo next --all
#34 Run the e2e tests on a sandbox board [enhancement]
#41 Add the release command [enhancement] — blocked by #38 (In progress)
#42 Configure another flow without code [documentation] — blocked by #40 (In progress)
```

- `--label`, `--mine` and `--sprint` filter as in [`list`](#list), and the
  issues they leave out are counted.
- At most `--limit` issues are printed (default 5), with the total.
- Closed issues and issues in other statuses are not candidates; draft
  issues, pull requests and items the token cannot see in the initial status
  or without one are counted as skipped.

Each candidate's blockers are read with it, each with its place on the
board, archived or not, so `next` takes one request (after the schema)
however many issues are done; a `--label` that no issue has reads every
item, as in `list`. It needs `statuses.initial`: without it, it is a
config error (exit code 1).

## new

```sh
gh dojo new <title> [--type T] [--area A]... [--label L]... [--assignee LOGIN]... [--milestone M] [--blocked-by N]... [--parent N] [--repo owner/repo] [--body TEXT | --body-file FILE] [--dry-run]
```

Creates an issue, labels it, adds it to the board and sets the initial status
([`statuses.initial`](configuration.md#statuses)) in one command:

- `--type bug` adds the label `<labels.type_prefix>bug` (with an empty
  [`labels.type_prefix`](configuration.md#labels), any repo label outside the
  areas), `--area cli` adds the label `<labels.area_prefix>cli`, a value may
  also carry its prefix, and `--label` adds any existing label;
- `--assignee octocat` assigns a user who can be assigned issues in the
  repository, `@me` the authenticated user (repeatable, or comma-separated;
  GitHub allows 10). Logins are matched ignoring case, and `@me` with your
  own login counts once;
- `--milestone v1.2` puts the issue in an open milestone, matched as a
  verb's [`milestone`](configuration.md#effects) is (`v0.3` for
  `v0.3 — extensibility`); `none` is no milestone. A verb with
  `milestone = "{milestone}"` moves an existing issue to another one;
- `--blocked-by 17` marks it blocked by #17 (repeatable, or comma-separated;
  `owner/repo#17` or a URL for another repository); `--by 17`, as
  [`wait`](#process-verbs) and [`block`](#block-unblock) call it, is the
  same flag, left out of the help;
- `--parent 40` makes it a sub-issue of #40, set as it is created (no extra
  request); [`show`](#show) lists a parent's sub-issues, and a verb with
  [`sub_issues`](configuration.md#checks) checks them. An existing issue becomes a sub-issue
  with `gh issue edit 15 --parent 40`: gh-dojo has no command for it, since
  the link changes no status. A parent with 100 sub-issues, GitHub's limit,
  fails before anything is created (exit code 1); one GitHub refuses
  otherwise (8 levels deep, a cycle, out of the token's reach) is exit
  code 1 once the rest is done, with GitHub's reason, since re-running
  cannot help;
- `--repo acme/api` creates it in another repository of
  [`board.repos`](configuration.md#board) than the first: its labels, milestones, assignees and
  repeat check are that repository's, the issue is named `acme/api#15`, and a bare
  `--blocked-by` or `--parent` number is still in the default repository. A repository
  not in `board.repos` is refused (exit code 1, listing them);
- `--body-file -` reads the body from stdin. A title starting with `-` goes
  after `--`: `gh dojo new --type bug -- "-v is ignored"`.

These fail before anything is created:

- an unknown label or milestone, listing the allowed values (exit code 1);
- `--assignee` logins that cannot be assigned issues in the repository, all
  named at once, listing who can be (exit code 1);
- a `--blocked-by` or `--parent` issue that does not exist, or a pull
  request (exit code 3).

```console
$ gh dojo new "Fix login redirect" --type bug --area cli
#15 "Fix login redirect" created, labels bug, area:cli, no status → Backlog
```

A repeat is safe: the body ends with a hidden marker of the title, and a run
that finds it on one of the repository's 20 newest open issues written by
you finishes that issue (labels, assignees, milestone, board, status,
blocked-by links, parent) instead of creating another:

- assignees it has stay (`--assignee` only adds; one that would take it
  past 10 assignees is exit code 1 before anything is written);
- a different milestone or parent it already has (or a parent the token
  cannot read) is kept, with a warning naming the command to change it.

If a run fails after creating the issue, it exits with code 5: run the
same command again.

`--dry-run` reads and checks everything a run does, so an unknown label, an
assignee who cannot be assigned or a missing blocker fails the same way,
and prints the plan instead of writing: the issue it would create, or, for
one an earlier run created, what finishing it would do.

A classic token without the `project` scope is refused after the read,
before the issue is created, `--dry-run` too: exit code 4 with the fix
(see [Exit codes](output.md#exit-codes)).

```console
$ gh dojo new "Fix login redirect" --type bug --area cli --assignee @me --blocked-by 26 --dry-run
dry run: new "Fix login redirect"
  create: in Trum-ok/gh-dojo
  labels: bug, area:cli
  assignees: Trum-ok
  add to the board
  status: no status → Backlog
  blocked by: #26
```

## comment

```sh
gh dojo comment <N> [text] [--body-file FILE] [--dry-run]
```

Comments on an issue without changing its status. The text comes from the
second argument or from `--body-file` (`-` reads stdin), exactly one of them.
Text starting with `-` goes after `--`: `gh dojo comment 42 -- "-1, still
broken"`. When the issue's last comment is yours with the same text, it is
not posted again. `--dry-run` reads the issue and prints the comment it
would post, or that it is the last comment already, without posting.

## block, unblock

```sh
gh dojo block <N> --by <M>... [--dry-run]
gh dojo unblock <N> --by <M>... [--dry-run]
```

Marks issue N blocked by the issues `--by` names, or removes those links,
without changing its status. N and `--by` take `12`, `"#12"`, `owner/repo#12`
or the issue's URL; repeat `--by` or separate the issues with commas.

- A link already there (`block`) or already gone (`unblock`) is not an
  error, and the line says so.
- An issue that does not exist, or a pull request, fails before anything
  is written (exit code 3).
- An issue cannot block itself, nor an issue it already blocks: GitHub
  refuses cycles (exit code 1).

`--dry-run` reads and checks the same and prints the links it would change,
then those already as asked, without writing.

```console
$ gh dojo block 42 --by 17,18
#42 blocked by #18, already blocked by #17
$ gh dojo unblock 42 --by 17
#42 no longer blocked by #17
$ gh dojo block acme/api#7 --by 42
acme/api#7 blocked by #42
```

Verbs check and add the same links: see [`blockers`](configuration.md#checks).

## init

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

## describe

```sh
gh dojo describe
```

Prints the project's process in a few lines per verb, short enough for an
agent's context:

- when to use each verb, the arguments it needs or takes, what it checks
  and does along the way (comment, sprint, other board fields, milestone,
  labels, assignees, closing or reopening, blocked-by links, the label,
  blockers and sub-issues checks, closed issues, the user's go-ahead);
- then the lifecycle (from → to), the initial, final and resolved
  statuses, and the exit codes;
- with an initial status, a pointer to [`next`](#next) to find an issue.

It reads only the config, never the network; a missing or broken config is
an error (exit code 1). `gh dojo <verb> --help` has the details of one
verb.

## doctor

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
| `config` | `.github/dojo.toml` loads; a key that still loads but does nothing (a verb's `batch`) is `warn`, with the fix to remove it |
| `token` | gh is logged in, and the token has the `project` scope (from `GH_TOKEN`: "set GH_TOKEN to …") |
| `board` | The board exists, is open, and has every field and status the config names, each mismatch on its own line |
| `repo` | Each repository of [`board.repos`](configuration.md#board), or the current one, is visible with issues enabled; a warning when `board.repos` spells one otherwise than GitHub does |
| `labels` | With [`labels.type_prefix`](configuration.md#labels) or `labels.area_prefix` set, in each repository of the `repo` check: how many labels start with each prefix. None is `warn` when `.github/dojo.toml` sets the prefix, since `new --type` or `--area` finds nothing (create one, or set the prefix to `""`); with the prefix from a preset, which many teams never use, it is a note on the `ok` line. Past 100 labels a count is a lower bound ("at least"), and none found is "could not confirm" rather than a warning |
| `sprint` | With [`fields.sprint`](configuration.md#fields): an iteration covers today, a date in [`board.timezone`](configuration.md#board) |
| `verbs` | When verbs have labels, assignees or milestones: the labels exist in the default repository, spelled exactly as there (those of `from_labels` and `refuse_labels` in any case, as the checks match them), the users can be assigned, and each milestone a verb names in the config matches one open milestone there, as the verb matches it (none or several is a problem) |
| `agent` | With [`[agent]`](configuration.md#agent) set (and `targets` not empty), or a file with a gh-dojo marker: the files [`agent install`](#agent-install) writes are up to date (`agent install --check`, without the diff; a file out of date names the version that wrote it when another). Out of date or missing is `warn`, since the text also depends on the gh-dojo version; a hand-written skill under `[agent]` is a problem |

It sends:

- one request for the token and the repositories;
- one more for the board when its schema is not cached;
- one for the labels when `labels.type_prefix` or `labels.area_prefix` is
  set;
- one for the verbs' labels, assignees and milestones when a verb has them
  (plus the open milestones page by page past 100).

The last three go at once, after the first: they need what it found.

The exit code is 0 when no check is a problem (warnings included), 4 when
one failed on auth or the network, else 1.

## agent install

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
agent should do about it: use only `gh dojo` for issues, the lifecycle and
the statuses, each verb with its rules (ask the user before `--yes`, don't
work around a refused check), the commands to find, read and create issues
(with `new`'s flags, so one of them needs no `new --help`), and what to do
on an error, with safe repeats. This repository's own
[`.claude/skills/gh-dojo/SKILL.md`](../.claude/skills/gh-dojo/SKILL.md) is
an example.

The skill's `description` names the repository, the board and the verbs,
and [`[agent] description`](configuration.md#agent) adds to it. The
markdown file [`[agent] notes`](configuration.md#agent) names is appended
as it is, in the project's language, for what the config cannot say.

The skill is gh-dojo's whole file, marked as generated after its front
matter; a skill without that marker, written by hand, is not overwritten
(exit code 1): move what is worth keeping into the notes and delete it.

The marker names the gh-dojo version that wrote the text:
`<!-- gh-dojo:generated by gh dojo agent install v0.4.0 from .github/dojo.toml: ... -->`.
A file that differs only in that version, or whose marker names none
(written before markers did), is up to date and is not rewritten, so the
version is the one that last changed the text, and a CI pinned to an
older version still passes.

In `AGENTS.md` and the Copilot file gh-dojo owns only the lines from
`<!-- gh-dojo:begin ... -->` to `<!-- gh-dojo:end -->`, and the rest of
the file stays byte for byte; a file without the markers gets the section
at its end. Marker lines count anywhere, fenced code blocks included, so
don't quote them in those files; markers out of order are an error, and
nothing is written. Reads and writes stay inside the repository: only
relative symbolic links that point inside it are followed.

A repeat with the same config writes nothing and prints `up to date` for
each file. `--check` writes nothing either, and reports a file `missing`,
`out of date` (with up to 20 changed lines, and `written by gh dojo
v0.3.0, this is v0.4.0` when another version wrote it) or `hand written`
(a skill), with exit code 1 and the fix: `gh dojo agent install`, or for a
hand-written skill moving it into the notes first. Paths are relative to
the repository root, the directory that holds `.github`; agent install
never uses the network.

The files depend on the config, the notes and the gh-dojo version (its text
and its command list), not on where it runs. So pin the version that checks
them in CI, and run `agent install` with that version when you upgrade:

```yaml
- run: gh extension install Trum-ok/gh-dojo --pin v0.3.0
  env:
    GH_TOKEN: ${{ github.token }}
- run: gh dojo agent install --check
```

[`doctor`](#doctor) only warns about files out of date: the installed
gh-dojo may be a different version than the one in CI.

## Process verbs

```sh
gh dojo <verb> <N>... [--comment TEXT] [--by N|TEXT] [--assignee LOGIN] [--milestone TITLE] [--of N] [--yes] [--dry-run] [--show] [--comments N|all] [--full]
```

Verbs come from [`[verbs]`](configuration.md#verbs) in the config, so each project has its
own; the [kanban](configuration.md#kanban-preset) and [sprints](configuration.md#sprints-preset) presets
define `start`, `wait`, `ready` and `done`. `gh dojo --help` lists them with
their `when` line and points to `gh dojo describe` for the lifecycle and the
exit codes, so an agent that reads both reads them once; `gh dojo <verb>
--help` shows the allowed starting statuses, the target status, the required
arguments, the other effects and the comment template. A subcommand's help
names the global flags (`--json`, `--jq`, `--template`) in one line; the
root help describes them.

A verb takes one issue number or several (see
[Several issues at once](output.md#several-issues-at-once)) and, for each
issue, in one command:

1. checks, in this order, that the required arguments are given, that the
   issue is open (a verb that moves it to a final status, closes it or
   reopens it also runs on a closed one, see
   [`close`](configuration.md#effects)), that its status is one of the
   verb's `from` statuses (no status, or not on the board, counts as
   [`statuses.initial`](configuration.md#statuses)), the issue's labels (`from_labels`,
   `refuse_labels`), its blockers (`blockers`) and sub-issues
   (`sub_issues`), and the user's go-ahead (`confirm`);
2. adds the issue to the board if it is not there; an archived item is
   unarchived when the verb moves it to a status that is not final;
3. applies its [effects](configuration.md#effects): reopens it, sets the status and the
   sprint, sets other board fields, sets the milestone, adds and removes labels, assigns and
   unassigns users, marks it blocked by the `--by` issues, posts the rendered
   [comment template](configuration.md#comment-templates) and closes it.

Each effect already in place is skipped: a status already set, labels
already there or gone, a comment that is already the latest gh-dojo comment,
an issue already closed (whatever its reason), and so on. Everything is
written in one GraphQL mutation, each effect its own field of it, applied
or failing on its own (two mutations when the issue is not on the board
yet: the status needs the new board item), so a verb on an issue on the
board costs one read and one write.

A verb takes these flags only when its config asks for them:

| Flag | Taken by a verb with | Gives |
|------|----------------------|-------|
| `--comment` | `{comment}` in its [template](configuration.md#comment-templates), or `require = ["comment"]` and no template | The text |
| `--by` | `{by}` in its template | An issue (rendered `#12`) or free text; with [`blockers = "link"`](configuration.md#checks) the issues are marked as blocking this one |
| `--assignee` | [`assign = "{assignee}"`](configuration.md#effects), which requires it | The user to assign, a login or `@me`; also `{assignee}` |
| `--milestone` | [`milestone = "{milestone}"`](configuration.md#effects), which requires it | The milestone to set, or `none`; also `{milestone}` |
| `--of` | [`close = "duplicate"`](configuration.md#effects), which requires it | The original (`17`, `owner/repo#17` or a URL); also `{of}` |
| `--yes` | [`confirm = "user"`](configuration.md#checks) | The user's go-ahead |

Any other of these flags given to a verb that does not take it is exit
code 1, with the flags it takes. `--yes` is the exception: given to a verb
without `confirm = "user"` it is ignored with a warning (`start needs no
confirmation: --yes ignored`), since the agent cannot know which verbs of a
project need the go-ahead, and one the verb does not need loses nothing.

A classic token without the `project` scope is refused after the cards are
read, before any write and for every issue, `--dry-run` too: exit code 4
with the fix, rather than a comment left and the status not changed (see
[Exit codes](output.md#exit-codes)).

`--dry-run` prints the plan without changing anything:

```console
$ gh dojo wait 42 --by 17 --dry-run
dry run: wait #42 "Fix login redirect"
  status: In progress → Waiting
  blocked by: #17
  comment: "Waiting for #17."

$ gh dojo drop 42 --comment "Not needed after #17."
error: drop needs the user's go-ahead for #42 (would be: comment left, closed as not planned)
this is the user's decision: ask the user, and re-run with --yes only if they agree

$ gh dojo drop 42 --comment "Not needed after #17." --yes
#42 comment left, closed as not planned

$ gh dojo duplicate 42 --of 17
#42 comment left, closed as duplicate of #17
```

`--show` also prints the issue's card, as [`show`](#show) prints one issue,
so taking an issue and reading it is one command, `gh dojo start 42 --show`,
not two. The card is the one the verb read to plan it, so the flag costs no
request. It follows the confirmation line or the dry-run plan; when the verb
changed the issue, the card is the issue as it was before, under `card as
read before start:`, and the confirmation line says what changed. A verb
that refuses the issue (exit code 2), needs `--yes` or writes it in part
prints the card alone, its error on stderr, as long as the issue was read.

`--comments N|all` and `--full` fold the card as [`show`](#show)'s flags
do, and imply `--show`, as `--jq` implies `--json`: `gh dojo start 42
--full` takes the issue and prints its card whole. The verb reads as many
comments as the card prints, at least the 20 it reads anyway, in its own
request: `--comments all` costs no request either.

`--show`, `--comments` and `--full` take one issue: a verb given several
numbers refuses them, and `gh dojo show 12 13` reads them. In `--json`
the issue's `card` is the card of [`show --json`](output.md#json-output),
`null` without these flags.

```console
$ gh dojo start 42 --show
#42 Backlog → In progress, comment left

card as read before start:
#42 Fix login redirect
state: open, opened 2026-11-10 by alice
board: Backlog
...
```

The board's built-in workflows run after the verb and may change the status
it set: "Item closed" moves an issue a verb closed (to Done by default),
"Item reopened" moves one it reopened. Turn them off in the board's workflow
settings, or point them at the statuses your verbs set.

Every verb takes several numbers: `gh dojo done 12 13 14`. Each issue is
checked and written on its own; see
[Several issues at once](output.md#several-issues-at-once) for the summary
and the exit code. [`release`](#release) runs a verb on the issues of a
release.

## release

```sh
gh dojo release <ref> --verb <verb> [--since <ref>] [--exclude N]... [--dry-run] [--yes] [--comment TEXT] [--by TEXT]
```

Once a release is tagged, runs a verb on the issues it includes, as
`gh dojo done 12 13 14` would: same checks, summary and exit codes ([Several issues at once](output.md#several-issues-at-once)).
Before the tag, `gh dojo release main --verb done --dry-run` shows what the
next release holds.

```console
$ gh dojo release v0.2.0 --verb done --dry-run
release v0.2.0: 5 commits since v0.1.0, 4 issues
already closed: #9 (Done)
not ready (done starts only from Ready): #17 (In progress)
  #3: commit 1111111
  #7: commit 1212121, commit a1a1a1a
dry run: done #3 "Read the config"
  status: Ready → Done
  close: as completed
...
```

- `--dry-run` lists how each issue was found (`PR #N`, `commit <sha>`,
  `suffix of commit <sha>`) and its plan.
- A fix reverted in the range is left out, listed as `reverted: #12
  (commit 1a2b3c4)` ([Reverts](#reverts)).
- No issues is a line saying where release looked, exit code 0.
- `--comment`, `--by` and `--yes` go to the verb, for every issue; `--yes`
  to a verb that needs no go-ahead is one warning for the release.
- Any verb runs but one that needs `--assignee`, `--milestone` or `--of`
  (`assign = "{assignee}"`, `milestone = "{milestone}"`,
  `close = "duplicate"`), which release does not take: that is exit code 1;
  run it on the issues themselves.
- `--exclude N` (`#N`, `owner/repo#N` or a URL; repeatable, or
  comma-separated) leaves out any issue found, by a pull request or a
  message, before it is read: the line `excluded: acme/api#3 (commit
  de25f45)` and the JSON's `skipped` (`excluded`) name it, and an
  `--exclude` the release does not include is a warning.
- For a verb that finishes issues, a warning names the board's issues in
  Ready the release does not include ([Ready, not in the
  release](#ready-not-in-the-release)).
- A `<ref>` or `--since` GitHub does not know is exit code 3; for one with
  a `/`, such as `origin/main`, one more request checks the name after it,
  and if GitHub has it the `fix:` names it (`pass main`).
- Requests: one for `<ref>` and the start (one per 100 tags), one per 100
  commits with their pull requests, then the batch's, then for a verb that
  finishes issues one for the board's issues in Ready.

### The range

The range is the commits of `<ref>` (a tag, branch or commit on GitHub; no
local checkout, so `main`, not `origin/main`) since `--since`, or else
since the previous release:

- The previous release is the highest version tag (`v1.2.3`, `1.2.3`)
  below `<ref>` when `<ref>` is one, else the highest not on `<ref>`'s
  commit. A start that is not in `<ref>`'s history, like a `--since` newer
  than `<ref>`, is exit code 1, not an empty release.
- Prereleases count only for a prerelease, so `v0.2.0` starts from
  `v0.1.0`, past `v0.2.0-rc.1`: an issue already done costs no write, a
  range too short misses issues.
- The first release needs `--since` (exit code 1 without it): taking the
  whole history would apply the verb to every issue any commit ever named.
- GitHub compares a tag or branch with `<ref>` exactly; from a commit (a
  SHA, `HEAD~10`), which the API cannot compare, `<ref>`'s history is read
  down to it, exact for a linear history (with merge commits, a merged
  branch's commits older than the start are missed, but their pull request
  is found through the merge commit).
- At most 1000 commits; a longer range is exit code 1. The previous release
  is looked for among the 1000 newest tags; up to 5 pull requests per
  commit and 25 closing issues per pull request are read, and the rest are
  left out with a warning.

### Issues in the release

An issue is in the release when:

- a merged pull request a commit belongs to closes it (its closing keywords
  or linked issues, `closingIssuesReferences`; squash, merge and rebase
  alike), or
- a commit message names it after a word of
  [`[release] keywords`](configuration.md#release), by default
  GitHub's closing keywords: `Fixes #12`, `closes: acme/api#3`,
  `Resolves <issue URL>`, any case; a list after a keyword counts whole up
  to the end of its line (`Fixes #12, #13 and #14`), a mention without one
  (`see #12`) does not.

`Refs #12` says a commit is about an issue, which may be done only in part,
so `refs` is not a default; a project that commits without pull requests
adds it, as this one does.

With [`[release] suffix = true`](configuration.md#release), the
parentheses that end a message's first line name issues too: `fix: login
limit (#12)`, `(#12, acme/api#3)`. A project whose commits go without pull
requests often marks them so.

- Each group holds only references separated by commas, and several groups
  in a row all count: `fix (#161) (#200)`, a squash merge's `(#PR)` after a
  title that names its issue.
- Nothing may follow but spaces, so `(#12).` ends no suffix; nor does
  the quoted title of `Revert "fix (#12)"` (see [Reverts](#reverts)).
- Parentheses in the middle of the line (`(see #12) fix`) or in the body do
  not count.
- The number of a pull request the commit belongs to, the `(#PR)` a squash
  merge appends, is left out without a word; that pull request's issues are
  found through it.

The suffix is off by default because where pull requests are
squash-merged, the parentheses name a pull request, not an issue.

With the suffix off, an issue only a suffix names is not in the release,
and release says so rather than miss it silently: a warning on stderr and
in the JSON's `warnings` names the issue and its commits, and what to do:

```console
warning: #85: named only in the suffix of commit b428f16, and a title's (#85) counts only with [release] suffix = true: left out; if #85 is an issue of this release, run gh dojo done 85, and name issues as Refs #85 in later commits
```

- The command is the `--verb`'s; the keyword, `refs` if
  [`[release] keywords`](configuration.md#release) has it, else `fixes`,
  else the first; with no keywords the warning says to set `suffix = true`.
- The number is not read (no request), so it may be a pull request: hence
  the "if".
- No warning for an issue the release includes anyway (a pull request, a
  keyword, another commit), for the `(#PR)` of the commit's own pull
  request, nor for the suffix of a commit reverted in the range.
- [`agent install`](#agent-install) tells the agent to name the issue after
  a keyword, so the commits it writes do not need the warning.

Code in a message does not count, as Markdown does not link it: a code span
(`` `Refs #12` ``), a fenced block (```` ``` ````) or an indented block
(four spaces or a tab, after a blank line) is an example of the syntax, not
a link, so put such examples in backticks.

### Reverts

A fix reverted in the same release is not released. A commit that a later
commit of the range reverts counts for nothing, neither its message nor its
pull requests, when the revert says so as git and GitHub write it:

- `This reverts commit <sha>.`, which `git revert` and GitHub's Revert
  button write; a reverted merge commit (`…, reversing changes made to
  <sha>.`) takes its pull request with it, all its commits;
- `Reverts owner/repo#20` on a line of its own, the description of the pull
  request GitHub's Revert button opens, which a squash merge may put in the
  message: every commit of that pull request in the range is reverted.

A revert reverted in turn (`Reapply "…"`) reverts nothing, so the original
counts again. One rebased commit of a pull request reverted leaves the pull
request in, through its other commits. An issue that only reverted commits
name or close is listed as `reverted: #12 (commit 1a2b3c4)` and in the
JSON's `skipped` (`reverted`), not read; one a commit that stays names too
is in the release, found through that commit.

The quoted title of a revert never names an issue: `Revert "Fixes #12"`
(also in a merge commit's body or a squash merge's list of commits) quotes
the reverted commit, so a revert of a commit of an earlier release does not
put `#12` in this one. Words of the revert's own count: `Refs #40` in its
body, the `(#21)` a squash merge appends after the quote.

### Numbers that are no issue

A message is text, a guess at a link, where a pull request closing an
issue is GitHub's own. So a number only commit messages name that is no
issue is left out with a warning on stderr and listed in the JSON's
`skipped`, not failed:

- a pull request (`pull_request`);
- a missing issue (`not_found`);
- an issue of a repository outside `board.repos` that is not found
  (`repo_not_found`), such as an example `acme/api#3` in a message. GitHub
  answers the same for a private repository the token cannot see, so the
  warning says so: `acme/api#3: repository acme/api not found, or the
  token cannot see it: left out (commit de25f45)`; if the issue is real,
  give the token access and run the verb on it.

These fail the batch as any issue does:

- the issues of a repository of `board.repos` (or the default one) not
  found, which is the token's lack of access almost surely (exit code 3);
- any other error: a token GitHub denies outright (SAML single sign-on,
  OAuth app restrictions) fails the batch as a network error, whose fix is
  the token's;
- an issue a merged pull request closes, even if a message names it too.

### Not ready

An issue whose status the verb's `from` does not allow:

- open, such as one still in progress with a `Refs` commit or a suffix
  when `done` starts only from Ready: listed as not ready and left out, not
  failed. Work goes on across releases, and a refusal (exit code 2) would
  end every such release. Only a later release whose commits name it again
  picks it up; otherwise run the verb on it (`gh dojo done 12`) once it is
  ready;
- closed (closed by a `Fixes #12` while in progress, say): a refusal (exit
  code 2), since no release would move it.

The verb's other checks (blockers, `--yes`) fail the batch as well.

### Already closed

A closed issue in a final status ([`statuses.final`](configuration.md#statuses))
is done with, whatever the verb: listed as `already closed: #200 (Done)` and
in the JSON's `skipped` (`closed`), not touched, and without effect on the
exit code. So issues that went straight to Done without a release (docs, CI)
are not refused by a verb that runs only on open issues, nor given today's
sprint by a repeat of `done` when they have none. One the board's own
workflows moved there ("Item closed" after a `Fixes #12` commit; the status
history marks the change automated) did not go through the process: the
verb gets it as any issue.

### Ready, not in the release

Work committed with no link to its issue (no keyword, no suffix, no pull
request) goes into the tag while its issue stays in Ready, unnoticed. So
for a verb that finishes issues (closes them as completed or moves them to
a final status, as `done` does), release then reads the board's issues in
the statuses where finished work waits for a release: those of
[`statuses.resolved`](configuration.md#statuses) that are not final and
that the verb starts from, Ready in the presets. Those it did not find, by
any way and whatever it did with them, are named in a warning with the
command that runs the verb on them, the release's `--comment` and `--by`
included, and in the JSON's `not_in_release`:

```console
$ gh dojo release v0.7.0 --verb done --comment "v0.7.0"
...
warning: in Ready but not found in the release: #67, #88, #89; for any it includes: gh dojo done 67 88 89 --comment "v0.7.0"
```

Release cannot tell whether such an issue shipped: run the command on those
that did, leave out the rest. The warning keeps the exit code. It costs one
request, which GitHub filters by status (`items(query: …)`), so it does not
grow with the board; a range with no commits, or a verb that finishes no
issue, reads nothing. A read that fails is a warning too (`not checked for
issues in Ready …`), the release itself done.
