# Retry a failed after-action in the SFTP and cloud-storage consumers (#640)

Open questions: none. The decisions are listed below.

## Goal

Backlog item 110. With `after_action` `delete` or `move`, a file or object
whose after-action failed is in the done set (#538), so it stays in the
folder or bucket for good. Before #538 a restart published it again and so
retried. Retry the after-action for done names that are still listed, with a
backoff, without publishing again.

## Affected files

- `src/pkg/doneset/retry.go` (new), `doneset.go`: the per-name backoff.
- `src/cmd/sftp-consumer/service.go`: `pollOnce`, `wantsAfterRetry`,
  `retryAfterAction`.
- `src/cmd/cloud-storage-consumer/service.go`: `pollOnce`, `ingest`
  (split out of `ingestObject`), `wantsAfterRetry`, `retryAfterAction`,
  a `now` clock.
- Tests: `pkg/doneset/retry_test.go`, `after_retry_test.go` in both
  consumers.
- `docs/connectors/sftp.md`, `docs/connectors/cloud-storage.md`,
  `plans/backlog.md` (strike 110, add 150).

## Approach

- The backoff lives on `doneset.Set` (`RetryDue`, `RetryFailed`,
  `RetryDone`, `Retrying`). Both consumers already pass the Set to
  `pollOnce`, so no signature changes, and `Observe` already walks the
  listing: a name that leaves it drops its backoff, so a name removed by
  someone else stops being retried and a name added again starts fresh.
- State is memory only. After a restart nothing is tracked, so a leftover is
  due at once: tried once at the first poll, then backed off. That is what
  a restart did before #538, without the republish.
- Backoff: 1 min after a failure, doubling, capped at 1 h. At most 20 retries
  per poll, so a folder of leftovers does not hold one poll.
- In `pollOnce`, where a name is `Done` at its listed stamp and still listed:
  if `after_action` is delete/move, the name matches `file_pattern` (and is
  not a temp name, SFTP), and it is due, run the same `afterAction` as the
  first try. A replaced file has another stamp, so it is not done and is
  published again as before; the retry never touches a changed file.
- The first try's failure goes through the same backoff (`RetryFailed`), so
  the first retry is a minute later, not at the next poll.
- Logging: the first failure and every failed retry log one Warn
  (`failures`, `retry_in`); success logs one Info. A poll that skips a name
  because its backoff has not run out logs nothing.
- Metric: the two consumers have no Prometheus metrics today, so none is
  added. The Warn lines carry the counts.

## Decisions

- `move` to a destination that already exists. SFTP: `realSFTP.Rename` is a
  POSIX rename that overwrites; retrying after a partial move behaves like the
  first try. A server without that extension falls back to a plain rename
  that fails on an existing name; that already fails on the first try and
  now backs off instead of failing every poll. Documented, not changed.
  Cloud storage: a move is copy then delete; a copy that landed with a
  delete that failed is copied over the same destination again (idempotent),
  then deleted.
- Switching `after_action` from `none` to `delete` or `move` now removes
  files already published and still listed (they are "done and still there").
  That follows the issue and the configured intent, and is limited by
  `file_pattern` and by the unchanged stamp. Documented.
- Retries need the name to match `file_pattern` (and not be a temp name), so
  a file the config no longer selects is not touched.
- Event mode (`runEventLoop`) has the same hole and is left as it is: it has
  no listing and no done set, the message is acked after the publish, and
  returning the error would redeliver the message and publish a duplicate.
  Behaviour is unchanged (`ingestObject` still logs and moves on). Filed as
  backlog item 150.

## Risks

- A deleting retry racing an old pod in a rolling deploy: both try, one gets
  a not-found error and logs a Warn. No data loss.
- A retried `delete` for a file that is done but was meanwhile replaced with
  an identical size and mtime: same limit as the done set itself (#538).

## How we verify

- Table-driven tests in both consumers with a store whose delete/move fails
  N times: published once, retries spaced by the backoff, the name finally
  removed; backoff stops growing at the cap; one Warn per attempt;
  `after_action: none` never calls the store; a name removed by someone else
  stops being retried; a restart retries leftovers once; leftovers are
  bounded per poll and pattern-bound.
- The tests fail with the retry switched off and with the backoff switched
  off (mutation run, both done by hand).
- `go build`, `go vet`, `golangci-lint run ./...`, `go test -race` on the
  three packages, `make lint-tenant`.
