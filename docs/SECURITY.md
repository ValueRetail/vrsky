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

A setting that is a longer string with a credential inside, like a header
value, can carry the secret inline as a placeholder instead:

```
"Authorization": "Bearer {secret:f0e0a3c2-…}"
```

Workers call `crypto.ResolveNodeSecrets` (or `ResolveBlockSecrets` for a
test or preview request) in `src/pkg/crypto/resolver.go` after loading a
connection from the management DB, and the value is decrypted in-process;
nothing leaves the worker boundary in cleartext form.

### Where a secret may be referenced

Only in a credential setting (#853). A connector checks its settings and
quotes them in errors (`auth_type "…" is not one of …`), and some it sends
on to the target as data (a header, a file name, an exchange). If any
setting could hold a secret, anyone who may edit a pipeline could put one
in such a setting and read any of the workspace's secrets back from Test
Connection, the activity feed, a connection's last error or the DLQ copy's
`X-Vrsky-Last-Error`. So, per connector type:

- `<field>_secret_id` is resolved only when `<field>` is in the table
  below. Anywhere else it is ignored, as if it were not there.
- A `{secret:<uuid>}` placeholder is expanded only where the table says
  "inline". Anywhere else, in a credential setting too, the node is
  refused: on save, on start, by Test Connection and schema discovery,
  and by the connector itself for a node stored earlier (a destination
  retries the message and then dead-letters it). The error names the
  setting, never a value.

Paths are relative to the node config; `*` is any header name or list
item. Keys match in any case, the way the connectors read them.

| Type | Credential settings | Inline `{secret:}` |
|---|---|---|
| `http` (webhook source, HTTP destination) | `http.headers.*` | yes |
| | `http.token`, `http.signature.secret`, `tls.client_ca`, `tls.cert`, `tls.key` | no |
| `api` | `api.endpoints.*.auth_value` | no |
| `database` | `database.password` | no |
| `sftp` | `sftp.password`, `sftp.private_key` | no |
| `kafka` | `kafka.password`, `kafka.client_cert`, `kafka.client_key` | no |
| `rabbitmq` | `rabbitmq.password` | no |
| `cloud_storage` | `cloud_storage.connection_string` | yes |
| | `cloud_storage.secret_access_key`, `cloud_storage.account_key`, `cloud_storage.credentials_json` | no |
| `sitoo` | `sitoo.api_password` | no |
| `front_systems` | `front_systems.subscription_key`, `front_systems.api_key` | no |
| `business_central` | `business_central.client_secret` | no |
| `visma` | `visma.client_secret` | no |
| `brightpearl` | `brightpearl.staff_token` | no |
| `sap_s4hana` | `sap_s4hana.password`, `sap_s4hana.client_secret` | no |
| `nats` | `nats.password`, `nats.creds`, `nats.nkey_seed`, `nats.ca_cert` | no |
| `bifrost_catalogue` | `bifrost_catalogue.token` | no |

Other types (file, remote agent, Salesforce, tenant) have no credential
settings. The list lives in `src/pkg/crypto/secretfields.go`; keep this
table in step with it.

What this does not stop: an editor can still point a credential setting at
a host they control (a header on an HTTP destination, a database host) and
receive any of the workspace's secrets there. Secrets belong to the
workspace, not to one connection.

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
   ```
   openssl rand -hex 32
   ```
2. Back up `secrets` before touching it, and keep `OLD_KEY` somewhere you can
   still reach — until step 5 it is the only thing that can read the table.
3. Preview the rewrap. It reports what it would change and writes nothing:
   ```
   ENCRYPTION_KEY_PREVIOUS=$OLD_KEY ENCRYPTION_KEY=$NEW_KEY \
     go run ./cmd/migrate-secrets --rewrap --dry-run
   ```
   A row that neither key can decrypt aborts the run with no changes written.
   Resolve that before continuing — it means material sealed under a third,
   unknown key.
4. Run it for real. Every row moves in one transaction, so the table is never
   half-rotated; re-running after an interruption skips what already moved:
   ```
   ENCRYPTION_KEY_PREVIOUS=$OLD_KEY ENCRYPTION_KEY=$NEW_KEY \
     go run ./cmd/migrate-secrets --rewrap
   ```
5. Roll out the new `ENCRYPTION_KEY` to management-api and every connector.
   Do this *after* the rewrap, not before: a service holding `NEW_KEY` cannot
   read rows still sealed with the old one, and services still holding
   `OLD_KEY` cannot read rows after it. Some decryption failure during the
   rollout window is expected.

Only `secrets.ciphertext` is rotated, and that is sufficient: every other
credential column in the schema (`oauth_grants.access_token_secret_id`,
`oauth_providers.client_secret_id`, `notification_targets.secret_id`, the
`<field>_secret_id` refs inside `connections.nodes`) stores a UUID pointing at
a row in `secrets`, not ciphertext of its own.

**What rotation does not cover:** `vrsky-cli backup` seals its archives with
the master key too. Archives taken before a rotation are readable only with
`OLD_KEY`, so retain it until those backups have aged out of your retention
window — deleting it strands them.

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
  A session the server rejects (unknown token, revoked, expired, or its user
  gone) answers `401`. If the lookup itself fails (the database is down or
  slow) the answer is `503 ServiceUnavailable` with a fixed message, and the
  error goes to the log, not to the client. The UI signs a user out on a
  `401` and keeps the session on a `503`, so a database blip does not end
  anyone's session.
- **Tenant API key** — opaque token bound to one tenant. Treated as
  effective role **admin**: enough to deploy / manage resources, but
  cannot transfer ownership or change tenant-wide settings.
  The same split applies: a key the server rejects (unknown, rotated away,
  revoked, or its workspace deleted) answers `401`. If the lookup itself
  fails (the database is down or slow) the answer is `503
  ServiceUnavailable` with a fixed message and the error goes to the log,
  never the key or its hash. A client should retry a `503` and fix its key
  on a `401`.

### Last-owner protection

Every tenant must keep at least one `owner`. The server refuses with 409
when a role change or member removal would leave zero owners. The UI
disables the relevant controls when the row is the sole owner.

### Audit

Every role change / member removal emits an audit_log row (#72). The
middleware auto-captures the request; `member.role_change` and
`member.remove` actions include `target_user_id` and (for role changes)
`new_role` in the details column.

Sign-in events go to a separate security log, the `auth_audit_log` table:
one row per event with the user (where known), email, event type, status,
a reason on failure, the client address and the user agent. Never a token,
a session id or a password, nor their hashes. Client-supplied text is made
to fit before the insert (email cut to 255 characters, user agent to 500,
NUL removed, invalid UTF-8 replaced), so an odd value cannot cost an event
its row. A row that still cannot be written (the database is down) logs
`auth audit row not written` with the event type and the error, and the
request goes on.

| Event type | Written by | Status |
|---|---|---|
| `registration` | sign-up | `success`, `failed` |
| `tenant_creation`, `auto_verify` | sign-up | `failed` |
| `login` | password login | `success`, `failed` |
| `oidc_login` | OIDC callback | `success`, `failed`, `denied` |
| `password_reset_requested` | forgot-password | `success`, `not_found`, `send_failed` |
| `password_reset` | reset-password (using the link) | `success`, `failed` |
| `password_changed` / `password_change` | change-password | `success` / `failed` |
| `email_verified` | the verification link | `success`, `failed` |
| `logout` | logout | `success`, `failed` |
| `account_deleted` | account deletion | `success` |
| `login`, `signup`, `forgot_password`, `change_password` | the failed-attempt limits | `blocked` (once, when a block starts) |

A failed `password_reset` or `email_verified` row says why (`invalid token`,
`token already used`, `token expired`) and names the user when the token
was a real one. Logout runs behind the session check, so it always names
the user: it ends the session the request authenticated with (the
`vrsky_session` cookie first, then the `Authorization` header), and an
unknown or expired session is a `401` with no row.

## Failed-attempt limits on the auth routes

`POST /api/v1/auth/login` and `POST /api/v1/auth/register` are the two
unauthenticated routes that do real work per request: a bcrypt check or hash
(cost 12) and a database round trip. Both are limited
(`pkg/managementapi/auth_limit.go`, plan in `plans/login-rate-limit.md`); the
remote-agent gateway limits `/agent/v1/register` the same way, with the same
bucket (`pkg/failurelimit`).

| Route | Keyed on | Allowance | Counts |
|---|---|---|---|
| login | client address | 10, then 1/min | failures only |
| login | submitted email | 10, then 1/5 min | failures only |
| sign-up | client address | 5, then 1/10 min | every request |

- **Failures only** means an attempt is spent before anything is looked up
  and handed back once the password proved right, so genuine use is never
  slowed and a burst of simultaneous guesses still stops at the allowance.
- **The account key is the submitted email, registered or not**, and the 429
  body is the same for both scopes, so the limit does not reveal who has an
  account.
- **A blocked account still admits an address that has logged in to it in
  the last 30 days** (read from `auth_audit_log`), so an attacker spending
  the account's attempts does not keep its owner out from their usual place.
- **The client address** is the first public hop reading `X-Forwarded-For`
  from the right, skipping our own proxies (private, loopback, link-local).
  A client-supplied entry only ever lands further left, so it cannot choose
  its key. Audit rows use the same address. Putting a proxy with a public
  address in front of ingress-nginx would make everyone one address;
  `TestClientAddr` documents the chains that are handled.
- Past the limit the answer is `429 RateLimited` with `Retry-After`; the
  block is logged and audited once when it starts, and refusals are counted
  in `vrsky_auth_limited_total{endpoint,scope}`.
- Knobs: `AUTH_LOGIN_MAX_FAILURES`, `AUTH_SIGNUP_MAX_ATTEMPTS` (0 = off).
  Counters are in memory per replica.

Forgot-password is limited per address (the sign-up numbers) and per
submitted email (3, then one per 10 minutes): it sends mail. Change-password
is limited per user like a login account (10 wrong current passwords, one
back per 5 minutes; a right one is handed back), so a stolen session cannot
grind the password out of it. Both answer 429 and count in the same metric
(`endpoint="forgot_password"` / `"change_password"`).

Not limited: OIDC sign-in, reset-password (a random single-use token),
API-key and session-token checks (random secrets; guessing is not a
realistic attack).

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

## Outbound URLs taken from tenant config (#616)

A connector that builds a request URL from a node's config and sends a
credential with it can be aimed at the cluster network or the cloud metadata
service. `pkg/safeurl` is the guard: `CheckBaseURL` gives an early error
(https only, no userinfo, query or fragment, no literal private IP), and
`NewClient` returns an HTTP client that refuses loopback, private, link-local,
CGNAT, multicast and the Azure platform address at dial time, on the address
actually connected to, so a hostname that resolves to one is refused and DNS
rebinding cannot swap it in. It ignores proxy environment variables and does
not follow redirects, unless the connector asks for same-host redirects
(`NewClientWith`): up to five, never to another host or from https to http.

What a tenant sees of a failed request is fixed text (#717). A transport
error's own text names the resolver (`lookup x on 10.0.0.10:53`), the resolved
address and the request URL with its query, so it must not reach the activity
feed, a sample-data response or a dead-lettered message's `X-Vrsky-Last-Error`
header. A refused dial carries only "connection to a private, loopback or
link-local address refused"; the address goes to the server log
(`safeurl.Detail`, which also cuts every request URL in the error down to
scheme, host and path, so a key in the query stays out of the log too).
`safeurl.UserMessage(err)` maps a transport error to one of "target address
not allowed", "host name could not be resolved", "request timed out",
"request cancelled", "connection refused", "connection failed", "TLS
certificate could not be verified" or "request failed". A refused redirect
keeps its own text: "redirect from https to http refused (URL)", "redirect
refused: url URL must use http or https" or "... points at a private,
loopback or link-local address", "redirect to another host refused (host to
host)" and "stopped after 5 redirects", where URL is scheme, host and path
only (userinfo as `xxxxx`, no query or fragment). The sample-data handlers of
the Sitoo, API, Visma, Brightpearl, SAP S/4HANA and Business Central
consumers use it, and each is tested against the same failures
(`pkg/safeurl/safeurltest`). `RedactURL` also shows nothing for a URL like `http://apikey:1234?secret@host`
(an "@" in the query with no path before it), since a password with "?" in it
parses that way; with a path (`/x?email=a@b`) the query is shown.

Used so far:

- The Sitoo consumer and producer ([Sitoo](connectors/sitoo.md#the-base-url)),
  https only; the management API also checks the Sitoo `base_url` when a
  connection is started.
- The API consumer (#639, [REST API polling](connectors/api.md#which-addresses-it-will-call)),
  which allows public `http` too because its URL is free-form and public APIs
  use it, follows same-host redirects, and is checked by the management API at
  start the same way.
- The Visma consumer and producer (#661, [Visma.net](connectors/visma.md#which-addresses-it-will-call)),
  https only for both `base_url` and `token_url`, no redirects, and checked by
  the management API at start the same way.

- The HTTP producer (#660, [HTTP & Webhooks](connectors/http.md#which-addresses-it-will-call)),
  on the same terms as the API consumer (public `http` allowed, same-host
  redirects). Its mTLS client is built on the same checked dialer
  (`Options.TLSConfig`). A refused delivery is retried and then dead-lettered,
  not dropped.
- The Brightpearl consumer and producer (#667,
  [Brightpearl](connectors/brightpearl.md#the-base-url)), https only; the
  derived URL's `datacenter` and `account_code` are checked too. The check
  runs in the connectors (consumer start, sample data, every delivery) and in
  the management API at start (#708).

- The Front Systems consumer and producer (#677,
  [Front Systems](connectors/front-systems.md#the-base-url)), https only: `base_url` is
  checked when the consumer registers webhooks and on every delivery, and
  every call goes through the checked client. The management API checks it at
  start too (#708), on the same terms. `callback_url` is not checked: Front
  Systems calls it, the connector never does.

- The SAP S/4HANA consumer and producer (#678,
  [SAP S/4HANA](connectors/sap-s4hana.md#which-addresses-it-will-call)),
  https only; `host` must be a bare host name with an optional port, and the
  OAuth2 token URL is checked too. Pagination links never take the
  credential off the configured origin. The check runs in the connectors
  (consumer start, sample data, every delivery) and in the management API at
  start (#708).

- The Business Central consumer and producer (#686,
  [Business Central](connectors/business-central.md#which-addresses-it-will-call)),
  https only; `api_base_url` and `token_url` (or their defaults) are checked,
  and `@odata.nextLink` never takes the token off the configured origin. The
  check runs in the connectors (consumer start, sample data, every delivery)
  and in the management API at start (#708).

Every connector that sends a credential to a tenant-supplied host is on it now
(#639). Where the management API checks at start, it calls the same function
as the connector (`pkg/vismaurl` for Visma, `pkg/connectorurl` for
Brightpearl, Front Systems, SAP S/4HANA and Business Central), so the two
cannot drift.

For local development with a mock upstream, the operator can set
`ALLOW_PRIVATE_UPSTREAMS=true` on the service to allow private addresses (and
`http` for the connectors that are https only), and on `management-api` for
the start check. `docker-compose.yml` sets it to true on `management-api` and
the connectors that have a mock (the dev stack only, #720); nothing under
`infrastructure/` sets it. A node cannot set it, and it must stay off in
production.

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
  generated `tenants.yml` is written to a shared file-provider volume. These
  manifests aren't wired up: no script applies them, and the Kubernetes
  management-api manifests don't set `TRAEFIK_DYNAMIC_DIR`. Production (AKS)
  runs ingress-nginx, not Traefik, so it has no per-plan gateway rate limit.
  The limits in the services themselves still apply there: the application
  quotas, the failed-attempt limits on login and sign-up (on by default,
  `src/pkg/managementapi/auth_limit.go`), the failed-attempt limit on
  remote-agent registration (`src/cmd/remote-agent/register.go`), and the
  per-connection rate limit on the tenant data endpoint
  (`ConnectionRateLimiter`, `src/pkg/managementapi/rate_limiter.go`).

### Observability
Traefik exposes Prometheus metrics (scrape job `traefik` →
`traefik_service_requests_total`, `traefik_service_request_duration_seconds`,
rate-limit drops), visible in Grafana alongside the app metrics (#84).

### Verify (hosted)

See [`docs/operator/webhook-mtls.md`](operator/webhook-mtls.md): the rollout,
curl checks with and without a certificate, a forged `ssl-client-cert`, and
the rollback.

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

## Mutual TLS for HTTP connectors (Phase 3F, #89)

High-security integrations (banking, public sector) require mutual TLS: the
inbound **webhook consumer** must reject callers that do not present a valid
client certificate, and the outbound **http producer** must present a client
certificate to mTLS-required endpoints. mTLS is opt-in per connection, keyed off
cert material stored as tenant secrets — no new secret plumbing (it reuses the
`*_secret_id` resolution from #66).

### Config surface

Add a `tls` block to the connector node's `config` (alongside `http`). Each
field is a reference to a secret holding PEM text; the worker resolves it to
plaintext at connection-start / send time (the resolver replaces a `*_secret_id`
key with the de-suffixed key, e.g. `cert_secret_id` → `cert`):

| Field                 | Consumer (webhook)                          | Producer (http)                                   |
|-----------------------|---------------------------------------------|---------------------------------------------------|
| `client_ca_secret_id` | **required** — CA that signs accepted client certs | optional — server CA to trust (empty → system roots) |
| `cert_secret_id`      | n/a (listener cert comes from env)          | **required** — client cert presented to the endpoint |
| `key_secret_id`       | n/a                                         | **required** — private key for `cert`             |

Example consumer node config (after the CA secret is stored):

```json
{ "type": "http", "tls": { "client_ca_secret_id": "<secret-uuid>" } }
```

The `tls` block is settable today via the connections API; UI cert pickers are a
follow-up. The webhook consumer's own server identity comes from
`WEBHOOK_TLS_CERT_FILE` / `WEBHOOK_TLS_KEY_FILE`; if unset it generates a
self-signed cert (dev only) — mTLS security here comes from verifying the
**client** cert, not from the client trusting the server cert.

### Behaviour

- **Consumer, hosted (edge, #454):** the client certificate is checked at
  ingress-nginx and again in the worker. The Ingress `vrsky-webhooks-mtls`
  (`infrastructure/kubernetes/ingress/webhooks-mtls-ingress.yaml`) serves its
  own host, because `auth-tls-*` applies per host and the UI host must not ask
  browsers for certificates. It sets `auth-tls-verify-client: "on"` against the
  CA bundle in the Secret `webhook-client-ca` (every tenant's webhook client
  CA) and `auth-tls-pass-certificate-to-upstream`, so nginx writes the
  verified leaf into `ssl-client-cert` and `SUCCESS` into `ssl-client-verify`,
  replacing any copy the caller sent. It proxies to the worker's edge port
  (`WORKER_MTLS_EDGE_PORT`, 9102) over TLS, presenting its own client
  certificate (`proxy-ssl-secret`, from a private CA cert-manager runs:
  `webhook-edge-tls.yaml`). That listener requires and verifies that
  certificate (`RequireAndVerifyClientCert`, client-auth EKU), so nothing else
  in the cluster can set the header; an optional NetworkPolicy also limits the
  port to ingress-nginx. The handler then requires exactly one
  `ssl-client-verify: SUCCESS` and exactly one `ssl-client-cert`, URL-decodes
  it, accepts exactly one PEM `CERTIFICATE` and nothing else, parses it and
  verifies it against the **connection's own** CA. `SUCCESS` alone is never
  enough: the bundle holds every tenant's CA. Only the subject and SHA-256
  fingerprint are logged. No other port reads the header, and no other
  Ingress may route to 9102: on a host without `auth-tls` nginx passes a
  caller's `ssl-client-cert` through. Guard tests in
  `src/pkg/managementapi/webhook_mtls_edge_guard_test.go` pin the annotations,
  the routes, the certificate usages and the deployment wiring.
- **Consumer, direct (compose):** when `WORKER_MTLS_PORT` is set (9101 in compose) the worker runs
  a dedicated TLS listener that demands *a* client cert at the handshake
  (`RequireAnyClientCert`) — a caller that presents none is refused at the TLS
  layer (no HTTP response). For a connection with `tls.client_ca`, the handler
  then verifies the presented cert chains to that per-connection CA (client-auth
  EKU only) and rejects with **401** if it does not. The same check also runs on
  the plain aux port (9100), where `r.TLS == nil` counts as "no cert" → **401** —
  so an mTLS connection cannot be reached unauthenticated over plain HTTP. This
  port reads the certificate from the TLS connection only, never from a
  header. Production does not set `WORKER_MTLS_PORT` (a guard test checks).
- **Producer:** when a node has a `tls` block with a resolved `cert`/`key`, the
  worker builds a dedicated `http.Client` that presents that client cert (and
  trusts `client_ca` as the server root if supplied), reused for every send.
- A connection with no `tls` block behaves exactly as before.
- Rejections increment `webhook_client_cert_failures_total{connection_id,reason}`
  (`reason` = `missing_cert` | `untrusted_cert` | `malformed_cert` |
  `untrusted_edge`; the last two only on the edge port).
- A tenant's certificate opens only connections whose CA it chains to. Two
  tenants who configure the same CA can reach each other's mTLS webhooks
  (#797 adds a per-connection pin).

### Verify (compose)

```bash
# 1. Throwaway CA + server + client certs.
openssl ecparam -genkey -name prime256v1 -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days 1 -subj "/CN=test-ca" -out ca.crt
openssl ecparam -genkey -name prime256v1 -out client.key
openssl req -new -key client.key -subj "/CN=client" -out client.csr
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 1 -out client.crt

# 2. Store ca.crt as a tenant secret and put its id in the consumer node's
#    tls.client_ca_secret_id, then start a webhook→http connection.

# 3. No client cert → TLS handshake refused (RequireAnyClientCert, no HTTP
#    response — curl exits 35/56); valid client cert → 202.
curl -k https://localhost:9101/webhook/<conn-id> -d '{"x":1}'                       # handshake refused
curl -k --cert client.crt --key client.key https://localhost:9101/webhook/<conn-id> -d '{"x":1}'  # 202
# A cert signed by a different CA completes the handshake but fails app-layer
# verification → 401. (macOS system curl uses LibreSSL, which mishandles EC
# client keys with "bad decrypt"; use an OpenSSL-based curl to test.)
```
