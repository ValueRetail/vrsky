# Monitoring and alerts in prod (Teams)

## Open questions

None. Decided 2026-10-01: alerts go to **Microsoft Teams**.

## Goal

Someone is told, in Teams, within ~10 minutes when any of these happen in
prod, without anyone opening a dashboard:

| Alert | Fires when | Severity |
|---|---|---|
| `ConnectionInError` | a deployed pipeline sits in status `error` for 10 min | critical |
| `RemoteAgentOffline` | a registered, non-revoked agent has not polled for 1 h | warning |
| `DLQGrowing` | messages dead-lettered in the last 10 min (existing rule) | warning |
| `PipelineDown` | a tenant that was publishing goes silent for 10 min (existing) | critical |
| `ConnectorUnavailable` | a `vrsky-*` Deployment has 0 available replicas for 10 min | critical |
| `MgmtAPIErrorRate`, `DiskUsageHigh`, `CertExpirySoon` | existing rules | — |
| `ClusterUnreachable` | the whole cluster is gone (like this morning) — **from Azure Monitor**, because Prometheus dies with the cluster | critical |

Plus Grafana for looking at things, reachable by port-forward only.

## What exists (checked 2026-10-01)

- Nothing monitoring-related runs in prod: `deploy-azure.sh` passes
  `SKIP_MONITORING=true`; no `vrsky-monitoring` namespace.
- `infrastructure/kubernetes/monitoring/`: kube-prometheus-stack + Grafana
  Helm values and `install-monitoring.sh`, written for k3s (`storageClassName:
  longhorn`, Prometheus requesting 1 CPU / 2 Gi / 50 Gi). Also Loki/Promtail
  and Tempo manifests (not in scope here).
- `infrastructure/prometheus-rules.yml`: 7 alert rules with `promtool` unit
  tests in `prometheus-rules-test.yml`; not deployed anywhere, not run in CI.
- **Alert delivery is already built** (#84): Alertmanager → one webhook
  `POST /api/v1/alerts/webhook` (bearer `ALERTS_WEBHOOK_TOKEN`) →
  management-api fans each alert out to the owning tenant's notification
  targets (`tenant_id` label) or to targets flagged `platform`. Target types:
  slack, email (needs SMTP — prod has none), pagerduty, webhook. Settings →
  Notifications UI exists, with a "test" action. **No Teams type**, and the
  generic webhook posts VRSky's own JSON, which a Teams Workflows webhook
  does not render. Prod management-api has no `ALERTS_WEBHOOK_TOKEN`.
- Metrics: management-api on `:9090` (`vrsky_mgmtapi_*`, `vrsky_connection_deploys_total`,
  `vrsky_tls_cert_expiry_timestamp_seconds`, NATS autoscaler gauges);
  every connector's health server on `:8080/metrics` (`vrsky_messages_published_total`,
  `vrsky_dlq_messages_total{pipeline_id}`, `vrsky_message_*`); the connector
  pods expose only their worker port (e.g. 9800) in the Pod spec, and no
  ServiceMonitor/PodMonitor exists. **There is no metric for connection status
  or agent liveness** — both live only in Postgres.
- Node budget (fpool, 2× 4 vCPU): CPU *requests* already at 56 % and 90 %
  (~1.7 vCPU unrequested in total); memory requests 16 % / 23 %. The repo
  values (1 CPU for Prometheus alone) do not fit.
- `JetStreamLagHigh` uses `jetstream_consumer_num_pending`, which comes from
  `prometheus-nats-exporter`, not from NATS itself (NATS has no Prometheus
  endpoint). Nothing deploys that exporter. Verify in rollout; if absent the
  rule is simply inert (no false alerts), and the exporter is a follow-up.

## Approach

### A. Code (one PR)

