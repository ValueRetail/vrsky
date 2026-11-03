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

## Which addresses it will call

`api_base_url` and `token_url` are your configuration, and the connector sends
the client secret to `token_url` and the bearer token to every API request.
Left unset they default to
`https://api.businesscentral.dynamics.com/v2.0/<aad_tenant_id>/<environment>/api/v2.0`
and `https://login.microsoftonline.com/<aad_tenant_id>/oauth2/v2.0/token`,
which keep working. Set or not, both must be `https` URLs without a user name,
password, query string or fragment, and neither may point at a private,
loopback, link-local or cloud-metadata address (`10.0.0.0/8`,
`172.16.0.0/12`, `192.168.0.0/16`, `127.0.0.0/8`, `169.254.0.0/16`,
`168.63.129.16`, and the IPv6 equivalents).

- A source that is refused does not start and logs why. The data preview
  ("show data structure") answers with the reason.
- A destination sends nothing. The message is retried and then dead-lettered
  with its payload, so it can be replayed once the URL is fixed.
- Every request (token, API, the next page, pictures, the destination's
  `dedupe_fields` lookup) is also checked when the connection is made, on the
  address actually connected to, so a host name that resolves to a private
  address is refused too. The preview and the dead-letter error then say
  only that the address is not allowed; the service log has which one.
- Redirects are not followed.
- `@odata.nextLink` never takes the token to another origin. A link on the
  same scheme, host and port is followed. A link to another host or port, or
  to `http`, whose path is under the API root is fetched from the configured
  host instead, as picture links already are (on-prem servers often name
  themselves by an internal host). Any other link stops the poll with an
  error; the watermark stays where it was.

These checks run in the connector, not yet when the management API starts
the connection, so the reason shows up in the service log rather than in the
start answer.

If you have a node with an `http` or private `api_base_url` or `token_url`, it
stops working after this change and needs a reachable https URL. For local
development with a mock upstream the operator can set
`ALLOW_PRIVATE_UPSTREAMS=true` on the service; a node cannot set it, and it
must stay off in production.

## As a source (consumer)

A consumer node polls one OData entity on `poll_interval_seconds`, following
`@odata.nextLink` pagination, and emits each page as a JSON-array message.

A poll follows at most 10 000 pages. If the API is still returning
`@odata.nextLink` after that, the poll stops with a warning that names the
connection and the cap. The pages already read stay published, but the
watermark is not advanced. An incremental poll that hits the cap therefore
repeats the same pages on every poll and never gets past them, so the records
beyond them are not delivered, until `page_size` is raised or `filter` is
narrowed.

- `entity` — the entity to poll (default `items`; e.g. `customers`, `salesOrders`).
- `filter` — optional OData `$filter` (e.g. `status eq 'Open'`).
- `poll_interval_seconds` — poll cadence.
- `incremental` — when `true`, remember the newest `cursor_field` value published
  and ask only for what changed since. Off by default: switching it on changes
  what a running pipeline delivers, so it is the connection owner's decision.
- `cursor_field` — the field the watermark is read from (default
  `lastModifiedDateTime`). A custom API page may name it something else. The
  field may be a DateTime or a date with no time (`Edm.Date`); see
  "Date fields" under Incremental polling.
- `lookback_days` — only with `incremental` and a date `cursor_field`: also read
  this many days before the watermark, so a document posted today with an
  older date is not missed. A whole number from 0 to 365; default 0 (off).
  See "Backdated records" under Incremental polling.
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
and `503`/`5xx`/network errors and a refused address retry, then go to the
DLQ; `4xx` bad-data and `401`/`403` auth are poison: the message is acked and
dropped with a log line, not dead-lettered.

## Templates: Sitoo ⇄ Business Central (beta)

Two onboarding templates connect Business Central to Sitoo POS through a
converter node. Both are beta until tested against live tenants; see
[Sitoo](sitoo.md#templates-sitoo-business-central-beta) for what the wizard
asks and the assumptions they make.

- **POS sales → ERP** posts one draft `salesInvoices` record per Sitoo
  receipt, lines inline (`salesInvoiceLines` with `lineType: Item`,
  `lineObjectNumber` = Sitoo SKU, `quantity`, `unitPrice` incl. VAT), for one
  customer number you choose. `dedupe_fields: ["externalDocumentNumber"]`
  keeps a receipt from becoming two invoices. Invoices rather than orders
  because `salesInvoices` lists posted invoices too, while a posted order
  drops out of `salesOrders` and the dedupe lookup would no longer find it.
- **ERP inventory → POS** reads `items` filtered on `type eq 'Inventory'`,
  500 per page, every 15 minutes, and not incrementally: an item's
  `lastModifiedDateTime` does not change when its stock does.

A Business Central destination writes only what its predecessor in the
pipeline sends: with a converter in front, the source's raw page (which every
node sees) is skipped and the converted record is written.

