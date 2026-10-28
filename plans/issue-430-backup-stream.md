# vrsky-cli backup: stream the dump instead of buffering it (#430)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Backlog item 55, found in review of #404. `vrsky-cli backup` holds
`pg_dump`'s output, the gzipped copy and the encrypted copy in byte slices,
so a dump of about a third of the CronJob's 512Mi limit gets the Job
OOM-killed. Stream pg_dump through gzip and encryption into
`ObjectStore.PutStream` so memory stays flat whatever the dump size.

## Acceptance criteria

1. `vrsky-cli backup` never holds the dump, the gzipped dump or the
   encrypted dump in memory as a whole. Memory use is bounded by fixed-size
   buffers (one encryption chunk, gzip's window, the store SDK's part
   buffers), not by the dump size.
2. Backups made by the new code restore with `vrsky-cli restore` to the
   exact bytes `pg_dump` produced.
3. Backups made before this change (single-shot AES-256-GCM,
   `nonce || ciphertext` over the gzipped dump) still restore.
4. A new-format backup that is truncated (at a chunk boundary or inside a
   chunk), has a chunk reordered, dropped or altered, has trailing bytes, or
   is opened with the wrong key fails restore with an error. It never
   yields a silently shortened dump.
5. A failing `pg_dump` (non-zero exit, or no output) fails the backup and
   the uploader sees an error, not a clean end of stream, so no valid
   object is committed. If the upload fails, `pg_dump` is stopped and no
   goroutine is left behind.
6. Object key, content type, the `backup complete: <key>` log line and the
   `list` command are unchanged.
7. Tests that fail without the change: a large synthetic dump through the
   backup pipeline with a bounded heap, a byte-identical round trip, legacy
   restore, and the tamper/truncation cases. A mutation run (reintroduce
   buffering, drop the final-chunk flag) makes a test fail.
8. Backlog item 55 struck with `(#430)`; `docs/DR.md` and
   `docs/operator/backup.md` describe the format and the memory behaviour.

## Files

- `src/cmd/vrsky-cli/backup.go`: streaming pipeline.
- `src/cmd/vrsky-cli/restore.go`: format detection, streaming restore.
- `src/cmd/vrsky-cli/stream.go` (new): the chunked encryption format.
- `src/cmd/vrsky-cli/*_test.go`: tests (test author).
- `docs/DR.md`, `docs/operator/backup.md`, `plans/backlog.md`.
  `docs/SECURITY.md`'s note on key rotation and backups still holds as is.

Also: the key-rotation caveat comment in `src/cmd/migrate-secrets/rewrap.go`
(comment only).

Not touched: `pkg/crypto`, `pkg/objectstore`, the CronJob, the DR drill
workflow.

## Approach

AES-GCM through Go's `cipher.AEAD` seals a whole message at once, so the
current format (one GCM seal over the whole gzipped dump) can't be produced
without holding all of it. The new archives use a chunked format in the
style of the STREAM construction, with a per-archive key as in `age`:

```
header  = "VRSKYBK1" (8 bytes) || chunk size uint32 BE (4) || salt (32, random)
key     = HKDF-SHA256(ENCRYPTION_KEY, salt, info = "vrsky-backup-v1")
chunk_i = AES-256-GCM(key, nonce = 0^7 || uint32 BE i || last, plaintext_i, aad = header)
file    = header || chunk_0 || chunk_1 || ... || chunk_n   (chunk_n has last = 1)
```

- Plain chunks are 64 KiB; every chunk but the last is full. The writer
  only marks a chunk last on `Close`, so an empty input gives one empty
  final chunk and a cut-off stream has no final chunk.
- The counter in the nonce stops reordering and dropping, the last flag
  stops truncation at a chunk boundary, the header as AAD stops header
  edits. The reader rejects a chunk size above 16 MiB before allocating,
  data after the final chunk, and EOF before it.
- The chunks are never sealed under `ENCRYPTION_KEY`, which `pkg/crypto`
  uses for the secrets table. HKDF with the info string separates the two
  uses, and the salt gives each archive its own key. The master key is
  decoded the same way as `pkg/crypto` (64 hex chars, 32 bytes).
- No random nonce prefix: no key encrypts more than one archive, so the
  counter and flag keep nonces unique (as in age).
- Restore peeks the first 8 bytes of `GetStream`. Magic: stream through
  the chunk reader and gunzip into `pg_restore`'s stdin. Anything else is a
  legacy archive: read it whole and use `crypto.DecryptBytes`, as today.
  A legacy nonce matches the magic with probability 2^-64.

Backup pipeline:

```
pg_dump stdout -> count -> gzip.Writer -> chunk writer -> io.Pipe -> PutStream
```

A goroutine copies pg_dump's stdout into gzip, waits for pg_dump to exit,
and only then closes gzip and the chunk writer (which writes the final
chunk) and closes the pipe. On a pg_dump failure it closes the pipe with
the error before the final chunk exists, so the store's upload fails and
even a committed partial object would not restore. If `PutStream` returns
first, the read side of the pipe is closed with its error and the context
is cancelled, which kills pg_dump; `runBackup` waits for the goroutine
before returning.

`vrsky-cli backup verify <key>` downloads an archive and reads it to the
end through decryption and gunzip into `io.Discard`, exiting non-zero on
any damage. `docs/DR.md` makes it the step before `restore`.

The pipeline is a function over interfaces (`io.Reader` for the dump, a
wait func for the exit status, a `PutStream` interface for the store) so
tests run it without pg_dump or a real store.

## Decisions

- New on-disk format instead of keeping the single GCM seal: the stdlib
  has no incremental GCM, and hand-rolling GHASH is worse than a well-known
  chunked construction. Restore reads both, so existing backups in the
  14-day window stay usable. File extension and key layout unchanged.
- Restore streams too for new archives. A backup the restore Job can't
  hold in memory is no backup. Trade-off: a corrupt new-format archive is
  detected partway through `pg_restore` rather than before it starts. The
  runbook restores into an empty scratch database and the source guard
  stays, so a partial restore there is harmless and the command exits
  non-zero. Legacy archives keep the old verify-then-restore behaviour.
  `backup verify` (added in review) closes the gap for new archives: the
  runbook verifies before it restores.
- Per-archive HKDF subkey (added in review of #471): it had to be in the
  first version of the format, or changing it later means a `VRSKYBK2`.
- `pg_restore --single-transaction` (review round 2): a download that
  breaks off, or an archive replaced between verify and restore, rolls
  back the `--clean` drops instead of leaving a half-restored database.
  We don't use `-j`, so nothing conflicts.
- `backup verify` rather than `restore --verify-only`: restore's flags
  (`--target-db-url`, `--confirm`) mean nothing for a read-only check, and
  verify needs no `BACKUP_DB_URL`.
- The format lives in `src/cmd/vrsky-cli`, not `pkg/crypto`: it is the only
  user, and the issue is scoped to the CLI. Move it if a second user
  appears.
- `gzipBytes`/`gunzipBytes` go away with their test; the round-trip tests
  cover gzip.

## Risks

- This changes how backups are encrypted, which touches secrets handling
  (the dumps contain the secrets table). The PR gets `agent:needs-human`
  for a human look at the format before merge.
- GCS `PutStream` closes (commits) its writer when the body returns an
  error. Not in scope; the missing final chunk makes such an object fail
  restore loudly. Backlog note added.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race ./...`, `make lint-tenant`. The DR drill workflow runs on
this PR (path filter on `src/cmd/vrsky-cli/**`) and does a real
pg_dump → Azurite → pg_restore round trip in the image.
