# VRSky — B2B Readiness Plan

Source: VRSky Demo deck (April 2026) + Manus speaker notes.
Cross-referenced against open GitHub issues on `ValueRetail/vrsky`.

Each section below is one GitHub issue. Copy the heading as the title, the body block as the issue body, and apply the listed labels.

---

## Cross-reference: existing issues to close after the new ones are created

All remaining content from these issues has been folded into the new Phase 1–4 specs below. You can close each cleanly once the corresponding new issues are open.

- **#4 Multi-Tenant Isolation & Authentication** → covered by **P1-1** (secrets), **P1-3** (OIDC), **P1-4** (RBAC), **P1-9** (quotas + isolation tests). NATS-credential auto-rotation deferred with #19/#20/#21.
- **#8 API Gateway, Monitoring & Observability** → covered by **P3-1** (alerts), **P3-7** (gateway rate limiting), **P3-8** (Loki), **P4-3** (OpenAPI auto-publish), **P3-5** (gateway latency baseline).
- **#12 Cross-Tenant Integration** → already shipped (tenant data sharing UI + `tenant_data_connections` table). Close as completed.
- **#14 Infrastructure** → K3s/Traefik/cert-manager/Terraform delivered. Close as completed.
- **#18 Connector SDK & Essential Connectors** → HTTP/File/Postgres shipped; remaining covered by **P2-9** (formal SDK), **P2-1** (OAuth), **P2-2..P2-6** (new connectors), **P4-3** ("Build your first connector" tutorial), **P3-5** (per-connector perf targets).
- **#20 NATS KV State + Retry** → covered by **P1-5** (JetStream + DLQ). Close once P1-5 lands.
- **#22 NATS Monitoring Dashboards** → covered by **P3-1** (alerts) + **P3-8** (Loki).
- **#32 OpenTelemetry** → covered by **P3-4**. Close once P3-4 lands.

Close-out comment template:

```
Closing — shipped portions: <list>.
Remaining work tracked in: <new issue refs>.
Deferred (revisit when scaling beyond shared NATS): #19, #21.
```

---

# PHASE 1 — Trust foundation (4–6 weeks)

Goal: pass an enterprise security review. Without this, no B2B deal closes.

---

## P1-1. Secrets management — encrypt connection credentials at rest

**Labels:** `P0-critical`, `security`, `b2b-blocker`, `phase-1`

**Body:**

### Problem
Connector configs (API keys, DB passwords, OAuth client secrets, signing keys) are persisted in cleartext in the management Postgres `connections.config` JSON column. Slide 10 of the demo explicitly calls this out: *"credentials lagres i klartekst i konfig."* Any enterprise customer's security review will reject this.

### Goal
All sensitive fields are stored encrypted at rest. Plaintext never leaves the encryption layer.

### Approach
1. Add a key-encryption layer using AES-256-GCM with a key from env var `VRSKY_MASTER_KEY` (32 bytes, base64-encoded). Document KMS/HSM upgrade path.
2. Create `secrets` table: `id UUID, tenant_id, ciphertext BYTEA, nonce BYTEA, created_at, rotated_at`.
3. Add a `Secret` type to the connector config schema. Instead of `"password": "abc123"`, configs store `"password_secret_id": "<uuid>"`.
4. Add `secrets.go` in `src/pkg/managementapi/` with: `Put(tenantID, plaintext) → id`, `Get(tenantID, id) → plaintext`, `Rotate(id)`, `Delete(id)`.
5. Worker services (consumers/producers) fetch secrets through Management API at deploy time, never the DB directly.
6. Migration: write a one-off script that re-writes existing connection rows, moving any value whose JSON key matches `/password|secret|token|key/i` into the secrets table.
7. UI: when editing a connection, show secret fields as masked `••••••` with a "Replace" button.

