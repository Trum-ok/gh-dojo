import React from "react";
import { Easing, interpolate, useCurrentFrame, useVideoConfig } from "remotion";
import { BLOCKER_RESOLVED, Card, cards, columns, WAIT_AT } from "./script";
import { clamp } from "./timeline";
import type { Theme } from "./theme";

const COL_W = 174;
const COL_GAP = 8;
const CARD_H = 80;
const CARD_GAP = 8;
const HEADER_H = 40;
const BOARD_W = columns.length * COL_W + (columns.length - 1) * COL_GAP;
const BOARD_H = HEADER_H + 3 * (CARD_H + CARD_GAP) + 4;

const MOVE = 1.1; // seconds a card takes to cross columns
const SHIFT = 0.5; // seconds a card takes to close a gap in its column
const ARC = 56; // how high a card is lifted on its way, px

const ease = Easing.inOut(Easing.cubic);

const slotXY = (col: number, slot: number) => ({
  x: col * (COL_W + COL_GAP) + 6,
  y: HEADER_H + slot * (CARD_H + CARD_GAP),
});

// Where a card is at sec. A card that changes columns is picked up, carried
// along an arc and put down (lift 0..1); one that closes a gap slides.
const place = (card: Card, sec: number) => {
  let i = 0;
  while (i + 1 < card.moves.length && card.moves[i + 1].at <= sec) i++;
  const cur = card.moves[i];
  const prev = card.moves[Math.max(0, i - 1)];
  const across = cur.col !== prev.col;
  const p = interpolate(sec, [cur.at, cur.at + (across ? MOVE : SHIFT)], [0, 1], { ...clamp, easing: ease });
  const a = slotXY(prev.col, prev.slot);
  const b = slotXY(cur.col, cur.slot);
  // Lifted only while in flight: at p = 1, sin(p·π) is 1e-16, not 0.
  const moving = across && p > 0 && p < 1;
  const lift = moving ? Math.sin(p * Math.PI) : 0;
  return {
    x: a.x + (b.x - a.x) * p,
    y: a.y + (b.y - a.y) * p - ARC * lift,
    lift,
    moving,
    dir: Math.sign(b.x - a.x),
    col: cur.col,
  };
};

const IssueIcon: React.FC<{ color: string }> = ({ color }) => (
  <svg width="15" height="15" viewBox="0 0 16 16" fill={color}>
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
      fontSize: 12,
      fontWeight: 600,
      lineHeight: "18px",
      padding: "0 7px",
      borderRadius: 9,
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
  const { x, y, lift, moving, dir } = place(card, sec);
  const hero = card.id === "42";
  const blockedShown = hero && sec >= WAIT_AT;
  const resolved = sec >= BLOCKER_RESOLVED + MOVE;
  const chipIn = interpolate(sec, [WAIT_AT, WAIT_AT + 0.3], [0, 1], clamp);
  return (
    <div
      style={{
        position: "absolute",
        left: x,
        top: y,
        width: COL_W - 12,
        height: CARD_H,
        scale: String(1 + 0.04 * lift),
        rotate: `${2.5 * dir * lift}deg`,
        zIndex: moving ? 3 : hero ? 2 : 1,
      }}
    >
      {/* the shadow grows as the card is lifted */}
      <div
        style={{
          position: "absolute",
          inset: 0,
          borderRadius: 8,
          boxShadow: t.shadow,
          opacity: lift,
        }}
      />
      <div
        style={{
          position: "absolute",
          inset: 0,
          boxSizing: "border-box",
          padding: "9px 11px",
          borderRadius: 8,
          backgroundColor: t.inset,
          border: `1px solid ${hero ? t.accent : t.border}`,
          display: "flex",
          flexDirection: "column",
          gap: 4,
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 5, fontSize: 12, color: t.muted }}>
          <IssueIcon color={t.green} />
          acme/shop #{card.id}
        </div>
        <div style={{ fontSize: 15, fontWeight: 600, color: t.fg, whiteSpace: "nowrap" }}>{card.title}</div>
        <div style={{ display: "flex", gap: 5 }}>
          {/* the blocker takes the label's place: both do not fit */}
          {blockedShown ? null : <Chip color={t.muted}>{card.label}</Chip>}
          {blockedShown ? (
            <Chip color={resolved ? t.green : t.orange} opacity={chipIn}>
              {resolved ? "#12 ready ✓" : "blocked by #12"}
            </Chip>
          ) : null}
        </div>
      </div>
    </div>
  );
};

// The status of #42 at sec, as its card shows it.
export const statusAt = (sec: number) => columns[place(cards[0], sec).col].name;

// The board: four columns, the cards moving between them.
export const Board: React.FC<{ t: Theme }> = ({ t }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const sec = frame / fps;

  return (
      <div style={{ position: "relative", width: BOARD_W, height: BOARD_H, flexShrink: 0 }}>
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
                boxSizing: "border-box",
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
                  gap: 7,
                  paddingLeft: 11,
                  fontSize: 14,
                  fontWeight: 600,
                  color: t.fg,
                }}
              >
                <div style={{ width: 11, height: 11, borderRadius: 6, border: `2px solid ${t[c.color]}` }} />
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
  );
};
