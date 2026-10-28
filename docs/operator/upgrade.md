# Upgrade

VRSky rolls forward by deploying new images and applying any new database
migrations. Workers are stateless; durable state lives in Postgres and NATS
JetStream.

## Order of operations

1. **Back up first.** Snapshot the management database (see
   [Backup & restore](backup.md)) before any migration.
2. **Apply migrations.** Migrations live in `infrastructure/migrations/`
   (`NNNNNN_name.{up,down}.sql`). They are forward-only in practice; every `up`
   has a tested `down`.
   ```bash
   migrate -path infrastructure/migrations \
     -database "$MANAGEMENT_DB_URL" up
   ```
   Migrations are additive and safe to run before rolling new code (new columns
   default sensibly; old code ignores them).
3. **Roll the images.** Update the management-API and worker images; they
   reconnect to NATS and resume. The SDK drains in-flight work on shutdown
   (readiness flips first), so a rolling restart is zero-loss.

## Compatibility notes

- **`ENCRYPTION_KEY` must not change** across an upgrade unless you run a key
  rotation (see the [encryption-at-rest checklist](encryption-at-rest.md)).
- JetStream stream config is reconciled on boot (`EnsureStreams`); the data
  stream is size-bounded (512 MiB / 1M msgs, discard-old) so a backlog can't
  OOM the broker. A stream is only updated when it differs on a field VRSky
  sets (subjects, retention, storage, max age, max bytes, max msgs, discard,
  duplicates window); a hand-made change to any other field, such as
  replicas, stays until one of those drifts.
- Check `/readyz` on each service after the roll; it reports NATS + DB health.

## Rollback

Roll the images back and, only if a migration is incompatible, apply its `down`
(after restoring the backup if data shape changed). Most migrations are additive
and need no down-migration to roll code back.

The file consumer keeps its list of published files as a full copy plus the
recent changes (#426); a file-consumer image from before that reads only the
full copy. A file consumer that stops normally writes the full copy first, so
rolling back after a normal deploy republishes nothing. If the pod crashed
instead, the older image republishes the files published since the last full
copy, at most 512 per pipeline.
