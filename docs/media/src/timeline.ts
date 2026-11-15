// A chat with an agent, laid out in time (seconds).

export type Msg =
  // typed into the input from `at`, sent as a bubble at `sent`
  | { kind: "user"; text: string; at: number; sent: number }
  // the agent's reply, streamed from `at`
  | { kind: "agent"; text: string; at: number }
  // a command the agent runs; its output shows up at `outAt`
  | { kind: "tool"; cmd: string; out: string[]; err: boolean; at: number; outAt: number };

export const USER = 0.045; // a person typing, seconds per character
export const AGENT = 0.014; // an agent's reply, streamed
const SEND = 0.3; // from the last character to the bubble

export const shownAt = (m: Msg) => (m.kind === "user" ? m.sent : m.at);

// Builds a chat: wait() moves the clock, user()/agent()/tool() add a message
// at the current time and move the clock to its end, mark() names a moment.
export class Chat {
  t = 0;
  msgs: Msg[] = [];
  marks: Record<string, number> = {};

  wait(s: number) {
    this.t += s;
    return this;
  }

  user(text: string) {
    const sent = this.t + text.length * USER + SEND;
    this.msgs.push({ kind: "user", text, at: this.t, sent });
    this.t = sent;
    return this;
  }

  agent(text: string) {
    this.msgs.push({ kind: "agent", text, at: this.t });
    this.t += text.length * AGENT;
    return this;
  }

  tool(cmd: string, out: string[], { latency = 0.5, err = false } = {}) {
    const outAt = this.t + latency;
    this.msgs.push({ kind: "tool", cmd, out, err, at: this.t, outAt });
    this.t = outAt;
    return this;
  }

  mark(name: string) {
    this.marks[name] = this.t;
    return this;
  }
}
