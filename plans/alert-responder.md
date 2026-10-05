# Alert responder: an AI that diagnoses and auto-resolves alerts, reporting in Teams

## Open questions

1. **Model.** The option I offered said "Opus 4.8"; the current Opus is
   **Claude Opus 5.5** (`claude-opus-5-5`, $4/$20 per MTok — cheaper than
   4.8 at $5/$25, same context). The plan uses Opus 5.5. Say so if you
   specifically want 4.8.
2. **Observe first?** The responder has an `observe` mode (diagnose and post,
   take no action) and an `act` mode. Recommended: run `observe` for the
   first week, read what it would have done, then switch to `act`. Decide at
   rollout.
3. **Platform alerts** (`ConnectorUnavailable`, `DiskUsageHigh`, …) have no
   workspace, and the responder's safe actions are all workspace-level
   (redeploy, DLQ retry, resend). For those it can only read and recommend
   (no k8s access by design, per your "safe actions" choice). Fine for v1?

## Decisions (2026-10-01)

Reacts **inside VRSky** and posts to Teams (no Teams bot, no M365 admin);
**diagnose + safe actions** (redeploy a pipeline, retry DLQ messages, resend
everything — each at most once per alert per hour; never delete, never touch
credentials or k8s); **Anthropic API** with your key as a k8s Secret.

## Goal

Within ~2 minutes of an alert firing, a card from "VRSky Responder" appears
in the Teams channel: what the alert means, what it found (pipeline log,
DLQ reasons, agent status), what it did (or would do in observe mode), and
what is left for a human. A second card when the alert resolves, if it acted.

## How it fits what exists

- Alerts already arrive at `POST /api/v1/alerts/webhook` (Alertmanager) and
  are fanned out to notification targets per workspace. The responder gets
  the same alerts: the webhook additionally publishes each alert to NATS
  (`vrsky.alerts.<tenant_id>` / `vrsky.alerts.platform`).
- Its report goes **back through the same webhook** as a synthetic alert
  named `ResponderReport` (same `tenant_id` and severity as the original,
  `summary` = one line, `description` = the findings). The existing Teams
  card renders it; no new delivery code, no webhook URL in the responder.
  The webhook does not republish `ResponderReport` to NATS (no loop).
- Everything it reads or does goes through the management API with the
  workspace's **tenant API key** (`tenant_api_keys`, Settings → API Key;
  admin role), so the existing audit log records every action as that key,
  and tenant isolation is the API's, not the responder's.
- Claude via the official Go SDK (`github.com/anthropics/anthropic-sdk-go`),
  **beta tool runner** (`client.Beta.Messages.NewToolRunner` +
  `toolrunner.NewBetaToolFromJSONSchema`, `RunToCompletion`), adaptive
  thinking (default on Opus 5.5), `effort: medium`, streaming off (short
  runs), server-side refusal fallbacks on by default. One system prompt
  (cached) with the playbooks; tools below.

## Design

### Service `cmd/alert-responder` (standing, 1 replica, core group)

```
NATS vrsky.alerts.>  →  dedupe/cooldown  →  Claude tool-runner run  →  ResponderReport → webhook → Teams
                                              │ read tools: management API (tenant key)
                                              │ act tools:  allowlist, 1/alert/hour, mode=act only
                                              └ run log (stdout JSON) + metrics
```

**Env:** `NATS_URL`, `MGMT_API_URL` (in-cluster), `ALERTS_WEBHOOK_TOKEN`
(to post reports), `ANTHROPIC_API_KEY`, `RESPONDER_MODE=observe|act`,
`RESPONDER_TENANT_KEYS` (Secret; `tenant_id=vrsky_<slug>_<hex>` per line —
v1 is one workspace), `RESPONDER_MAX_RUNS_PER_HOUR=20`,
`RESPONDER_MAX_TOOL_CALLS=25`.

**Per alert:**
1. Ignore `ResponderReport`, `Watchdog`, `TestAlert`, `severity=info`, and
   alerts for a tenant with no key (log once).
2. Fingerprint = alertname + sorted labels. `firing`: skip if a run for this
   fingerprint happened < 1 h ago (Alertmanager repeats every 4 h; the
   responder must not re-run on every repeat). `resolved`: post a one-line
   resolution card only if the responder acted on that fingerprint.
3. Run the model with the alert, the playbook for its name, and the tools.
   Hard limits enforced in code, not by prompt: tool-call cap, action cap,
   30 s per tool call, 3 min per run, runs/hour cap.
4. Post the report. Record the run (in-memory ring + log line
   `responder run` with fingerprint, actions, tokens, cost estimate).

### Tools (all through the management API with the tenant key)

| Tool | Reads/does | Route |
|---|---|---|
| `get_pipeline` | name, status, `last_error`, nodes (types, config **with secrets redacted** — the API already omits them) | `GET /connections/{id}` |
| `list_pipeline_events` | last 50 events (started/stopped/error with messages) | `GET /connections/{id}/events`? — **does not exist**; v1 reads `connection_events` through a new viewer route `GET /api/v1/connections/{id}/events?limit=` (small, audited) |
| `get_pipeline_metrics` | counters | `GET /connections/{id}/metrics` |
| `list_dlq` / `get_dlq_message` | reasons, counts, a sample (payload truncated to 2 KB) | `GET /connections/{id}/dlq[/{seq}]` |
| `list_agents` | online/offline, last seen, groups | `GET /agents` |
| `redeploy_pipeline` **(act)** | stop + start | `POST …/stop`, `POST …/start` |
| `retry_dlq_message` **(act)** | one message | `POST …/dlq/{seq}/retry` |
| `resend_everything` **(act)** | BC re-poll | `POST …/resend` |

