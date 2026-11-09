# salesforce-producer: wait for Bulk API jobs and dead-letter what they refuse (#760)

Backlog item 191. Found in #735 (PR #759). No open questions; the
decisions below were made by the issue worker and are listed in the PR.

## Goal

`bulkWrite` creates a Bulk API 2.0 job, uploads the CSV, closes the job and
acks. It never looks at the job again, so a record Salesforce refuses while
it processes the job reaches no queue and no log. And when the answer to the
close is lost, the retry creates a second job, so an `insert` duplicates
every record.

## Acceptance criteria

- After the close, the producer polls the job state with backoff (1 s,
  doubling, at most 30 s between polls) until `JobComplete`, `Failed` or
  `Aborted`, for at most `bulkPollTimeout` (2 minutes) per delivery
  attempt. The SDK's dispatch loop marks the message in progress while
  Deliver runs (#139), so the poll does not trigger a redelivery.
- A poll that runs out of its bound returns a retryable error that names the
  job and its state. The message is NAK'd, never acked; the redelivery
  resumes polling the same job; after five attempts it is dead-lettered.
- Then the producer reads `failedResults` (when the job reports failed
  records) and `unprocessedrecords` (when a `Failed`/`Aborted` job did not
  process everything), and matches each row back to its record by content
  (the rows come back in no fixed order). Each refused record is named in
  the error by its index in the payload with Salesforce's code and
  message, as on the REST path since #735; a record the job never processed
  is named too. The records that went in are added to the skip set, so the
  redelivery submits a new job with only the refused and unprocessed ones.
- A result row that cannot be matched to a record, a result body over the
  size cap, or a missing column: the delivery fails, nothing is resent (the
  job stays remembered), so the message ends in the DLQ instead of risking
  duplicates.
- A retry after a lost close finds the job: the job id is remembered per
  message and target as soon as Salesforce answers the create. On the retry
  the producer reads the job state: `Open` (the close never arrived, or the
  upload failed) is aborted and a new job is created; any other state is
  polled and resolved as above.
- A failed upload aborts the open job. If the abort fails too, the job stays
  remembered and the retry aborts it.
- Delivery semantics unchanged elsewhere: non-JSON is `sdk.Permanent`;
  everything else is retryable, five attempts, then the DLQ.

## Mechanism for finding the job: in-process memory

Bulk API 2.0 ingest jobs carry no caller-supplied tag, so a job cannot be
found later by the envelope id. Listing open jobs and matching by object,
operation, user and time would adopt another message's job, which loses
this message's records. So the job id lives in process memory next to the
skip set (same key, same TTL and bound). A redelivery handled by another
process (a restart, a rolling deploy while a job is being polled) does not
see it and submits a second job. That is the same residual the skip set
has since #735; #727/#758 (a durable skip set) would close it for both.

## Files

- `src/cmd/salesforce-producer/service.go`: `bulkWrite` split into create,
  upload, close, poll and resolve; `doRequest` gets a result-size variant.
- `src/cmd/salesforce-producer/bulk.go` (new): job memory, polling, result
  parsing and row matching.
- Tests: `bulk_test.go` (new, stateful fake Bulk API on httptest, harness
  tests on the embedded JetStream), the existing fake in
  `rejected_test.go` learns the job-state and result endpoints,
  `producer_test.go`'s harness Bulk test answers the job-state GET.
- Docs: `docs/connectors/salesforce.md`, the Salesforce row in
  `docs/operator/troubleshooting.md`, `plans/backlog.md` item 191.

## Risks

- A delivery now takes as long as the job (up to 2 minutes per attempt).
  The SDK dispatches one message at a time, so other Salesforce messages
  wait behind it.
- Matching by content depends on Salesforce echoing the uploaded values
  unchanged. When it does not, the fallback is the DLQ, not a resend.
- The restart residual above.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race -count=1 ./cmd/salesforce-producer/... ./pkg/sdk/...`; from
the root `make lint-tenant` and `make lint-connector`. Mutation checks on
the job memory (lost close), the failed-results handling, the poll bound
and the abort on a failed upload.

## Review round 1 (PR #777)

- A complete job that processed fewer records than it was given fails the
  delivery and keeps the job (nothing resent). The uploaded CSV is read back
  before the create and must give one row per record; a record a
  single-column CSV cannot carry (no value) is refused locally as
  `nothing to send`.
- The matcher compares result rows with the uploaded CSV read back through
  `csv.Reader`, so `\r\n` inside a value is normalised on both sides.
- Result headers are matched without regard to case.
- Docs: what a DLQ retry of a Bulk message does in the same process and
  after a restart.
