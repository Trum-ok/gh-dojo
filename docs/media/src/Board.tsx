import React from "react";
import { Easing, interpolate, useCurrentFrame, useVideoConfig } from "remotion";
import { BLOCKER_RESOLVED, Card, cards, columns, comments, WAIT_AT } from "./script";
import type { Theme } from "./theme";

export const COL_W = 214;
const COL_GAP = 12;
const CARD_H = 84;
const CARD_GAP = 8;
const HEADER_H = 40;
const MOVE = 0.7; // seconds a card takes to move

const ease = Easing.bezier(0.65, 0, 0.35, 1);

const slotXY = (col: number, slot: number) => ({
  x: col * (COL_W + COL_GAP) + 8,
  y: HEADER_H + slot * (CARD_H + CARD_GAP),
});

// Where a card is at sec, and how far it is lifted (0..1) while moving.
const place = (card: Card, sec: number) => {
  let i = 0;
  while (i + 1 < card.moves.length && card.moves[i + 1].at <= sec) i++;
  const cur = card.moves[i];
  const prev = card.moves[Math.max(0, i - 1)];
  const p = interpolate(sec, [cur.at, cur.at + MOVE], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: ease,
  });
  const a = slotXY(prev.col, prev.slot);
  const b = slotXY(cur.col, cur.slot);
  return {
    x: a.x + (b.x - a.x) * p,
    y: a.y + (b.y - a.y) * p,
    lift: cur.col !== prev.col ? Math.sin(p * Math.PI) : 0,
    col: cur.col,
  };
};

const IssueIcon: React.FC<{ color: string }> = ({ color }) => (
  <svg width="16" height="16" viewBox="0 0 16 16" fill={color}>
    <path d="M8 9.5a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3Z" />
    <path d="M8 0a8 8 0 1 1 0 16A8 8 0 0 1 8 0ZM1.5 8a6.5 6.5 0 1 0 13 0 6.5 6.5 0 0 0-13 0Z" />
  </svg>
);

const Chip: React.FC<{ color: string; children: React.ReactNode; opacity?: number }> = ({
  color,
  children,
  opacity = 1,
}) => (
  <span
    style={{
      fontSize: 13,
      fontWeight: 600,
      lineHeight: "20px",
      padding: "0 8px",
      borderRadius: 10,
      border: `1px solid ${color}`,
      color,
      opacity,
      whiteSpace: "nowrap",
    }}
  >
    {children}
  </span>
);

const CardView: React.FC<{ card: Card; t: Theme; sec: number }> = ({ card, t, sec }) => {
  const { x, y, lift } = place(card, sec);
  const hero = card.id === "42";
  const blockedShown = hero && sec >= WAIT_AT;
  const resolved = sec >= BLOCKER_RESOLVED + MOVE;
  const chipIn = interpolate(sec, [WAIT_AT, WAIT_AT + 0.3], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });
  return (
    <div
      style={{
        position: "absolute",
        left: x,
        top: y,
        width: COL_W - 16,
        height: CARD_H,
        boxSizing: "border-box",
        padding: "10px 12px",
        borderRadius: 8,
        backgroundColor: t.inset,
        border: `1px solid ${hero ? t.accent : t.border}`,
        boxShadow: lift > 0.01 ? t.shadow : "none",
        scale: String(1 + 0.05 * lift),
        rotate: `${-1.5 * lift}deg`,
        zIndex: hero ? 2 : 1,
        display: "flex",
        flexDirection: "column",
        gap: 4,
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 13, color: t.muted }}>
        <IssueIcon color={t.green} />
        acme/shop #{card.id}
      </div>
      <div style={{ fontSize: 16, fontWeight: 600, color: t.fg, whiteSpace: "nowrap" }}>{card.title}</div>
      <div style={{ display: "flex", gap: 6 }}>
        <Chip color={t.muted}>{card.label}</Chip>
        {blockedShown ? (
          <Chip color={resolved ? t.green : t.orange} opacity={chipIn}>
            {resolved ? "#12 ready ✓" : "blocked by #12"}
          </Chip>
        ) : null}
      </div>
    </div>
  );
};