Act tools are **absent from the tool list in observe mode** (the model
cannot call what it cannot see); in act mode each is wrapped by the
allowlist + once-per-alert-per-hour guard, and returns the guard's refusal
as a tool error the model has to report, not work around.

### Playbooks (system prompt, one per alert; summarised)

- `ConnectionInError`: read status + last_error + events; classify
  *config/credentials* (→ recommend; never redeploy) vs *transient*
  (upstream 5xx/timeouts → redeploy once) vs *unknown* (recommend).
- `DLQGrowing`: read DLQ reasons; if one transient cause and ≤ 20 messages
  → retry them; if a schema/mapping error → recommend, do not retry.
- `PipelineDown`: distinguish "source legitimately quiet" (BC poll with 0
  changes) from "poller died" (no `fetch complete` events) → redeploy once.
- `RemoteAgentOffline`: nothing to act on; report last seen, which pipelines
  wait on it, how long data is held (72 h).
- Platform alerts: read-only summary + recommendation.
- Always: say what you checked, what you did, what a human should do; never
  claim an action you did not get a 2xx for.

### Report card

`[FIRING:critical] ResponderReport — ConnectionInError on "Catalogue → tills": redeployed, recovering`
Body: cause (2–3 lines), evidence (events/DLQ excerpts), action taken (or
"would have: … (observe mode)"), next step for a human, run cost.

## Files

| Area | Files |
|---|---|
| Service | `src/cmd/alert-responder/{main.go,service.go,tools.go,guard.go,playbooks.go,report.go,Dockerfile}` + tests |
| Management API | `notifications_handler.go` (publish alerts to NATS; ignore `ResponderReport` for republish), `nats_publisher.go` (`PublishAlert`), new `GET /api/v1/connections/{id}/events` + `openapi_registry.go` |
| Deploy | `infrastructure/kubernetes/alert-responder/{deployment,secret.example}.yaml`, `build-push-acr.sh` (core), `deploy-core-azure.sh` (service table), `docker-compose.yml` |
| Docs | `docs/operator/alert-responder.md` (what it may do, modes, cost, how to read a card), `docs/OBSERVABILITY.md` (link) |
| Deps | `go.mod`: `github.com/anthropics/anthropic-sdk-go` |

## Tests

- `guard_test.go`: once-per-alert-per-hour, runs/hour cap, tool-call cap;
  act tools absent in observe mode; an act tool call outside the allowlist
  is refused. *Mutations: drop the cooldown → fails; expose act tools in
  observe → fails.*
- `tools_test.go` against an `httptest` management API: each tool sends the
  tenant key and the workspace header, redacts payloads > 2 KB, surfaces
  non-2xx as tool errors.
- `service_test.go` with a **fake model** (interface over the tool runner):
  a ConnectionInError run posts exactly one `ResponderReport` with the
  original tenant/severity; a resolved alert with no prior action posts
  nothing; `ResponderReport`/`Watchdog` ignored. *Mutation: republish
  `ResponderReport` to NATS → loop test fails.*
- Management API: `TestAlertsWebhook_PublishesToNATS` (and not for
  `ResponderReport`); events route is tenant-scoped (isolation test).
- One real-model integration test, skipped without `ANTHROPIC_API_KEY`:
  the model calls `get_pipeline` and produces a report (checks the SDK
  wiring, not the judgment).
- `promtool`/lint suite as usual; `lint-tenant` for the new route.

## Rollout

1. You: create Secrets `anthropic-api-key` and `alert-responder-tenant-keys`
   (the workspace's API key from Settings → API Key) in `vrsky-platform` —
   values never in chat or git.
2. `build-push-acr.sh core` → deploy management-api (NATS publish + events
   route) → apply the responder Deployment (`RESPONDER_MODE=observe`).
3. Prove it: apply the `TestAlert` rule? No — it is ignored by design. Use a
   real one: stop the BC pipeline's token (or point it at a wrong company id)
   → `ConnectionInError` → a Responder card with the diagnosis within
   ~2 min. Fix the config → resolved.
4. After a week of observe cards: switch to `act`, repeat the test, see
   "redeployed" in the card and the start in the audit log.

## Risks

- **Wrong action.** Bounded by the allowlist (nothing destructive), once
  per alert per hour, observe-first, and the audit log. Worst case: one
  unnecessary redeploy or DLQ retry per hour per alert.
- **Cost.** ~20k input / 2k output tokens per run ≈ $0.12 at Opus 5.5;
  capped at 20 runs/hour (~$60/day worst case, pennies in practice).
- **Prompt injection via data**: pipeline configs, DLQ payloads and BC
  error strings enter the prompt. The model cannot do anything outside the
  allowlist no matter what the data says; payload excerpts are truncated and
  labelled as data in the tool results.
- **Tenant key scope** is admin for that workspace; the responder only
  exposes the routes above. A leaked key is rotated in Settings.
- **Model/API outage**: the run fails, nothing is posted, the normal alert
  card still arrives; logged.

## Non-goals (v2)

Reading the Teams channel (bot registration), k8s actions, per-tenant
configuration in the UI, a run history page, multi-workspace key management
beyond the Secret.
