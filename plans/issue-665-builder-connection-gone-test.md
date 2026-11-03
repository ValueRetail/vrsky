# #665: page test for the builder's "connection gone" redeploy

## Goal

`PipelineBuilder.tsx` asks "Deploy as new pipeline?" when the connection a
canvas deployed has been deleted (a new connection means a new webhook URL).
Only the service layer was tested. Pin the page behaviour.

## Touches

- `ui/src/pages/PipelineBuilder.deploy.test.tsx` only. The test renders
  `ConfirmDialog` beside the page, as `App.tsx` does. No page code changes.
- `plans/backlog.md`: strike item 152.

## Cases

- The stop says 404, or the update says 404: the dialog appears with the
  old id and the webhook warning, and nothing is created or started yet.
- Confirm: stop (old id), create, start (new id). No update of the old id.
  The success toast carries the new webhook URL. The canvas links to the new id.
- Cancel: no create, no update, no start, no toast; the dialog closes. The
  stale link was already dropped when the server said 404, so nothing is linked.

## Verify

`npx tsc -b`, `npm run lint`, `npm run build`, `npx vitest run --coverage`.
Mutation: invert, remove the `ConnectionGone` branch, make confirm redeploy
without `asNew` (the #355 bug), make cancel deploy. Each must fail a case.