const Comment: React.FC<{ body: string; at: number; t: Theme; sec: number; mono: string }> = ({
  body,
  at,
  t,
  sec,
  mono,
}) => {
  const p = interpolate(sec, [at, at + 0.35], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
    easing: Easing.out(Easing.cubic),
  });
  if (p === 0) return null;
  const isCommit = /^[0-9a-f]{7}$/.test(body);
  return (
    <div style={{ display: "flex", gap: 10, opacity: p, translate: `0 ${(1 - p) * 12}px` }}>
      <div
        style={{
          width: 28,
          height: 28,
          flexShrink: 0,
          borderRadius: 14,
          backgroundColor: t.accent,
          color: t.canvas,
          fontSize: 14,
          fontWeight: 700,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
        }}
      >
        a
      </div>
      <div
        style={{
          flex: 1,
          minWidth: 0,
          border: `1px solid ${t.border}`,
          borderRadius: 8,
          backgroundColor: t.inset,
          padding: "6px 10px",
        }}
      >
        <div style={{ fontSize: 12, color: t.muted }}>
          <b style={{ color: t.fg }}>agent</b> commented now
        </div>
        <div
          style={{
            fontSize: 14,
            color: isCommit ? t.accent : t.fg,
            fontFamily: isCommit ? mono : undefined,
            overflowWrap: "anywhere",
          }}
        >
          {body}
        </div>
      </div>
    </div>
  );
};

export const Board: React.FC<{ t: Theme; mono: string }> = ({ t, mono }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const sec = frame / fps;
  const status = columns[place(cards[0], sec).col].name;

  return (
    <div style={{ display: "flex", gap: 20 }}>
      <div
        style={{
          position: "relative",
          width: columns.length * COL_W + (columns.length - 1) * COL_GAP,
          height: HEADER_H + 3 * (CARD_H + CARD_GAP) + 4,
        }}
      >
        {columns.map((c, i) => {
          const count = cards.filter((card) => place(card, sec).col === i).length;
          return (
            <div
              key={c.name}
              style={{
                position: "absolute",
                left: i * (COL_W + COL_GAP),
                top: 0,
                bottom: 0,
                width: COL_W,
                borderRadius: 10,
                backgroundColor: t.panel,
                border: `1px solid ${t.border}`,
              }}
            >
              <div
                style={{
                  height: HEADER_H - 6,
                  display: "flex",
                  alignItems: "center",
                  gap: 8,
                  paddingLeft: 12,
                  fontSize: 15,
                  fontWeight: 600,
                  color: t.fg,
                }}
              >
                <div style={{ width: 12, height: 12, borderRadius: 6, border: `2px solid ${t[c.color]}` }} />
                {c.name}
                <span style={{ color: t.muted, fontWeight: 400 }}>{count}</span>
              </div>
            </div>
          );
        })}
        {cards.map((card) => (
          <CardView key={card.id} card={card} t={t} sec={sec} />
        ))}
      </div>

      <div
        style={{
          flex: 1,
          borderRadius: 10,
          border: `1px solid ${t.border}`,
          backgroundColor: t.panel,
          padding: "12px 14px",
          display: "flex",
          flexDirection: "column",
          gap: 10,
          overflow: "hidden",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <IssueIcon color={t.green} />
          <span style={{ fontSize: 16, fontWeight: 600, color: t.fg }}>
            Add CSV export <span style={{ color: t.muted, fontWeight: 400 }}>#42</span>
          </span>
        </div>
        <div style={{ fontSize: 13, color: t.muted }}>
          Status: <b style={{ color: t.fg }}>{status}</b>
        </div>
        {comments.map((c) => (
          <Comment key={c.body} body={c.body} at={c.at + 0.15} t={t} sec={sec} mono={mono} />
        ))}
      </div>
    </div>
  );
};
