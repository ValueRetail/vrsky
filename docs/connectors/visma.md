# Visma.net

The Visma connector (`visma`) integrates with [Visma.net](https://docs.vismasoftware.no/vismanetapi/)
— a Nordic cloud ERP. Visma.net is **multi-service**: each API has its own host
(e.g. `https://salesorder.visma.net`, the Financials API host, …), so `base_url`
is set per connection. Auth is OAuth 2.0 **client-credentials** via Visma
Connect.

> **Authentication — OAuth 2.0 client-credentials (Visma Connect).** Register an
> application at the [Visma Developer Portal](https://oauth.developers.visma.com)
> and configure:
>
> - `client_id` — the application id.
> - `client_secret_secret_id` — a secrets-vault reference to the client secret.
> - `scope` — the per-service scope (required; from the developer portal).
> - `token_url` — defaults to `https://connect.visma.com/connect/token`.
>
> The connector fetches and caches a bearer token (shared `pkg/oauthcc`). The
> **company context** is sent as the `ipp-company-id` header when `company_id`
> is set (required by the Financials API).

## Which addresses it will call

`base_url` and `token_url` are your configuration, and the connector sends the
client secret to `token_url` and the bearer token to `base_url`. So both must
be `https` URLs without a user name, password, query string or fragment, and
neither may point at a private, loopback, link-local or cloud-metadata
address (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `127.0.0.0/8`,
`169.254.0.0/16`, `168.63.129.16`, and the IPv6 equivalents).

- The management API refuses such a URL when the connection is started, with
  the reason in the error.
- The connector checks again at start. A consumer that is refused does not
  start and logs why; a producer sends the message to the DLQ with the reason
  instead of retrying.
- Every request (token and API) is also checked when the connection is made,
  on the address actually connected to, so a hostname that resolves to a
  private address is refused too.
- Redirects are not followed.
- The real Visma hosts and the default `token_url` are unaffected.

If you have a connection with an `http` or private `base_url` or `token_url`,
it stops working after this change and needs a real Visma URL. For local
development with a mock upstream the operator can set
`ALLOW_PRIVATE_UPSTREAMS=true` on the service; a node cannot set it, and it
must stay off in production.

## As a source (consumer)

A consumer node polls a configured `resource` under `base_url` on
`poll_interval_seconds` and emits the response as a message.

- `base_url` — the per-service host + version path, e.g.
  `https://salesorder.visma.net/api/v3` (required).
- `resource` — the resource under `base_url`, e.g. `SalesOrders`, `customer`.
- `query` — optional query string for service-specific paging/filtering.
- `company_id` — sent as `ipp-company-id`.
- `poll_interval_seconds` — poll cadence.

```json
{
  "type": "visma",
  "visma": {
    "base_url": "https://salesorder.visma.net/api/v3",
    "scope": "<service-scope>",
    "client_id": "<app-id>",
    "client_secret_secret_id": "<secret-uuid>",
    "company_id": "<company-id>",
    "resource": "SalesOrders",
    "poll_interval_seconds": 300
  }
}
```

A JSON-array response is published as-is; a single-object response is wrapped in
a one-element array so downstream steps always see a list.

## As a destination (producer)

A producer node writes each message to a Visma resource (`POST`/`PUT`/`PATCH`).

- `base_url` / `scope` / `client_id` / `client_secret_secret_id` — as above.
- `resource` — the write target, e.g. `SalesOrders`.
- `method` — `POST` (default), `PUT`, or `PATCH`.

```json
{
  "type": "visma",
  "visma": {
    "base_url": "https://salesorder.visma.net/api/v3",
    "scope": "<service-scope>",
    "client_id": "<app-id>",
    "client_secret_secret_id": "<secret-uuid>",
    "company_id": "<company-id>",
    "resource": "SalesOrders",
    "method": "POST"
  }
}
```

Delivery failures are classified for correct retry behaviour: `2xx` acks; `429`
and `503`/`5xx`/network errors retry; `4xx` and `401`/`403` are poison and go to
the DLQ.

## Notes & roadmap

- **Per-service paging** — Visma's paging differs across services (Financials
  uses `pageNumber`/`numberToRead`; newer services differ). Use `query` to drive
  it; automatic multi-page fetch per service is a follow-up.
- **Business NXT** — Visma also offers the Business NXT API (same Visma Connect
  OAuth); point `base_url`/`scope` at it to reuse this connector.
- **UI source-type wiring** — config is API-settable today.

Uses the same OAuth2 client-credentials token source (`pkg/oauthcc`) as the
Business Central connector.
