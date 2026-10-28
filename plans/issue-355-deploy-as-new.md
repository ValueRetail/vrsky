# Builder: "Deploy as new" after a pipeline was deleted on the server (#355)

## Goal

Find out whether the builder's "Deploy as new" button actually creates a new
pipeline once the canvas's deployed connection has been deleted on the
server, and fix it if it doesn't.

## Suspected bug

`deployPipeline` in `ui/src/pages/PipelineBuilder.tsx` catches
`ConnectionGone`, unlinks the canvas (`setDeployedConnectionId(id, null)`)
and opens a confirm dialog. Its `onConfirm` calls `deployPipeline()` again,
but that is the function object from the render that started the deploy. Its
closure still holds the `activeCanvas` with the old `deployedConnectionId`,
so the second call would:

- build an update-style payload (`deployIdentity` sees a link: no
  description, maybe no name), and
- pass the deleted id to `deployPipelineSequence`, which stops and PUTs it,
  gets 404 again and reopens the same dialog.

## Acceptance criteria (from the issue)

1. A test reproduces the flow: a canvas linked to a connection whose stop and
   PUT return 404, the user confirms "Deploy as new", and the test asserts
   what reaches the API.
2. If the bug is real: after confirming, exactly one
   `POST /api/v1/connections`, no further stop or PUT on the deleted id, the
   canvas links to the new id, and the payload's name follows
   `deployIdentity` for a create (tab name without `Edit: `, plus the
   editor's description). If the flow already works: keep the test, say so in
   the PR, change nothing else.
3. Full UI suite, `npx tsc -b`, lint and `npm run build` pass; coverage
   thresholds hold.

## Files

| File | Change |
|---|---|
| `ui/src/services/deployPipeline.ts` | `deployPlan`, `deployCanvas` (see Outcome) |
| `ui/src/services/deployPipeline.test.ts` | the reproduction and the wiring checks (test author) |
| `ui/src/pages/PipelineBuilder.tsx` | the fix, only if the test fails |
| `plans/backlog.md` | strike item 4 |

## Approach

1. An independent test author writes the reproduction from the criteria
   alone. Preferred: render `PipelineBuilder` with the heavy children
   (`KonvaCanvas`, panels) mocked, the API client mocked at
   `services/api`, the canvas store seeded with a linked, valid canvas; click
   Deploy, confirm through `useUIStore`'s dialog, assert the calls. Fallback
   if the page cannot render in jsdom: a small extracted unit that the page
   uses, pinned by a source check like the one in
   `services/deployPipeline.test.ts`.
2. Run it against today's code.
3. If it fails, the fix: give `deployPipeline` an explicit "deploy as new"
   intent. The confirm handler calls it with that intent, which builds the
   payload from the canvas with its link removed (so `deployIdentity` takes
   the create branch) and passes no previous id to the deploy sequence. The
   Deploy button calls it without the intent (it must stop passing the click
   event as the first argument). Nothing else changes: the first deploy,
   redeploys and the cancel path behave as before.

   Alternative considered: read the active canvas fresh from the store inside
   the handler. It also works, but makes the page reach past
   `useCanvasPersistence` into the store, and the result depends on the
   unlink having been written before the user clicks. The explicit intent says
   what the button means.

## Outcome

The bug is real. The test author's page test (real `PipelineBuilder`, real
deploy sequence, API client and canvas component mocked) failed on main:
after "Deploy as new" the page sent a second `POST …/gone-1/stop`, made no
create, and opened the same dialog again.

The page test cannot stay in the suite. Importing `PipelineBuilder.tsx`
adds its 628 statements and 636 branches to the coverage report (before,
only its source was read as `?raw`, so it counted 0/0). The test covers
about 40% of them, and global coverage fell to 73/60/71/76 against CI's
80/75/80/80. Changing the coverage config to fit is off the table. So, the
fallback from the brief:

- `deployPlan(canvas, asNew, now)` in `services/deployPipeline.ts` decides
  which id a deploy updates and what name and description it sends; with
  `asNew` the canvas counts as unlinked.
- `deployCanvas(client, payload, previousConnectionId, link)` runs the
  sequence and keeps the link in step (link after a create or a rename,
  unlink on `ConnectionGone`). This is the code the page had inline.
- The page calls both. The confirm handler calls `deployPipeline(true)`,
  and the Deploy button calls `deployPipeline()` without the click event.
- Unit tests drive the flow through these two functions. Source checks pin
  the page's wiring, as the existing one in `deployPipeline.test.ts` already
  did. The old check moves from `...deployIdentity(` to `...identity` from
  `deployPlan`.
- The page test passed against the fix as an extra check outside the tree.

## Risks

- `onClick={deployPipeline}` passes the click event; with a new parameter that
  would be read as the intent. The button must call it with no argument.
- A page test that renders the whole builder may be brittle. Mocks stay at
  module boundaries (API client, canvas component) so the deploy code under
  test is the real one.

## Verify

From `ui/`: `npx tsc -b`, `npx eslint` on the touched files, `npm run build`,
`npx vitest run --coverage` (lines/functions/statements 80, branches 75).
If production code changed: revert the fix, confirm the new test fails, then
restore it.
