# SDK interfaces

Every connector embeds a `Base*` struct (which satisfies the bulk of
`component.Component`) and implements `Configure` plus its role method.

```go
type Producer interface {
    component.Component
    Configure(ctx, *Resources) error
    Deliver(ctx, *Envelope) error          // nil=ack; Retriable=NAK; Permanent=drop
}

type Consumer interface {                  // NEW in the SDK (no Consumer existed before)
    component.Component
    Configure(ctx, *Resources) error
    Run(ctx, PublishFunc) error            // drives its own ingestion loop; blocks until ctx done
}

type Filter interface {
    component.Component
    Configure(ctx, *Resources) error
    Evaluate(ctx, *Envelope) (keep bool, out *Envelope, err error)
}

type Converter interface {
    component.Component
    Configure(ctx, *Resources) error
    Convert(ctx, *Envelope) (*Envelope, error)
}
```

## Configure & Resources

`Configure(ctx, *Resources)` is called once before processing starts. The
runner provides:

```go
type Resources struct {
    Logger *slog.Logger  // labelled with the connector name
    DB     *sql.DB       // non-nil when DATABASE_URL is set
    NATS   *nats.Conn    // the live connection (see below)
    Health *healthToggle // SetReady(bool) for the readiness probe
}
```

Connectors read their own environment in `Configure` (the SDK does not impose a
single config schema — see ADR 0001 for why).

**`Resources.NATS`** is exposed for fleet-style **consumers** that need the
control plane directly — subscribing to the connection command subjects
(`vrsky.commands.*.connection.{start,stop}`) or running their own durable
JetStream subscriptions (e.g. `tenant-consumer`'s cross-tenant bridge).
Producers/filters/converters get their subscription from the runner and a
simple consumer just calls the injected `publish` func, so most connectors
never touch it. Prefer `publish` over `NATS` for emitting data.

## The reference connector

`cmd/example-connector` is the canonical, <150-line Consumer — the cleanest
thing to clone when starting a new connector. It shows `Configure` (read env),
`Run` (a ticker loop calling `publish`), and `main` (`sdk.RunConsumer`).

## Error classification

`Deliver` / `Evaluate` / `Convert` results map onto the messaging layer:

| Return | Effect |
|--------|--------|
| `nil` | ack — success |
| `sdk.Retriable(err)` | NAK → redelivered after 1 s, 5 s, 30 s, 2 min → DLQ after `MaxDeliveryAttempts` (see [NATS architecture](../NATS_ARCHITECTURE.md#what-a-worker-does-with-a-message)) |
| `sdk.Permanent(err)` | ack + log — poison message, retrying can't help |
| `sdk.RateLimited(err, d)` | Like `Retriable`, but each wait is the longer of `d` (kept between 1 s and the largest step, 2 min) and the backoff step; `sdk.Permanent` wins if either wraps the other |
| `sdk.ConfigUnavailable(err)` | NAK like Retriable, plus an Error log line with the connection id |
| any other error | Retriable (safe default) |

### Reading the connection's config

Every producer on the shared durable sees every connection's messages and
acks the ones that are not its own. "Not mine" must be a definite answer: the
connection does not exist, or has no node of your type. A config or secret
read that fails (a database blip, a failover, a secret that will not decrypt)
is not that answer. Acking then loses the message.

- Pass the connection query's error through `sdk.ConfigReadError(err)`. It
  keeps `sql.ErrNoRows` as a plain "connection not found" and marks anything
  else `sdk.ErrConfigUnavailable`.
- Wrap a secret resolution error in `sdk.ConfigUnavailable(err)`, and resolve
  secrets only for your own nodes: another connector's broken secret is not
  a reason to hold the message.
- In `Deliver`, return the error when `sdk.IsConfigUnavailable(err)`; ack the
  rest as "not mine".
- If you implement `sdk.ConnectionScoped`, answer
  `err == nil || sdk.IsConfigUnavailable(err)` from `ServesConnection`, so
  `Deliver` runs and returns the error. A false answer acks.

`harness.RunConfigReadCases` checks all of this against a sqlmock database.

## Starting a connection (command-driven consumers)

A consumer that starts connections on `vrsky.commands.*.connection.start`
(and from `sdk.RestoreRunning` at boot) claims each start with a
`sdk.StartGuard`, so two starts of one connection never both run and a stop
that lands mid-start wins. The comment on `StartGuard` in
`src/pkg/sdk/startguard.go` has the full pattern.

Since #725 a start that fails for a reason that passes is retried in the
background. Route every start through `StartRetrying` and reserve with the
context it hands you:

```go
func (s *X) startConnection(ctx context.Context, id, tenant string) {
	s.starts.StartRetrying(ctx, sdk.RetryOptions{Mu: &s.mu, DB: s.db, Logger: s.logger}, tenant, id,
		func(ctx context.Context) error { return s.tryStart(ctx, id, tenant) })
}

func (s *X) tryStart(ctx context.Context, id, tenant string) error {
	claim, ok := s.starts.ReserveOrRetryContext(ctx, &s.mu, tenant, id, isActive,
		func() { s.startConnection(ctx, id, tenant) })
	if !ok {
		return nil // active or starting already
	}
	defer claim.Release()
	cfg, err := s.getConfig(ctx, id, tenant)
	if err != nil {
		return sdk.TransientIfDBDown(err) // only a database that doesn't answer is retried
	}
	...
}
```

- Return `nil` for every start that did not fail: activated, refused,
  abandoned because a stop took the claim, not your node.
- Mark what a retry may fix with `sdk.TransientStart(err)`;
  `sdk.TransientIfDBDown(err)` does it for a database that does not answer
  (`sdk.IsTransientDBError`). Anything unmarked is permanent and fails the
  start once, as before: bad config, a refused address, credentials refused,
  a missing secret, a refused `{secret:}` placeholder.
- Retries wait 5 s, doubling to 5 min, with jitter (`sdk.RetryPolicy`), and
  give up 6 h after the first failure. Before each one the row is read
  (`sdk.ConnectionRunning`): a row that is no longer `running` ends them.
- Retries are kept per tenant and connection: a start or stop naming another
  tenant for the connection neither takes the retry's place nor ends it.
- They also end on a stop for the connection's tenant (the stop handler's
  `StopForTenantLocked`), on `StopAllLocked` in `Stop`, when the start's
  context ends, on a permanent failure, and when any start activates the
  connection. A stop that lands just before a retry's reserve wins, because
  `ReserveOrRetryContext` checks the context under your lock.
- Keep nothing that outlives `tryStart` under its `ctx`: run pollers under
  a context of their own, as every consumer here does.
- While a retry waits the row keeps saying `running`; the consumer writes
  nothing until a retry activates and writes `running` again.

`sdk.RestoreRunning` retries its list query on the same schedule when the
database does not answer. A list that never worked is logged at Error, and `sdk.RestoreRunningErr` returns it
as `sdk.ErrRestoreList` rather than as zero pipelines.

## Custom HTTP handlers

`BaseProducer.RegisterHTTPHandler(pattern, handler)` (callable in `Configure`)
serves an extra handler on the auxiliary HTTP port (`WORKER_HTTP_PORT`, or
`FILE_PRODUCER_HTTP_PORT`). file-producer uses this for its `/files`
management API.

## Filter/Converter republish

When a filter forwards (keep=true) or a converter emits, the SDK republishes
with a **fresh envelope ID** — reusing the inbound ID is dropped by JetStream's
dedup window. Connectors don't need to handle this themselves.
