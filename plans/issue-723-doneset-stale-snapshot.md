# A done set's changes can be written on a snapshot nobody reads (#723)

Open questions: none.

## Goal

Backlog item 109. A poller that wrote a snapshot writes later saves as small
changes rows on it, after `ownsSaved` has read the changes row and seen its own
snapshot's Gen as Base. That read says nothing about the snapshot row. Two ways
it goes wrong:

- Another poller's snapshot and changes reset land between the read and this
  poller's write. This poller's write overwrites the other's reset, so the row
  says "mine" while the snapshot is the other's.
- Another poller writes a snapshot and its changes reset fails. The changes row
  is still on this poller's snapshot.

Either way every reader ignores this poller's changes, and this poller keeps
believing it owns the save. A restart publishes those names again.

## Decision

Compare the snapshot's Gen, but not at every save: reading the snapshot is the
read #426 removed from the busy path.

`ownsSaved` stays as it is while the changes row is on our Gen. Once
`verifyEvery` (8) changes-only saves have been written since the last check, or
`verifyAfter` (5 minutes) has passed with at least one such save unchecked, it
also reads the snapshot row and compares its Gen with ours. A different Gen, or
a missing or undecodable row, means not ours: the caller merges and writes a
snapshot, as it already does for a changes row on another Gen.

Considered:

- Force a snapshot after N changes-only saves. Needs no extra read, but
  rewrites the whole set every N saves on a large folder, which is the cost
  #426 removed, and it still overwrites a snapshot another poller wrote
  without looking at it first.
- Verify after every changes write. Closes the window completely, but reads
  the large row at every busy poll.
- A compare-and-swap on the rows. The correct fix, but `checkpoint.Store` has
  none and it is a schema and interface change.

The check is correct under concurrent pollers because the Gen is a random id
no two snapshots share; equal means nobody wrote a snapshot since we did. The
window between the check and the next write remains, as it does for the
changes-row check; the check after it closes it within the bound.

The time bound matters on a quiet folder, where there may be no further save
for days. `Save` (doneset) and `saveKnown` (file consumer) run once per poll
and used to return at once when nothing was dirty. They now run the check when
it is due and, if the snapshot is not ours, mark the set dirty and write a
snapshot. A quiet poll costs nothing until a changes-only save is unchecked and
the time is up.

Bound: a poller writes changes on a snapshot nobody reads for at most 8
changes-only saves or 5 minutes after the stale one, whichever comes first
(the file consumer, whose polls also make the check when idle, recovers at
the poll after the 8th save). The snapshot it then writes holds everything it
published, so a restart does not publish those names again. Names another
poller published in the window are the same duplicate-publish exposure the
race always had; the bound limits it.

## Affected files

- `src/pkg/doneset/doneset.go`: counters, `verifyDue`, snapshot compare in
  `ownsSaved`, idle check in `Save`.
- `src/cmd/file-consumer/known.go`, `service.go`: the same for `ActiveConnection`
  (the file consumer has its own copy of the design; folding it into
  `pkg/doneset` is not trivial because of the `Known`/`Files` formats shared
  with the previous image).
- sftp-consumer and cloud-storage-consumer use `pkg/doneset` and need no change.
- Docs: `docs/connectors/{sftp,cloud-storage,file}.md`; `plans/backlog.md` item 109.

## Risks

- Two pollers that both go stale and both recover in the same instant can
  still overwrite each other: without a compare-and-swap on the rows the check
  narrows the window, it does not close it. The next check after it catches
  the loser.
- One more read of the snapshot row every 8 changes-only saves per poller.
- `Save` can now read when nothing is dirty. A failed read is returned (doneset)
  or ignored until the next poll (file consumer); nothing is written.
- Row formats unchanged.

## Verification

- `pkg/doneset`, unit (in-memory store with a hook that runs the other poller
  between this poller's read and write): the overwritten reset, a failed reset,
  a quiet folder by `Refresh` and by `Save`, and that a healthy poller reads the
  snapshot at most once per 8 saves.
- `pkg/doneset`, Postgres: the overwritten reset against
  `connection_node_checkpoints`, then a restart finds every name.
- `cmd/file-consumer`: the same three scenarios with two watchers, and a fresh
  watcher on the rows publishes nothing.
- Mutations: bound 8 to 9 and 800, time bound 5 to 4 and 6 minutes, and the Gen
  compare removed, each fails a test.
- The standard Go checks, `make lint-connector`, `make lint-tenant`.
