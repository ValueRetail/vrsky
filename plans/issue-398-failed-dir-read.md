# One failed directory read republishes the whole folder (#398)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

A directory read that fails, during a poll or at start, changes nothing:
no file is dropped from the done set, nothing is saved over the
checkpoint, and nothing already published goes out again when the read
works again.

## Acceptance criteria (from the issue)

1. A failed directory read skips the poll. It is logged with the
   connection id, and `knownFiles`, `pending` and the saved checkpoint are
   left untouched.
2. A failed read at start records no baseline and does not overwrite the
   saved set. The start retries a few times, then fails cleanly (below).
3. Deterministic tests: a read error between two good polls publishes
   nothing new and saves nothing; a read error at start does not overwrite
   the saved set; a read that recovers resumes normally. Mutation: put back
   the empty set on error and the tests fail.
4. Backlog item 42 struck with `(#398)`.

## Where it goes wrong today

`listFiles` (`src/cmd/file-consumer/service.go`) returns an empty set when
`os.ReadDir` fails.

- `pollOnce` then sees every known name as deleted, drops it from
  `knownFiles`, clears `pending`, marks the set dirty and, since #394,
  saves the empty set. The next good read finds every file "new" and
  publishes the whole folder again two polls later.
- `loadKnown` (`known.go`) at start: with a saved set, known becomes the
  saved names in an empty listing (none), `dirty` is set because names
  were "pruned", and the start saves an empty set over the history. On a
  first start the baseline is empty, so everything in the folder is
  published on the next good read. Either way the folder is republished.

## The folder is missing vs. the read failed

At start, `startConnection` runs `os.MkdirAll(watchDir)` just before the
listing, so a missing folder there means it vanished in between, or the
mount is unhealthy. During a poll a missing folder means someone deleted
it, or the share dropped out (an SMB or Azure Files mount can report
ENOENT while reconnecting). An empty folder lists fine and is not an
error.

Both cases are treated the same: a failed read. They differ only in the
log line ("watch folder is missing" vs "could not read the watch folder"),
so an operator can tell them apart. Treating a missing folder as "empty"
is what causes this bug when the mount blips, and a folder that really was
deleted holds no files, so keeping the old names costs nothing until it
comes back. When it does, the first good read prunes the names that are
gone, as today.

## Approach

`service.go`:

- `listFiles(dir) (map[string]bool, error)`. It reads through a new
  field `fileConsumer.readDir func(string) ([]os.DirEntry, error)`; nil
  means `os.ReadDir`. That field is the test seam: tests inject a reader
  that fails on demand. No permission tricks (root in CI, and macOS ACLs,
  make `chmod 000` unreliable).
- `pollOnce`: if `listFiles` fails, log and return before touching
  `knownFiles`, `pending` or `dirty`, and without calling `saveKnown`. A
  save left dirty by an earlier failed save waits for the next good poll.
  - Logging: the first failure in a run is logged at Warn with
    `connection_id`, `watch_dir` and the error; repeats are logged at
    Debug so a share that stays down for an hour does not write 720 lines;
    the first good read after a run logs Info with how many polls were
    skipped. The counter (`ac.readFailures`) is touched only by the
    watcher goroutine, like the other fields.
- `loadKnown(ctx, ac) error` lists the folder first. On error it returns
  the error and changes nothing: no `knownFiles`, no `dirty`, no
  checkpoint read or write.
- `startConnection`: calls `loadKnown` up to `startReadAttempts` (4)
  times, waiting `startReadBackoff` (1 s, a field so tests can shorten
  it) doubled after each failure: 1 + 2 + 4 = 7 s at most. Between
  attempts it gives up if the start's context ends or a stop took the
  reservation (`res.stopped`, read under `s.mu`). After the last failure
  it logs Error with the connection id and returns. The deferred
  `releaseStart` frees the claim, nothing is active, no status is
  written, no watcher runs, and the checkpoint is untouched. A later
  start command, or the next pod start, tries again.

## Reservation rules (#383) kept

The retries happen while the start holds its reservation, the same as a
slow database lookup does today:

- A second start for the connection during the retries is refused as
  "already starting".
- A stop during the retries marks the reservation stopped; the start sees
  it before the next attempt (or at `activateStart`) and gives up without
  writing anything.
- A failed start frees the claim (the existing `defer releaseStart`), so
  the next start is not refused.

## Decisions

- Retry then fail at start, not "start anyway and baseline later". A late
  baseline would bring back #362 (a file dropped between "active" and the
  first read counted as already there). A start that cannot read its
  folder in 7 s has a mount problem an operator needs to see; the
  connection stays as the management API left it, like the other failed
  start paths (bad path, database error).
- A missing folder is a failed read, not an empty folder. The poll does
  not recreate it; the start does, as before.
- Log once per run of failures, then Debug, then Info on recovery.
- Uploads (`/upload`) and `pickSampleFile` are unchanged: they do not use
  `listFiles`.

## Files

- `src/cmd/file-consumer/service.go`: `listFiles`, `pollOnce`,
  `startConnection`, the `readDir`/`startReadBackoff` fields,
  `ActiveConnection.readFailures`
- `src/cmd/file-consumer/known.go`: `loadKnown` returns the read error
- `src/cmd/file-consumer/watch_test.go`, `known_test.go`: the fixtures that
  call `listFiles`/`loadKnown` follow the new signatures
- `src/cmd/file-consumer/dirread_test.go`: new tests (test author)
- `docs/connectors/file.md`: one line on what a failed read does
- `plans/backlog.md`: item 42 struck

## Risks

- A start that fails after its retries leaves the pipeline unwatched until
  someone starts it again or the pod restarts. Today it would start and
  republish the folder. Logged at Error with the connection id.
- The start can now take up to 7 s longer while holding its reservation.
  Only when the folder cannot be read. `sdk.RestoreRunning` starts
  connections one after another, so with the shared volume down at boot
  20 file connections take about 2.5 minutes to restore.
- A stop during the retries is noticed when the current wait ends (up to
  4 s). The stop has already freed the claim, so a new start is not held
  up by it.
- A folder deleted and recreated while running keeps the old names as
  done until a good read shows they are gone. A file put back under an
  old name before that read is not republished; same as backlog item 29.

## Verification

From `src/`:
- `go test -race -count=20 ./cmd/file-consumer/...`
- `go test -race ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `make lint-tenant`
- Mutation: make `listFiles` return an empty set and no error when the
  read fails. The poll and start tests must fail. Restore.

## Verification results

- Tests in `dirread_test.go`, written by a separate test author from the
  criteria above and the existing helpers: a failed poll (once or three
  times, writer finishing or not) changes nothing; a failed start keeps
  the saved set byte for byte and a later start resumes; a start whose
  read recovers on a retry (resume and first start); a poll that recovers
  prunes deleted names and publishes new files once; a stop during the
  retries abandons the start; a really missing folder during a poll.
- Mutation (empty set, no error, run with `go test -overlay` so the source
  was never edited): all five poll and start tests fail. The stop test
  does not depend on it; with the stop check between retries removed it
  fails (test author's run).
- Review: approved. Two follow-ups taken: an abandoned start logs at Info
  instead of Error, and the restore delay is in Risks.
