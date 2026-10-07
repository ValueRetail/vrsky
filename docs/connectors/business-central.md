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

### Pictures

Set `pictures: true` on a source node (**Also send pictures** in the editor)
to send the records' pictures along with them. The records go out exactly as
without it — every field, same request, so a converter after the node makes
the same CSV as before — and after each page, one message per picture of that
page's records, carrying the image file. Business Central has pictures on
`items`, `customers`, `vendors`, `employees` and `contacts`; any other entity
is refused when the pipeline starts.

Only a picture that is **new or changed** is downloaded and sent: the node
checks each record's picture id and skips one it has already sent. After the
connector restarts, the first poll sends each picture once more.

Each picture message has BC's content type (`image/jpeg`, `image/png` …),
the original image bytes, and this metadata:

| Key | Value |
|---|---|
| `filename` | `<number>.<ext>`, e.g. `1896-S.jpg`; the record id when there is no usable number |
| `number`, `display_name` | from the record, when it has them |
| `record_id`, `picture_id` | BC's ids |
| `width`, `height` | in pixels, from BC |
| `entity` | e.g. `items` |

Along the pipeline:

- **Converters and filters pass pictures through untouched** — every media
  file does (images, audio, video, PDF). A filter on the records does not
  filter their pictures.
- **The file, remote-agent and cloud-storage destinations write each picture
  under its `filename`**, even when a filename pattern or key template is
  set: the pattern names the records, the pictures keep their own names. So **BC items (pictures on) →
  converter (CSV) → Remote Agent** puts `catalogue-….csv` and `1896-S.jpg`,
  `1900-S.jpg` … side by side in the agent's folder.
- Pictures over 256 KB travel through the claim-check like any large payload.
- **Removed pictures.** When an item keeps existing but its picture is
  removed in BC, the next poll sends one **empty** file
  `<number>.no-picture` (e.g. `HBB-1000.no-picture`) next to the records —
  once, not on every poll — so a till can drop the thumbnail. An item that
  never had a picture produces nothing, and an item that leaves the feed gets
  no marker (the next full CSV covers it). The node remembers which items had
  a picture in its checkpoint, so a restart or redeploy does not re-send every
  picture either.
- **Incremental polls and pictures.** An incremental feed carries only items
  BC marked as modified, which a picture change may not do. After each poll
  the node therefore re-checks the pictures it remembers for items the poll
  did not carry (one small metadata request each; the image is only
  downloaded when it changed). That is fine for a till catalogue of a few
  hundred pictures; for a very large catalogue, prefer full polls.

Notes:

- **Picture changes on an unchanged item.** With `incremental`, the node only
  looks at records whose `lastModifiedDateTime` moved; replacing a picture in
  BC is expected to move it, but that is BC's behaviour, not something VRSky
  controls. Without `incremental`, every poll checks every record's picture id
  — one small request per record, no download unless it changed.
- **A failed picture download** fails that poll: the incremental watermark
  holds, so the next poll sends that page's records again and retries the
  picture.
- **Request cost.** The first poll of a large catalogue is about two extra
  requests per record (picture details, then the image); after that, one
  small request per record polled.

```json
{
  "type": "business_central",
  "business_central": {
    "aad_tenant_id": "<tenant-guid>",
    "company_id": "<company-guid>",
    "client_id": "<app-id>",
    "client_secret_secret_id": "<secret-uuid>",
    "entity": "items",
    "pictures": true,
    "poll_interval_seconds": 900
  }
}
```

## As a destination (producer)

A producer node writes each message to a BC entity (create with `POST`, update
with `PATCH` — updates send `If-Match: *`).

- `entity` — write target (default `items`; e.g. `salesOrders`).
- `method` — `POST` (default) or `PATCH`.
- `dedupe_fields` — optional, `POST` only: before writing, GET the entity
  filtered on these fields (payload field = BC property, equality) and skip
  the write when a record already matches. Guards against a message
  redelivered inside VRSky after BC accepted it. For sales from a till:
  `["externalDocumentNumber", "customerNumber"]`. `salesInvoices` is an
  aggregate whose `status` runs Draft → Open → Paid, so posted invoices are
  found by the same lookup. A field missing from a message means the check
  cannot run and the write proceeds, with a log line; if BC cannot be asked
  (5xx, network) the message waits rather than risk a duplicate.

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

For a till posting sales:

```json
{
  "type": "business_central",
  "business_central": {
    "…": "…",
    "entity": "salesInvoices",
    "method": "POST",
    "dedupe_fields": ["externalDocumentNumber", "customerNumber"]
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
