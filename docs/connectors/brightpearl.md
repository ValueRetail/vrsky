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
> Both are checked before anything is called; see [The base URL](#the-base-url).

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
and `503`/`5xx`/network errors retry, and after the last attempt the message
goes to the DLQ. `4xx` (including `401`/`403`), an incomplete node config and
a payload that is not JSON are permanent: the message is logged and dropped,
not dead-lettered. A base URL
the connector refuses (below) is retried like a network error, so those
messages end in the DLQ and can be retried from there once the node is fixed.

## The base URL

Every request carries the node's staff token, and the URL it goes to comes
from the node, so the connector only calls addresses it can trust (#667):

- A `base_url` override must be `https`, with a host, and no user name, query
  string or fragment.
- Without an override, `datacenter` must be a single host label (`eu1`,
  `use1`) and `account_code` may only hold letters, digits, `-` and `_`, so
  neither can move the request off `ws-{datacenter}.brightpearl.com`.
- The connector will not connect to loopback, private (10/8, 172.16/12,
  192.168/16, fc00::/7), link-local (169.254/16, which includes the cloud
  metadata address 169.254.169.254, and fe80::/10), carrier-grade NAT
  (100.64/10), unspecified or multicast addresses, or to the Azure platform
  address 168.63.129.16. This is checked on the address it actually connects
  to, after the name is resolved, so a hostname that points at one of these is
  refused too. Redirects are not followed.

Where it is checked: the management API refuses to start a connection that
breaks these rules, with the field and the reason in the answer (#708). The
connectors check again: a source refuses to start its poll (the error is in
the consumer's log), the "show data structure" preview answers with the
reason, and a destination retries the message until it ends in the DLQ. A
source is checked only when it polls (`poll_interval_seconds` above 0 and a
`resource`): in webhook mode it calls nothing, so these fields are not checked
and may be left empty.

A `base_url` can still point at any public https host, so only set one for a
host you trust with the staff token.

**Local development with a mock Brightpearl.** The operator (not the node) can
set `ALLOW_PRIVATE_UPSTREAMS=true` on `brightpearl-consumer`,
`brightpearl-producer` and `management-api`. That allows `http` and private addresses for
`base_url`. Never set it in production; it is off in `docker-compose.yml` and
the Kubernetes manifests.

**Existing connections.** A node whose `base_url` is `http://` or points at a
private address, or whose `datacenter` or `account_code` breaks the rules
above, stops working when this change is deployed: a running source keeps
polling until its next restart, then refuses to start; a destination refuses
from the next delivery, and those messages end in the DLQ. Fix the field, or
clear `base_url` to use the default, then retry them from the DLQ.

## Notes & roadmap

- **Per-endpoint paging** — Brightpearl search endpoints return a `metaData`
  block (`firstResult`/`lastResult`/`resultsAvailable`); drive paging via `query`
  today; automatic multi-page fetch is a follow-up.
- **OAuth (public apps)** — public/multi-account apps use OAuth2 Authorization
  Code instead of a staff token; that path can reuse the platform OAuth grants.
- **Webhook signing** — add signature verification if enabled for the account.
- **UI source-type wiring** — config is API-settable today.
