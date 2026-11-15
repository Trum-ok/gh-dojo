// The comparison: the same request to an agent with plain gh and to one with
// gh dojo. #42 was never started, so moving it to Ready skips the process.
// gh project item-edit prints nothing when stdout is not a terminal, as for an
// agent; the gh dojo lines are its real refusal (exit code 2).
import { AGENT, Timeline, USER } from "./timeline";

const CMD = 0.018; // both agents type at the same speed
const START = 2.6; // both get the request at the same moment

export const request = { text: "#42 is done, move it to Ready.", at: 0.5, char: USER };

const gh = new Timeline()
  .wait(START)
  .line("cmd", "gh project item-edit 5 --owner acme --url https://github.com/acme/shop/issues/42 --field Status --value Ready", CMD)
  .wait(0.35)
  .mark("moved")
  .wait(0.5)
  .line("agent", "Done: #42 is in Ready.", AGENT)
  .wait(0.5)
  .mark("verdict");

const dojo = new Timeline()
  .wait(START)
  .line("cmd", "gh dojo ready 42", CMD)
  .wait(0.35)
  .mark("refused")
  .line("err", "error: ready cannot move #42 from Backlog; it starts only from In progress or Waiting")
  .line("err", "this is a process rule: ask the user how to proceed")
  .wait(0.8)
  .line("agent", "#42 was never started, it is still in Backlog. Start it now, or did you mean another issue?", AGENT)
  .wait(0.4)
  .mark("verdict");

export const ghSide = { lines: gh.lines, moved: gh.marks.moved, verdict: gh.marks.verdict };
export const dojoSide = { lines: dojo.lines, refused: dojo.marks.refused, verdict: dojo.marks.verdict };

export const FADE = 0.35;
export const DURATION = Math.max(gh.t, dojo.t) + 4.2 + FADE;
