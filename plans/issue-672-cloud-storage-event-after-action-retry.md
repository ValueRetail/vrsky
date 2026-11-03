# Retry a failed after-action in cloud-storage event mode (#672)

Open questions: none.

## Goal

Backlog item 151. In event mode `ingestObject` logs a failed `delete` or
`move`, the message is acked, and the object stays in the bucket for good:
there is no listing and no done set. Poll mode retries since #640. Retry the
after-action from memory, without fetching or publishing again.

## Affected files

- `src/cmd/cloud-storage-consumer/event_after_retry.go` (new): the queue.
- `src/cmd/cloud-storage-consumer/service.go`: `runEventLoop` starts the
  queue, `ingestObject` hands it a failed after-action, `afterAction` marks
  the move-onto-own-key refusal with a sentinel, `cloudConsumer.afterRetry`
  (tuning for tests).
- Tests: `event_after_retry_test.go`.
- `docs/connectors/cloud-storage.md`, `plans/backlog.md` (strike 151).

## Approach

- One `afterRetryQueue` per event loop, only for `delete` and `move`. A map
  keyed by object key (one entry per key), at most 1000 entries.
- One goroutine runs it, with a timer for the earliest due key. It stops with
  the loop's context; `runEventLoop` cancels it and waits for it on return,
  so no goroutine outlives the connection.
- Backoff is the poll-mode one (`doneset.RetryBase` 1 min, doubling, capped
  at `doneset.RetryMax` 1 h). Up to 8 retries after the first failure, about
  three hours in all. Then Error log with bucket and key, and the key is
  dropped. A full queue drops the new key with the same Error log.
- `objectstore.ErrNotFound` from the first try or a retry means done.
- The message ack is unchanged: it follows the publish, not the after-action.
- A key that fails again after being published again starts over; a later
  successful after-action for a key forgets its queued retry.

## Decisions

- Not queued: the refusal to move an object onto its own key (#567). It
  cannot change, so the Warn stays and no retry runs.
- Queue and attempt limits are constants, not config: event mode has no
  other tuning for this and the numbers only bound memory and time.
- No metric: the consumer has none (same call as #640).
- The queue is in memory by decision of the issue; a restart loses it.

## Risks

- A key replaced after its failure and before its retry is deleted or moved.
  The replacement's notification publishes it and drops the queued retry, but
  if the retry runs first the new object goes unpublished and its
  notification then finds nothing (`ErrNotFound`, treated as handled). The
  first retry is a minute after the failure, so this needs a notification
  that is more than a minute late. Event mode has no object stamp to compare.
  In the docs.
- A retry that outlives a move whose copy landed: the copy is repeated over
  the same destination, then deleted. Idempotent.

## How we verify

- Table-driven loop tests on the event loop with a scripted store:
  fails then works (delete and move), always fails and stops at the cap with
  an Error, ErrNotFound on a retry and on the first try, move onto own key
  not retried; each checks published once and acked once.
- Queue full (dropped with Error, duplicate key takes no place), forget on a
  later success, stop in backoff, stop while a retry is in flight (the loop
  returns, which means the goroutine is gone), backoff values.
- Run with `-race`. Mutation: the queue switched off in `runEventLoop` fails
  the loop and stop tests.
- `go build`, `go vet`, `gofmt -l`, `golangci-lint run ./...`,
  `go test -race`, `make lint-tenant`.
