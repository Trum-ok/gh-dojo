# gh-dojo project notes

## Process: which verb when

- `ready` only when the change is **in main** (merged, tests green), not when it is done on a branch.
- `done` only when the change has shipped in a release; **never without the user's word**. For a release, read
  "Releases" in `CONTRIBUTING.md` first: this project's rules for `gh dojo release` and what to do with its output.
- In `--comment` for `ready` and `done` it helps to give the commit or release: `--comment "5fc0e30"`.

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

`gh dojo` is the build of the last `make install` on main (`gh dojo --version`): a branch's changes reach it only
after the merge and a reinstall.
