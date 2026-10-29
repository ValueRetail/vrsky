# Bind a backup archive to its object key (#549)

Open questions: none. Calls made without asking are under "Decisions".

## Goal

Backlog item 84, found in review of #471. A `VRSKYBK1` archive says
nothing about which backup it is. Anyone who can write to the backup
bucket can copy an older valid archive over the newest key, and
`backup verify` and `restore` both accept it, so a restore can be rolled
back to an older dump without anyone noticing. New archives get a
`VRSKYBK2` header that names the object key and the creation time, the
header is authenticated by every chunk, and `verify` and `restore` check
the key they were given against it.

## Acceptance criteria

1. `vrsky-cli backup` writes `VRSKYBK2` archives whose header holds the
   object key they were uploaded under and the time the backup started.
2. The header is the additional data of every chunk and goes into the HKDF
   info, so editing any header byte (key, time, salt, chunk size) makes
   the first chunk fail authentication.
3. `backup verify <key>` and `restore <key>` refuse a `VRSKYBK2` archive
   whose header names another key, before any chunk is decrypted or
   anything reaches `pg_restore`. A v2 archive copied to another key, or
   with an edited stamp, fails both.
4. `VRSKYBK1` and pre-#430 single-seal archives still verify and restore,
   but only with `--allow-unbound`, and then with a warning on stderr that
   the archive is not bound to its key. Without the flag both commands
   refuse them with an error that names the flag.
5. An unknown `VRSKYBK?` version is refused as unsupported, not tried as
   a legacy archive.
6. Round trips are byte-identical at sizes 0, 1, chunk-1, chunk, chunk+1
   and several chunks. All of #471's tamper cases (truncation at every
   offset, a bit flip at every offset, dropped/swapped/duplicated/spliced
   chunks, trailing bytes, foreign header, wrong key, unclosed writer)
   still fail under v2.
7. No secret in output: messages name the object key (quoted with `%q`,
   since an unauthenticated header can hold any bytes) and the format,
   never key material.
8. Tests fail without the change (mutation run). The DR drill passes.
9. `docs/DR.md` and `docs/operator/backup.md` describe v2, the binding,
   and `--allow-unbound`. Backlog item 84 struck with `(#549)`; a new item
   removes v1 reading and the flag once the 14-day window holds only v2.

## Files

- `src/cmd/vrsky-cli/stream.go`: v2 header, writer, a reader for v1 and v2.
- `src/cmd/vrsky-cli/backup.go`: `streamBackup` binds the key it uploads
  to; `backup verify --allow-unbound`.
- `src/cmd/vrsky-cli/restore.go`: `openArchive` dispatch and binding check;
  `restore --allow-unbound`.
- `src/cmd/vrsky-cli/main.go`: usage text.
- `src/cmd/vrsky-cli/*_test.go`: existing tests adapted to the new
  signatures (mechanical), new tests from the test author.
- `docs/DR.md`, `docs/operator/backup.md`, `plans/backlog.md`.

Not touched: `pkg/crypto`, `pkg/objectstore`, the CronJob, the DR drill
workflow.

## Approach

```
header  = "VRSKYBK2" (8) || chunk size u32 BE (4) || salt (32, random)
          || created, Unix seconds i64 BE (8) || key length u16 BE (2) || key (1..1024 bytes)
key     = HKDF-SHA256(ENCRYPTION_KEY, salt, info = "vrsky-backup-v2" || header)
chunk_i = AES-256-GCM(key, nonce = 0^7 || u32 BE i || last, plaintext_i, aad = header)
```

- The chunk layer (nonce, last flag, truncation and trailing-data checks)
  is unchanged and shared by both versions; only the header and the key
  derivation differ. v1 keeps `info = "vrsky-backup-v1"` and its 44-byte
  header.
- The fixed prefix (magic, chunk size, salt) has the same offsets in v1
  and v2. The key length is checked (1..1024, the S3 and Azure limit)
  before anything is allocated for it.
- The writer only writes v2. Nothing in the binary can write v1 any more;
  tests carry their own v1 sealer, written from the format comment.
- `newOpenReader` reads either header and returns what it says
  (`archiveInfo`: format, key, created). `openArchive` makes the decision:
  v2 must name exactly the key that was downloaded; v1, legacy and
  anything else without a known magic need `allowUnbound`.
- `streamBackup` binds the key it passes to `PutStream`, so the bound key
  and the stored key can't drift apart. The creation time is taken there.

## Downgrade

The question the review of #471 asks: can an attacker who can write to
the bucket make restore skip the binding by handing it an older format?

- Swapping the magic of a v2 archive to `VRSKYBK1` or to garbage makes it
  read as v1 or legacy. Both need `--allow-unbound`, and even then they
  fail: v1 uses a different HKDF info and a shorter header as AAD, and the
  legacy path opens it under `ENCRYPTION_KEY` directly.
- Putting a genuine v1 or legacy archive under the newest key is the real
  attack. Accepting those with only a warning would leave it open for as
  long as v1 reading exists, and for longer than 14 days: any v1 archive
  ever made under the same `ENCRYPTION_KEY` stays valid. So unbound
  archives need an explicit `--allow-unbound` on each command. The
  attacker can't supply the flag; the operator only needs it to restore a
  backup made before this change, and the newest backup is v2 from the
  first CronJob run after the deploy.

## Decisions

- Bind the full object key, prefix included, and not the bucket. Copying
  an archive to another bucket or storage account under the same key
  still restores (that is how a DR copy works); renaming it does not. No
  flag to override the key: an archive that has to be restored from
  another name can be copied back to its original name, which the error
  message prints.
- The creation time is authenticated and printed by `verify` and
  `restore`, not checked against anything. The key already contains the
  stamp, and `--stamp` lets the drill pick a non-time stamp.
- `--allow-unbound` instead of the warning alone the issue suggests. See
  "Downgrade". The warning still prints when the flag is used.
- The header goes into the HKDF info as well as the AAD, so a different
  key name means a different subkey too. The AAD alone would be enough
  for authentication; the info makes the binding hold even if a future
  change stops passing the header as AAD.
- A `VRSKYBK` prefix with an unknown version byte is refused instead of
  falling through to the legacy path, so a future v3 archive read by this
  binary gets a clear error.

## Review

A fresh security reviewer approved, with five non-blocking findings, all
taken:

- An object shorter than a magic was treated as legacy, so its error
  pointed at `--allow-unbound`. It is now a read error.
- The mismatch error said the archive "was made as" the header's key
  before the header had authenticated. It now says the header names it,
  and the docs say to verify after copying an archive back.
- `restore` ignored arguments after the key, so `… KEY --allow-unbound`
  dropped the flag silently. It now refuses them.
- The unbound error and warning now say that backups made since
  `VRSKYBK2` never need the flag, so needing it for a newer key means
  tampering.
- The usage text shows `--allow-unbound` for `verify`.

## Risks

- Format change in a secrets-bearing artifact: PR plus `agent:needs-human`
  and a security review.
- An operator who renamed archives in the bucket can no longer restore
  them under the new name. Documented, and the error names the original
  key.
- The runbook gains a flag for old archives during the first 14 days.
  Documented in `docs/DR.md`.

## Verification

From `src/`: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
`go test -race ./...`, `make lint-tenant`. Mutation run on the binding
check, the AAD, the HKDF info and the unbound gate. The DR drill runs on
the PR (path filter on `src/cmd/vrsky-cli/**`): backup with
`--stamp drill`, restore of the same key, so it exercises a v2 round trip
through the real image, Azurite and `pg_restore`.
