import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame, useVideoConfig } from "remotion";
import { Board } from "./Board";
import { mono, sans } from "./fonts";
import { confirmation } from "./format";
import { DURATION, FADE, lines } from "./script";
import { themes } from "./theme";
import { Transcript } from "./Transcript";

export type DemoProps = { theme: "light" | "dark" };

// The main demo: the user asks for an issue, the agent works it through
// gh dojo, and the board follows.
export const Demo: React.FC<DemoProps> = ({ theme }) => {
  const t = themes[theme];
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  // Fade in and out so the looping GIF does not jump back to the start.
  const opacity = interpolate(frame / fps, [0, FADE, DURATION - FADE, DURATION], [0, 1, 1, 0], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });

  return (
    <AbsoluteFill style={{ backgroundColor: t.canvas, fontFamily: sans }}>
      <AbsoluteFill style={{ padding: 28, gap: 22, opacity }}>
        <Transcript
          t={t}
          mono={mono}
          title="agent session · acme/shop"
          lines={lines}
          fontSize={20}
          height={350}
          indentTools
          format={confirmation(t)}
        />
        <Board t={t} mono={mono} />
      </AbsoluteFill>
    </AbsoluteFill>
  );
};
