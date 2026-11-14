# Consumers retry a start that fails for a reason that passes (#725)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Since #503 only the Front Systems consumer retries a start that failed for a
reason that passes. Every other command-driven consumer logs the failure and
gives up while the row says `running`, until someone stops and starts the
connection. And `sdk.RestoreRunning` returns 0 when its list query fails,
which looks the same as "nothing running", so one failed query at boot
restores nothing on any consumer.

## Acceptance criteria

1. The SDK has the retry next to `StartGuard`: a consumer routes its start
   through `StartGuard.StartRetrying`, and a start that fails with an error
   marked `sdk.TransientStart` is retried in the background on the Front
   Systems schedule (5 s doubling to 5 min, jitter, give up after 6 h).
2. Each consumer keeps its own transient/permanent split. Only the errors it
   marks transient are retried: the management database not answering
   (`sdk.IsTransientDBError`), and a partner that does not answer where the
   start itself calls one. Bad config, a refused URL or address, auth refused,
   a missing secret or a refused `{secret:}` placeholder (#868) fail the start
   as they do today, once.
3. Retries end on a stop command for the connection's tenant, on `Stop`
   (`StopAllLocked`), when the caller's context ends, when some start
   activates the connection, when the row no longer says `running` (or is
   gone), on a permanent failure, and after the give-up time. No goroutine
   outlives them.
4. A stop that lands between a retry's wait and its start wins: the retry's
   reserve is refused under the owner's lock (`ReserveOrRetryContext`). A
   start command that arrives while a retry waits runs at once; the guard
   still lets only one start hold the claim, and its activation ends the
   pending retry.
5. `RestoreRunning` retries its list query on the same schedule and tells a
   failed list from an empty one: `RestoreRunningErr` returns the error,
   `RestoreRunning` logs it at Error and keeps its signature. A row iteration
   error (`rows.Err`) counts as a failed list.
6. Front Systems uses the shared policy and database classifier; its own
   loop stays (see Decisions) and its tests pass unchanged.
7. Tests (table-driven, `-race`): transient then success; permanent not
   retried; stop during a retry's wait and in the gap before its reserve;
   context cancel; `StopAllLocked`; a start command while a retry waits;
   `RestoreRunning` list failure retried then succeeding, and a list failure
   that persists reported as an error. NATS command flow on the embedded
   server (`sdk/harness`), the database parts through `testdb`. Mutation:
   with the retry taken out, the tests fail.
8. Docs: operator troubleshooting, the SDK interfaces page, the connector
   pages that describe a failed start.

## Touched

- `src/pkg/sdk`: `startretry.go` (new), `startguard.go`, `restore.go`, tests.
- `src/cmd/*-consumer/service.go` for every consumer on `StartGuard`: api,
  brightpearl, business-central, cloud-storage, db, file, kafka, nats,
  rabbitmq, salesforce, sap-s4hana, sftp, sitoo, tenant, visma, webhook.
- `src/cmd/front-systems-consumer/retry.go`, `service.go`.
- `docs/operator/troubleshooting.md`, `docs/sdk/interfaces.md`.
- `plans/backlog.md` (strike 135).

## Decisions

- API is additive only. New: `RetryPolicy`, `RetryOptions`,
  `StartGuard.StartRetrying`, `StartGuard.ReserveOrRetryContext`,
  `StartGuard.BeforeRetryAttempt` (test hook, like `AfterReserve`),
  `TransientStart`, `IsTransientStart`, `TransientIfDBDown`,
  `IsTransientDBError`, `ConnectionRunning`, `RestoreRunningErr`,
  `ErrRestoreList`. `RestoreRunning`, `Reserve`, `ReserveOrRetry` and the
  stop methods keep their signatures. `StopLocked`'s return value still means
  "a start held the claim"; ending a pending retry does not change it.
- Unmarked errors are permanent. A consumer that has not marked anything
  behaves as before, which keeps the change backwards compatible.
- Status while retrying: the row keeps `running` (what the management API
  wrote), as it did before when a start failed. The consumer writes nothing
  until a retry activates and writes `running` through `RunningStatus`. On a
  permanent failure or giving up nothing is written either: that is #822
  (other consumers' early returns write no `error`). Front Systems keeps
  writing `error` for both, as since #503. So nothing gets worse for #822,
  and a retry that works makes the `running` row true again.
- Front Systems keeps its own loop. It is tied to state the generic helper
  has no business holding: the webhook route answers 503 while a start is
  pending, a permanent failure or give-up writes `error` with a version
  check, and a start queued behind a permanent failure is dropped (#574).
  It now takes its delays and give-up time from `sdk.RetryPolicy` and its
  database classifier from `sdk.IsTransientDBError`, so the schedule and the
  split of database errors are the same code for every consumer.
- The remote agent is not on `StartGuard` (`lanes.go`), so it gets the
  `RestoreRunning` list retry but not the start retry.
- Before each retry the loop reads the row (`ConnectionRunning`, scoped by id
  and tenant). A stop command is core NATS and can be missed; the row is the
  record. A row read that fails because the database is down counts as one
  more transient failure; any other read error ends the retries.
- Every consumer sees every start command, so a database blip at the config
  lookup makes every consumer retry that lookup, not only the owner. Each
  stops at its first lookup that works (not ours: no retry). That costs one
  small query per consumer per retry, only while the database is failing.
- `RestoreRunning` retries only a list error that `IsTransientDBError` calls
  the database not answering (review: a permission error retried for 6 h
  would only spam). It blocks `Run` while it retries, as the single list
  query did; start commands are handled meanwhile because the subscriptions
  are already in place. In the remote agent gateway that also holds off the
  membership loop, which starts after restore; noted in backlog 229.
- Retries are keyed by tenant and connection (review). Every consumer sees
  every command, so a start or stop naming the wrong tenant for a connection
  can be in flight beside the right tenant's retry; it must neither take
  the retry's place nor end it. `StopLocked` names no tenant and ends every
  tenant's retry for the connection; `StopForTenantLocked` ends only its
  own tenant's, also when it refuses the stop for another tenant's claim.
- A retry whose attempt is refused because another start holds the claim
  keeps looping until that claim is gone (review): the start it queued on
  the claim runs under the retry's context, and would be refused if the
  loop had ended. The loop removes itself from the pending map in the same
  critical section that decides to end.
- A host name that does not resolve counts as the database not answering
  (`net.Error`), as it did in Front Systems: for the database source a
  mistyped host is retried for up to 6 h. Kept, and documented.
