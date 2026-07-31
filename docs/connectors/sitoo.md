# Sitoo

The Sitoo connector (`sitoo`) ingests data from the [Sitoo Retail
Platform](https://developer.sitoo.com/) — a cloud POS / unified-commerce system
— into a VRSky pipeline. It supports two ingestion modes, usable together:

- **Poll** — periodically fetch a Sitoo collection (orders/transactions,
  warehouse stock, products, …) over the REST API with `start`/`num` pagination.
- **Webhook** — receive Sitoo **SPI Event** notifications (Orders, Warehouse
  Transactions, …) in real time.

> **Credentials.** Sitoo authenticates with HTTP Basic auth using an **API ID +
> password** generated in the Sitoo Backoffice (Settings → Sitoo REST API,
> requires the API extension + Administrator rights). Store the password as a
> secret and reference it from the node as `api_password_secret_id`; the
> platform decrypts it at connection-start time. Never put the raw password on
> the node.

## As a source (consumer)

### Poll mode

A consumer node fetches the configured `resource` on `poll_interval_seconds`,
paging through all results, and emits each page as a JSON-array message.

- `account_id` / `site_id` — your Sitoo account and site (integers).
- `api_id` — the Sitoo REST API ID.
- `api_password_secret_id` — secret reference to the API password.
- `resource` — the collection to poll (default `transactions`; e.g.
  `warehouseitems`, `products`).
- `poll_interval_seconds` — poll cadence. Set `0` (or omit) for **webhook-only**.
- `page_size` — Sitoo `num` (default `1000`; 1000–5000 is optimal).
- `base_url` — optional override (default `https://api.mysitoo.com/v2`).

```json
{
  "type": "sitoo",
  "sitoo": {
    "account_id": 12345,
    "site_id": 1,
    "api_id": "your-api-id",
    "api_password_secret_id": "<secret-uuid>",
    "resource": "transactions",
    "poll_interval_seconds": 300,
    "page_size": 1000
  }
}
```

The connector honours Sitoo's rate limit: on HTTP `429` it waits for
`X-Rate-Limit-Reset` / `Retry-After` and retries.

### Webhook mode (real-time SPI Events)

Point Sitoo's SPI Events at the connector's auxiliary HTTP endpoint:

```
POST /sitoo/events/{connectionID}
```

served on `WORKER_HTTP_PORT` (9260 in compose). Each event body is published as a
message routed to the owning tenant/connection. Configure the connection with
`poll_interval_seconds: 0` if you want webhook-only (no polling).

## As a destination (producer)

A `sitoo` producer node writes each incoming message into a Sitoo REST resource
(e.g. updating warehouse stock, prices, or products) with HTTP Basic auth —
completing two-way sync. The message payload is sent as the request body, so an
upstream converter/filter should shape it to the target resource's schema.

- `account_id` / `site_id` / `api_id` / `api_password_secret_id` — as above.
- `resource` — the target collection (default `warehouseitems`; e.g. `prices`).
- `method` — `POST` (default) or `PUT`.
- `base_url` — optional override.

```json
{
  "type": "sitoo",
  "sitoo": {
    "account_id": 12345,
    "site_id": 1,
    "api_id": "your-api-id",
    "api_password_secret_id": "<secret-uuid>",
    "resource": "warehouseitems",
    "method": "POST"
  }
}
```

Delivery failures are classified for correct retry behaviour: `2xx` acks; `429`
and `5xx`/network errors are retried (with the rate-limit backoff hint); `4xx`
(bad request/data) and `401`/`403` (auth) are treated as poison and sent to the
DLQ rather than retried forever.

## Notes & roadmap

- **Incremental polling** — the current poller does a full paged fetch each
  cycle; a `modified_after`-style cursor is a straightforward enhancement for
  high-volume order streams.
- **Webhook signature verification** — add HMAC verification once the SPI Event
  signing scheme is confirmed for your account.
