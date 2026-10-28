# Renaming a pipeline in the builder doesn't reach the Connections list (#450)

## What was reported (production, 2026-10-28)

1. Tab renamed to "HTTP→BusinessCentral", Deploy. The Connections list then
   showed "Pipeline 15:26:55" where it had shown "Pipeline 11:22:54 AM";
   Created (20.10.2026) unchanged, so an update in place.
2. Tab renamed to "BusinessCentral→RemoteAgent", Deploy. The list still
   showed "Pipeline 2:26:01 PM".
3. The header badge said "0 connections" while two were running.

## What the code does today

Checked against `main` (4b901d36) and against the bundle production served
on 2026-10-28 (`/assets/index-BmTmfvjx.js`, last-modified 11:18 GMT, fetched
read-only). The bundle's `deployIdentity`, `deployPlan` and `deployCanvas`
are the same as `main`'s.

- `deployIdentity` (`ui/src/services/deployPipeline.ts:94`) produces
  `Pipeline <time>` only on the create branch (no `deployedConnectionId`,
  untitled tab). An update sends `{}` when the tab wasn't renamed and
  `{ name: tab }` when it was (`:97-99`). `deployPlan` (`:123`) takes the
  target id and the identity from the same canvas, so a `PUT` can't carry a
  generated name.
- `renameCanvas` (`ui/src/store/canvasStore.ts:239`) changes `canvas.name`,
  the field `deployIdentity` reads; `CanvasSelector` calls it on Enter and
  on blur.
- The API applies a non-blank `name` on update
  (`src/pkg/managementapi/handler.go:398`) and the repository writes it
  (`postgres_repository.go:242`). Nothing ignores it.

So the current builder sends the renamed name, and never a generated one on
an update. A test of that flow (below) passes on today's code.

## Root causes

### 1 and 2: the page that showed them was running an old bundle

The Connections list (`ui/src/pages/ConnectionsList.tsx:53`) reads
`response.connections`, but `GET /api/v1/connections` answers
`{ data, total, limit, offset }` (`handler.go:152`, `:342`). Rendered
against that shape, the current list shows "No connections yet" (checked
with a throwaway render test). It has done so since 3816546d (2026-10-14),
which replaced `response.connections || response.data` with
`response.connections ?? []`. The same change broke the connection picker
in `ConnectionRequestsPage.tsx:71`.

A list that showed names and created dates on 2026-10-28 therefore came
from a bundle older than 2026-10-14, i.e. a tab left open across deploys
(the bundle is one file with no lazy chunks, so an old tab keeps working).
That bundle also predates a827ba0a (2026-10-22), whose builder sent
`name: Pipeline ${new Date().toLocaleTimeString()}` on every deploy, the
update included. That is exactly symptom 1: a fresh time-stamped name, in
place, in the browser's locale ("15:26:55", nb-NO).

Symptom 2 is a guess either way; I can't confirm it without production
data, which I don't read. Two explanations fit: a list loaded before the
second deploy and not reloaded, or a stale `deployedName` (found in
review). `deployedName` is a client-side record of the server's name that
nothing re-syncs: the edit flow switches to an already linked canvas
(`PipelineBuilder.tsx:235-239`) without refreshing it from the loaded
connection. If something else changed the server name (another browser:
"2:26:01 PM" is en-US, so a different browser wrote it), a tab can read
"X", `deployedName` also "X", and Deploy sends no name. That keeps #355's
rule (no rename, no name change), so it is not changed here; it is in
`plans/backlog.md`.

What is broken today, for a user on the current bundle:
- the Connections list is empty, so a rename can't be seen there at all;
- the Connection requests picker offers no connections.

Fix: `listConnections` (`ui/src/services/connectionService.ts:48`) maps the
API's `data` onto the `connections` field the pages read (keeping
`connections` when a response has it), so every caller gets the real list.

### 3: the badge counts a store nothing fills

`Header.tsx:76` shows `useConnectionsStore().connections.length`; the
sidebar's counts read the same store. The only writer is `Dashboard.tsx`,
and no route renders `Dashboard` (`App.tsx`). So the badge is always 0.

Fix: the store gets `total` and a `load()` that fetches the current
workspace's connections (only the latest load may write); `RootLayout`
clears the store and calls it when the workspace changes (and clears it
when there is no workspace, e.g. after a logout),
and `ConnectionsList` calls it after a delete, start or stop so the badge
keeps up. The header shows `total`; the sidebar keeps its running / stopped
/ error counts from the loaded list.

## Acceptance criteria

1. With the API's real list shape, the Connections list shows each
   connection's current name (and after a rename + Deploy, the new one).
2. Rename tab → Deploy on a linked canvas: the `PUT` body carries the new
   name. Deploy without a rename: the `PUT` body has no `name`, and never a
   generated `Pipeline <time>`. "Deploy as new" still creates and names
   after the tab (#355).
3. The header badge shows the workspace's connection count from the API on
   any page under the layout, and follows a workspace switch.
4. Tests for 1 and 3 fail on today's code; the test for 2 is a regression
   guard (today's code passes it; the old bundle's code would not). This
   departs from the issue's "fails on today's behaviour" for the builder
   part, because today's builder is not what failed.
5. The badge never shows a previous workspace's or user's count.

## Files

| File | Change |
|---|---|
| `ui/src/services/connectionService.ts` | `listConnections` reads `data` |
| `ui/src/store/connectionsStore.ts` | `total`, `load()` |
| `ui/src/components/Layout/RootLayout.tsx` | loads the store per workspace |
| `ui/src/components/Layout/Header.tsx`, `Sidebar.tsx` | show `total` |
| `ui/src/pages/ConnectionsList.tsx` | refresh the store after delete/start/stop |
| `ui/src/pages/ConnectionDetail.tsx` | refresh the store after start/stop/clone |
| tests | independent test author |
| `plans/backlog.md` | a stale-bundle notice (below) |

No API change: the handler already accepts `name` on update.

## Not done here

- A notice telling an open tab that a newer UI is deployed. That is what
  would have stopped symptoms 1 and 2, but it's new behaviour (a version
  file, polling, a banner), so it goes in `plans/backlog.md`.
- `Dashboard.tsx` is dead code (no route). Left alone; noted in the
  backlog with the item above.

## Verify

`npx tsc -b`, `npm run lint`, `npm run build`, `npx vitest run --coverage`
(80/80/80/75). Mutations: revert the `data` mapping (list test fails); drop
the layout's `load()` (badge test fails); make an update send the tab name
always / a generated name (builder flow test fails).
