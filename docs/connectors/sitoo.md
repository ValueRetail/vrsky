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
  `warehouseitems`, `products`). It may carry its own filters as a query
  string, e.g. `orders?orderstate=10`; the poller adds `start`/`num` to it.
  A `#` is refused.
- `poll_interval_seconds` — poll cadence. Set `0` (or omit) for **webhook-only**.
- `page_size` — Sitoo `num` (default `1000`; 1000–5000 is optimal).
- `base_url` — optional override (default `https://api.mysitoo.com/v2`).
- `webhook_raw_event` — forward SPI event bodies verbatim instead of
  dereferencing them. See below; leave unset unless you mean it.
- `webhook_id_field` — override the event field holding the resource id. The
  default follows Sitoo's naming (`transactions` → `transactionid`).

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

### Incremental polling

By default every poll reads and publishes the whole collection. For the
`orders` resource, set `incremental: true` to publish only orders that
haven't been published yet:

- `incremental` — on for `orders` only. Sitoo has no common "modified since"
  filter, and `orders` is the one collection whose filter (`orderidfrom`)
  and sort (`sort=orderid`) the API reference documents. Any other resource
  with `incremental` on is refused at start. A query string on the resource
  is fine (`orders?orderstate=10`), as long as it doesn't set `orderidfrom`,
  `sort`, `start` or `num`: the poller sets those itself.
- `cursor_overlap` — how far back each poll re-reads, in order ids (default
  `1000`). It covers an order whose id was handed out before others but that
  showed up in Sitoo after them, for example from a till that was offline.
  Between `0` and `100000`. Raising it doesn't re-send the orders between
  the old window and the new one; the wider window builds up as new orders
  come in.

```json
{
  "type": "sitoo",
  "sitoo": {
    "account_id": 12345,
    "site_id": 1,
    "api_id": "your-api-id",
    "api_password_secret_id": "<secret-uuid>",
    "resource": "orders",
    "poll_interval_seconds": 300,
    "incremental": true
  }
}
```

How it behaves:

- The first poll reads every order and publishes it once. So does the first
  poll after the resource or its query string changes. After that each poll
  starts at `last order id − cursor_overlap`.
- It reads orders in id order, a page at a time: each next page asks for
  orders from the previous page's last id on, until a page brings nothing
  new. A page shorter than `page_size` doesn't end the poll, in case Sitoo
  returns fewer orders than asked for.
- Every page is checked before it's published: each order has a numeric
  `orderid`, the ids rise, and none is below the id asked for. A page that
  fails the check fails the poll with an error saying which, and nothing on
  it is published.
- The highest order id published is saved per connection node in
  `connection_node_checkpoints` after every page, so a restart, a redeploy
  or a failed poll resumes from there. The page that failed can arrive
  twice; no order is skipped.
- A poll reads at most 100 pages (100 000 orders at the default page size).
  The next poll re-reads the overlap and goes on from there. Keep
  `cursor_overlap` well below 100 × `page_size`: if re-reading the overlap
  takes more than one poll, new orders wait several polls, and a late order
  can be missed during that catch-up.
- Orders re-read inside the overlap are not sent again. An order that shows
  up later than `cursor_overlap` ids behind the newest one published is
  missed; raise the overlap if your tills can lag more than that.
- It picks up new orders, not changes to existing ones: Sitoo's
  `orderidfrom` filters on the id, which never changes.
- Orders that also arrive through webhook mode on the same connection aren't
  known to the poller, so an order can come through both.
- A resource query string that doesn't parse is refused, in either mode,
  rather than polled without its filter.

Turning `incremental` on for a running pipeline sends every order once more
on its first poll, then only new ones. This hasn't been run against a live
Sitoo account yet; the filter and sort come from the API reference. If Sitoo
ignores either, the poll fails with that error rather than losing orders.

### Webhook mode (real-time SPI Events)

Point Sitoo's SPI Events at the connector's auxiliary HTTP endpoint:

```
POST /sitoo/events/{connectionID}
```

served on `WORKER_HTTP_PORT` (9260 in compose). Configure the connection with
`poll_interval_seconds: 0` if you want webhook-only (no polling).

### Events are dereferenced, not forwarded

An SPI event is a **notification**, not a resource — it carries
`{eventid, eventtype, <resource>id}` and nothing else. The connector fetches the
resource the event names and publishes it in the same shape the poll path uses:
a JSON array of resource objects, with `dereferenced: true` in the envelope
metadata.

That matters because it is what makes the two modes interchangeable. Forwarding
the event verbatim meant a webhook-fed pipeline delivered an event object where
a poll-fed one delivered an array of transactions — and a `sitoo` destination
POSTs whichever it receives to the same collection endpoint, so only one of them
was a write it could make sense of.

One extra GET per event is the cost. If your destination genuinely wants the
event rather than the resource — a filter keyed on `eventtype`, or an HTTP
producer pointed at something event-shaped — set `webhook_raw_event: true`.
A `sitoo` producer refuses event notifications outright rather than POSTing
them, so do not set it on a pipeline whose destination is Sitoo.

## As a destination (producer)

A `sitoo` producer node writes each incoming message into a Sitoo REST resource
(e.g. updating warehouse stock, prices, or products) with HTTP Basic auth —
completing two-way sync. The message payload is sent as the request body, so an
upstream converter/filter should shape it to the target resource's schema.
The producer writes only what its predecessor in the pipeline sends: with a
converter in front, the source's raw page (which every node sees) is skipped
and the converted one is written.

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

## Templates: Sitoo ⇄ Business Central (beta)

The onboarding gallery has two templates between Sitoo and Business Central.
Both are **beta**: their field mapping is tested against the converter, not
yet against live Sitoo and BC tenants. Try them on a test company and a test
warehouse first. The full field tables and the assumptions behind them are in
`plans/issue-435-sitoo-bc-mapping.md`.

**POS sales → ERP.** Polls Sitoo `transactions` every 5 minutes and creates
one draft BC sales invoice per receipt, one line per item. The wizard asks
for:

- Sitoo account number, site id, API id and password
- BC Entra tenant id, environment, company id, client id and secret
- the BC customer number every invoice is created for

What to know before you use it:

- The Sitoo `sku` must equal the BC item number. Item variants are not
  handled.
- Sitoo prices include VAT, so the BC customer needs *Prices Including VAT*
  turned on.
- The receipt number goes to `externalDocumentNumber`, which is how a receipt
  already in BC is skipped, posted or not. The template polls `transactions`
  without `incremental`, so it re-reads every receipt on each poll, one BC
  lookup each; keep it to a test site with few receipts. The template will
  switch to incremental polling once a live Sitoo sandbox confirms which
  collection holds receipts.
- Use one Sitoo site per pipeline: receipt numbers from two sites could
  collide.
- Currency and posting date are not sent. BC takes them from the customer
  and the work date.

**ERP inventory → POS.** Reads every BC inventory item every 15 minutes and
sends `[{sku, decimaltotal}]` to `warehouses/<id>/warehouseitems`. The wizard
asks for the BC and Sitoo credentials and that path with your warehouse id in
it. BC's item `inventory` is the total over all locations, and prices are not
synced.

Running both together: a draft invoice does not lower BC stock until it is
posted, so until then the stock push sends Sitoo the level from before the
sale.

## Notes & roadmap

- **Webhook signature verification** — add HMAC verification once the SPI Event
  signing scheme is confirmed for your account.
