# #580: a failed start after Deploy still says "deployed and running!"

## Problem

`ui/src/pages/PipelineBuilder.tsx` ran `POST /connections/:id/start` in a
`try` whose `catch` only logged to the console. Whatever the API answered,
the builder then showed the success toast "Pipeline … deployed and running!"
and the green DEPLOYED bar. A pipeline that was saved but never started
looked fine.

## Change (UI only, `ui/src`)

- `services/deployPipeline.ts`: `startDeployed(client, id)` posts the start
  and returns `{ started: true }` or `{ started: false, message }`, with the
  message from `getErrorMessage` (the API's own text). It never throws, and
  never drops the error.
- `services/deployPipeline.ts`: `startOutcome(id, start, extras)` is the pure
  decision: the toast (type, title, message) and the status bar (label, tone).
  An empty API message reads "the API gave no reason".
- `pages/PipelineBuilder.tsx`: after the deploy (create or update) the page
  calls `startDeployed`.
  - Started: the success toast as before.
  - Not started: an error toast "Pipeline saved, not started" with the
    API's message, and no success toast.
  - `deploymentInfo` gets `started`; the bar under the canvas reads
    "SAVED, NOT STARTED" (red) instead of "DEPLOYED" (green). The test
    panels stay, since the pipeline is saved and can be started from the
    connections list.
- The deploy itself is unchanged: create/update errors still go to the
  "Deployment failed" toast.

## Acceptance criteria

- A failed start shows an error toast with the API's message, not "running".
  The toast says the pipeline was saved.
- The status bar shows the real state after a failed start.
- Tests that fail before the fix, in `services/deployPipeline.test.ts`:
  `startDeployed` returns the API's message for a rejected start;
  `startOutcome` gives the toast and bar for success, an API error and an
  empty message; source pins check that the builder's deploy function goes
  through `startDeployed` and `startOutcome`, has no success text of its own
  and renders the bar from the outcome.
  The page is not rendered in the suite: it is a ~2000 line component the
  coverage gate keeps out, and importing it into a test drops the global
  thresholds below 80/75 (measured: statements 78%, branches 67%). The same
  scenario (real page, real stores, start call rejecting) was run once as a
  throwaway test, failing before the fix and passing after; it is not
  committed for that reason.

## Touches

`ui/src/pages/PipelineBuilder.tsx`, `ui/src/services/deployPipeline.ts` and
their tests. No Go, no API change.

## Not done

The bar reflects the result of this deploy's start call. It does not poll the
connection afterwards; the connections list and detail pages already show the
live status.
