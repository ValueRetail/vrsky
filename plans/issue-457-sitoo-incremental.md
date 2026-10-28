# Sitoo incremental polling (#457)

The Sitoo poller (`src/cmd/sitoo-consumer`, `fetchAndPublish`) reads the
whole configured collection on every poll and publishes all of it. The POS
sales template sends every receipt to Business Central every 5 minutes, and
only the BC producer's `dedupe_fields` keeps that from becoming duplicate
invoices, at one BC GET per receipt per poll.

## What Sitoo offers

Checked against developer.sitoo.com on 2026-10-28 (no live account to try it
on). There is no general modified-since filter. Each collection has its own:

| resource | filter param | record field | sort | notes |
|---|---|---|---|---|
| `orders` | `orderidfrom` (">= value") | `orderid` | `sort=orderid` (default is `-orderid`) | no modified-date filter on orders |
| `products` | `datelastmodified` ("modified after", Unix seconds) | `datemodified` | none documented | rechecked in review: Get Products has no `sort` |
| `warehouseitems` | `datelastmodifiedfrom` (2018 release notes) | not confirmed | `sort` exists | field name unknown |

Timestamps are Unix seconds. `transactions`, the connector's default
resource and the POS template's, does not appear in the API reference at
all. That is a separate question (backlog), not this issue.

So the cursor has to be configurable per node, with a default only where
the docs pin down both the filter and a sort: `orders`.

## Approach

Opt-in, like the Business Central consumer's `incremental`: turning it on
changes what a running pipeline delivers.

New `sitoo` consumer keys:

- `incremental` (bool): read only what changed since the last complete poll.
- `cursor_field`: record field holding an increasing integer (a Unix-seconds
  timestamp or an id). Default `orderid` for `orders`; otherwise required.
- `cursor_param`: query parameter Sitoo filters that field with. Default
  `orderidfrom`; otherwise required.
- `cursor_sort`: `sort` value listing the collection ascending on the field.
  Default `orderid` for `orders`; otherwise required.
- `cursor_overlap`: how far back (in cursor units) each poll re-reads.
  Default 60. `0` only with an inclusive filter.
- `cursor_inclusive`: the param returns records equal to the value (`>=`).
  Known true for `orderidfrom`. Unset, the poller sends one below the value,
  which is right for an inclusive and an exclusive filter alike.

A node with `incremental` on is refused at start with a logged error, the
same way an incomplete config is today, when it has no field, param or sort
for its resource, when `cursor_param` is `start`, `num` or `sort` (the
poller sets those), or when `cursor_overlap` is 0 on a filter not known to
be inclusive. A `resource` containing `#` is refused in either mode.

Each poll:

1. Load the checkpoint for (tenant, connection, node) from
   `connection_node_checkpoints` via `pkg/checkpoint` (the BC consumer's
   store). `last_processed_message_id` is the watermark W; `state` holds the
   query it belongs to (resource, resource query string, cursor field and
   param) and the hashes of the records published within the overlap
   window. An unreadable checkpoint fails the poll rather than re-sending
   everything.
2. No checkpoint, or one for a different query: full read. Otherwise start
   at `W - overlap`. The overlap covers records committed late with a
   slightly older cursor value.
3. Page by cursor value (keyset), sorted ascending on the cursor: each next
   page asks the filter for records from the previous page's last value on,
   at offset 0. A record that changes mid-poll gets a newer value, so it
   lands ahead of the read position and is read in this poll; nothing can
   slide behind the reader. Records at exactly the last value come back on
   the next page and are deduped.
4. Check every page before publishing it: ascending on the cursor field
   (else `errNotSorted`) and nothing below the value asked for (else
   `errFilterIgnored`). Either fails the poll without saving, so a sort or
   filter Sitoo ignores cannot lose a record; it stops the pipeline's
   progress with a clear error instead.
5. A page whose records all share one cursor value (a run longer than a
   page, e.g. a bulk import in one second) cannot be stepped past by value.
   Page through the run by offset, each page starting on the previous one's
   last record; if that record is not there, the run changed: read it again
   from its start.
6. Cap the pages per poll at 3 × (totalcount / page size + 1) + 10 from the
   first answer. Hitting it logs, fails the poll and saves nothing.
7. Skip a record whose content hash (SHA-256 of the raw item) was already
   published, from the checkpoint or earlier in this poll. A changed record
   has new content, so a new hash, and goes out again.
