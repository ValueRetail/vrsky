# Front Systems

The Front Systems connector (`front_systems`) integrates with [Front
Systems](https://developer.frontsystems.com/) — a Nordic mobile/omnichannel POS
(part of EG). Front Systems is **webhook-first**: it pushes events to a
registered callback URL.

> **Credentials — two keys.** The Front Systems API sits behind Azure API
> Management and requires **two** headers:
>
> - `Ocp-Apim-Subscription-Key` — the APIM subscription key (developer-portal
>   signup).
> - `x-api-key` — a per-integration key (Front Systems Backoffice → Admin →
>   Integration users; shown once).
>
> Store both as secrets and reference them as `subscription_key_secret_id` and
> `api_key_secret_id`; the platform decrypts them at connection time.
>
> **Base URL** is the per-partner Azure APIM host you receive at onboarding — set
> it as `base_url` (there is no public default).

## As a source (consumer) — webhooks

Front Systems POSTs events to the connector's aux HTTP endpoint:

```
POST /frontsystems/events/{connectionID}
```

served on `WORKER_HTTP_PORT` (9270 in compose). Each event body is published as a
message; the handler returns `202` (Front Systems **requires** a 2xx or it treats
the event as undelivered and retries).

If `callback_url` + `events` are configured, the connector **auto-registers** its
callback for those event types on connection-start via `POST /api/webhooks`.
Otherwise register the webhook out of band.

- `base_url` — per-partner Azure APIM host (required).
- `subscription_key_secret_id` / `api_key_secret_id` — the two auth keys.
- `events` — event types to register, e.g. `SaleCreated`, `StockMovementCreated`,
  `POSSettlementCreated`, `DeliveryItemsReceived`, `ProductTransferCreated`,
  `StockReservationCreated`, `DeliveryCompleted`, `OmniChannelEndlessAisleOrderCreated`.
- `callback_url` — the publicly reachable URL Front Systems should call (your
  ingress → `/frontsystems/events/{connectionID}`).

```json
{
  "type": "front_systems",
  "front_systems": {
    "base_url": "https://<partner-apim-host>",
    "subscription_key_secret_id": "<secret-uuid>",
    "api_key_secret_id": "<secret-uuid>",
    "events": ["SaleCreated", "StockMovementCreated"],
    "callback_url": "https://<your-ingress>/frontsystems/events/<connectionID>"
  }
}
```

## As a destination (producer) — master data

A `front_systems` producer node writes master data (products, prices) into
Front Systems with the two auth headers.

- `resource` — the target path (default `/api/Products/bulk-upsert`; also
  `/api/Products`, `/api/PriceListV2`). Bulk payloads are capped at 2 MB.
- `method` — `POST` (default) or `PUT`.

```json
{
  "type": "front_systems",
  "front_systems": {
    "base_url": "https://<partner-apim-host>",
    "subscription_key_secret_id": "<secret-uuid>",
    "api_key_secret_id": "<secret-uuid>",
    "resource": "/api/Products/bulk-upsert"
  }
}
```

Delivery failures are classified for correct retry behaviour: `2xx` acks; `429`
and `5xx`/network errors retry (with the backoff hint); `4xx` and `401`/`403`
are poison and go to the DLQ.

## Notes & roadmap

- **Missed-event reconciliation** — Front Systems exposes
  `GET /api/WebhooksEvents/{webhookId}?success=false&from=&to=` plus a resend
  endpoint. A periodic reconcile poller that replays missed events is a
  straightforward enhancement.
- **Webhook signature verification** — no HMAC scheme is documented; confirm
  with Front Systems and add verification if one exists (today the callback
  relies on URL secrecy + the `x-api-key`).
- **UI source-type wiring** — config is API-settable today.
