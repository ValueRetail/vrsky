# Drop the unrouted Dashboard page (#513)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

`ui/src/pages/Dashboard.tsx` has had no route since the router moved to
`App.tsx`; nothing imports it. Since #450 the layout fills the connections
store itself, so the page was the last caller of three store setters. Delete
the page and the store code only it used.

## Acceptance criteria

1. No file under `ui/src` imports or routes `pages/Dashboard`, and the file
   is gone.
2. `connectionsStore` no longer has `setConnections`, `setLoading` or
   `setError`; no production code called them besides the Dashboard.
3. A guard test fails when a page module under `ui/src/pages` is not
   imported by any other source file, so the next dead page gets caught.
   It fails with `Dashboard.tsx` restored.
4. From `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
   `npx vitest run --coverage` pass, thresholds held.
5. Backlog item 71 struck with `(#513)`.

## Files

- `ui/src/pages/Dashboard.tsx` (deleted)
- `ui/src/store/connectionsStore.ts`
- `ui/tests/unit/store.connections.test.ts`,
  `ui/src/store/connectionsStore.load.test.ts` (tests of the removed setters)
- a new guard test under `ui/src/pages`
- `plans/backlog.md`

## Decisions

- Delete rather than route it. The header badge, the sidebar counts and the
  Connections list already cover what it showed, and nobody asked for a
  dashboard. "Return to Dashboard" links elsewhere point at `/`, which is the
  builder; they stay as they are.
- Remove the three setters with it. Their unit tests go too. The one check
  in `connectionsStore.load.test.ts` that also covered `clear()` resetting
  the total keeps that part, seeded with `setState`.
- The store's other unused helpers (`addConnection`, the getters and so on)
  are not the Dashboard's, so they stay.
