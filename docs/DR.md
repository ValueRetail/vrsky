# Disaster Recovery — management database (Phase 3C, #86)

This is the runbook for backing up and restoring the **management Postgres** —
the database that holds every tenant, connection/pipeline definition, encrypted
secret reference, OAuth provider/grant, notification target, and the audit log.
Losing it loses the platform's control plane, so it is backed up daily and the
restore path is tested in CI.

| Objective | Target | How it's met |
|-----------|--------|--------------|
| **RPO** (max data loss) | **≤ 24h** | Daily backup CronJob (`infrastructure/kubernetes/backup/cronjob.yaml`, 02:00 UTC) |
| **RTO** (max time to restore) | **≤ 1h** | Single `vrsky-cli restore` of a small dump; CronJob has a 1h `activeDeadlineSeconds` |

> **Scope:** the management DB only. Tenant *data in flight* lives in NATS
> JetStream (durable, replicated) and large payloads in object storage; those
> have their own durability and are out of scope here.

## How a backup works
`vrsky-cli backup` (shipped in the `vrsky-cli` image):
1. `pg_dump --format=custom` of the management DB,
2. gzip,
3. encrypt with **AES-256-GCM** using the platform `ENCRYPTION_KEY` (the same
   master key that protects the secrets vault — see `SECURITY.md`),
4. upload to object storage (S3 / Azure / GCS via `pkg/objectstore`) as
   `mgmt-backups/management_db-<UTC-timestamp>.dump.gz.enc`.

Config is environment-driven (`BACKUP_DB_URL`, `ENCRYPTION_KEY`,
`BACKUP_PROVIDER`/`BACKUP_BUCKET`/`BACKUP_ENDPOINT`/`BACKUP_REGION`/
`BACKUP_ACCESS_KEY_ID`/`BACKUP_SECRET_ACCESS_KEY`, or the Azure/GCS equivalents).
Deployment + the Secret are documented in
[`infrastructure/kubernetes/backup/README.md`](../infrastructure/kubernetes/backup/README.md).

> ⚠️ **The `ENCRYPTION_KEY` is NOT in the backup** — and it can't be: the dump is
> encrypted with it, and the dump's `*_secret_id` ciphertext is only decryptable
> with it. Store the key in a separate secret manager / sealed secret. **A backup
> without the key is unrecoverable.** This is the one thing object-store
> versioning can't protect you from, so guard the key independently.

## Restoring (the recovery procedure)

1. **Stand up a fresh, empty Postgres** (the new management DB) of the **same
   major version as the backed-up server, or newer** — a dump does not restore
   cleanly into an *older* major (e.g. a PG 18 dump won't fully apply to PG 16) —
   and make sure the **same `ENCRYPTION_KEY`** as the lost instance is available.

2. **Find the backup to restore:**
   ```sh
   vrsky-cli list      # prints keys, sizes, ages — pick the most recent good one
   ```

3. **Restore into the new database:**
   ```sh
   vrsky-cli restore \
     --target-db-url 'postgres://postgres:PASSWORD@NEW-HOST:5432/management_db?sslmode=disable' \
     --confirm \
     mgmt-backups/management_db-20260610T020000Z.dump.gz.enc
   ```
   `restore` downloads → decrypts → gunzips → `pg_restore --clean --if-exists`.
   It **requires** `--target-db-url` and `--confirm` (restore is destructive), and
   refuses to overwrite the configured source DB unless you also pass
   `--allow-source-db`.

4. **Bring migrations up to date.** A backup is a point-in-time snapshot at
   whatever migration version was live then. After restoring, run the migrations
   so the schema matches the current code:
   ```sh
   migrate -path infrastructure/migrations -database "$TARGET_DB_URL" up
   ```
   (The management-api also runs migrations on boot, so simply starting the new
   management-api against the restored DB achieves the same.)

5. **Point the platform at the restored DB** (update `MGMT_API_DB_URL`) and start
   the management-api. Verify: log in, list connections, confirm secrets decrypt
   (they will, given the matching `ENCRYPTION_KEY`).

## The CI restore drill
`.github/workflows/dr-drill.yml` proves this path on every change to the backup
tooling: it seeds a source Postgres, runs `vrsky-cli backup` to MinIO, runs
`vrsky-cli restore` into a *separate* target Postgres, and asserts the sentinel
rows survived. A green run is the standing guarantee that the backup format and
restore command actually work — not just that they compile.

## Local dry-run (compose)
```sh
docker compose up -d postgres-management minio-test minio-init
docker compose run --rm backup            # backup the local mgmt DB → MinIO
docker compose run --rm backup list       # see it
# restore into a throwaway DB to rehearse (never the live one without intent)
```
