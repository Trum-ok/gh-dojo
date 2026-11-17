import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame, useVideoConfig } from "remotion";
import { Board } from "./Board";
import { ChatPanel } from "./Chat";
import { mono, sans } from "./fonts";
import { confirmation } from "./format";
import { DURATION, msgs } from "./script";
import { themes } from "./theme";
import { Thread } from "./Thread";
import { clamp, FADE } from "./timeline";

export type DemoProps = { theme: "light" | "dark" };

// The main demo: the user asks the agent for an issue in a chat, the agent
// works it through gh dojo, and the board follows.
export const Demo: React.FC<DemoProps> = ({ theme }) => {
  const t = themes[theme];
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  // Fade in and out so the looping GIF does not jump back to the start.
  const opacity = interpolate(frame / fps, [0, FADE, DURATION - FADE, DURATION], [0, 1, 1, 0], clamp);

  return (
    <AbsoluteFill style={{ backgroundColor: t.canvas, fontFamily: sans }}>
      <AbsoluteFill style={{ padding: 28, gap: 20, flexDirection: "row", opacity }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <ChatPanel t={t} mono={mono} title="Agent · acme/shop" msgs={msgs} format={confirmation(t)} />
        </div>
        <div style={{ display: "flex", flexDirection: "column", gap: 16 }}>
          <Board t={t} />
          <Thread t={t} mono={mono} />
        </div>
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
