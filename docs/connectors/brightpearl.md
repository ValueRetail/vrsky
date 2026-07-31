# Brightpearl (OMS)

The Brightpearl connector (`brightpearl`) integrates with
[Brightpearl](https://api-docs.brightpearl.com/) — a retail order-management /
retail-ops platform (orders, inventory & warehouses, products, contacts). It
supports polling and webhooks.

> **Credentials — staff app token.** A private/staff app authenticates with two
> headers:
>
> - `brightpearl-app-ref` — your app reference.
> - `brightpearl-staff-token` — the staff auth token (revocable per staff
>   member).
>
> Store the token as a secret and reference it as `staff_token_secret_id`.
>
> **Base URL** is account- and datacenter-scoped:
> `https://ws-{datacenter}.brightpearl.com/public-api/{account_code}`. Set
> `datacenter` (e.g. `eu1`, `use1`) + `account_code`, or override `base_url`.

## As a source (consumer)

### Poll mode

A consumer node GETs a configured `resource` on `poll_interval_seconds` and
publishes the response. Brightpearl wraps most bodies in `{"response": …}`; the
connector unwraps and publishes the inner `response` value.

- `datacenter` / `account_code` — for the base URL (or set `base_url`).
- `app_ref` / `staff_token_secret_id` — the two auth values.
- `resource` — e.g. `/order-service/order-search`, `/warehouse-service/…`.
- `query` — optional query string (Brightpearl search paging/filters).
- `poll_interval_seconds` — poll cadence (set `0`/omit + no `resource` for
  webhook-only).

```json
{
  "type": "brightpearl",
  "brightpearl": {
    "datacenter": "eu1",
    "account_code": "youraccount",
    "app_ref": "yourapp",
    "staff_token_secret_id": "<secret-uuid>",
    "resource": "/order-service/order-search",
    "query": "orderTypeId=1&pageSize=100",
    "poll_interval_seconds": 300
  }
}
```

### Webhook mode

Point a Brightpearl webhook at the connector's aux HTTP endpoint:

```
POST /brightpearl/events/{connectionID}
```

served on `WORKER_HTTP_PORT` (9280 in compose). Each webhook body is published as
a message routed to the owning tenant/connection.

## As a destination (producer)

A producer node writes each message to a Brightpearl resource (`POST`/`PUT`/`PATCH`).

- `resource` — the write target, e.g. `/order-service/order`.
- `method` — `POST` (default), `PUT`, or `PATCH`.

```json
{
  "type": "brightpearl",
  "brightpearl": {
    "datacenter": "eu1",
    "account_code": "youraccount",
    "app_ref": "yourapp",
    "staff_token_secret_id": "<secret-uuid>",
    "resource": "/order-service/order",
    "method": "POST"
  }
}
```

Delivery failures are classified for correct retry behaviour: `2xx` acks; `429`
and `503`/`5xx`/network errors retry; `4xx` and `401`/`403` are poison → DLQ.

## Notes & roadmap

- **Per-endpoint paging** — Brightpearl search endpoints return a `metaData`
  block (`firstResult`/`lastResult`/`resultsAvailable`); drive paging via `query`
  today; automatic multi-page fetch is a follow-up.
- **OAuth (public apps)** — public/multi-account apps use OAuth2 Authorization
  Code instead of a staff token; that path can reuse the platform OAuth grants.
- **Webhook signing** — add signature verification if enabled for the account.
- **UI source-type wiring** — config is API-settable today.
