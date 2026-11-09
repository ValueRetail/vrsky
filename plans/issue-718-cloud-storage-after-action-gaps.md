# Close the remaining cloud-storage after-action gaps (#718)

Open questions: none.

## Goal

Backlog items 189, 190 and 167. The cloud-storage consumer can still delete
or move a customer's object that it never published:

- 189: the poll-mode retry pass (#640) runs the after-action for a key that
  is done at the listing's stamp, with no further check. A replacement with
  the same size in the same second (S3 listings have whole seconds), or one
  that lands between the listing and the retry, is removed unpublished.
- 190: the stamp check compares last-modified, which on GCS is `updated` and
  changes on a metadata-only edit while the generation stays. Such an object
  is left (event mode) or published again (poll mode).
- 167: between the Stat and the delete (for a move, through the copy) there
  is still a window in which a replacement is removed.

## Acceptance criteria

- `doneset.Stamp` gets `Version` (the object's ETag or generation). It is
  compared only when both stamps have one, so done sets saved before this
  change still match and nothing is published again after the deploy. Old
  code reading a new row ignores the extra JSON field.
- Poll mode marks done with the listing's version. A same-size, same-second
  replacement of a done object has another version, is not done, is not
  retried blind, and is published at that poll.
- The retry pass stats the object before the retry and compares it with the
  listing (size, whole seconds, version). A mismatch leaves the object,
  forgets it from the done set so the next poll publishes it, and logs a Warn.
- On GCS, stamps with a version on both sides compare size and generation and
  ignore last-modified, in the done set, in the Stat checks of both modes and
  in the event retry queue.
- `objectstore` gets an optional `Conditional` interface (`DeleteIf`,
  `CopyIf`), implemented by all three backends: S3 `If-Match` /
  `x-amz-copy-source-if-match`, GCS `ifGenerationMatch` /
  `ifSourceGenerationMatch`, Azure `If-Match` on the delete and on the source
  download of the copy. A version mismatch wraps `ErrVersionChanged`; a
  missing object wraps `ErrNotFound`.
- The after-action uses them with the version it checked. A mismatch is
  "replaced": left for the next poll or its own notification, never retried.
- MinIO: checked against the release in `docker-compose.yml`
  (`RELEASE.2026-08-04T00-00-00Z`). It accepts `If-Match` on DeleteObject
  and deletes anyway (204, then HEAD 404), so on MinIO the Stat check stays
  the guard for deletes. It does refuse a stale
  `x-amz-copy-source-if-match`. An S3-compatible store that answers
  `501 NotImplemented` to the condition gets a plain delete or copy instead,
  after the Stat check, as before.
- Tests that fail without each fix (mutation runs), docs updated, backlog
  items 189, 190 and 167 struck with `(#718)`.

## Affected files

- `src/pkg/doneset/doneset.go`: `Stamp.Version`, `Match`, `Set.UseMatch`,
  `Set.Forget`; `Done` and `mergeSaved` compare with the Set's match.
- `src/pkg/objectstore/store.go`, `s3.go`, `gcs.go`, `azure.go`:
  `Conditional`, `ErrVersionChanged`, the three implementations.
- `src/cmd/cloud-storage-consumer/service.go`, `event_after_retry.go`:
  versioned listing stamps, provider match, Stat check in the retry pass,
  conditional after-action.
- Tests in those packages, plus integration tests for the conditional calls.
- `docs/connectors/cloud-storage.md`, `plans/backlog.md`.

Not touched: the SFTP consumer (its stamps have no version, so nothing
changes for it), the producer, the SDK.

## Approach

- `Match.Same(a, b)`: sizes equal; versions equal when both are set; with
  `MatchVersion` (GCS) and both versions set that is all, otherwise
  last-modified must match too.
- The consumer builds every stamp with `stampOf(obj)` (size, last-modified,
  version) and compares with `cfg.match()`. `objectStamp` goes away: the
  doneset stamp now carries the version.
- `afterAction` takes the version it checked. With a version and a backend
  that implements `Conditional` it calls `CopyIf`/`DeleteIf`; otherwise the
  plain calls. `ErrVersionChanged` becomes `errReplaced`.
- Retry pass: Stat, compare at whole seconds (S3 listings carry milliseconds
  on MinIO, HEAD only seconds, see #707), then the conditional after-action
  with the listing's version (or the Stat's when the listing has none).

## Decisions

- Version in the stamp and the Stat check, both. The version closes the
  same-second case, which a Stat can't see (the Stat shows the replacement,
  same as the listing). The Stat closes the gap between listing and retry on
  stores where the conditional delete is not honoured (MinIO).
- Old done entries have no version and keep matching on size and
  last-modified. They are not upgraded with the listing's version: the
  listing may already show a same-second replacement.
- GCS metadata edits: compare the generation, not `updated`, only when both
  stamps have one. An old GCS entry still compares `updated`; after one
  metadata edit it is published once more (as today) and then carries the
  generation.
- A replaced object in the retry pass is forgotten from the done set rather
  than only dropped from the retries, so the next poll publishes whatever is
  there, even on a store whose listing has no version.
- `Conditional` is an optional interface, not part of `ObjectStore`: the
  test fakes and the other users of `ObjectStore` (data-filter spill,
  remote agent) need nothing new, and a store without it gets the old path.
- S3 `501 NotImplemented` to a conditional request falls back to the plain
  call. Other S3-compatible stores (R2, Ceph, Wasabi) may not support the
  headers, and refusing would leave every object in the bucket.

- Emulators, checked: Azurite honours both conditions; fake-gcs-server
  ignores `ifGenerationMatch` and `ifSourceGenerationMatch` (the client
  sends them, its request log shows). The integration tests assert a refusal
  only where the emulator gives one; the per-provider unit tests check the
  request and the mapping of 412 and 404 for all three.
- The retry's Stat compares with the listing at whole seconds. Checked
  live: MinIO lists `12.300`, its HEAD says `12.000`; Azurite and
  fake-gcs-server agree to the nanosecond. ETag and generation agree between
  listing and Stat on all three.

## Risks

- On MinIO (and any store that ignores the headers) the window between the
  Stat and the delete stays. Documented.
- On S3, the ETag of a single-part upload is the MD5 of the content, so an
  identical re-upload in the same second matches and is deleted: it is the
  same bytes as were published.
- Azure `If-Match` on the copy's download protects the read; the upload to
  the destination is unconditional, as before.
- Not checked against real S3: `If-Match` on DeleteObject in a
  versioning-enabled bucket. If S3 refused it with anything but 412 or 501,
  every delete there would fail and back off. The SDK documents the header
  for general purpose buckets without that caveat.
- A key the retry pass forgets can come back as done if, before the next
  save, `Refresh` merges a snapshot another poller saved (only while two
  pollers overlap) and the stale listing still matches it. With versions in
  the listing the next poll sees the replacement's version and publishes it;
  without them (no real backend) a same-size, same-second replacement could
  still be retried blind.
- A store whose listing and Stat never agree would make every retry read as
  a replacement: the object is published again, then removed. Duplicates,
  not loss.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race -count=1 ./cmd/cloud-storage-consumer/... ./pkg/doneset/...
./pkg/objectstore/... ./pkg/io/...` with a local Postgres; integration tests
for `pkg/objectstore` against a local MinIO, fake-gcs-server and Azurite;
`make lint-tenant`, `make lint-connector`. Mutation runs: revert each fix,
see its test fail, restore.
