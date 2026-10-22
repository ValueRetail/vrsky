# The builder's Deploy overwrites a pipeline's name

Investigated 2026-10-22 from the local stack's request log and database
(the CSV → Database test where a second pipeline appeared) and production's
connections table (read-only).

## What the evidence says

**The suspected bug is not there.** Every builder Deploy on an existing
pipeline did what `services/deployPipeline.ts` says: stop → `PUT` → start
on the same id. The local API's log shows each of today's three pipelines
created by one `POST` (the template wizard: create + start, then navigate
to the builder) and then updated in place by the builder's Deploy:

| Created (wizard) | Updated (builder Deploy) | Pipeline |
|---|---|---|
| 09:44:05 POST | 09:44:17 stop, PUT, start | `557807e2` API poll → File |
| 09:47:27 POST | 09:47:29 stop, PUT, start | `86bbc51a` CSV → Database |
| 09:50:05 POST | 09:50:06 stop, PUT, start | `11adf496` CSV → Database again |

The "second pipeline" was a second run of the template wizard, which
creates a new pipeline by design. No builder Deploy ever created one.

**A real bug instead:** `buildConnectionPayload` in
`ui/src/pages/PipelineBuilder.tsx` sends

```ts
name: `Pipeline ${new Date().toLocaleTimeString()}`,
description: 'Created via visual pipeline editor',
```

on **every** deploy, the update included. So every redeploy throws away
the pipeline's name and description:

- Local: all three are now `Pipeline 11:44:17`, `Pipeline 11:47:29`,
  `Pipeline 11:50:06`; the wizard had named them after their template.
- **Production: all three running pipelines** are `Pipeline 2:26:01 PM`,
  `Pipeline 2:00:49 PM`, `Pipeline 11:22:54 AM`, description "Created via
  visual pipeline editor". Nobody can tell from the list which is the
  Business Central poller and which the remote agent.
- The file destination names its output after the pipeline, so a
  redeploy also moves where files land (`…/API poll → File` became
  `…/Pipeline 11_44_17` locally).

## Open questions

1. **Where the name comes from.** Recommended: **the canvas tab's name**,
   minus the `Edit: ` prefix the edit flow adds (so "Edit: CSV →
   Database" deploys as "CSV → Database"), and **on an update, the name
   the server already has** unless the user renamed the tab. Concretely:
   an update sends the name loaded with the connection; a first deploy
   sends the tab's name; the generated `Pipeline <time>` is the fallback
   only for an untitled canvas. Alternative: a "Pipeline name" field in
   the Deploy step, which is more UI for a problem the tab name already
   solves.
2. **Description.** Recommended: **keep the server's description on an
   update** (the wizard writes `Created from the "…" template`); send
   "Created via visual pipeline editor" only on a create.
3. **The three production names.** Recommended: **rename them once, by
   hand in the builder after this ships** (open each, rename the tab,
   Deploy), to names you choose: Business Central → Bifrost, the webhook
   pipeline, the remote agent. Alternative: an SQL update, which I would
   not do on production data without your exact names.
4. **The wizard creating a new pipeline on every run.** Recommended:
   **leave it** (that is what "From template" means), but have its form say
   so in one line ("This creates a new pipeline").

## Approach

- `PipelineBuilder.tsx`: `buildConnectionPayload` takes the name and
  description from a small pure helper, `deployIdentity(canvas,
  loadedConnection)`, in `ui/src/services/deployPipeline.ts` beside the
  deploy sequence:
  - update (canvas has `deployedConnectionId`): the loaded connection's
    name and description, unless the tab was renamed since load, then the
    tab name;
  - create: the tab name without `Edit: `, or `Pipeline <time>` for an
    untitled canvas; description "Created via visual pipeline editor".
- The edit flow already loads the connection (`connectionService.get`);
  the canvas store keeps its name and description beside
  `deployedConnectionId` so a linked canvas reopened later still knows
  them.
- `OnboardingWizard.tsx`: one help line on the name field.

## Files

| File | Change |
|---|---|
| `ui/src/services/deployPipeline.ts` | `deployIdentity` |
| `ui/src/pages/PipelineBuilder.tsx` | uses it |
| `ui/src/store/canvasStore.ts` | keeps the loaded name/description with the link |
| `ui/src/onboarding/OnboardingWizard.tsx` | the help line |
| tests beside each | below |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `deployIdentity` table: update keeps the server's name and description; update after a tab rename sends the new name; create from "Edit: X" sends "X"; create from an untitled canvas sends `Pipeline <time>` | the rules above | return the generated name always (today's bug) |
| builder deploy test (existing `PipelineBuilder` / `deployPipeline` tests): a redeploy's `PUT` body carries the loaded name | the wiring | drop the helper from `buildConnectionPayload` |
| canvas store: the loaded name survives a reload of the store | the persistence | — |

Then `npm run build`, the full vitest run with coverage, lint.

## Verify

Local stack: run a template, Deploy from the builder, `GET` the
connection: the template's name and description are intact, and a file
destination keeps writing to the same folder. Then rename the three
production pipelines by hand (question 3).

## Non-goals

Changing how the wizard creates pipelines; renaming production pipelines
by SQL; a name field in the Deploy step.
