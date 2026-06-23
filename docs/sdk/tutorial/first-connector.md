# Build your first connector

**Goal:** build a working consumer on the VRSky SDK in under 30 minutes. The SDK
owns NATS/JetStream, the durable subscription, health/readiness, signals, and
graceful drain — you implement only the connector logic.

Prerequisites: Go 1.22, the repo cloned, and a quick skim of the
[SDK overview](../README.md) and [interfaces](../interfaces.md). The smallest
real reference is [`cmd/example-connector`](../README.md) (~130 LoC).

## 1. The contract

A connector implements one of four interfaces — `Consumer`, `Producer`,
`Filter`, `Converter`. A **consumer** pulls data from the outside world and
emits envelopes into the pipeline. The minimal shape:

```go
package main

import (
    "context"
    "github.com/ValueRetail/vrsky/pkg/sdk"
)

type myConsumer struct {
    sdk.BaseConsumer // free no-op lifecycle hooks; override what you need
}

// Configure wires dependencies from the runner (DB, NATS, logger, health).
func (c *myConsumer) Configure(ctx context.Context, res *sdk.Resources) error {
    return nil
}

// Run produces data until ctx is cancelled. Call publish() for each message.
func (c *myConsumer) Run(ctx context.Context, publish sdk.PublishFunc) error {
    // ... fetch/receive data, then for each item:
    //     publish(ctx, envelope)   // the one data-emit path
    <-ctx.Done()
    return nil
}

func main() {
    if err := sdk.RunConsumer(context.Background(), "my-consumer", &myConsumer{}); err != nil {
        // RunConsumer handles signals + graceful drain; this only returns on exit.
        panic(err)
    }
}
```

`sdk.RunConsumer` is the entry point: it connects to NATS, ensures the streams,
serves `/healthz` + `/readyz` + `/metrics`, calls your `Configure` then `Run`,
and drains on shutdown. A **producer** instead implements `Deliver(ctx, env)`
(one envelope at a time) and runs via `sdk.RunProducer`.

> Check the exact method signatures + error types (`Retriable` vs `Permanent`,
> which route to retry vs the DLQ) in [interfaces](../interfaces.md).

## 2. Test it without Docker

The SDK harness spins up an embedded JetStream so you can round-trip your
connector in a unit test — no broker, no compose:

```go
func TestMyConsumer(t *testing.T) {
    h := harness.NewConsumerHarness(t, &myConsumer{}, harness.Options{Name: "my-consumer"})
    // drive your source, then:
    got := h.ExpectEnvelope(t, harness.MatchTenant("tenant-x"), 5*time.Second)
    // assert on got.Payload
}
```

## 3. Make it a worker

Add a `Dockerfile` (copy an existing connector's), a `docker-compose.yml`
service, and — if it exposes config — wire its `config.type` into the UI
PropertyEditor. The connector linter (`make lint-connector`) checks you go
through `sdk.Run*` and don't bypass the SDK.

## 4. Ship it

`go build ./... && go test ./... && make lint` then open a PR. Your connector
gets health probes, metrics, tracing, structured logs, DLQ handling, and
graceful drain for free — all from the SDK.

## Next

- [Interfaces & error types](../interfaces.md)
- [Migration case study](../migration.md) (refactoring file-producer onto the SDK)
- The full connector list: [Connectors](../../connectors/index.md)
