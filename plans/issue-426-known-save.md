# The file consumer's saved done set is rewritten and re-read in full on every busy poll (#426)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

A busy poll (one that publishes, or sees a file deleted) writes and reads
only what changed, not the whole done set. Before this, `saveKnown` wrote
every name and stamp as one jsonb value in `connection_node_checkpoints.state`
and `mergeSaved` read it all back before the poll's first publish. With the
stamps from #420 that is about 11.5 MB of JSON per 100 000 names each way,
every 5 s while files arrive (measured below).

## Constraints

- The checkpoint row keeps the format #465 shipped: `watch_dir`, `known`
  (every done name) and `files` (the stamps). The previous image reads only
  that row, so after a rollback it must hold the whole set.
- Two watchers for one connection (the old one finishing a poll after a stop
  and a quick start, or two pods) keep working through `mergeSaved`.
- No migration, no change to `pkg/checkpoint` or the SDK.

## Options considered

1. Save only when the set changed. Already the case since #391 (`dirty`):
   idle polls write nothing. A busy poll changes the set, so this alone does
   not help.
2. Store the changes (chosen). See below.
3. A row per name in a new table. Fixes it too, but needs a migration, makes
   the PR foundational and `agent:needs-human`, and still has to keep writing
   the full row for the previous image. More than this issue needs.

Not chosen either: saving less often (every minute, say). It widens the
window in which a crash republishes files from one poll to a minute.

A server-side jsonb patch of the one row (`jsonb_set`, `||`, `-`) was tried
in psql on 100 000 names: the client sends a few hundred bytes, but Postgres
still rewrites the whole TOASTed value (1.6 MB of WAL, 210 ms, against
3.1 MB and 144 ms for the full rewrite). Not worth it.

## Approach

- The row stays the full snapshot and gains a `gen` field, a random id
  written with every snapshot. The previous image ignores the field when it
  reads and drops it when it writes.
