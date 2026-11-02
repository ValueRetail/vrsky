# BC consumer: a failed checkpoint save must not turn a cursor_field change into a full re-read (#638)

Backlog item 145, found in review of #614.

## Problem

`saveProgress` (`src/cmd/business-central-consumer/service.go`) logged a
failed `Save` and carried on. After a `cursor_field` change (#610) the stored
row still names the old field. If the save of the first poll on the new field
fails, or the poll's context is cancelled at shutdown just before it runs, the
next poll sees "stored field differs", drops the watermark and sends the whole
entity again. The records were already published, so it costs repeats, not
data, but the repeats are the whole entity.

Related: the `Get` that `saveProgress` does to keep the stored watermark
(empty cursor, no replace) ignored its error, so a read that failed on a
cancelled context saved an empty watermark over the real one.

## Acceptance criteria

- `saveProgress` returns its error. A failed attempt is retried once on a
  context that is not cancelled with the poll's (`context.WithoutCancel`),
  so a save at shutdown still completes.
- Every attempt has its own timeout (5 s, field `saveTimeout` for tests), so
  a hanging store cannot hang the shutdown for more than two attempts.
- A store that cannot be read is not answered with an empty watermark.
- A poll after a cursor_field change whose save still fails keeps the
  checkpoint it meant to store (`cfg.unsaved`) and returns the error. The next
  poll stores it before it reads anything, and reads nothing while that fails.
  Then the stored row names the new field and nothing is read from the start.
- A save that fails without a field change returns the error too, but does
  not hold the next poll: it costs a few repeats, as before.
- At-least-once is unchanged: the checkpoint never moves past records that
  are not published. The save runs after every page has been published, as it
  did, and the flush stores the same values that save would have.

## Decisions

- **Both options from the issue, not one.** Retry-once alone does not help
  when the store is down longer than a few seconds: the next poll would find
  the stale row. Remembering the unsaved checkpoint in memory covers that.
  Cost: while the store is down after a field change, polls are skipped
  rather than read and published again. Skipping loses nothing: the source
  still has the records, and the watermark has not moved.
- **Only for a field change.** Skipping polls on every failed save would
  turn a database blip into an outage for a case that only repeats a few
  records.
- **In memory only.** A restart before the flush loses it, and the entity is
  read from the start once. Persisting it would need the same store.
- **Errors go up.** `fetchAndPublish` returns the save error, so
  `runPoller` logs "fetch failed" once. A cancelled poll returns it as well;
  `runPoller` already ignores errors once its context is done.

## Files

- `src/cmd/business-central-consumer/service.go`
- `src/cmd/business-central-consumer/save_retry_test.go` (new)
- `docs/connectors/business-central.md`, `plans/backlog.md`

## Verification

Table tests with a store that works / fails once / fails twice / hangs, and a
poll cancelled right after it published. They assert what the next poll
asks Business Central (the new field's watermark, not an unfiltered read) and
what it publishes (nothing new), and that a hanging save is bounded.
Mutations checked: no retry, and no remembered checkpoint, each fail the
tests.

`go build`, `go vet`, `golangci-lint run ./...`,
`go test -race ./cmd/business-central-consumer/... ./pkg/checkpoint/...`,
`make lint-tenant`.
