import React from "react";
import { Easing, interpolate, useCurrentFrame, useVideoConfig } from "remotion";
import { AGENT, clamp, Msg, shownAt, USER } from "./timeline";
import { Avatar } from "./Avatar";
import type { Theme } from "./theme";

// A chat with an agent: the user types into the input and sends a bubble; the
// agent answers in text and runs commands, shown as tool blocks with their
// output. New messages grow in at the bottom and push the older ones up.

// Grows its children from nothing to their full height: a one-row grid whose
// track is p × the content's height (p fr, with p ≤ 1).
const Grow: React.FC<{ p: number; children: React.ReactNode }> = ({ p, children }) => (
  <div style={{ display: "grid", gridTemplateRows: `${p}fr`, opacity: p, flexShrink: 0 }}>
    <div style={{ minHeight: 0, overflow: "hidden" }}>{children}</div>
  </div>
);

export type ChatProps = {
  t: Theme;
  mono: string;
  title: string;
  msgs: Msg[];
  fontSize?: number;
  codeSize?: number;
  clampOutput?: number; // at most this many lines of a command's output
  format?: (line: string) => React.ReactNode; // colors an output line
};

export const ChatPanel: React.FC<ChatProps> = ({
  t,
  mono,
  title,
  msgs,
  fontSize = 16,
  codeSize = 14,
  clampOutput,
  format,
}) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const sec = frame / fps;

  const typing = msgs.find(
    (m): m is Extract<Msg, { kind: "user" }> => m.kind === "user" && m.at <= sec && sec < m.sent,
  );
  const draft = typing ? typing.text.slice(0, Math.floor((sec - typing.at) / USER)) : "";

  const grow = (m: Msg) =>
    interpolate(sec, [shownAt(m), shownAt(m) + 0.35], [0, 1], { ...clamp, easing: Easing.out(Easing.cubic) });

  const bubble = (m: Extract<Msg, { kind: "user" }>) => (
    <div style={{ display: "flex", justifyContent: "flex-end", paddingLeft: 60 }}>
      <div
        style={{
          backgroundColor: t.accent,
          color: "#ffffff",
          fontSize,
          padding: "9px 14px",
          borderRadius: "16px 16px 4px 16px",
        }}
      >
        {m.text}
      </div>
    </div>
  );

  const reply = (m: Extract<Msg, { kind: "agent" }>) => (
    <div style={{ fontSize, color: t.fg, lineHeight: 1.4, padding: "4px 0" }}>
      {m.text.slice(0, Math.floor((sec - m.at) / AGENT))}
    </div>
  );

  const tool = (m: Extract<Msg, { kind: "tool" }>) => {
    const done = sec >= m.outAt;
    const dots = ".".repeat(1 + (Math.floor(sec * 4) % 3));
    return (
      <div
        style={{
          fontFamily: mono,
          fontSize: codeSize,
          lineHeight: 1.45,
          backgroundColor: t.term.bg,
          border: `1px solid ${t.border}`,
          borderRadius: 8,
          padding: "8px 12px",
          margin: "2px 0",
        }}
      >
        <div style={{ color: t.term.fg, overflowWrap: "break-word" }}>
          <span style={{ color: t.term.prompt, fontWeight: 700 }}>$ </span>
          {m.cmd}
        </div>
        <div
          style={{
            marginTop: 3,
            color: done ? (m.err ? t.red : t.term.muted) : t.term.muted,
            overflowWrap: "anywhere",
            ...(clampOutput
              ? { display: "-webkit-box", WebkitLineClamp: clampOutput, WebkitBoxOrient: "vertical", overflow: "hidden" }
              : {}),
          }}
        >
          {done ? m.out.map((l, i) => <div key={i}>{format && !m.err ? format(l) : l}</div>) : `running${dots}`}
        </div>
      </div>
    );
  };

  return (
    <div
      style={{
        height: "100%",
        display: "flex",
        flexDirection: "column",
        borderRadius: 12,
        border: `1px solid ${t.border}`,
        backgroundColor: t.panel,
        overflow: "hidden",
        boxShadow: t.shadow,
      }}
    >
      <div
        style={{
          height: 44,
          flexShrink: 0,
          display: "flex",
          alignItems: "center",
          gap: 10,
          padding: "0 16px",
          borderBottom: `1px solid ${t.border}`,
          fontSize: 15,
          fontWeight: 600,
          color: t.fg,
        }}
      >
        <div style={{ width: 9, height: 9, borderRadius: 5, backgroundColor: t.green }} />
        {title}
      </div>

      <div
        style={{
          flex: 1,
          minHeight: 0,
          display: "flex",
          flexDirection: "column",
          justifyContent: "flex-end",
          gap: 10,
          padding: "14px 16px",
          overflow: "hidden",
        }}
      >
        {msgs.map((m, i) => {
          if (sec < shownAt(m)) return null;
          if (m.kind === "user") return <Grow key={i} p={grow(m)}>{bubble(m)}</Grow>;
          const prev = msgs[i - 1];
          const first = !prev || prev.kind === "user";
          return (
            <Grow key={i} p={grow(m)}>
              <div style={{ display: "flex", gap: 10 }}>
                <div style={{ visibility: first ? "visible" : "hidden" }}>
                  <Avatar size={30} t={t} />
                </div>
                <div style={{ flex: 1, minWidth: 0 }}>{m.kind === "agent" ? reply(m) : tool(m)}</div>
              </div>
            </Grow>
          );
        })}
      </div>

      <div style={{ padding: "0 16px 14px", flexShrink: 0 }}>
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 10,
            border: `1px solid ${typing ? t.accent : t.border}`,
            borderRadius: 22,
            backgroundColor: t.inset,
            padding: "8px 8px 8px 16px",
            fontSize,
          }}
        >
          <div style={{ flex: 1, color: draft ? t.fg : t.muted, whiteSpace: "nowrap", overflow: "hidden" }}>
            {draft || "Message the agent"}
            {typing ? (
              <span
                style={{
                  display: "inline-block",
                  width: 2,
                  height: fontSize * 1.1,
                  marginLeft: 1,
                  verticalAlign: "text-bottom",
                  backgroundColor: t.fg,
                }}
              />
            ) : null}
          </div>
          <div
            style={{
              width: 30,
              height: 30,
              borderRadius: 15,
              backgroundColor: draft ? t.accent : t.border,
              color: "#ffffff",
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              fontSize: 17,
              fontWeight: 700,
            }}
          >
            ↑
          </div>
        </div>
      </div>
    </div>
  );
};
