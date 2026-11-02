# Cloud-storage event mode: a key that is already gone counts as done (#609)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

In event mode, a redelivered multi-key message acks once every key is either
ingested or no longer in the bucket. A key that really fails still leaves the
message for redelivery.

## Where it goes wrong today

`runEventLoop` (`src/cmd/cloud-storage-consumer/service.go`) ingests each key of
a message and acks only if all succeeded. With `after_action` `delete` or
`move`, the keys that succeeded are already deleted or moved when a sibling key
fails. The queue redelivers the message, `get` fails for the gone keys, so the
message never acks and loops until the queue's retention drops it.

## Acceptance criteria

1. `pkg/objectstore` exports `ErrNotFound`. `Get` and `GetStream` of S3, Azure
   and GCS return an error that satisfies `errors.Is(err, ErrNotFound)` when
   the object does not exist, and only then (a missing bucket or container,
   a denied request, a server error are not not-found).
2. In event mode, a key whose fetch returns `ErrNotFound` is logged at Info
   ("already gone"), is not published, gets no after-action, and does not stop
   the message from acking.
3. A message with one gone key and one key that fails still does not ack. A
   message whose keys are all gone or ingested acks. Nothing is published twice
   beyond what happens today.
4. Poll mode is unchanged.
5. Tests: a not-found mapping test per provider against an HTTP fake of the
   provider's API; table-driven event-loop tests; the new tests fail without
   the fix.
6. Backlog item 142 struck with (#609). Docs say what event mode does.

## Files

- `src/pkg/objectstore/store.go`, `s3.go`, `azure.go`, `gcs.go`
- `src/pkg/objectstore/notfound_test.go` (new)
- `src/cmd/cloud-storage-consumer/service.go`
- `src/cmd/cloud-storage-consumer/event_not_found_test.go` (new)
- `docs/connectors/cloud-storage.md`, `plans/backlog.md`

## Approach

`ErrNotFound` is a sentinel. Each provider wraps its SDK error with both, using
two `%w`, so the original error is kept:

- S3: `*s3types.NoSuchKey`, or an API error coded `NoSuchKey` (S3-compatible
  stores like MinIO). `NoSuchBucket` is not a match.
- Azure: `bloberror.HasCode(err, bloberror.BlobNotFound)`. `ContainerNotFound`
  is not a match.
- GCS: `storage.ErrObjectNotExist`. The read API answers 404 for a missing
  object and a missing bucket alike and the client reports both as a missing
  object, so GCS can't tell them apart on a read. A removed bucket would then
  count as "gone" for every key. That is the one place the check is looser;
  the Info line names each key, and a deleted bucket has nothing left to
  ingest.

In `runEventLoop`, `ingestObject`'s error is checked with
`errors.Is(err, objectstore.ErrNotFound)` before the failure path. `fetchAndPublish`
already wraps with `%w`, so the sentinel survives.

Why not the other options:
- Remember ingested keys per message in memory: lost on restart, and the queue
  gives a new handle per receive, so it needs a message id the sources don't
  expose. The not-found check also covers an object someone else removed.
- Ack the message and drop the failed key: loses data.

## Risks

- An object that was never ingested (removed by someone else before the
  consumer reached it) is now acked and dropped instead of looping. Redelivery
  can't bring it back, so this is the right outcome; the Info log names the key.
- A store that answers 403 instead of 404 for a missing key (S3 without
  `s3:ListBucket`) still counts as a failure. Unchanged from today.
- If a key were wrongly decoded from the notification, its not-found is now
  acked rather than looping with an error. Key parsing is covered by existing
  tests (#594).

## Decisions

- Log level Info, as the issue asks. One line per gone key.
- Event mode only. In poll mode a vanished object is retried next cycle and
  leaves the listing, so nothing loops.
- No after-action for a gone key: there is nothing to delete or move.

## How it is verified

- Provider tests run the real SDK clients against `httptest` servers and check
  the not-found mapping both ways (object missing: matches; bucket missing:
  does not match; 500: does not match).
- Event-loop tests (table-driven) over a fake store and event source, plus one
  through the embedded NATS harness with a redelivered message.
- Revert the `runEventLoop` change locally: the loop tests fail. Restore.
- `go build`, `go vet`, `golangci-lint run`, `go test -race`, `make lint-tenant`.
