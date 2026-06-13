# VRSky Security

This document covers the platform's security posture for credentials,
configuration secrets, and the master encryption key that wraps them.

## Secrets at rest (issue #66 — Phase 1A)

Every credential a connector needs — DB passwords, HTTP bearer tokens,
basic-auth passwords, API keys, HMAC signing secrets, OAuth refresh tokens —
is stored encrypted in the `secrets` table in the management Postgres
database. Plaintext is never written to disk by the application.

### Cipher

- **Algorithm:** AES-256-GCM with a random 96-bit nonce per ciphertext.
- **Wire format:** `aes256:` + base64( nonce || ciphertext )
- **Key:** 32 bytes (256 bits), provided as 64 hex characters via the
  `ENCRYPTION_KEY` environment variable. The management-api refuses to start
  without it.
- Reference implementation lives in `src/pkg/crypto/secretbox.go`.

### Reference model

Connector configs hold references — never plaintext:

```jsonc
{
  "password_secret_id": "f0e0a3c2-…"  // resolved at worker startup
}
```

For database DSNs (a single string containing user, password, host, etc.)
the password is replaced inline with a placeholder:

```
postgres://app:{secret:f0e0a3c2-…}@db.internal:5432/orders
```

Workers call `crypto.ResolveSecrets` in
`src/pkg/crypto/resolver.go` after loading a connection from the management
DB. The function expands every `<key>_secret_id` and every `{secret:<uuid>}`
placeholder into plaintext in-process; nothing leaves the worker boundary
in cleartext form.

### Tenant isolation

The `secrets` table has a `tenant_id` column and every read/write filters
on it. The HTTP handlers in `src/pkg/managementapi/secrets_handler.go`
additionally verify ownership at the application layer:

- Tenant A's API key cannot fetch tenant B's secret (returns 404, not 403,
  to avoid confirming existence).
- Deleting a secret refuses with 409 if any connection in the same tenant
  references it.

## Generating an ENCRYPTION_KEY

```
openssl rand -hex 32
```

Store the output in your secrets manager (Doppler, AWS SSM, Vault, ...) and
inject as `ENCRYPTION_KEY` into the management-api container and every
worker that needs to decrypt credentials.

## Rotating the master key

There are two flavours of rotation:

### Re-wrap a single secret

A user-triggered operation that keeps the same plaintext but generates a
fresh nonce (defence against nonce-reuse attacks if a leak is suspected):

```
POST /api/v1/secrets/{id}/rotate
```

### Rotate the master key itself

Done out-of-band when the master key may have been compromised. The
high-level procedure:

1. Generate a new 64-hex-character key. Call it `NEW_KEY`.
2. Stop the management-api with the old key (graceful — workers can keep
   running).
