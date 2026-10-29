# SFTP consumer publishes a file still being uploaded (#562)

Backlog item 107. Open questions: none. Calls made without asking are under
"Decisions".

## Goal

The SFTP consumer (`src/cmd/sftp-consumer`) publishes a remote file only once
the uploader has finished with it, and publishes the finished file once. Same
rule as the file consumer's check (#374): the size and mtime must be the same
on two consecutive looks, at least one settle gap apart (the poll interval
kept between 5 and 30 s; see "Review round 1").

## Acceptance criteria

1. A file is published only once its size and mtime are the same on two
   consecutive polls. A file seen for the first time is never published on
   that poll.
2. A file whose size or mtime changes between polls is not published; the
   wait starts again from the change. Once it stops changing it is published
   once, with its final contents.
3. A file that never stops changing is never published.
4. The two looks must be at least one settle gap apart on the consumer's
   clock. A look that comes sooner (a slow poll followed straight by the next
   one) does not count, and keeps the first sighting.
5. Works with every `after_action` (`none`, `delete`, `move`): the after-action
   runs once, after the single publish of the finished file.
6. Works with the done set from #538: a file already done with the same stamp
   is not tracked or published again, also after a restart; a done file that
   is replaced goes through the same check before it is published again.
7. A one-shot connection (`poll_interval_seconds: 0`) looks twice, a settle
   wait apart, when its first look found files not yet done; a file still
   changing at the second look is not published by that run. A folder with
   nothing new ends after one look, as today.
8. Tests that fail without the change: table-driven, listing faked at the
   `sftpConn` boundary, NATS round trip through `src/pkg/sdk/harness`. A
   mutation run on the size check, the mtime check, the two-polls requirement,
   the clock check and the reset on change.
9. `docs/connectors/sftp.md` says a file must stay unchanged across two polls.
10. `plans/backlog.md` item 107 struck with `(#562)`.

## Files

- `src/cmd/sftp-consumer/service.go`: pending sightings, the check in
  `pollOnce`, a timer instead of a ticker in `runPoller`, the one-shot
  second look.
- `src/cmd/sftp-consumer/stable_test.go` (new): the tests for this issue.
- Existing tests in `src/cmd/sftp-consumer` that expect a publish on the first
  look of a file: they get a second look (or a short one-shot settle wait).
  Their assertions about what is published stay the same.
- `docs/connectors/sftp.md`: one paragraph, and the sentence that says there
  is no stability check.
- `plans/backlog.md`

Nothing outside `src/cmd/sftp-consumer` in Go. No shared package with the file
consumer (another worker owns it); the pattern is copied.

## Approach

`runPoller` owns a `pending map[string]sighting` next to the done set and
passes it to `pollOnce`. A sighting is the file's stamp (`doneset.StampOf`:
size and mtime from the listing) and the consumer clock time it was first
seen with that stamp.

In `pollOnce`, per listed file, after the done-set and pattern checks:

