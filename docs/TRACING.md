# Distributed tracing (Phase 3D, #87)

Every message that flows through VRSky carries a W3C trace context, so a single
request can be followed end to end — webhook receive → NATS publish → filter →
converter → producer deliver → external API call — as one trace in Grafana
Tempo, with per-stage (per-pipeline) latency. This is the third observability
pillar alongside metrics (#84) and health (#85): metrics say *that* a pipeline
is slow, traces say *where*.

## Architecture

```
 services (AlwaysSample)         OTLP            tail-sampling           Tempo        Grafana
 ┌───────────────────┐                     ┌────────────────────┐     ┌───────┐     ┌────────┐
 │ webhook-consumer   │── traceparent ───▶ │  otel-collector    │────▶│ Tempo │────▶│Grafana │
 │ data-filter        │   in NATS header   │  • groupbytrace    │ OTLP│       │     │ (Tempo │
 │ data-converter     │                    │  • tail_sampling:  │     └───────┘     │  +     │
 │ http-producer      │── OTLP/HTTP 4318 ─▶ │    100% errors     │                  │ Prom)  │
 │ management-api      │                    │    +  1% success   │                  └────────┘
 └───────────────────┘                    └────────────────────┘
```

**Why services sample everything and the Collector decides:** tail-sampling
("keep the trace *if* it errored") can only be done once all of a trace's spans
are in one place. So each service head-samples at 100% (`AlwaysSample`) and
ships every span to the Collector, which groups spans by trace and applies the
keep/drop policy. Head-sampling in the services would throw away spans the
Collector needs to make that decision.

## Sampling policy

The Collector (`infrastructure/otel-collector-config.yaml`) keeps a trace if
**either**:
- it contains an **error** span (`status_code` policy → 100% of error traces), or
- it wins a **1%** dice roll (`probabilistic` policy → ~1% of successful traces).

Tune the percentage in that file.

## Propagation

Context is carried with the standard **W3C Trace Context** (`traceparent` /
`tracestate`):
- **HTTP edges** (webhook ingress, management-api, the http-producer's outbound
  call) use `otelhttp`, which reads/writes the headers automatically.
- **The NATS hop** uses a small `nats.Header` carrier (`pkg/tracing`): the
  producer injects `traceparent` into the message header on publish, and the SDK
  consumer extracts it before starting the next stage's span. So the trace
  survives every queue boundary.

## How it's wired (mostly central)

- `pkg/tracing` — `Init(ctx, serviceName)` sets up the OTLP exporter + global
  propagator and returns a flush-on-shutdown func; `InjectNATS`/`ExtractNATS`
  bridge the propagator over `nats.Header`.
- `pkg/messaging` `Publisher.Publish` — producer span + header injection on every
  publish (so all SDK workers inherit it).
- `pkg/sdk` runner — calls `Init`, and `subscribeDispatch` extracts the context
  and opens a per-stage consumer span around every filter/converter/producer.
  Aux HTTP routes (webhook ingress, file upload) are wrapped with `otelhttp`.
- `cmd/http-producer` — `otelhttp` transport on the outbound client.
- `cmd/management-api` — `otelhttp` handler wrapping the router.

## Configuration

| Env var | Purpose | Default |
|---------|---------|---------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Collector OTLP/HTTP base URL. **Tracing is off unless this (or `OTEL_TRACES_ENABLED=true`) is set.** | unset (off) |
| `OTEL_SERVICE_NAME` | `service.name` on spans | the worker name |
| `OTEL_TRACES_ENABLED` | Force on (`true`) / off (`false`) regardless of endpoint | unset |

When tracing is off, `Init` installs no exporter (the global provider stays
OTel's no-op) — unit tests, the load harness, and bare `go run` pay nothing.

## Viewing traces locally

```sh
docker compose up -d nats postgres-management management-api \
  webhook-consumer data-filter data-converter http-producer httpbin \
  otel-collector tempo grafana prometheus

# deploy a webhook→…→http pipeline and drive traffic (see tests/load/), then:
open http://localhost:3001        # Grafana → Explore → Tempo → Search
```

Search Tempo for `service.name = webhook-consumer` (or by trace ID) and open a
trace: you'll see the webhook span as root, the NATS publish/consume spans per
stage, the producer deliver, and the external httpbin call — each with its own
duration. Error traces are always retained; successful ones are ~1% sampled.

## Kubernetes

The same Collector config + Tempo run in-cluster under
`infrastructure/kubernetes/monitoring/`; the Tempo datasource is provisioned via
`grafana-values.yaml`. Point workloads at the collector with
`OTEL_EXPORTER_OTLP_ENDPOINT`.
