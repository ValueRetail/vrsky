# The sidebar's status counts cover at most 100 connections (#541)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

The connections store loads one summary page (`limit=100`) for the header
badge and the sidebar. The badge shows the API's `total`, but the sidebar's
Running, Stopped and Errors cards count the loaded items. A workspace with
more than 100 connections gets a badge that disagrees with the cards. Fix it
on the client only; the API stays as it is.

`GET /api/v1/connections` already takes `status`
(`src/pkg/managementapi/handler.go`, `ListFilters.Status`), and the
Postgres repository applies it to the count as well
(`postgres_repository.go`, `ListConnections`). So the client can ask for
each status's total with `?status=<s>&limit=1`.

## Acceptance criteria

1. When the summary page comes back full (100 items) and the API's total is
   larger, `load()` asks for `status=running`, `status=stopped` and
   `status=error` totals and the sidebar cards show those totals.
2. When the page holds the whole workspace (fewer than 100, or exactly the
   total), no extra request is made and the cards count the loaded items as
   before.
3. A stale load (an older workspace, or one cleared meanwhile) never writes
   its status totals, whether the list or a status request answers late.
4. A failed status request fails the load the way a failed list does
   (error set, list, total and counts cleared), so the cards never show a
   sample as if it were the whole.
5. `clear()` and a failed load reset the status totals.
6. Tests from these criteria fail without the fix; mutation run.
7. From `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
   `npx vitest run --coverage` pass, thresholds held.
8. Backlog item 75 struck with `(#541)`.

## Files

- `ui/src/services/connectionService.ts`: optional `status` on `list`
- `ui/src/store/connectionsStore.ts`: `statusCounts`, filled by `load`
- `ui/src/components/Layout/Sidebar.tsx`: reads `statusCounts` when set
- tests next to each, `plans/backlog.md`

## Decisions

- Per-status totals from the API rather than labelling the cards as a
  sample: the filter exists and counts on the server, so the cards can be
  right.
- The extra three requests only go out when the summary page is full and
  the total is larger. Below 100 connections the loaded items are the whole
  workspace, so nothing changes for almost every workspace and no request
  is added.
- `limit=1` per status: the handler clamps limit to 1..100, and only `total`
  is read.
- The status requests run after the list answers, in parallel with each
  other, under the same load ticket as the list.
- A failed status request fails the whole load. Keeping the badge with
  sample cards would bring the bug back on errors; hiding only the cards
  needs a third state for little gain.
- The store's `statusCounts` is null when the loaded items are the whole
  workspace, and the sidebar counts those items itself. Existing sidebar
  tests that set only `connections` keep their meaning.
