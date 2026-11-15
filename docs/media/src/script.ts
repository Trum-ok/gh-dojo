// The story of the main demo. Command output and comments are the real text
// gh-dojo prints and posts with the kanban preset (internal/config/presets/kanban.toml).
import { Chat } from "./timeline";

const chat = new Chat()
  .wait(0.4)
  .user("Take #42, the CSV export.")
  .wait(0.5)
  .agent("On it.")
  .wait(0.2)
  .tool("gh dojo start 42", ["#42 Backlog → In progress, comment left"])
  .mark("start")
  .wait(1.6)
  .agent("The export needs the payments API from #12, so I'll wait for it.")
  .wait(0.3)
  .tool("gh dojo wait 42 --by 12", ["#42 In progress → Waiting, blocked by #12, comment left"])
  .mark("wait")
  .wait(1.2)
  .mark("resolved") // #12 is finished elsewhere: it reaches Ready and no longer blocks #42
  .wait(1.7)
  .agent("#12 is ready. The export is done and committed as a1b2c3d.")
  .wait(0.3)
  .tool("gh dojo ready 42 --comment a1b2c3d", ["#42 Waiting → Ready, comment left"])
  .mark("ready")
  .wait(1.3)
  .agent("#42 is in Ready.")
  .wait(2.6);

export const msgs = chat.msgs;
export const FADE = 0.35;
export const DURATION = chat.t + FADE;

const { start, wait, resolved, ready } = chat.marks;
export const WAIT_AT = wait;
export const BLOCKER_RESOLVED = resolved;

// What each verb does to #42, in the order gh-dojo does it: the status, the
// blocked-by link, the comment.
export type Entry = { at: number } & (
  | { kind: "moved"; from: string; to: string }
  | { kind: "blocked"; by: string }
  | { kind: "comment"; body: string }
);

export const thread: Entry[] = [
  { at: start + 0.1, kind: "moved", from: "Backlog", to: "In progress" },
  { at: start + 0.3, kind: "comment", body: "Started. Session: claude://claude.ai/epitaxy/7f3a…" },
  { at: wait + 0.1, kind: "moved", from: "In progress", to: "Waiting" },
  { at: wait + 0.25, kind: "blocked", by: "#12" },
  { at: wait + 0.4, kind: "comment", body: "Waiting for #12." },
  { at: ready + 0.1, kind: "moved", from: "Waiting", to: "Ready" },
  { at: ready + 0.3, kind: "comment", body: "a1b2c3d" },
];

export const columns = [
  { name: "Backlog", color: "muted" },
  { name: "In progress", color: "yellow" },
  { name: "Waiting", color: "orange" },
  { name: "Ready", color: "green" },
] as const;

export type Card = {
  id: string;
  title: string;
  label: string;
  // where the card is from each moment on: column and slot in it
  moves: { at: number; col: number; slot: number }[];
};

export const cards: Card[] = [
  {
    id: "42",
    title: "Add CSV export",
    label: "feature",
    moves: [
      { at: 0, col: 0, slot: 0 },
      { at: start, col: 1, slot: 2 },
      { at: wait, col: 2, slot: 0 },
      { at: ready, col: 3, slot: 2 },
    ],
  },
  {
    id: "43",
    title: "Dark mode",
    label: "feature",
    moves: [
      { at: 0, col: 0, slot: 1 },
      { at: start + 0.15, col: 0, slot: 0 },
    ],
  },
  {
    id: "44",
    title: "Webhook retries",
    label: "bug",
    moves: [
      { at: 0, col: 0, slot: 2 },
      { at: start + 0.2, col: 0, slot: 1 },
    ],
  },
  {
    id: "12",
    title: "Payments API v2",
    label: "feature",
    moves: [
      { at: 0, col: 1, slot: 0 },
      { at: resolved, col: 3, slot: 1 },
    ],
  },
  {
    id: "38",
    title: "Flaky login test",
    label: "bug",
    moves: [
      { at: 0, col: 1, slot: 1 },
      { at: resolved + 0.15, col: 1, slot: 0 },
    ],
  },
  {
    id: "35",
    title: "Onboarding guide",
    label: "docs",
    moves: [{ at: 0, col: 3, slot: 0 }],
  },
];
