# gh-dojo project notes

This section is the project's addition to the generated part above: the file `.github/dojo-notes.md` (`[agent] notes`
in `.github/dojo.toml`), inserted as is. Don't edit the skill itself: change the config or these notes and run
`make agent` (that is `go run ./cmd/gh-dojo agent install`; CI checks `agent install --check`).

## Process: which verb when

- `ready` only when the change is **in main** (merged, tests green), not when it is done on a branch. `done` starts
  only from `Ready` (`from = ["Ready"]` in the config).
- `done` only when the change has shipped in a release; **never without the user's word**.
- `done` for a release goes through `gh dojo release` once the tag `vX.Y.Z` is pushed: first
  `gh dojo release vX.Y.Z --verb done --dry-run`, show the list to the user, and only on their word run the same
  without `--dry-run` (optionally with `--comment "vX.Y.Z"`). Release finds issues by `Refs #N` and closing keywords
  in the commits since the previous version tag (`[release] keywords` in `.github/dojo.toml`); the first release takes
  `--since c720ec9`.
- A line `not ready (done starts only from Ready): #41 (In progress)` in release's output is normal: the release
  includes a partial commit (`Refs #41`) of an open issue still in progress. Release leaves it alone and doesn't count
  it as a failure (exit code 0). Don't move it by hand for the release. But it won't reach `Done` on its own: only a
  release whose commits name it again (`Refs #41`) picks it up; otherwise, once it is in `Ready` and shipped in a
  release, move it explicitly: `gh dojo done 41 --comment "vX.Y.Z"` (on the user's word, like any `done`).
- Exit code 2 from release is a refusal, not "not ready": for example, the issue is closed (`Fixes #12` in a commit)
  but not in `Ready` (`#12 is closed but in In progress`). Release won't move it now or later; ask the user.
- An example reference in a commit message (syntax, another repository) goes **in backticks**: `` `Fixes #12` ``,
  `` `refs: #12, acme/api#3` ``. Release ignores code; without backticks it takes those numbers as the release's
  issues. A real reference goes without backticks: `Refs #51`.
- A release warning like `acme/api#3: repository acme/api not found, or the token cannot see it: left out` or
  `#99 not found: left out` means the number appears only in a commit's text and there is no such issue (or the token
  can't see it); release skips it (exit code 0). If it is a false match, do nothing (or `--exclude acme/api#3`); if the
  issue is real but the token can't see it, tell the user. Only repositories outside `board.repos` are skipped: our own
  repository "not found" fails the batch (exit code 3), almost certainly because the token lacks access; tell the user.
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
reinstalled. The command table above was produced by the build that generated the skill; the installed version may lag
behind it:

```sh
gh dojo --version   # git describe of the installed build
make install        # in a working copy on main, after the merge
```
