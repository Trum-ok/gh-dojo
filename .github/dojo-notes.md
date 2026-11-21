# gh-dojo project notes

## Process: which verb when

- `ready` only when the change is **in main** (merged, tests green), not when it is done on a branch.
- `done` only when the change has shipped in a release; **never without the user's word**. For a release, read
  "Releases" in `CONTRIBUTING.md` first: this project's rules for `gh dojo release` and what to do with its output.
- A release is one command, not a sequence of your own: show the user `gh dojo release main --verb done --dry-run`
  and the notes, `script/release-notes.sh HEAD`, ask for the tag, and on their word run `make release TAG=vX.Y.Z` (`script/release.sh`: tag, push, wait for the
  release run, `gh extension install` of the release, move the release's issues to Done). It takes several minutes: run it in the background.
  After a failure, run it again with the same tag.
- In `--comment` for `ready` and `done` it helps to give the commit or release: `--comment "5fc0e30"`.
- A commit with a change users would notice adds `Changelog: <kind>: <text>` to its message, kind `breaking`,
  `added`, `changed` or `fixed`, text in the user's words: the release notes are made of these lines
  ("Release notes" in `CONTRIBUTING.md`). Internal commits have none.

## Inconvenience → a `dogfood` issue

Any inconvenience while working with gh-dojo (an unclear error, an extra step, a missing flag, help that lies, having
to work around it with `gh`): open an issue **right away**, without putting it off or quietly fixing it:

```sh
gh dojo new "<what is inconvenient, briefly>" --type dogfood --body "What I did: … What I expected: … What I got: …"
```

Then go on with the main task.

## Closing and reopening

Our config has no `drop` or `revive` verbs: to close an issue other than through `done` (not needed, a duplicate)
or to reopen one, use `gh issue close N --reason "not planned"` / `gh issue reopen N`, only on the user's decision.

## Which version runs

`gh dojo` is the latest release, installed with `gh extension install` (`gh dojo --version`): a change reaches it
only after the merge and a release.
