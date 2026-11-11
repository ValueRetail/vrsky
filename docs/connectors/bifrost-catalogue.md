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
  the raw token on the node. This is the only setting read from the secrets
  store: other `*_secret_id` keys are ignored, and a `{secret:<id>}`
  placeholder in any setting makes the node fail as misconfigured (retried,
  then the DLQ). The error names the setting, not its value.
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

### Sequence

The object form can carry a `sequence` beside the items:

```json
{"items": [ ... ], "sequence": "2026-11-11T12:00:00.123Z"}
```

It is sent on `begin` as Bifrost's ordering guard (see
[Retries are safe](#retries-are-safe)). It is either:

- a whole number from 1, sent as it is; or
- an RFC 3339 timestamp, sent as its Unix time in milliseconds. This is for a
  source event's own time: a converter can copy a timestamp but can't turn it
  into a number.

It has to grow from one message to the next for the same Bifrost tenant. Use
the source's own time or counter, not a VRSky stream sequence: a stream that
is rebuilt (a DR restore) starts counting again, and Bifrost would then refuse
every later import. Two messages with the same value are one delivery to
Bifrost, so the second is dropped and its rows are lost. A source that can
send two lists in the same millisecond needs a counter instead, and so does a
source whose clock can step backwards. A number that passes through a
converter is read as a floating-point number, so keep it below 2^53
(9007199254740992); a timestamp is the safe form.

Without `sequence` (or with `null`) the import is sent unnumbered, as before:
Bifrost applies it and keeps the highest sequence it already has. A list
payload, or a single item in `delta` mode, has no sequence. A `sequence` that
is neither of the two forms (a fraction, a negative number, `0`, a string that
isn't a timestamp) is refused without contacting Bifrost, like a bad payload.

## What happens on failure

| What went wrong | Result |
|---|---|
| Network error, timeout, 5xx, 3xx | Retried (1 s, 5 s, 30 s, 2 min), then the DLQ. The import is abandoned each time and starts over. |
| 429 (too many imports open) | Retried after `Retry-After`. |
| 401, 403 | Retried, then the DLQ. The token, its `catalogue.import` permission or `tenant_id` is wrong. Fix the node, then replay the message from the DLQ. |
| 413, or 400 on items | Retried, then the DLQ. Either a proxy in front of Bifrost refuses the request size (ingress-nginx defaults to 1 MiB; lower `batch_size`) or an item isn't valid UTF-8/JSON (fix the source). Then replay from the DLQ. |
| 404 | Retried: the import expired or Bifrost restarted (imports live in its memory). On the first call it can also mean `base_url` isn't the catalogue-api. |
| 200 that isn't Bifrost's (an HTML page from a proxy, an items answer without `total_so_far`, or one whose count differs from what was sent) | Retried, then the DLQ. The import is abandoned. |
| 409 at commit, for a message with a `sequence` | Dropped, not retried or dead-lettered: Bifrost already has this import or a newer one. In `full` mode nothing is lost (a newer whole list is live) and it is logged as a warning. In `delta` mode the message's rows are lost unless a later import carried the same articles, so it is logged as an error (see [Retries are safe](#retries-are-safe)). |
| Other 4xx (the shrink guard, a file that is mostly bad rows, validation, a 409 for a message without a `sequence`) | Dropped, not retried. The same list would be refused again. Bifrost's reason is in the log and in the error text. |
| The node's settings are missing or wrong | Retried, then the DLQ, for a replay after the fix. |
| The payload isn't a list of items, or its `sequence` can't be used | Dropped. |

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

**Ordering needs a sequence.** The connector runs as one pod, and a pod
delivers one message at a time, so messages that go through on the first
attempt commit in the order they were published. Two things break that:

- A retry can arrive late (retries wait up to 2 minutes). If an older message
  is retried after a newer one has committed, the older list would commit last
  and win, delta or full.
- A rolling update briefly runs the old and the new pod side by side, and both
  can be running an import at the same moment.

A message with a [`sequence`](#sequence) closes both. Bifrost refuses the
commit with 409 when the tenant already has an import with that sequence or a
higher one (Bifrost#253, `docs/catalogue-doors.md` there), and the connector
drops that delivery and logs it. The same answer covers the redelivery after a
lost ack: the list is already there. Messages without a sequence keep the old
behaviour, so a source that has no growing number or time still has the
retry case open.

**What the guard costs in `delta` mode.** Bifrost keeps one sequence per
tenant, not one per article. So when delta N fails for a while (a 5xx, a
timeout, a Bifrost restart) and delta N+1 commits before N's retry, N is
refused and dropped, even if N+1 held other articles. N's rows then reach the
tills only when a later import carries them. Without the sequence, N would
have committed late and overwritten any article N+1 also held with an older
price. The connector logs such a drop as an error
(`dropped the delivery; its rows reach the tills only if a later import
carries them`); alert on it. To recover, send the articles again with a newer
sequence, or load the whole catalogue in `full` mode (#856).

**Replaying from the DLQ.** A message with a `sequence` that waited in the DLQ
(a bad token, a missing setting) is usually older than what committed while
it waited. Replayed, it is refused with 409 and dropped, as above. Send the
articles again with a newer sequence instead, or replay a copy without
`sequence`.

The connector is kept at one replica on purpose. Two pods would run messages
N and N+1 at the same time, so the older list could commit last even without a
retry, and would use twice as many of Bifrost's 32 open imports, which every
client of that Bifrost shares.

Limits worth knowing:

- If Bifrost accepts the begin but the answer is lost, the connector has no id
  to abandon. That import sits unused until Bifrost expires it after an hour.
- With several Bifrost nodes on one connection, each gets the list. If one
  fails, the message is retried and the others import again, which changes
  nothing.
- The message is held in memory while it is delivered, and a delivery may take
  at most 20 minutes. The payload size is bounded by the platform's claim-check
  limit (128 MiB by default).
