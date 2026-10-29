# GCS PutStream aborts on a failed body (#524)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

`gcsStore.PutStream` in `src/pkg/objectstore/gcs.go` copies the body into
a `storage.Writer` and calls `w.Close()` when the copy fails. `Close`
finalises the upload, so a `vrsky-cli backup` whose dump or seal fails
halfway leaves a cut-off archive in the bucket. It shows up in
`backup list` and only fails at restore. The storage package's own docs
say to cancel the writer's context to stop without saving. S3 and Azure
already abort. Make GCS abort too, and pin the same rule for all three
stores.

## How the other stores behave today

- S3 (`manager.Uploader.Upload`): a body that fails while the first part
  is buffered returns before any request; a failure after that aborts
  the multipart upload (`LeavePartsOnError` is false). No object.
- Azure (`azblob.Client.UploadStream`): stages blocks and only commits
  the block list after the body reaches EOF. A body error returns before
  the commit, so no blob (staged blocks are dropped by the service).

No code change needed there.

## Acceptance criteria

1. On a body error, `gcsStore.PutStream` cancels the writer's context
   before closing it, returns an error that wraps the body error, and no
   object exists under the key afterwards. A previous object under the
   same key is left as it was.
2. Covered for a body that fails before its first byte and for both GCS
   upload shapes: a body that fails within the first
   chunk (sent as one multipart request on `Close`) and one that fails
   after the first 16 MiB chunk of a resumable upload went out.
3. A good body still uploads the exact bytes with its content type.
4. PutStream returns only once the writer's upload goroutine has finished
   (no goroutine left behind), and a failed body doesn't hang.
5. A unit test runs in the plain `go test ./...` suite against an
   `httptest` fake of the GCS JSON upload API (no emulator, no new
   dependency). It fails on the current code.
6. The shared integration helper gains the same check (failed body,
   nothing listed, no `Get`) for S3/MinIO, Azure/Azurite and
   fake-gcs-server, run by `integration.yml`.
7. Mutation: put `w.Close()` back in place of the cancel and the unit test
   fails.
8. From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
   `go test -race ./...`, `make lint-tenant` pass.
9. Backlog item 82 struck with `(#524)`.

## Files

- `src/pkg/objectstore/gcs.go`
- `src/pkg/objectstore/gcs_test.go` (new, httptest fake)
- `src/pkg/objectstore/integration_common_test.go`
- `plans/backlog.md`

## Decisions

- Cancel a child context of the caller's, then still call `Close`. After
  the cancel, `Close` returns the context error without finalising, and
  it waits for the upload goroutine, so nothing is left running.
- `Put` keeps its shape. Its body is a byte slice, so a `Write` error
  there comes from the upload itself and `Close` cannot commit anything.
- No change to S3 or Azure; the integration check pins their behaviour.
- The integration check runs inside `assertRoundTrip`, so the three
  backend tests pick it up without edits. Bodies fail after 64 KiB and
  after 17 MiB, past GCS's first 16 MiB chunk and S3's first 5 MiB part.
- Mutation "cancel but no `Close`" survives: the cancel alone stops the
  upload goroutine a moment later, and a check without settle time would
  flake. `Close` stays because it waits for that goroutine; dropping both
  is caught by the goroutine checks.

## What the GCS client does (v1.56.0)

- The first 16 MiB chunk is buffered before anything is sent. A body that
  ends inside it is one multipart request on EOF; a longer one is a
  resumable session with `bytes a-b/*` chunks and a final `bytes a-b/N`.
- `Writer.Close` closes the pipe, which the upload goroutine reads as EOF,
  so it finishes the upload with whatever it has. That is the bug.
- After the cancel, every request carries a cancelled context and the
  transport refuses it before sending, whichever of `monitorCancel` and
  `Close` wins the pipe.
- fake-gcs-server commits the partial object on the old code too (64 KiB
  and 17 MiB), so the integration check also fails without the fix.