### Acceptance criteria
- [ ] No plaintext credentials in any DB column at rest.
- [ ] Master key rotation procedure documented (re-wrap secrets, no plaintext re-entry).
- [ ] Secrets API enforces tenant isolation (tenant A cannot read tenant B's secret IDs).
- [ ] Migration script run successfully on staging with zero plaintext remaining.
- [ ] Audit log records every secret read (`secret_id`, `tenant_id`, `service`, `timestamp`).

### Files
- `src/pkg/managementapi/secrets.go` (new)
- `src/pkg/managementapi/handler.go` — `/api/v1/secrets` CRUD endpoints
- `migrations/NNN_create_secrets_table.sql`
- `migrations/NNN_encrypt_existing_credentials.sql`
- `ui/src/components/Pipeline/PropertyEditor.tsx` — masked input + replace UX
- `src/cmd/*-consumer/main.go`, `src/cmd/*-producer/main.go` — fetch via secret_id

### Effort
~2 weeks, 1 engineer.

---

## P1-2. Webhook signature validation (HMAC)

**Labels:** `P0-critical`, `security`, `b2b-blocker`, `phase-1`

**Body:**

### Problem
The webhook (HTTP consumer) accepts any POST without verifying the signature header. Stripe, GitHub, Twilio, Shopify, etc. all sign payloads — failing to verify is both a security hole and a vendor requirement.

### Goal
Each webhook consumer can be configured with an HMAC verification scheme. Unsigned/invalid payloads are rejected with 401.

### Approach
1. Extend webhook consumer config schema with optional `signature` block:
   ```json
   {
     "header": "X-Hub-Signature-256",
     "algorithm": "hmac-sha256",
     "secret_id": "<uuid>",
     "encoding": "hex" | "base64",
     "prefix": "sha256=" | ""
   }
   ```
2. Add presets in the UI for the top 5 providers (GitHub, Stripe, Twilio, Shopify, GitLab) so the user doesn't need to read docs.
3. In `src/cmd/http-consumer/main.go`, before publishing to NATS:
   - Read raw body (do NOT parse first — signature is over raw bytes).
   - Compute expected MAC, compare in constant time (`hmac.Equal`).
   - On mismatch: log, return 401, increment `webhook_signature_failures_total` metric.

### Acceptance criteria
- [ ] When `signature` block is configured, payloads without the header → 401.
- [ ] Payloads with a wrong signature → 401.
- [ ] Valid signatures pass through unchanged.
- [ ] Backward compatible: if `signature` is unset, behavior is unchanged.
- [ ] Stripe + GitHub presets selectable in the UI with one click.
- [ ] Failure counter visible in Prometheus.

### Files
- `src/cmd/http-consumer/main.go`
- `src/pkg/managementapi/schema.go` — webhook config schema
- `ui/src/components/Pipeline/PropertyEditor.tsx` — signature config UI + presets

### Effort
~3–4 days, 1 engineer.

---

## P1-3. Platform authentication — OIDC/SSO for the UI

**Labels:** `P0-critical`, `security`, `auth`, `b2b-blocker`, `phase-1`

**Body:**

### Problem
Today users authenticate to the platform UI/API with static API keys. Enterprise customers require SSO via their IdP (Google Workspace, Microsoft Entra, Okta). API keys remain for machine-to-machine use.

### Goal
Users sign in via OIDC. Email-based identity, mapped to a tenant + role. API keys become service-account credentials.

### Approach
1. Add OIDC client (e.g. `github.com/coreos/go-oidc/v3`) to Management API.
2. Per-tenant `oidc_config` table: `issuer_url, client_id, client_secret_id, allowed_domains[]`.
3. Routes: `GET /auth/login`, `GET /auth/callback`, `POST /auth/logout`.
4. Session: signed JWT cookie (HS256, 8h TTL, refresh).
5. On first login, auto-provision a `users` row (email, tenant_id, role=viewer). Admins promote.
6. UI: replace "API key" login screen with "Sign in with [provider]" buttons configured per tenant.
7. Keep `/api/v1/*` API-key auth for service accounts (no UI change for machine clients).

### Acceptance criteria
- [ ] Google + Microsoft OIDC tested end-to-end.
- [ ] Self-hosted Keycloak documented as the reference dev setup.
- [ ] Session cookie is `HttpOnly`, `Secure`, `SameSite=Lax`.
- [ ] Auto-provisioning respects `allowed_domains` (reject @gmail.com if tenant only allows @acme.com).
- [ ] API keys still work (independent codepath).
- [ ] Audit log records every login attempt (success + failure).

### Files
- `src/pkg/managementapi/auth_oidc.go` (new)
- `src/pkg/managementapi/middleware.go` — chain OIDC session before API key check
- `ui/src/pages/Login.tsx` (new)
- `migrations/NNN_oidc_users_tables.sql`

### Effort
~2 weeks, 1 engineer.

---

## P1-4. RBAC within a tenant

**Labels:** `P0-critical`, `security`, `b2b-blocker`, `phase-1`

**Body:**

### Problem
Today every user in a tenant is effectively an admin. Real customers have separation-of-duties requirements.

### Goal
Four roles: `owner`, `admin`, `editor`, `viewer`. Permission checks enforced server-side on every mutation.

### Approach
1. `roles` enum + `user_roles (user_id, tenant_id, role)` table.
2. Permission matrix:
   - `owner`: everything + tenant settings + billing.
   - `admin`: everything except tenant deletion/billing.
   - `editor`: create/update/deploy pipelines, manage secrets.
   - `viewer`: read-only.
3. Middleware: `requireRole(minRole)` wrapper on each handler.
4. UI: hide buttons the current user can't trigger, but **never trust client** — server is the source of truth.
5. The first user of a tenant is the owner. Owners invite + assign roles.

### Acceptance criteria
- [ ] Every API mutation has a documented minimum-role guard.
- [ ] Integration test: viewer attempting to deploy gets 403.
- [ ] Last-owner-deletion is rejected (must transfer ownership first).
- [ ] Role changes logged to audit log.

### Files
- `src/pkg/managementapi/rbac.go` (new)
- `src/pkg/managementapi/handler.go` — wrap handlers
- `ui/src/pages/Settings/Users.tsx` (new)
- `migrations/NNN_rbac.sql`

### Effort
~1 week, 1 engineer.

---

## P1-5. NATS JetStream — at-least-once delivery + DLQ

**Labels:** `P0-critical`, `reliability`, `b2b-blocker`, `phase-1`, related to #20

**Body:**

### Problem
Slide 10 / FAQ: *"Per nå bruker vi core NATS (at-most-once)."* For business-critical integrations (payments, orders, EDI), losing a message is unacceptable.

### Goal
All pipelines run on JetStream with explicit ack + retry. Permanently-failed messages land in a dead-letter stream that the user can inspect, retry, or discard.

### Approach
1. Replace each consumer→producer NATS subject pair with a JetStream stream:
   - Stream: `pipeline.<pipeline_id>` with file storage, retention=`workqueue`.
   - Durable consumer per producer with `AckPolicy=Explicit`, `MaxDeliver=5`, exponential backoff.
2. Add `pipeline.<id>.dlq` JetStream stream — 7-day retention.
3. Producer worker logic:
   - On success: `msg.Ack()`.
   - On retriable error: `msg.NakWithDelay(backoff)`.
   - On `MaxDeliver` exhaustion: NATS auto-routes to DLQ subject (via `MaxDeliver=5` + `Backoff` slices).
4. Management API: `GET /api/v1/pipelines/{id}/dlq`, `POST /api/v1/pipelines/{id}/dlq/{msg_id}/retry`, `POST /api/v1/pipelines/{id}/dlq/{msg_id}/discard`.
5. UI: a "Failed Messages" tab per pipeline.

### Acceptance criteria
- [ ] Killing a producer mid-message → message redelivered on restart, not lost.
- [ ] 5 consecutive failures → DLQ; 6th attempt does not happen.
- [ ] DLQ messages survive a NATS restart (file storage).
- [ ] Retry from DLQ publishes a new attempt with `MaxDeliver` reset.
- [ ] Prometheus: `vrsky_dlq_messages_total{pipeline_id=...}` gauge.

### Files
- `src/pkg/messaging/jetstream.go` (new)
- All `src/cmd/*-consumer/main.go`, `src/cmd/*-producer/main.go` — switch from `nc.Publish/Subscribe` to JetStream API
- `src/pkg/managementapi/dlq_handler.go` (new)
- `ui/src/pages/PipelineDetail/DLQTab.tsx` (new)

### Effort
~3 weeks, 2 engineers.

---

## P1-6. Encryption at rest — management Postgres + MinIO

**Labels:** `P0-critical`, `security`, `b2b-blocker`, `phase-1`

**Body:**

### Problem
Compliance reviews (SOC 2, ISO 27001, GDPR Art. 32) require encryption of data at rest.

### Goal
Both the management Postgres volume and the MinIO bucket are encrypted at rest. Documented per supported deployment target (Docker Compose, K3s, managed cloud).

### Approach
1. **K3s**: use a `StorageClass` backed by an encrypted disk (e.g. AWS gp3 + KMS, GCP PD with CMEK, or LUKS for self-hosted).
2. **MinIO**: enable SSE-S3 with `MINIO_KMS_AUTO_ENCRYPTION=on` + a KES sidecar holding the key.
3. **Postgres**: in managed cloud, enable native at-rest encryption. For self-hosted, document `dm-crypt`/LUKS on the data volume.
4. **Application-layer column encryption** for the highest-sensitivity columns (handled by P1-1 secrets manager).
5. Update `docs/DEPLOYMENT.md` with a compliance-mode checklist.

### Acceptance criteria
- [ ] `docs/DEPLOYMENT.md` has an "Encryption at rest" section per target.
- [ ] Helm chart in `deployments/k8s/` defaults to an encrypted storage class.
- [ ] MinIO healthcheck verifies KES is reachable; refuses to start otherwise (compliance mode).
- [ ] Compliance whitepaper draft (1–2 pages) added to `docs/`.

### Files
- `deployments/k8s/values.yaml`
- `deployments/k8s/templates/minio.yaml`
- `docs/DEPLOYMENT.md`, `docs/SECURITY.md` (new)

### Effort
~1 week, 1 engineer.

---

## P1-7. Audit log

**Labels:** `P0-critical`, `compliance`, `phase-1`

**Body:**

### Problem
No record of who did what, when. Required for SOC 2, ISO 27001, and customer security questionnaires.

### Goal
Every state-changing API call writes an immutable audit record. Visible per-tenant in the UI. Exportable.

### Approach
1. `audit_log` table: `id, tenant_id, user_id, action, resource_type, resource_id, before_json, after_json, ip, user_agent, occurred_at`. Append-only via DB trigger or `INSERT`-only DB role.
2. Middleware wraps every mutating handler — captures action + diff.
3. UI: read-only audit table at `/settings/audit`, paginated, filterable by user / resource / time.
4. Export: `GET /api/v1/audit?format=jsonl` for SIEM ingestion.

### Acceptance criteria
- [ ] Pipeline create/update/delete/deploy all logged.
- [ ] Login/logout, role changes, secret access logged.
- [ ] Audit table cannot be deleted by application role (DB-level RLS or `REVOKE DELETE`).
- [ ] Log retention: 365 days (configurable per tenant).
- [ ] Tenant isolation: cannot see another tenant's audit log.

### Files
- `src/pkg/managementapi/audit.go` (new)
- `src/pkg/managementapi/middleware.go` — install audit wrapper
- `ui/src/pages/Settings/Audit.tsx` (new)
- `migrations/NNN_audit_log.sql`

### Effort
~1 week, 1 engineer.

---

## P1-8. Quality-of-life bugs from the post-demo plan

**Labels:** `P1-high`, `ux`, `phase-1`

**Body:**

Fold the items from `/home/ludvik/.claude/plans/starry-discovering-frog.md` into one issue:
- [ ] Persistent webhook URL across redeploys (UPDATE existing connection instead of delete+create).
- [ ] File manager UI for file-producer output (list + delete, no `sudo` needed).
- [ ] Filter/converter preview works pre-deploy (done for tenant consumer — extend to file consumer by reading sample file directly).
- [ ] File ownership: stop chown-walking parent dirs; only chown what `MkdirAll` actually created (done).

### Effort
~1 week, 1 engineer.

---

## P1-9. Per-tenant quotas, rate limits, and isolation tests

**Labels:** `P1-high`, `security`, `multi-tenant`, `phase-1`, **replaces parts of #4**

**Body:**

### Problem
Issue #4 (Multi-Tenant Isolation & Authentication) defined per-tenant quotas, throughput limits, integration-count limits, and a test suite proving tenant A cannot reach tenant B's data. Basic API-key auth and PostgreSQL tenant rows are shipped, but the quota enforcement and the isolation test suite have not been built. Without them: (a) a single noisy tenant can starve the rest of the platform, (b) we have no proof that the isolation we claim actually holds, which is the first question any enterprise security review will ask.

### Goal
- App-level quotas on each tenant: max messages/sec, max active integrations, max stored bytes (Postgres + MinIO).
- Quota exceeded → HTTP 429 with `Retry-After` header, or NATS publish refused with a structured error.
- An end-to-end test suite that proves cross-tenant isolation at API, NATS, and DB layers.

### Approach
1. New `tenant_quotas` table: `tenant_id, max_msg_per_sec, max_integrations, max_storage_bytes, plan_name`. Seed with sensible defaults per plan.
2. New `src/pkg/managementapi/quotas.go` with `CheckMessageRate(tenantID)`, `CheckIntegrationCount(tenantID)`, `CheckStorage(tenantID)`. Use a token-bucket per tenant in memory (Redis if we go multi-replica).
3. Middleware: wrap producer publish + integration-create endpoints with quota checks.
4. Storage check runs as a hourly cron — flips a `tenant_quotas.storage_exceeded` flag that the create/upload paths respect.
5. UI: `/settings/usage` shows current usage vs limit; admins can request a plan upgrade.
6. Integration test suite under `tests/isolation/`:
   - Test A: Tenant B's API key cannot list/read/modify Tenant A's pipelines, connections, secrets, audit log, or DLQ messages.
   - Test B: Tenant B's worker cannot subscribe to `pipeline.<tenantA-pipeline-id>.*` NATS subjects.
   - Test C: PostgreSQL row-level checks — every query in handler.go must filter by `tenant_id`; a code-level lint enforces it.
   - Test D: MinIO bucket ACLs prevent cross-tenant object access.
   - Run on every PR.

### Acceptance criteria
- [ ] Burst publishing above `max_msg_per_sec` → 429 with `Retry-After`.
- [ ] Creating an integration past `max_integrations` → 429 with quota error.
- [ ] Exceeding `max_storage_bytes` blocks new uploads but does not corrupt existing data.
- [ ] All four isolation tests pass in CI.
- [ ] Linter fails the build if a new DB query is missing a `tenant_id` filter.
- [ ] Quota changes audit-logged (per P1-7).

### Files
- `src/pkg/managementapi/quotas.go` (new)
- `src/pkg/managementapi/middleware.go` — install quota middleware
- `src/pkg/managementapi/handler.go` — quota CRUD endpoints under `/api/v1/quotas`
- `migrations/NNN_tenant_quotas.sql`
- `tests/isolation/` (new directory)
- `tools/lint-tenant-filter/main.go` (new — static analyser for missing `tenant_id` filters)
- `ui/src/pages/Settings/Usage.tsx` (new)

### Effort
~1.5 weeks, 1 engineer.

---

# PHASE 2 — Connector reach (6–8 weeks)

Goal: cover the integration surface customers actually ask for.

---

## P2-1. OAuth 2.0 framework

**Labels:** `P0-critical`, `auth`, `connector`, `b2b-blocker`, `phase-2`, related to #18

**Body:**

### Problem
Salesforce, Microsoft 365, Google Workspace, HubSpot, Shopify, Slack — none of these support static API keys. Without OAuth, half of B2B integration scenarios are off the table (slide 10).

### Goal
Generic OAuth 2.0 client (auth-code flow with PKCE + refresh-token rotation) usable by any connector, plus 5 pre-configured providers.

### Approach
1. `oauth_providers` table: `tenant_id, provider_name, client_id, client_secret_id (→ secrets), auth_url, token_url, scopes[]`.
2. `oauth_grants` table: `provider_id, connection_id, access_token_secret_id, refresh_token_secret_id, expires_at`.
3. Routes:
   - `GET /api/v1/oauth/{provider}/authorize?connection_id=X` → redirect to provider.
   - `GET /api/v1/oauth/callback` → exchange code, store tokens via secrets manager.
   - Background refresher: when `expires_at < now+5min`, refresh.
4. Consumers/producers that need OAuth pull tokens by `connection_id` from Management API.
5. Pre-configure: Salesforce, Microsoft 365 (Graph), Google (Workspace), HubSpot, Shopify.
6. UI: "Connect to <Provider>" button in PropertyEditor → opens popup with the auth flow.

### Acceptance criteria
- [ ] Auth code + PKCE; no implicit flow.
- [ ] Refresh tokens stored encrypted (via P1-1).
- [ ] Token refresh happens transparently; in-flight requests retried once on 401.
- [ ] Revoke from UI triggers provider revocation endpoint.
- [ ] Audit log records grant + revoke.

### Files
- `src/pkg/oauth/` (new package)
- `src/pkg/managementapi/oauth_handler.go` (new)
- `src/cmd/http-consumer/main.go` — accept `oauth_connection_id` config
- `src/cmd/http-producer/main.go` — same
- `ui/src/components/Pipeline/OAuthConnect.tsx` (new)

### Effort
~3 weeks, 1 engineer.

---

## P2-2. SFTP consumer + producer

**Labels:** `P1-high`, `connector`, `phase-2`, related to #18

**Body:**

### Problem
EDI partners (logistics, banking, retail supply chain) overwhelmingly use SFTP. Slide 9 lists this as a roadmap gap.

### Goal
SFTP consumer (watch a remote dir, fetch + ack) and SFTP producer (upload file to remote dir). Both support key-based and password auth.

### Approach
1. New service `src/cmd/sftp-consumer/main.go` (use `github.com/pkg/sftp` + `golang.org/x/crypto/ssh`).
2. Config: host, port, username, auth (password_secret_id OR private_key_secret_id), remote_dir, poll_interval, after_action (delete | move | rename).
3. State: track processed filenames in NATS KV to prevent re-processing.
4. Producer: takes incoming NATS message → uploads as a file named per template (e.g. `order_{{.id}}_{{.timestamp}}.json`).

### Acceptance criteria
- [ ] Key auth + password auth both work.
- [ ] After-action `move` actually moves the file out of the watch dir.
- [ ] Reconnection on transient failure with backoff.
- [ ] Tested against `atmoz/sftp` Docker image in CI.

### Files
- `src/cmd/sftp-consumer/main.go` (new)
- `src/cmd/sftp-producer/main.go` (new)
- `docker-compose.yml`
- `ui/src/components/Pipeline/PropertyEditor.tsx` — config form

### Effort
~2 weeks, 1 engineer.

---

## P2-3. Kafka consumer + producer

**Labels:** `P1-high`, `connector`, `phase-2`, related to #18

**Body:**

### Problem
Many enterprises run Kafka as their event backbone. Currently no way to integrate.

### Goal
Kafka consumer (subscribe to topic, publish to NATS) and Kafka producer (subscribe to NATS, publish to topic). Support SASL/SCRAM and mTLS.

### Approach
Use `github.com/segmentio/kafka-go` or `github.com/twmb/franz-go`. Config: brokers, topic, consumer_group, auth (none | sasl-plain | sasl-scram-256 | sasl-scram-512 | mtls).

### Acceptance criteria
- [ ] Consumer group offset committed only after successful NATS publish.
- [ ] Producer waits for `acks=all`.
- [ ] mTLS works against a Confluent-like setup.
- [ ] CI integration test against `bitnami/kafka` Docker image.

### Effort
~2 weeks, 1 engineer.

---

## P2-4. RabbitMQ consumer + producer

**Labels:** `P2-medium`, `connector`, `phase-2`, related to #18

**Body:**

AMQP 0-9-1 via `github.com/rabbitmq/amqp091-go`. Config: URL, exchange, queue, routing_key, auth.

### Acceptance criteria
- [ ] Consumer uses manual ack and acks only after successful NATS publish.
- [ ] Producer publishes with delivery_mode=2 (persistent).

### Effort
~1 week, 1 engineer.

---

## P2-5. Salesforce connector

**Labels:** `P1-high`, `connector`, `phase-2`

**Body:**

Built on P2-1 (OAuth) + P2-7 (schema discovery).

Capabilities:
- Consumer: poll-based query (SOQL), CDC via Salesforce Streaming API, or platform events.
- Producer: REST insert/upsert + Bulk API 2.0 for batches > 200.

### Acceptance criteria
- [ ] Connect to a sandbox via OAuth without leaving the UI.
- [ ] Field mapping UI uses live schema from `describe`.
- [ ] Bulk API used automatically for batches ≥ 200.

### Effort
~2 weeks, 1 engineer.

---

## P2-6. S3 / Azure Blob / GCS connectors

**Labels:** `P1-high`, `connector`, `phase-2`

**Body:**

Cloud object-storage producer + consumer. One service, pluggable backend via interface.

### Acceptance criteria
- [ ] One connector handles all three providers via config.
- [ ] Server-side encryption configurable per bucket.
- [ ] Consumer supports event-driven mode (S3 notifications → SQS → NATS) AND poll mode.

### Effort
~2 weeks, 1 engineer.

---

## P2-7. Schema discovery + visual field mapping

**Labels:** `P1-high`, `ux`, `phase-2`

**Body:**

### Problem
Today a user has to know the exact JSON path of every field they want to filter/transform. Real users want to point-and-click.

### Goal
For any consumer, fetch a live sample and show the field tree. Drag fields from source to target in a visual mapping UI.

### Approach
- DB consumer: `information_schema` query.
- HTTP/webhook: previous payload (already implemented in PropertyEditor).
- File: parse first row.
- Tenant consumer: existing `/api/v1/sample-data/source` endpoint.
- Use the new sample-data endpoint extended per source type.
- React DnD-based mapping UI generates a JSONPath/JMESPath converter config.

### Acceptance criteria
- [ ] Click "Discover schema" → tree shows fields with types within 3s for a 10MB sample.
- [ ] Drag-mapping produces a valid converter config that round-trips through the platform.

### Effort
~2 weeks, 1 frontend + 1 backend engineer.

---

## P2-8. Connection test button

**Labels:** `P2-medium`, `ux`, `phase-2`

**Body:**

"Test connection" button in the connector config UI that calls `POST /api/v1/connections/test` with the draft config (without persisting). Returns ok/error and a sample of received data.

### Acceptance criteria
- [ ] Works for HTTP, DB, SFTP, file, OAuth-backed connectors.
- [ ] Returns within 10s or times out gracefully.
- [ ] Does not write to the DB.

### Effort
~3 days, 1 engineer.

---

## P2-9. Connector SDK package + testing framework

**Labels:** `P1-high`, `developer-experience`, `phase-2`, **replaces parts of #18**

> **Sequencing note:** This must land **before** P2-2 (SFTP), P2-3 (Kafka), P2-4 (RabbitMQ), P2-5 (Salesforce), P2-6 (S3/Azure/GCS) — those connectors should all be built on top of the SDK. Schedule it first in Phase 2.

**Body:**

### Problem
Issue #18 (Connector SDK & Essential Connectors) called for a formal `pkg/sdk/` with base interfaces, helpers (logging, metrics, config, retries), and a testing framework. The connectors shipped (HTTP, File, Postgres consumer + producer) instead hand-roll all of this in each `cmd/*/main.go`. Result: ~40% of each new connector is duplicated boilerplate, and there's no consistent way to unit-test a connector before wiring it into a full pipeline. Slide 5 lists "Pluggbare koblinger" as a core differentiator — without an SDK the marketplace ambition (slide 9) is unachievable.

### Goal
- A versioned `pkg/sdk/` package providing `BaseConsumer`, `BaseProducer`, `BaseConverter`, `BaseFilter` types and helpers.
- A testing framework that lets you write `TestMyConsumer(t)` without spinning up Docker.
- Existing HTTP/File/Postgres connectors refactored onto the SDK.
- Documentation for third-party connector authors.
- Boilerplate per new connector reduced by ≥70%.

### Approach
1. Define interfaces in `pkg/sdk/types.go`:
   ```go
   type Consumer interface {
       Start(ctx context.Context, publish func(msg Message) error) error
       Stop(ctx context.Context) error
       HealthCheck() error
   }
   type Producer interface {
       Handle(ctx context.Context, msg Message) error
       Stop(ctx context.Context) error
       HealthCheck() error
   }
   // ...similar for Converter, Filter
   ```
2. `pkg/sdk/base/` — embeddable structs that handle NATS connection, config reload, graceful shutdown, panic recovery, health/readiness HTTP server.
3. `pkg/sdk/logging/` — JSON structured logger with mandatory `tenant_id`, `pipeline_id`, `connection_id`, `service` fields. Replaces ad-hoc `log.Printf` calls.
4. `pkg/sdk/metrics/` — Prometheus helpers: `RegisterStandardConnectorMetrics(connectorName)` returns the standard counter/histogram set.
5. `pkg/sdk/config/` — config struct loader with validation and hot-reload via Management API.
6. `pkg/sdk/retry/` — exponential backoff + jitter helpers.
7. `pkg/sdk/testing/` — `harness.NewConsumerHarness(t, MyConsumer{})` provides an in-memory NATS, fake Management API, and assertion helpers.
8. Refactor `src/cmd/http-consumer`, `src/cmd/http-producer`, `src/cmd/file-consumer`, `src/cmd/file-producer`, `src/cmd/postgres-consumer`, `src/cmd/postgres-producer` to use the SDK.
9. Update `cmd/example-connector/` with a 100-line reference implementation.

### Acceptance criteria
- [ ] Each of the 6 existing connectors compiles + passes its old behaviour tests after refactor.
- [ ] Reference implementation in `cmd/example-connector/` is under 150 LoC and works end-to-end.
- [ ] `pkg/sdk/testing/` harness can test a consumer without Docker.
- [ ] `pkg/sdk/` builds standalone (no import of `cmd/...`).
- [ ] SDK has ≥80% test coverage.
- [ ] Documented at `docs/sdk/`; covered by P4-3 tutorial.

### Files
- `src/pkg/sdk/` (new package tree)
- `src/cmd/example-connector/` (new reference)
- Refactor: `src/cmd/http-consumer/main.go`, `src/cmd/http-producer/main.go`, `src/cmd/file-consumer/main.go`, `src/cmd/file-producer/main.go`, `src/cmd/postgres-consumer/main.go`, `src/cmd/postgres-producer/main.go`
- `docs/sdk/` (new)

### Effort
~2.5 weeks, 1–2 engineers.

---

# PHASE 3 — Operational maturity (4–6 weeks)

Goal: pass an SRE review. Customers can rely on this in production.

---

## P3-1. Alerting rules + notifier integration

**Labels:** `P1-high`, `observability`, `phase-3`, related to #8 #22

**Body:**

### Goal
Ship a default Prometheus alert ruleset and pluggable notifiers (Slack, email, PagerDuty, webhook).

### Default rules
- Pipeline down (no messages in N minutes vs. baseline).
- DLQ growing (>0 for 10 minutes, or rate > threshold).
- NATS JetStream lag > threshold.
- Cert expiry < 14 days.
- Management API error rate > 5% over 5m.
- Disk usage > 80%.

### Notifier
- Alertmanager already in stack — configure receivers per tenant.
- UI: `/settings/notifications` to manage targets.

### Acceptance criteria
- [ ] All 6 default rules fire correctly in a staged test.
- [ ] Slack + email notifier confirmed working.
- [ ] Per-tenant notification routing (Slack channel per tenant).

### Effort
~1 week, 1 engineer.

---

## P3-2. Health/readiness probes audit

**Labels:** `P1-high`, `reliability`, `phase-3`

**Body:**

Confirm every microservice exposes `/healthz` (liveness) and `/readyz` (readiness, includes upstream checks: NATS connected, DB reachable). K8s deployment uses both. Rolling deploys don't drop in-flight messages.

### Acceptance criteria
- [ ] All 17 microservices have both probes wired.
- [ ] Rolling deploy of a producer during 1 msg/s load loses zero messages (verified with DLQ + ack metrics).

### Effort
~3 days, 1 engineer.

---

## P3-3. Backup + restore documentation and tooling

**Labels:** `P1-high`, `operations`, `phase-3`

**Body:**

### Goal
A documented, tested DR plan for the management Postgres.

### Approach
- Helm chart cron-job that runs `pg_dump`, encrypts with the master key, and uploads to a customer-configurable S3 bucket.
- `vrsky-cli restore <backup-url>` command.
- Quarterly restore drill in CI (restore a backup into an ephemeral env, assert pipelines listable).

### Acceptance criteria
- [ ] Backup cron documented + ships in Helm chart.
- [ ] Restore drill passes in CI.
- [ ] RPO ≤ 24h, RTO ≤ 1h documented in `docs/DR.md`.

### Effort
~1 week, 1 engineer.

---

## P3-4. Open Telemetry — distributed tracing

**Labels:** `P2-medium`, `observability`, `phase-3`, **closes #32**

**Body:**

### Goal
Every message carries a trace context. End-to-end traces visible in Grafana Tempo (or Jaeger).

### Approach
- Add `go.opentelemetry.io/otel` SDK to all Go services.
- Propagate context via NATS message headers (`traceparent`, `tracestate`).
- Instrument: HTTP entry points, NATS publish/consume, DB queries, outbound HTTP.
- Add Tempo + datasource to existing Grafana.

### Acceptance criteria
- [ ] A single trace shows: webhook receive → filter → converter → producer publish → external API call.
- [ ] Per-pipeline latency breakdown visible.
- [ ] Tail-sampling configured to retain all error traces + 1% of success traces.

### Effort
~1.5 weeks, 1 engineer.

---

## P3-5. Load test harness + capacity baselines

**Labels:** `P1-high`, `performance`, `phase-3`, related to #15

**Body:**

### Goal
Document real numbers, not aspirational ones. Confirm or correct slide-5's "millions of messages per second" claim.

### Approach
- k6 scripts under `tests/load/`.
- Scenarios: pure webhook→HTTP, DB CDC→HTTP, file→DB, tenant→tenant.
- Run weekly in CI on a fixed-size cluster, results published to a Grafana panel.

### Acceptance criteria
- [ ] Documented p99 latency + sustained msg/s for each of the 4 scenarios.
- [ ] Numbers reproducible: same hardware, same script → same result ±10%.
- [ ] Marketing claims updated to match.
- [ ] **Per-connector throughput floors enforced as named scenarios** (extracted from #18): HTTP consumer ≥1K req/s sustained, HTTP producer ≥1K req/s sustained, SFTP poll cycle ≤5s on 1K-file dir, Kafka consumer ≥10K msg/s. Build fails if any floor regresses.
- [ ] **API gateway latency ceiling enforced** (extracted from #8): the gateway adds ≤10ms p99 over a direct-to-service call in the same scenario. Tracked as `vrsky_gateway_overhead_p99_ms` and asserted in CI.

### Effort
~1 week, 1 engineer.

---

## P3-6. mTLS for high-security customers

**Labels:** `P2-medium`, `security`, `phase-3`

**Body:**

### Goal
HTTP consumer + HTTP producer can be configured to require client certificates. Used for bank/public-sector integrations.

### Approach
- Add `tls.client_ca_secret_id`, `tls.cert_secret_id`, `tls.key_secret_id` to HTTP connector config.
- Consumer: terminate TLS with client-cert verification.
- Producer: present client cert.

### Acceptance criteria
- [ ] Consumer rejects connection without a valid client cert when configured.
- [ ] Producer test connection succeeds against an mTLS-required endpoint.

### Effort
~1 week, 1 engineer.

---

## P3-7. API gateway rate limiting

**Labels:** `P2-medium`, `security`, `observability`, `phase-3`, **replaces parts of #8**

**Body:**

### Problem
Issue #8 specified per-tenant rate limiting at the API gateway as edge protection — distinct from app-level quotas (P1-9) which protect downstream resources. Without gateway-level rate limiting, a misbehaving client (intentional or not) can saturate the management API even before quota checks run. Today the Traefik install has no rate-limit middleware configured.

### Goal
- Per-tenant rate limit at the gateway, keyed off the API key (or OIDC user from P1-3).
- Configurable per plan (free / pro / enterprise).
- 429 with `Retry-After` returned by the gateway, not the app.

### Approach
1. Add Traefik `RateLimit` middleware in `deployments/k8s/traefik/middlewares.yaml`:
   ```yaml
   apiVersion: traefik.io/v1alpha1
   kind: Middleware
   metadata:
     name: tenant-rate-limit
   spec:
     rateLimit:
       average: 100
       burst: 200
       sourceCriterion:
         requestHeaderName: X-API-Key
   ```
2. Per-plan overrides via Traefik dynamic config — Management API publishes a config file (or via Traefik CRD) when a tenant's plan changes.
3. For docker-compose deployments: same middleware in Traefik static config.
4. Metrics: scrape Traefik metrics endpoint for `traefik_service_request_duration_seconds` + `traefik_ratelimit_dropped_total`.
5. Document in `docs/SECURITY.md`.

### Acceptance criteria
- [ ] Burst above the configured rate returns 429 with `Retry-After` from Traefik (not the app).
- [ ] Different tenants have independent limits (one tenant being rate-limited does not affect another).
- [ ] Plan upgrade propagates new limit within 30 seconds without restarting Traefik.
- [ ] `traefik_ratelimit_dropped_total` scraped by Prometheus and visible in Grafana.

### Files
- `deployments/k8s/traefik/middlewares.yaml` (new)
- `deployments/k8s/traefik/dynamic-config.yaml` (new)
- `docker-compose.yml` — Traefik static config block
- `src/pkg/managementapi/handler.go` — `/api/v1/tenants/{id}/plan` writes Traefik config
- `docs/SECURITY.md`

### Effort
~4 days, 1 engineer.

---

## P3-8. Centralized logging (Loki)

**Labels:** `P2-medium`, `observability`, `phase-3`, **replaces parts of #8**

**Body:**

### Problem
Issue #8 specified Loki for centralized log aggregation. Today logs are scattered across container stdout — `docker compose logs -f` works for a single dev box but is unusable for a multi-replica K3s deployment, and there's no way for a customer support engineer to find "all logs touching pipeline X over the last hour". Logs also lack consistent structured fields, so even grep-based investigation is painful.

### Goal
- Loki deployed alongside Prometheus + Grafana.
- All 17 services emit JSON-structured logs with consistent labels.
- Searchable in Grafana with 7-day default retention.

### Approach
1. Deploy Loki via Helm chart in `deployments/k8s/loki/`. Sized for 7-day retention (~50 GB for ~100 msg/s baseline).
2. Deploy Promtail or Grafana Alloy as a DaemonSet to scrape pod stdout.
3. Add Loki as a Grafana datasource.
4. Refactor logging in all services to use the new `src/pkg/logging/` package (shared by P2-9 SDK). Mandatory fields: `tenant_id`, `pipeline_id`, `connection_id`, `service`, `trace_id` (from P3-4), `level`, `msg`.
5. Promtail relabel config extracts these fields as Loki labels.
6. Pre-built Grafana panels: "Logs for pipeline X", "Errors per tenant per hour", "Connection failures".
7. For docker-compose dev: same Loki + Promtail stack with smaller retention.

### Acceptance criteria
- [ ] Querying `{pipeline_id="abc-123"}` in Grafana returns all logs touching that pipeline across every service.
- [ ] Every log line in every service has the 5 mandatory fields (verified by a lint test).
- [ ] 7-day retention enforced; older logs automatically deleted.
- [ ] Loki disk usage stays under the configured ceiling under normal load.
- [ ] Logs from sensitive operations (secret access, login) do not include the secret value itself.

### Files
- `deployments/k8s/loki/` (new)
- `deployments/k8s/promtail/` (new)
- `src/pkg/logging/` (new shared package — also used by P2-9)
- Refactor `log.Printf` calls in all `src/cmd/*/main.go` and `src/pkg/managementapi/*.go`
- `docs/OBSERVABILITY.md` (new)

### Effort
~1.5 weeks, 1 engineer.

---

# PHASE 4 — Commercial (in parallel with first pilots)

Goal: be sellable, billable, and onboardable.

---

## P4-1. Per-tenant usage metering

**Labels:** `P1-high`, `billing`, `phase-4`

**Body:**

### Goal
Count messages, deploys, storage per tenant. Surface in `/settings/usage`. Exportable to Stripe or invoice generator.

### Approach
- A "metering" counter increments on each successful producer publish.
- Daily rollup job writes `usage_daily` aggregates.
- UI dashboard + CSV export.

### Acceptance criteria
- [ ] Counters survive service restart (Prometheus + Postgres snapshot).
- [ ] Tenant-visible page shows current month usage.

### Effort
~1 week, 1 engineer.

---

## P4-2. Onboarding flow for non-developers

**Labels:** `P1-high`, `ux`, `phase-4`

**Body:**

### Goal
A non-developer can deploy a first pipeline in <10 minutes.

### Approach
- First-login wizard: pick template (Webhook→Slack, CSV→Database, Salesforce→HubSpot).
- Pre-fills 80% of the config; user only fills credentials.
- Inline help text + links per field.
- Sample-data pre-populated; click "Test" to see it work.

### Acceptance criteria
- [ ] User-testing with 3 non-developers: all complete the wizard in <10 min without help.
- [ ] At least 4 templates available at launch.

### Effort
~2 weeks, 1 frontend engineer.

---

## P4-3. Documentation set

**Labels:** `P1-high`, `docs`, `phase-4`, **replaces parts of #8, #18**

**Body:**

Ship `docs/` covering:
- **Operator docs**: install, upgrade, backup (P3-3), monitoring (P3-1, P3-4, P3-8), troubleshooting, encryption-at-rest checklist (P1-6).
- **Integrator docs**: one page per connector (consumer + producer) with config reference, examples, and OAuth flow if applicable.
- **API reference auto-generated from OpenAPI** (extracted from #8): Management API serves its OpenAPI spec at `/openapi.json`; Swagger UI hosted at `/docs`. CI fails the build if a handler is missing an OpenAPI annotation. The spec is also re-rendered into the static docs site.
- **Security whitepaper** (1–2 pages): covers P1-1 (secrets), P1-6 (at-rest), P1-7 (audit), P3-6 (mTLS), P1-9 (isolation tests).
- **"Build your first pipeline" tutorial**: clickthrough for non-developers, completable in <10 minutes.
- **"Build your first connector" tutorial** (extracted from #18): step-by-step using the P2-9 SDK, builds a working consumer from scratch in <30 minutes; published in `docs/sdk/tutorial/`.

### Acceptance criteria
- [ ] Docs site published (e.g. `docs.vrsky.example`) generated from `docs/` via mkdocs or similar.
- [ ] All connectors (existing + Phase 2 new ones) documented before their issue can close.
- [ ] OpenAPI spec auto-generated and served at `/openapi.json`; CI lint blocks merges that add handlers without OpenAPI annotations.
- [ ] Swagger UI at `/docs` works against a live Management API.
- [ ] Two user-tested tutorials: "first pipeline" (≤10 min) and "first connector" (≤30 min).

### Effort
~2.5 weeks, 1 technical writer + 1 engineer.

---

## P4-4. Status page + uptime SLA

**Labels:** `P2-medium`, `operations`, `phase-4`

**Body:**

Public status page (`status.vrsky.example`), auto-driven by Prometheus probe data. SLA template in `docs/SLA.md`.

### Effort
~3 days, 1 engineer.

---

# Deferred but acknowledged

Keep these GitHub issues open and unchanged:
- **#10 Marketplace** — Phase 5+ when there's real connector adoption.
- **#11 Storage-as-a-Service** — Phase 5+ as a paid add-on.
- **#19 NATS Auto-Scaling**, **#21 Service Discovery for Tenant NATS** — only relevant if we move from shared platform NATS to per-tenant NATS instances. Revisit when a customer hits the shared-NATS ceiling.

# Execution summary

| Phase | Issues | Effort (eng-weeks) | Calendar |
|-------|--------|--------------------|----------|
| 1 — Trust foundation | P1-1..P1-9 (9 issues) | ~13.5 | 5–6 weeks (2 engineers in parallel) |
| 2 — Connector reach | P2-1..P2-9 (9 issues) | ~18.5 | 7–8 weeks (2 engineers); **P2-9 SDK first** |
| 3 — Operational maturity | P3-1..P3-8 (8 issues) | ~8 | 5 weeks (1–2 engineers) |
| 4 — Commercial | P4-1..P4-4 (4 issues) | ~5.5 | parallel with pilots |

**Total: 30 new issues**, ~45 engineer-weeks.

**Critical path to first paying B2B customer:** Phase 1 complete + P2-9 (SDK) → P2-1 (OAuth) → P2-5 (Salesforce) or P2-2 (SFTP) depending on target vertical + P3-1 (alerting) + P3-3 (backup) + P3-8 (centralized logs). Estimated 11–13 weeks with 2 engineers.

**Existing GitHub issues to close after the new ones are filed:** #4, #8, #12, #14, #18, #20, #22, #32 — see the cross-reference section at the top of this file for the exact mapping. Keep open: #10, #11, #19, #21.