3. Run the re-wrap helper:
   ```
   ENCRYPTION_KEY_PREVIOUS=$OLD_KEY ENCRYPTION_KEY=$NEW_KEY \
     go run ./cmd/migrate-secrets --rewrap
   ```
   *(The `--rewrap` flag is a follow-up addition to migrate-secrets and is
   not part of #66; track in a future issue.)*
4. Roll out the new `ENCRYPTION_KEY` to all services. Workers using the old
   key will fail to decrypt until they restart.

Audit-trail entries for rotation events will be added when issue #72
(audit log) lands.

## RBAC (issue #69 — Phase 1D)

Every member of a tenant has one of four roles. Roles are ordered:
`viewer < editor < admin < owner`. The server enforces a minimum role on
each route; the UI hides controls it knows are denied but always
re-checks server-side.

### Permission matrix

| Operation                                            | Min role |
|------------------------------------------------------|----------|
| List / read connections, secrets metadata, audit log | viewer   |
| Create / update / start / stop a pipeline            | editor   |
| Create / update / rotate a secret                    | editor   |
| Retry / discard a DLQ message                        | editor   |
| Send test data through a pipeline                    | editor   |
| Delete a connection                                  | admin    |
| Delete a secret                                      | admin    |
| Read / update / delete tenant's OIDC config          | admin    |
| Change another member's role                         | owner    |
| Remove another member                                | owner    |
| Approve / deny / revoke cross-tenant data connection | owner    |
| Rotate the tenant API key                            | owner    |
| Delete the tenant                                    | owner    |

### Authentication

Two ways to authenticate against mutating endpoints:

- **Session token** (issued by `/auth/login` or OIDC callback) — present as
  `Authorization: Bearer <token>` or via the `vrsky_session` cookie. The
  server looks up the user's role in the tenant from `X-Tenant-ID`.
- **Tenant API key** — opaque token bound to one tenant. Treated as
  effective role **admin**: enough to deploy / manage resources, but
  cannot transfer ownership or change tenant-wide settings.

### Last-owner protection

Every tenant must keep at least one `owner`. The server refuses with 409
when a role change or member removal would leave zero owners. The UI
disables the relevant controls when the row is the sole owner.

### Audit

Every role change / member removal emits an audit_log row (#72). The
middleware auto-captures the request; `member.role_change` and
`member.remove` actions include `target_user_id` and (for role changes)
`new_role` in the details column.

## OIDC / SSO (issue #68 — Phase 1C)

Each tenant can configure one OIDC provider. The client secret is stored
encrypted in the `secrets` table (#66); only the UUID reference lives on
`oidc_config`. The flow is auth-code with PKCE (S256), nonce, and state.

### Tenant configuration

Admins of a tenant call `PUT /api/v1/tenants/{tenant_id}/oidc` with:

```json
{
  "issuer_url": "https://accounts.google.com",
  "client_id": "...",
  "client_secret": "...",
  "redirect_url": "https://app.vrsky.example/api/v1/auth/oidc/callback",
  "scopes": ["openid", "email", "profile"],
  "allowed_domains": ["acme.com"],
  "default_role": "viewer",
  "provider_label": "Acme SSO"
}
```

The same `redirect_url` must be registered with the IdP. `allowed_domains`
is enforced server-side: a user whose email domain is not in the list is
denied at callback time, before any session is minted.

### Sign-in flow

1. UI → `GET /api/v1/auth/oidc/{slug}/login` (slug = tenant slug)
2. Management-API → redirect to IdP (state + PKCE + nonce in cookies)
3. IdP → `GET /api/v1/auth/oidc/callback?code=…&state=…`
4. Management-API validates state / exchanges code / verifies ID-token /
   matches nonce / checks allowed_domains / finds or auto-provisions the
   user / mints a session / sets `vrsky_session` cookie
5. Browser is redirected to `/`

### Local development with Keycloak

The reference dev setup is Keycloak in compose:

```yaml
keycloak:
  image: quay.io/keycloak/keycloak:24.0
  command: start-dev
  environment:
    KEYCLOAK_ADMIN: admin
    KEYCLOAK_ADMIN_PASSWORD: admin
  ports: ["8081:8080"]
```

Create a realm `vrsky-dev`, a confidential client `vrsky-app` with redirect
URI `http://localhost:3000/api/v1/auth/oidc/callback`, then point the
tenant's OIDC config at `http://localhost:8081/realms/vrsky-dev`.

Note: `redirect_url` must use `https://` in production. `http://localhost*`
is allowed as a special case for local dev.

### Audit

Every OIDC login attempt — success, failure, denial-by-domain — emits a row
in `auth_audit_log` with event type `oidc_login`. The general audit log
(#72) records the callback hit as `oauth.write`.

## Threat model

In scope:
- Database dump leak → all credentials remain encrypted, requiring the
  master key to recover.
- Casual read access to the DB → no plaintext visible.
- A misconfigured backup that includes Postgres but not the secret
  manager → same as above.

Out of scope (planned for later phases):
- Compromise of a service host (the master key is in memory).
- KMS/HSM-managed master key. The env-var design is intentional first
  step; envelope encryption against a cloud KMS is tracked separately as
  part of Phase 5.

## Verification

After deploying:

```bash
# 1. Management-api refuses to start without the key.
ENCRYPTION_KEY="" docker compose up management-api  # expect Fatal

# 2. Create a secret.
curl -X POST localhost:3000/api/v1/secrets \
  -H "X-Tenant-ID: $TENANT" -H "Authorization: Bearer $API_KEY" \
  -d '{"name":"pg-pwd","value":"hunter2"}'

# 3. Read it back — value must NOT appear in the response.
curl localhost:3000/api/v1/secrets/$ID \
  -H "X-Tenant-ID: $TENANT" -H "Authorization: Bearer $API_KEY"

# 4. Verify the database holds ciphertext only.
psql -c "SELECT ciphertext FROM secrets WHERE id = '$ID'"
# expected: aes256:<base64...>
```

## Gateway rate limiting (Phase 3G, #90)

Application quotas (#74) protect downstream resources, but they run *inside* the
management API — a misbehaving client can still saturate the API before a quota
check executes. The **gateway** (Traefik) adds edge protection: it rejects
excess traffic with **429 + `Retry-After` before the request ever reaches the
app**.

### Model
- **Per-tenant, per-plan.** Three rate-limit middlewares — `rl-free`, `rl-pro`,
  `rl-enterprise` — with increasing `average` (sustained req/s) and `burst`.
- **Keyed off `X-Tenant-ID`** (`sourceCriterion.requestHeaderName`), so every
  tenant gets an **independent token bucket** — one tenant being throttled never
  affects another. We key on the tenant id (not the raw API key) because the
  platform stores only API-key *hashes*; `X-Tenant-ID` is non-secret and the
  management API already requires it, so per-plan routing needs no secret
  material. (`X-API-Key` keying remains available for the API-key data path.)
- **429 + `Retry-After` come from Traefik**, not the app.

### How a plan change propagates (< 30s, no restart)
`PUT /api/v1/tenants/{id}/plan` (owner-only) updates `subscription_plan`, then
the management API **regenerates the Traefik dynamic config**
(`tenants.yml`, one router per tenant matched on `X-Tenant-ID` → the plan's
middleware) and writes it **atomically** (temp + rename) into the file-provider
directory it shares with Traefik. Traefik's file watcher hot-reloads in ~1–2s —
no restart. Seeded from all tenants' plans on management-API startup. Writes are
**env-gated** on `TRAEFIK_DYNAMIC_DIR`, so deployments without a gateway are
unaffected.

- Compose: `traefik` fronts the API on host `:8090`; base config in
  `infrastructure/traefik/`; the shared dir is `infrastructure/traefik/dynamic/`.
- Kubernetes: `Middleware` CRDs in `infrastructure/kubernetes/traefik/`; the same
  generated `tenants.yml` is written to a shared file-provider volume.

### Observability
Traefik exposes Prometheus metrics (scrape job `traefik` →
`traefik_service_requests_total`, `traefik_service_request_duration_seconds`,
rate-limit drops), visible in Grafana alongside the app metrics (#84).

### Verify (compose)
```bash
docker compose up -d traefik management-api postgres-management nats prometheus
# Free tier is intentionally low for the demo (avg 5 / burst 10). Burst past it:
for i in $(seq 1 30); do
  curl -s -o /dev/null -w "%{http_code} " \
    -H "X-Tenant-ID: tenant-A" http://localhost:8090/api/v1/health
done            # expect a run of 200s then 429s
# A 429 response carries Retry-After and Server: traefik. A different
# X-Tenant-ID (tenant-B) is unaffected — independent buckets.
```
