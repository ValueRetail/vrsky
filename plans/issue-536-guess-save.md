# A file-consumer start that couldn't read its checkpoint saves its guess anyway (#536)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Backlog item 102. When `loadKnown` can't read the checkpoint at a start, it
takes every file in the folder as done, with its stamp as it is now. Its
comment says that guess is never saved, but the first change saves it:
`saveKnown` sees `savedGen == ""` and writes a snapshot, and
`keepSavedStamps` reads nothing because every guessed name has a stamp. A
poll that only saw a deletion writes it with no read at all. The snapshot
then replaces what another pod saved:

- pod B published `x.csv` at stamp S and saved S; `x.csv` was replaced (S')
  before this pod (A) started. A's guess has S'. A's snapshot replaces S
  with S'. B's next `mergeSaved` finds S' saved and the file at S', so B
  takes it as done. S' is never published;
- a file that landed while A was stopped and B hasn't published yet is in
  A's guess as done. Once A saves it, B's `mergeSaved` takes it as done too,
  and it is never published by anyone.

## Approach

The guess stays in memory and the watcher keeps working with it, but it is
never written over a saved set this watcher hasn't read.

- `loadKnown`'s fallback records the names it guessed (`ac.guessed`) and
  marks the set dirty, so every poll tries to save.
- `saveSnapshot` starts with `settleGuess`: while there is a guess, it reads
  the saved set first. If the read fails, nothing is written and the set
  stays dirty, so the next poll tries again (the same rule
  `keepSavedStamps` follows since #500). If it works, each guessed name the
  watcher still has as done is corrected the way a start that could read
  would have done it:
  - not in the saved set: no longer done, so the next polls publish it
    after the stability check (it arrived while the connection was
    stopped);
  - saved with a stamp: takes that stamp, so a file replaced since is seen
    as replaced and published once (#420);
  - saved without a stamp (a row from before #420): keeps the guessed
    stamp, which is what `loadKnown` adopts too.
  With no checkpoint for the folder at all, the guess is the first-start
  rule and is kept as it is. The guess is then gone, and the snapshot is
  written from the corrected set.
- Every write goes through `saveSnapshot` while there is a guess: the
  changes row needs `savedGen`, which only a snapshot sets. So the busy
  poll, the deletion-only poll and the stop flush are all covered.
- A name the watcher publishes itself (it was replaced after the start)
  leaves the guess; that entry is real. If a stop cuts the publish off, it
  goes back into the guess with the rest of its old entry.

## Trade-off

The issue offers two ways: re-read before the first save, or don't save
until a read succeeds. This does both in one place: the first save is the
re-read, and it doesn't write until that read works.

The other option considered was to defer the whole baseline, as #409 does
for a deferred start: publish nothing until the checkpoint can be read.
That is safer for the files guessed as done but stops every new file
during a checkpoint outage. The fallback exists so new files keep flowing,
and it is kept.

What this costs:

- During a checkpoint outage nothing this watcher publishes is saved. A
  crash then a start that can read publishes those files again. Before
  this the same outage usually failed the save too; a duplicate beats a
  file never published.
- Files that arrived while stopped wait until the checkpoint can be read,
  as they did before (the fallback took them as done), but now they are
  published once it can. Before this they were never published.
- A saved row that can never be decoded stops every save of this watcher,
  logged at each poll. Before this the first change overwrote it with the
  guess. The deferred baseline (#409) and `keepSavedStamps` (#500) already
  wait for good on such a row; this does the same, and each start falls
  back to the guess again, so nothing published is published twice.
- One checkpoint read per poll while the store is failing, and two at the
  first save after it recovers when the stop flush or a stampless name is
  involved. The store is failing anyway; the read is the same size as the
  snapshot.

## Acceptance criteria

1. A start whose checkpoint read fails writes nothing while reads keep
   failing: not after a publish, not after a deletion-only poll, not at the
   stop.
2. Two watchers: B published `x.csv` at S and saved it, `x.csv` is replaced,
   A starts with a failed read, then reads work again and A saves. S' is
   published exactly once, by A or B, and the saved stamp for `x.csv` is
   never S' before S' is published. Fails without the fix.
3. A file that landed while stopped, with a saved set that lacks it, is
   published exactly once after reads come back (by A, or by B if B gets
   there first). Fails without the fix.
4. With no checkpoint for the folder (first start) and a failed read, the
   first save that can read keeps the first-start rule: nothing present
   at the start is published.
5. A file this watcher published after the start stays done after the
   guess is settled; nothing it published is published again.
6. Against Postgres (`src/pkg/testdb`): the two-pod case of 2.
7. Mutation run: writing the snapshot without the settle, ignoring a failed
   settle read, and keeping guessed names that the saved set lacks each
   make a test fail.
8. From `src/`: build, vet, golangci-lint, `go test -race ./...`,
   `make lint-tenant`; the package with `-count=10 -race`.
9. Backlog item 102 struck with `(#536)`.

## Files

- `src/cmd/file-consumer/known.go` (`loadKnown`, `settleGuess`,
  `saveSnapshot`)
- `src/cmd/file-consumer/service.go` (`ActiveConnection.guessed`,
  `pollOnce` publish path)
- tests in `src/cmd/file-consumer`
- `plans/backlog.md`

## Decisions

- Correct the guess from the saved set instead of only merging into it. A
  merge adds names; the harm here is names and stamps the guess has that
  the saved set doesn't.
- The fix sits in `saveSnapshot`, the one place every write goes through
  while there is a guess, next to `keepSavedStamps`.
- `TestKnown_FailedReadDoesNotOverwriteSavedSet` asserted that the publish
  after a failed start read writes a snapshot while reads still fail. That
  is the bug, so it now asserts no write, then the write once reads work.
- `docs/connectors/file.md` doesn't describe the fallback; no docs change.

## Risks

- Two watchers of this version still have the read-then-write window #426
  lists: between the settle read and the snapshot write, the other can
  save.
- A watcher that never manages a read never saves. See the trade-off.
