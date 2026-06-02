# OAuth 2.0 Framework (#75)

VRSky's OAuth 2.0 framework lets connectors authenticate to providers that
require OAuth (Salesforce, Microsoft 365, Google, HubSpot, Shopify, …) instead
of static API keys. It implements the auth-code flow with PKCE, encrypted
token storage, automatic background refresh, and transparent refresh-on-401 at
the worker.

## Architecture

```
Browser ──(popup)──► provider authorize URL ──► /api/v1/oauth/callback
                                                      │ exchange code (PKCE)
                                                      ▼
                                           oauth_grants + secrets (encrypted)
                                                      ▲
   background refresher (1m tick) ─── refresh ────────┤
                                                      │
worker ──GET /oauth/grants/{id}/token (X-Service-Token)┘ ──► fresh access token
   │                                                         (refreshes if near expiry)
   └─► outbound API call; on 401 → ?refresh=1 retry once
```

- **`pkg/oauth`** — provider-agnostic client (StartAuth / Complete / Token /
  Refresh / Revoke). Concurrent refreshes for one grant are deduped via
  `singleflight`. No DB imports; persistence is the `Store` interface.
- **`pkg/managementapi/repo_oauth.go`** — Postgres `Store`; tokens are stored
  as `aes256:`-encrypted rows in the `secrets` table (#66), referenced by
  `oauth_grants.{access,refresh}_token_secret_id`.
- **`pkg/managementapi/oauth_refresher.go`** — background ticker that refreshes
  grants expiring within 5 minutes.
- **`pkg/oauthtoken`** — worker-side client for the token endpoint, with an
  in-process cache (until `expires_at − 30s`).

All refresh coordination lives in management-api (single `Client` +
singleflight + refresher); workers never refresh directly, so a rotating
refresh token can't be raced across the fleet.

## Configuring a provider (admin)

`POST /api/v1/oauth/providers` (admin role). The five shipped profiles seed
their auth/token URLs and default scopes — you supply `client_id`,
`client_secret`, and `redirect_url`:

```json
{ "name": "Acme MS365", "provider_type": "microsoft365",
  "client_id": "…", "client_secret": "…",
  "redirect_url": "https://app.example.com/api/v1/oauth/callback" }
```

Per-provider notes:

| Provider | Notes |
|---|---|
| `microsoft365` | Default scopes include `offline_access` (required for a refresh token). No programmatic revoke endpoint — revoke is local-only. |
| `salesforce` | Production by default. For a sandbox org set `extra_params.environment = "sandbox"` → swaps to `test.salesforce.com`. |
| `google` | Profile sets `access_type=offline` + `prompt=consent`, required for a refresh token. |
| `hubspot` | Standard flow. Refresh tokens expire 30 days after last use. No RFC-7009 revoke endpoint → local revoke only. |
| `shopify` | `auth_url`/`token_url` are templated with the store subdomain; the user enters the shop on the Connect screen. Access tokens are long-lived and are **not** refreshed. |
| `custom` | Supply your own `auth_url`, `token_url`, `scopes`. |

Use `provider_type: "custom"` for any standards-compliant provider not in the
list.

## Connecting + using a grant (end user)

In the pipeline builder, an API Consumer endpoint's auth dropdown has an
**OAuth 2.0** option. Pick a configured provider, click **Connect to …**, and
complete the popup. The resulting grant is selectable per endpoint; the worker
injects its access token as `Authorization: Bearer …` at request time.

Revoke from the same selector — it calls the provider's revoke endpoint (where
one exists) and marks the grant revoked locally. A grant whose refresh token
has expired shows **Reconnect required**.

## Worker integration

`auth_type=oauth` + `oauth_grant_id` on an endpoint config makes the worker
resolve a token via:

```
GET /api/v1/oauth/grants/{id}/token
    X-Service-Token: <OAUTH_TOKEN_SERVICE_SECRET>
    X-Tenant-ID: <tenant>
→ { "access_token": "...", "expires_at": "..." }
```

`?refresh=1` forces a refresh (the retry-once-on-401 path). Required env:

- management-api: `OAUTH_TOKEN_SERVICE_SECRET` (endpoint is disabled — 503 — if unset)
- worker: `MGMT_API_URL`, `OAUTH_TOKEN_SERVICE_SECRET`

> **http-producer** does not yet expose an auth selector in its node UI, so
> OAuth output delivery is a follow-up: the backend pieces (token endpoint +
> `pkg/oauthtoken`) are reusable; only the UI + a small wiring change in
> `cmd/http-producer` remain.

## Manual live smoke test (Microsoft 365)

Gated behind `OAUTH_LIVE=1` so CI never calls real providers:

1. Register an app in Entra ID; add redirect URI `http://localhost:5173/api/v1/oauth/callback` and the `offline_access` + Graph scopes.
2. Create the provider via the UI (or `POST /api/v1/oauth/providers`).
3. Connect through the popup; confirm a grant appears with an `expires_at`.
4. Wait past `expires_in` (≈1h) or force it; confirm the refresher renewed the
   token (`last_refreshed_at` advances, `refresh_failed_at` stays null).
5. Revoke from the UI; confirm `revoked_at` is set and the token endpoint then
   returns 403.

## Security notes

- Auth-code + PKCE only; no implicit flow.
- Access + refresh tokens and client secrets are AES-256-GCM encrypted at rest;
  never returned by any list/get endpoint.
- The token endpoint is service-secret gated and tenant-scoped (a grant is only
  returned for its owning tenant). It is not audited (hot path); grant + revoke
  are.
- `oauth_providers` / `oauth_grants` are tenant-scoped and enforced by
  `lint-tenant-filter`.