- done with this stamp: drop any sighting, skip (unchanged from #538).
- no sighting, or a different stamp: record (stamp, now), skip.
- same stamp, less than the settle gap since the sighting: skip, keep it.
- same stamp, at least the gap: publish as today. On success drop the
  sighting and mark done. On a failed publish keep it, so the next poll
  retries (today's behaviour).

Names gone from the listing lose their sighting. The settle gap is the poll
interval kept between 5 and 30 s; a one-shot gets 5 s. While files wait, the
next poll comes one settle gap later (review round 1). The clock is a field
(`now`, nil means `time.Now`).

`runPoller` re-arms a timer after each poll instead of a ticker, so polls are
at least one interval apart even after a slow poll (#374's review round).

## Decisions

- The clock check (two looks at least one interval apart) is copied from the
  file consumer's review round (#374) even though the timer alone already
  spaces polls: it also covers the one-shot's two looks and costs one field.
  The time of a look is taken right after `List`, not when the loop reaches
  the file, since the listing is what the check compares.
- One-shot runs get a second look 5 s after the first, only when the first
  found files not yet done. A file still changing then is left for the next
  run (logged as a warning) instead of looping until it settles: a one-shot
  should end.
- A file whose publish fails stays pending, so the next poll retries it as
  before; it does not start the wait again.
- No temp-name skip (`.part`, `.tmp`, dotfiles) like the file consumer has.
  The issue does not ask for it; it is backlog item 117.
- The existing done-set tests that expect a publish after one poll now poll
  twice, and the one-shot tests set a short settle wait. What they assert
  about counts and payloads is unchanged.

## Risks

- Latency: a new file goes out one interval later than today (on the second
  poll that sees it).
- An uploader that pauses longer than one interval mid-file with nothing
  changing still gets a partial publish, then the full file once it changes
  again (#538). Uploaders that write to a temp name and rename are safe.
- A one-shot run now takes up to 5 s longer when it has something to publish.
- Sightings are memory only. A restart starts the wait again from the
  start's first poll; the file goes out one settle gap (at most 30 s) after
  it. See "Review round 1" for why this is no longer one interval.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race -count=1 ./cmd/sftp-consumer/... ./pkg/doneset/...` (with
`VRSKY_TEST_POSTGRES_URL` set for the `done_persist_pg_test.go` tests),
`go test -race ./...`, `make lint-tenant`. New tests run against the code
without the change first and must fail.

## Review round 1

The reviewer found that a poller restarted more often than its poll interval
never published anything: sightings are memory only and the second look had
to be a full interval after the first. A daily pickup (86400 s) on a cluster
parked 16:00–07:00 (pods live at most ~9 h), or any connection whose consumer
a deploy restarts, lost every file. They also found that a 1–2 s interval let
a WAN upload that stalls for a couple of seconds look finished.

Changes:
- The settle gap is decoupled from the poll interval:
  `min(max(interval, 5 s), 30 s)`. A one-shot run gets 5 s. The bounds are
  fields (`settleMin`, `settleMax`) so tests can shorten them.
- `pollOnce` returns how many files it left waiting to settle (after a failed
  connect or listing: every pending file). When that is non-zero, `runPoller`
  arms the next timer for the settle gap instead of the interval, also after
  the start's first poll. Latency is at most interval + 30 s. A failed
  publish is not counted, so a file that keeps failing is retried once per
  interval as before, not every 30 s.
- A file that never stops changing makes the poller look every settle gap
  (one SSH session per look) until it settles or goes. Accepted: it is what
  the check needs to tell when it settles.
- Tests: the reviewer's `TestReview_RestartsShorterThanTheIntervalNeverPublish`,
  adapted: as written it polls once per boot, so no fix could pass it; now
  each day polls as runPoller would until the 16:00 park. Also table tests
  for the gap and the next-poll wait, a real-timer `runPoller` test (hour
  interval, published a settle gap after the first poll, then back to the
  interval), a stop during the settle wait in both modes, gone names pruned
  from `pending`, a failed publish retried, and a failed connect keeping
  files waiting. The rig's `poll()` now waits what `runPoller` would.
- Docs: the paragraph states the gap, the latency and that a restart starts
  the wait again.
- Not fixed here: `TestDonePersistPG_RestartAndTenants` is flaky on main too
  (the one-shot can write "stopped" before the test reads "running"); it gets
  its own issue.

## Review round 2

Approved, with three small changes asked for before merge:

- Round 1 made a failed connect or listing count every pending file as
  waiting, so the poller retried every settle gap (3 login attempts each)
  for as long as the server refused, from the cluster's one egress address.
  A fail2ban-style ban would then lock out every tenant on that host. Now
  the wait doubles after each failed poll in a row (gap, 2×gap, 4×gap, …),
  capped at the poll interval, and a poll that gets through resets it. The
  bookkeeping is a small `pollSchedule` (waiting count and failures in a
  row) that `runPoller` and the test rig share, so the rig cannot drift from
  the loop.
- Tests: `TestStable_UnreachableServerBacksOff` (30, 60, 120, 240 s, then
  30 s again after a poll that gets through), backoff rows in
  `TestStable_NextPoll` including the cap, a failed listing next to the
  failed connect in `TestStable_PendingBookkeeping`, and
  `TestStable_RunPollerKeepsLookingWhileAFileChanges` (real timers, hour
  interval: a file that changes between the first and second look is
  published on the third, a settle gap later), which pins the timer reset
  in the poll loop.
