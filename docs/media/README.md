# README media

The animated media of the [README](../../README.md), made with
[Remotion](https://www.remotion.dev/), each in a light and a dark version:

- `demo-*.gif`, at the top: the user asks an agent to take #42; the agent runs
  `gh dojo start`, `wait` and `ready`, and the card moves on the board with a
  comment for each step. Story: `src/script.ts`; scene: `src/Demo.tsx`,
  `src/Board.tsx`.
- `compare-*.gif`, under "Why": asked to move #42, still in Backlog, to Ready,
  an agent with plain `gh` moves it silently, and an agent with gh dojo is
  refused and asks the user. Story: `src/compareScript.ts`; scene:
  `src/Compare.tsx`.

The command output, errors and comments in the stories are the text gh-dojo
prints and posts with the kanban preset, and `gh project item-edit` prints
nothing when stdout is not a terminal; keep them in step with the code.
`src/Transcript.tsx` draws an agent session, `src/theme.ts` has GitHub's light
and dark colors. `render.sh` renders all four GIFs (Remotion, then ffmpeg for
the palette); `make demo` in the repository root runs it.

Preview and edit in Remotion Studio:

```sh
npm ci
npm run dev
```

Remotion is free for individuals and teams of up to 3; see its
[license](https://www.remotion.dev/docs/license).
