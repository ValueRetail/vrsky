# Bifrost catalogue

The Bifrost catalogue connector (`bifrost_catalogue`) delivers catalogue items
(articles and prices) to the catalogue-api of Bifrost, the POS and basket
platform. It is a destination only. It uses Bifrost's staged import, so a full
price list lands atomically and Bifrost's shrink guard applies. The contract is
`docs/catalogue-doors.md` in the Bifrost repository.

For a handful of changed prices, the [HTTP producer](http.md) can already call
`PUT /v1/catalogue/items` one message at a time. This connector is for the
cases that endpoint can't cover: a whole list that must replace the catalogue
in one step, or a delta of many rows.

## How an import runs

For each message the connector runs one import against Bifrost:

1. `POST /v1/catalogue/imports` with `{"kind": "full" | "delta"}` opens it.
2. `POST /v1/catalogue/imports/{id}/items` sends the items in batches of
   `batch_size`. Each request has an `Idempotency-Key` (`<message id>-<batch
   number>`).
3. `POST /v1/catalogue/imports/{id}/commit` makes it visible.
4. On any failure after step 1, `DELETE /v1/catalogue/imports/{id}` abandons it.

Nothing a till sees changes before the commit. A connection that dies at batch
23 of 40 leaves an import that never commits and is abandoned (Bifrost also
drops an untouched import after an hour, and allows 32 open at once).

**One message is one import.** The window is the message, not a time span: the
message must hold everything the import should contain. In `full` mode that is
the whole catalogue, as a JSON array (or `{"items": [...]}`). A source that
emits one message per page or per article must not feed a `full` node, since
every message would replace the catalogue with its page. A bare object is
refused in `full` mode for that reason. Collect the list into one message
first, or use `delta`.

## Configuration

```json
{
  "type": "bifrost_catalogue",
  "bifrost_catalogue": {
    "base_url": "https://catalogue.example.com",
    "token_secret_id": "<secret-uuid>",
    "mode": "full",
    "batch_size": 1000
  }
}
```

- `base_url` (required) — the origin of the catalogue-api. `https`, no user
  name, query string or fragment; private, loopback and link-local addresses
  are refused, like in the other connectors. Bifrost itself runs without TLS,
  so it needs a TLS-terminating proxy in front.
- `token_secret_id` (required) — secret holding the tenant's bearer token. It
  needs the permission `catalogue.import` in Bifrost's credentials. Never put
  the raw token on the node.
- `tenant_id` — Bifrost's tenant ID (the one in its credentials file), not
  VRSky's, sent as `X-Tenant-Id`. Optional: Bifrost takes the tenant from the
  credential and answers 403 if the header names another one.
- `mode` — `delta` (default) upserts the items. `full` makes the items the
  whole catalogue: articles not in the list are removed. The default is the
  safe one on purpose.
- `batch_size` — items per request, 1 to 10000, default 1000. A request is also
  split at about 4 MiB.
- `allow_shrink_reason` — overrides Bifrost's shrink guard for every import of
  this node and is recorded there with the reason. Leave it out. The guard is
  what stops a failed export from emptying the tills; set it only for a run
  where the catalogue is meant to shrink, and remove it afterwards.

## Message payload

A JSON array of item objects, or an object `{"items": [...]}`. In `delta` mode
a single item object is accepted. The items are passed on exactly as they
arrive, field for field, so prices stay the strings the source wrote
(`"24.90"`, never a float) and fields Bifrost adds later pass through.
Bifrost validates the items. The fields it knows today include `article_no`,
`name`, `gtin`, `price_ex_vat`, `vat_code`, `unit`, `category`, `min_age`,
`alcohol` and `effective_from`. Bifrost is adding a VAT-inclusive shelf price
(`price_incl_vat` with `vat_rate`, Bifrost#249); build the item in the
converter in front of this node. Use `.` as the decimal separator: Bifrost
refuses commas.

An empty list, a payload that isn't JSON and an item that isn't an object are
refused without contacting Bifrost.

## What happens on failure

| What went wrong | Result |
|---|---|
| Network error, timeout, 5xx, 3xx | Retried (1 s, 5 s, 30 s, 2 min), then the DLQ. The import is abandoned each time and starts over. |
| 429 (too many imports open) | Retried after `Retry-After`. |
| 401, 403 | Retried, then the DLQ. The token, its `catalogue.import` permission or `tenant_id` is wrong. Fix the node, then replay the message from the DLQ. |
| 413, or 400 on items | Retried, then the DLQ. Either a proxy in front of Bifrost refuses the request size (ingress-nginx defaults to 1 MiB; lower `batch_size`) or an item isn't valid UTF-8/JSON (fix the source). Then replay from the DLQ. |
| 404 | Retried: the import expired or Bifrost restarted (imports live in its memory). On the first call it can also mean `base_url` isn't the catalogue-api. |
| 200 that isn't Bifrost's (an HTML page from a proxy, an items answer without `total_so_far`, or one whose count differs from what was sent) | Retried, then the DLQ. The import is abandoned. |
| Other 4xx (the shrink guard, a file that is mostly bad rows, validation) | Dropped, not retried. The same list would be refused again. Bifrost's reason is in the log and in the error text. |
| The node's settings are missing or wrong | Retried, then the DLQ, for a replay after the fix. |
| The payload isn't a list of items | Dropped. |

Error texts carry Bifrost's own reason (its `{"error": "..."}` body, cut at 300
characters) and never the token or a network address.

A commit answered 200 whose report can't be parsed counts as done only when
the body starts with `{` (if it was cut off by the 1 MiB read limit, the
rejected count is unknown and logged as such). Any other body is retried.

Rows Bifrost rejects while still accepting the import (it takes an import with
a few bad rows) are logged as warnings with their row number, article number
and reason, ten at most per import. They are not retried.

## Retries are safe

A retry never commits a half-filled import: every attempt opens a new import,
and an attempt that fails abandons its own. A message that was committed but
whose acknowledgement was lost (a crash between the commit and the ack) is
delivered again: a `full` import of the same list replaces the catalogue with
the same list, and a `delta` upserts the same rows. On its own that ends in the
same catalogue.

**Ordering is not guaranteed, though.** Bifrost's commit overwrites the
snapshot, and a retry can arrive late (retries wait up to 2 minutes). If an
older message is retried after a newer one has committed, the older list
commits last and wins, delta or full, until the next import changes the
catalogue. Closing that needs a version check in Bifrost (an import that says
which source version it is built from); that is being filed there. Until then,
send full lists from one source at a time and don't rely on a retried message
being the latest.

Limits worth knowing:

- If Bifrost accepts the begin but the answer is lost, the connector has no id
  to abandon. That import sits unused until Bifrost expires it after an hour.
- With several Bifrost nodes on one connection, each gets the list. If one
  fails, the message is retried and the others import again, which changes
  nothing.
- The message is held in memory while it is delivered, and a delivery may take
  at most 20 minutes. The payload size is bounded by the platform's claim-check
  limit (128 MiB by default).
