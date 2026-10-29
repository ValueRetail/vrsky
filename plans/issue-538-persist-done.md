# The SFTP and cloud-storage consumers keep their done set across restarts (#538)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

In poll mode, the SFTP and cloud-storage consumers remember which files and
objects they already published in `connection_node_checkpoints`, per tenant,
connection and node. A restart, deploy or park/start no longer publishes
everything still in the folder or bucket again (`after_action: none`). A file
or object replaced under the same name is published once more; one deleted
and added again is published again. Backlog item 92.

## Constraints

- No migration, no change to `pkg/checkpoint` or its API.
- Stay out of `src/cmd/file-consumer`, `ui/` and `docs/operator` (other
  workers).
- Keep #479 (`dropActive`), #487/#492 (StartGuard) and #520 (tenant-checked
  stops) as they are: the done set lives inside the poller goroutine and does
  not touch the start, stop or status paths.
- Event mode of the cloud-storage consumer is unchanged: the queue's ack is
  its done set.

## Approach

A new package `src/pkg/doneset`, used by both consumers. It is the file
consumer's #426 design (`src/cmd/file-consumer/known.go`) without the parts
that exist only for that consumer's history: no rollback format for a
previous image (nothing older reads these rows), no name-only entries (every
listing carries a size and mtime), no stability check.

- The node's checkpoint row is the snapshot: `scope` (where the names live),
  `gen` (a random id per snapshot) and `files` (name to size/mtime stamp).
- A second row, node id `<node>#changes`, holds what changed since that
  snapshot: `base` (the snapshot's `gen`), `files` (done names with their
  stamp) and `gone` (names no longer done). It is rewritten whole on each
  save and stays small. Past `min(512, len(set)/2)` changed names, or at the
  first save after a start, the save writes a new snapshot and an empty
  changes row on it.
- Loading reads the snapshot and applies the changes row only when its `base`
  is the snapshot's `gen`. A row for another scope (the remote dir, host,
  bucket or prefix was edited) is not used: the start counts as a first one.
- Before the first publish of each poll, `Refresh` re-reads what another
  poller of the same connection may have saved (the old pod in a rolling
  deploy, or the old poller finishing its poll after a quick stop and start).
  While the changes row is on this poller's own snapshot only that small row
  is read. Otherwise the whole set is read and merged: a saved name counts
  only while the listing still shows the stamp it was saved with.
- Each save checks the same thing first. If the saved set is not this
  poller's (no snapshot of its own yet, or another poller wrote one since),
  it merges what is saved and writes a snapshot. The file consumer writes
  its first snapshot after a start without that merge; the test author's
  probe showed a save from the other poller in between was lost, so this
  one merges before every snapshot it does not own.
- A name missing from a listing leaves the set (`gone`), so a file deleted
  and added again is published again, and with `delete`/`move` the set stays
  near empty.

Per poll in both consumers:

1. List. On a list error, skip the poll as before.
2. If the saved set has not been read yet (start, or a read that failed),
   read it. If that fails, publish nothing this poll, log an error, and try
   again next poll. A one-shot connection (interval 0) ends with status
   `error`.
3. Prune names missing from the listing.
4. For each matching name not done with its current stamp: before the first
   such publish, `Refresh`; then publish, mark it done with the listed stamp,
   run the after-action. Save every 32 publishes, so a crash in a long poll
   repeats at most that many.
5. At the end of the poll (also when a stop cut it short) save, on a context
   detached from the poller's.

Stamps: SFTP gets the file's mtime from the listing (`remoteFile` gains
`ModTime`; `pkg/sftp` already returns it). Cloud storage uses
`objectstore.Object.LastModified`, which all three backends fill. Size plus
mtime, not ETag: `objectstore.Object` has no ETag and adding one changes a
shared type for three backends.

## Acceptance criteria

1. Restart (stop then start, and a new consumer on the same store) does not
   publish again what was published before, `after_action: none`, both
   consumers.
2. A file or object replaced under the same name (size or mtime changed) is
   published exactly once more, including across a restart.
