# The builder's edit route loads only the first connection it sees (#516)

Open questions: none. Calls made without asking are under "Decisions".
Background: `plans/issue-502-deployed-name.md`.

## Goal

`useEditConnectionLoad` (`ui/src/hooks/useEditConnectionLoad.ts`) keeps
`editLoadDone` as a single flag. The route `/connections/:id/edit` keeps the
same `PipelineBuilder` mounted when only `:id` changes, so after A has loaded,
going to `/connections/B/edit` finds the flag set and does nothing: no switch
to B's linked canvas, no import of B. The page stays on A's canvas. Make the
flow run once per connection id, and keep the #502 name sync tied to the
connection it was found for.

## Acceptance criteria

1. On an open builder, A then B where B has a linked canvas: the builder
   switches to B's canvas, and B's name is synced onto it (#502), B's graph
   kept.
2. A then B where B has no linked canvas: B is imported as "Edit: <name>",
   linked to B, and opened.
3. A canvas found for A is never synced against B: no request pairs A's
   canvas with B, and A's canvas keeps its name and link.
4. Routing from A to B while A's fetch is still out: A's late answer imports
   nothing and syncs nothing; B loads as in 1 or 2.
5. A, B, back to A: the builder returns to A's canvas (with a fresh name
   sync if it is a linked canvas, and without a second import).
6. A canvas this flow just imported is still not fetched a second time on
   the pass right after the import (#502), but a later visit to that
   connection syncs as any linked canvas does.
7. The same id on a re-render still loads only once.
7b. Leaving the edit route for the plain builder (`/`, same page mounted)
   and coming back to the same connection loads it again: its canvas is
   current and, if linked, synced. No request is made while off the route.
8. Tests with `renderHook` against the real canvas and UI stores (only the
   HTTP client mocked) cover 1 to 7 and fail without the fix where the old
   code is wrong. Mutation run on the per-id done check and the keyed
   linked canvas.
9. From `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
   `npx vitest run --coverage` pass, thresholds held.
10. Backlog item 100 struck with `(#516)`.

## Files

- `ui/src/hooks/useEditConnectionLoad.ts`
- `ui/src/hooks/useEditConnectionLoad.test.tsx`
- `plans/backlog.md`

## Decisions

- `editLoadDone` becomes derived: the hook stores the connection id it last
  finished for (`loadedForId`) and is done when that equals the route's id.
  A change of id is then "not done" with no reset effect, so there is no
  render in which a stale flag can win.
- The linked canvas is stored with the connection it was found for, and the
  sync effect only sees it while that connection is the route's. That is the
  issue's "drop the linked canvas id with it" without a separate reset.
- The "just imported" ref is consumed by the next linked pass. Before, it
  only had to survive one pass; with per-id loads, a stale value would skip
  the sync on a later visit to that connection.
- Off the edit route (no id) the hook forgets its last load, the linked
  canvas and the "just imported" ref. React Router keeps the builder
  mounted between `/` and `/connections/:id/edit`, so without this,
  A, then `/`, then A again did nothing but re-sync A's name while the
  builder stayed on another tab. Found in review.
- Backlog item 101 (a stale answer undoing a rename deployed meanwhile) is
  a separate race in the store's sync rule and stays in the backlog.
