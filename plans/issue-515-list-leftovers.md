# Two leftovers in the Connections list (#515)

Open questions: none. Calls made without asking are under "Decisions".
Background: `plans/issue-501-list-race.md`.

## Goal

Two things the #501 review left in `ui/src/pages/ConnectionsList.tsx`:

1. A delete that answers after a workspace switch still runs
   `setTotal(prev => prev - 1)`, now on the new workspace's total, so its
   page count can be one short.
2. A failed load shows "No connections yet" beside the error toast, which
   tells the user the workspace is empty when we don't know that.

## Acceptance criteria

1. Delete a connection in workspace A, switch to B and let B's list land
   before the delete answers. B's total and page count stay as B's load
   said, B's rows stay, and the success notification still shows.
2. The same holds when the list is reloaded in the same workspace
   (page change or retry) before the delete answers: the reload's total
   wins.
3. A delete that answers with no reload in between still removes the row
   and lowers the total, as before.
4. A failed load shows an error state ("Couldn't load connections", the
   error's message, a "Try again" button) and never "No connections yet".
   The error toast stays.
5. "Try again" loads the same page of the same workspace again; on
   success the rows show and the error state is gone; a second failure
   shows the error state again.
6. The #501 guarantees hold: a late answer or failure for an older
   workspace never writes, and the skeleton shows until the current
   workspace's load lands.
7. Tests over the real component (only the HTTP client mocked) cover 1 to
   5 and fail without the fix. Mutation run: drop the delete guard, or the
   error state, and they fail.
8. From `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
   `npx vitest run --coverage` pass, thresholds held.
9. Backlog item 98 struck with `(#515)`.

## Files

- `ui/src/pages/ConnectionsList.tsx`
- `ui/src/pages/ConnectionsList.*.test.tsx`
- `plans/backlog.md`

## Decisions

- The delete guard counts loads that wrote rows (`rowsLoads`, a ref)
  instead of comparing workspace ids. A workspace id check misses A, B,
  back to A: A's fresh load lands with the real total and the late delete
  would still subtract from it. "Did any load write since the delete
  started" covers that, page changes and retries alike. If no load has
  landed yet after a switch, the local edit hits rows that aren't shown and
  the pending load overwrites them, so that case needs nothing.
- The delete's success notification and the header summary refresh still
  run when the guard skips the local edit; the delete did happen.
- Keep the error toast and add the inline error state. The toast matches
  every other action on the page; the inline state is what's left once the
  toast is gone.
- Two existing assertions expected "No connections yet" after a failed
  load. That is the behaviour this issue changes, so they now expect the
  error state and also check "No connections yet" is absent.
