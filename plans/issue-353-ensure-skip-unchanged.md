# ensure() skips UpdateStream when nothing changed (#353)

## Open questions

None blocking. Decisions taken without asking are listed under Approach
and repeated in the PR.

## Goal

`messaging.ensure` calls `js.UpdateStream` for every stream that already
exists, on every start of every connector and core service. A fleet
restart sends dozens of concurrent updates of `VRSKY_DATA` and `VRSKY_DLQ`.
That was the trigger for the nats-server data race fixed in #339
(`plans/nats-server-race.md`) and it is noise. After this change an
existing stream is only updated when its config differs from the wanted
one on a field `ensure` sets.

## Acceptance criteria

1. `ensure()` compares the existing stream's config with the wanted one on
   the fields it sets: subjects, retention, storage, max age, max bytes,
   max msgs, discard, duplicates window. It calls `UpdateStream` only when
   one of them differs.
2. Server-filled defaults compare equal to an unset field: a stream created
   by `EnsureStreams` and ensured again with the same config is not
   updated (the DLQ config leaves max bytes, max msgs, discard and the
   duplicates window unset).
3. A stream whose config differs on one of those fields is still updated,
   as today, and the new value is applied.
4. A stream that does not exist is created, as today.
5. `UpdateStream` errors stay best effort: ignored, as today.
6. Tests against the embedded JetStream server show (a) a second
   `EnsureStreams` causes no stream update, (b) a changed limit is applied
   and does produce an update, (c) a missing stream is created.
7. `go test -race` passes for `pkg/messaging`, `pkg/sdk` and the rest of
   the module.

## Files

| File | Change |
|---|---|
| `src/pkg/messaging/messaging.go` | `ensure` compares before updating; new `streamDrift` |
| `src/pkg/messaging/ensure_streams_test.go` | new tests (written by a separate test author) |
| `docs/operator/upgrade.md` | one sentence on what the boot-time reconcile compares |
| `plans/backlog.md` | strike item 2, add #353 |

No Go code outside `src/pkg/messaging`. No caller changes: `EnsureStreams`
keeps its signature.

## Approach

- `streamDrift(have, want nats.StreamConfig) []string` names the fields
  that differ, the same shape as `consumerDrift` in `subscriber.go`.
  `ensure` returns early when it is empty; otherwise it sends the wanted
  config through `UpdateStream` exactly as today and ignores the error.
- Normalise the wanted side the way nats-server v2.15.1 does in
  `checkStreamCfg` (`server/stream.go`) before comparing:
  - max msgs / max bytes: 0 (or below -1) means -1, unlimited.
  - duplicates window: 0 means 2 minutes, or max age when max age is set
    and shorter.
  - retention, storage and discard: the zero values are real values
    (limits, file, old) and compare as they are.
  - subjects: compared in order; the server keeps the order it was given.
- Where the normalisation could be wrong (an account limit lowering the
  duplicates window, a future server changing a default), the comparison
  reports a difference and `ensure` updates, which is today's behaviour.
  Mistakes in that direction cost an unneeded update, never a missed one.
- Not compared: replicas and other fields `ensure` does not set. Today an
  update with replicas 0 resets the stream to R1 on every start; after
  this change a hand-made change to such a field stays until one of the
  compared fields drifts. Production streams are R1
  (`plans/disk-costs.md`) and only `ensure` creates them, so nothing in
  production changes.
- No logging: `ensure` has no logger today and adding one changes the
  `EnsureStreams` signature used by four callers.

## Risks

- A field the comparison misses stops a real config change from being
  applied. Mitigation: the compared list is the full list of fields
  `EnsureStreams` sets today; a test fails if a changed limit is not
  applied; the `streamDrift` comment says a new field set in
  `EnsureStreams` must be compared too.
- Server defaults normalised wrongly make an unchanged config compare as
  different. That only brings back today's behaviour (an update each
  start); the no-update test catches it for the embedded server.
- Production runs `nats:2.12-alpine`
  (`infrastructure/kubernetes/platform-nats/statefulset.yaml`), the tests
  run the embedded v2.15.1. The defaults used here (-1 for unlimited, a
  2-minute duplicates window capped by max age) have been the same across
  the 2.x line; if 2.12 filled one differently, the same fallback applies.

## Verify

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race -count=1 ./pkg/messaging/... ./pkg/sdk/...`, then
`go test -race ./...`.

Mutation run: make `streamDrift` always return nothing (the changed-limit
test must fail) and always return a field (the no-update test must fail);
restore and confirm green.
