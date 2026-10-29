# Refuse a cloud-storage `move_prefix` inside `prefix` (#567)

Backlog item 130.

## Problem

The cloud-storage consumer lists `prefix` recursively. With
`after_action: move`, an object moved to `<move_prefix>/<base name>` that
the listing of `prefix` also returns is a new key at the next poll. It is
published again, and its second move copies it onto its own key and then
deletes it. Azure and GCS accept that copy, so the object is lost. S3
refuses it, so there the after-action fails and the object stays.

## How the listing matches keys

`objectstore.List` passes `prefix` to the provider unchanged (S3
`Prefix`, Azure `ListBlobsFlat` `Prefix`, GCS `Query.Prefix`). All three
match it as a plain string prefix, not as a folder. So `prefix: in`
returns `in-done/a.csv` as well as `in/a.csv`; `prefix: in/` returns only
the second. The check has to follow that, or it lets `in` + `in-done`
through and the loop stays.

A move writes `path.Join(move_prefix, path.Base(key))`. Every destination
is therefore `D + b`, where `D` is the cleaned `move_prefix` plus `/` (or
empty for the bucket root) and `b` is a base name: not empty, no `/`.
Some destination is listed when either:

1. `D` starts with `prefix` (this covers equal prefixes, a `move_prefix`
   under `prefix`, and an empty `prefix`), or
2. `prefix` starts with `D` and the rest of `prefix` has no `/`. Then a base
   name that starts with that rest is listed. Example: `prefix:
   data/report_`, `move_prefix: data`. `data/report_1.csv` moves onto its
   own key at once.

Leading slashes are ignored on both sides for the comparison. The listing
would not match `/in/` against `in/x`, but whether a provider keeps a
leading slash in a key differs, and refusing one more config is cheaper
than losing objects.

## Event mode

In event mode `prefix` is not applied (backlog 120): the bucket
notification decides which keys arrive, and the consumer cannot see it. A
moved object is a new object (S3 `ObjectCreated:Copy`, Azure
`BlobCreated`, GCS `OBJECT_FINALIZE`), so it notifies again if the
notification covers `move_prefix`. A start check on `prefix` there would
refuse safe configs (empty `prefix`, notification scoped elsewhere) and
pass unsafe ones. So the start check applies to poll mode only. For both
modes, `afterAction` refuses a move whose destination is the object's own
key: no copy, no delete, the object stays and the failure is logged. That
removes the data loss in event mode too.

## Acceptance criteria

- Poll mode: `start` refuses `after_action: move` when moved objects would
  be listed again (rules 1 and 2 above), logs an error that names
  `prefix` and `move_prefix` with their values, and starts no poller.
- `move_prefix` outside the listing keeps working: `in/` + `in-done/`,
  `in/` + `processed`, `incoming/a/` + `incoming` (move up a level).
- `in` (no slash) + `in-done/` is refused, because the listing of `in`
  returns `in-done/...`. The error says so.
- Event mode is not refused at start; see above.
- `afterAction` never copies an object onto its own key, in any mode.
- Table-driven tests that fail without the change.
- `docs/connectors/cloud-storage.md` says the start is refused. Backlog
  130 struck with #567.

## Files

- `src/cmd/cloud-storage-consumer/service.go`: `validateMove` on
  `cloudConfig`, called from `startConnection`; self-copy guard in
  `afterAction`.
- `src/cmd/cloud-storage-consumer/move_prefix_test.go`: new tests.
- `src/cmd/cloud-storage-consumer/consumer_test.go`:
  `TestCloudConsumer_AfterActionMove` used `prefix: in/` with
  `move_prefix: in/processed`, the config this change refuses. It moves to
  `processed` now.
- `docs/connectors/cloud-storage.md`, `plans/backlog.md`.

The UI editor does not validate `move_prefix` today, so it is left alone
(backlog 133).
