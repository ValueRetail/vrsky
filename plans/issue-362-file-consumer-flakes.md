# File-consumer tests flake under a full race run (#362)

Open questions: none. The causes were reproduced on purpose (below), so the
fix does not rest on a guess.

## Goal

`TestFileConsumer_WatchRoundTrip` and `TestFileConsumer_RestoresOnBoot`
(`src/cmd/file-consumer`) pass under load, waiting on the event they need
instead of on time, without asserting less.

## Acceptance criteria (from the issue)

- Find why each can miss its window and make it wait on the event it needs.
- `-race -count=30` on the package, and the full module once, with no
  failure; the cause described in the PR.
- A real consumer bug found on the way is fixed in the same PR and called out.
- `plans/backlog.md` item 13 struck with `(#362)`.

## What the code does

`startConnection` registers the connection in `activeConnections`, writes
status `running`, then starts `watchDirectory` in a goroutine. That goroutine
takes the baseline snapshot of the directory (files already there are never
ingested) and then polls every 5 s; a name missing from the snapshot is read
and published.

Both tests wait until the connection shows up in `activeConnections` and then
write a file, expecting it to be ingested on the first tick.

## Causes

1. Baseline snapshot after "active" (both tests; a consumer bug). The
   connection is visible as active, and marked `running` in the database,
   before the watcher goroutine has listed the directory. A file written in
   that gap lands in the baseline and is never published. The tests poll
   every 10 ms and write as soon as they see the connection, so a goroutine
   scheduled late under load loses the file: "no matching envelope within
   12s" (WatchRoundTrip) or "within 10s" (RestoresOnBoot). Production has
   the same gap: a file dropped right after the UI shows the pipeline running
   can be silently treated as pre-existing.
   Reproduced: a 50 ms sleep at the top of `watchDirectory` fails
   WatchRoundTrip every time with the exact message from the issue. For
   RestoresOnBoot it also needs a slow start (next point), since the test only
   starts polling after a 200 ms sleep.
2. Unlocked map allocation in `Configure` (RestoresOnBoot; a data race).
   `Configure` assigns `s.activeConnections` without `s.mu`, on the runner's
   goroutine. RestoresOnBoot reads that field under `s.mu.RLock` and relies on
   a 200 ms sleep to come after it. A sleep is no happens-before edge, so when
   the runner starts late the race detector reports it and fails the test.
   Reproduced with a 300 ms sleep at the top of `Configure`. The reviewer
   pointed out the same race reaches WatchRoundTrip and UploadRoundTrip
   through `getActiveConnection`, whose only happens-before edge with
   `Configure` was the NATS connection's mutex; with the injected delay all
   three tests report it.
3. Start command published after a fixed 200 ms sleep (WatchRoundTrip, and
   UploadRoundTrip which uses the same pattern). The command subscription is
   made in `Run`, after `Configure`. If the runner is slower than 200 ms the
   command goes to a subject nobody listens on and the connection never
   starts: "connection active" not met within 5 s. Reproduced with the same
   300 ms `Configure` delay.

Not causes: temp dirs and NATS servers are per test (`t.TempDir`, port -1);
tenants and connection IDs differ per test; the harness's data subscription
is on the same connection the consumer publishes on, so it is in place first.
The 5 s poll with a 10–12 s deadline leaves one spare interval and is a
deadline on the event, not a sleep. There is no stable-size check, which is a
separate gap (below), not a cause of these failures.

## Approach

Consumer (`service.go`), ordering only:
- Take the baseline snapshot in `startConnection` before the connection is
  stored in `activeConnections` and before status `running` is written; the
  goroutine starts from `ac.knownFiles` instead of listing again. "Active"
  then means "anything written from now on is new".
- Allocate `activeConnections` in `Configure` under `s.mu`.

Tests:
- A helper starts a connection by publishing the start command and
  republishing it until the connection is active (start is idempotent:
  "already active" returns before touching the DB), with one overall deadline
  and a clear failure. Used by WatchRoundTrip and UploadRoundTrip; replaces
  the 200 ms sleeps.
- RestoresOnBoot drops its 200 ms sleep; the lock in `Configure` makes the
  poll safe.
- New `TestStartConnection_BaselineTakenBeforeActive`: with a file already
  in the directory, once `startConnection` returns the connection's baseline
  holds that file. Pins the ordering the round-trip tests depend on.
- Assertions stay as they are: same payload, integration ID, content type
  and DB expectations.

## Files

- `src/cmd/file-consumer/service.go`
- `src/cmd/file-consumer/consumer_test.go`
- `src/cmd/file-consumer/restore_test.go`
- `plans/backlog.md` (strike 13; add the partial-write gap as a new item)

Nothing outside `src/cmd/file-consumer`: the harness is fine as it is.

## Risks

- Listing the directory moves from the goroutine to the start path: one
  `ReadDir` on the NATS handler goroutine. Same cost as before, just earlier.
- Behaviour change for a file dropped in the start window: it is now ingested
  instead of silently ignored. That is the intended semantics.
- No tenant-isolation or permission code changes (the directory is resolved
  and created exactly as before), so no `agent:needs-human`.

## Verification

From `src/`:
- The two tests with `-race -count=30`, plain and under CPU pressure, zero
  failures (before/after counts in the PR).
- `go test -race ./cmd/file-consumer/...`, full `go test -race ./...` once,
  `go vet ./...`, `golangci-lint run ./...`.
- Mutation: move the snapshot back into the goroutine; the new test and (with
  a 50 ms watcher delay) WatchRoundTrip must fail. Remove the lock in
  `Configure`; RestoresOnBoot must report the race under a slow start.

## Found on the way (backlog, not this PR)

The watcher ingests a new file on the first tick that sees its name, with no
check that the writer is done. A large file copied in slowly can be published
half-written, and since its name is then known the complete file is never
published. Needs a stable-size (or mtime) check across two polls.
Backlog item 19.

The "already active" check and the insert in `startConnection` are separate
lock sections, and the boot restore runs alongside the start-command handler,
so a start command arriving during restore could start a second watcher.
Not hit by these tests (the restore query fails on the mock in the
command-driven ones). Backlog item 20.

## Review

An independent reviewer read the code and the diagnosis before the tests
changed: confirmed causes 1 and 2 (2 wider, above), confirmed 3 as real but
not the source of the recorded message, found no further cause, and checked
that repeating the start command cannot consume a sqlmock expectation twice.
