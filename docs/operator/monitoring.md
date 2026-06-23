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
and feed the per-tenant notification targets (#84).

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
