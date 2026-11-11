# Contributing to gh-dojo

## Build and test

Requires Go (the version in `go.mod`) and `gh`.

```sh
make build    # ./gh-dojo, stamped with git describe
make test     # go test ./...
make vet      # go vet ./...
make install  # build and install as the gh dojo extension (see README)
```

CI (`.github/workflows/ci.yml`) runs on pushes to `main` and on pull
requests: `gofmt -l .` must print nothing, then `go mod tidy -diff`,
`go vet ./...`, `go test ./...` and `go build ./...`. Run the same before
sending a change.

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

The plan is separate from its execution: the workflow engine returns the
operations without touching the network, which is what `--dry-run`, the
tests and finishing a partially applied command rely on.

## Golden files

Command output, errors and help are compared with `testdata/**/*.golden`
files. After an intended output change, rewrite them and review the diff
before committing:

```sh
make golden   # UPDATE_GOLDEN=1 go test ./...
```

Rewriting is refused when `CI` is set, so CI can never pass by accepting its
own output. `internal/config`'s `TestReadmeExamples` loads every `toml` block
in `README.md`, so README examples must stay valid configs.

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

## Working on gh-dojo with gh-dojo

gh-dojo tracks its own development on its board with the repository's
[`.github/dojo.toml`](.github/dojo.toml): issues are created and moved only
through `gh dojo` (`new`, `start`, `wait`, `ready`, `done`, `comment`).
`Ready` means committed to `main`; `Done` means released. Agents load the
rules from [`.claude/skills/gh-dojo/SKILL.md`](.claude/skills/gh-dojo/SKILL.md).

- Use the installed `gh dojo`, the last `make install` from `main`, not a
  build of your branch: a broken branch must not break issue tracking.
  After a change is merged, run `make install` on `main` again.
- Any inconvenience noticed while using gh-dojo becomes an issue right away:
  `gh dojo new "<title>" --type dogfood --body "..."`.
