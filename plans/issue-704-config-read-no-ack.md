# Don't ack a message when its connection config can't be read (#704)

No open questions.

## Goal

Every producer on the shared data durable asks "is this connection mine?"
before delivering (`ServesConnection`, then `Deliver`). Today both treat any
error from the config lookup as "not mine" and ack. A Postgres blip, a
failover or a failed secret read therefore acks and loses every message in
flight, with no log line, no metric and no DLQ entry. data-filter and
data-converter do the same in their own handlers.

## Acceptance criteria

- A failed config or secret read never acks. `ServesConnection` answers true,
  `Deliver` (and `DeliverStream`) return a retriable error, so the message is
  redelivered and goes to the DLQ when the failure outlasts the retries.
- A definite "not mine" still acks: the connection row does not exist, or it
  has no node of this producer's type.
- One Error log line per failed attempt, with the connection id, tenant id
  and envelope id. No secret values (the errors carry secret ids at most).
- The SDK change is backwards compatible: no interface changes.
- Tests per producer that fail on main; an SDK test on the embedded NATS
  server that shows retries and the DLQ; a mutation run on the
  not-found/error split.

## Design

One mechanism in the SDK, `pkg/sdk/errors.go`:

- `sdk.ErrConfigUnavailable`, a sentinel, and `sdk.ConfigUnavailable(err)`,
  which wraps a cause so `errors.Is` matches both. Not Permanent, so the
  runner NAKs it like any other error.
- `sdk.IsConfigUnavailable(err)`.
- `sdk.ConfigReadError(err)` for the connection query: `sql.ErrNoRows` stays
  a plain "connection not found" (not mine), anything else becomes
  `ConfigUnavailable`. This is the not-found vs error split in one place.

The runner (`subscribeDispatch` in `lifecycle.go`) logs a `ConfigUnavailable`
error from `Deliver` at Error with the connection id, then NAKs it as before.
Permanent handling is unchanged and still checked first.

Why the sentinel and not a tri-state `ServesConnection` (a new optional
interface returning `(bool, error)`): the issue allows either, and the
sentinel needs no new interface. `ServesConnection` keeps its signature; it
answers true on `ErrConfigUnavailable` so `Deliver` runs and returns the
error, which is what `ConnectionScoped`'s doc already asked for and what
file-producer already did. The cost is that during an outage an offloaded
payload is rehydrated before `Deliver` fails again; that is rare and it NAKs
either way.

Per producer the change is small:

- the connection query error goes through `sdk.ConfigReadError`;
- a secret resolution error is wrapped in `sdk.ConfigUnavailable`;
- `ServesConnection` answers `err == nil || sdk.IsConfigUnavailable(err)`
  (or `sdk.IsConfigUnavailable(err)` where the target list decides);
- `Deliver` returns the error when `sdk.IsConfigUnavailable(err)`, otherwise
  logs at Debug and acks as before.

Secrets are now resolved only for the producer's own nodes. Before, several
producers resolved every node's secrets (http, db, sftp, kafka, rabbitmq,
cloud-storage: the whole `nodes` array; the six per-message ones: every
producer node). With a failed secret read now meaning "retry", an unreadable
secret on another connector's node would make a bystander producer NAK and
dead-letter a message that its real producer delivered, and a DLQ retry
would then deliver it twice. Each producer now picks its nodes by `type`
from the stored config (the type and the sub-object key are never secrets)
and resolves secrets for those only.

data-filter and data-converter do not run on the SDK runner, so they use
`sdk.ConfigReadError` and log and return the error themselves.

## Affected files

- `src/pkg/sdk/errors.go`, `lifecycle.go`, `producer.go` (doc only).
- `src/pkg/sdk/harness/configread.go`: `RunConfigReadCases`, the shared
  table test.
- Producers: `src/cmd/{visma,sitoo,sap-s4hana,front-systems,brightpearl,
  business-central,http,db,sftp,kafka,rabbitmq,cloud-storage,salesforce,
  file}-producer`.
- `src/cmd/data-filter/main.go`, `src/cmd/data-converter/main.go`.

Not touched: `src/pkg/safeurl`, the SAP and BC consumers,
`src/pkg/managementapi`, `docs/operator/troubleshooting.md` (PR #703 is
rewriting it; the text is in the PR body).

## What stays as it is

- A `nodes` column that doesn't parse still acks (not a read failure, and
  retrying won't fix it).
- salesforce-producer has no `ServesConnection` and resolves no secrets; only
  its connection query changes.
- file-producer already retried; its error is now marked
  `ConfigUnavailable` so it gets the same Error log.
- postgres-producer runs on `pkg/io`, not the SDK; out of scope.

## Risks

- During an outage longer than the retry schedule (about 156 s) every
  producer dead-letters every in-flight message. The DLQ publish uses the
  stream sequence as its message id, so copies from different producers of
  the same message dedupe to one DLQ entry inside the duplicate window, and a
  DLQ retry republishes to all producers, where the bystanders ack it as not
  theirs. This is the intended trade: visible in the DLQ instead of lost.
- "Couldn't tell" can't know whose message it was. If one worker's database
  reads fail while the real producer's succeed (a bad pod, not an outage),
  that worker dead-letters a message the real producer delivered, and a DLQ
  retry delivers it a second time. Operators retrying a DLQ entry should
  check `X-Vrsky-Worker` against the connection's destination type.
- A secret that is gone for good on a producer's own node now dead-letters
  its messages instead of dropping them silently. Same trade.

## Verification

- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run ./...`,
  `make lint-tenant`.
- `go test -race -count=1 ./pkg/sdk/... ./pkg/messaging/...` and every
  touched producer, filter and converter package.
- Fail-on-main: the new tests copied onto origin/main with only the SDK error
  helpers and test hooks added fail in every producer, filter and converter
  package, and the SDK DLQ test fails on the missing Error line.
- Mutation run over the split (see the PR).
