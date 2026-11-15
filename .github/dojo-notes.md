# gh-dojo project notes

This section is the project's addition to the generated part above: the file `.github/dojo-notes.md` (`[agent] notes`
in `.github/dojo.toml`), inserted as is. Don't edit the skill itself: change the config or these notes and run
`make agent` (that is `go run ./cmd/gh-dojo agent install`; CI checks `agent install --check`).

## Process: which verb when

- `ready` only when the change is **in main** (merged, tests green), not when it is done on a branch. Several at
  once: `gh dojo ready 12 13 14`.
- `done` only when the change has shipped in a release; **never without the user's word**. For a release, read
  "Releases" in `CONTRIBUTING.md` first: how `gh dojo release` finds the issues and what its output means.
- In `--comment` for `ready` and `done` it helps to give the commit or release: `--comment "5fc0e30"`.

## Inconvenience → a `dogfood` issue

Any inconvenience while working with gh-dojo (an unclear error, an extra step, a missing flag, help that lies, having
to work around it with `gh`): open an issue **right away**, without putting it off or quietly fixing it:

```sh
gh dojo new --type dogfood --body "What I did: … What I expected: … What I got: …" -- "<what is inconvenient, briefly>"
```

Then go on with the main task.

## What gh-dojo lacks

Here `gh` is allowed, but only when the task needs it or the user asks:

- close an issue other than through `done` (not needed, a duplicate) or reopen one:
  `gh issue close N --reason "not planned"` / `gh issue reopen N`, only on the user's decision (our config has no
  `drop` or `revive` verbs);
- change the title, body, labels or assignees of an existing issue: `gh issue edit N …`.

## Which version runs

The installed `gh dojo` is the build of the last `make install` from main, not of the current branch: a broken branch
doesn't break issue tracking. New behavior from a branch won't appear in `gh dojo` until it is merged into main and
reinstalled. The text above was produced by the build that generated the skill; the installed version may lag behind
it:

```sh
gh dojo --version   # git describe of the installed build
make install        # in a working copy on main, after the merge
```
