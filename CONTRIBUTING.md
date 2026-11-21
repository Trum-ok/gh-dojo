# Contributing to gh-dojo

## Build and test

Requires Go (the version in `go.mod`) and `gh`.

```sh
make build    # ./gh-dojo, stamped with git describe
make check    # what CI checks, offline (see below)
make test     # go test ./...
make vet      # go vet, also for Windows, macOS and -tags e2e
make lint     # staticcheck, also as Windows and macOS see the code
make fmt      # files gofmt -l lists (gofmt -w . fixes them)
make tidy     # go mod tidy -diff, tools/ too
make vuln     # govulncheck: known vulnerabilities the code can reach
make e2e      # end-to-end tests against the sandbox boards (see below)
make dist     # release binaries for all platforms into dist/
make agent    # regenerate the agent skill from .github/dojo.toml
make install  # build and install as the gh dojo extension (see below)
make demo     # render the README animations (needs Node and ffmpeg)
```

The animations in the README are [Remotion](https://www.remotion.dev/)
videos in [`docs/media/`](docs/media).

CI (`.github/workflows/ci.yml`, on pushes to `main` and pull requests)
checks once, on Linux, `gofmt -l .`, `go mod tidy -diff` (`tools/` too),
`go vet` (also for Windows, macOS and `-tags e2e`), `make lint`, the release
build and its `agent install --check` (see below), its `checks` job running
Format, Tidy and Vet through `make fmt`, `make tidy` and `make vet` (like
Staticcheck through `make lint`), so they match the local targets by
construction; then runs `go test ./...`
and `go build ./...` on Ubuntu, macOS and Windows, on Ubuntu with `-race`
through `script/unused-golden.sh`, which also fails on golden files no test
reads.
`make check` runs all of it on your machine, the release build checked with
your own system's binary and the tests on that system only; run it before
sending a change. It leaves out `make vuln`, which needs the network and
can fail on a newly published vulnerability with the code unchanged, and
the end-to-end tests.

The linters are pinned in `tools/go.mod`, a module of their own, so they add
nothing to gh-dojo's dependencies; dependabot updates them with the rest.
`make lint` and `make vuln` build them into `.tools/`. A staticcheck finding
is fixed, not silenced; if one is wrong, a `//lint:ignore <check> <reason>`
on that line says why.

`.github/workflows/vuln.yml` runs `make vuln` on pushes, pull requests and
daily, with Go's latest patch release, and fails only on a vulnerability the
code can reach.

Code and tests must work on all three: build paths with `path/filepath`,
show them to users with `filepath.ToSlash`, and keep OS error texts out of
golden files. `.gitattributes` checks every file out with LF line endings,
Windows included.

## Golden files

Command output, errors and help are compared with `testdata/**/*.golden`
files. After an intended output change, rewrite them and review the diff
before committing:

```sh
make golden   # UPDATE_GOLDEN=1 go test ./...
```

Rewriting is refused when `CI` is set, so CI can never pass by accepting its
own output. A golden file no test reads any more fails
`script/unused-golden.sh` (in `make check` and CI): delete it with its test. `internal/config`'s `TestReadmeExamples` loads every `toml` block
in `README.md` and `docs/*.md`, and `TestExampleConfigs` every `examples/*.toml`, so README
examples and example configs must stay valid configs. `cmd/gh-dojo`'s
`TestExampleReview` runs `describe`, a dry run of every verb and
`agent install` on `examples/review.toml`, against the hand-crafted board
and cards in `review_board.json` and `review_example.json`: a new verb
there needs a dry run and a card. Another example without a preset needs
such a test too, and a line in `examplesWithoutPreset`, or
`TestExampleConfigs` fails.

## GraphQL fixtures

Tests never touch the network: GraphQL responses are replayed from
`testdata/graphql/*.json` through `internal/graph/graphtest`. A request
matches an interaction by operation name, query and variables; each
interaction answers once unless it has `"repeat": true`, an unexpected
request fails the test, and so does an interaction never requested.

To re-record read-only fixtures from the real API with your gh token:

```sh
GH_DOJO_RECORD=1 go test ./internal/board -run TestUserBoard
```

Mutations are refused while recording unless `GH_DOJO_RECORD_MUTATIONS=1`
is set too; only use that against a sandbox board. Fixtures marked
`"handcrafted": true` (errors, outages) cannot be recorded, and their tests
are skipped in record mode. Tests point the schema cache at a temporary
directory through `GH_DOJO_CACHE_DIR`.

## End-to-end tests

`e2e/` runs the built gh-dojo against real boards, behind the `e2e` build
tag, so `go test ./...` never touches the network. For each preset it goes
through `init`, `doctor`, `new`, `next`, the verbs, `comment`, `block` and
`unblock`, `show` and `list` up to closing the issues, then deletes the
issues it created. `.github/workflows/e2e.yml` runs it daily and by hand
(Actions → e2e → Run workflow), not on pull requests, in the sandbox
repository `Trum-ok/gh-dojo-sandbox` with boards `Trum-ok/6` (kanban) and
`Trum-ok/7` (sprints), with the `E2E_TOKEN` secret: a classic token with
the `project` and `public_repo` scopes of an admin of the sandbox. Locally,
with gh's token, `make e2e` runs the same (the `GH_DOJO_E2E_*` variables
override the sandbox and boards):

