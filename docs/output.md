# Output and exit codes

A successful write prints one line per issue: what was, what is now and what
was done along the way (`#42 Backlog → In progress, comment left`), so there
is no need to re-read the issue. Warnings go to stderr. An error is a few
lines on stderr: what is wrong, the allowed values and a command to fix it.

```console
$ gh dojo ready 42
error: ready cannot move #42 from Backlog; it starts only from In progress or Waiting
this is a process rule: ask the user how to proceed
```

## Exit codes

| Code | Meaning                     | What to do                                                   |
|------|-----------------------------|--------------------------------------------------------------|
| 0    | Everything applied          | Go on                                                        |
| 1    | Bad arguments or config     | Fix them as the error says                                   |
| 2    | A process check refused     | Don't work around it; ask the user                           |
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

A rate limit says how long to wait before the re-run, in its `fix:` and in
a partial write's message: GitHub's limit on creating issues and comments,
hit after many of them in a short time, waits as long as GitHub's
`Retry-After` says, else a few minutes:

```console
$ gh dojo ready 66 --comment cc5ff46
error: #66 In progress → Ready; comment not posted: GitHub's limit on creating issues and comments was hit; wait a few minutes, then re-run the same command
fix: wait a few minutes, then re-run the same command
```

## Several issues at once

