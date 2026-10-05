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

## Alerts in prod (plans/monitoring-prod.md)

Prod runs kube-prometheus-stack + Grafana in `vrsky-monitoring`, installed with
`PROFILE=azure infrastructure/kubernetes/monitoring/install-monitoring.sh`
(README there has the Secrets to create first). Grafana and Prometheus are
reachable by port-forward only.

**What fires** — `infrastructure/prometheus-rules.yml` (promtool-tested in CI):

| Alert | Fires when | Severity | Routed to |
|---|---|---|---|
| `ConnectionInError` | a pipeline has been in status `error` for 10 min | critical | the workspace |
| `RemoteAgentOffline` | a registered agent has not polled for 1 h (its data waits 72 h) | warning | the workspace |
| `DLQGrowing` | messages dead-lettered in the last 10 min | warning | platform |
| `PipelineDown` | a workspace that was publishing goes silent for 10 min | critical | the workspace |
| `ConnectorUnavailable` | a `vrsky-*` Deployment has 0 available replicas for 10 min | critical | platform |
| `MgmtAPIErrorRate`, `DiskUsageHigh`, `CertExpirySoon`, `NATSInstanceApproachingCapacity`, `JetStreamLagHigh` | see the rules file | — | platform / workspace |

`ConnectionInError` and `RemoteAgentOffline` read the management-api gauges
`vrsky_connections{tenant_id,status}` and
`vrsky_remote_agent_online{tenant_id,agent_id,agent}` (`platform_gauges.go`,
refreshed from the DB every 30 s).

**Where they go** — Alertmanager has one receiver, the management-api
(`POST /api/v1/alerts/webhook`, bearer `ALERTS_WEBHOOK_TOKEN`). It delivers
each alert to the notification targets of the workspace in the `tenant_id`
label, or to targets flagged **platform** when there is none. Targets are
managed in Settings → Notifications: **Microsoft Teams** (Workflows incoming
webhook, Adaptive Card), Slack, email (needs `SMTP_*`), PagerDuty, webhook.

**Adding Teams**: in the Teams channel → Workflows → "Post to a channel when a
webhook request is received" → copy the URL. In VRSky: Settings →
Notifications → type *Microsoft Teams*, paste the URL, tick *platform* if this
channel should also get infrastructure alerts, **Test**. The URL is a secret
(anyone with it can post) and is stored encrypted.

**Proving it end to end**: apply a temporary rule
(`alert: TestAlert`, `expr: vector(1)`, `for: 1m`, `severity: warning`) as a
`PrometheusRule` in `vrsky-monitoring`; a card arrives within ~3 minutes;
delete the rule. Nothing is posted when an alert clears: the channel only
gets what is wrong (`send_resolved: false` on the Alertmanager receiver).

**Who answers them**: the [alert responder](operator/alert-responder.md)
receives the same alerts, diagnoses the affected workspace with Claude and
posts a `ResponderReport` to the same targets — in `act` mode after taking
one of a few safe actions (redeploy, DLQ retry, resend).

**What this cannot see**: a cluster with no nodes takes Prometheus with it.
Pair it with an Azure Monitor metric alert on the AKS node count.

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
