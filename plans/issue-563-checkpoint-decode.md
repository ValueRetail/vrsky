# A file-consumer checkpoint row that can't be decoded is never replaced (#563)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Backlog item "A file-consumer checkpoint row that can't be decoded is never
replaced". Since #536 a start that couldn't read its checkpoint saves nothing
until a read works. `loadSaved` returns the same kind of error for a store
failure and for a row it can't decode, and every caller treats both alike.
For a row that fails to decode, no read ever works:

- a start falls back to its guess (#536), and `settleGuess` fails at every
  save, so nothing is written and `saveSnapshotOrLog` logs an Error every
  poll; every later start guesses again;
- a deferred baseline (#409) stays deferred for good: nothing is published;
- `keepSavedStamps` (#500) blocks every snapshot while a done name has no
  stamp.

Postgres keeps the state in a `jsonb` column, so such a row is valid JSON of
the wrong shape: a value of another type in a field, an array, a string. It
can come from a hand edit, a bug, or a future version's format after a
rollback. Nobody can read it, so it carries nothing to keep.

## Approach

- `loadSaved` and `loadChanges` wrap a decode failure in a new sentinel,
  `errSavedUndecodable`. A store error stays as it is.
- One helper, `undecodableAsNone`, turns that sentinel into "no checkpoint":
  the caller sees no error and found=false. It logs a Warn the first time
  per watcher (Debug after that), and sets the watcher up so its next save
  writes a full snapshot (`savedGen = ""`, dirty). Any other error comes
  back unchanged.
- It is used where the saved set is read to decide or before writing over
  it:
  - `loadKnown`, both at the start and for the deferred baseline (#409):
    the first-start rule, so files in the folder now count as done and
    nothing is published for them; the start's save (or the baseline's)
    replaces the row;
  - `settleGuess` (#536): the guess is the first-start rule and stays, and
    the snapshot is written;
  - `keepSavedStamps` (#500): no saved stamps to keep, the snapshot is
    written;
  - `mergeSaved` and `savedStampFor`: nothing another watcher saved can be
    read, so there is nothing to merge and no saved stamp. Without this
    `mergeSaved` returned false, and `flushKnown` wrote nothing at the stop.
  - `mergeSaved`'s `ownsSaved` read: an undecodable changes row is not this
    watcher's.
- An undecodable changes row on a decodable snapshot makes the whole saved
  set count as none, not just the changes row. Using the snapshot alone
  would publish again every file published since that snapshot; taking the
  whole set as none publishes nothing that is in the folder, like the guess
  did.

What a start does with such a row now: it logs one Warn ("can't be
decoded"), takes every file in the folder as done with its stamp now (the
first-start rule, the same set the guess took), and its start save writes a
new snapshot and changes row over it. From then on the row decodes; the next
start resumes from it. Files that arrive after the start are published as
usual.

## Delivery semantics

- Nothing is published that the old fallback didn't publish: the first-start
  rule takes the same files as done as the guess (every file in the folder,
  stamp now). After the row is replaced, a restart reads what this watcher
  saved, so a later start publishes only files that really arrived while it
  was stopped. Before, every start guessed again and skipped those.
- Files skipped: the files in the folder at the start that were never
  published, which the guess also skipped. Nobody can tell them from
  published files, since the record that would is unreadable. Blocking until
  the row is fixed by hand would stop every new file instead.
- Deferred baseline (#409): it now takes the baseline at the first poll
  that reads the folder, instead of never. The files in the folder then
  count as done, as at a first start.

## Acceptance criteria

1. A start whose snapshot row can't be decoded (valid JSON, wrong shape):
   nothing in the folder is published, the start writes a decodable snapshot
   holding every file in the folder, and a later start resumes from it (a
   file that landed while stopped is published once, nothing else is).
2. Same with a decodable snapshot whose changes row can't be decoded:
   nothing in the folder is published and both rows are rewritten.
3. A start whose read fails (store error) and then reads an undecodable row:
   `settleGuess` keeps the guess and the snapshot is written at the next
   save. No Error is logged for it at any poll.
4. A deferred baseline (#409) that reads an undecodable row takes the
   baseline (first-start rule) and writes the snapshot; it does not stay
   deferred.
5. `keepSavedStamps` with a stampless done name and an undecodable row
   writes the snapshot.
6. A store error still writes nothing in each of these places (#536, #409,
   #500 behaviour unchanged); the existing tests keep passing.
7. The decode problem is logged once at Warn per watcher across polls,
   and no Error about it is logged.
8. A snapshot row that breaks while a watcher runs, and still owns the
   changes row on it, is replaced by a full snapshot at the next save (not
   only a changes row), so a crash before the stop flush leaves a row the
   next start can read.
9. Against Postgres (`src/pkg/testdb`): criterion 1 with the row in
   `connection_node_checkpoints`.
10. Tests fail without the change. Mutation run on: the decode vs store
    classification in `loadKnown`, `settleGuess`, `keepSavedStamps` and
    `mergeSaved`, the snapshot-row and changes-row classification, store
    errors taken as none, the forced snapshot, and the once-only Warn.
11. From `src/`: build, vet, golangci-lint, `go test -race` on the package
    and `pkg/doneset` with Postgres, `go test -race ./...`, `make
    lint-tenant`.
12. Backlog line struck with `(#563)`.

## Files

- `src/cmd/file-consumer/known.go`
- `src/cmd/file-consumer/service.go` (one field on `ActiveConnection`)
- new tests in `src/cmd/file-consumer` (`checkpoint_decode_test.go`,
  `checkpoint_decode_pg_test.go`)
- `plans/backlog.md`

Not touched: `src/pkg/doneset`. Its consumers (SFTP, cloud storage) have
the same kind of problem, worse (they publish nothing until a read works),
but those packages belong to other workers today; filed as a backlog item.

## Decisions

- Also in `mergeSaved` and `savedStampFor`, not only the three places the
  issue names: otherwise the stop flush writes nothing on such a row and
  each publishing poll logs a Warn.
- An undecodable changes row discards the whole saved set (see Approach).
- The Warn is once per watcher, not once per row: after the row is replaced
  it decodes, so a second one means it was broken again, which is rare
  enough for Debug.
- No change to `src/pkg/doneset`.
- `docs/connectors/file.md` doesn't describe the checkpoint fallback; no
  docs change.

## Risks

- A second watcher running at the same time (the old pod of a rolling
  deploy) that has a file in `pending` sees this watcher's snapshot list it
  as done, and its `mergeSaved` takes it as done. This is the hazard #536
  closed for store errors. With an undecodable row it can't be closed: the
  row that would tell the two apart is the broken one. It needs a broken row
  and two watchers at once.
