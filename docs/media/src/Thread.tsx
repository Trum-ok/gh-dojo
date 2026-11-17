import React from "react";
import { Easing, interpolate, useCurrentFrame, useVideoConfig } from "remotion";
import { Avatar } from "./Avatar";
import { statusAt } from "./Board";
import { columns, Entry, thread } from "./script";
import { clamp } from "./timeline";
import type { Theme } from "./theme";

// Issue #42 as on GitHub: its title and state, and a timeline of what each
// verb did to it: the status change, the blocked-by link, the comment.
// New entries grow in at the bottom and push the older ones up.

const RAIL = 15; // x of the timeline's line, the middle of the icons and avatars

const Grow: React.FC<{ p: number; children: React.ReactNode }> = ({ p, children }) => (
  <div style={{ display: "grid", gridTemplateRows: `${p}fr`, opacity: p, flexShrink: 0 }}>
    <div style={{ minHeight: 0, overflow: "hidden" }}>{children}</div>
  </div>
);

const MoveIcon: React.FC<{ color: string }> = ({ color }) => (
  <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke={color} strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
    <path d="M2 8h11M9 4l4 4-4 4" />
  </svg>
);

const BlockIcon: React.FC<{ color: string }> = ({ color }) => (
  <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke={color} strokeWidth="1.6" strokeLinecap="round">
    <circle cx="8" cy="8" r="6" />
    <path d="M3.8 12.2l8.4-8.4" />
  </svg>
);

const IssueIcon: React.FC<{ color: string; size?: number }> = ({ color, size = 16 }) => (
  <svg width={size} height={size} viewBox="0 0 16 16" fill={color}>
    <path d="M8 9.5a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3Z" />
    <path d="M8 0a8 8 0 1 1 0 16A8 8 0 0 1 8 0ZM1.5 8a6.5 6.5 0 1 0 13 0 6.5 6.5 0 0 0-13 0Z" />
  </svg>
);

const statusColor = (t: Theme, name: string) => {
  const c = columns.find((c) => c.name === name);
  return c ? t[c.color] : t.muted;
};

const Status: React.FC<{ t: Theme; name: string }> = ({ t, name }) => (
  <b style={{ color: t.fg, fontWeight: 600 }}>
    <span
      style={{
        display: "inline-block",
        width: 9,
        height: 9,
        borderRadius: 5,
        border: `2px solid ${statusColor(t, name)}`,
        marginRight: 4,
        verticalAlign: "-1px",
      }}
    />
    {name}
  </b>
);

const Event: React.FC<{ t: Theme; icon: React.ReactNode; children: React.ReactNode }> = ({ t, icon, children }) => (
  <div style={{ display: "flex", alignItems: "center", gap: 10, padding: "3px 0" }}>
    <div
      style={{
        width: RAIL * 2,
        height: 26,
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        flexShrink: 0,
      }}
    >
      <div
        style={{
          width: 26,
          height: 26,
          borderRadius: 13,
          backgroundColor: t.panel,
          border: `1px solid ${t.border}`,
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
        }}
      >
        {icon}
      </div>
    </div>
    <div style={{ fontSize: 14, color: t.muted }}>
      <b style={{ color: t.fg }}>agent</b> {children} <span style={{ marginLeft: 2 }}>now</span>
    </div>
  </div>
);

const Comment: React.FC<{ t: Theme; mono: string; body: string }> = ({ t, mono, body }) => {
  const isCommit = /^[0-9a-f]{7}$/.test(body);
  return (
    <div style={{ display: "flex", gap: 10, padding: "3px 0" }}>
      <div style={{ width: RAIL * 2, flexShrink: 0 }}>
        <Avatar size={RAIL * 2} t={t} />
      </div>
      <div style={{ flex: 1, minWidth: 0, border: `1px solid ${t.border}`, borderRadius: 8, overflow: "hidden" }}>
        <div
          style={{
            fontSize: 13,
            color: t.muted,
            backgroundColor: t.panel,
            borderBottom: `1px solid ${t.border}`,
            padding: "5px 12px",
          }}
        >
          <b style={{ color: t.fg }}>agent</b> commented now
        </div>
        <div
          style={{
            fontSize: 15,
            backgroundColor: t.inset,
            padding: "7px 12px",
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

const entry = (e: Entry, t: Theme, mono: string) => {
  switch (e.kind) {
    case "moved":
      return (
        <Event t={t} icon={<MoveIcon color={t.muted} />}>
          moved this from <Status t={t} name={e.from} /> to <Status t={t} name={e.to} />
        </Event>
      );
    case "blocked":
      return (
        <Event t={t} icon={<BlockIcon color={t.orange} />}>
          marked this as blocked by <b style={{ color: t.accent }}>{e.by}</b>
        </Event>
      );
    case "comment":
      return <Comment t={t} mono={mono} body={e.body} />;
  }
};

export const Thread: React.FC<{ t: Theme; mono: string }> = ({ t, mono }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const sec = frame / fps;
  const status = statusAt(sec);

  return (
    <div
      style={{
        flex: 1,
        minHeight: 0,
        display: "flex",
        flexDirection: "column",
        borderRadius: 10,
        border: `1px solid ${t.border}`,
        backgroundColor: t.canvas,
        overflow: "hidden",
      }}
    >
      <div style={{ padding: "12px 16px 10px", borderBottom: `1px solid ${t.border}`, flexShrink: 0 }}>
        <div style={{ fontSize: 19, fontWeight: 600, color: t.fg }}>
          Add CSV export <span style={{ color: t.muted, fontWeight: 400 }}>#42</span>
        </div>
        <div style={{ display: "flex", alignItems: "center", gap: 12, marginTop: 7, fontSize: 13, color: t.muted }}>
          <span
            style={{
              display: "inline-flex",
              alignItems: "center",
              gap: 5,
              padding: "3px 10px",
              borderRadius: 13,
              backgroundColor: t.green,
              color: "#ffffff",
              fontWeight: 600,
            }}
          >
            <IssueIcon color="#ffffff" size={14} /> Open
          </span>
          <span>
            Shop roadmap · Status <Status t={t} name={status} />
          </span>
        </div>
      </div>
      <div
        style={{
          flex: 1,
          minHeight: 0,
          position: "relative",
          display: "flex",
          flexDirection: "column",
          justifyContent: "flex-end",
          padding: "8px 16px 10px",
          overflow: "hidden",
        }}
      >
        {/* the timeline's line, behind the icons */}
        <div style={{ position: "absolute", left: 16 + RAIL, top: 0, bottom: 0, width: 2, backgroundColor: t.border }} />
        <div style={{ position: "relative", display: "flex", flexDirection: "column", gap: 4 }}>
          {thread.map((e, i) => {
            if (sec < e.at) return null;
            const p = interpolate(sec, [e.at, e.at + 0.35], [0, 1], { ...clamp, easing: Easing.out(Easing.cubic) });
            return (
              <Grow key={i} p={p}>
                {entry(e, t, mono)}
              </Grow>
            );
          })}
        </div>
      </div>
    </div>
  );
};
