# BC pictures: tell the till when a picture was removed

## Open questions

1. **Incremental polls only carry items BC marked as modified.** Whether BC
   bumps an item's `lastModifiedDateTime` when its picture is removed (or
   replaced) is still unverified — it was flagged as such when pictures
   shipped (#281). If it does not, a deletion is invisible to an incremental
   poll and the marker could only go out on a full fetch / "Resend
   everything". The plan below therefore adds a **sweep**: after the feed,
   every item we remember a picture for that was *not* in this poll gets one
   cheap metadata request (`…/picture`, no content). That is one request per
   remembered picture per poll — fine for a till catalogue (CRONUS ≈ 80
   pictures), not for 50 000 items. **Recommended: include the sweep,
   incremental mode only.** Alternative: ship without it and accept that
   deletions surface only on a full fetch. Decide before build.

## Goal (Bifrost's request, 2026-10-01)

When an item keeps existing in BC but its picture is removed, the pipeline
delivers one **empty** file `<item number>.no-picture` into the agent folder,
once. Bifrost already understands the marker (removes the thumbnail; a marker
for an item without a picture is ignored). Items that leave the feed need no
marker. CSV and picture files are unchanged.

## How pictures work today (what this has to fit)

- `publishPictures` (`cmd/business-central-consumer/pictures.go`) runs per
  page: `GET …/items(id)/picture`; 404 or empty `contentType` = "no picture";
  otherwise download and publish unless `cfg.pictureSeen[rec.ID] == pic.ID`.
- `pictureSeen` is **in memory per poller**: lost on restart and on redeploy,
  so every picture is re-sent after either. "Resend everything" clears it.
- The picture file name comes from `pictureFilename(rec, ct)` =
  sanitised `rec.Number` + extension (falls back to the id).
- The name survives the pipeline because `envelope.IsMedia(contentType)` is
  true: converter/filter pass the message through, and the remote-agent,
  file and cloud-storage outputs keep `Metadata["filename"]`.
- The remote agent writes `~vrsky-<id>.part` + rename, verifying a sha256;
  an empty body is a valid body (checksum of "").
- Watermark persistence: `checkpoint.Store` row per
  (tenant, connection, node) with `last_processed_message_id`; no free-form
  column.

## Design

### 1. Remember pictures in the checkpoint row

- Migration `000025_checkpoint_state`: `ALTER TABLE connection_node_checkpoints
  ADD COLUMN state JSONB NOT NULL DEFAULT '{}'`.
- `checkpoint.Checkpoint` gains `State json.RawMessage`; `Save` writes it,
  `Get` reads it (in-memory store too).
- The BC poller keeps `pictureSeen` as today but **loads it from
  `State.pictures`** at the start of a fetch (when empty) and **saves it after
  every successful fetch**, with the cursor when incremental and alone
  otherwise. Side effect worth having: a restart or redeploy no longer
  re-sends every picture. "Resend everything" still clears the map first.
- Checkpoint key is the node, so two BC nodes in one pipeline stay apart.

### 2. Detect the removal

In `publishPictures`, the two "no picture" branches (404, empty
`contentType`) become: if `pictureSeen[rec.ID]` is set → publish a marker,
delete the entry, count `removed`; else count `none` as now. One marker per
deletion follows from deleting the entry; an item that never had a picture
never has an entry.

### 3. The sweep (incremental mode only — open question 1)

After the last page, for every `rec.ID` in `pictureSeen` that was not in this
poll's feed: `GET …/items(id)/picture` (metadata only). 404 / empty →
marker, delete entry. A different `pic.ID` → the picture was replaced
without the item being modified: download and send it (the same gap). A
request failure holds the watermark like a picture failure does today. The
record number for the marker name comes from the stored state
(`State.pictures[id] = {picture_id, number}`), since the record itself is not
in the feed.

### 4. The marker message

- `envelope.ContentType = envelope.NoPictureContentType`
  (`application/vnd.vrsky.no-picture`), empty payload, `PayloadSize 0`.
- `Metadata`: `filename: <number>.no-picture` (same `sanitizeFilename` +
  `ValidFilename` path as the picture, so the number is byte-identical to
  the picture file and the CSV's `article_no`), `record_id`, `number`,
  `entity`, `marker: "no-picture"`.
- `envelope.IsMedia` returns true for the marker type, with a comment: it is
  a file carried as-is. That single change makes converter and filter pass
  it through and the three file outputs keep its name — no per-output edits.
- Bifrost's name rule (`[A-Za-z0-9._-]`, ≤ 35 chars): `sanitizeFilename`
  replaces more than that only for exotic numbers; CRONUS numbers are plain.
  The marker inherits whatever the picture got, which is the contract.

### 5. Logging / events

Poll summary gains `pictures_removed`; the builder's BC panel line already
shows sent/none/unchanged counts — add removed.

## Files

| File | Change |
|---|---|
| `infrastructure/migrations/000025_checkpoint_state.{up,down}.sql` | `state JSONB` |
| `src/pkg/checkpoint/store.go` (+test) | `State` on the struct, in `Save`/`Get`, in-memory store |
| `src/pkg/envelope/media.go` (+test) | `NoPictureContentType`, `IsMedia` includes it |
| `src/cmd/business-central-consumer/pictures.go` (+test) | marker publish, removal branches, sweep, `markerFilename`, state load/save |
| `src/cmd/business-central-consumer/service.go` | `pictureCounts.removed`, state save after fetch, summary log |
| `src/pkg/agent/writer_test.go` | empty body is written and renamed (0-byte file, checksum of "") |
| `docs/connectors/business-central.md` | "Removed pictures" paragraph |

Not changed: CSV, picture files, remote-agent, converter, filter, UI.

## Tests

`pictures_test.go` on the fake BC:
- `TestPictures_RemovedPictureSendsOneMarker` — poll 1: picture → `.jpg`;
  poll 2: fake answers empty → exactly one envelope, content type marker,
  `filename HBB-1000.no-picture`, empty payload; poll 3: nothing.
- `TestPictures_NeverHadAPictureSendsNoMarker`.
- `TestPictures_PictureBackAfterMarker` — poll 4: picture again → `.jpg`.
- `TestPictures_RemovalSeenBySweepWhenItemNotInFeed` — incremental, item
  absent from the feed, picture removed → marker (and a replaced picture →
  resent). *Mutation: skip the sweep → fails.*
- `TestPictures_SeenPicturesSurviveRestart` — new consumer on the same
  in-memory store sends nothing for an unchanged picture. *Mutation: don't
  load state → re-sends → fails.*
- `TestPictures_MarkerNameMatchesPictureName` — number with a space and a
  slash: both files share the sanitised base.
- `TestResend_*` still pass (resend clears the map; no markers on resend).

`pkg/envelope`: `TestIsMedia_NoPictureMarker`. `pkg/checkpoint`: state round
trip, missing column default. `pkg/agent`: empty body write.

Then `gofmt`, `go vet`, `golangci-lint`, `lint-tenant`, `go test -race ./...`.

## Acceptance (prod, Ludvik, after deploy)

1. Remove the picture from CRONUS item HBB-1000 in BC. Next poll: exactly
   one `HBB-1000.no-picture` (0 bytes) in `catalogue-in`; the poll after:
   nothing new.
2. Add it back: `HBB-1000.jpg` arrives.
3. Restart the BC consumer: no picture storm on the next poll (state is
   persisted now).

## Rollout

Migration runs on management-api start (`build-push-acr.sh core` →
`deploy-core-azure.sh management-api`), then rebuild + `rollout restart`
the BC consumer and redeploy the BC pipelines in the builder.

## Risks

- **Sweep cost** scales with remembered pictures (one GET each per poll).
  Logged per poll; a cap/interval (`picture_sweep_every: n` polls) is the
  follow-up if a large catalogue appears.
- **Pictures mode on an item entity without pictures**: unchanged.
- A marker for an item that later leaves the feed is harmless (Bifrost
  ignores unknown items).
- `IsMedia` now covers a non-media type; the name says "carried as-is",
  which is what every caller means by it.