```sh
make e2e   # go test -tags e2e -count=1 -v -timeout 15m ./e2e
```

The sprints board's Sprint field has iterations up to November 2027: add
more before then, or the sprint checks fail. `e2e/doc.go` lists the
variables and what the boards need.

## Debugging GitHub errors

Errors never print GitHub's raw response, so a GraphQL error of a type
gh-dojo does not map shows only as "GitHub API rejected the request". To see what GitHub actually answered, run the command again with
go-gh's debug logging, which gh-dojo's client keeps on:

```sh
GH_DEBUG=api gh dojo <command> 2> debug.log
```

`GH_DEBUG=api` logs every request and response, headers and bodies, to
stderr; the `errors` array of the failing response holds the type and
message. The `Authorization` header is redacted, but bodies are logged as
is, so review the log before sharing it. Any other `GH_DEBUG` value except
`0`, `false` and `no` logs only the request lines and timings.

## Install from source

To run the latest `main`, install from source:

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

## Layout

| Package                    | Responsible for                                                                       |
|----------------------------|---------------------------------------------------------------------------------------|
| `cmd/gh-dojo`              | Entry point; cobra commands, verbs built from the config                              |
| `internal/config`          | Loading and validating `.github/dojo.toml`; embedded presets                          |
| `internal/board`           | Board schema, its cache, config checks; items for `list`, `next`; projects for `init` |
| `internal/workflow`        | Verb checks and the effect plan; `new`'s checks and plan                              |
| `internal/effects`         | Effects → one GraphQL mutation; `new`'s mutations                                     |
| `internal/batch`           | Running a verb on its issues: one by one, retries, the summary                        |
| `internal/newissue`        | Running `new`: its plan applied, re-reads, resuming, the partial result               |
| `internal/issue`           | Reading issues (cards) and batches of them; what `new` reads                          |
| `internal/graph`           | GraphQL client over go-gh: retries, pagination, errors, partial answers               |
| `internal/graph/graphtest` | Replaying recorded GraphQL responses in tests ([above](#graphql-fixtures))            |
| `internal/render`          | Errors, exit codes, confirmations, folding with counts, text helpers                  |
| `internal/agent`           | `agent install`'s files: targets, markers, writing, diffs                             |
| `internal/doctor`          | `doctor`'s checks of the setup and its report                                         |
| `internal/release`         | `release`: a release's commits, their pull requests and keywords                      |
| `internal/testutil`        | Test helpers shared across packages: golden files                                     |
| `bench`                    | The benchmark against plain gh ([docs/bench.md](docs/bench.md))                       |
| `e2e`                      | Tests against real boards ([above](#end-to-end-tests))                                |

The plan is separate from its execution: the workflow engine returns the
operations without touching the network, which is what `--dry-run`, the
tests and finishing a partially applied command rely on.

A command prints its result through `emit` (`cmd/gh-dojo/output.go`;
`emitWrite` for a command that writes): it builds one typed result and
passes the function that prints it as text, so the text and `--json` come
from the same data. A command returns its error as usual: `run` prints it as
JSON too. The JSON keeps the rules and schemas of
[docs/output.md](docs/output.md#json-output) ("Stability"); each command has
JSON golden files under `cmd/gh-dojo/testdata/json/`.

The Go types are named by role, so output is never mistaken for what is
read from GitHub. A command's result, the top level of its `--json`, is
`xxxResult` (`showResult`, `verbResult`); a nested part of the output is
`xxxJSON` (`cardJSON`, `milestoneJSON`), and a function that builds one is
`toXxxJSON`, never the type's own name. A type that decodes a GraphQL
answer is `xxxResponse` for the whole `data` (`itemsResponse`) and
`xxxNode` for an object in it (`issueNode`, `itemNode`). The JSON field
names come from the `json:"…"` tags, so renaming a type never changes the
output.

## Design principles

Every command is checked against these rules.

1. **Process verbs, not fields.** The agent says what happened (`start`,
   `wait`, `ready`), not which field to set. It never sees node IDs, field
   names or option IDs.
2. **Rules are data, the mechanism is code.** A project's process lives in
   its config and changes without a new release. What the config cannot
   express is not a reason to make the format more complex.
3. **One line of confirmation.** A write prints what was, what is now and
   what was done along the way: `#42 Backlog → In progress, comment left`.
   The agent need not re-read the issue to check.
4. **An error names the next step.** What is wrong, the allowed values and
   the command to run. No stack traces, no raw GraphQL responses.
5. **Repeats are safe.** Agents repeat commands after errors: a repeat never
   creates a second issue or comment, and a partially applied command says
   what is already done and finishes the rest.
6. **Fewest requests.** See below.
7. **Nothing hidden silently.** A cut list says how much it cut, a filter
   counts what it hid.
8. **Self-describing.** `--help` and `describe` show this project's verbs and
   when to use each: the agent needs no outside documentation.

The interface, help text and documentation are in English. Texts that go
into issues (verb `when` lines and comment templates) come from the config.

### Requests and retries

- Reading a card is one GraphQL request; a verb on an issue already on the
  board is two with a warm cache (read, then one mutation with all its
  effects). An issue not on the board takes three, `new` four (read,
  `createIssue`, add to the board, the rest): GraphQL cannot feed one
  mutation field's result into another in the same document, and setting a
  status needs the item ID only `addProjectV2ItemById` returns.
  `createIssue` with `projectV2Ids` is not used: GitHub adds the item
  asynchronously and does not return its ID
  ([#17](https://github.com/Trum-ok/gh-dojo/issues/17)).
- The board schema is cached for an hour (see
  [Checked against the board](docs/configuration.md#checked-against-the-board)).
- `list` and `next` read only the items they may show, with GitHub's filter
  (`ProjectV2.items(query:)`, `board.Filter`), and count the rest with
  `totalCount` of other filters in the same request (`cmd/gh-dojo/items_filter.go`):
  one request after the schema, however much is done, where reading every
  item took one per 100 items, Done included
  ([#94](https://github.com/Trum-ok/gh-dojo/issues/94)). `next` reads each
  blocker's place on the board with it (`projectItems`), so no blocker
  needs to be among the items read. `list --all`, `--status` naming a final
  status, a name or value the filter has no checked spelling for, and a
  `--label` that no issue has (for `checkLabels`' suggestion) read every
  item. `release` reads the issues in Ready the same way
  (`board.ReadInStatus`): every read of the board's items is
  `board.ReadItemsWith`, every filter a `board.Filter`. GitHub's filter
  lags behind writes for a moment, so what is read is checked again by the
  item's own fields.
- Independent reads go in one request when they fit (aliases), else at
  once ([#96](https://github.com/Trum-ok/gh-dojo/issues/96)): the
  `issue.MaxBatch` chunks of a read of many issues, up to
  `issue.ChunksAtOnce` in flight, taken in order as if read one after
  another; doctor's labels and verbs queries, after the Doctor query they
  need. Writes stay one after another: GitHub asks for requests one at a
  time to stay clear of its secondary rate limits, which creating content
  hits first, and a write that fails for every issue stops the rest. A
  goroutine recovers its own panics (see `run` in `cmd/gh-dojo/main.go`).
- Queries are retried on 5xx and secondary rate limits with exponential
  backoff, at most `MaxAttempts` sends (`internal/graph/graph.go`).
  Mutations are sent once: re-running the command re-reads the state and
  finishes what is left.
- GitHub's limit on creating content (issues, comments; a secondary rate
  limit) is exit code 4 whose fix says to wait (`Retry-After` when GitHub
  gives one, else "a few minutes") before the re-run, and a partial run
  (exit 5) keeps that wait. GitHub gives it no type of its own: in a 200
  GraphQL answer it is `UNPROCESSABLE` "was submitted too quickly", over
  HTTP a 403 "…blocked from content creation", and `createIssue` may answer
  with no issue and no error at all
  ([#67](https://github.com/Trum-ok/gh-dojo/issues/67)). So
  `render.IsContentLimit` matches GitHub's wording; add a phrase there when
  GitHub words it anew.
- Auth comes only from gh's token (or `GH_TOKEN`); gh-dojo stores no secrets,
  never prints the token and sends no telemetry.

### Decisions

- **No MCP server.** The target agents have a shell, and a CLI costs fewer
  tokens and works in any of them. A local MCP server would only serve agents
  without a shell; revisit if one asks.
- **Every command that writes to GitHub takes `--dry-run`** (verbs,
  `release`, `new`, `comment`, `block`, `unblock`; `init` for its file):
  agents carry the flag from one command to the next
  ([#60](https://github.com/Trum-ok/gh-dojo/issues/60)). It reads and
  checks what the run does and prints the plan; its JSON has `dry_run`
  (`init`'s has `written`). A new command that writes gets it too.
- **An unknown flag is answered from the commands, not a synonym table**
  ([#63](https://github.com/Trum-ok/gh-dojo/issues/63)). In the bench
  transcripts and our sessions every unknown flag was another gh-dojo
  command's (`--show` on `show`, `--by` on `new`, `--dry-run`), none a typo.
  So the error lists the command's flags (`allowed:`), names the commands
  that take the flag, and suggests a flag of this command only when one is
  clearly closest: edit distance 2 at most and at most half the name, else
  the same last word (`--blocked-by` → `--by`); two equally close, none. A
  flag agents keep carrying with the same meaning gets a hidden synonym
  instead (`new --by`, [#61](https://github.com/Trum-ok/gh-dojo/issues/61)),
  which costs no second call.
- **`release` goes through pull requests**, so it works with any merge
  strategy (squash, merge, rebase); commit keywords are the fallback.
- **The generated skill is loaded into every session, so it repeats
  nothing:** it does not point to `describe` (the same text) or list the
  commands; of the exit codes it covers only what to do on 2, 4 and 5 (an
  error's `fix:` line covers the rest); other repositories get detail only
  with several `board.repos`, and `release` one line pointing to its
  `--help`. It does name `new`'s flags in one line: a flag beyond `--type`
  and `--body` would otherwise cost a read of `new --help`, the longest help.
  Keep it that way when changing `internal/agent`.
- **Help says a thing once, since agents read it as tokens.** The lifecycle
  and the exit codes are in `describe` only, and the root help points there
  (it prints the exit codes itself only without a config, when `describe`
  fails); a verb's `Batch:` is two lines, the batch's exit codes being in
  `describe`; a subcommand names the global flags in one line, the root help
  describes them. `new --help` and `release --help` state a rule once, not
  per flag: the Flags section already says what each flag takes.
- **Label groups are a prefix, not a list**
  ([#75](https://github.com/Trum-ok/gh-dojo/issues/75)).
  `labels.type_prefix` and `labels.area_prefix` work alike: the values are
  the repository's labels with the prefix, so the config cannot drift from
  them, and a new type is `gh label create`. The price: `--type` is no
  longer checked offline and the skill names the prefix, not the types;
  `new`'s error lists them. An earlier `labels.types` list was dropped for
  this; do not bring it back, nor add a key per new label scheme.
- **GitHub filters, gh-dojo counts the same**
  ([#94](https://github.com/Trum-ok/gh-dojo/issues/94)). `list` and `next`
  read only what they may show, but their counts (hidden, filtered out,
  skipped) stay those of the whole board: each is a `totalCount` alias in
  the same request, about 0.03 s each, less than reading the archive. The
  filter's spelling was checked against GitHub, and `board.Filter` says
  what was found (drafts match `is:issue`, field names are hyphenated,
  values quoted, no OR of a value with no value); a name or value with
  another spelling reads every item rather than guessing. Tests of how
  `list` and `next` sort items out read every item (`everyItem`);
  `TestFilteredSameAsEvery` records the board both ways at once and
  compares.
- **Every verb takes several issue numbers; how many is not config**
  ([#89](https://github.com/Trum-ok/gh-dojo/issues/89)). `internal/batch`
  checks and writes each issue on its own, so no verb is riskier on
  several issues than on one; how many numbers a command takes is CLI
  mechanics, not a process rule. `release --verb` runs any verb but one
  that needs `--assignee`, `--milestone` or `--of`: the user names the
  verb, so a separate permission would guard nothing. The verb key
  `batch`, which once allowed both, is deprecated rather than removed,
  since configs of released versions must keep loading: a deprecated key
  loads, does nothing, is left out of the allowed keys of errors, and
  `gh dojo doctor` warns about it with the fix to remove it
  (`Config.Deprecated`).
- **Statuses match regardless of case.** GitHub creates a board with
  `In Progress`, the presets say `In progress`: matching exactly made the
  user override four preset keys or rename the option for a capital letter.
  A verb only picks an option by its ID and never creates one, so ignoring
  case cannot make a second status, as it could a second label (`+label`
  keeps the exact spelling). The config is not rewritten in the board's
  spelling: each verb copies its `config.Verb` before the board is read. So
  a status of the config is compared with another only through
  `config.SameStatus` or `config.HasStatus`, never with `==` or
  `slices.Contains`; `board.Field.Option` finds the option, refusing a name
  that two options differing only in case match, and a filter GitHub runs
  gets the board's spelling (`Field.Spell`). A field effect's option stays
  exact: `planFields` compares its value as text.

## Working on gh-dojo with gh-dojo

gh-dojo tracks its own development on its board with the repository's
[`.github/dojo.toml`](.github/dojo.toml): issues are created and moved only
through `gh dojo`. `Ready` means committed to `main`; `Done` means released.
Agents load the rules from
[`.claude/skills/gh-dojo/SKILL.md`](.claude/skills/gh-dojo/SKILL.md), which
`gh dojo agent install` generates from the config and the notes in
[`.github/dojo-notes.md`](.github/dojo-notes.md), such as filing every
inconvenience as a `dogfood` issue right away. Never edit the skill: change
the config or the notes and run `make agent`, which uses the build of your
branch. So does CI's `agent install --check`, so a change to the generated
text, the notes or the config needs `make agent` too.

Issue tracking uses the installed `gh dojo`, the latest release installed
with `gh extension install` (`make release` does it), not a build of your
branch: a broken branch must not break it. A merged change reaches it with
the next release. Unknown config keys are errors, so a change that adds a
key to `.github/dojo.toml` (as `[agent]` did) breaks the installed
`gh dojo` until it is released: release it before the config uses the key.

## Releases

Pushing a `v*` tag runs `.github/workflows/release.yml`: the tests, then
`cli/gh-extension-precompile` with `script/build-release.sh`, which builds
darwin, linux and windows for amd64 and arm64 with the tag stamped into
`gh dojo --version`, and publishes them as a GitHub release for
`gh extension install`. A tag with a `-` (`v0.1.0-rc.1`) makes a prerelease.
The release notes list the commits since the previous tag, merges left out
(`script/release-notes.sh`), so a commit's first line is written for them
too.

Next to the binaries the release has `gh-dojo_<tag>_checksums.txt`, their
SHA-256 sums, and every asset has a build provenance attestation: a
statement, signed through Sigstore with the workflow's OIDC identity, that
`release.yml` of this repository built that file from the tagged commit.
Anyone can check a downloaded binary against it:

```sh
gh release download v0.5.0 --repo Trum-ok/gh-dojo --pattern '*darwin-arm64'
gh attestation verify gh-dojo_v0.5.0_darwin-arm64 --repo Trum-ok/gh-dojo
```

The actions in the workflows are pinned to commit SHAs, with the version in
a comment: a tag or branch can be moved to other code, a SHA cannot.
Dependabot updates the SHA and the comment together.

A release is one command, `make release TAG=vX.Y.Z`
([`script/release.sh`](script/release.sh)), which stops at the first
failure. It checks that `main` is checked out with nothing uncommitted, is
the same as `origin/main` and that the tag is newer than every `v*` tag;
tags `HEAD` and pushes the tag; waits for that tag's `release.yml` run and
checks the release's assets; installs the release with
`gh extension install` (removing the `gh dojo` installed before, a release
or a `make install` build; a prerelease is pinned with `--pin`) and checks
that `gh dojo --version` prints the tag; and moves the release's issues to Done
with `gh dojo release vX.Y.Z --verb done --comment "vX.Y.Z"`. After a
failure, run it again with the same tag: a tag already at `HEAD` is not
created again, and the later steps are safe to repeat.

The last step moves issues with the installed `gh dojo` through
[`release`](docs/commands.md#release), which explains how it finds a
release's issues and what its output means. This project's rules:

- `done` starts only from Ready here, and `[release]` in
  `.github/dojo.toml` adds `Refs #N` to the closing keywords. Before
  tagging, `gh dojo release main --verb done --dry-run` shows what the next
  release holds.
- An agent releases only on the user's word, since the release moves
  issues to Done: it runs `gh dojo release main --verb done --dry-run`,
  shows the list to the user and asks for the tag, and once they agree runs
  `make release TAG=vX.Y.Z`. On its own, the same is
  `gh dojo release vX.Y.Z --verb done --dry-run`, then without `--dry-run`,
  with `--comment "vX.Y.Z"`.
- `not ready (done starts only from Ready): #41 (In progress)` exits 0: a
  `Refs #41` commit of an issue still in progress. Don't move it by hand for
  the release; a later release picks it up only if its commits name it
  again, so once it is Ready and released, run
  `gh dojo done 41 --comment "vX.Y.Z"`, on the user's word like any `done`.
- `warning: in Ready but not found in the release: #88, #89; for any it
  includes: gh dojo done 88 89 --comment "vX.Y.Z"` exits 0: issues in Ready
  no commit of the release names, most likely work committed without
  `Refs #N`. Find their commits in the release (`git log vA..vB`) and show
  the user; on their word, run the command for those that shipped.
- Exit code 2 is a refusal, not "not ready": say, an issue closed by
  `Fixes #12` but not in Ready. Release won't move it now or later; ask the
  user.
- A number only a commit message names that is no issue the token can see
  is left out with a warning (exit code 0): a false match needs nothing (or
  `--exclude`), a real issue the token can't see goes to the user. Our own
  repository "not found" fails the batch (exit code 3), almost surely a
  token without access: tell the user.
- In a commit message, an issue reference that is not a link, such as an
  example of the syntax, goes in backticks (`` `Fixes #12` ``), or release
  takes it for one of the release's issues.