- A second checkpoint row for the same node, node id `<node>#changes`, holds
  the changes since that snapshot: `base` (the snapshot's `gen`), done names
  with their stamps (`files`), done names without one (`known`), and names
  no longer done (`gone`). It is rewritten whole on each busy poll, and stays
  small.
- The watcher keeps the names it touched since its snapshot (`changed`,
  filled by `touch`). `saveKnown` writes the changes row while that list is
  at most `min(512, len(set)/2)` names, and a new snapshot (then an empty
  changes row on the new `gen`) otherwise, when the watcher has no snapshot
  of its own yet (the first save after each start), or after `mergeSaved`
  found another writer's data.
- Loading reads the snapshot and applies the changes row only when its
  `base` is the snapshot's `gen`. A changes row on any other base is stale and
  is ignored, so it is never applied to the wrong snapshot. The snapshot is
  written before its empty changes row, so a crash between the two leaves a
  stale changes row next to a complete snapshot.
- `mergeSaved` reads only the changes row while its `base` is the snapshot
  this watcher wrote last (`ownsSaved`): only that watcher writes changes on
  its own snapshot, so nobody else saved since. Anything else means someone
  else may have: it reads both rows, as before, and the next save is a
  snapshot.
- `saveKnown` checks `ownsSaved` before every changes-row write too. If
  another watcher replaced the snapshot since, changes on this one's would
  be ignored by readers, and the row would fool this watcher's later
  re-reads. It merges what the other one saved and writes a snapshot instead
  (found in review: a deletion-only save, which has no re-read before it,
  hit this). One more read of the small row per busy poll.
- A start that read part of its set from a changes row (the last watcher
  crashed) counts as dirty, so its first save is a snapshot even if nothing
  else changed. Otherwise a crash, a quiet run and a clean stop left the set
  split across the two rows, and a rollback after that republished up to 512
  files (found in review).
- A watcher that stops writes a snapshot if it has changes not in one, so the
  previous image finds the whole set after a rollback. It runs `mergeSaved`
  first, and writes nothing if that read fails: written blind, the snapshot
  dropped a name another watcher had published and saved since (found by the
  test author, `TestKnownChanges_StopFlushKeepsWhatAnotherWatcherSaved`).

## Acceptance criteria

1. A busy poll on a large set writes only the changes row until the changes
   pass the limit; bytes written and read per busy poll drop by orders of
   magnitude (measured, 100 000 names).
2. `mergeSaved` on a busy poll reads only the changes row while nobody else
   wrote a snapshot.
3. A crash (no stop) followed by a start finds every name saved before it:
   snapshot plus changes. Nothing published before the last save is
   published again.
4. A stopped watcher leaves a snapshot row that holds the whole set in the
   #465 format (`known` plus `files`), decodable by the previous image's
   struct; the previous image's load logic, run on it, publishes nothing
   twice.
5. A snapshot without `gen` (written by the previous image) loads, and a
   changes row next to it is ignored; likewise a changes row on another
   `gen`.
6. Two watchers on one connection still skip what the other published, with
   changes rows in play.
7. Past the limit a save writes a snapshot, and the changes row starts empty
   on it.
8. Against Postgres (`src/pkg/testdb`): restart and the rollback read.
9. Mutation run on the base check, the compaction rule, the stop snapshot
   and the cheap path of `mergeSaved`.
10. From `src/`: build, vet, golangci-lint, `go test -race ./...`,
    `make lint-tenant`; the package with `-count=10 -race`.
11. Backlog item 41 struck with `(#426)`.

## Files

- `src/cmd/file-consumer/known.go`, `service.go`
- tests in `src/cmd/file-consumer`: new ones, and the existing helpers that
  read the stored set (`dirRig.saved`, `savedNames`, `expectCheckpoints`, the
  Postgres row count, `failingReadStore`) taught about the second row
- `src/cmd/file-consumer/known_measure_test.go`: the measurement, skipped
  unless `VRSKY_MEASURE_KNOWN` is set
- `docs/connectors/file.md`, `docs/operator/upgrade.md`, `plans/backlog.md`

## Measurements

Synthetic folder of 100 000 files (`order-2026-10-28-NNNNNNN.csv`), local
Postgres 16 in Docker, `TestMeasureKnownBusyPoll`; one new file published per
busy poll, mean of 10 busy polls.

| | before | after |
|---|---|---|
| bytes written per busy poll | 11 488 827 (1 save) | 574 (1 save) |
| bytes read per busy poll | 12 288 783 (1 read) | 1 083 (2 reads) |
| time in the checkpoint store per busy poll | 280 ms | 5.1 ms |
| whole busy poll (includes stat of 100 000 files) | 804 ms | 403 ms |
| stop | nothing written | one snapshot, 11.5 MB, 294 ms |

The changes row grows by roughly 70 bytes per changed name until the next
snapshot, so at most about 40 KB just before one. A snapshot every 512
changes costs about 22 KB per change at 100 000 names, spread out.

## Risks

- The previous image running at the same time as this one (not a rollback,
  which stops one before the other starts) sees only the snapshot, so it can
  miss names this one saved as changes since and publish them again. The
  other way round too: while this one's changes row is on its own snapshot it
  only reads that row, so it doesn't see a snapshot the previous image
  rewrote meanwhile. Since #425 the Deployment is `Recreate`, so this overlap
  no longer happens on a deploy.
- A crash, then a rollback to the previous image: that image misses the
  changes since the last snapshot (at most 512 names) and publishes them
  again. Noted in `docs/operator/upgrade.md`.
- Two watchers of this version at once: between `ownsSaved` and the write
  that follows it, the other one can still write. That is the same
  read-then-write window the single row always had (last writer wins).
- Two rows per file node instead of one. `DeleteForConnection` and the
  `ON DELETE CASCADE` on `connections` remove both.

## Decisions

- Option 2, as above. No migration, so the PR is not foundational.
- The changes row lives in the same table under node id `<node>#changes`. A
  node id too long for the suffix (over 247 characters) only ever gets
  snapshots.
- The limit is 512 names: near the square root of twice the set size at
  100 000 names, which keeps the average bytes per busy poll lowest there.
- Each start writes one snapshot at its first save rather than adopting the
  saved `gen`, so a watcher only ever writes changes on its own snapshot.
- Existing tests that read the stored set directly were taught about the
  changes row (same assertions, both rows), and the Postgres test now
  expects two rows per connection.