8. Only after every page has landed: W' = max(W, highest cursor value seen),
   keep hashes with cursor >= W' - overlap, save, with a context a stop
   cannot cancel (the records are already out). A poll that fails midway
   saves nothing, so the next one re-reads from W (at-least-once, as in BC).

Records without a usable cursor value are published (never dropped), do not
move the watermark and are not remembered.

Also: `resource` may now carry its own query string (`orders?orderstate=10`);
the poller merges it with `start`/`num` instead of appending a second `?`.
The webhook path and the sample-data endpoint use the resource path without
it.

### Why keyset paging (changed in review, #472 round 2)

Rounds 1 and 2 paged by offset and repaired drift with an anchor search
(overlap one record, look for it, step back, grow a slack). Review found that
it could still lose an update when the listing was not sorted on the cursor
(`products`, no sort: a record already read changes in place, a later record
carries the watermark past it), that a changed or deleted anchor at page
size 1000 failed the poll after 11 go-backs, and that the slack made polls
~50× more requests after one miss. With a mandatory ascending sort on the
cursor, paging by value removes the drift itself, so the anchor machinery
is gone. What is left of offset paging is confined to runs of one value.

## Acceptance criteria

1. With `incremental` off, behaviour is unchanged (full read every poll).
2. First incremental poll (no checkpoint): no cursor param on the first
   request, every record is published once, the checkpoint stores the
   highest cursor value.
3. Next poll starts at `W - overlap` and publishes only records not already
   published; an unchanged collection publishes nothing.
4. A record changed during a poll is neither lost nor published twice.
5. A new consumer (restart) with the same checkpoint store resumes from the
   stored cursor and does not re-send.
6. Two tenants (or two connections) with the same node id keep separate
   checkpoints.
7. A failed poll does not advance the checkpoint.
8. `resource` with a query string produces one well-formed URL.
9. Default for `orders`; other resources need `cursor_field`,
   `cursor_param` and `cursor_sort` or the start is refused.
10. (review) Overlap 0 on a non-inclusive filter, a reserved `cursor_param`
    and a `#` in `resource` are refused before any request.
11. (review) Changing the resource, its query, the cursor field or param
    starts over with a full read.
12. (review) A stop right after the last publish does not lose the save; an
    unreadable checkpoint fails the poll with nothing published.
13. (review 2) A listing not sorted on the cursor, or a filter Sitoo
    ignores, fails the poll without saving; a sorted listing publishes a
    change made to a record already read.
14. (review 2) At page size 1000, a boundary record updated in place,
    deleted, or the last record of all: no failure, no loss, no duplicate,
    and no more requests than the collection needs.
15. (review 2) A run of equal cursor values longer than a page is read
    completely, also when it changes mid-run; a poll that can never finish
    stops at the page cap without saving.

## Files

- `src/cmd/sitoo-consumer/service.go` (config, fetch, checkpoint)
- `src/cmd/sitoo-consumer/incremental.go` (new: cursor, paging, dedupe, state)
- `src/cmd/sitoo-consumer/server.go` (sample URL via the shared builder)
- `src/cmd/sitoo-consumer/incremental_test.go` (new, test author; updated
  for keyset paging in review)
- `src/cmd/sitoo-consumer/incremental_config_test.go` (new, review fixes)
- `docs/connectors/sitoo.md`, `plans/backlog.md`

Not touched: the POS template in `ui/src/onboarding/templates.ts` (another
worker owns `ui/`, and its resource `transactions` needs checking against a
live Sitoo first). Follow-up in the backlog.

## Risks

- No live Sitoo account: parameter semantics come from the docs only.
- `orders` by `orderid` only sees new orders, not status changes on old
  ones. That is what the documented filter offers.
- Hash dedupe assumes Sitoo serialises an unchanged record identically. If
  not, the overlap window re-sends (duplicate, never a drop).
- A record committed late with a cursor value more than `cursor_overlap`
  below the newest value seen is missed, as with any watermark (BC too).
  Set the overlap to cover the commit lag.
- Follow-ups from review (backlog 90 to 92): stored hashes bounded by the
  window, not by time; a far-future cursor value freezes the cursor;
  `base_url` is unvalidated tenant config (SSRF) and the webhook id is not
  path-escaped.

## Verify

`go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race ./...`, `make lint-tenant` from `src/`, `-count=3` on the
package; mutation run on the paging, the checks, the cursor and the overlap.
