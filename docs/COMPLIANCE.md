# VRSky compliance whitepaper

> 2-page summary of the security posture for SOC 2 / ISO 27001 / GDPR
> reviews. Intended for customer security questionnaires and pre-sales
> due-diligence rounds.

## 1. Architecture in one paragraph

VRSky is an iPaaS — a control plane (Management API, Postgres, NATS) plus
a fleet of data-plane workers (consumers, converters, filters, producers)
that move payloads between customer systems. Every customer ("tenant") is
strictly isolated at the data layer; the platform itself is multi-tenant.
Payloads flow only **in transit** through NATS JetStream and are deleted
from the stream once acked — there is no long-term message store. The
only persistent state is configuration (Postgres) and the encrypted
secrets used by connectors.

## 2. Encryption posture

| Layer | Default | Compliance mode |
|-------|---------|-----------------|
| **Connector credentials** | AES-256-GCM, master key from `ENCRYPTION_KEY` (#66). Plaintext never written to Postgres. | Same. Master key sourced from KMS-backed Kubernetes Secret. |
| **Postgres** | `local-path` on host filesystem. | Encrypted StorageClass (cloud KMS) or LUKS-backed disk. |
| **MinIO objects** | At-rest on the same StorageClass. | SSE-S3 via KES sidecar; key wrapped by Vault / AWS KMS / GCP KMS / Azure Key Vault. |
| **NATS JetStream** | At-rest on the same StorageClass. Messages are short-lived. | Same — encryption inherited from the underlying disk. |
| **Audit log** | Append-only DB triggers + on the same encrypted disk (#72). | Same. |
| **TLS in transit** | Terminated at Traefik ingress with cert-manager. Internal cluster traffic relies on the cluster-internal CA. | Same; internal mTLS optional via service mesh. |

See `docs/DEPLOYMENT_GUIDE.md` → "Encryption at rest" for per-target setup.

## 3. Identity & access

- **Human users**: OIDC SSO (#68). Per-tenant config; supports Google,
  Microsoft, Okta, Keycloak. PKCE + nonce + state, `allowed_domains`
  enforcement.
- **Machine clients**: per-tenant API keys with HMAC-hashed storage.
- **Sessions**: `vrsky_session` HttpOnly + Secure + SameSite=Lax cookie,
  8-hour TTL.
- **RBAC** (#69): four roles per tenant (viewer / editor / admin /
  owner). Mutating endpoints require ≥ editor; tenant-level changes
  require owner. Last-owner protection enforced server-side.
- **Webhook signatures** (#67): HMAC-SHA256 / SHA1 / SHA512 verified on
  every incoming webhook; signing key stored encrypted via #66.

## 4. Audit & accountability

- **Append-only audit log** (#72) — DB triggers reject UPDATE/DELETE.
  Every state-changing API call is recorded with actor, action,
  resource, status, IP, user agent, request ID. Sensitive reads on
  `/api/v1/secrets/*` are also recorded.
- **Auth audit log** — login, logout, failure, OIDC denial events.
- 365-day retention by default; configurable per tenant.
- JSONL export for SIEM ingestion (`GET /api/v1/audit?format=jsonl`).

## 5. Data isolation

- Every Postgres query in the management API filters on `tenant_id`.
- Cross-tenant data sharing is opt-in: a request must be approved by
  the target tenant; access is recorded per-payload in
  `tenant_data_access_log`.
- NATS JetStream subjects are namespaced as
  `vrsky.data.<tenant>.pipeline.<conn>` — consumers filter by tenant
  via durable consumer config; the message body's envelope additionally
  carries the tenant ID.
- Connector secrets live in a per-tenant column; UI and API reject any
  attempt to read another tenant's secret with 404 (no enumeration leak).

## 6. Threat model — what's in scope

| Threat | Mitigated? | How |
|--------|------------|-----|
| Stolen Postgres dump | ✅ | Secrets encrypted; rest of schema low-sensitivity by design (no payload bodies). |
| Stolen disk image | ✅ in compliance mode | StorageClass encryption + KES wraps MinIO keys. |
| Compromised application connection | ✅ for audit / ⚠️ for secrets | Audit table append-only; secrets exposed if `ENCRYPTION_KEY` is also stolen. |
| Replay of a captured request | ✅ | Session tokens server-side stored as hashes; API keys hashed; OIDC nonce/state prevent replay through the IdP. |
| Tenant A reading tenant B | ✅ | Per-query `tenant_id` filter + isolation tests (#74 — in flight). |
| Long-term data residency / GDPR right-to-erasure | ✅ | Ephemeral payloads (NATS WorkQueue + acked = deleted). Connection rows + audit rows deletable on request; backups can be excluded per tenant. |

## 7. Out of scope / known gaps

- **HSM-backed master key.** Today `ENCRYPTION_KEY` is a 32-byte env var.
  KMS envelope-encryption (load on boot, never persisted) is a Phase 5
  upgrade.
- **DLP / payload scanning.** VRSky neither inspects payload bodies for
  PII nor blocks based on content. Customers handle classification.
- **Anonymous read access.** A few read-only endpoints currently only
  check `X-Tenant-ID`. Tightening to require auth is a small follow-up
  to #69 (out of scope for that issue).
- **SOC 2 Type II attestation.** This whitepaper is preparation material;
  formal attestation is a separate engagement.

## 8. Control mapping (excerpt)

| Control | Where it's met |
|---------|----------------|
| **SOC 2 CC6.1** — Logical access | OIDC (#68) + RBAC (#69) + audit (#72) |
| **SOC 2 CC6.6** — Encryption | #66 + this doc + StorageClass overlays |
| **SOC 2 CC7.2** — Monitoring | Prometheus stack (#22, in progress) + DLQ alerts (#70) |
| **ISO 27001 A.5.15** — Access control | RBAC (#69) |
| **ISO 27001 A.8.24** — Cryptography | #66 secrets + #71 encryption-at-rest |
| **ISO 27001 A.8.15** — Logging | #72 audit + auth_audit_log |
| **GDPR Art. 32** — Security of processing | This whitepaper end-to-end |

---

**Document owner**: Security  
**Last reviewed**: 2026-05-13  
**Next review**: when any Phase 1 issue body changes
