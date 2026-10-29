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
message and the handler returns `202`. Front Systems counts only a 2xx as
delivered and does not resend on its own: a failed event stays in its event log
(`GET /api/WebhooksEvents/{webhookId}?success=false`) until someone resends it.

Events are published only while the connection is running. Otherwise nothing is
published and the handler answers:

| Status | When |
|---|---|
| `404` | No connection with that id. |
| `410` | The connection exists but is stopped, or its start failed for good (status `error`). |
| `503` | The connection is starting, or its start failed for a reason that passes and is waiting to retry; or the connector is starting up and hasn't restored its running connections yet, or is shutting down. |

A start that fails for a reason that passes is retried: the database not
answering, or Front Systems answering 5xx, 408 or 429, or not answering at
all. The first retry comes after about 5 seconds, then the wait doubles up to
5 minutes, with some jitter. Retries stop when the pipeline is stopped or
deleted, when its status is no longer `running`, or after 6 hours, when the
status becomes `error`. A start that fails in a way no retry fixes (Front
Systems refusing the registration with another 4xx such as 401 or 403, a
missing `base_url` or one without `https://`, Front Systems refusing the keys
on the first webhook list with 401 or 403, a secret that can't be read) is not retried: the
pipeline's status becomes `error` and its last error names the cause. Fix it
and start the pipeline again.

Events refused with `503` are not replayed once the pipeline is running; they
stay in Front Systems' failed-events log until someone resends them.

The route has no authentication of its own (Front Systems documents no
signature): the connection id in the URL is the secret. The event is published
for the tenant that owns the connection, and only if that is the tenant the
connection was started for.

If `callback_url` + `events` are configured, the connector **auto-registers** its
callback for those event types on connection-start via `POST /api/webhooks`.
It lists the existing webhooks first (`GET /api/webhooks`) and reuses any with
the same event and URL, so restarts don't register the callback twice. On
connection-stop it deletes them again (`DELETE /api/webhooks/{id}`). That is best
effort: a failed delete is logged and the pipeline stops anyway, and the next
start reuses the leftover registration. A restart of the connector itself (deploy,
cluster start) keeps the registrations. If `callback_url` is not set, register
the webhook out of band; the connector then neither registers nor deletes
anything, but still refuses events while the connection is stopped.

- `base_url` — per-partner Azure APIM host as a URL, `https://<partner-apim-host>`
  (required). A bare host without `https://` is refused at start and the
  pipeline goes to `error`.
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
  endpoint. Replaying the events refused with `503` above after a start is
  backlog item 136; it needs the event id as an idempotency key so an event
  whose `202` got lost isn't published twice.
- **Webhook signature verification** — no HMAC scheme is documented; confirm
  with Front Systems and add verification if one exists (today the callback
  relies on URL secrecy + the `x-api-key`).
- **UI source-type wiring** — config is API-settable today.
