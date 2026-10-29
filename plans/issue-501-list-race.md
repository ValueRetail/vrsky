# The Connections list can show the previous workspace's rows (#501)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

`ui/src/pages/ConnectionsList.tsx` loads its page of connections in its own
effect, keyed on the page and the current workspace. Nothing stops an older
load from writing after a newer one, so on a slow switch from workspace A to
B, A's answer can arrive after B's and the list shows A's connections under
B. That is a tenant leak in the UI. Only the latest load may write, the same
rule the connections store's `load()` follows since #466.

## Acceptance criteria

1. Switch from A to B while A's list request is still pending; answer B
   first, then A. The list shows B's connections and never A's, also after
   A's answer lands.
2. A's late failure after the switch shows no error notification under B.
3. A load that fails clears the rows, so a failed load for B never leaves
   A's rows on screen.
4. A's rows are gone from the very first render after the switch, before
   any request answers, and stay gone through a second switch (A, B, C).
5. A late answer for an older page in the same workspace doesn't overwrite
   the newer page either.
6. Tests over the real component (only the HTTP client mocked) cover 1 to 5
   and fail without the fix. Mutation run: drop the guard and they fail.
7. From `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
   `npx vitest run --coverage` pass, thresholds held.
8. Backlog item 74 struck with `(#501)`.

## Files

- `ui/src/pages/ConnectionsList.tsx` (the load effect)
- a new test file next to it
- `plans/backlog.md`

## Decisions

- A `cancelled` flag in the effect's cleanup rather than a module-level
  ticket like the store's. The effect already reruns on every page or
  workspace change, so its cleanup is exactly "a newer load started", and
  the flag dies with the component.
- The cancelled load also leaves `loading` alone; the newer load owns it.
- The rows also remember the workspace they were loaded for
  (`rowsTenantId`). Rows from another workspace are never rendered, and
  the skeleton shows until the current workspace's load lands. Found in
  review: without it, A's rows stayed in state after a switch and only the
  `loading` flag hid them, so any path that dropped `loading` early (or the
  one commit between the switch and the next load starting) showed them
  under B.
- A failed load now clears the rows and the total. Before, it kept whatever
  was on screen, which after a switch is the previous workspace's rows. The
  store's `load()` does the same on error.

## Not done here (added to `plans/backlog.md`)

- A delete that resolves after a workspace switch still runs
  `setTotal(prev => prev - 1)` against the new workspace's total.
- A failed load shows "No connections yet" next to the error toast; an
  error state would read better.
