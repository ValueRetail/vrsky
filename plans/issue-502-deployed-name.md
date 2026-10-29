# A linked builder tab's deployedName never re-syncs with the server (#502)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

A builder canvas linked to a deployed connection records `deployedName`,
the name the server had when this tab last deployed or loaded it. Deploy
sends a name only when the tab differs from `deployedName`
(`deployIdentity`, #450). Opening `/connections/:id/edit` for a connection
whose canvas is already linked only switches to that canvas
(`PipelineBuilder.tsx` edit flow), so if the name changed elsewhere (another
browser, the API), the tab and `deployedName` still read the old name. They
agree with each other, Deploy sends no name, and the tab shows a name the
server no longer has. See `plans/issue-450-rename.md`, "Root causes", for
where this was found.

## Acceptance criteria

1. Opening the edit route for a connection whose canvas is already linked
   fetches the connection and sets that canvas's `deployedName` to the
   server's name. The canvas's own nodes and positions are kept.
2. If the tab was never renamed here (it reads the old `deployedName`, with
   or without the "Edit: " prefix, or a default "Tenant N" name), the tab
   shows the server's name, keeping the prefix.
3. If the tab was renamed here, it keeps that name, and the next Deploy
   sends it (`deployIdentity` gives `{ name: tab }`).
4. After a sync with no local rename, Deploy sends no name.
5. A tab renamed while the fetch was out keeps its new name; a canvas no
   longer linked to that connection, or from another workspace, is left
   alone; a blank server name changes nothing; a failed fetch changes
   nothing and shows no error.
6. Tests over the pure rule, the store action and the builder's edit flow
   fail without the fix. Mutation run on the rule and the wiring.
7. From `ui/`: `npx tsc -b`, `npm run lint`, `npm run build`,
   `npx vitest run --coverage` pass, thresholds held.
8. Backlog item 72 struck with `(#502)`.

## Files

- `ui/src/services/deployPipeline.ts`: `syncWithServerName`, the rule, next
  to `deployIdentity` (they share the tab-name parsing)
- `ui/src/store/canvasStore.ts`: `syncDeployedName` action
- `ui/src/hooks/useEditConnectionLoad.ts` (new): the edit flow, moved out
  of `PipelineBuilder.tsx` unchanged, plus the sync
- `ui/src/pages/PipelineBuilder.tsx`: calls the hook
- tests next to each, `plans/backlog.md`

## Decisions

- The sync runs only on the edit route, as the issue asks. Picking a linked
  tab in the tab bar doesn't fetch; that would be a request per tab switch.
- The canvas switch stays immediate and the name follows when the fetch
  answers, so the edit route opens as fast as before.
- The store action reads the canvas when the answer lands rather than when
  the request went out, so a rename typed meanwhile wins, and it checks the
  canvas is still linked to that connection. Canvas ids are per workspace,
  so a workspace switch in between finds no canvas and does nothing.
- A canvas with no recorded `deployedName` (deployed before #450) keeps its
  tab: there is no telling whether it was renamed, and keeping it is what
  Deploy assumed before. Its `deployedName` is filled in.
- The edit flow moved into a hook. Rendering the whole builder in a test
  pulls its ~500 coverable lines into coverage for the first time and drops
  the suite below every threshold; excluding the page from coverage would be
  a config change to make CI pass. The hook is the existing two effects
  moved as they were, plus the new one, and is tested with `renderHook`
  against the real canvas store.
- A canvas the flow has just imported isn't synced again. The import
  re-renders before its own pass finishes, so the next pass finds it as
  "linked"; the hook remembers the imported id so that pass doesn't fetch
  the connection a second time. Found by the test author.
- A failed fetch is silent. The canvas is already open and usable, Deploy
  behaves as before, and a deleted connection is reported by Deploy
  (`ConnectionGone`, #355).
