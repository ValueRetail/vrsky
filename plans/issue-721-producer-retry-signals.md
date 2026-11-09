# #721: producer retry signals (backlog 180; 182 moved to #794)

Checked in the code on 2026-11-09 (`main` at `eafadd3c`).

## Goal

180: `sitoo-producer` retries and dead-letters a message whose `base_url` it
refuses, like the HTTP, Visma, Brightpearl, Front Systems, SAP and Business
Central destinations, instead of dropping it.

## What the code did

`Deliver` returned `sdk.Permanent` from `safeurl.CheckBaseURL`, so the SDK
acked the message and nothing was kept for a replay.

## Change

- `src/cmd/sitoo-producer/service.go`: `sdk.Retriable` for a refused
  `base_url`.
- `src/cmd/sitoo-producer/base_url_test.go`: `TestDeliver_RefusesBadBaseURL`
  asserts the error is not permanent. The issue asks for this change of
  behaviour; the test is not weakened.
- Docs: the delivery-failure paragraph in `docs/connectors/sitoo.md` and the
  Sitoo row in `docs/operator/troubleshooting.md`.
- `plans/backlog.md`: strike 180.

## Acceptance

- A refused Sitoo `base_url` is not `sdk.IsPermanent`; the test fails on the
  old code (all six cases).
- Mutation: putting `sdk.Permanent` back makes the test fail.

## Item 182 moved to #794

The first version of this PR (#793, commit `4bb1adf4`, kept as branch
`wip/794-delayed-nak`) also NAK'd an `sdk.RateLimited` error with its delay.
The code was right, but review measured a cross-tenant stall: a delayed
message holds its `MaxAckPending` slot (32 per durable) from its first
delivery until the DLQ. Producer durables are shared by every connection of a
type. In a harness, 32 tenant-A messages returning `RateLimited(1s)` held a
tenant-B message for 10.1 s, against 6 ms with a plain NAK. JetStream adds
`Backoff[n-1] - Backoff[0]` to a NAK delay, so a slot is held for at least
about 156 s with the default schedule, and up to about 632 s at the 2 min
cap. A smaller cap cannot get under that floor. Visma, Brightpearl, Business
Central and SAP map `503` to `RateLimited`, so one tenant's target in
maintenance would stall the durable for every tenant. Slot pressure is
settled in #794 first, and 182 follows there.

Found while doing it, also for #794: a plain NAK is redelivered at once (the
next fetch, about 150 ms), not on the 1 s, 5 s, 30 s, 2 min backoff, which
JetStream applies only when an ack wait runs out. So a retried message reaches
the DLQ in under a second today.

## Verification

- In `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -race -count=1 ./cmd/sitoo-producer/... ./pkg/sdk/... ./pkg/messaging/...`
- From the root: `make lint-connector`, `make lint-tenant`
