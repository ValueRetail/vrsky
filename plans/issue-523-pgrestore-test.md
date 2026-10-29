# Unit tests for `pgRestore` in vrsky-cli (#523)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

`pgRestore` in `src/cmd/vrsky-cli/restore.go` decides which error an
operator sees when a restore fails, and it is the only place a chunked
archive's final chunk and the gzip checksum get checked once pg_restore
has stopped reading. Nothing tests it; the DR drill only runs the happy
path. Pin its behaviour with a fake `pg_restore` on `PATH`.

The precedence it implements:

1. The archive read failed (truncation, decryption, gzip): report
   `read backup: …`, whatever pg_restore did with its cut-off input.
2. Otherwise pg_restore exited non-zero: report `pg_restore: …` with its
   stderr.
3. Otherwise drain the rest of the archive. A read error there is
   `backup archive failed verification after pg_restore finished: …`.

## Acceptance criteria

1. A good archive passes, whether the fake reads all of stdin or exits 0
   early. The returned byte count is the full dump length.
2. A truncated archive fails, both when the fake reads everything and
   exits 0, and when it exits 0 after a few bytes.
3. A truncated archive that the fake reads to the end and then exits 1
   on reports the read error (`read backup`), not pg_restore's exit.
4. pg_restore exiting 1 on a good archive surfaces as `pg_restore: …`,
   with the fake's stderr text in the message.
5. A damaged final chunk after the fake exits 0 early still fails, with
   the post-exit verification error. The archive is large enough (MBs,
   incompressible) that the exec stdin copy cannot have reached the last
   chunk before the fake exits, so this goes through the drain. The same
   with a bad gzip CRC in an otherwise valid archive. If the fake exits 1
   early on the damaged archive, its error wins over the drain's.
6. The fake records its argv; the test checks `--single-transaction`,
   `--clean`, `--if-exists` and `--dbname <target>`.
7. Mutation runs, each of which must fail at least one test:
   - drop the `in.err` branch
   - drop the `runErr` branch
   - drop the drain
   - swap the `in.err` and `runErr` checks
   - move the drain ahead of the `runErr` check (found in review: a
     pg_restore that fails early on a damaged archive must still report
     its own error)
   - drop `--single-transaction` from the args
8. From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
   `go test -race ./...`, `make lint-tenant` pass.
9. Backlog item 83 struck with `(#523)`.

## Files

- `src/cmd/vrsky-cli/pgrestore_test.go` (new)
- `plans/backlog.md`

No production code changes.

## Decisions

- The fake is a `/bin/sh` script written into `t.TempDir()` and put first
  on `PATH` with `t.Setenv`. It reads its behaviour from environment
  variables the child inherits (bytes to read or all, exit code, stderr
  text, argv file), so one script covers every case. `head -c` and `cat`
  do the reading. Skipped on Windows; CI runs Linux.
- Archives come from the existing helpers in `stream_test.go`
  (`seal`, `gzipBytes`, `openArchive`), so the test reads exactly what a
  real restore reads.
- Error branches are told apart by message prefix, since the three
  branches wrap different text and the truncation error doesn't always
  survive gzip as a sentinel.
