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

- `host` — e.g. `my347623.s4hana.ondemand.com`: a host name, optionally with
  `:port`, and nothing else (no scheme, path or user name).
- `service` — the OData service, e.g. `API_SALES_ORDER_SRV`.
- `entity_set` — the entity set within it, e.g. `A_SalesOrder`.
- `odata_version` — `v2` (default) or `v4`.

For on-premise gateways or non-standard routes, set `api_base_url` to the full
service root instead of `host` + `service`; `entity_set` is appended to it.
Both are checked before anything is called; see
[Which addresses it will call](#which-addresses-it-will-call).

## As a source (consumer)

A consumer node polls the entity set on `poll_interval_seconds`, follows the
version-appropriate pagination cursor (`d.__next` on v2, `@odata.nextLink` on
v4 — both opaque `$skiptoken` cursors), and emits each page as a JSON-array
message. The connector requests JSON explicitly, since OData v2 services return
Atom/XML by default.

A poll follows at most 10 000 pages. If the service is still returning next
links after that, the poll stops with a warning that names the connection and
the cap. The pages already read stay published; the records past the cap
are not read. Use `filter` to narrow the read.

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
auxiliary HTTP port (9290 in dev compose). The builder reaches it through
`POST /api/v1/schema-discovery/sap_s4hana` rather than calling the port
directly, so the request is authenticated and the workspace comes from the
session. The endpoint exists so the pipeline builder can show the
real record shape *before* the connection is deployed. See
[schema discovery](index.md#conventions).

## As a destination (producer)

A producer node writes each message to the entity set (create with `POST`,
update with `PATCH`).

- `method` — `POST` (default) or `PATCH`.

SAP's OData gateway requires **CSRF protection** on writes: before each write
the producer fetches a token with `X-CSRF-Token: Fetch`, then sends the write
with that token. If SAP answers `403` with `X-CSRF-Token: Required` — the token
or session expired — the producer re-fetches once and retries transparently.

Each delivery runs in an SAP session of its own: the session cookies SAP sets
on the fetch (`SAP_SESSIONID_*`, `MYSAPSSO2`) are kept only for that
delivery's fetch, write and OAuth2 token call, then dropped. Two connections,
or two tenants, writing to the same SAP host never send each other's session
(#682).

When a delivery ends, success or failure, the producer logs that session off
with `GET /sap/public/bc/icf/logoff` on the service's own host (with
`sap-client` when set), sending only the session cookie. This keeps sessions
from piling up per technical user until SAP's idle timeout (#736). It is
best-effort: it waits at most 5 seconds, it is skipped when SAP set no
`SAP_SESSIONID_*` cookie, and an error or a `404` (a gateway without the ICF
logoff service) is logged at debug level and never changes the delivery's
result. There is no setting for it.

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
and `503`/`5xx`/network errors retry with backoff and go to the DLQ once the
retries are used up. Other `4xx` answers to the write, `401`/`403` auth
failures included, are permanent: the message is acked and dropped (the error
is logged), not retried and not dead-lettered. So is an incomplete node
config or a payload that is not JSON. The token fetch comes first, though:
if SAP answers it with anything but `2xx` (a rejected login shows up
here), the message is retried and dead-lettered like a network error.
An address the connector refuses (below) is
retried like a network error and then goes to the DLQ with its payload, so it
can be replayed once the node is fixed. Its error says the address was refused
but not which address a name resolved to.

## Which addresses it will call

Every request carries the node's SAP credential (the communication user's
password, or the OAuth2 client secret on the token call and the bearer token
after it), and the URLs come from the node, so the connector only calls
addresses it can trust (#678):

- `host` must be one host name with an optional numeric port, so it cannot
  carry a scheme, a path, a user name or a query that changes where the
  request goes. It is checked wherever it is used: when there is no
  `api_base_url`, and for OAuth2 when there is no `token_url`.
- The service root (`api_base_url`, or the one built from `host`) and the
  OAuth2 token URL (`token_url`, or `https://{host}/sap/bc/sec/oauth2/token`)
  must be `https`, with a host, and no user name, query string or fragment.
- The connector will not connect to loopback, private (10/8, 172.16/12,
  192.168/16, fc00::/7), link-local (169.254/16, which includes the cloud
  metadata address 169.254.169.254, and fe80::/10), carrier-grade NAT
  (100.64/10), unspecified or multicast addresses, or to the Azure platform
  address 168.63.129.16. This is checked on the address it actually connects
  to, after the name is resolved, so a hostname that points at one of these is
  refused too. It applies to every call: the OData requests, the pagination
  links SAP returns, the CSRF fetch and the token call. Redirects are not
  followed.
- Pagination links (`d.__next`, `@odata.nextLink`) stay on the service
  root's scheme, host and port. A link to another host or port, or an `http`
  link where the root is `https` (a TLS-terminating web dispatcher may
  answer with one), is fetched from the configured root instead, with the
  link's path and query, as long as that path, with `.` and `..` resolved, is
  under the service root's path and holds no encoded dot or slash (`%2e`,
  `%2f`) and no backslash (`\` or `%5c`). A link anywhere else stops the
  poll with an error; the pages before it are already published. Relative
  links, which is what SAP and `mock-sap` send, are not affected.

Where it is checked: the management API refuses to start a connection that
breaks these rules, with the field and the reason in the answer (#708). The
connectors check again: a source refuses to start its poll (the error is in
the consumer's log), the "show data structure" preview answers with the
reason, and a destination retries the message and then sends it to the DLQ.

An `api_base_url` or `token_url` can still point at any public https host, so
only set one for a host you trust with the credential.

**Local development with mock-sap.** The operator (not the node) can set
`ALLOW_PRIVATE_UPSTREAMS=true` on `sap-s4hana-consumer` and
`sap-s4hana-producer`. That allows `http` and private addresses, which is what
`http://mock-sap:8099` needs. `docker-compose.yml` sets it on both services
(override with `ALLOW_PRIVATE_UPSTREAMS=false`), so the dev stack and the
nightly end-to-end keep working. To start a mock-sap connection through the
management API (the UI's Start), set it on `management-api` too; the compose
file does not. The Kubernetes manifests do not set it; never set it in
production. It does not loosen the `host` shape check.

**Existing connections.** A node whose `api_base_url` or `token_url` is
`http://` or points at a private address, or whose `host` carries more than a
host name and port, stops working when this change is deployed: a running
source keeps polling until its next restart, then refuses to start; a
destination refuses the next delivery. Fix the field. An on-premise gateway
has to be reachable on a public https address.

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
`entity_set: A_SalesOrder`, and watch `docker logs vrsky-mock-sap`. This needs
`ALLOW_PRIVATE_UPSTREAMS=true` on both SAP services, which `docker-compose.yml`
sets by default.

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
