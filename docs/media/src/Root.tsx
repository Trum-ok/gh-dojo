import { Composition, Folder } from "remotion";
import { Compare } from "./Compare";
import { DURATION as COMPARE_DURATION } from "./compareScript";
import { Demo } from "./Demo";
import { DURATION as DEMO_DURATION } from "./script";

// Each scene in two themes: the README picks one with <picture> and
// prefers-color-scheme. The durations come from the scripts.
const FPS = 30;

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
          height={790}
          defaultProps={{ theme: "light" as const }}
        />
        <Composition
          id="DemoDark"
          component={Demo}
          durationInFrames={Math.round(DEMO_DURATION * FPS)}
          fps={FPS}
          width={1280}
          height={790}
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
          height={510}
          defaultProps={{ theme: "light" as const }}
        />
        <Composition
          id="CompareDark"
          component={Compare}
          durationInFrames={Math.round(COMPARE_DURATION * FPS)}
          fps={FPS}
          width={1280}
          height={510}
          defaultProps={{ theme: "dark" as const }}
        />
      </Folder>
    </>
  );
};
