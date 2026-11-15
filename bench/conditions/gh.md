# CLAUDE.md

## Issues and the board

Issues of {{.Repo}} are tracked on the GitHub Projects board of {{.Owner}}, project {{.Project}}, in its
single-select field Status. Work with them through `gh` (`gh issue`, `gh project`, `gh api graphql`).

IDs of the board, for `gh project item-edit` and GraphQL:

- project: `{{.ProjectID}}`
- field Status: `{{.StatusField}}`
- its options: Backlog `{{index .Options "Backlog"}}`, In progress `{{index .Options "In progress"}}`,
  Waiting `{{index .Options "Waiting"}}`, Ready `{{index .Options "Ready"}}`, Done `{{index .Options "Done"}}`

### Process

`Backlog` → `In progress` → (`Waiting`) → `Ready` → `Done`. Allowed moves:

| Step  | When                                                     | From                     | To          | Also                                                         |
|-------|----------------------------------------------------------|--------------------------|-------------|--------------------------------------------------------------|
| start | you begin working on the issue                           | Backlog, Waiting, Ready  | In progress | comment `Started.`                                           |
| wait  | work is stuck: waiting for another issue or a decision  | In progress              | Waiting     | comment `Waiting for <what>.`; waiting for an issue: mark the issue blocked by it |
| ready | done, verified and committed, not released yet           | In progress, Waiting     | Ready       |                                                              |
| done  | the change is released                                   | Ready, In progress       | Done        | close the issue as completed                                 |

- A new issue goes on the board in `Backlog`, with one of the labels `bug`, `enhancement`, `documentation`.
  An issue without a status, or not on the board, counts as `Backlog`; put it on the board when you move it.
- An issue is *blocked* by its blocked-by links (GitHub's issue dependencies). A blocker is resolved when it is
  closed or its status is `Ready` or `Done`.
- The next issue to take: open, in `Backlog` (or without a status), with no unresolved blocker, first in board order.
- Before starting an issue with an unresolved blocker, ask the user.
- A move the table does not allow: do not work around it, ask the user.
- Do not post the same comment twice: after a failure, check what is already done before repeating a step.

### Recipes

The state of issue N: its board item, status, blockers and their state. `item` is empty when the issue is not on
the board.

```sh
gh api graphql -F n=N -f query='query($n: Int!) { repository(owner: "{{.Owner}}", name: "{{.Name}}") { issue(number: $n) {
  id state title
  projectItems(first: 10) { nodes { id project { number } status: fieldValueByName(name: "Status") { ... on ProjectV2ItemFieldSingleSelectValue { name } } } }
  blockedBy(first: 20) { nodes { number state projectItems(first: 10) { nodes { project { number } status: fieldValueByName(name: "Status") { ... on ProjectV2ItemFieldSingleSelectValue { name } } } } } }
} } }' --jq '.data.repository.issue | {id, state, title,
  item: ([.projectItems.nodes[] | select(.project.number == {{.Project}})][0] // {}),
  blockedBy: [.blockedBy.nodes[] | {number, state, status: ([.projectItems.nodes[] | select(.project.number == {{.Project}}) | .status.name][0])}]}'
```

Put an issue on the board (prints its item ID; an issue already there keeps its item):

```sh
gh project item-add {{.Project}} --owner {{.Owner}} --url https://github.com/{{.Repo}}/issues/N --format json --jq .id
```

Set the status of an item:

```sh
gh project item-edit --id ITEM_ID --project-id {{.ProjectID}} --field-id {{.StatusField}} --single-select-option-id OPTION_ID
```

Mark issue N blocked by issue M (node IDs, `id` of the state query, or `gh issue view M --json id --jq .id`):

```sh
gh api graphql -f query='mutation { addBlockedBy(input: {issueId: "N_ID", blockingIssueId: "M_ID"}) { clientMutationId } }'
```

The board in order, open issues only, with status and unresolved blockers:

```sh
gh api graphql -f query='query { user(login: "{{.Owner}}") { projectV2(number: {{.Project}}) { items(first: 100) { nodes {
  status: fieldValueByName(name: "Status") { ... on ProjectV2ItemFieldSingleSelectValue { name } }
  content { ... on Issue { number title state
    blockedBy(first: 20) { nodes { number state projectItems(first: 10) { nodes { project { number } status: fieldValueByName(name: "Status") { ... on ProjectV2ItemFieldSingleSelectValue { name } } } } } } } }
} } } } }' --jq '.data.user.projectV2.items.nodes[] | select(.content.state == "OPEN") | {number: .content.number, title: .content.title, status: .status.name,
  unresolvedBlockers: [.content.blockedBy.nodes[] | select(.state == "OPEN" and ([.projectItems.nodes[] | select(.project.number == {{.Project}}) | .status.name][0] | . != "Ready" and . != "Done")) | .number]}'
```

Comment, close, create: `gh issue comment N --body "…"`, `gh issue close N --reason completed`,
`gh issue create --title "…" --body "…" --label bug` (prints the issue's URL).
