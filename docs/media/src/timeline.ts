// A transcript of an agent session, laid out in time (seconds).

export type Kind = "user" | "agent" | "cmd" | "out" | "err";

export type Line = {
  kind: Kind;
  text: string;
  at: number; // the line starts to appear
  char: number; // seconds per character; 0 = all at once
};

export const USER = 0.045; // a person typing
export const CMD = 0.025; // an agent's command
export const AGENT = 0.012; // an agent's reply, streamed

export const lineEnd = (l: Line) => l.at + l.text.length * l.char;

// Builds a transcript: wait() moves the clock, line() adds a line at the
// current time and moves the clock to its end, mark() names a moment.
export class Timeline {
  t = 0;
  lines: Line[] = [];
  marks: Record<string, number> = {};

  wait(s: number) {
    this.t += s;
    return this;
  }

  line(kind: Kind, text: string, char = 0) {
    const l = { kind, text, at: this.t, char };
    this.lines.push(l);
    this.t = lineEnd(l);
    return this;
  }

  mark(name: string) {
    this.marks[name] = this.t;
    return this;
  }
}
