# Business Central connector: pictures

## Revision 2 (2026-09-28) — records AND pictures on one pipeline

Supersedes decision 4 below ("pictures *instead of* records"). PR #282 is
held and reworked on the same branch.

**Requirements (Bifrost, the till's catalogue importer):**

| Requirement | Why |
|---|---|
| CSV records unchanged (same converter, same columns) | the watcher and importer stay as they are |
| One file per picture, named `<item number>.<ext>` (`1896-S.jpg`), ext from the content type | matched by article number, the catalogue key |
| Written into the same agent folder (`catalogue-in`) | the watcher ignores non-CSV; an image ingester picks up `*.jpg`/`*.png` |
| Only when the picture exists or changed | 80 images every 15 minutes is waste |
| Original size | Bifrost downsizes for the 64 px tile itself |

**What that needs, found by reading the code:**

1. **BC consumer.** The switch becomes **"Also send pictures"**
   (`pictures: true`): the record pages are published exactly as today — no
   `$select`, all fields — and, after each page, one message per picture of
   that page's records, only when the picture is new or its id changed
   (per-poller seen-map, as built). "Pictures only" mode is dropped (YAGNI;
   nobody asked for it).
2. **data-converter drops pictures today.** An image cannot be parsed, so
   `processEntry` emits "Payload parse failed" and acks — the picture is
   gone. Fix: a message whose content type is a media type (`image/*`, and
   `application/octet-stream`, `application/pdf`, `audio/*`, `video/*`) is
   **passed through unchanged**: fresh envelope id, `_last_processed_by` set
   to the converter node (so the next node accepts it), no `_converted`, the
   claim-check ref kept as is (no rehydrate, no re-offload), a "passed
   through" event for the panel.
3. **data-filter drops them the same way.** Same pass-through. A filter rule
   cannot be evaluated on an image; documented: a filter on records does not
   filter their pictures.
4. **Destination naming.** With a `filename_pattern` set (likely, for the
   CSV), the remote agent and file-producer name *every* message by the
   pattern, so pictures would become `catalogue-<ts>.jpg`. Fix: a **media**
   message that carries a `filename` keeps it, pattern or not; the pattern
   keeps naming the records. In `agentproto.GenerateFilename` (remote agent)
   and file-producer's `generateFilename`.

**Failure semantics.** A picture that fails to download fails the fetch, so
the incremental watermark holds: the next poll re-sends that page's records
(Bifrost's import is keyed by article number, so a repeat is harmless) and
retries the picture. Picture changes on an unchanged item are only seen if
BC bumps the item's `lastModifiedDateTime` (assumption A) — with
`incremental` off, every poll checks every item's picture id (one small
request per item, no download when unchanged).

**Tests added/changed:** BC: records + pictures both published, records
unchanged byte-for-byte vs pictures off, no `$select`. Converter and filter:
an image passes through with bytes/ref/filename intact and `_last_processed_by`
set, a JSON message still converts. Naming: pattern + media with filename →
filename; pattern + CSV → pattern. An end-to-end check over the local stack:
BC-shaped JSON + an image through converter (JSON→CSV) into file-producer.

**Built (2026-09-28), deviations:** the local end-to-end over the compose
stack was not run (it needs a logged-in session to create the pipeline); the
hops are covered by unit tests on real JetStream and real servers, and the
end-to-end is the prod check with the till PC. The shared media rule lives in
`envelope.IsMedia` (image/audio/video/PDF; not `application/octet-stream`).

**Rollout grows:** data-converter and data-filter (core), remote-agent and
file-producer (connectors) are rebuilt along with business-central-consumer.

---

## Decisions (2026-09-28, with Ludvik)

1. **BC → pipeline only.** The consumer downloads pictures; uploading into BC
   is a follow-up.
2. **Any entity that has a picture**: items, customers, vendors, employees,
   contacts (the set BC's API v2.0 documents). `items` stays the default.
3. **One message per picture**, image bytes as the payload, content type from
   BC (`image/jpeg` …), the record's identity and a filename in the metadata.
4. **A `pictures` switch on the node**: it sends pictures *instead of*
   records. One pipeline carries items, another carries item pictures, so a
   converter or filter on a pipeline keeps seeing one kind of data.

## Open questions

None blocking. Two things are **assumptions to verify in TEST** before this
is called done (see Verification):

- A. Replacing a picture in BC bumps the parent record's
  `lastModifiedDateTime`, so incremental polling picks the change up.
- B. A replaced picture gets a new picture `id` (BC media ID), so an unchanged
  picture can be recognised without downloading it.

If A is false, incremental pipelines miss picture-only changes; the fallback
is non-incremental polling plus B. If B is false, every poll re-downloads
every picture — the pipeline still works, it just costs more BC requests.

## Goal

A BC consumer node with `pictures: true` polls its entity as today (same
`filter`, `incremental`, `cursor_field`, `page_size`) and, for every record
that has a picture, emits one message with the image bytes. Large images go
through the existing claim-check. A remote-agent or file destination then
writes `1896-S.jpg` to disk; Bifrost/PrestaShop gets the bytes with the item
number beside them.

## What BC offers (verified against Microsoft Learn, API v2.0, 2026-08 docs)

- `GET …/companies({c})/{entity}({id})/picture` → `{ id, parentType, width,
  height, contentType, "pictureContent@odata.mediaReadLink": "…/picture({pid})/content" }`.
  Entities: items, customers, vendors, employees, contacts.
- `GET …/picture({pid})/content` → raw image bytes.
- (Upload, not in scope: `PATCH …/{entity}({id})/picture/pictureContent`,
  `If-Match` required, 204.)

## Approach

### Consumer (`src/cmd/business-central-consumer`)

**Config.** `BCConfig` gets `Pictures bool json:"pictures"`. When set, the
entity must be one of the five above; otherwise the poller refuses to start
with `pictures is only available for items, customers, vendors, employees and
contacts`. (Deploy-time validation in `nodeconfig.go` is presence-only; the
consumer check is the authoritative one, as for the other BC settings.)

**Fetch.** `fetchAndPublish` is unchanged in shape: it pages through the
entity, tracks the watermark, and advances it only after every page has
landed. In pictures mode:

- the page request adds `$select=id,number,displayName,<cursor field>` — the
  records are not published, only used to find pictures, so ask for what is
  needed (`number`/`displayName` are absent on some entities; BC ignores
  unknown `$select` fields? **No** — it 400s. So the select list is per
  entity: items/customers/vendors have `number` + `displayName`, employees
  `number` + `displayName`, contacts `number` + `displayName`; verified in the
  same TEST pass, and the fallback is no `$select` at all);
- for each record: `GET {entity}({id})/picture`. **404 or an empty
  `contentType`** = no picture, skipped and counted. Anything else non-2xx =
  the fetch fails, the watermark holds, the next poll retries — the same
  bargain a failed page makes today;
- the content URL is the `mediaReadLink`'s **path** joined to the configured
  API host (an on-prem BC reports an internal hostname there);
- the content response is streamed: `Content-Length > InlineMaxBytes` →
  `publishStream` (claim-check, `PayloadRef`), else read and `publish`.
  This means `bcConsumer` implements `RunStream(ctx, publish, publishStream)`
  (the SDK prefers it when present; `Run` stays for the tests that use it)
  and `Configure` keeps `res.InlineMaxBytes()`.

**Envelope per picture.**

| Field | Value |
|---|---|
| `ContentType` | BC's `contentType` (`image/jpeg`, `image/png` …) |
| `Payload` / `PayloadRef`, `PayloadSize` | the bytes, or the claim-check ref; `Content-Length` |
| `Source`, `StepHistory` | `business-central-consumer`, as today |
| `Metadata.entity` | e.g. `items` |
| `Metadata.record_id`, `.number`, `.display_name` | from the record (`number`/`display_name` only when present) |
| `Metadata.picture_id`, `.width`, `.height` | from BC |
| `Metadata.filename` | `<number or record_id>.<ext>`, ext from the content type (`jpeg`→`jpg`, `png`, `gif`, `bmp`, else `mime.ExtensionsByType`, else `bin`); `/`, `\` and control characters replaced |

`filename` is what remote-agent and file-producer already use to name the
file, so no destination changes.

**Not re-sending unchanged pictures.** Each running poller keeps
`recordID → pictureID` in memory; a record whose picture `id` is unchanged
since the last poll is skipped without downloading the content (assumption B).
The map is per process: after a restart, the first poll sends everything
again — at-least-once, and bounded by `incremental` where it is on.
Persisting the map (a checkpoint row) is a follow-up if that first poll is
too heavy in practice.

**Preview.** The pre-deploy "show data structure" endpoint returns, in
pictures mode, a description (`one message per picture: image/* bytes, metadata
filename/number/record_id/…`) instead of trying to preview an image.

### UI

`PropertyEditor.tsx`, BC consumer block (~line 3409): a checkbox **"Send
pictures instead of records"**, shown only when `entity` is one of the five,
mapping to `business_central.pictures`, with one line of help ("One message
per picture; a file or remote-agent destination writes `<number>.jpg`").
The BC block lives inside the large, untested `PropertyEditor.tsx`; moving it
out is a refactor of its own and not done here.

### Docs

`docs/connectors/business-central.md`: a **Pictures** subsection under
*As a source*: the switch, which entities, the message shape and metadata
table, the two-pipelines pattern, the change-detection notes, request cost.

## Files

| File | Change |
|---|---|
| `src/cmd/business-central-consumer/service.go` | `Pictures` field + validation; `RunStream`; `inlineMax`; `$select` in `entityURL` for pictures mode; branch to `publishPictures` |
| `src/cmd/business-central-consumer/pictures.go` (new) | picture metadata/content fetch, envelope building, filename, the per-poller seen-map |
| `src/cmd/business-central-consumer/pictures_test.go` (new) | tests below |
| `src/cmd/business-central-consumer/server.go` | sample-data preview text in pictures mode |
| `ui/src/components/Pipeline/PropertyEditor.tsx` | the checkbox |
| `docs/connectors/business-central.md` | Pictures section |

Not changed: producer, remote-agent, file-producer, SDK, nodeconfig rules,
deploy scripts.

## Tests (fake BC with `httptest`, as `consumer_test.go` does)

- `TestPictures_OneEnvelopePerPicture`: two items, one without a picture
  (404) → one envelope: `image/jpeg`, the bytes, `filename` `1896-S.jpg`,
  `number`, `record_id`, `picture_id`, `width`/`height`; **no** JSON page
  envelope.
- `TestPictures_LargeContentGoesThroughTheClaimCheck`: content above a small
  `inlineMax` → `publishStream` with the bytes; below → `publish` inline.
- `TestPictures_RequiresAnEntityWithPictures`: `salesOrders` + `pictures` →
  the poller does not start, the error names the five entities.
- `TestPictures_SelectsOnlyIdentityFields`: the page request carries
  `$select=…`; a records-mode request does not.
- `TestPictures_UnchangedPictureIsNotResent`: two polls, same picture id →
  one envelope and no content request the second time; a new picture id →
  a second envelope.
- `TestPictures_ContentFailureHoldsTheWatermark`: 500 on content → error,
  cursor not saved (mirrors `TestWatermarkHoldsWhenAPageFails`).
- `TestPictures_MediaLinkHostIsReplacedByTheAPIHost`: `mediaReadLink` on
  `http://bcserver:7048/…` → the content request goes to the API host.
- `TestPictures_FilenameIsSafe`: `number` `A/B\C` → `A_B_C.jpg`; no number →
  `<record_id>.png`.
- ~~Contract: extend `TestContract_BusinessCentralEnvelopes` with a picture
  envelope.~~ **Changed while building:** that golden is replayed by the BC
  *producer*'s contract test (which would POST image bytes to BC), and its
  `payload` field must be JSON, so an image cannot go in it. The metadata keys
  are pinned by `TestPictures_OneEnvelopePerPicture` instead.
- Added: `TestPictures_PreviewExplainsThereIsNoRecordStructure`.

Mutations to run and paste into the PR: 404 treated as an error; `$select`
dropped; seen-map ignored; `publishStream` never chosen; filename not
sanitised.

## Risks

- **BC request budget.** Pictures mode costs up to two extra requests per
  record per poll. `$select`, the seen-map and `incremental` keep it small
  after the first poll; the first poll of 10k items is ~20k requests, and the
  consumer has no `429` handling today (pre-existing) — a throttled poll
  fails and retries next interval. Documented; a `max_pictures_per_poll`
  cap is a follow-up if needed.
- **Assumptions A/B** above — verified in TEST before merge is called done.
- **Entities without `number`/`displayName`** — the `$select` list is per
  entity and checked in TEST; a wrong field is a 400, not silent.
- **Image size.** Anything over 256 KB rides the claim-check (bucket TTL 1
  day, as for every large payload); a remote agent offline longer than that
  loses it, as documented for the agent.

## Verification

- Unit: the tests above, mutations, `gofmt`, `vet`, `golangci-lint`,
  `go test -race ./...`; UI `tsc`, `build`, `test:coverage` (the checkbox
  adds no tested surface, so coverage must not drop).
- Local: `make up-core`, a BC node in pictures mode against the fake BC
  server from the tests is not possible in the UI — so the local check is the
  Go tests, and the real check is TEST.
- **TEST/prod (Ludvik's BC trial, CRONUS has item pictures):** pipeline
  **BC items (pictures) → Remote Agent (outbox)**; `C:\VRSky\outbox\1896-S.jpg`
  and friends appear on the PC, open as images. Then replace one item's
  picture in BC: with `incremental: true` the next poll delivers only that one
  (assumption A), and the log shows the others skipped as unchanged
  (assumption B).

## Non-goals / follow-ups

Uploading pictures into BC (producer mode); persisting the seen-map; `429`
back-off in the BC consumer; item **variant** pictures (`itemVariant` has a
picture navigation but no documented `GET …/picture` route); extracting the
BC block from `PropertyEditor.tsx`.
