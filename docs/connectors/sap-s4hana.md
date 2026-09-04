# SAP S/4HANA

The SAP connector (`sap_s4hana`) integrates with [SAP S/4HANA](https://api.sap.com/products/SAPS4HANACloud/apis/ODATA)
OData APIs — S/4HANA Cloud (public and private edition) and on-premise S/4HANA
via an exposed OData gateway. It speaks both **OData v2** (the majority of
`API_*` services, the default) and **OData v4**, and supports Basic
authentication with a communication user or OAuth 2.0 client credentials.

> **Authentication — two modes, selected with `auth_type`.**
>
> **`basic` (default)** — an S/4HANA Cloud *communication user*, created through
> a Communication Arrangement for the API you intend to use:
>
> - `username` — the communication user.
> - `password_secret_id` — a secrets-vault reference to its password.
>
> **`oauth2`** — client credentials against the SAP OAuth token endpoint:
>
> - `client_id`
> - `client_secret_secret_id`
> - `token_url` — optional; defaults to `https://{host}/sap/bc/sec/oauth2/token`.
> - `scope` — optional.
>
> `sap_client` (the mandt, e.g. `100`) is optional and, when set, is sent as the
> `sap-client` query parameter — normally needed only for on-premise systems.

## Addressing a service

A node targets one OData **entity set** inside one **service**:

- `host` — e.g. `my347623.s4hana.ondemand.com` (no scheme).
- `service` — the OData service, e.g. `API_SALES_ORDER_SRV`.
- `entity_set` — the entity set within it, e.g. `A_SalesOrder`.
- `odata_version` — `v2` (default) or `v4`.

For on-premise gateways or non-standard routes, set `api_base_url` to the full
service root instead of `host` + `service`; `entity_set` is appended to it.

## As a source (consumer)

A consumer node polls the entity set on `poll_interval_seconds`, follows the
version-appropriate pagination cursor (`d.__next` on v2, `@odata.nextLink` on
v4 — both opaque `$skiptoken` cursors), and emits each page as a JSON-array
message. The connector requests JSON explicitly, since OData v2 services return
Atom/XML by default.

- `filter` — optional OData `$filter`, e.g. `LastChangeDate gt datetime'2026-01-01T00:00:00'`.
- `poll_interval_seconds` — poll cadence.

```json
{
  "type": "sap_s4hana",
  "sap_s4hana": {
    "host": "my347623.s4hana.ondemand.com",
    "service": "API_SALES_ORDER_SRV",
    "entity_set": "A_SalesOrder",
    "odata_version": "v2",
    "auth_type": "basic",
    "username": "COMM_USER",
    "password_secret_id": "<secret-uuid>",
    "filter": "SalesOrderType eq 'OR'",
    "poll_interval_seconds": 300
  }
}
```

**Live schema preview** — the consumer serves `POST /sample-data/` on its
auxiliary HTTP port (9290 in dev compose), so the pipeline builder can show the
real record shape *before* the connection is deployed. See
[schema discovery](index.md#conventions).

## As a destination (producer)

A producer node writes each message to the entity set (create with `POST`,
update with `PATCH`).

- `method` — `POST` (default) or `PATCH`.

SAP's OData gateway requires **CSRF protection** on writes: the producer fetches
a token with `X-CSRF-Token: Fetch` and reuses it for subsequent writes. If SAP
answers `403` with `X-CSRF-Token: Required` — the token or session expired — the
producer re-fetches once and retries transparently.

```json
{
  "type": "sap_s4hana",
  "sap_s4hana": {
    "host": "my347623.s4hana.ondemand.com",
    "service": "API_SALES_ORDER_SRV",
    "entity_set": "A_SalesOrder",
    "auth_type": "oauth2",
    "client_id": "<client-id>",
    "client_secret_secret_id": "<secret-uuid>",
    "method": "POST"
  }
}
```

Delivery failures are classified for correct retry behaviour: `2xx` acks; `429`
and `503`/`5xx`/network errors retry with backoff; `4xx` bad-data and
`401`/`403` auth failures are poison and go to the DLQ.

## Validation

Validated end to end in the TEST (docker-compose) environment on 2026-09-04
against `mock-sap`, with a `sap_s4hana` source feeding a `sap_s4hana`
destination — both pointed at the mock's service root via `api_base_url`:

- **OData v2 pagination** — the consumer read page 1, followed the relative
  `d.__next` cursor, and read page 2 (3 sales orders total).
- **Basic auth** — `username` + `password_secret_id`, with the password
  resolved from the secrets vault at claim time.
- **CSRF handshake** — the producer fetched a token (`X-CSRF-Token: Fetch`)
  before each write and reused it.
- **Delivery** — both pages written back with `POST`, `201` on all four calls,
  no DLQ entries.

One thing worth knowing before configuring a node: with `auth_type: basic`, a
`username` alone is not enough. The connector refuses the connection with

```
SAP S/4HANA config incomplete: basic auth needs username + password (from password_secret_id)
```

which is a loud, logged failure rather than a silent one — but it happens at
claim time, not at save time, so check the consumer's logs if a SAP connection
starts and no polling appears.

To reproduce:

```bash
docker compose up -d nats postgres-management management-api mock-sap \
  sap-s4hana-consumer sap-s4hana-producer
```

then create a connection whose nodes set
`api_base_url: http://mock-sap:8099/sap/opu/odata/sap/API_SALES_ORDER_SRV` and
`entity_set: A_SalesOrder`, and watch `docker logs vrsky-mock-sap`.

Both services are deployed to prod as of this validation
(`infrastructure/azure/deploy-connectors-azure.sh`).

## Notes & roadmap

- **Which OData version?** Most `API_*` S/4HANA Cloud services are v2 — keep the
  default unless the SAP API Business Hub page for your service says v4.
- **Communication Arrangement** — in S/4HANA Cloud, each API needs an arrangement
  (with a communication system, user, and the relevant scenario, e.g.
  `SAP_COM_0109` for sales orders) before it will answer.
- **Incremental polling** — use `filter` on the entity's change-date field for
  high-volume entity sets. Cursor persistence across restarts is a follow-up;
  today each poll cycle restarts from the filter.
- **Deep inserts** — writing an order with nested items in one call works if the
  payload matches the service's deep-insert shape; the connector sends the
  message body through unchanged.
- **On-premise** — set `api_base_url` to the gateway service root and, usually,
  `sap_client`.
