# salesforce-producer: dead-letter rejected records, don't resend the ones that went in (#735)

Backlog item 181. No open questions; the decisions below were made by the
orchestrator and are listed in the PR.

## Goal

`restWrite` logs a `4xx` per record and moves on, so the message is acked
and the rejected record is in no queue. When another record in the same
message fails transiently, the whole message comes back and the records that
already went in are posted again, so `insert` creates duplicates. The Bulk
API path retries every non-`2xx`, so the two paths disagree.

## Acceptance criteria

- A record Salesforce answers with a `4xx` (validation, duplicate, bad
  field) fails the delivery with a retryable error, so the message is
  retried and then dead-lettered. The error names the record by its index
  in the payload and carries Salesforce's `errorCode` and `message`. It
  never carries the token or request headers.
- Exception: on `upsert`, when every rejected record of a target is
  rejected only as a duplicate (`DUPLICATE_VALUE`, `DUPLICATES_DETECTED`),
  the target counts as done (Warn log, no error).
- A record with no external-id value on `upsert`, or one that cannot be
  encoded, is a rejected record too (same error path), not a silent skip.
- Records that went in are not sent again when the message comes back: an
  in-process skip set keyed by connection, envelope id, node, instance,
  object, operation and record index. Bounded (10 000 entries) with a 7-day
  TTL (the DLQ retention, so a retry from the DLQ in the same process skips
  them too), as in db-producer (#705). Per process: a redelivery handled by
  another process sends everything again.
- A transient failure (network, token, `401`/`403` after the one token
  refresh, `408`, `429`, `5xx`, a redirect) stops the target's loop and
  returns a retryable error; on the retry only the records not yet written
  are sent.
- The Bulk path uses the same classification and error text. A Bulk job
  that was submitted for a target is recorded in the skip set (one entry
  for the whole message, index -1), so a redelivery caused by another
  target does not submit it again.
- Several targets: errors are joined, each prefixed with its node id.
- Unchanged: non-JSON payload is `sdk.Permanent`; no `object` or `upsert`
  with no `external_id_field` is logged and acked (that is #734's
  question); the #704 config-read behaviour.

## Decisions

- Dead-letter through the normal retry path (`sdk.Retriable`), not a new
  "dead-letter now" error kind: the SDK has none, and adding one is an SDK
  contract change. A rejected record is tried five times; the cost is four
  extra requests for that record only, because the written ones are
  skipped.
- The rejected record is not split off as its own message; the whole
  message goes to the DLQ, and a retry from there sends only the records
  that are not in the skip set (same process, within 7 days).
- `300 Multiple Choices` (an upsert whose external id matches several
  records) is a rejected record. Other `3xx` are transient: they are about
  the instance URL, not the record.
- A transient error stops the loop for that target instead of trying the
  rest: the next record would most likely hit the same `401`/`429`/`5xx`,
  and with the skip set nothing is lost by waiting for the retry.
- `writeRecords` takes the target config by value; it used to set
  `APIVersion` on the cached, shared config.
- The skip set is copied from db-producer (#751, not merged yet) rather
  than shared: the issue is scoped to `src/cmd/salesforce-producer`, and a
  shared package would touch three connectors. Noted for the backlog.

## Not in scope (noted)

- Bulk API 2.0 jobs are not polled: a record the job rejects while
  processing is only visible in Salesforce (`failedResults`). Backlog
  item 191; documented on the Salesforce page.
- The skip set now exists three times (remote agent, db-producer, here).
  Backlog item 192.
- The Salesforce page listed `operation: update`, which the code sends as
  an insert and the editor does not offer; the page now lists `insert` and
  `upsert` only.

## Affected files

- `src/cmd/salesforce-producer/service.go`: classification, error text,
  skip set use in `Deliver`, `restWrite`, `bulkWrite`; node id on targets.
- `src/cmd/salesforce-producer/written.go` (new): the skip set.
- `src/cmd/salesforce-producer/rejected_test.go` (new), `written_test.go`.
- `docs/connectors/salesforce.md`, the Salesforce row and the dropped list
  in `docs/operator/troubleshooting.md`, `plans/backlog.md` item 181.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run ./...`, `go test -race -count=1
./cmd/salesforce-producer/...`; from the root `make lint-connector` and
`make lint-tenant`. New tests run against main's `service.go` first to show
they fail there, then mutations of the classification, the duplicate
exception and the skip set.
