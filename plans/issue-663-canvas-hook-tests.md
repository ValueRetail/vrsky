# Unit tests for the canvas hooks (#663)

Backlog item 153. The page test added in #654 loads `useNodeDrag`,
`useCanvasPersistence` and `useConnectionDrawing` for the first time and
covers them only partly. Global branch and function coverage had little room
above the thresholds (75 branches, 80 functions).

## Acceptance criteria

- Each hook has its own test file next to it, using Vitest and
  `renderHook`/`act`.
- The tests assert outcomes: positions after a drag, what the store holds and
  when it was written, which edge a drop creates.
- Failure paths: a failing localStorage write, a drawing that is cancelled
  (Escape, mouseup off a port), a drop on an invalid target (input port as
  start, output port as target, same node, duplicate edge).
- Global branch and function coverage go up from the baseline; no new
  coverage excludes; the hooks' code is not changed.

## Baseline (main at a9c92471)

Global: statements 85.63, branches 77.59, functions 82.67, lines 86.41.
`useNodeDrag` 42.9 / 0 / 33.3, `useConnectionDrawing` 30.8 / 3.6 / 30.8,
`useCanvasPersistence` 68 / 40 / 71.4 (statements / branches / functions).

## Files

New, all under `ui/src/hooks/`:

- `useNodeDrag.test.ts`
- `useConnectionDrawing.test.ts`
- `useCanvasPersistence.test.ts`

Also `plans/backlog.md`: strike item 153, add anything found.

## Approach

- `useNodeDrag`: pure function of its inputs; check the snapped position and
  that other nodes and the input array are untouched.
- `useConnectionDrawing`: fake Konva events, real window events, fake timers
  for the 10 ms global-mouseup delay.
- `useCanvasPersistence`: real `canvasStore` and real localStorage, `authStore`
  set directly; fake timers for the 2 s debounce.

## Risks

- Tests that pin current quirks would block a later fix. Where a behaviour looks
  accidental it goes in the backlog and is left out of the assertions.

## Verify

`npx tsc -b`, `npm run lint`, `npm run build`, `npx vitest run --coverage`.
