import React from "react";
import { useCurrentFrame, useVideoConfig } from "remotion";
import { Line, lineEnd } from "./timeline";
import type { Theme } from "./theme";

// A terminal window with an agent session in it: the user's request, the
// agent's replies, its commands and their output, typed out in time.

const Cursor: React.FC<{ visible: boolean; t: Theme; size: number }> = ({ visible, t, size }) => (
  <span
    style={{
      display: "inline-block",
      width: size * 0.55,
      height: size * 1.15,
      marginLeft: 1,
      verticalAlign: "text-bottom",
      backgroundColor: t.term.fg,
      opacity: visible ? 0.85 : 0,
    }}
  />
);

export type TranscriptProps = {
  t: Theme;
  mono: string;
  title: string;
  lines: Line[];
  fontSize: number;
  height: number; // of the text area
  indentTools?: boolean; // indent commands and their output under the replies
  format?: (text: string) => React.ReactNode; // colors an output line
};

export const Transcript: React.FC<TranscriptProps> = ({
  t,
  mono,
  title,
  lines,
  fontSize,
  height,
  indentTools,
  format,
}) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const sec = frame / fps;
  const blink = Math.floor(sec * 2) % 2 === 0;

  const shown = lines.filter((l) => l.at <= sec);
  const typing = shown.find((l) => sec < lineEnd(l));

  const prefix = (l: Line) => {
    switch (l.kind) {
      case "user":
        return <span style={{ color: t.term.ref, fontWeight: 700 }}>you › </span>;
      case "agent":
        return <span style={{ color: t.term.status, fontWeight: 700 }}>agent › </span>;
      case "cmd":
        return <span style={{ color: t.term.prompt, fontWeight: 700 }}>$ </span>;
      default:
        return null;
    }
  };

  const body = (l: Line) => {
    const text = l.char ? l.text.slice(0, Math.floor((sec - l.at) / l.char)) : l.text;
    if (l.kind === "err") return <span style={{ color: t.red }}>{text}</span>;
    if (l.kind === "out" && format) return format(text);
    return <span style={{ color: t.term.fg }}>{text}</span>;
  };

  const tool = (l: Line) => l.kind === "cmd" || l.kind === "out" || l.kind === "err";

  return (
    <div
      style={{
        borderRadius: 12,
        border: `1px solid ${t.border}`,
        backgroundColor: t.term.bg,
        overflow: "hidden",
        boxShadow: t.shadow,
      }}
    >
      <div
        style={{
          height: 36,
          display: "flex",
          alignItems: "center",
          gap: 8,
          paddingLeft: 16,
          backgroundColor: t.term.bar,
          borderBottom: `1px solid ${t.border}`,
          position: "relative",
        }}
      >
        {["#ff5f57", "#febc2e", "#28c840"].map((c) => (
          <div key={c} style={{ width: 12, height: 12, borderRadius: 6, backgroundColor: c }} />
        ))}
        <div
          style={{
            position: "absolute",
            inset: 0,
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            color: t.term.muted,
            fontSize: 15,
            fontWeight: 600,
          }}
        >
          {title}
        </div>
      </div>
      <div
        style={{
          fontFamily: mono,
          fontSize,
          lineHeight: 1.45,
          padding: "12px 20px",
          height,
          display: "flex",
          flexDirection: "column",
          justifyContent: "flex-start",
        }}
      >
        {shown.map((l, i) => {
          const gap = i > 0 && (l.kind === "user" || l.kind === "agent") ? fontSize * 0.5 : 0;
          return (
            <div
              key={i}
              style={{
                marginTop: gap,
                paddingLeft: indentTools && tool(l) ? "2ch" : 0,
                whiteSpace: "pre-wrap",
                overflowWrap: "break-word",
              }}
            >
              {prefix(l)}
              {body(l)}
              {typing === l ? <Cursor visible t={t} size={fontSize} /> : null}
            </div>
          );
        })}
        {typing ? null : (
          <div style={{ marginTop: fontSize * 0.5 }}>
            <Cursor visible={blink} t={t} size={fontSize} />
          </div>
        )}
      </div>
    </div>
  );
};