Every verb takes several issue numbers, and [`release`](commands.md#release)
runs a verb on a release's issues the same way. An issue given twice counts
once (`12` and `acme/shop#12` too, in the default repository acme/shop); a
single issue prints no summary. The cards are read together, in a few
requests, from any repositories
(`gh dojo done 12 acme/api#3`); then each issue is checked, planned and
written on its own, with its own mutation, so a lost answer or a
refusal touches that issue alone. An issue that fails is skipped and the
others go on. An error that would fail every write alike (the token rejected
or missing a scope, a rate limit, the run interrupted) stops the writes: the
issues left are reported as not attempted. `--yes` counts for every issue;
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

## JSON output

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
(`list`, `next`, `show`, `release` and a verb's issues also carry them in
`warnings`).

**Stability.** Every object has `"schema_version": 1` first and `"error"`
second. Field names are snake_case. Every key is always there: lists are
`[]` when empty, never `null`; an absent value (no status, no milestone, no
due date, no fix) is `null`, never `""`. Times are RFC 3339 in UTC, dates
`YYYY-MM-DD`. GitHub node IDs never appear. A field that holds a reference
to an issue or pull request always has it in full, `owner/name#12`, and
every issue has its `repo`: the default repository as the config spells it,
others as GitHub does (also in the text). The short `#12` of the text, also
in `message` and `warnings`, is only for reading. New fields may be added;
renaming or removing one, or changing its meaning, is a breaking change and
comes only with a new `schema_version` in a major release.

An error, top-level or inside a result, is `{exit_code, message, allowed,
fix}`: the fields of the error block, `fix` `null` when there is none.
`message` and `allowed` are whole; the block on stderr may cut them. Its
`allowed:` line lists the keys of a config table in full, since a key must
be typed exactly; other lists, such as the board's statuses or a
repository's labels, stop after 20 values with `and N more`.

The schemas, by command, after `schema_version` and `error`:

- `show`: `brief` (`--brief`: every card's `history` and `comments` are
  `[]`, `body` `null`), `issues`: cards; `failed`: `[{number, repo,
  error}]` for issues not shown; `warnings`.
- card: `number`, `repo`, `url`, `title`, `state` (`open`/`closed`),
  `state_reason` (`completed`, `not_planned`, `duplicate`, `reopened` or
  `null`), `author`, `created_at`, `closed_at`, `incomplete` (GitHub
  withheld part of it);
  - `board`: `{on_board, unknown_reason, status, status_unknown, archived,
    fields: [{name, value}], fields_truncated}`;
  - `sprint`: `null` without `fields.sprint`, else `{field, iteration:
    {title, start_date, end_date, duration_days} or null, unknown_reason}`;
  - `milestone`: `{title, due_on}` or `null`;
  - `labels`, `assignees` with `labels_total`, `assignees_total`;
  - `blocked_by`, `blocking`: `[{ref, repo, number, title, state, on_board,
    status, archived, status_unknown_reason, resolved}]` with
    `blocked_by_total`, `blocking_total`, `unresolved_blockers`;
  - `parent`: a linked issue as in `blocked_by`, or `null` (with
    `parent_hidden` when GitHub withheld it from the token);
  - `sub_issues`: linked issues as in `blocked_by` with `sub_issues_total`
    and `sub_issues_closed` (of all of them, closed for any reason) and
    `unresolved_sub_issues` (of those read);
  - `history`: `[{at, from, to, actor, automated}]` with
    `history_truncated`, `history_untracked`;
  - `body`: `{text, cut, length}`;
  - `comments`: `[{author, created_at, text, cut, length}]` with
    `comments_total`.
- `list`: `sprint_filter` (`null` without `--sprint`, `"none"` for issues
  without a sprint, `"iteration"` for those in `sprint`), `sprint` (the
  `--sprint` iteration or `null`), `issues`: `[{repo, number, title, state,
  status, labels, labels_total, assignees, assignees_total}]` in the
  printed order, cut to `--limit`; `matched` (before `--limit`),
  `unchecked`, `hidden`, `filtered`, `skipped`: `{draft_issues,
  pull_requests, unseen}`, `incomplete`, `warnings`.
- `next`: as `list`, each issue with `unresolved_blockers`: `[{ref, repo,
  number, on_board, status, archived, status_unknown_reason}]` (empty
  unless `--all`) and `unread_blockers` (blockers the token cannot read,
  unresolved); `matched`: issues to show before `--limit` (with `--all`,
  the blocked ones too); `hidden`: blocked issues left out (0 with
  `--all`); `incomplete`: GitHub withheld part of an issue to show or hide,
  or of its blockers.
- verbs: `verb`, `default_repo` (where a bare number is), `dry_run`,
  `issues`: `[{number, repo, title, outcome, planned, applied, in_place,
  needs_yes, warnings, error, card}]` in argument order (`title` `null`
  when the issue was not read; `card`: with `--show`, the issue as read
  before the verb, a card as in `show`, else `null`), `summary`: `{total,
  done, written, failed, not_attempted}`.
- `release`: as verbs, with
  - `release`: `{ref, commit, since, since_picked, since_commit, compared,
    commits}` (`since_picked`: the previous version tag, not `--since`;
    `compared`: GitHub compared the two, else the history was read down to
    a `--since` commit; commits are full SHAs),
  - each issue with `found_by`: `[{by, pull_request, commit}]` (`by`
    `pull_request`, `keyword` or `suffix`: what links the commit to it; a
    commit that names it both ways counts as `keyword`; `pull_request`
    `owner/name#12` that closes it, with its first commit in the release,
    or `null`: the commit's message names it),
  - `skipped`: `[{number, repo, ref, reason, on_board, status, found_by}]`
    (`reason` `reverted`, found only through commits reverted later in
    the range; `excluded` by `--exclude`; `pull_request`, `not_found` or
    `repo_not_found` for a number only commit messages name; `closed`, a
    closed issue in a final status, done with; or `not_ready`, its status
    one the verb's `from` does not allow; for `closed` and `not_ready`,
    `on_board` and `status` say where the issue stands, `status` `null` for
    no status, else both are `null`),
  - `not_in_release`: `[{number, repo, ref, title, state, status}]`, the
    board's issues in Ready the release does not include, for a verb that
    finishes issues ([Ready, not in the
    release](commands.md#ready-not-in-the-release); `state` `open` or
    `closed`; `[]` when none, not read or the read failed, which
    `warnings` then says),
  - and `warnings`, among them each issue only a suffix names while
    `[release] suffix` is off, with what to do
    ([Issues in the release](commands.md#issues-in-the-release));

  `issues` and `summary` count only the issues the verb ran on, the default
  repository's first.
- `new`: `number` (`null` in a dry run that would create the issue),
  `repo`, `title`, `dry_run` (nothing written: the other fields say what
  would be), `created` (`false`: resumed), `labels`, `status`,
  `status_set`, `unarchived`, `milestone` (`{title, due_on}` or `null`),
  `milestone_set`, `blocked_by`, `already_blocked_by`, `parent`
  (`owner/name#12` or `null`; read only with `--parent`), `parent_set`,
  `assigned` (the `--assignee` logins assigned now, as GitHub spells
  them), `already_assigned` (those a resumed issue had).
- `comment`: `number`, `repo`, `dry_run`, `posted` (`false`: already the
  last comment; in a dry run, whether it would be posted).
- `block`, `unblock`: `number`, `repo`, `action`, `dry_run` (`changed`: the
  links it would change), `changed`, `unchanged`, `missed`.
- `describe`: `config`, `preset`, `board`, `repos`, `verbs`: `[{name, when,
  from, to, requires, requires_one_of, optional, batch, checks,
  effects}]` (`batch`, the verb takes several numbers, is always `true`
  now that every verb does), `statuses`: `{initial, final, resolved}`,
  `exit_codes`: `[{code, meaning, next}]`.
- `doctor`: `ok` (no check is a problem), `checks`: `[{name, result,
  detail, allowed, fix}]`, `result` one of `ok`, `problem`, `warn`,
  `skip`.
- `agent install`: `check`, `files`: `[{agent, path, state, diff, fix}]`;
  `state` is `created`, `updated` or `up_to_date`, with `--check`
  `up_to_date`, `missing`, `out_of_date` or `hand_written`; `diff`
  (`--check`, out of date) the changed lines as printed, else `[]`; `fix`
  (`--check`, not up to date) the command or step that brings it up to
  date, else `null`.
- `init`: without `--project` (exit code 1, the choice is yours): `owner`,
  `owner_type`, `projects`: `[{number, title, items, closed}]`,
  `suggested_project`, `command`; with it: `path`, `written`, `board`,
  `board_title`, `preset`, `repos`, `matches_board`, `problems`:
  `[{problem, fix}]`, `content`.
- `--version`, or `gh dojo --json`: `version`.

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
| `set_milestone` | `from`, `to` (milestone titles, `null` for none) |
| `add_labels`, `remove_labels` | `labels` |
| `assign` | `login` |
| `unassign` | `logins` (in `in_place`: the users not assigned; empty for `unassign = "all"` with nobody to remove), `kept` (`true` in `in_place` for a user it names and also assigns, who stays) |
| `add_blocked_by` | `issue` (`owner/name#12`) |
| `add_comment` | `body` (`null` in `in_place`: the comment was posted before) |
| `close` | `reason` (`completed`, `not_planned`, `duplicate`), `of` (the original of a duplicate, `owner/name#17`; `null` otherwise) |
