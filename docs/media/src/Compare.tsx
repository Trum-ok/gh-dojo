import React from "react";
import { AbsoluteFill, Easing, interpolate, useCurrentFrame, useVideoConfig } from "remotion";
import { dojoSide, DURATION, FADE, ghSide, request } from "./compareScript";
import { mono, sans } from "./fonts";
import { columns } from "./script";
import { Theme, themes } from "./theme";
import { Transcript } from "./Transcript";

const clamp = { extrapolateLeft: "clamp", extrapolateRight: "clamp" } as const;

const COL_W = 141;
const COL_GAP = 8;

// A strip of the board with only #42 on it.
const MiniBoard: React.FC<{ t: Theme; sec: number; movedAt?: number; refusedAt?: number }> = ({
  t,
  sec,
  movedAt,
  refusedAt,
}) => {
  const p = movedAt === undefined ? 0 : interpolate(sec, [movedAt, movedAt + 0.7], [0, 1], { ...clamp, easing: Easing.bezier(0.65, 0, 0.35, 1) });
  const lift = p > 0 && p < 1 ? Math.sin(p * Math.PI) : 0;
  const shake =
    refusedAt === undefined ? 0 : interpolate(sec, [refusedAt, refusedAt + 0.5], [1, 0], clamp) * Math.sin((sec - refusedAt) * 40);
  const refused = refusedAt !== undefined && sec >= refusedAt;
  return (
    <div style={{ position: "relative", height: 104 }}>
      {columns.map((c, i) => (
        <div
          key={c.name}
          style={{
            position: "absolute",
            left: i * (COL_W + COL_GAP),
            width: COL_W,
            top: 0,
            bottom: 0,
            borderRadius: 8,
            backgroundColor: t.panel,
            border: `1px solid ${t.border}`,
            padding: "7px 10px",
            boxSizing: "border-box",
            fontSize: 13,
            fontWeight: 600,
            color: t.fg,
            display: "flex",
            gap: 6,
            alignItems: "flex-start",
          }}
        >
          <div style={{ width: 10, height: 10, marginTop: 3, borderRadius: 5, border: `2px solid ${t[c.color]}` }} />
          {c.name}
        </div>
      ))}
      <div
        style={{
          position: "absolute",
          top: 34,
          left: 6 + 3 * (COL_W + COL_GAP) * p,
          width: COL_W - 12,
          height: 60,
          boxSizing: "border-box",
          padding: "7px 10px",
          borderRadius: 7,
          backgroundColor: t.inset,
          border: `1px solid ${refused ? t.red : t.accent}`,
          boxShadow: lift > 0.01 ? t.shadow : "none",
          scale: String(1 + 0.05 * lift),
          rotate: `${-1.5 * lift + 2 * shake}deg`,
          translate: `${6 * shake}px 0`,
        }}
      >
        <div style={{ fontSize: 12, color: t.muted }}>acme/shop #42</div>
        <div style={{ fontSize: 14, fontWeight: 600, color: t.fg, whiteSpace: "nowrap" }}>Add CSV export</div>
      </div>
    </div>
  );
};

const Verdict: React.FC<{ t: Theme; sec: number; at: number; good: boolean; children: React.ReactNode }> = ({
  t,
  sec,
  at,
  good,
  children,
}) => {
  const p = interpolate(sec, [at, at + 0.35], [0, 1], { ...clamp, easing: Easing.out(Easing.cubic) });
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
      }}
    >
      <span
        style={{
          width: 24,
          height: 24,
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
  const typed = request.text.slice(0, Math.max(0, Math.floor((sec - request.at) / request.char)));

  return (
    <AbsoluteFill style={{ backgroundColor: t.canvas, fontFamily: sans }}>
      <AbsoluteFill style={{ padding: 28, gap: 18, opacity }}>
        <div
          style={{
            alignSelf: "center",
            fontFamily: mono,
            fontSize: 22,
            padding: "10px 22px",
            minWidth: 520,
            borderRadius: 24,
            border: `1px solid ${t.border}`,
            backgroundColor: t.panel,
            color: t.fg,
          }}
        >
          <span style={{ color: t.term.ref, fontWeight: 700 }}>you › </span>
          {typed}
        </div>
        <div style={{ display: "flex", gap: 24 }}>
          {[
            {
              title: "agent with gh",
              side: ghSide,
              board: <MiniBoard t={t} sec={sec} movedAt={ghSide.moved} />,
              verdict: (
                <Verdict t={t} sec={sec} at={ghSide.verdict} good={false}>
                  Backlog → Ready, skipping In progress, and no comment
                </Verdict>
              ),
            },
            {
              title: "agent with gh dojo",
              side: dojoSide,
              board: <MiniBoard t={t} sec={sec} refusedAt={dojoSide.refused} />,
              verdict: (
                <Verdict t={t} sec={sec} at={dojoSide.verdict} good>
                  Refused with exit code 2: the agent asks you
                </Verdict>
              ),
            },
          ].map((s) => (
            <div key={s.title} style={{ flex: 1, display: "flex", flexDirection: "column", gap: 14 }}>
              <Transcript t={t} mono={mono} title={s.title} lines={s.side.lines} fontSize={16} height={196} />
              {s.board}
              {s.verdict}
            </div>
          ))}
        </div>
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
