# A stale edit-route answer can undo a rename deployed meanwhile (#540)

Open questions: none. Calls made without asking are under "Decisions".
Background: `plans/issue-502-deployed-name.md`, `plans/issue-516-edit-route-id.md`.

## Goal

The #502 sync in `useEditConnectionLoad` fetches the connection for a linked
canvas and hands the server's name to `syncDeployedName`, which reads the
canvas when the answer lands. If the user renames the tab and deploys while
that GET is still out, `deployedName` already equals the new tab. The stale
answer (old name) then looks like "not renamed here" and the store puts the
old name on both the tab and `deployedName`. The server keeps the new name,
so the tab is wrong until the edit route is opened again, and the next
Deploy would send the old name back.

Fix: note the canvas's `deployedName` when the request goes out and drop the
answer if it changed by the time it lands.

## Acceptance criteria

1. Edit route on a linked canvas, GET still out; the user renames the tab and
   a deploy records the new name as `deployedName`; the old answer then
   lands. The tab and `deployedName` keep the new name (memory and
   localStorage), and the next `deployIdentity` sends no name.
2. A rename typed while the GET is out, without a deploy, still keeps the
   tab and takes the server's name into `deployedName` (#502 behaviour).
3. With no change to `deployedName` while the GET is out, the sync works as
   before (tab follows the server when not renamed here).
4. A canvas with no recorded `deployedName` (pre-#450) still gets it filled
   in by the sync.
5. Tests that fail without the fix; mutation run on the new guard.
6. From `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
   `npx vitest run --coverage` pass, thresholds held.
7. Backlog item 101 struck with `(#540)`.

## Files

- `ui/src/hooks/useEditConnectionLoad.ts`
- `ui/src/hooks/useEditConnectionLoad.test.tsx`
- `plans/backlog.md`

## Decisions

- The check lives in the hook, not in the store's `syncDeployedName`. The
  hook is what knows when the request went out; the store action keeps its
  signature and its existing guards (still linked, same connection, blank
  name), and its tests are untouched.
- The guard compares `deployedName` only. That field changes only when this
  tab deploys a name, a sync runs, or ConnectionGone unlinks the canvas, so a
  change means the tab already knows a name at least as new as the answer (or
  the canvas is no longer linked, and the store would skip it anyway). A deploy that kept the name doesn't change
  it and the sync still applies, which is right: the server's name was not
  changed by that deploy.
- A dropped answer is not retried. The tab and `deployedName` already hold
  what the deploy sent, which the server accepted.
- Renames that come back to the same name (A to B to A, deployed each time)
  pass the guard, and the stale answer then says A, which is also the
  server's current name. No harm.
- Known gap, left alone: if this tab deploys a name during the GET and
  someone renames the connection elsewhere before the server answers, that
  newer name is dropped with the answer. The tab is stale until the edit
  route is opened again, which is the #502 situation inside one request's
  latency. Found in review.
