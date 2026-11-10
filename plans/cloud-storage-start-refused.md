# Cloud-storage start the consumer refuses (#724)

Goal: a cloud-storage consumer config the consumer would refuse at start does
not end up as a `running` connection that ingests nothing.

## Acceptance criteria
- Starting a connection whose cloud-storage consumer has `after_action=move`
  without `move_prefix`, or (poll mode) a `move_prefix` inside what `prefix`
  lists, returns 400 naming the node and the reason.
- Event mode with an overlap is not refused (the consumer only warns, #594).
- The consumer sets status `error` for any refusal it still sees (saved
  earlier, or restored on boot).
- The "Move to prefix" field shows the rule.

## Touched
- `src/pkg/objectstore/moveprefix.go`: the overlap rule, moved out of the
  consumer so the management API shares it.
- `src/cmd/cloud-storage-consumer/service.go`: `refuseStart`.
- `src/pkg/managementapi/nodeconfig.go`: `validateCloudStorageConsumer`.
- `ui/src/components/Pipeline/CloudStorageConfigEditor.tsx`, `moveInsideListing.ts` (the field is
  there, not in `PropertyEditor.tsx` as the issue says).
- `docs/connectors/cloud-storage.md`, `plans/backlog.md`.

## Decisions
- Shared the rule in `pkg/objectstore` (both already import it or may); the UI
  keeps a TS mirror with tests for the same cases.
- `refuseStart` takes its status slot with `StartGuard.StopStatusLocked`
  (nothing added to pkg/sdk), and writes nothing if the connection is active.
- All post-lookup refusals (no bucket, bad event config too) write `error`,
  not only the move ones. The validator only adds the move rules; bucket is
  left alone to avoid breaking saved fixtures.

## Risks
- A stop racing a refused start could be overwritten by `error`; stops write
  only for active connections, so this needs a stop of a never-started one.

## Verification
Go tests (validator table, consumer refusal writes error, rule table), UI
tests with coverage, mutation runs.
