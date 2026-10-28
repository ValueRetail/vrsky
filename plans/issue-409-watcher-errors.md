# File watcher: show unreadable folders, stay deferred on a checkpoint error (#409)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Backlog items 50 and 51, both found in review of #398.

1. A watch folder that can't be read shows up in the File watcher panel,
   not only as one Warn line in the file-consumer log.
2. While the baseline is deferred (#398), a failed checkpoint read keeps
   it deferred. Today `loadKnown` falls back to "everything in the folder
   is done", and files that landed during the outage are never published.

## Acceptance criteria (from the issue)

1. A failed poll emits a `FileEvent{Type: "error"}` with a useful message.
   The message names the folder relative to the workspace's root, never
   the full path on the volume. The panel shows it (it already shows
   `error` events in red). Throttled.
2. In the deferred path a checkpoint read error keeps the connection
   deferred and is retried on the next poll. Nothing is saved or published
   until a folder read and a checkpoint load both work in the same poll.
3. Deterministic tests for both, with mutations;
   `go test -race -count=20 ./cmd/file-consumer/...` passes.
4. Backlog items 50 and 51 struck with `(#409)`.

## Approach

`src/cmd/file-consumer/service.go`:

- `noteReadFailure` keeps its log rules (Warn first, Debug after) and also
  emits an `error` event: on the first failure of a run, then at most once
  every `readErrorEventEvery` (30 s, on the watcher clock `s.clock()`).
  `ActiveConnection.readErrorEventAt` holds when the last one went out;
  zero means none in this run. Touched only by the watcher goroutine.
- The message: `Could not read watch folder "orders": permission denied;
  nothing is picked up until it can be read`, or `Watch folder "orders" is
  missing; ...`, plus `(N polls skipped)` from the second failure on. The
  folder is `ac.WatchDir` relative to `tenantpath.Root`; the reason is the
  `*fs.PathError`'s inner error only, so the volume path never reaches the
  browser. Anything else gets a fixed text.
- The first good read after a run that emitted an error event emits an
  `info` event (`Watch folder "orders" can be read again; files are
  picked up`), so the last
  line in the panel is not a stale error. The panel shows unknown types in
  grey with the message.
- `takeDeferredBaseline` calls `loadKnown(ctx, ac, true)`.

`src/cmd/file-consumer/known.go`:

- `loadKnown(ctx, ac, requireSaved bool)`. With `requireSaved` a failed
  checkpoint read returns an `errCheckpointRead`-wrapped error and changes
  nothing on `ac` (no `knownFiles`, no `dirty`). Without it (the start
  path) the fallback stays as it is, which
  `TestKnown_FailedReadDoesNotOverwriteSavedSet` pins.
- `noteReadFailure` recognises that error: log line "Could not read which
  files were already published; still deferred", and the panel message
  "Could not load which files in "orders" were already published; nothing
  is picked up until that works". The database error goes to the log only.

## Decisions

- Throttle: 30 s. Every 5 s would fill the panel's 50 rows in four
  minutes and push out everything else. 30 s keeps 25 minutes of an
  outage in view, and a panel opened during one shows the error within
  30 s. The first failure always goes out at once.
- Start path unchanged on a checkpoint error (fallback for this run, not
  saved). The issue is scoped to the deferred path; the start path's
  behaviour is pinned by an existing test, and changing it would delay
  every start during a database blip.
- The `connected` event that `/events/` sends still names the full watch
  path (`Watching /data/input/<tenant>/…`). Out of scope here; added to the
  backlog.
- Recovery `info` event added (not in the issue): without it the panel's
  newest line stays red after the share comes back.
- On a deferred start the failed read at start counts as the first
  skipped poll (it already sets `readFailures` to 1 so the polls after it
  log at Debug). The first deferred poll that fails therefore says "(2
  polls skipped)". Left as is: the start's read was skipped too.

## Files

- `src/cmd/file-consumer/service.go`, `known.go`
- `src/cmd/file-consumer/known_test.go` (fixture follows the new signature)
- new tests in `src/cmd/file-consumer/watcher_errors_test.go` (test author)
- `docs/connectors/file.md`, `plans/backlog.md`

## Risks

- A checkpoint store that stays down keeps a deferred connection
  publishing nothing. That is the point: it publishes nothing rather than
  losing files, and the panel and log say why.
- Event channels are buffered (50) and drop when full; a slow panel can
  miss an error event. The next one comes 30 s later.

## Verification

From `src/`: `go test -race -count=20 ./cmd/file-consumer/...`,
`go test -race ./...`, `go vet ./...`, `golangci-lint run ./...`,
`make lint-tenant`. Mutations through `go test -overlay`:

- no error event from `noteReadFailure`; the event tests must fail
- no throttle (emit every failure); the throttle test must fail
- `loadKnown` with `requireSaved` falls back as before; the deferred
  checkpoint tests must fail

## Verification results

- `go test -race -count=20 ./cmd/file-consumer/...`: ok (276 s).
- `go build ./...`, `go vet ./...`, `golangci-lint run ./...` (0 issues),
  `make lint-tenant`, `go test -race ./...`: all pass.
- Tests in `watcher_errors_test.go`, written by a separate test author
  from the criteria: throttled error events (table of clock steps),
  message names the folder and reason without the volume path (injected
  error, removed folder, `chmod 000`), no events without failures, and a
  deferred start whose checkpoint read fails stays deferred, publishes and
  saves nothing, then resumes from the saved set and publishes the file
  that landed during the outage once.
- Mutations through `go test -overlay`, source never edited:

  | Mutation | Caught by |
  |---|---|
  | no error event | throttle, message and deferred tests |
  | no throttle | throttle test |
  | `requireSaved` falls back like the start | deferred test |
  | raw `err.Error()` in the message | message test (path) and deferred test (DB text) |
  | no recovery `info` event | throttle, message and deferred tests |
