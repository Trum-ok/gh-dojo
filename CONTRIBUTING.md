# Contributing to gh-dojo

## Build and test

Requires Go (the version in `go.mod`) and `gh`.

```sh
make build    # ./gh-dojo, stamped with git describe
make test     # go test ./...
make vet      # go vet ./...
make dist     # release binaries for all platforms into dist/
make agent    # regenerate the agent skill from .github/dojo.toml
make install  # build and install as the gh dojo extension (see README)
```

CI (`.github/workflows/ci.yml`) runs on pushes to `main` and on pull
requests. Once, on Linux: `gofmt -l .` must print nothing, then
`go mod tidy -diff`, `go vet ./...` (also with `GOOS=windows` and
`GOOS=darwin`), `go vet -tags e2e ./e2e`, the release build, and
`agent install --check` with that build (see below). On Ubuntu, macOS and Windows:
`go test ./...` (with `-race` on Ubuntu) and `go build ./...`. Run the
checks and `go test -race ./...` before sending a change.

Code and tests must work on all three: build paths with `path/filepath`,
show them to users with `filepath.ToSlash`, and keep OS error texts out of
golden files. `.gitattributes` checks every file out with LF line endings,
Windows included.

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

Once the tag is pushed, its issues move to Done with the installed
`gh dojo`: `gh dojo release vX.Y.Z --verb done --dry-run` lists them (found
through `Refs #N` and closing keywords in the commits since the previous
version tag, see `[release]` in `.github/dojo.toml`), then the same without
`--dry-run`. The first release has no version tag before it:
`--since c720ec9`, the first commit. `done` starts only from Ready here, so
an open issue still in progress with a `Refs` commit in the release is
listed as not ready and left alone; a later release picks it up only if its
commits name it again, else `gh dojo done N` once it is Ready and released.

The interface, help text and documentation are in English. Texts that go
into issues (verb `when` lines and comment templates) come from the config.

## Layout

| Package             | Responsible for                                                    |
|---------------------|--------------------------------------------------------------------|
| `cmd/gh-dojo`       | Entry point; cobra commands, verbs built from the config           |
| `internal/config`   | Loading and validating `.github/dojo.toml`; embedded presets        |
| `internal/board`    | Board schema for user and org projects, its cache, config checks   |
| `internal/workflow` | Verb checks and the effect plan                                    |
| `internal/effects`  | Effects → one GraphQL mutation                                     |
| `internal/issue`    | Reading issues (cards) and batches of them                         |
| `internal/graph`    | GraphQL client over go-gh: retries, pagination, error mapping      |
| `internal/render`   | Errors, exit codes, confirmations, folding with counts             |
| `internal/agent`    | `agent install`'s files: targets, markers, writing, diffs          |
| `internal/release`  | `release`: a release's commits, their pull requests and keywords   |

The plan is separate from its execution: the workflow engine returns the
operations without touching the network, which is what `--dry-run`, the
tests and finishing a partially applied command rely on.

A command prints its result through `emit` (`cmd/gh-dojo/output.go`): it
builds one typed result and passes the function that prints it as text, so
the text and `--json` come from the same data (`emitWrite` for a command
that writes). JSON fields are snake_case and always present: lists are `[]`,
an absent value is `null`, never `""`; no node IDs. A command returns its
error as usual: `run` prints it as JSON too. A new field is fine, a renamed or
removed one needs a new `schema_version` (README, "JSON output", lists the
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
in `README.md`, and `TestExampleConfigs` every `examples/*.toml`, so README
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
through `gh dojo` (`new`, `start`, `wait`, `ready`, `done`, `release`,
`comment`, `block`, `unblock`).
`Ready` means committed to `main`; `Done` means released. Agents load the
rules from [`.claude/skills/gh-dojo/SKILL.md`](.claude/skills/gh-dojo/SKILL.md),
which `gh dojo agent install` generates from the config and the Russian notes
in [`.github/dojo-notes.md`](.github/dojo-notes.md). Never edit the skill:
change the config or the notes and run `make agent`, which uses the build of
your branch. So does CI's `agent install --check`, so a change to a command's
short help, the generated text or the config needs `make agent` too.

- Use the installed `gh dojo`, the last `make install` from `main`, not a
  build of your branch: a broken branch must not break issue tracking.
  After a change is merged, run `make install` on `main` again. Unknown
  config keys are errors, so a change that adds a key to `.github/dojo.toml`
  (as `[agent]` did) breaks the installed `gh dojo` until that reinstall.
- Any inconvenience noticed while using gh-dojo becomes an issue right away:
  `gh dojo new "<title>" --type dogfood --body "..."`.
