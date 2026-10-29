# A stop names its tenant (#498)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Every command-driven consumer's stop handler found the active entry by
connection ID alone and cancelled it. Only the `stopped` UPDATE filtered on
the command's `tenant_id`. A stop naming the wrong tenant (it would have to be
published on platform NATS, which is ClusterIP-only; management-api checks
ownership before it publishes) stopped another tenant's poller and left its
row saying `running`. Defence in depth: the handler acts only when the stop
names the tenant the connection was started for, and the subject's tenant
token agrees with the payload's.

## Acceptance criteria

1. For each of the 16 consumers on `sdk.StartGuard` (api, brightpearl,
   business-central, cloud-storage, db, file, front-systems, kafka, rabbitmq,
   salesforce, sap-s4hana, sftp, sitoo, tenant, visma, webhook): a stop whose
   payload names another tenant leaves the active connection running (its
   context not cancelled, its entry still in the map) and writes no status.
2. Same for a stop whose subject tenant disagrees with its payload tenant,
   even when the payload names the right tenant.
3. A stop naming the right tenant on a matching subject still stops it, as
   before (cancelled, removed, `stopped` written where the consumer writes
   status).
4. A start in progress is not abandoned by a stop naming another tenant, and
   still is by one naming its own.
5. Remote agent: a stop naming another tenant leaves the session in place and
   writes no status (already true since #429; now also logged), and a stop
   whose subject tenant disagrees with its payload is refused before it takes
   a turn.
6. A mismatch logs one warning with the connection ID and both tenant IDs,
   nothing from the payload besides.
7. `sdk` keeps its exported API: additions only.
8. Backlog item 95 struck with #498.

## Design

`src/pkg/sdk/startguard.go`:

- `Activate` records the claim's tenant per connection in `owners`, under the
  owner's lock. Every consumer inserts into its active map only inside
  `Activate`, so the latest record is the tenant of the active entry.
- `StopForTenantLocked(subject, tenantID, connectionID) (bool, error)`
  replaces `StopLocked` in stop handlers. It refuses (a
  `*StopTenantMismatch`) when the subject's tenant is not the payload's, when
  a start in progress is for another tenant, or when the recorded owner is
  another tenant. Otherwise it runs `StopLocked` and returns its result.
- `CommandSubjectTenant(subject)` parses `vrsky.commands.{tenant}.connection.{verb}`.

Each consumer's `handleStopCommand` calls it first in its critical section,
and on an error unlocks, logs a warning and returns before touching its map.
Sitoo moves from `Reserve` to `ReserveOrRetry(..., tenantID, ..., nil)` so its
claim carries the tenant (no retry, as before).

The remote agent does not use the guard; its `stopConnection` already
compared the session's tenant. It gets the subject check in `onCommand` (stops
only) and a warning when a stop finds another tenant's session.

Touches: `src/pkg/sdk`, the 16 consumers' `service.go`,
`src/cmd/remote-agent/service.go`, tests next to each.

## Decisions

- One SDK helper rather than a tenant field on every consumer's entry type:
  four consumers store a bare `context.CancelFunc`, and the guard already sees
  every `Activate`. A consumer that forgets the helper is the risk; the tests
  per consumer cover that.
- A record with an empty tenant (a start through plain `Reserve`) is not
  checked, so an outside user of `Reserve` keeps today's behaviour. Every
  consumer in this repo now passes a tenant.
- `owners` is never pruned, like `seq`. A stale record matters only for a
  stop naming another tenant, which is then refused; there is nothing active
  to stop anyway.
- A stop refused because another tenant's start is in flight still drops its
  own tenant's start queued behind that claim by `ReserveOrRetry`: the stop
  is newer, which is what `StopLocked` does too.
- A malformed subject is a mismatch. Every consumer sees every stop, so one
  forged stop logs a warning in each. That's the signal we want.
- Start commands are not subject-checked: a start naming the wrong tenant
  already fails the tenant-scoped lookup and starts nothing.

## Risks

- A publisher that sends stops on a subject whose tenant differs from the
  payload would now be ignored. management-api's `publishCommand` builds the
  subject from the same tenant it puts in the payload (all three callers).

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race ./...` (with Postgres via `src/pkg/testdb` where available),
`make lint-tenant`. Mutation run per consumer: revert the handler to
`StopLocked` and the new tests must fail.
