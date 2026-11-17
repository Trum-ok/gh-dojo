// The comparison: the same request to an agent with plain gh and to one with
// gh dojo. With plain gh the agent writes GraphQL and carries node IDs between
// calls, and nothing reminds it to say why the issue waits. The mutations are
// the ones gh-dojo itself sends; the gh dojo line is its real output.
import { Chat, FADE } from "./timeline";

const REQUEST = "#42 is stuck until #12 lands.";

const gh = new Chat()
  .wait(0.4)
  .user(REQUEST)
  .wait(0.5)
  .agent("I need the node IDs of the issue, its project item and the Status field.")
  .wait(0.2)
  .tool(
    `gh api graphql -f query='{ repository(owner: "acme", name: "shop") { issue(number: 42) { id projectItems(first: 5) { nodes { id project { id } } } } } }'`,
    [
      `{"data":{"repository":{"issue":{"id":"I_kwDOH2x4Lc6tW9Qa","projectItems":{"nodes":[{"id":"PVTI_lADOBx4Kzc4AqA3fzgV9Z1Q","project":{"id":"PVT_kwDOBx4Kzc4AqA3f"}}]}}}}}`,
    ],
    { latency: 0.7 },
  )
  .wait(0.4)
  .tool("gh project field-list 5 --owner acme --format json", [
    `{"fields":[{"id":"PVTF_lADOBx4Kzc4AqA3fzgg2Hk0","name":"Title","type":"ProjectV2Field"},{"id":"PVTSSF_lADOBx4Kzc4AqA3fzgg2Hk4","name":"Status","options":[{"id":"f75ad846","name":"Backlog"},{"id":"47fc9ee4","name":"In progress"},{"id":"98236657","name":"Waiting"},{"id":"df73e18b","name":"Ready"}]}]}`,
  ])
  .wait(0.4)
  .tool(
    `gh api graphql -f query='mutation { updateProjectV2ItemFieldValue(input: {projectId: "PVT_kwDOBx4Kzc4AqA3f", itemId: "PVTI_lADOBx4Kzc4AqA3fzgV9Z1Q", fieldId: "PVTSSF_lADOBx4Kzc4AqA3fzgg2Hk4", value: {singleSelectOptionId: "98236657"}}) { projectV2Item { id } } }'`,
    [`{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"PVTI_lADOBx4Kzc4AqA3fzgV9Z1Q"}}}}`],
  )
  .wait(0.4)
  .tool("gh issue view 12 --json id --jq .id", ["I_kwDOH2x4Lc6tVx7b"])
  .wait(0.4)
  .tool(
    `gh api graphql -f query='mutation { addBlockedBy(input: {issueId: "I_kwDOH2x4Lc6tW9Qa", blockingIssueId: "I_kwDOH2x4Lc6tVx7b"}) { issue { id } } }'`,
    [`{"data":{"addBlockedBy":{"issue":{"id":"I_kwDOH2x4Lc6tW9Qa"}}}}`],
  )
  .wait(0.4)
  .agent("Done: #42 is in Waiting, blocked by #12.")
  .wait(0.5)
  .mark("verdict");

const dojo = new Chat()
  .wait(0.4)
  .user(REQUEST)
  .wait(0.5)
  .tool("gh dojo wait 42 --by 12", ["#42 In progress → Waiting, blocked by #12, comment left"])
  .wait(0.4)
  .agent("Done: #42 waits for #12, and a comment on it says so.")
  .wait(0.5)
  .mark("verdict");

export const ghSide = { msgs: gh.msgs, verdict: gh.marks.verdict };
export const dojoSide = { msgs: dojo.msgs, verdict: dojo.marks.verdict };

export const DURATION = Math.max(gh.t, dojo.t) + 3.5 + FADE;
