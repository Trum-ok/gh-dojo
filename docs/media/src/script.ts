// The story of the main demo. Command output and comments are the real text
// gh-dojo prints and posts with the kanban preset (internal/config/presets/kanban.toml).
import { AGENT, CMD, Timeline, USER } from "./timeline";

const tl = new Timeline()
  .wait(0.5)
  .line("user", "Take #42, the CSV export.", USER)
  .wait(0.7)
  .line("cmd", "gh dojo start 42", CMD)
  .wait(0.45)
  .mark("start")
  .line("out", "#42 Backlog → In progress, comment left")
  .wait(1.5)
  .line("agent", "The export needs the payments API from #12. Waiting for it.", AGENT)
  .wait(0.4)
  .line("cmd", "gh dojo wait 42 --by 12", CMD)
  .wait(0.45)
  .mark("wait")
  .line("out", "#42 In progress → Waiting, blocked by #12, comment left")
  .wait(1.0)
  .mark("resolved") // #12 is finished elsewhere: it reaches Ready and no longer blocks #42
  .wait(1.3)
  .line("agent", "#12 is ready. The export is done and committed as a1b2c3d.", AGENT)
  .wait(0.4)
  .line("cmd", "gh dojo ready 42 --comment a1b2c3d", CMD)
  .wait(0.45)
  .mark("ready")
  .line("out", "#42 Waiting → Ready, comment left")
  .wait(2.4);

export const lines = tl.lines;
export const FADE = 0.35;
export const DURATION = tl.t + FADE;

const { start, wait, resolved, ready } = tl.marks;
export const WAIT_AT = wait;
export const BLOCKER_RESOLVED = resolved;

// What each verb posts on #42, and when it shows up.
export const comments = [
  { at: start, body: "Started. Session: claude://claude.ai/epitaxy/7f3a…" },
  { at: wait, body: "Waiting for #12." },
  { at: ready, body: "a1b2c3d" },
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
    title: "Dark mode for settings",
    label: "feature",
    moves: [
      { at: 0, col: 0, slot: 1 },
      { at: start + 0.15, col: 0, slot: 0 },
    ],
  },
  {
    id: "44",
    title: "Retry failed webhooks",
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
    title: "Fix flaky login test",
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
