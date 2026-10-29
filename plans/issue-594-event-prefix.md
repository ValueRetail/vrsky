# Cloud-storage event mode: apply `prefix` and `file_pattern` (#594)

Backlog items 120 and 134.

## Problem

In event mode `runEventLoop` ingested every key a notification named.
`prefix` and `file_pattern` were ignored, and the S3 parser took every
record, not only created ones (Azure keeps `BlobCreated`, GCS keeps
`OBJECT_FINALIZE`). The editor shows Prefix in event mode, so a connection
with `prefix: incoming/` read the whole bucket's notifications. Because
`prefix` did not apply, #567 could not check `move_prefix` at start in event
mode, and a moved object that the notification covers was ingested again.

## Change

All in `src/cmd/cloud-storage-consumer`.

- `service.go`
  - `cloudConfig.matchesFilePattern(key)`: the glob against the base name
    that `pollOnce` had inline. `pollOnce` calls it now. An empty pattern
    matches everything; a malformed glob matches nothing, as before.
  - `cloudConfig.wantsEventKey(key)`: `strings.HasPrefix(key, prefix)` and
    `matchesFilePattern`. The providers match `prefix` as plain text when
    they list (see #567), so event mode does the same: `in` selects
    `in-done/a.csv`. No slash normalisation, because the listing does none.
  - `runEventLoop`: a key that is not wanted is logged at Debug and
    skipped. It is not an error and does not set `ok = false`, so the
    message is acked once the wanted keys are ingested, or at once when none
    are. A skipped key never reaches `ingestObject`, so nothing is
    published, moved or deleted for it. Event mode has no done set; nothing
    is added there either.
  - `validateMoveOutsideListing`: the `Mode == "event"` exemption is gone.
    Event mode with `after_action: move` is checked at start with the same
    rule as poll mode.
- `events.go`: `parseS3Notification` reads each record's `eventName` and
  keeps `ObjectCreated:*` only (`Put`, `Post`, `Copy`,
  `CompleteMultipartUpload`). MinIO writes the name with an `s3:` prefix,
  so that is stripped first. `ObjectRemoved:*`, lifecycle, restore, tagging,
  ACL and replication records yield no keys and drain.

## Decisions

- A record with no `eventName` is still treated as a create. AWS and
  MinIO always set it, and every removal names itself, so this cannot
  ingest a delete. It keeps hand-built or forwarded test bodies working,
  and matches GCS without `eventType` (Azure is stricter because Event Grid
  validation events carry a subject).
- Match on the `ObjectCreated:` prefix, not on `Created`:
  `ObjectRemoved:DeleteMarkerCreated` contains "Created" and is a removal.
- Skipped messages are acked, not left for redelivery. A key outside the
  selection will never become wanted, so redelivery would only loop.
- Event mode with `after_action: move` and an empty `prefix` is refused at
  start now (the rule refuses every `move_prefix` with an empty `prefix`).
  This is the behaviour change for existing connections: such a connection
  stays stopped with `invalid move_prefix` in the log until it gets a
  `prefix` and a `move_prefix` outside it. Weakening the rule for event mode
  would let a moved object come back as a new notification, which is the
  loss #567 closed.
- The `afterAction` self-copy guard from #567 stays as the second line.
- UI untouched. The editor hides File pattern in event mode; backlog 138.

## Acceptance criteria

- Event mode, S3, Azure and GCS: a key outside `prefix` or not matching
  `file_pattern` is acked and skipped with a Debug log; poll mode behaves as
  before.
- S3: only `ObjectCreated:*` records yield keys.
- `start` refuses event mode with `after_action: move` when `prefix` would
  select moved objects.
- Table-driven tests that fail before the change (below).
- `docs/connectors/cloud-storage.md` no longer says event mode ignores them.
- Backlog 120 and 134 struck with #594.

## Tests

- `events_filter_test.go`
  - `TestEventMode_AppliesPrefixAndFilePattern`: 9 cases times 3 providers.
    Each provider's real parser turns keys into a "created" notification,
    `fakeEvents` delivers it, and `runEventLoop` runs over a `fakeStore`.
    Checks what was published, what was deleted, that the one message was
    acked, and the Debug skip line.
  - `TestEventMode_SkippedKeyIsNotMoved`: with `after_action: move` only the
    wanted key is copied and deleted.
  - `TestEventMode_FailedPublishStillLeavesMessageUnacked`: a skipped key
    does not change redelivery of a failed one.
  - `TestParseS3Notification_EventNames`: 19 event names, plus a mixed
    record body.
  - `TestPollMode_PrefixAndPatternUnchanged`.
- `move_prefix_test.go`: event-mode rows in
  `TestStart_RefusesMovePrefixTheListingReturns` (the old "event mode is not
  checked at start" row now expects a refusal).

## Files

`src/cmd/cloud-storage-consumer/{service,events}.go`, the tests above,
`docs/connectors/cloud-storage.md`, `plans/backlog.md`.
