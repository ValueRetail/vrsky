# Microsoft Dynamics 365 Business Central

The Business Central connector (`business_central`) integrates with [Microsoft
Dynamics 365 Business Central](https://learn.microsoft.com/dynamics365/business-central/dev-itpro/api-reference/v2.0/)
— and, because **LS Central / LS Retail runs on Business Central**, it also
covers LS Retail deployments. It uses the OData v4 REST **API v2.0** (items,
customers, salesOrders, inventory, and ~55 other entities).

> **Authentication — OAuth 2.0 client-credentials (Microsoft Entra ID).**
> Unattended integration uses the `client_credentials` grant against Entra ID.
> Register an Entra app (with the `API.ReadWrite.All` Business Central
> permission), grant it access to the BC environment, and configure:
>
> - `aad_tenant_id` — your Entra tenant (GUID or domain).
> - `client_id` — the app registration id.
> - `client_secret_secret_id` — a secrets-vault reference to the client secret.
> - `company_id` — the BC company GUID (API v2.0 scopes entities by company).
> - `environment` — e.g. `Production` (default).
>
> The connector fetches and caches a bearer token for scope
> `https://api.businesscentral.dynamics.com/.default`.

## As a source (consumer)

A consumer node polls one OData entity on `poll_interval_seconds`, following
`@odata.nextLink` pagination, and emits each page as a JSON-array message.

- `entity` — the entity to poll (default `items`; e.g. `customers`, `salesOrders`).
- `filter` — optional OData `$filter` (e.g. `status eq 'Open'`).
- `poll_interval_seconds` — poll cadence.
- `incremental` — when `true`, remember the newest `cursor_field` value published
  and ask only for what changed since. Off by default: switching it on changes
  what a running pipeline delivers, so it is the connection owner's decision.
- `cursor_field` — the field the watermark is read from (default
  `lastModifiedDateTime`). A custom API page may name it something else.
- `page_size` — ask BC to page the response, sent as
  `Prefer: odata.maxpagesize`. Unset leaves BC on its own default, which
  returns most entities whole — 80 CRONUS items came back in a single page.
  Note that `$top` is not a substitute: it caps the result set rather than
  paging it, and produces no `@odata.nextLink`. BC may cap what you ask for
  and reports what it applied in `Preference-Applied`.

```json
{
  "type": "business_central",
  "business_central": {
    "aad_tenant_id": "<tenant-guid>",
    "environment": "Production",
    "company_id": "<company-guid>",
    "client_id": "<app-id>",
    "client_secret_secret_id": "<secret-uuid>",
    "entity": "salesOrders",
    "filter": "status eq 'Open'",
    "incremental": true,
    "poll_interval_seconds": 300
  }
}
```

## As a destination (producer)

A producer node writes each message to a BC entity (create with `POST`, update
with `PATCH` — updates send `If-Match: *`).

- `entity` — write target (default `items`; e.g. `salesOrders`).
- `method` — `POST` (default) or `PATCH`.

```json
{
  "type": "business_central",
  "business_central": {
    "aad_tenant_id": "<tenant-guid>",
    "company_id": "<company-guid>",
    "client_id": "<app-id>",
    "client_secret_secret_id": "<secret-uuid>",
    "entity": "items",
    "method": "POST"
  }
}
```

Delivery failures are classified for correct retry behaviour: `2xx` acks; `429`
and `503`/`5xx`/network errors retry; `4xx` bad-data and `401`/`403` auth are
poison and go to the DLQ.

## Notes & roadmap

- **On-prem / sandbox** — set `api_base_url`, `token_url`, and `scope` overrides
  for on-prem Business Central or a non-default Entra authority.
- **LS Central** — the standard BC entities work as-is; LS Retail-specific data
  (e.g. the LS eCommerce API for Commerce) can be reached by pointing `entity` /
  `api_base_url` at those endpoints.
- **Incremental polling** — set `incremental: true`. The watermark is stored per
  connection node in `connection_node_checkpoints` and survives restarts, so a
  connector that stops and starts does not re-read the entity from the
  beginning. It is composed with any configured `filter` rather than replacing
  it: `(your filter) and lastModifiedDateTime gt <watermark>`.

    The watermark only advances once **every** page of a fetch has been
    published. A fetch that fails halfway resumes from the previous watermark,
    so its records arrive twice rather than being skipped — the same
    at-least-once bargain the rest of the pipeline makes.

    The comparison is `gt`, so a record written in the same millisecond as the
    watermark but after the fetch read it is not picked up. `ge` would redeliver
    the watermark record on every poll instead.
- **UI source-type wiring** — config is API-settable today.

The same OAuth2 client-credentials pattern (via `pkg/oauthcc`) is reused by the
Visma.net connector.
