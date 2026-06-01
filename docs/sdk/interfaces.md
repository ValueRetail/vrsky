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
    Logger *slog.Logger // labelled with the connector name
    DB     *sql.DB      // non-nil when DATABASE_URL is set
    Health *healthToggle // SetReady(bool) for the readiness probe
}
```

Connectors read their own environment in `Configure` (the SDK does not impose a
single config schema — see ADR 0001 for why).

## Error classification

`Deliver` / `Evaluate` / `Convert` results map onto the messaging layer:

| Return | Effect |
|--------|--------|
| `nil` | ack — success |
| `sdk.Retriable(err)` | NAK → redeliver with backoff → DLQ after `MaxDeliveryAttempts` |
| `sdk.Permanent(err)` | ack + log — poison message, retrying can't help |
| `sdk.RateLimited(err, d)` | NAK (retriable); the delay `d` is preserved via `RetryAfter` |
| any other error | Retriable (safe default) |

## Custom HTTP handlers

`BaseProducer.RegisterHTTPHandler(pattern, handler)` (callable in `Configure`)
serves an extra handler on the auxiliary HTTP port (`WORKER_HTTP_PORT`, or
`FILE_PRODUCER_HTTP_PORT`). file-producer uses this for its `/files`
management API.

## Filter/Converter republish

When a filter forwards (keep=true) or a converter emits, the SDK republishes
with a **fresh envelope ID** — reusing the inbound ID is dropped by JetStream's
dedup window. Connectors don't need to handle this themselves.
