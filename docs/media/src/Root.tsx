import { Composition, Folder } from "remotion";
import { Compare } from "./Compare";
import { DURATION as COMPARE_DURATION } from "./compareScript";
import { Demo } from "./Demo";
import { DURATION as DEMO_DURATION } from "./script";

// Each scene in two themes: the README picks one with <picture> and
// prefers-color-scheme. The durations come from the scripts.
const FPS = 25; // GIFs play it as is: 4 hundredths of a second a frame, none dropped

export const RemotionRoot: React.FC = () => {
  return (
    <>
      <Folder name="Demo">
        <Composition
          id="DemoLight"
          component={Demo}
          durationInFrames={Math.round(DEMO_DURATION * FPS)}
          fps={FPS}
          width={1280}
          height={720}
          defaultProps={{ theme: "light" as const }}
        />
        <Composition
          id="DemoDark"
          component={Demo}
          durationInFrames={Math.round(DEMO_DURATION * FPS)}
          fps={FPS}
          width={1280}
          height={720}
          defaultProps={{ theme: "dark" as const }}
        />
      </Folder>
      <Folder name="Compare">
        <Composition
          id="CompareLight"
          component={Compare}
          durationInFrames={Math.round(COMPARE_DURATION * FPS)}
          fps={FPS}
          width={1280}
          height={600}
          defaultProps={{ theme: "light" as const }}
        />
        <Composition
          id="CompareDark"
          component={Compare}
          durationInFrames={Math.round(COMPARE_DURATION * FPS)}
          fps={FPS}
          width={1280}
          height={600}
          defaultProps={{ theme: "dark" as const }}
        />
      </Folder>
    </>
  );
};
