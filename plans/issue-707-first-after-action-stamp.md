# Check the object before the first cloud-storage after-action (#707)

Open questions: none.

## Goal

Backlog item 166. The cloud-storage consumer's first `delete` or `move`
after a publish runs blind in event and poll mode: an object uploaded again
under the same key while the old one was being published is deleted or moved
without ever being published. #672/#676 check the stamp only on retries. Do
the same check before the first after-action.

## Acceptance criteria

- Event mode: the stamp taken before the fetch (size, last-modified,
  version) is compared with a `Stat` right before the first `delete` or
  `move`. A changed object is left alone with a Warn (`object replaced since
  published; left for its own notification`) and is not queued for a retry.
- Poll mode: the same check, with a Warn that says the next poll picks it up.
  The object is not marked published, so the next poll publishes the
  replacement, even one with the listing's size and last-modified.
- `ErrNotFound` from that `Stat` counts as done (no retry, no error).
- Any other `Stat` error is a failed after-action: event mode queues it as
  today (#672), poll mode backs off and retries as today (#640).
- Tests with a fake store that replaces the object during the publish, for
  both modes and both actions, that fail on main.
- `docs/connectors/cloud-storage.md` describes the check and the window that
  is left; `plans/backlog.md` item 166 struck with `(#707)`.

## Affected files

- `src/cmd/cloud-storage-consumer/service.go`: `ingest` stats before the
  fetch and checks before the after-action, `ingestObject` and `pollOnce`
  handle the outcomes.
- Tests: new `src/cmd/cloud-storage-consumer/first_after_stamp_test.go`;
  `event_after_retry_test.go` (one Stat index shifts, see Decisions).
- `docs/connectors/cloud-storage.md`, `plans/backlog.md`.

Not touched: `src/pkg/objectstore`, `src/pkg/sdk`, the producer.

## Approach

- `ingest` (both modes) with `delete` or `move`: `Stat` before the fetch,
  keep the stamp; after the publish, `Stat` again; same stamp runs the
  after-action, a different one returns `errReplaced`, a `Stat` error is
  returned as the after-action error (wrapping `ErrNotFound` when gone).
- Event mode: `errReplaced` logs the Warn and forgets the key in the retry
  queue; the rest goes to `retryQ.failed`, which already treats
  `ErrNotFound` as done.
- Poll mode: the object is marked done at the listing stamp unless the
  result is `errReplaced`, which logs a Warn and leaves it unmarked; `ErrNotFound` logs Info, any other error goes
  to the existing `RetryFailed` backoff.

## Decisions

- Poll mode stats before the fetch instead of reusing the listing entry.
  The listing and `Stat` don't agree on last-modified everywhere: S3
  ListObjectsV2 returns milliseconds (MinIO fills them in), HeadObject's
  `Last-Modified` header is whole seconds. Comparing the listing with a
  `Stat` would see every MinIO object as replaced and never delete
  anything. Stat-to-Stat compares like with like. Cost: one HEAD per
  published object, only with `delete` or `move`.
- A replacement between the listing and the first `Stat` is the object that
  gets fetched and published, so deleting it after is right.
- A replacement between the first `Stat` and the fetch is published under
  the old stamp; the check then sees a mismatch and leaves it. A duplicate
  publish at worst (poll: the next listing has the new stamp), never a loss.
- The event retry test "stat fails on a retry, then works" scripted the
  failure on Stat call 1, which was the retry's. Call 1 is now the check
  before the first delete, so the failure moves to call 2 to keep testing
  the retry's Stat.
- One check before the action, as on the retry path. A `move` is not checked
  again between its copy and its delete; that window is backlog item 167
  (conditional deletes).

## Risks

- The window between the check and the delete (for a `move`, through the
  copy, which on Azure downloads and re-uploads the blob) is still there.
  Closing it needs conditional deletes: backlog item 167.
- GCS `updated` changes on a metadata update, the generation doesn't. A
  metadata update during the publish reads as a replacement, so the object
  is left (event) or published once more (poll). Fails safe.
- Poll mode: a replacement with the same size and last-modified as the
  listing (S3, same second) can't be told from the published object by the
  done set. Marking it done would leave it unpublished, and the retry pass
  (#640), which has no Stat check, would then delete or move it blind
  (found in review of #710). So a replaced object is not marked done and
  the next poll publishes it. Cost: when the replacement is in fact the
  same object re-read (a GCS metadata edit), it is published twice.
- The poll-mode retry pass (#640) still compares only against the listing:
  backlog item 189. GCS metadata edits read as replacements: item 190.

## How we verify

- `first_after_stamp_test.go`: a store that writes a new version under the
  key during the publish. Event and poll mode, `delete` and `move`: the
  replacement is still there, not copied, the Warn is logged, nothing is
  queued or retried. Unchanged objects are still deleted or moved. Gone
  before the after-action: done, no error, no retry. A failed `Stat` before
  the after-action: event mode retries from the queue, poll mode backs off
  and retries at a later poll.
- New tests fail on main (`git stash` the service change).
- Mutations: stamp comparison forced equal; the check removed; `version`
  left out of the comparison (same-size replacement).
- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run ./...`,
  `go test -race -count=3 ./cmd/cloud-storage-consumer/... ./pkg/doneset/...`,
  `make lint-tenant`.
