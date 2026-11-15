# Commands

| Command | What it does |
|---------|--------------|
| [`show`](#show) | Read issues: board status, sprint, blockers, history, description, comments |
| [`list`](#list) | One line per open issue on the board, grouped by status |
| [`next`](#next) | Issues to take: initial status, no unresolved blockers, in board order |
| [`new`](#new) | Create an issue with labels, milestone, blockers and parent, on the board, in the initial status |
| [`comment`](#comment) | Comment without changing the status |
| [`block`, `unblock`](#block-unblock) | Add or remove blocked-by links without changing the status |
| [`init`](#init) | Write `.github/dojo.toml` for a board and check it |
| [`describe`](#describe) | Print this project's process, for an agent's context |
| [`doctor`](#doctor) | Check config, token, board and repositories, with fixes |
| [`agent install`](#agent-install) | Write the process into a skill or `AGENTS.md` section for agents |
| [`<verb>`](#process-verbs) | The project's process verbs, from the config (`start`, `wait`, …) |
| [`release`](#release) | Apply a verb to the issues a release includes, found through pull requests and commits |

Issues are written `42` or `#42` for the default repository (the first of
[`board.repos`](configuration.md#board)), and `owner/repo#42` or the issue's URL for any
other: in a verb, a batch, `show`, `comment`, both sides of `block` and
`unblock`, `wait --by` and `new --blocked-by`. A repository outside
`board.repos` is accepted too, since the command names it; a verb adds its
issue to the board like any other. The text names an issue of the default
repository `#42` and any other `owner/repo#42`; the JSON names every issue
in full, `owner/repo#42` ([JSON output](output.md#json-output)). A verb's comment
names issues as GitHub links them from the issue's own repository
(`acme/shop#12` on an issue of acme/api), but free text in `--by` goes in
as is, so a `#12` in it links to the issue's repository. Every command
prints its usage with `--help` and takes `--json`, `--jq` and `--template`
([JSON output](output.md#json-output)). Flags go before a `--`: anything after it is
an argument.

## show

```sh
gh dojo show <N>... [--brief] [--comments N|all] [--full]
```

Reads issues before working on them: state, board status and fields, the
sprint (with [`fields.sprint`](configuration.md#fields): `sprint: Sprint 12 (2026-11-09 to
2026-11-22)`), milestone, labels, assignees, blockers both ways, the
parent issue and the sub-issues ([GitHub's
sub-issues](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/adding-sub-issues)),
each with its board status, status history on the board, the description
and the latest comments. Times are UTC. Unresolved blockers (see
[`statuses.resolved`](configuration.md#statuses)) are listed first; a linked issue is shown
with its status, "on the board, no status", "not on the board", or "status
not read" and why, and a blocker whose status was not read counts as
unresolved. Sub-issues follow the same rule and order, under a header such
as `sub-issues: 3 of 5 closed, 1 unresolved`: the unresolved ones are what
a verb with [`sub_issues`](configuration.md#checks) checks. The first 20 sub-issues are
read, with a note when there are more; the closed count is GitHub's, of
all of them (`17 of 55 closed`), and counts an issue closed for any
reason, not planned too. A parent the token cannot read is `parent: not
readable`, not `none`.

Long content is folded, always with a note saying what is hidden: the last 5
comments are printed (`--comments N` for the last N, `--comments all` for up
to 100); the description is cut at 2000 characters and the comments shown
share 1500. `--full` prints them whole. `--brief` prints three lines per
issue: title; state, status, sprint, milestone, labels and assignees;
blockers, the parent and the sub-issues (when it has them) and counts.

Several issues are read in one request (up to 20 issues per request), from
any repositories: `gh dojo show 42 acme/api#7`. A missing issue, a pull
request number or a missing repository is reported after the others are
printed, with exit code 3.

## list

```sh
gh dojo list [--status S]... [--label L]... [--mine] [--sprint current|TITLE|none] [--all] [--limit N]
```

One line per open issue, grouped by status in board order, issues without a
status first. Closed issues and issues in a final status
([`statuses.final`](configuration.md#statuses)) are hidden and counted; `--all` shows them,
and so does `--status` naming a final status. `--status none` selects issues
without a status. Filters combine with AND, and the issues they leave out are
counted. Only issues are listed: draft issues and pull requests on the board
are counted. At most `--limit` issues are printed (default 50), with the
total.

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
A blocker is resolved when it is closed or on the board in a
[`statuses.resolved`](configuration.md#statuses) status, archived or not: the rule of `show`
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

## new

```sh
gh dojo new <title> [--type T] [--area A]... [--label L]... [--milestone M] [--blocked-by N]... [--parent N] [--repo owner/repo] [--body TEXT | --body-file FILE]
```

Creates an issue, labels it, adds it to the board and sets the initial status
([`statuses.initial`](configuration.md#statuses)) in one command:

- `--type` takes one of [`labels.types`](configuration.md#labels) (or any repo label outside
  the areas when that list is empty), `--area cli` adds the label
  `<labels.area_prefix>cli`, `--label` adds any existing label;
- `--milestone v1.2` puts the issue in the open milestone titled `v1.2`
  (ignoring case), or else in the only one whose title starts with it:
  `--milestone v0.3` for `v0.3 — extensibility`. Several such are exit
  code 1, listing them. A verb with
  [`milestone = "{milestone}"`](configuration.md#effects) moves an existing
  issue to another milestone, matched the same way;
- `--blocked-by 17` marks it blocked by #17 (repeatable, or comma-separated;
  `owner/repo#17` or a URL for another repository);
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
  [`board.repos`](configuration.md#board) than the first: its labels, milestones and repeat
  check are that repository's, the issue is named `acme/api#15`, and a bare
  `--blocked-by` or `--parent` number is still in the default repository. A repository
  not in `board.repos` is refused (exit code 1, listing them);
- `--body-file -` reads the body from stdin. A title starting with `-` goes
  after `--`: `gh dojo new --type bug -- "-v is ignored"`.

An unknown label or milestone fails before anything is created, listing the
allowed values (exit code 1); so does a `--blocked-by` or `--parent` issue
that does not exist, or a pull request (exit code 3).

```console
$ gh dojo new "Fix login redirect" --type bug --area cli
#15 "Fix login redirect" created, labels bug, area:cli, no status → Backlog
```

A repeat is safe: the body ends with a hidden marker of the title, and a run
that finds it on one of the repository's 20 newest open issues written by
you finishes that issue (labels, milestone, board, status, blocked-by links,
parent) instead of creating another; a different milestone or parent it
already has (or a parent the token cannot read) is kept, with a warning
that names the command to change it: a verb with `milestone = "{milestone}"`
when the config has one.
If a run fails after creating the issue, it exits with code 5: run the
same command again.

## comment

```sh
gh dojo comment <N> [text] [--body-file FILE]
```

Comments on an issue without changing its status. The text comes from the
second argument or from `--body-file` (`-` reads stdin), exactly one of them.
Text starting with `-` goes after `--`: `gh dojo comment 42 -- "-1, still
broken"`. When the issue's last comment is yours with the same text, it is
not posted again.

## block, unblock

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
agent's context: when to use each verb, the arguments it needs or takes, what
it checks and does along the way (comment, sprint, labels, assignee, closing
or reopening, blocked-by links, the blockers check, closed issues, the
user's go-ahead), then the lifecycle (from → to), the initial, final and
resolved statuses, and the exit codes; with an initial status it points at
[`next`](#next) to find an issue. It reads only the config, never the
network; a missing or broken config is an error (exit code 1).
`gh dojo <verb> --help` has the details of one verb.

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
| `config` | `.github/dojo.toml` loads |
| `token` | gh is logged in, and the token has the `project` scope (from `GH_TOKEN`: "set GH_TOKEN to …") |
| `board` | The board exists, is open, and has every field and status the config names, each mismatch on its own line |
| `repo` | Each repository of [`board.repos`](configuration.md#board), or the current one, is visible with issues enabled; a warning when `board.repos` spells one otherwise than GitHub does |
| `labels` | With [`labels.types`](configuration.md#labels) or `labels.area_prefix` set, in each repository of the `repo` check: every type label exists (missing is a problem, since `new --type` fails on it, fixed by `gh label create`; spelled in another case is `warn`, since `new` matches labels regardless of case, fixed by `gh label edit`), and how many labels start with the area prefix. None is `warn` when `.github/dojo.toml` sets the prefix, since `new --area` finds nothing (create one, or set the prefix to `""`); with the prefix from a preset, which many teams never use, it is a note on the `ok` line. Past 100 labels the area count is a lower bound ("at least"), and none found is "could not confirm" rather than a warning |
| `sprint` | With [`fields.sprint`](configuration.md#fields): an iteration covers today, a date in [`board.timezone`](configuration.md#board) |
| `verbs` | When verbs have labels, assignees or milestones: the labels exist in the default repository, spelled exactly as there (those of `from_labels` and `refuse_labels` in any case, as the checks match them), the users can be assigned, and each milestone a verb names in the config matches one open milestone there, as the verb matches it (none or several is a problem) |
| `agent` | With [`[agent]`](configuration.md#agent) set (and `targets` not empty), or a file with a gh-dojo marker: the files [`agent install`](#agent-install) writes are up to date (`agent install --check`, without the diff). Out of date or missing is `warn`, since the text also depends on the gh-dojo version; a hand-written skill under `[agent]` is a problem |

It sends one request for the token and the repositories, one more for the
board when its schema is not cached, one for the labels when
`labels.types` or `labels.area_prefix` is set (plus a repository's labels
page by page when it has over 100 and a type is not found), and one for
the verbs' labels, assignees and milestones when a verb has them (plus the
open milestones page by page past 100). The exit code is 0
when no check is a problem (warnings included), 4 when one failed on auth
or the network, else 1.

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
agent should do about it: use only `gh dojo` for issues; the lifecycle, the
longest way from the initial status to a final one along the verbs (a status
a verb can jump over in parentheses); the initial, final and resolved
statuses; each verb with its `when`, the statuses it moves between, its
arguments, checks and effects, and the rules behind them (ask the user before
`--yes`, don't work around a refused blockers or labels check, what `--by` links,
several numbers at once); `show`, `next` and `list`; `new`, `comment`,
`block` and `unblock`; safe repeats; the session link; exit codes; JSON; and
a table of the commands of the gh-dojo that wrote them, so a command added in
a later version shows up there by itself. The skill's `description` names the
repository, the board and the verbs, and [`[agent] description`](configuration.md#agent)
adds to it. The markdown file [`[agent] notes`](configuration.md#agent) names is appended as
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

## Process verbs

```sh
gh dojo <verb> <N> [--comment TEXT] [--by N|TEXT] [--assignee LOGIN] [--milestone TITLE] [--yes] [--dry-run]
gh dojo <verb> <N>... [flags]     # a verb with batch = true, such as done
```

Verbs come from [`[verbs]`](configuration.md#verbs) in the config, so each project has its
own; the [kanban](configuration.md#kanban-preset) and [sprints](configuration.md#sprints-preset) presets
define `start`, `wait`, `ready` and `done`. `gh dojo --help` lists them with
their `when` line and the lifecycle; `gh dojo <verb> --help` shows the
allowed starting statuses, the target status, the required arguments, the
other effects and the comment template.

A verb takes one issue number (several with [`batch = true`](output.md#several-issues-at-once))
and, in one command:

1. checks, in this order, that the required arguments are given, that the
   issue is open (a verb that moves it to a final status, closes it or
   reopens it also runs on a closed one; one that closes and has no `to`,
   only on an issue closed with its reason), that its status is one of the
   verb's `from` statuses (no status, or not on the board, counts as
   [`statuses.initial`](configuration.md#statuses)), the issue's labels (`from_labels`,
   `refuse_labels`), its blockers (`blockers`) and sub-issues
   (`sub_issues`), and the user's go-ahead (`confirm`);
2. adds the issue to the board if it is not there;
3. applies its [effects](configuration.md#effects): reopens it, sets the status and the
   sprint, sets other board fields, sets the milestone, adds and removes labels, assigns and
   unassigns users, marks it blocked by the `--by` issues, posts the rendered
   [comment template](configuration.md#comment-templates) and closes it.

Each effect already in place is skipped: a status already set, labels
already there or gone, a comment that is already the latest gh-dojo comment,
an issue already closed (whatever its reason), and so on. Everything is
written in one GraphQL mutation (two when the issue is not on the board yet:
the status needs the new board item), so a verb on an issue on the board
costs one read and one write.

`--comment` and `--by` fill `{comment}` and `{by}` in the template; a verb
whose template does not use one of them rejects that flag. `--by` takes an
issue number (rendered `#12`) or free text; with
[`blockers = "link"`](configuration.md#checks) issues are also marked as blocking this one.
`--assignee` (a login, or `@me`) exists only on a verb with
[`assign = "{assignee}"`](configuration.md#effects), and is required there: the user to
assign, also `{assignee}` in the template. `--milestone` (an open
milestone's title, or a start only that title has) exists only on a verb
with [`milestone = "{milestone}"`](configuration.md#effects), and is
required there: the milestone to set, also `{milestone}` in the template.
`--yes` exists only on a verb
with [`confirm = "user"`](configuration.md#checks).
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
[Several issues at once](output.md#several-issues-at-once) for the summary and the
exit code. [`release`](#release) runs it on the issues of a release.

## release

```sh
gh dojo release <ref> --verb <verb> [--since <ref>] [--exclude N] [--dry-run] [--yes] [--comment TEXT]
```

Once a release is tagged, runs a verb with [`batch = true`](output.md#several-issues-at-once)
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
of [`[release] keywords`](configuration.md#release-keywords), by default GitHub's closing
keywords: `Fixes #12`, `closes: acme/api#3`, `Resolves <issue URL>`, any
case; a list after a keyword counts whole up to the end of its line (`Fixes
#12, #13 and #14`), a mention without one (`see #12`) does not. `Refs #12` says a commit is about
an issue, which may be done only in part, so `refs` is not a default; a
project that commits without pull requests adds it, as this one does.
Code in a message does not count, as Markdown does not link it: a code span
(`` `Refs #12` ``) or a fenced block (```` ``` ````) is an example of the
syntax, not a link, so put such examples in backticks.

**A message is text**, a guess at a link, where a pull request closing an
issue is GitHub's own. So a number only commit messages name that is no
issue is left out with a warning on stderr and listed in the JSON's
`skipped`, not failed: a pull request (`pull_request`), a missing issue
(`not_found`), or an issue of a repository outside `board.repos` that is
not found (`repo_not_found`), such as an example `acme/api#3` in a message.
GitHub answers the same for a private repository the token cannot see, so
the warning says so: `acme/api#3: repository acme/api not found, or the
token cannot see it: left out (commit de25f45)`; if the issue is real, give
the token access and run the verb on it. A repository of `board.repos` (or
the default one) not found is the token's lack of access almost surely, so
its issues fail the batch (exit code 3) as any issue does. So does any
other error: a token GitHub denies outright (SAML single sign-on, OAuth app
restrictions) fails the batch as a network error, whose fix is the token's.
An issue a merged pull request closes, even if a message names it too,
fails the batch as any issue does. `--exclude N` (`#N`, `owner/repo#N` or a
URL; repeatable, or comma-separated) leaves out any issue found, by a pull
request or a message, before it is read: the line `excluded: acme/api#3
(commit de25f45)` and the JSON's `skipped` (`excluded`) name it, and an
`--exclude` the release does not include is a warning.

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
without `batch = true` is exit code 1, and so is one that needs
`--assignee` or `--milestone` (`assign = "{assignee}"`,
`milestone = "{milestone}"`), which release does not take: run it on the
issues themselves, a `<ref>` or `--since` GitHub does not
know exit code 3. Requests: one for `<ref>` and the start (one per 100
tags), one per 100 commits with their pull requests, then the batch's.
