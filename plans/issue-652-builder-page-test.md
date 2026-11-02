# #652: page-level deploy test for the pipeline builder

## Problem

#580 pinned the builder's deploy wiring from `services/deployPipeline.test.ts`
(source pins and pure functions) because a test that renders
`ui/src/pages/PipelineBuilder.tsx` pushed global coverage under the gate: the
page is about 1,650 lines, and the throwaway page test measured 78.4 / 66.8 /
76.8 (statements / branches / functions) against 80 / 75 / 80. Backlog item
137 asked whether to exclude the page from `coverage.exclude`.

## Decision (Ludvik, 2026-11-02)

Exclude `src/pages/PipelineBuilder.tsx` and commit the page-level test. No
such test existed in a branch, a plan or the backlog text (searched `plans/`,
`git log --all`), so it is written here.

## Change (UI only)

- `ui/vitest.config.ts`: `coverage.exclude` gets `src/pages/PipelineBuilder.tsx`,
  with a comment pointing at #652 and the page-level test. Only that file.
  Thresholds unchanged.
- `ui/src/pages/PipelineBuilder.deploy.test.tsx` (new): the real page, the real
  canvas, UI and auth stores, the real deploy service. Replaced: the HTTP
  client (`@/services/api` default export), `workerEvents`, `dlqService`, and
  the canvas widgets (Konva canvas, palette, property editor, canvas tabs,
  DLQ and remote-agent panels, tenant selector). The Konva stand-in shows a
  button per node and calls `onNodeSelect`; the property editor stand-in calls
  `onUpdate` with a fixed config.
- `plans/backlog.md`: item 137 struck with `(#652)`.

## Acceptance criteria

- Deploy of an unlinked canvas: `POST /api/v1/connections` with the canvas's
  name, nodes (consumer/producer) and edges, then
  `POST /api/v1/connections/:id/start`, one success toast naming the id and
  the webhook URL, the green DEPLOYED bar, and the canvas linked to the new id.
- Deploy of a linked canvas: stop, `PUT` to the same id, start; no create.
- Start fails: error toast "Pipeline saved, not started" with the API's text,
  no success toast, red "SAVED, NOT STARTED" bar.
- Create fails: "Deployment failed" toast with the API's text, no start call.
- Unconfigured nodes: "Configure Input node before deploying" and no API
  call; after configuring both nodes through the editor the deploy goes
  through.
- Global coverage still meets 80 / 75 / 80 with the exclude in place.

## Mutation run

Each of these edits to `PipelineBuilder.tsx` makes at least one of the new
tests fail (all six did): a failed start treated as started; start never
called; config check skipped; linked id ignored (always create); deploy error
swallowed; status bar always green.

## Coverage

Excluding the page leaves the other files' numbers as they were. Rendering it
newly loads `useCanvasPersistence`, `useConnectionDrawing`, `useNodeDrag` (and
`CanvasSelector`, which the test mocks to keep it out), all partly covered, so
the global numbers move a little. Measured before and after in the PR.

## Not done

The panels the deploy opens after a start (file upload, HTTP/DB producer,
converter, filter, file manager, remote agent) and the canvas editing
(drag, edges) are not asserted; the stand-in widgets are the boundary of this
test.