## Notes & roadmap

- **On-prem / sandbox** — set `api_base_url`, `token_url`, and `scope` overrides
  for on-prem Business Central or a non-default Entra authority. Both URLs must
  be public `https` addresses; see "Which addresses it will call".
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

    For a DateTime the comparison is `gt`, so a record written in the same millisecond as the
    watermark but after the fetch read it is not picked up. `ge` would redeliver
    the watermark record on every poll instead.

    **Records dated in the future.** The watermark never moves past the
    consumer's own clock. Business Central stamps what it writes with its own
    clock, so a watermark in the future would hide every record stamped before
    it; one record stamped years ahead would hide everything for years. A
    record whose `cursor_field` is later than now is still published, once,
    but does not move the watermark. Because the watermark stays behind it,
    Business Central keeps returning it on every poll; the node remembers (in
    the same checkpoint row) the records it has sent this way, with the cursor
    value each carried, and leaves them out until their value changes. When
    the clock reaches the value, the record is not sent again and the
    watermark moves past it. A record a few seconds ahead is normal (BC's
    clock running ahead of the pod's): it is held for one poll and passes
    without a log line. One more than 5 minutes ahead is logged at Warn with
    its `record_id`, the value and the bound.

    **Pick a last-modified field.** A `cursor_field` that is a business date
    (a due date, say) has every future-dated record held this way: each poll
    reads all of them again, page by page, and the checkpoint row lists each
    one. They are not sent again, but the poll costs grow with them, and past
    1,000 such records the node logs a Warn once.

    **Date fields (`Edm.Date`).** A `cursor_field` whose values are dates with
    no time (`2026-10-29`) works like a DateTime field, with three
    differences:

    - The filter is `<field> ge 2026-10-29`: the date written without quotes,
      which is the OData v4 `Edm.Date` literal, and `ge`, not `gt`. A date has
      no time of day, so `gt` would hide every record added later on the
      watermark day. The node remembers (in the checkpoint row, next to the
      future-dated records) the records it has sent that carry the watermark
      date, and leaves them out when BC returns the day again. A record added
      later that day is sent. Only the newest day is kept; when the watermark
      moves, the older day is dropped.
    - The ceiling is yesterday, not now. BC's own date can be up to 14 hours
      ahead of, or 12 behind, the pod's UTC date, so a watermark on today's
      UTC date could be a day ahead of BC's today and hide the records BC
      dates with it. A record dated today, or later, is sent once and kept out
      of the watermark, like a future-dated DateTime record, and is picked up
      without a second send once its date is yesterday or earlier. A Warn
      names a record dated later than tomorrow (UTC).
    - A date says nothing about modification. An edit that leaves the date
      alone is not seen again (a record without `id` or `number` is
      identified by its content, so there it is). A record whose date is
      earlier than the watermark when it is added is never read, and nothing
      is logged, unless `lookback_days` is set (below). Business dates are
      often backdated — for example, Saturday's sales posted on Monday with
      posting date Saturday — so use
      `lastModifiedDateTime` as the cursor unless you are sure the date field
      is never backdated. The blank date BC returns for an empty field
      (`0001-01-01`) is ignored.

    The cost of a date cursor is the watermark day: each poll reads it again
    and the checkpoint lists its records. A day with more than 1,000 records
    logs a Warn once. A last-modified DateTime field avoids that.

    **Backdated records (`lookback_days`).** With `lookback_days: N` and a
    date `cursor_field`, the filter is `<field> ge W - N days` (W is the
    watermark), so a document posted today with a date inside the last N days
    is read. The node remembers every record it has read dated from W - N
    onward, in the same checkpoint list as the watermark day, and leaves them
    out when BC returns the window again. A record is sent once; an edit that
    changes its date sends it again, as without the option.

    - The range is 0 to 365. 0 is the behaviour described above; a negative
      value or one above 365 stops the node from starting (an Error in the
      log) and fails the poll of a running one. The management API does not
      check the setting.
    - It applies to date fields only. A DateTime `cursor_field` such as
      `lastModifiedDateTime` is stamped by BC when the record is written, so
      nothing can arrive with an older stamp, and a window would only read
      days of changes on every poll. With a DateTime field the option does
      nothing, and the node logs a Warn once. Without `incremental` it does
      nothing either.
    - A record dated before W - N is still never read. Pick N from how late
      your bookkeepers post.
    - The remembered list is rebuilt from each poll, so it holds only the
      records dated from the newest date minus N onward (plus the future-dated
      ones). A record that falls out of the window, or is deleted in BC,
      leaves the checkpoint on the next poll. It is as large as the window: one
      entry is about 55 bytes (a GUID and a date), so 10,000 records in the
      window are about 550 KB of checkpoint JSON, which every poll reads and
      writes when it changes. Past 1,000 the node logs the Warn above; if it
      does, lower `lookback_days` or use a last-modified field. The poll
      itself reads the whole window each time, so a large window is mostly a
      cost in BC calls, not in the checkpoint.
    - The watermark itself does not move back: it is the newest date seen,
      or the stored one if the window found nothing newer.
    - Changing `lookback_days` needs no reset. Lowering it, or setting it to
      0, prunes the remembered list on the next poll and sends nothing.
      Raising it reads the extra days; the node did not remember those records
      before, so the ones that were already sent are sent once more.

    **Changing `cursor_field`.** The checkpoint row records which field its
    watermark was read from. When the configured field differs from the
    recorded one, the node drops the watermark and the records it remembers
    for it, logs a Warn, and reads the entity from the start on the first
    poll, so every record is sent once more. Without this, the old value would
    be sent as a filter on the new field: a DateTime against a date field
    makes BC answer 400 on every poll, and a date from another column would
    silently hide records. Changing back is a change like any other. A
    checkpoint saved by an earlier version has no recorded field; it is taken
    to belong to the configured one, so an upgrade reads nothing again, and
    the next poll records the field. If you changed `cursor_field` on such a
    node before upgrading, its stored watermark may still belong to the old
    field. There is no checkpoint reset, and stopping and starting the
    connection keeps the checkpoint. Use **Resend everything** (`POST
    /api/v1/connections/{id}/resend`): that poll ignores the stored watermark
    and reads the entity from the start, and it stores a watermark from the
    new field with the field recorded. Every record is sent once more.

    The read of that first poll is stored in one checkpoint save at its end.
    If the save fails, it is tried once more on a fresh context (so one cut
    off by a stop still completes; each attempt gives up after 5 seconds). If
    both fail, the poll returns an error and keeps what it meant to store in
    memory; the next poll stores it first and reads nothing until that works,
    so the field change does not turn into a second full read. A restart
    before that loses the memory, and the entity is read from the start once
    more.

    **Nodes frozen before #591.** Earlier versions took any value as the
    watermark, so a node may have stored one in the future and read nothing
    since. On its first poll after the upgrade a stored watermark more than
    5 minutes ahead is replaced, with a Warn. A node without pictures saved
    its checkpoint only when the watermark moved, so `last_processed_at` is
    when it froze: the node reads again from one hour before that, which
    sends that hour's records a second time and the records it missed since
    for the first time. A node with pictures saved on every poll, so its
    `last_processed_at` does not say when it froze; it reads the entity from
    the start and sends everything again once. That is the costly option, but
    resuming from a guessed time would drop whatever changed between the
    freeze and the guess without a trace. If the recovery read fails partway
    it starts over on the next poll and sends the same records again.

    The one-hour window assumes the node never saved after it froze. Earlier
    versions did save again when they read an even later value (2099, then
    2100), and records changed between those two saves are older than the
    window. The recovery read returns every future-dated record, so when it
    sees one more than 5 minutes ahead but below the stored watermark, the
    node may have saved again: it stores no watermark, logs a Warn, and the
    next poll reads the entity from the start (the future-dated records it
    already sent are not sent again). What this cannot see: the first
    far-future record deleted since, or the same record moved from 2099 to
    2100. Several far-future records already there when the node first froze
    trigger a full read that was not needed. The Warns carry `watermark`,
    `last_processed_at` and `resume_from`; if in doubt, use **Resend
    everything** on the pipeline, which reads the entity from the start.

    The code since #591 never stores a watermark later than its own clock, so
    a frozen checkpoint only comes back if the database is restored from a
    backup taken before the upgrade (a DR restore); the recovery then runs
    again for that node.
- **UI source-type wiring** — config is API-settable today.

The same OAuth2 client-credentials pattern (via `pkg/oauthcc`) is reused by the
Visma.net connector.
