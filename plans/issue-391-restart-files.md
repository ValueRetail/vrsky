# Files that land just before a restart are never published (#391)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

A file that reaches a watched folder and has not been published when the
file consumer stops (stop command, pod restart, deploy) is published once
when the connection runs again. Nothing published before the stop goes out
again, and the first start still ignores what was already in the folder.

## Acceptance criteria (from the issue)

1. A file that arrives before a stop or restart and was not published is
   published once after the connection starts again.
2. Files published before the stop are not published again.
3. Files that were in the folder before the connection was ever started
   (its first start) are not published, as today (#366).
4. The approach and its trade-offs are written down here. It survives a pod
   restart, so in-memory state alone is not enough.
5. Tests cover restart with an unpublished file, restart with a published
   file and the first start, asserting what is published. Also a file still
   being written at the stop.
6. `docs/connectors/file.md` updated; backlog item 30 struck with `(#391)`.

## Where the gap is

`startConnection` (`src/cmd/file-consumer/service.go`) builds the new
watcher's `knownFiles` from `listFiles(watchDir)`: every name in the folder
counts as done. Since #378 a file is published on the second unchanged poll,
5 to 10 s after it lands. A file in that window at a stop is in the next
start's baseline and is skipped for good. Every deploy restarts the pod.

## Options

### A. Persist the published names per connection (chosen)

Keep the set the watcher already has (`knownFiles`: the baseline plus every
name published or failed, minus names that have gone) in the database and
load it at start. Names in the folder but not in the saved set are new.

There is already a place for it: `connection_node_checkpoints`, used through
`pkg/checkpoint`. It is keyed by `(tenant_id, connection_id, node_id)`, has a
free-form `state jsonb` column (migration 000025, used by the Business
Central consumer for the same kind of thing) and `connection_id` references
`connections(id) ON DELETE CASCADE`. No migration needed.

- Clock skew: none involved. Only names are compared.
- First start: no checkpoint row means first start. The baseline is taken
  as today and saved at once, so a restart before the first publish is not
  mistaken for another first start.
- Deleted connection: the row goes with it (cascade).
- Tenant isolation: every `pkg/checkpoint` query filters on `tenant_id`;
  the row is written with the tenant the start command carried and that
  `getConnection` checked.
- Large folders: the state is one JSON array of the folder's names, about
  as many bytes as a directory listing (100 000 names of 30 bytes is ~3 MB).
  It is written only at the end of a poll that changed the set, not per
  file, and read at start and once per poll that is about to publish.
- Files that arrive while a connection is stopped are not in the set, so
  they are published when it starts again. See Decisions.
- Cost: a crash between a publish and the end-of-poll save republishes
  that poll's files on the next start. At-least-once, as the rest of the
  pipeline is.

### B. Store the last stop time; files with an mtime after it are new

One timestamp per connection instead of a set.

- Files that landed just before the stop, the very case in the issue, have
  an mtime before the stop time. It would have to be "mtime after the last
  completed poll minus a margin", which publishes some files twice and
  still misses others.
- mtime is set by the writer or the file server (Azure Files), not by the
  pod. Skew between the two moves the line either way.
- Tools that keep the source mtime (`cp -p`, `rsync -t`, unzip, SFTP
  clients with "preserve timestamps") deliver files that look old and are
  skipped for good, the bug this issue is about.
- An ungraceful kill (OOM, node loss) leaves no stop time at all.

### C. A marker in the watched folder

Rename or move published files (`.done/`), or keep a dotfile listing the
published names next to them.

- Writes into the customer's folder, which can be read-only for us or
  watched by more than one connection (the marker would need the connection
  ID in its name).
- Moving files changes what customers see today; tools that drop and later
  re-check their files would break.
- Stale markers stay behind when a connection is deleted.
- Survives a restart and needs no database, which is its only advantage.

## Approach (A)

`service.go`:

- `fileConsumer.checkpoints checkpoint.Store`, set in `Configure` to
  `checkpoint.NewPostgresStore(res.DB)` unless a test set it. Nil means "no
  persistence" (the behaviour before this change); the hand-built
  consumers in the existing tests stay as they are.
- `ActiveConnection` gains `NodeID` (the file consumer node's `id` from the
  connection, `"file-consumer"` if it has none) and `dirty` (the set changed
  since the last successful save). Both touched only by the start path
  before the watcher goroutine runs, then only by that goroutine.
- State: `{"watch_dir": "<resolved dir>", "known": ["a.csv", ...]}`, names
  sorted.
- Start (`loadKnown`), still before the connection shows as active (#366):
  list the folder; read the checkpoint.
  - No row, or the saved `watch_dir` differs from the resolved one (the
    path was edited): first start. Baseline = everything listed. Dirty.
  - Row with the same dir: known = saved names that are still in the
    folder. Everything else in the folder is new and goes through the
    normal stability check. Dirty if names were pruned.
  - Read error or unreadable state: log, fall back to today's baseline,
    dirty. A transient database error then costs what it costs today rather
    than keeping the pipeline down.
  - After `activateStart`, before the watcher starts: save if dirty.
- `pollOnce`:
  - Before the first publish in a poll, re-read the checkpoint and add any
    saved name that is in the folder to `knownFiles`. In a rolling deploy
    the old and new pods overlap for a few seconds and both watch the same
    folder; the old pod may publish a pending file after the new pod loaded
    its set. Re-reading right before publishing narrows that to the time
    between two saves. The same covers a stop followed quickly by a start
    on the same pod, where the old watcher may still be finishing a poll.
  - If the context is cancelled (stop, shutdown) the poll stops before the
    next file. A file whose ingest fails because the context was cancelled
    is not marked known, so the next start publishes it. Today it is marked
    known, which only mattered until the restart's baseline; persisted, it
    would be lost for good.
  - Mark dirty whenever `knownFiles` gains or loses a name. At the end of
    the poll, save if dirty, on a context detached from the watcher's (so
    a cancelled watcher still records what it published) with a 5 s
    timeout. A failed save keeps dirty and is retried next poll.

No change to the stability rule, temp-name skip, start reservation,
tenant path resolution or the upload path.

## Decisions

- Option A over B and C, for the reasons above.
- `connection_node_checkpoints` instead of a new table: it is the existing
  per-node state store, tenant-scoped, cascades on delete, and the BC
  consumer already keeps a set of seen ids in `state`. No migration.
- Files that arrive while a connection is stopped are published when it
  starts again. The issue limits "not published" to the first start, and a
  drop folder that keeps what arrived while the pipeline was paused is what
  people expect from an inbox. Documented.
- First start for connections that are running when this ships: they have
  no checkpoint row, so the deploy that ships this takes one last baseline
  the old way. A file in flight during that one deploy can still be
  skipped; from then on it cannot.
- Editing the watch path counts as a first start for the new folder.
- A failed checkpoint read falls back to today's baseline instead of
  refusing to start.
- Ingest errors other than cancellation still mark the name known (no
  retry every 5 s), as today, and that is now persisted too.

## Files

- `src/cmd/file-consumer/service.go`, and a new `known.go` (load, re-read
  and save of the done set)
- `src/cmd/file-consumer/consumer_test.go`, `restore_test.go`: the sqlmock
  round-trip tests gain expectations for the checkpoint read and saves, so
  the real path runs instead of failing quietly into the fallback.
- `restart_test.go`, `restart_pg_test.go` (the test author's, written from
  the acceptance criteria only) and `known_test.go` (mine: the two-watcher
  overlap and a publish cut off by a stop, which the AC tests don't reach)
- `docs/connectors/file.md`
- `plans/backlog.md` (item 30 struck; item 40 added, #393 takes 39)

## Verification results

- Mutations, each restored afterwards:
  - start always takes the whole folder as done: 5 tests fail, the AC1,
    AC4, isolation and background-watcher restart tests among them.
  - saved names ignored: 9 tests fail, including
    `TestRestart_PublishedFileNotPublishedAgain`.
  - no re-read before publishing: `TestKnown_SecondWatcherSkipsWhatTheFirstPublished`
    and the two sqlmock round trips fail.
  - a cancelled publish kept as done:
    `TestKnown_PublishCutOffByStopIsPublishedByNextStart` fails.
- Not tested: a failing checkpoint read or save (falls back to the old
  rule, or retries next poll); logged only.

## Risks

- Duplicates when two watchers overlap (rolling deploy, or stop then start
  on one pod while the old watcher finishes a poll): narrowed by the
  re-read before publishing, not removed. Two pods watching one folder
  already publish files that land during the overlap twice; that is older
  than this change.
- State size for very large folders, above. Writes only on change.
- A crash between publish and save republishes that poll's files.
- A file replaced under a known name while stopped is not republished,
  the same as while running (backlog item 29).

## Verification

From `src/`:
- `go test -race -count=20 ./cmd/file-consumer/...`
- `go test -race ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `make lint-tenant`
- `make test-db` when `VRSKY_TEST_POSTGRES_URL` is set
- Mutations: drop the restart handling (load the saved set and ignore it,
  i.e. always take the baseline) and the restart-with-unpublished-file test
  must fail; drop the "already published" check (ignore saved names) and
  the no-republish test must fail. Restore both.
