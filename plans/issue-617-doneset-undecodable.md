# An SFTP or cloud-storage done set that can't be decoded stops the poller for good (#617)

Open questions: none. The one real choice is under "Decision".

## Goal

Backlog item 118. `doneset.Set.Load` returns the same kind of error for a
checkpoint row it can't decode as for a store error, and both pollers
(`src/cmd/sftp-consumer`, `src/cmd/cloud-storage-consumer`) publish nothing
until `Load` works, logging an Error every poll. A row that fails to decode
never loads, so the connection publishes nothing until someone fixes the row
by hand. `Refresh` and `Save` read the same rows, so they fail the same way.

Postgres keeps the state in a `jsonb` column, so such a row is valid JSON of
the wrong shape (a field of another type, an array, a string). It can come
from a hand edit, a bug, or another version's format after a rollback. Nobody
can read it, so it carries nothing to keep.

## Decision: what an undecodable row means

Two candidates:

1. No saved set. The poller then follows its first-start rule, which for
   these two pollers is to publish every file or object in the folder.
2. Everything present now is done (what the file consumer does, #563).

I take 1.

- The file consumer's first-start rule is "the files in the folder now count
  as done", so #563 mapped the undecodable row onto that rule. For the SFTP
  and cloud-storage pollers the first-start rule is the opposite: publish
  everything (`docs/connectors/sftp.md`, `cloud-storage.md`, "First start").
  Following "the same rule as a first start" gives 1 here, and the same
  principle as #563.
- With 2, a file that arrived after the last good save but before the row
  went bad is never published, and nothing in the logs says which. With 1 it
  is published, and so are files that were already published: duplicates.
  This platform delivers at least once, downstream consumers already deal
  with duplicates (every restart before #538 produced them, and a first
  start still does), and a lost file cannot be found again. Duplicates over
  loss.
- Many connections use `after_action` delete or move, so the folder holds
  only unpublished files and the duplicates are few.

Only the snapshot row is "no saved set". A snapshot that decodes with an
undecodable changes row on top keeps the snapshot: the names in it were
published, and the files published since the snapshot are published once more
(at most `maxChanges` names). Dropping the whole set for a damaged small row
could republish 100 000 files. This differs from #563, which drops both
(there the choice is between skipping and not skipping; here, using the
snapshot is the cheaper way to the same safe side).

## Approach (`src/pkg/doneset`)

- `read` wraps a decode failure in a new `ErrUndecodable`. A store error is
  returned as it is.
- `loadSaved` returns, besides saved and found, the decode error it set
  aside:
  - snapshot undecodable: found is false, nothing saved, the error is set
    aside;
  - changes row undecodable: the snapshot alone, found is true, the error is
    set aside;
  - a store error from either row is still an error.
- `ownsSaved` treats an undecodable changes row as not this Set's, so
  `Refresh` and `Save` go on to `mergeSaved`.
- `mergeSaved` (called from `Refresh` and `Save`) takes a set-aside error as
  "nothing there to merge". Its caller's next write is a snapshot, which
  replaces both rows.
- `Load` stores the error it set aside (`Undecodable()`), clears `gen` and
  marks the Set dirty, so the first `Save` writes a snapshot and a fresh
  changes row even when the poll publishes nothing. After that the rows
  decode, and the next start resumes from them.
- Pollers: after the first successful `Load`, `if err := done.Undecodable()`
  gives one Warn with the connection id (the poller's logger carries it),
  saying the saved list can't be read, what happens now and that it is
  replaced at the next save. `Load` runs once per Set (the poller keeps it
  until it succeeds), so the Warn is not repeated per poll. In the cloud
  storage poller this edit is inside `pollOnce`, outside `runEventLoop`.
- A store error (DB down) is untouched: `Load` returns it, the Set stays
  unloaded, the poller publishes nothing and logs an Error, retries at the
  next poll.

## Acceptance criteria

1. A snapshot row that can't be decoded: `Load` returns found=false and no
   error, the Set is loaded and empty, and the first `Save` replaces the row
   (and the changes row) so the next `Load` finds it.
2. A snapshot that decodes with a changes row that can't: `Load` returns the
   snapshot's names, found=true; the first `Save` rewrites both rows.
3. A changes row that can't be decoded while the Set has its own snapshot
   (`Refresh`, `Save` mid-run) does not fail either call.
4. A store error on either row still makes `Load` fail and leaves the Set
   unloaded; the pollers publish nothing and keep logging the Error.
5. A row that decodes loads as before (the existing tests stay green).
6. SFTP and cloud-storage pollers: with a corrupt row in the store, the
   first polls publish every file/object once (and once only), the row is
   replaced, a restart after that publishes nothing again, and the Warn is
   logged once with the connection id.
7. Docs for both connectors say what happens.
8. Backlog item 118 struck with #617.

## Files

- `src/pkg/doneset/doneset.go`, tests in `doneset_test.go` and
  `doneset_pg_test.go` (real Postgres through `src/pkg/testdb`)
- `src/cmd/sftp-consumer/service.go` (the Warn in `pollOnce`), a test in
  `done_persist_test.go`
- `src/cmd/cloud-storage-consumer/service.go` (the Warn in `pollOnce` only,
  not `runEventLoop`, which #609 edits), a test in `done_persist_test.go`
- `docs/connectors/sftp.md`, `docs/connectors/cloud-storage.md`
- `plans/backlog.md`

## Risks

- Duplicates after a corrupt row, up to the whole folder. Accepted above.
- Two pollers of one connection (rolling deploy): the old pod may still hold
  its own good set and write a snapshot over the corrupt row first; the new
  pod's first save then merges that one, because `mergeSaved` reads the row
  again. Nothing new here.
- A future format change that old pods can't decode now makes an old pod
  republish instead of stopping. Rolling back has the same cost as any other
  first start; the field-adding rule in `known.go` still applies.
- Merge order with #609: both touch `src/cmd/cloud-storage-consumer/service.go`
  but different functions, so a rebase should be clean.

## Verification

- New doneset tests (table-driven) and pg test fail with the fix reverted
  and pass with it.
- New poller tests fail without the fix.
- From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `go test -race -count=1` on `./pkg/doneset ./cmd/sftp-consumer
  ./cmd/cloud-storage-consumer`, `make lint-tenant`.
