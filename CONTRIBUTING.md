# Contributing to gh-dojo

## Build and test

Requires Go (the version in `go.mod`) and `gh`.

```sh
make build    # ./gh-dojo, stamped with git describe
make test     # go test ./...
make vet      # go vet ./...
make lint     # staticcheck, also as Windows and macOS see the code
make vuln     # govulncheck: known vulnerabilities the code can reach
make dist     # release binaries for all platforms into dist/
make agent    # regenerate the agent skill from .github/dojo.toml
make install  # build and install as the gh dojo extension (see below)
make demo     # render the README animations (needs Node and ffmpeg)
```

The animations in the README are [Remotion](https://www.remotion.dev/)
videos in [`docs/media/`](docs/media).

CI (`.github/workflows/ci.yml`) runs on pushes to `main` and on pull
requests. Once, on Linux: `gofmt -l .` must print nothing, then
`go mod tidy -diff` (for `tools/` too), `go vet ./...` (also with
`GOOS=windows` and `GOOS=darwin`), `go vet -tags e2e ./e2e`, `make lint`,
the release build, and `agent install --check` with that build (see below).
On Ubuntu, macOS and Windows: `go test ./...` (with `-race` on Ubuntu) and
`go build ./...`. Run the checks, `make lint` and `go test -race ./...`
before sending a change.

The linters are pinned in `tools/go.mod`, a module of their own, so they add
nothing to gh-dojo's dependencies; dependabot updates them with the rest.
`make lint` and `make vuln` build them into `.tools/`. A staticcheck finding
is fixed, not silenced; if one is wrong, a `//lint:ignore <check> <reason>`
on that line says why.

`.github/workflows/vuln.yml` runs `make vuln` on pushes to `main`, on pull
requests and daily, with the latest patch release of Go, since vulnerabilities
are published while the code stays the same. It fails only on a
vulnerability the code can reach; a vulnerable module whose affected code is
never called is listed in the log, and dependabot updates it.

Code and tests must work on all three: build paths with `path/filepath`,
show them to users with `filepath.ToSlash`, and keep OS error texts out of
golden files. `.gitattributes` checks every file out with LF line endings,
Windows included.

## Install from source

To run the latest `main`, install from source; this needs Go
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
with gh's token:

```sh
GH_DOJO_E2E_REPO=Trum-ok/gh-dojo-sandbox GH_DOJO_E2E_KANBAN=6 GH_DOJO_E2E_SPRINTS=7 \
  go test -tags e2e -count=1 -v ./e2e
```

The sprints board's Sprint field has iterations up to November 2027: add
more before then, or the sprint checks fail. `e2e/doc.go` lists the
variables and what the boards need.

## Releases

Pushing a `v*` tag runs `.github/workflows/release.yml`: the tests, then
`cli/gh-extension-precompile` with `script/build-release.sh`, which builds
darwin, linux and windows for amd64 and arm64 with the tag stamped into
`gh dojo --version`, and publishes them as a GitHub release for
`gh extension install`. A tag with a `-` (`v0.1.0-rc.1`) makes a prerelease.

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

Once the tag is pushed, its issues move to Done with the installed
`gh dojo`: `gh dojo release vX.Y.Z --verb done --dry-run` lists them (found
through `Refs #N` and closing keywords in the commits since the previous
version tag, see `[release]` in `.github/dojo.toml`), then the same without
`--dry-run`. The first release has no version tag before it:
`--since c720ec9`, the first commit. `done` starts only from Ready here, so
an open issue still in progress with a `Refs` commit in the release is
listed as not ready and left alone; a later release picks it up only if its
commits name it again, else `gh dojo done N` once it is Ready and released.

So in a commit message, an issue reference that is not a link, such as an
example of the syntax, goes in backticks: `` `Fixes #12` ``,
`` `refs: #12, acme/api#3` ``. Release does not count code; without the
backticks it takes those numbers as the release's issues. One a message
names that is no issue is left out with a warning (exit code 0); any other
mistaken match is left out with `--exclude N`.

An agent moves a release's issues to Done only on the user's word: it runs
the `--dry-run`, shows the list to the user, and once they agree runs the
same without `--dry-run`, optionally with `--comment "vX.Y.Z"`. What the
output means:

- `not ready (done starts only from Ready): #41 (In progress)` is normal and
  exits 0: a partial commit (`Refs #41`) of an issue still in progress.
  Don't move it by hand for the release; once it is Ready and released, run
  `gh dojo done 41 --comment "vX.Y.Z"`, on the user's word like any `done`.
- Exit code 2 is a refusal, not "not ready": say, the issue is closed
  (`Fixes #12` in a commit) but not in Ready. Release won't move it now or
  later; ask the user.
- A warning like `acme/api#3: repository acme/api not found, or the token
  cannot see it: left out` or `#99 not found: left out` means a number only
  in a commit's text that is no issue the token can see (exit code 0). A
  false match needs nothing (or `--exclude acme/api#3`); a real issue the
  token can't see goes to the user. Our own repository "not found" fails
  the batch (exit code 3), almost surely a token without access: tell the
  user.

The interface, help text and documentation are in English. Texts that go
into issues (verb `when` lines and comment templates) come from the config.

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

### Requests and retries

- Reading a card is one GraphQL request; a verb on an issue already on the
  board is two with a warm cache (read, then one mutation with all its
  effects). An issue not on the board takes three, `new` four (read,
  `createIssue`, add to the board, the rest): GraphQL cannot feed one
  mutation field's result into another in the same document, and setting a
  status needs the item ID only `addProjectV2ItemById` returns.
  `createIssue` with `projectV2Ids` is not used: GitHub adds the item
  asynchronously and does not return its ID (#17).
- The board schema is cached for an hour (see
  [Checked against the board](docs/configuration.md#checked-against-the-board)).
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
  with no issue and no error at all (#67). So `render.IsContentLimit`
  matches GitHub's wording; add a phrase there when GitHub words it anew.
- Auth comes only from gh's token (or `GH_TOKEN`); gh-dojo stores no secrets,
  never prints the token and sends no telemetry.

### Decisions

- **No MCP server.** The target agents have a shell, and a CLI costs fewer
  tokens and works in any of them. A local MCP server would only serve agents
  without a shell; revisit if one asks.
- **Every command that writes to GitHub takes `--dry-run`** (verbs,
  `release`, `new`, `comment`, `block`, `unblock`; `init` for its file):
  agents carry the flag from one command to the next (#60). It reads and
  checks what the run does and prints the plan; its JSON has `dry_run`.
  A new command that writes gets it too.
- **`release` goes through pull requests**, so it works with any merge
  strategy (squash, merge, rebase); commit keywords are the fallback.
- **The generated skill is loaded into every session, so it repeats
  nothing:** it does not point to `describe` (the same text) or list the
  commands; of the exit codes it covers only what to do on 2, 4 and 5 (an
  error's `fix:` line covers the rest); other repositories get detail only
  with several `board.repos`, and `release` one line pointing to its
  `--help`. Keep it that way when changing `internal/agent`.
- **Label groups are a prefix, not a list** (#75). `labels.type_prefix` and
  `labels.area_prefix` work alike: the values are the repository's labels
  with the prefix, so the config cannot drift from them, and a new type is
  `gh label create`. The price: `--type` is no longer checked offline and
  the skill names the prefix, not the types; `new`'s error lists them. An
  earlier `labels.types` list was dropped for this; do not bring it back,
  nor add a key per new label scheme.

## Layout

| Package             | Responsible for                                                    |
|---------------------|--------------------------------------------------------------------|
| `cmd/gh-dojo`       | Entry point; cobra commands, verbs built from the config           |
| `internal/config`   | Loading and validating `.github/dojo.toml`; embedded presets        |
| `internal/board`    | Board schema for user and org projects, its cache, config checks   |
| `internal/workflow` | Verb checks and the effect plan; `new`'s checks and plan           |
| `internal/effects`  | Effects → one GraphQL mutation; `new`'s mutations                  |
| `internal/batch`    | Running a verb on its issues: one by one, retries, the summary     |
| `internal/issue`    | Reading issues (cards) and batches of them; what `new` reads       |
| `internal/graph`    | GraphQL client over go-gh: retries, pagination, error mapping      |
| `internal/render`   | Errors, exit codes, confirmations, folding with counts             |
| `internal/agent`    | `agent install`'s files: targets, markers, writing, diffs          |
| `internal/doctor`   | `doctor`'s checks of the setup and its report                      |
| `internal/release`  | `release`: a release's commits, their pull requests and keywords   |
| `internal/testutil` | Test helpers shared across packages: golden files                  |

The plan is separate from its execution: the workflow engine returns the
operations without touching the network, which is what `--dry-run`, the
tests and finishing a partially applied command rely on.

A command prints its result through `emit` (`cmd/gh-dojo/output.go`): it
builds one typed result and passes the function that prints it as text, so
the text and `--json` come from the same data (`emitWrite` for a command
that writes). JSON fields are snake_case and always present: lists are `[]`,
an absent value is `null`, never `""`; no node IDs. A command returns its
error as usual: `run` prints it as JSON too. A new field is fine, a renamed or
removed one needs a new `schema_version` ([docs/output.md](docs/output.md#json-output) lists the
schemas).
Each command has JSON golden files under `cmd/gh-dojo/testdata/json/`.

## Golden files

Command output, errors and help are compared with `testdata/**/*.golden`
files. After an intended output change, rewrite them and review the diff
before committing:

```sh
make golden   # UPDATE_GOLDEN=1 go test ./...
```

Rewriting is refused when `CI` is set, so CI can never pass by accepting its
own output. `internal/config`'s `TestReadmeExamples` loads every `toml` block
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

## Working on gh-dojo with gh-dojo

gh-dojo tracks its own development on its board with the repository's
[`.github/dojo.toml`](.github/dojo.toml): issues are created and moved only
through `gh dojo` (`new`, `start`, `wait`, `ready`, `done`, `plan`,
`release`, `comment`, `block`, `unblock`).
`Ready` means committed to `main`; `Done` means released. Agents load the
rules from [`.claude/skills/gh-dojo/SKILL.md`](.claude/skills/gh-dojo/SKILL.md),
which `gh dojo agent install` generates from the config and the notes
in [`.github/dojo-notes.md`](.github/dojo-notes.md). Never edit the skill:
change the config or the notes and run `make agent`, which uses the build of
your branch. So does CI's `agent install --check`, so a change to the
generated text, the notes or the config needs `make agent` too.

- Use the installed `gh dojo`, the last `make install` from `main`, not a
  build of your branch: a broken branch must not break issue tracking.
  After a change is merged, run `make install` on `main` again. Unknown
  config keys are errors, so a change that adds a key to `.github/dojo.toml`
  (as `[agent]` did) breaks the installed `gh dojo` until that reinstall.
- Any inconvenience noticed while using gh-dojo becomes an issue right away:
  `gh dojo new "<title>" --type dogfood --body "..."`.
