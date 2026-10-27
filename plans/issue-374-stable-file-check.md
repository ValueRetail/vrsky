# File watcher can publish a half-written file (#374)

Open questions: none. One call made without asking is listed under
"Decisions".

## Goal

The directory watcher in `src/cmd/file-consumer` publishes a file only once
whoever writes it has finished, and publishes the complete file exactly once.

## Acceptance criteria (from the issue)

1. A file is ingested only once its size and modification time are unchanged
   across two consecutive polls. Dotfiles and common temp names (`*.tmp`,
   `*.part`, `~*`) are skipped until renamed.
2. A file still growing at the first poll is published once, complete, after
   it stops changing. A file already stable at its first poll is ingested on
   the next poll.
3. The baseline taken at start (#366) still keeps files present before the
   connection started from being ingested.
4. Tests drive the watcher through polls deterministically (a poll trigger,
   not sleeps) and assert what gets published: a file written in two parts
   across a poll is published once with the full content; a temp file renamed
   to its final name is published once; a stable file is published on the
   next poll; pre-existing files are never published.
5. `go test -race -count=20 ./cmd/file-consumer/...` passes.
6. `plans/backlog.md` item 19 struck with `(#374)`.

## Files

- `src/cmd/file-consumer/service.go`
- `src/cmd/file-consumer/consumer_test.go`, `restore_test.go` (poll interval
  for the round-trip tests, see Risks)
- a new `src/cmd/file-consumer/watch_test.go` (the test author's)
- `plans/backlog.md`
- `docs/connectors/file.md` (added in review round 1)

No code outside `src/cmd/file-consumer`. Tenant path resolution, directory
creation and its permissions, `ingestWatchedFile` and the streaming path stay
as they are.

## Approach

Split the body of the ticker loop into `pollOnce(ctx, ac)`. `watchDirectory`
calls it on every tick; tests call it directly, which is the poll trigger.
The interval becomes a field (`pollInterval`, zero means the 5 s default) so
the NATS round-trip tests can poll faster.

Per connection the watcher keeps two things:

- `knownFiles map[string]bool` (unchanged type): names that are done. The
  start baseline, plus every name that was published or failed to ingest.
  Never ingested again while the name exists.
- `pending map[string]fileState`: names seen but not yet published, with the
  size and mtime from the last poll.

On each poll, for every non-directory entry (`os.Stat` on the path, so the
check measures what `ingestWatchedFile` reads):

- in `knownFiles`: skip.
- a temp name (starts with `.` or `~`, or ends in `.tmp` / `.part`, suffix
  case-insensitive): skip and do not track. When it is renamed to its final
  name, that name is new and goes through the normal path.
- in `pending` with the same size and mtime: ingest, move to `knownFiles`.
- otherwise: record its size and mtime in `pending`, wait for the next poll.

Names gone from the directory are dropped from both maps; a known one emits
the "deleted" UI event as today. A name that comes back later is new again,
also as today. An ingest error still marks the name known (no retry every
5 s), as today.

A file that grows between polls keeps resetting its pending entry, so it is
published once, after the first pair of polls that see it unchanged, with its
full content. The baseline is still taken in `startConnection` before the
connection shows as active and goes straight into `knownFiles`, so
pre-existing files are never pending and never published.

## Decisions

- Latency: a new file is now published on the second poll that sees it
  instead of the first, so 5–10 s after it lands instead of 0–5 s. That is
  the literal reading of "unchanged across two consecutive polls" and of "a
  file already stable at its first poll is ingested on the next poll". The
  shortcut of trusting an mtime older than the last poll was left out: it
  rests on the file server's clock agreeing with ours, which an Azure Files
  share does not promise.
- Temp names: exactly the issue's list (`.` prefix, `~` prefix, `.tmp`,
  `.part`). Other tools' suffixes (`.filepart`, `.crdownload`) are still
  caught by the stability check unless the writer pauses longer than a poll.

## Risks

- Latency, above. The NATS round-trip tests (`WatchRoundTrip`,
  `RestoresOnBoot`) waited 12 s and 10 s for one 5 s poll; two polls would
  leave no margin. They set a 200 ms poll interval instead of a longer
  deadline, which also makes them faster. Their assertions do not change.
- Memory: one small entry per pending file, bounded by the directory's size,
  same order as `knownFiles` today.
- A file that changes forever (an appended log) is never published. That is
  correct: it is never finished.
- A writer that pauses longer than one poll interval mid-file still gets a
  truncated publish. Writers that rename into place are safe.
- Rename into place: the temp name is skipped, the final name appears with
  its final size, and is published one poll later. Rename over a known name
  is not republished, as today.

## Verification

From `src/`:
- `go test -race -count=20 ./cmd/file-consumer/...`
- `go test -race ./...` once, `go vet ./...`, `golangci-lint run ./...`,
  `make lint-tenant`
- Mutation: ingest on first sight (drop the stability check) and the
  two-part test must fail; drop the temp-name skip and the rename test must
  fail. Restore and re-run.

## Review round 1

The reviewer found that the stability window could collapse. With
`time.NewTicker`, a poll slower than the interval (a big file being
published) was followed at once by the next tick, so a file stat'ed late in
the slow poll was stat'ed again a few milliseconds later, looked stable, and
went out truncated. They reproduced it through the real `watchDirectory`
every time.

Changes:
- `watchDirectory` uses a timer re-armed after each `pollOnce`, so polls
  are at least one interval apart.
- `pollOnce` also requires at least one interval on the watcher's clock
  between the first sighting of a size and mtime and the look that publishes
  it. A look that comes too soon keeps the first sighting. The clock is a
  field (`now`, nil means `time.Now`) so tests drive it. This check alone
  closes the bug, whatever the loop does; the timer keeps normal polls from
  bunching up.
- New tests (separate author): mtime moved with `os.Chtimes` at the same
  size restarts the wait; a poll a few ms behind the first sighting does not
  publish, the first sighting survives it, and a write in between restarts
  the wait. The fixture's `poll()` now means "one interval passed, then
  poll" on a fake clock.
- `docs/connectors/file.md` describes the skipped names, the 5–10 s delay and
  rename-into-place. A skipped temp name is logged at debug level.
- Backlog items renumbered to follow main (24, 25); 25 is the reviewer's
  point that files still settling at a stop or restart land in the new
  baseline and are never published.

Not covered by a test: swapping the timer back to a ticker. With the clock
check in place that no longer publishes a truncated file, so a deterministic
test cannot tell the two apart, and a real-time one would need a writer that
keeps pace under `-race -count=20`.
