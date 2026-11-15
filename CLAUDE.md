# CLAUDE.md

Requirements and architecture live in `docs/spec.md` (~35 KB, in Russian). Don't read it whole: find the
section (`grep -n '^## ' docs/spec.md`) and read only that one with Read `offset`/`limit`. The spec is not
committed, so in a worktree read it from the main checkout:
`"$(git rev-parse --path-format=absolute --git-common-dir)/../docs/spec.md"`.

| Spec section                                                       | Read it when                                                                                          |
|--------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------|
| Зачем (Why)                                                        | Deciding whether a feature belongs in the product: alternatives and non-goals                        |
| Принципы (Principles)                                              | Designing or changing any command's behavior: 8 rules (verbs, one line, retry, fewest requests, nothing hidden) |
| Модель и конфиг (Model and config)                                 | Touching `.github/dojo.toml`, checks (`from`, `require`, `blockers`, `*_labels`, `confirm`) or verb effects |
| Функциональные требования (Functional requirements)                | Working on a command: table F1–F14 with stages, batch exit codes, `release`, comments without duplicates |
| Нефункциональные требования (Non-functional requirements)          | Output, errors, `--json`, exit codes 0–5, request limit, cache, retries, security, tests               |
| Архитектура на Go (Go architecture)                                | Deciding which package code goes in; stack and distribution                                          |
| Интеграция с агентами (Agent integration)                          | `agent install`, skill, `--help`/`describe`, `[session]`, `confirm = "user"`                          |
| Много агентов на одной доске (черновик, не в первых этапах) (Many agents on one board) | Only for claim/lease/handoff tasks (draft, outside the stages)                     |
| Этапы (Stages)                                                     | Finding which version (v0.0–v0.3) a task belongs to; self-hosting rules                               |
| Решения и открытые вопросы (Decisions and open questions)          | Before revisiting a decision: why 3–4 requests, several repositories, hooks                           |
| Источники (Sources)                                                | Usually not needed                                                                                    |

Actual behavior of what is already built is in the user docs (`docs/commands.md`, `docs/configuration.md`,
`docs/output.md`). They are large too: read the section for the command or key.

Issues and the board are handled only through `gh dojo` (the installed version, see skill
`.claude/skills/gh-dojo/SKILL.md`), not `gh issue` or `gh project`. Any gh-dojo inconvenience noticed
along the way becomes an issue right away: `gh dojo new "<what is wrong>" --type dogfood`.

## Worktrees

- Before the first code change, if the session is in the main checkout, move into a worktree (EnterWorktree)
  and work on its own branch. Edit `main` directly only when the user explicitly asks.
- Questions, reading code and reviews need no worktree.
- Merge into `main` only on the user's command ("merge" / «слей»); push only after the user's OK.
- Spawn implementer subagents with `isolation: "worktree"`. A worktree may start from `origin/main`: rebase
  onto local `main` first and check `git merge-base --is-ancestor main HEAD` before reporting.
- Parallel sessions never edit the same checkout.
