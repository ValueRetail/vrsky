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
   saved set. The start goes ahead with the baseline deferred (below).
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
- `startConnection`: if `loadKnown` fails, the start goes ahead with
  `ac.baselineDeferred` set. It takes the #375/#383 reservation path as
  usual, writes `running`, and starts the watcher. It logs once at Warn
  with the connection id, and sets `readFailures` to 1 so the polls that
  follow log at Debug. Nothing is saved: `dirty` is false.
- `pollOnce` while the baseline is deferred (`takeDeferredBaseline`): it
  calls `loadKnown` again. A failed read changes nothing. The first read
  that works sets the baseline exactly as a start would: the saved set if
  there is one for this folder, otherwise first-start rules. It then saves
  if that changed anything (always on a first start), logs Info, and
  returns without publishing. From the next poll on it runs normally, so
  a new file is pending on one poll and published on the next, the same
  as after a normal start.

## Review change: defer the baseline instead of failing the start

The first version retried the read 4 times (1, 2 and 4 s apart), then
refused the start. Review pointed out that this left the connection
`running` in the database and the UI with nothing watching the folder.
After a pod restart during a short share outage, files would stop flowing
silently until someone noticed. The deferred baseline keeps the watcher
running and needs no retry loop, so a boot restore with the volume down
no longer costs about 7 s per connection.

## Reservation rules (#383) kept

The start path is the same as before apart from the deferral flag:
reserve, look up, `activateStart` (which still gives up if a stop took the
claim), save if dirty (never, when deferred), write status, start the
watcher. A stop while deferred is an ordinary stop of an active
connection: it cancels the watcher, and nothing was saved.

## Decisions

- Start with the baseline deferred, not "retry then fail" (review, above).
- Trade-off on a first start (no saved set): a file that lands while the
  folder is unreadable is in the folder at the first good read, so it
  counts as already there and is not published. That is #362 again, but
  only for the window in which the folder can't be read at a pipeline's
  very first start. With a saved set nothing is lost: a file that is not
  in the set is published.
- A missing folder is a failed read, not an empty folder. The poll does
  not recreate it; the start does, as before.
- Log once per run of failures, then Debug, then Info on recovery.
- Uploads (`/upload`) and `pickSampleFile` are unchanged: they do not use
  `listFiles`.

## Files

- `src/cmd/file-consumer/service.go`: `listFiles`, `pollOnce`,
  `takeDeferredBaseline`, `noteReadFailure`, `startConnection`, the
  `readDir` field, `ActiveConnection.readFailures` and `baselineDeferred`
- `src/cmd/file-consumer/known.go`: `loadKnown` returns the read error
- `src/cmd/file-consumer/watch_test.go`, `known_test.go`: the fixtures that
  call `listFiles`/`loadKnown` follow the new signatures
- `src/cmd/file-consumer/dirread_test.go`: new tests (test author)
- `docs/connectors/file.md`: one paragraph on what a failed read does
- `plans/backlog.md`: item 42 struck

## Risks

- First start with an unreadable folder: see the trade-off under
  Decisions.
- A connection whose folder stays unreadable shows as running and
  publishes nothing. Its log has one Warn with the connection id, then
  Debug lines until the read works.
- A folder deleted and recreated while running keeps the old names as
  done until a good read shows they are gone. A file put back under an
  old name before that read is not republished; same as backlog item 29.

## Verification

From `src/`:
- `go test -race -count=20 ./cmd/file-consumer/...`
- `go test -race ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `make lint-tenant`
- Mutations, each run through `go test -overlay` so the source is never
  edited:
  - `listFiles` returns an empty set and no error when the read fails.
    The poll and start tests must fail.
  - Publish before the baseline is taken (a failed start read no longer
    defers, so the watcher starts with an empty done set). The deferred
    start tests must fail.

## Verification results

- `go test -race -count=20 ./cmd/file-consumer/...`, `go test -race ./...`,
  `go vet ./...`, `golangci-lint run ./...` (0 issues) and
  `make lint-tenant`: all pass.
- Tests in `dirread_test.go`, written by a separate test author from the
  criteria and the existing helpers:
  - a failed poll (once or three times, writer finishing or not) changes
    nothing
  - deferred start: resume from a previous run, resume from a seeded
    checkpoint, and a first start
  - a deferred start never overwrites the saved set
  - a stop while deferred (stop command and pod shutdown)
  - a poll that recovers prunes deleted names and publishes new files once
  - a folder really deleted during a poll
- Mutations, through `go test -overlay`:
  - empty set, no error: every `TestDirRead_*` test fails
  - a failed start read no longer defers (publish before the baseline):
    `TestDirRead_DeferredBaseline` fails ("3 envelopes published in total,
    want 1")
  - the test author's runs: no `return` after the deferred baseline is
    caught by both resume cases, and a deferred start marked dirty is
    caught by the deferred, never-overwrites and stop tests
- First review approved the retry-then-fail version. The coordinator's
  review asked for the deferred baseline (see "Review change").