1. **Teams notification target** — `pkg/notify/teams.go`: posts an Adaptive
   Card (`{"type":"message","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","content":{...}}]}`)
   to a Teams *Workflows* incoming-webhook URL (the secret, like Slack's).
   Card: title `[FIRING:critical] Name`, summary, description, tenant, time;
   colour by severity/status like `slackColor`. Handler `buildNotifier` gains
   `case "teams"`, validation requires the URL secret, the UI type list gains
   `Teams (Workflows webhook)`. Tests mirror the Slack ones (payload shape,
   secret never echoed, tenant isolation unchanged).

2. **Platform gauges in management-api** — `pkg/managementapi/platform_gauges.go`:
   every 30 s, one query each:
   - `vrsky_connections{tenant_id,status}` from `connections` grouped by status;
   - `vrsky_remote_agent_online{tenant_id,agent_id,agent}` = 1 if
     `last_seen_at > now() - 90 s` else 0, for non-revoked agents.
   Stale series are removed when an agent is revoked / a tenant has no rows
   (reset the vec before each refresh). Tenant label on both, so the alerts
   route to the tenant's Teams target. Tests with sqlmock: values and that a
   disappeared row disappears from the vec.

3. **Alert rules** — add to `infrastructure/prometheus-rules.yml`:
   `ConnectionInError`, `RemoteAgentOffline`, `ConnectorUnavailable`
   (`kube_deployment_status_replicas_available{namespace="vrsky-platform",deployment=~"vrsky-.*"} == 0`,
   platform alert). Unit tests for each in `prometheus-rules-test.yml`, and a
   CI step `promtool test rules` (`prom/prometheus` image) so the file stays
   valid.

4. **Install script for AKS** — `infrastructure/kubernetes/monitoring/`:
   - `prometheus-values.azure.yaml` overlay: `storageClassName: managed-csi`,
     Prometheus `requests 300m/1Gi, limits 1/2Gi`, retention 15 d / 20 Gi,
     Alertmanager 50m/128Mi + 2 Gi, kube-state-metrics / node-exporter /
     operator trimmed to 50–100m; `grafana-values.azure.yaml`: 100m/256Mi,
     5 Gi, `managed-csi`, admin password from an existing Secret
     (`grafana-admin`, created by the operator, never in git).
   - `podmonitors.yaml`: PodMonitor `vrsky-management-api` (port 9090) and
     `vrsky-connectors` (`tier=connector`, `targetPort: 8080`, path `/metrics`).
   - `alertmanager-config.yaml`: one receiver → management-api webhook with
     `Authorization: Bearer` from Secret `alerts-webhook-token`; `Watchdog`
     routed to a null receiver; group by `alertname, tenant_id`, repeat 4 h.
   - `install-monitoring.sh` gains `PROFILE=azure` (adds the overlay values,
     applies PodMonitors + the PrometheusRule rendered from
     `../../prometheus-rules.yml` + the Alertmanager config) and stops
     printing the default password. `deploy-azure.sh` header lists it as
     step 6; `SKIP_MONITORING` stays (it is a separate install).
   - A Go test in `pkg/managementapi` (same family as
     `TestAgentIngressTargetsRealServices`) checks the Alertmanager config
     targets `vrsky-management-api.vrsky-platform.svc:8080` and path
     `/api/v1/alerts/webhook` — the route in `handler.go` — so a rename of
     either breaks CI, not alerting.

5. Docs: `docs/OBSERVABILITY.md` gains "Alerts in prod" (what fires, where it
   goes, how to add a Teams target, how to test); README in `monitoring/`
   updated for the Azure profile; `docs/operator/troubleshooting.md` entry
   "no alerts arriving".

### B. Rollout (prod, after merge — each step yours or mine on your word)

1. `build-push-acr.sh core` (management-api with the gauges + Teams type; UI
   with the new type) → `deploy-core-azure.sh management-api ui`.
2. Create Secrets (you, values never in chat): `alerts-webhook-token` in
   `vrsky-platform` **and** `vrsky-monitoring` (same random value), and
   `grafana-admin` in `vrsky-monitoring`. Patch management-api env
   `ALERTS_WEBHOOK_TOKEN` from the secret (strategic patch; `deploy-core` does
   not apply env — see memory).
3. `PROFILE=azure infrastructure/kubernetes/monitoring/install-monitoring.sh`
   (Helm 4.2 is installed locally). Wait for the namespace to be Ready.
4. Verify targets: port-forward Prometheus `19090`, `/targets` shows
   management-api, all connector pods, kube-state, node-exporter **up**;
   `/rules` shows the VRSky groups; `vrsky_connections` and
   `vrsky_remote_agent_online` return series.
5. In Teams: create a Workflows incoming webhook on the channel ("Post to a
   channel when a webhook request is received"). In VRSky Settings →
   Notifications: new target, type Teams, paste the URL, tick **platform**,
   press **Test** → a card appears.
6. Force a real alert end to end: apply a temporary rule
   `TestAlert: expr: vector(1), for: 1m, severity: warning`; the card must
   arrive within ~3 min; delete the rule; a "resolved" card follows.
7. Azure Monitor (you, portal): metric alert on the AKS resource —
   `node_status_condition` Ready count < 1 for 5 min (or "Pods ready %" < 50)
   → action group → email/Teams. This is the only alert that survives the
   cluster being gone, and it is what would have paged this morning.
8. Leave Grafana behind port-forward; no Ingress.

## Files

| Area | Files |
|---|---|
| Teams | `src/pkg/notify/teams.go` (+test), `notifications_handler.go`, `ui/src/pages/NotificationsPage.tsx` (+test), `agentService`-style types in `ui/src/services/notificationService.ts` |
| Gauges | `src/pkg/managementapi/platform_gauges.go` (+test), `cmd/management-api/main.go` (start it) |
| Rules | `infrastructure/prometheus-rules.yml`, `prometheus-rules-test.yml`, `.github/workflows/*.yml` (promtool step) |
| Install | `monitoring/prometheus-values.azure.yaml`, `grafana-values.azure.yaml`, `podmonitors.yaml`, `alertmanager-config.yaml`, `install-monitoring.sh`, `README.md`; `src/pkg/managementapi/*_test.go` (webhook target guard); `infrastructure/azure/deploy-azure.sh` (header) |
| Docs | `docs/OBSERVABILITY.md`, `docs/operator/troubleshooting.md` |

## Tests

- Go: Teams payload is a valid Adaptive Card with title/summary/severity
  colour; `buildNotifier("teams")` resolves the secret; gauges from sqlmock
  rows, stale series dropped; Alertmanager-config ↔ route guard.
  Mutations: drop the tenant label from the agent gauge → test fails
  (routing would silently go to platform targets); point the config at the
  wrong path → guard fails.
- Rules: `promtool test rules` for the three new alerts (fires / does not
  fire / resolves), run in CI.
- UI: `NotificationsPage.test.tsx` — Teams option present, URL required.
- Prod: steps 4–6 above are the acceptance test; the plan is done when the
  `TestAlert` card and its resolution arrive in Teams.

## Risks

- **CPU requests are tight**: ~700m of new requests against ~1.7 vCPU
  unrequested. Fits, but the next connector added may not schedule; the
  honest fix is a third node or lower connector requests — flag in the PR.
- **Alert noise**: `RemoteAgentOffline` fires for every till switched off
  overnight if tills are powered down. `for: 1h` and `repeat 4 h` limit it;
  if it is noisy, raise to 12 h or restrict to business hours — decide after a
  week of real data.
- **Teams Workflows webhook URL is a secret** (anyone with it can post);
  stored encrypted as a notification secret like Slack's.
- **Prometheus restarts lose nothing important** (15 d on a PVC); an AKS
  stop/start re-pulls the images (quay.io for kube-prometheus-stack — same
  class of risk as MinIO had; mirror to ACR as a follow-up if it bites).
- `ClusterUnreachable` depends on you configuring Azure Monitor; without it a
  dead cluster is silent, as today.

## Non-goals

Loki/Promtail and Tempo (phase 2 — logs are `kubectl logs` for now);
public Grafana; per-tenant dashboards; NATS exporter sidecar; Postgres and
MinIO exporters; SLO burn-rate alerts; email/SMTP.
