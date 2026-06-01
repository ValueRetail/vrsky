# VRSky Connector SDK (`pkg/sdk`)

The Connector SDK is the contract and runtime every VRSky connector builds on.
It removes the ~250 lines of boilerplate each worker used to hand-roll (NATS +
JetStream wiring, the durable subscription, health/metrics server, DB
connection, signal handling, graceful shutdown) and provides a Docker-less
testing harness.

A connector embeds one of the `Base` structs and implements only its role
method(s) + `Configure`:

```go
type myProducer struct{ sdk.BaseProducer }

func (p *myProducer) Configure(ctx context.Context, res *sdk.Resources) error {
    // res.DB is ready if DATABASE_URL is set; res.Logger is labelled; read env here
    return nil
}
func (p *myProducer) Deliver(ctx context.Context, env *sdk.Envelope) error {
    // deliver to the outside world; return nil / sdk.Retriable(err) / sdk.Permanent(err)
    return nil
}

func main() { sdk.RunProducer(context.Background(), "my-producer", &myProducer{}) }
```

`RunProducer` / `RunConsumer` / `RunFilter` / `RunConverter` own the boilerplate.

## Status

PR 1/3 of issue #83:
- ✅ `pkg/sdk` — interfaces (`Consumer` is new; `Producer`/`Filter`/`Converter`), `Base*` structs, the `Run*` runner, typed errors.
- ✅ `pkg/sdk/harness` — embedded JetStream + producer/consumer/filter/converter harnesses (no Docker).
- ✅ `cmd/file-producer` refactored onto the SDK as the proof (≈870 → ≈640 lines).
- ⏳ Remaining producers (PR #2), then consumers + a reference connector (PR #3).
- ⏳ `cmd/lint-connector` analyzer (catch "didn't call sdk.Run", "imports nats.go directly") — deferred.

## Docs

- [interfaces.md](interfaces.md) — the four connector contracts + error classes
- [migration.md](migration.md) — porting an existing worker (file-producer case study)
- [adr/0001-sdk-package-structure.md](adr/0001-sdk-package-structure.md) — why `pkg/sdk` wraps the internal packages, and the fleet-vs-orchestrator config model
