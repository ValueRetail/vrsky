# Observability (metrics + traces + logs)

VRSky's three observability signals share one Grafana, and correlate:

| Signal | Backend | Source | Issue |
|--------|---------|--------|-------|
| **Metrics** | Prometheus | `/metrics` on each worker + management-api | #84 |
| **Traces** | Tempo (via otel-collector tail-sampling) | OpenTelemetry spans | #87 |
| **Logs** | Loki (via Promtail) | structured JSON on stdout | #91 |

A request carries one `trace_id` end to end: logs link to their trace (Loki
`trace_id` → Tempo), traces link to metrics and logs, so you can pivot between
"how slow" (metrics), "where" (traces), and "why" (logs) without leaving Grafana.

## Centralized logging (#91)

### Structured logs everywhere
Every long-running service builds its logger with **`pkg/logging`** (`logging.New(service)`),
emitting **JSON to stdout**. A context-aware handler stamps each record with the platform's
standard fields:

| Field | Always? | Source |
|-------|---------|--------|
| `service`, `level`, `msg`, `time` | yes | the logger / slog |
| `trace_id` | when in a trace | active OTel span (#87) |
| `tenant_id`, `pipeline_id`, `connection_id` | when handling a pipeline message | `logging.ContextWith` (the SDK sets these per message) |

Workers inherit this for free via the SDK runner (`pkg/sdk` `newLogger` →
`logging.New`, and `subscribeDispatch` enriches the per-message context).
management-api logs JSON the same way; its HTTP access log is fully structured.
A unit test (`pkg/logging/logging_test.go`) is the lint gate that fails if a log
line loses the mandatory fields.

### Shipping & storage
Promtail (compose) / a Promtail DaemonSet (K3s) tails container/pod stdout,
parses the JSON, and promotes `service` / `tenant_id` / `pipeline_id` /
`connection_id` / `level` to **Loki labels** (`trace_id` stays a field — high
cardinality — and powers the trace link). Loki keeps **7 days**
(`retention_period: 168h`, compactor deletes older chunks).

> Promtail is in upstream maintenance mode; **Grafana Alloy** is the
> forward-looking replacement and a drop-in for this pipeline when we migrate.

### Using it
```sh
docker compose up -d ... loki promtail grafana   # + the app stack
open http://localhost:3001          # Grafana → Explore → Loki
```
- All logs touching a pipeline, across every service: `{pipeline_id="abc-123"}`
- Errors for a tenant: `{tenant_id="t1", level="error"}`
- From a log line, click `trace_id` to jump to the full trace in Tempo.

A starter dashboard (**VRSky — Logs**) ships with panels for logs-by-pipeline,
errors-per-tenant-per-hour, and recent errors.

### Don't log secrets
Log **identifiers/references**, never secret values (tokens, passwords, keys,
decrypted payloads). `pkg/logging` only emits the explicit fields you pass — it
never reflects whole structs — but the rule is on the caller: e.g. log
`grant_id`, not the access token; log `email`, not the password. Login and
secret-access paths follow this.

## Kubernetes
- Metrics: kube-prometheus stack.
- Traces: `infrastructure/kubernetes/monitoring/otel-tracing.yaml`.
- Logs: `infrastructure/kubernetes/monitoring/loki-promtail.yaml`.
- All three datasources are provisioned in `grafana-values.yaml`.
