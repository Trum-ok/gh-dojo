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
branches in the working copy. Re-run it to update.

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
make golden   # rewrite golden files after an intended output change
```
