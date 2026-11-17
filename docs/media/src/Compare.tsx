import React from "react";
import { AbsoluteFill, Easing, interpolate, useCurrentFrame, useVideoConfig } from "remotion";
import { ChatPanel } from "./Chat";
import { dojoSide, DURATION, ghSide } from "./compareScript";
import { mono, sans } from "./fonts";
import { confirmation } from "./format";
import { Theme, themes } from "./theme";
import { clamp, FADE } from "./timeline";

const Verdict: React.FC<{ t: Theme; sec: number; at: number; good: boolean; children: React.ReactNode }> = ({
  t,
  sec,
  at,
  good,
  children,
}) => {
  const p = interpolate(sec, [at, at + 0.4], [0, 1], { ...clamp, easing: Easing.out(Easing.cubic) });
  return (
    <div
      style={{
        opacity: p,
        translate: `0 ${(1 - p) * 8}px`,
        display: "flex",
        alignItems: "center",
        gap: 10,
        fontSize: 17,
        color: t.fg,
        height: 28,
      }}
    >
      <span
        style={{
          width: 24,
          height: 24,
          flexShrink: 0,
          borderRadius: 12,
          backgroundColor: good ? t.green : t.orange,
          color: t.canvas,
          fontWeight: 700,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
          fontSize: 15,
        }}
      >
        {good ? "✓" : "!"}
      </span>
      {children}
    </div>
  );
};

export type CompareProps = { theme: "light" | "dark" };

// The same request to an agent with plain gh and to one with gh dojo.
export const Compare: React.FC<CompareProps> = ({ theme }) => {
  const t = themes[theme];
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const sec = frame / fps;
  const opacity = interpolate(sec, [0, FADE, DURATION - FADE, DURATION], [0, 1, 1, 0], clamp);

  const sides = [
    {
      title: "Agent with gh",
      side: ghSide,
      good: false,
      verdict: "Hand-written GraphQL and node IDs; no comment on why it waits",
    },
    {
      title: "Agent with gh dojo",
      side: dojoSide,
      good: true,
      verdict: "One verb: status, blocked-by link and comment, as configured",
    },
  ];

  return (
    <AbsoluteFill style={{ backgroundColor: t.canvas, fontFamily: sans }}>
      <AbsoluteFill style={{ padding: 28, gap: 24, flexDirection: "row", opacity }}>
        {sides.map((s) => (
          <div key={s.title} style={{ flex: 1, minWidth: 0, display: "flex", flexDirection: "column", gap: 14 }}>
            <div style={{ flex: 1, minHeight: 0 }}>
              <ChatPanel
                t={t}
                mono={mono}
                title={s.title}
                msgs={s.side.msgs}
                fontSize={15}
                codeSize={13}
                clampOutput={2}
                format={confirmation(t)}
              />
            </div>
            <Verdict t={t} sec={sec} at={s.side.verdict} good={s.good}>
              {s.verdict}
            </Verdict>
          </div>
        ))}
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
