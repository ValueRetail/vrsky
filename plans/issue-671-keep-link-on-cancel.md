# Cancelling the "connection gone" redeploy keeps the canvas linked (#671)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

When the server says the linked connection is gone, the builder asks
"Deploy as new?". Cancel must leave the canvas linked to the old id, so the
next Deploy asks again instead of creating a new connection (and a new
webhook URL) without a word.

## Acceptance criteria

1. Cancel leaves `deployedConnectionId` and `deployedName` as they were; the
   next Deploy shows the dialog again and creates nothing.
2. "Deploy as new" still creates one connection, starts it and links the new id.
3. The cancel case in `PipelineBuilder.deploy.test.tsx` expects the old link
   (the behaviour changes on purpose), plus a case for Cancel then Deploy again.
4. Unit tests in `deployPipeline.test.ts` for the link handling.
5. The new tests fail when `link(null)` is put back (mutation run).
6. Backlog item 158 struck with `(#671)`.

## Cause

`deployCanvas` (`ui/src/services/deployPipeline.ts`) called `link(null)` in
its catch for `ConnectionGone`, before the page asked.

## Approach

Drop that catch. `ConnectionGone` is still thrown for the page. On confirm,
`deployPlan(canvas, true, ...)` plans a create and `deployCanvas` links the
new id over the old one, so nothing needs clearing first. The `link`
parameter type narrows to `string`, since it is no longer called with null.

## Files

- `ui/src/services/deployPipeline.ts`
- `ui/src/services/deployPipeline.test.ts`, `deployPipeline.renameFlow.test.ts`
  (both asserted the unlink at the 404)
- `ui/src/pages/PipelineBuilder.deploy.test.tsx`
- `plans/backlog.md`

## Decisions

- A "Deploy as new" whose create fails (quota, network) also leaves the old
  link, so the next Deploy asks again. Before, it had been unlinked already
  and the retry created silently. Covered by a page test.
- `PipelineBuilder.tsx` is unchanged; its `link` callback still accepts null
  and is assignable to the narrower type.

## Risks

Low. The stale link now survives until a create succeeds. Nothing else reads
the link in between except the DLQ panel and the DEPLOYED badge for that
canvas, which show the old id until the user answers.

## Verification

`npx tsc -b`, `npm run lint`, `npm run build`, `npx vitest run --coverage`.
Mutation: restore the `link(null)` catch and see the new tests fail.
