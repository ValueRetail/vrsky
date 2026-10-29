# Two more file-consumer messages show the path on the volume (#499)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Backlog item 96. Two user-facing file-consumer messages still carry
`/data/input/<tenant-id>/…`:

- the `error` event a watched file sends when it can't be published
  (`pollOnce` in `src/cmd/file-consumer/service.go` sent `ingErr.Error()`,
  and `os.Stat`, `os.ReadFile` and `os.Open` put the path in it; a failed
  publish puts the tenant's NATS subject in it);
- the sample-data endpoint's error (`pickSampleFile` in `server.go`:
  "directory does not exist: /data/input/<tenant-id>/…", `os.ReadDir`'s
  text, "no readable files in <path>", and `os.Open`'s text).

Both should name the folder relative to the tenant root, the way
`folderLabel` does for the `connected` event (#455) and the read-failure
events (#409), and keep only the operating system's reason.

## Acceptance criteria

1. A watched file that fails to read sends an `error` event naming the file
   and the folder relative to the tenant root, plus the OS reason
   (`permission denied`, `no such file or directory`).
2. A watched file that fails to publish sends an `error` event naming the
   file and the folder, without the publish error's text (it carries the
   tenant's subject).
3. A file over the size limit on a worker that can't stream still says so.
4. The sample-data endpoint's errors (missing folder, unreadable folder,
   empty folder, unreadable file) name the folder relative to the tenant
   root and give only the OS reason.
5. None of these messages contains the volume root, the tenant root, the
   absolute folder or the tenant id. Tests check this over the real
   `pollOnce` and the real `handleSampleData` handler, and fail without the
   fix.
6. The full Go error still goes to the log.
7. Mutation run: putting back `ingErr.Error()` or `err.Error()` makes the
   tests fail.
8. From `src/`: build, vet, golangci-lint, `go test -race ./...` and
   `make lint-tenant` pass; the package passes with `-count=10 -race`.
9. Backlog item 96 struck with `(#499)`; `docs/connectors/file.md`
   mentions the new messages.

## Files

- `src/cmd/file-consumer/service.go`: `ingestFailureMessage`, `dirLabel`
  (what `folderLabel` did, for any directory), `errWatchFileTooLarge`.
- `src/cmd/file-consumer/server.go`: `pickSampleFile` takes the label and
  returns a `*sampleError`; `sampleFailureMessage` builds the text; the
  handler logs the full error.
- a new test file in `src/cmd/file-consumer`
- `docs/connectors/file.md`, `plans/backlog.md`

## Decisions

- A failed publish shows no reason, only "the reason is in the
  file-consumer log". Its errors come from the SDK and NATS
  (`js.Publish vrsky.<tenant>…`, `stream payload "<key>"`) and there is no
  safe way to keep part of them. The user can't fix those anyway; the
  operator reads the log.
- A failed read is spotted with `errors.As(*fs.PathError)`, the same test
  `readFailureMessage` uses. That also catches a file read that fails while
  it is being streamed into the payload store, when the store wraps the
  error with `%w`.
- `folderLabel` became a wrapper over `dirLabel(tenantID, dir)`, so the
  sample endpoint (which has a tenant and a resolved folder but no
  connection) labels folders the same way.
- The sample endpoint's messages stay lower case like its other errors
  ("path not allowed", "path is required").

## Risks

Small. The UI prints these messages and doesn't parse them
(`ui/src` shows the sample error text as it is). Wording changes only.
