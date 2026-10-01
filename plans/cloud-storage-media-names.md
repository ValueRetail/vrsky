# Cloud-storage output: pictures keep their names; doc drift from the node pool swap

## Open questions

None.

## Goal

1. A picture that reaches a **Cloud Storage** output lands as
   `<prefix>/<number>.<ext>` (e.g. `catalogue/1896-S.jpg`), the same name the
   file and remote-agent outputs give it (#281), instead of a bare uuid.
2. The two places that still describe the old node pool say what runs now.

## What is wrong (checked 2026-10-01)

- `cmd/cloud-storage-producer/service.go` `upload` → `renderKey` always
  renders the key template. For a picture the payload is not JSON, so the
  template sees only `{timestamp, uuid}`; the default template is
  `{{.uuid}}` and a record template like `orders/{{.id}}_{{.timestamp}}.json`
  fails on the missing field and falls back to the uuid with a warning. Either
  way the picture's `Metadata["filename"]` (`1896-S.jpg`) is ignored, so a BC
  (pictures on) → Azure Blob pipeline cannot be matched by Bifrost's rule.
- `docs/scalability.md:89` and `infrastructure/azure/deploy-azure.sh:76` say
  `2× E4bds_v5`; since today it is `fpool` = `2× E4ds_v6`, managed OS disks,
  after Norway East ran out of E4bds_v5 capacity.
- The "run as administrator" note for the installer is already in
  `docs/operator/remote-agent.md` (troubleshooting table) — nothing to do.

## Approach

### Cloud-storage producer

In `upload`, before the template:

```go
// A media file that carries a name keeps it, template or not — the same rule
// as file-producer and the remote agent (#281): the template names the
// records, the pictures beside them are matched by their own names.
if name := mediaName(env); name != "" {
    key = joinPrefix(cfg.Prefix, name)
} else {
    key, err = p.renderKey(...)   // unchanged, incl. fallback
}
```

- `mediaName` = `Metadata["filename"]` when non-empty and
  `envelope.IsMedia(env.ContentType)`; `path.Base` + the same
  `path.Clean` clamp `renderKey` applies, so `../x.jpg` cannot escape the
  prefix. Empty after cleaning → treated as no name (template path).
- `prefix` still applies, so the operator's "folder" is honoured; the
  template is not (it is the records' name).
- `renderKey` itself is untouched; the prefix/clamp code moves into a small
  `joinPrefix` helper both paths use.

### Docs

- `docs/scalability.md`: `2× E4ds_v6 (fpool, managed OS disks; the earlier
  E4bds_v5 pool hit a Norway East capacity shortage on 2026-10-01)`.
- `deploy-azure.sh` comment: same one-liner.
- `docs/connectors/cloud-storage.md`: one bullet under the key template:
  pictures and other media keep their filename under the prefix.
- `docs/connectors/business-central.md:90`: "file, remote-agent **and
  cloud-storage** destinations".

## Files

| File | Change |
|---|---|
| `src/cmd/cloud-storage-producer/service.go` | `mediaName`, `joinPrefix`, branch in `upload` |
| `src/cmd/cloud-storage-producer/producer_test.go` | tests below |
| `docs/connectors/cloud-storage.md`, `docs/connectors/business-central.md` | naming rule |
| `docs/scalability.md`, `infrastructure/azure/deploy-azure.sh` | node pool |

No UI, schema or deploy changes.

## Tests

`producer_test.go`, with the existing fake store:

- `TestCloudProducer_MediaKeepsFilename` — `image/jpeg` + `filename:
  "1896-S.jpg"` + template `orders/{{.id}}.json` + prefix `catalogue` →
  key `catalogue/1896-S.jpg`, no warning fallback.
  *Mutation: drop the `IsMedia` branch → key is the uuid → fails.*
- `TestCloudProducer_MediaFilenameCannotEscapePrefix` — `filename:
  "../../x.jpg"` → `catalogue/x.jpg`.
- `TestCloudProducer_NonMediaIgnoresFilename` — `application/json` with a
  `filename` → template still wins (today's behaviour pinned).
- Streamed path (`DeliverStream`) covered by making the first test run
  through both `Deliver` and `DeliverStream`.

Then `gofmt`, `go vet`, `golangci-lint`, `go test -race ./cmd/cloud-storage-producer/...`.

## Risks

- A tenant that *wanted* media under the template (unlikely: a uuid) loses
  that; the file and remote-agent outputs already behave this way, so this is
  the consistent choice.
- Two pictures with the same name overwrite each other in the bucket — same
  as a folder, and BC names are unique per item.

## Rollout

`az acr build` cloud-storage-producer only; `kubectl rollout restart
deploy/vrsky-cloud-storage-producer` (imagePullPolicy Always; pipelines
ending in it need a redeploy). No prod pipeline uses it today, so this can
wait for the next connector rollout.
