import React from "react";
import type { Theme } from "./theme";

// The agent's avatar, the same in the chat and on the issue: a robot head on
// the purple gh dojo prints statuses in.
export const Avatar: React.FC<{ size: number; t: Theme }> = ({ size, t }) => (
  <div
    style={{
      width: size,
      height: size,
      flexShrink: 0,
      borderRadius: size / 2,
      backgroundColor: t.term.status,
      display: "flex",
      alignItems: "center",
      justifyContent: "center",
    }}
  >
    <svg width={size * 0.66} height={size * 0.66} viewBox="0 0 24 24" fill="none">
      <line x1="12" y1="2.8" x2="12" y2="6" stroke="#fff" strokeWidth="1.8" strokeLinecap="round" />
      <circle cx="12" cy="2.6" r="1.4" fill="#fff" />
      <rect x="4.5" y="6.5" width="15" height="12" rx="3.8" stroke="#fff" strokeWidth="1.9" />
      <circle cx="9.4" cy="12.3" r="1.6" fill="#fff" />
      <circle cx="14.6" cy="12.3" r="1.6" fill="#fff" />
      <line x1="2.5" y1="11" x2="2.5" y2="14" stroke="#fff" strokeWidth="1.8" strokeLinecap="round" />
      <line x1="21.5" y1="11" x2="21.5" y2="14" stroke="#fff" strokeWidth="1.8" strokeLinecap="round" />
    </svg>
  </div>
);
