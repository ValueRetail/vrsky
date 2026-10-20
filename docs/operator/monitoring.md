# Monitoring

VRSky emits metrics (Prometheus), traces (OpenTelemetry → Tempo), and structured
logs (JSON → Loki). All three are provisioned in Grafana
(`http://localhost:3001` locally).

## Metrics (Prometheus)

Scraped from every worker's `/metrics` (port 8080) and the management API
(`:3000/metrics`). Key series:

- `vrsky_messages_published_total{tenant_id}` — throughput per tenant
- `vrsky_connection_deploys_total{tenant_id}` — deploys per tenant
- `vrsky_dlq_messages_total` — dead-lettered messages
- `vrsky_message_processing_seconds` — processing latency histogram
- `vrsky_mgmtapi_http_request_duration_seconds`, `..._requests_total` — API SLIs
- `vrsky_tls_cert_expiry_timestamp_seconds{path}` — cert-expiry gauge
- `webhook_signature_failures_total`, `webhook_client_cert_failures_total` — auth rejections

Alert rules + Alertmanager routing live in `infrastructure/prometheus-rules.yml`
and feed the per-tenant notification targets (#84). The deploy job's smoke
check fails on the same rules (the `vrsky-*` groups, above `info`) and on
anything `critical`; stock warnings such as `KubeClientErrors` reach Teams
as before but do not turn a rollout red (`infrastructure/azure/smoke-alerts.sh`).

## Traces (Tempo)

Each message is a trace spanning consumer → producer (and the external call).
Explore → Tempo → Search, or click a trace ID in a log line. See
[Tracing](../TRACING.md) for sampling configuration.

## Logs (Loki)

Structured JSON logs with `service`, `tenant_id`, `connection_id`, `level`
promoted to labels. Example query: `{service="http-producer"} | level="error"`.
See [Observability](../OBSERVABILITY.md).

## Health & readiness

Every service serves `/healthz` (liveness) and `/readyz` (readiness — reports
NATS + DB and flips to not-ready during graceful drain). Wire these into your
orchestrator's probes.

## Usage & billing

Per-tenant message/deploy/storage usage is rolled up daily and shown under
**Settings → Usage & quotas**, exportable as CSV for invoicing (#92).

## CPU requests versus use

A request is what the scheduler *reserves* for a pod, not what the pod may
use; limits cap use. On 2026-10-14 the production nodes were 81% reserved
and 2% busy, because every request had been set by guess. They were then
sized at twice each workload's 7-day peak (`plans/cpu-request-headroom.md`),
limits untouched, and two guards keep it that way:

- `NodeCPURequestsHigh` fires when a node's reservations exceed 85% of its
  allocatable CPU for an hour. The fix is to right-size the biggest
  requests on that node (`kubectl describe node` lists them); a third node
  is the answer only when *use* is high too (`kubectl top nodes`).
- `TestCPURequestsStayWithinBudget` fails the build when the requests in
  the repo's manifests add up to more than 2000m, naming the largest.

The AKS add-ons (Container Insights, metrics-server, Azure Policy, CoreDNS)
reserve about 1.4 cores between them and are Azure's to size.

## Grafana restarts on every upgrade

Grafana's data disk attaches to one node at a time, so its Deployment uses
the `Recreate` strategy: the old pod stops before the new one starts, and
Grafana is away for a few seconds during `install-monitoring.sh`. (With a
rolling update the new pod could land on the other node and hang on
"Multi-Attach error" until someone scaled the Deployment to zero and back.)

