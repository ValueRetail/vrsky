# A watched file replaced under the same name is never published again (#420)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Once `orders.csv` has been published, a writer that overwrites it, or
renames a new `orders.csv` over it, gets the new file published once, after
it stops changing. A file nobody touched is never published again, and a
restart that reads a checkpoint saved by the previous version publishes
nothing extra.

## Acceptance criteria

1. A known file whose size or mtime changes is a new file. It goes through
   the stability rule (#374) and is published once, with the new content.
2. A known file whose size and mtime are unchanged is never published
   again, however many polls or restarts pass.
3. A rename over a known name is published once.
4. A checkpoint in the old format (`{"watch_dir", "known": [names]}`) still
   loads. A restart on it publishes nothing that the old code would not
   have published, and the next save writes the new format.
5. A file replaced while the connection was stopped is published once when
   it starts again, the same as a file that arrived while stopped (#391).
6. Two watchers for one connection (rolling deploy) do not both publish
   one replacement: `mergeSaved` matches the saved stamp.
7. Tests for 1–4 written by a separate test author from these criteria;
   they fail without the fix. `docs/connectors/file.md` updated; backlog
   item 29 struck with `(#420)`.

## Where the gap is

`pollOnce` skips any name in `ac.knownFiles` (`map[string]bool`). A name
leaves that set only when a poll does not see it. Overwrite in place and
rename-over both keep the name in every listing, so the new content is never
read. The checkpoint (#391) saves names only, so a restart does not help
either.

## Decision: size or mtime changed means a new file

The done set remembers, per published name, the size and mtime the file had
when it was published (its stamp). A poll that finds a known name with a
different stamp treats it as new from that poll on: pending, the usual two
unchanged looks one interval apart, then published, and the new stamp
recorded.

Alternatives considered:

- Content hash. Catches only real content changes, so `touch` would not
  resend. But it reads every changed file in full to decide, twice for a
  streamed file over the inline threshold (ADR 0001, files of any size), and
  a hash per name grows the checkpoint by 64 bytes a file. Rejected for now.
- Inode / file id. Catches rename-over even with identical size and mtime,
  but not an in-place overwrite, is platform-specific (`syscall.Stat_t`),
  and Azure Files over SMB does not give stable inode numbers. Rejected.
- Leave it as it is and document "copy in under a new name". That is what
  the docs say today, and the issue asks to decide; a drop folder fed by a
  tool that always writes `orders.csv` is common enough.

## Approach

`src/cmd/file-consumer/service.go`, `known.go`:

- `ActiveConnection.knownStamps map[string]fileStamp`, next to the
  unchanged `knownFiles map[string]bool`. `fileStamp{size, mtimeSec,
  mtimeNsec int64}` from `os.Stat` (seconds and nanoseconds kept apart, so
  no year overflows `UnixNano`). Invariant: every key of `knownStamps` is in
  `knownFiles`. A known name with no stamp is one whose stamp is not known
  yet (old checkpoint, failed stat): the next poll that can stat it adopts
  the current stamp without publishing. Keeping the bool set as it is keeps
  the meaning of "done" and every existing test that reads it unchanged.
- `pollOnce`, per listed name:
  - temp names: skip (before the stat, as before).
  - stat. Known with no stamp: adopt, dirty, skip. Known with the same
    stamp: skip. Known with a different stamp, or unknown: the stability
    check, unchanged.
  - before publishing, `mergeSaved` as today; then skip if the name is now
    known with no stamp or with this stamp.
  - publish: record `knownFiles[name] = true` and the stamp. A publish cut
    off by a stop restores what was there before (old stamp, or not
    known), so the next start publishes it. An ingest error keeps the new
    stamp: the same file is not retried every poll, but a fixed one written
    over it is.
  - deleted names leave both maps.
- `loadKnown`: stats the names it takes as done. First start (and the
  fallbacks with no checkpoint store or a failed checkpoint read): every
  file gets its current stamp. Resume: saved names still in the folder are
  done; their saved stamp is kept, so a file replaced while stopped differs
  and goes through the check (criterion 5). A saved name without a stamp
  (old format) gets its current stamp now, which publishes nothing (4), and
  makes the set dirty, so the start saves the stamps. Once a row has stamps
  nothing is adopted, so this is one save per folder. (A first draft left
  adoption out of `dirty`; review showed a quiet folder then kept a
  name-only row for good, and a file replaced while the cluster was parked
  was adopted again at every start and never published.)
- Checkpoint state:
  `{"watch_dir": "...", "known": ["a.csv", ...], "files": {"a.csv": {"size": 12, "mtime": 1767225600, "mtime_nsec": 0}}}`.
  `known` still lists every done name. Old code reads only `known`, so a
  rollback to the previous image, or the old pod during a rolling deploy,
  sees the same done set it always did. New code reads `files` for stamps;
  a name in `known` and not in `files` has no stamp. This is the "migrate
  on read": nothing rewrites rows in the database; the first save after a
  start writes both keys.
- `mergeSaved(ctx, ac, current)`, for each saved name in the folder:
  - saved with no stamp (written by an old pod): if this watcher does not
    know the name, it becomes known with no stamp (as today; adopted next
    poll). Otherwise nothing.
  - saved with a stamp that this watcher already has: nothing.
  - otherwise stat the file; if it matches the saved stamp, another
    watcher published this version: known, that stamp, out of pending. If
    not, this watcher holds a newer version and goes on.

Tenant scoping: unchanged. The checkpoint is read and written through
`pkg/checkpoint` with the tenant, connection and node of the start, and the
folder is still resolved by `tenantpath.Resolve`.

## Interactions

- Stability rule (#374): a replaced file is pending like a new one. A
  writer that rewrites `orders.csv` slowly is published once, complete. A
  poll that comes too soon does not publish.
- Restart (#391): stamps are saved with the names, so a restart neither
  republishes an unchanged file nor misses a file replaced while stopped.
  A crash between publish and save republishes that poll's files, as
  before.
- Old checkpoints: adopted at the first start of this version, so nothing
  extra goes out. A file replaced between the last poll of the old version
  and that start is not published (the old code would not have either).
  That start saves the stamps, so from the next stop on a replacement is
  seen.
- Missing folder (#398): a remount brings files back with their mtimes, so
  nothing is republished. `TestDirRead_MissingFolderDuringPoll` simulated
  the remount by writing fresh copies, which now have new mtimes and are
  new files under this rule; the test now gives the copies the originals'
  mtimes, as a remount would. Its assertions are unchanged.
- Two pods (`mergeSaved`): matched on the stamp, so pod B skips the version
  pod A already published but still publishes a later one. During a rolling
  deploy an old pod saves names only; the new pod then sees no stamps and
  adopts, the old behaviour for that window.
- Deferred baseline (#398, #409): goes through `loadKnown`, so it gets the
  same stamping.
- Clock skew: stamps are compared for equality, never ordered against the
  pod's clock, so skew between the writer (or Azure Files) and the pod does
  not matter. Writers that set an old mtime (`cp -p`, `rsync -t`, unzip)
  still produce a different stamp. The stability window uses the pod's own
  clock as before.

## Risks

- `touch orders.csv` (or any rewrite with the same content) publishes the
  file again. Accepted and documented: there is no cheap way to tell it
  from a real rewrite, and "touch to resend" is a reasonable thing to have.
- A file that keeps growing in the folder (a log someone writes into the
  drop folder) is published again every time it pauses for a poll. Before,
  a name already known was ignored for good. Documented: drop folders are
  for finished files.
- A replacement with the same size and the same mtime is not seen: a
  coarse-mtime filesystem (2 s on FAT, 1 s on some shares) where a
  same-size rewrite lands in the same tick, or a `cp -p` of a different
  file with identical size and mtime. Documented.
- Every poll now stats every file in the folder, not only the new ones.
  `os.ReadDir` alone did not. A folder of thousands of files on a network
  share costs that many stat calls every 5 s. The docs already say to move
  processed files out.
- The checkpoint row grows by the stamps, about 50 bytes a name.

## Files

- `src/cmd/file-consumer/service.go`, `known.go`
- `src/cmd/file-consumer/replace_test.go` (the test author's, from the
  acceptance criteria only)
- `src/cmd/file-consumer/known_test.go` (mine: two watchers and one
  replacement, a cut-off publish of a replacement (two tests), an
  old-format row that gets its stamps at the first start)
- `src/cmd/file-consumer/dirread_test.go`: the remount keeps mtimes
  (above), and `dirRig.seed`, which writes a checkpoint "as a previous
  lifetime would have left it", now writes the stamps a previous lifetime
  leaves. Without that it wrote the old format by accident, and the
  deferred-baseline case's "prunes nothing, saves nothing" assertion would
  see the one migration save.
- `docs/connectors/file.md`
- `plans/backlog.md` (item 29 struck)

## Verification

From `src/`:

- `go test -race -count=20 ./cmd/file-consumer/...`
- `go test -race ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `make lint-tenant`
- Mutations, each restored afterwards:
  - known names skipped without comparing stamps (the old rule): the
    overwrite and rename-over tests fail.
  - unchanged stamp not treated as done (any known name republished): the
    unchanged-file test fails.
  - old-format names not adopted at load (treated as new): the old-format
    restart test fails.
  - `mergeSaved` ignores stamps: the two-watcher replacement test fails.
