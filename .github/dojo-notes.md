# gh-dojo project notes

## Process: which verb when

- `ready` only when the change is **in main** (merged, tests green), not when it is done on a branch.
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

`gh dojo` is the build of the last `make install` on main (`gh dojo --version`): a branch's changes reach it only
after the merge and a reinstall.
