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
    A key ending in `/` (a folder placeholder, what the S3 console's
    "Create folder" writes) is not wanted, as the S3 listing skips it in
    poll mode. Keys are matched after the parser decodes them.
  - `runEventLoop`: a key that is not wanted is skipped. The first skip of
    a run is logged at Info (production logs at Info, so a filter that drops
    more than intended shows), later ones at Debug. It is not an error and does not set `ok = false`, so the
    message is acked once the wanted keys are ingested, or at once when none
    are. A skipped key never reaches `ingestObject`, so nothing is
    published, moved or deleted for it. Event mode has no done set; nothing
    is added there either.
  - `validateMoveOutsideListing`: the `Mode == "event"` exemption is gone,
    so it reports the overlap in both modes. `startConnection` refuses the
    start in poll mode, as before, and in event mode logs a Warn and starts
    anyway (see Decisions).
- `events.go`: `parseS3Notification` reads each record's `eventName` and
  keeps `ObjectCreated:*` only (`Put`, `Post`, `Copy`,
  `CompleteMultipartUpload`). MinIO writes the name with an `s3:` prefix,
  so that is stripped first. MinIO's metadata events under `ObjectCreated`
  (`PutTagging`, `DeleteTagging`, `PutRetention`, `PutLegalHold`) are
  excluded by name; the rest of `ObjectCreated:*` stays a prefix match, not
  an allow-list. `ObjectRemoved:*`, lifecycle, restore, tagging, ACL and
  replication records yield no keys and drain.
- `ui/src/components/Pipeline/CloudStorageConfigEditor.tsx`: File pattern is
  shown in both modes (it was poll-only, while the stored value was kept),
  with help text saying it applies in event mode too. Prefix was already
  shown in both.

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
- Event mode with `after_action: move` and a `move_prefix` inside `prefix`
  (an empty `prefix` included) logs a Warn and starts. The first round
  refused it, which was wrong for event mode on two counts. It would not
  have "stayed stopped": the consumer's refusal writes no status, so after
  a deploy `RestoreRunning` leaves the row `running` (backlog 133) while
  nothing reads the queue, and notifications pile up until retention drops
  them. And it would not have brought back #567's loss: in event mode a
  moved key is always `move_prefix/<base name>`, so when its notification
  comes back the `afterAction` self-copy guard (#567, PR #571) sees the destination
  equals the key and refuses the move. The worst case is one duplicate
  publish, and the object stays in `move_prefix`. The refusal also stopped a
  setup the old docs recommended (empty `prefix`, notification scoped to a
  folder). Poll mode keeps the refusal: there the listing returns the moved
  object at every poll.
- The `afterAction` self-copy guard from #567 stays, and in event mode it is
  what bounds the overlap above.
- File pattern is shown in event mode. A connection switched poll to event
  with `*.csv` kept the value while the editor hid it; after this change
  it would ack and skip every other object, so the field has to be visible.

## Acceptance criteria

- Event mode, S3, Azure and GCS: a key outside `prefix` or not matching
  `file_pattern`, or ending in `/`, is acked and skipped; the first skip of a
  run logs at Info, later ones at Debug; poll mode behaves as before.
- S3: only `ObjectCreated:*` records yield keys, minus MinIO's four metadata
  events.
- `start` refuses poll mode with `after_action: move` when `prefix` would
  select moved objects; event mode starts with a Warn, and a moved key that
  comes back is published once more and not moved.
- The editor shows File pattern in event mode, with help text.
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
  - `TestEventMode_EncodedS3KeyMatchesDecoded`: `in+folder/%C3%A4/my+file%2B1.csv`
    matches the decoded prefix and pattern, not the encoded ones.
  - `TestEventMode_MoveInsidePrefix_MovedKeyComesBackOnce`: the reviewer's
    probe config (empty prefix, move to `processed`) over a store whose copy
    notifies the new key: two publishes, two acks, object in `processed/`.
  - `TestEventMode_FirstSkipInfoAcrossMessages`.
  - Folder placeholder rows in the provider table, MinIO metadata names in
    the S3 event-name table.
- `move_prefix_test.go`: event-mode rows in
  `TestStart_RefusesMovePrefixTheListingReturns` expect a start with a Warn
  where poll mode refuses.
- `ui/src/components/Pipeline/CloudStorageEventFilter.test.tsx`: the field
  in event mode, its help text, a poll-to-event switch keeping `*.csv`.

## Files

`src/cmd/cloud-storage-consumer/{service,events}.go`,
`ui/src/components/Pipeline/CloudStorageConfigEditor.tsx`, the tests above,
`docs/connectors/cloud-storage.md`, `plans/backlog.md`.
