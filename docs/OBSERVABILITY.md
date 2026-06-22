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

## Per-tenant usage metering (#92)

Phase 4A turns the per-tenant metrics into a billable record. Three axes are
tracked per tenant per UTC day in the `usage_daily` table (migration `000016`):

| Axis                 | Source                                                              |
|----------------------|---------------------------------------------------------------------|
| `messages_published` | `increase(vrsky_messages_published_total[24h])` (Prometheus)        |
| `deploys`            | `increase(vrsky_connection_deploys_total[24h])` (Prometheus)        |
| `storage_bytes`      | `tenant_quotas.storage_bytes` snapshot                              |

`vrsky_messages_published_total` is incremented by the publisher on every
successful JetStream publish; `vrsky_connection_deploys_total` is incremented by
the management-api on every successful connection start. Prometheus scrapes both
(jobs `vrsky-workers` + `management-api`).

**Rollup.** The management-api runs an hourly `UsageRollup` (`PROMETHEUS_URL`,
default `http://prometheus:9090`) that queries the two counters by `tenant_id`
and upserts the current day's row (idempotent `ON CONFLICT`). Running hourly
keeps the live day's totals fresh and re-derives the current day after a restart;
prior days persist in `usage_daily` — so the metering survives worker/API
restarts even though Prometheus counters reset. With `PROMETHEUS_URL` unset the
rollup records storage only.

**Surfacing.** `GET /api/v1/tenants/{id}/usage[?from=&to=]` returns current-month
(default) totals + daily rows; `…/usage/export?format=csv` streams the same as
CSV (`day,messages_published,deploys,storage_bytes`) for handoff to a billing /
invoice system. Both are shown on the **Usage & quotas** settings page. A live
Stripe API integration is out of scope — CSV export is the billing handoff.
