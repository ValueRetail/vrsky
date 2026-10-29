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

`transactions`, the connector's default resource and the POS template's,
does not appear in the API reference at all (backlog 126, human item 9).

## Scope: `orders` only (decided with Ludvik, 2026-10-29)

Rounds 1 to 3 built a generic cursor (`cursor_field`, `cursor_param`,
`cursor_sort`, `cursor_inclusive`) so any collection could be polled
incrementally. Round 3 still found two ways to lose a record, both from that
generality: a partial override paired a custom field with the `orders`
defaults, and runs of equal cursor values longer than a page relied on
Sitoo's tie order. Without a Sitoo sandbox none of the other collections'
filter, sort and tie behaviour can be checked.

So incremental polling ships for `orders` only, the one collection whose
filter and sort the API reference documents, with the cursor fixed in code:
field `orderid`, filter `orderidfrom`, `sort=orderid`. `orderid` is unique,
so two records never share a cursor value and there are no runs to page
through. Other collections are backlog 127, after a sandbox.

## Approach

Opt-in, like the Business Central consumer's `incremental`: turning it on
changes what a running pipeline delivers.

Sitoo consumer keys:

- `incremental` (bool): read only orders not yet published.
- `cursor_overlap` (orderids, default 1000): how far back each poll
  re-reads, for an order whose id was handed out before others but that
  became visible after them (several tills). At the default page size that
  costs at most one extra page per poll; the re-read orders are not sent
  again.

A node with `incremental` on is refused at start with a logged error, the
same way an incomplete config is today, when its resource is not `orders`
(a query string is allowed, `orders?orderstate=10`), when that query string
sets `orderidfrom`, `sort`, `start` or `num` (the poller sets those), or
when `cursor_overlap` is negative. A `resource` containing `#` is refused in
either mode.

Each poll:

1. Load the checkpoint for (tenant, connection, node) from
   `connection_node_checkpoints` via `pkg/checkpoint` (the BC consumer's
   store). `last_processed_message_id` is the watermark W, the highest
   orderid published. `state` holds the query it belongs to (resource and
   its query string) and the orderids published in `[W − overlap, W]`.
   A store that can't be read fails the poll. A row that can't be decoded,
   or one for a different query, means a first poll.
2. First poll: no filter. Otherwise the first page asks for
   `orderidfrom = W − overlap`.
3. Every page is `sort=orderid`, `start=0`, `num=page size`, and is
   checked before anything on it is published: every record has an
   integer `orderid` (else `errNoOrderID`), the ids rise strictly (else
   `errNotSorted`), and none is below the `orderidfrom` asked for (else
   `errFilterIgnored`). Any of these fails the poll; pages already
   published stay saved.
4. Publish the records whose orderid is not in the stored set and not met
   earlier in this poll. Then save the checkpoint: W' = the page's last
   orderid if higher, with the published ids pruned to `[W' − overlap, W']`.
   The save uses a context a stop can't cancel (the records are out). A
   failed save fails the poll.
5. A page shorter than the page size ends the poll. Otherwise the next page
   asks for `orderidfrom = the last orderid`. Whether Sitoo treats the
   filter as `>=` or `>`, that returns everything after it; the repeated id
   is dropped as met.
6. At most 100 pages per poll (100 000 orders at the default size). The
   rest is read by the next poll, from the saved watermark.

Saving per page is safe because `orderid` is unique and the listing is
sorted on it: once a page is out, every order up to its last id is out,
except one that becomes visible later, which the overlap covers.

Also: `resource` may carry its own query string (`orders?orderstate=10`);
the poller merges it with `start`/`num` instead of appending a second `?`.
The webhook path and the sample-data endpoint use the resource path
without it.

## Acceptance criteria

1. With `incremental` off, behaviour is unchanged (full read every poll).
2. First incremental poll (no checkpoint): no `orderidfrom` on the first
   request, `sort=orderid` on every request, every order published once,
   the checkpoint stores the highest orderid.
3. The next poll's first request asks for `W − overlap`, and only orders not
   already published go out; an unchanged collection publishes nothing.
4. An order that becomes visible late with an id inside the overlap is
   published once; one below the overlap is not (documented).
5. A new consumer (restart) with the same checkpoint store resumes from the
   stored watermark and doesn't re-send.
6. Two tenants (or two connections) with the same node id keep separate
   checkpoints; the node id keys the checkpoint.
7. A poll that fails on page k has saved pages 1 to k−1; the next poll
   resumes after them and nothing is lost. A first page that fails saves
   nothing.
8. A listing not sorted ascending on orderid, a repeated orderid, a filter
   Sitoo ignores, or an order without an integer orderid fails the poll
   before that page is published.
9. Whether `orderidfrom` is inclusive or exclusive, every order is
   published exactly once.
10. A poll stops after 100 pages with the watermark saved, and the next
    poll carries on from it.
11. Incremental on a resource other than `orders`, a resource query setting
    `orderidfrom`, `sort`, `start` or `num`, a negative overlap, or a `#` in
    the resource is refused before any request.
12. Changing the resource or its query string starts over with a full read.
13. A stop right after a publish doesn't lose that page's save; a store
    that can't be read fails the poll with nothing published; a row that
    can't be decoded reads everything once and is replaced.
14. `resource` with a query string produces one well-formed URL.

## Files

- `src/cmd/sitoo-consumer/service.go` (config, fetch, refusal at start)
- `src/cmd/sitoo-consumer/incremental.go` (cursor, paging, dedupe, state)
- `src/cmd/sitoo-consumer/server.go` (sample URL via the shared builder)
- `src/cmd/sitoo-consumer/incremental_test.go`,
  `incremental_config_test.go` (test author)
- `docs/connectors/sitoo.md`, `plans/backlog.md`

Not touched: the POS template in `ui/src/onboarding/templates.ts`. Its
resource `transactions` needs checking against a live Sitoo first
(backlog 126).

## Risks

- No live Sitoo account: `orderidfrom` and `sort=orderid` come from the API
  reference only. A sort or filter Sitoo ignores fails the poll with an
  error naming it; it can't lose an order.
- `orders` by `orderid` sees new orders, not status changes on existing
  ones. That is what the documented filter offers.
- An order that becomes visible more than `cursor_overlap` ids behind the
  newest one published is missed, as with any watermark. Raise the overlap
  if a site's tills can lag more than that.
- The first poll publishes every order in the collection, as the BC
  consumer's first incremental poll does.

## Verify

`go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race ./...`, `make lint-tenant` from `src/`, `-count=3` on the
package; mutation run on the checks, the dedupe, the save and the overlap.