3. One deleted and added again is published again.
4. Two tenants with the same connection id and node id have separate sets:
   one tenant's saved set never suppresses the other's publish.
5. A failed checkpoint read at start publishes nothing, logs an error, and
   the next poll whose read works publishes only what is not done. One-shot
   ends with `error`.
6. A first start (no row, or a row for another scope) publishes everything
   in the folder or bucket, as before.
7. Against Postgres (`src/pkg/testdb`): restart through `PostgresStore`.
8. Mutation run on the done check, the prune, the scope check, the base
   check, the read-failure gate and the tenant key.
9. From `src/`: build, vet, golangci-lint, `go test -race ./...`,
   `make lint-tenant`.
10. Docs for both connectors say what is published again and when. Backlog
    item 92 struck with `(#538)`.

## Files

- `src/pkg/doneset/` (new): `doneset.go`, tests
- `src/cmd/sftp-consumer/service.go`, `sftp.go`, tests
- `src/cmd/cloud-storage-consumer/service.go`, tests
- `docs/connectors/sftp.md`, `docs/connectors/cloud-storage.md`
- `plans/backlog.md`

## Risks

- The first start after this deploys has no saved set, so it publishes what
  is in the folder or bucket once more, the same as every restart did until
  now. After that, restarts are quiet.
- SFTP has no stability check. A file still being uploaded when a poll lists
  it was published half-written once and then never again; now the next poll
  sees a new size and publishes the finished file too. More messages for slow
  uploads, but the complete file arrives. A stability check like the file
  consumer's (#374) is backlog item 107.
- Size plus mtime misses a replacement with the same size inside the same
  mtime tick (one second on most SFTP servers and on S3).
- A rolling deploy still overlaps two pods for a few seconds. With `none`
  the saved set covers most of it (each poll re-reads before publishing, and
  saves every 32 publishes). With `delete`/`move` both pods can still list
  and publish the same file before either removes it. `strategy: Recreate`
  for both Deployments closes that; it is a manifest change, so it is a
  backlog item for a human-gated PR rather than part of this one (item 106).
- Two pollers at once: a save landing between one poller's merge and its
  snapshot write is still lost (no compare-and-swap in `pkg/checkpoint`).
  Worse, found in review: if the other poller's snapshot lands between this
  one's ownership check and its changes write, or the other poller writes a
  snapshot and its changes reset fails, this poller keeps writing changes on
  a snapshot that is no longer current, and readers ignore them until its
  next snapshot. A restart in that stretch publishes those files again.
  Duplicates only, needs a millisecond window during an overlap, and the
  file consumer has the same hole. Backlog 109 bounds it for both.
- After-action failures: a file whose `delete` or `move` failed is done, so
  it stays in the folder and is not tried again, also across restarts.
  Before, a restart published it again and retried the action. Documented;
  retrying the action for done names is backlog 110.
- Two rows per node instead of none. `ON DELETE CASCADE` on `connections`
  removes them with the connection.

## Decisions

- A shared package rather than two copies of the logic: the two consumers
  need the same thing, and `file-consumer` is out of bounds for this change.
- A checkpoint read failure blocks publishing until the read works, rather
  than falling back to "nothing is done" (republishes everything) or
  "everything present is done" (loses what arrived meanwhile).
- A `Refresh` failure mid-run does not block: this poller's own set is
  complete in memory, so the worst case is publishing a file another poller
  just published. It is logged.
- Save every 32 publishes plus at the end of each poll. Saving after each
  publish rewrites the changes row per file; only at the end means a crash
  in a long first poll republishes all of it.
- First-start behaviour unchanged: everything present is published.
- Recreate is decided as "yes, in a separate human-gated PR" (backlog 106),
  as the issue asked to decide it after the done set lands.
- Existing harness tests that run on a sqlmock database inject an in-memory
  checkpoint store (`memstore_test.go`); Configure would otherwise point the
  done set at Postgres on the mock. Their assertions are unchanged.
- The connector pages name fields the code doesn't read (`sftp.path`,
  `event_mode`). Left for backlog 108 rather than fixed here.
