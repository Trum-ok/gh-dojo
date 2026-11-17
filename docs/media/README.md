# README media

The animated media of the [README](../../README.md), made with
[Remotion](https://www.remotion.dev/), each in a light and a dark version:

- `demo-*.gif`, at the top: the user asks an agent to take #42; the agent runs
  `gh dojo start`, `wait` and `ready`, and the card moves on the board with a
  comment for each step. Story: `src/script.ts`; scene: `src/Demo.tsx`,
  `src/Board.tsx`, `src/Thread.tsx` (the issue's timeline).
- `compare-*.gif`, under "Why": told that #42 is stuck until #12 lands, an
  agent with plain `gh` writes GraphQL and carries node IDs between calls, and
  an agent with gh dojo runs one verb. Story: `src/compareScript.ts`; scene:
  `src/Compare.tsx`.

Not Remotion: `bench-workday-*.svg`, under "Benchmark", is drawn by the
benchmark from its results, `go run ./bench chart bench/out/agents-*/results.jsonl`
(see [bench/doc.go](../../bench/doc.go)).

Not Remotion either: the logo, a torii of dots on a 16×16 grid with an ema
plaque between its pillars. `logo-light.svg` and `logo-dark.svg` sit in the
README heading; the dots of the pillars shrink over the bottom six rows.
`social-preview-dark.png` (1280×640) is the repository's social preview,
uploaded by hand in the repository's Settings; a link card stands out better
dark than light in both themes. `social-preview-light.png` is the light one.

The gh-dojo output and comments in the stories are the text gh-dojo prints and
posts with the kanban preset and a [`[session]`](../configuration.md#session)
link (in `start`'s comment), and the GraphQL mutations are the ones it sends;
keep them in step with the code. `src/Chat.tsx` draws the chat with the agent,
`src/theme.ts` has GitHub's light and dark colors. `render.sh` renders all
four GIFs (lossless frames from Remotion, then ffmpeg for the palette); `make
demo` in the repository root runs it.

Preview and edit in Remotion Studio:

```sh
npm ci
npm run dev
```

Remotion is free for individuals and teams of up to 3; see its
[license](https://www.remotion.dev/docs/license).
