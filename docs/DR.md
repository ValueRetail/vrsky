# Disaster Recovery — management database (Phase 3C, #86)

This is the runbook for backing up and restoring the **management Postgres** —
the database that holds every tenant, connection/pipeline definition, encrypted
secret reference, OAuth provider/grant, notification target, and the audit log.
Losing it loses the platform's control plane.

## What runs today

As far as this repo shows, **no scheduled backup of the production database
runs**:

- The daily CronJob in `infrastructure/kubernetes/backup/cronjob.yaml`
  (02:00 UTC) is not applied by any script or workflow.
- Its image, `ghcr.io/valueretail/vrsky/vrsky-cli:latest`, is built by no
  workflow or script, and AKS can't pull from `ghcr.io`. Applied as it is, the
  CronJob's pods would fail to pull. Item 27 in `plans/backlog.md` tracks this.
- No script or workflow snapshots the Postgres volume (an Azure disk through
  the `managed-csi` storage class).
- The [CI restore drill](#the-ci-restore-drill) tests the backup and restore
  commands, not a production backup.

The repo can't show whether someone applied the CronJob by hand or set up
backups on the Azure side. Until one of those is confirmed, assume there are
none.

| Objective | Target | Status |
|-----------|--------|--------|
| **RPO** (max data loss) | **≤ 24h** | Not met while no backup runs. The CronJob would meet it once it can pull an image and is applied. |
| **RTO** (max time to restore) | **≤ 1h** | Depends on having a backup. With one, a single `vrsky-cli restore` of the small management dump into a fresh DB runs in minutes. |

> **Scope:** the management DB only. Tenant *data in flight* lives in NATS
> JetStream (durable, replicated) and large payloads in object storage; those
> have their own durability and are out of scope here.

## How a backup works
`vrsky-cli backup` (source in `src/cmd/vrsky-cli/`; its image is built from
the `Dockerfile` there, which today only the compose `backup` profile does):
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
[`infrastructure/kubernetes/backup/README.md`](https://github.com/ValueRetail/vrsky/blob/main/infrastructure/kubernetes/backup/README.md).

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
`.github/workflows/dr-drill.yml` runs on pushes and pull requests to `main` or
`develop` that change `src/cmd/vrsky-cli/`, `src/pkg/crypto/`, `src/pkg/objectstore/`,
`infrastructure/migrations/` or the workflow itself, and can be started by
hand. It has no schedule. It:

1. builds `vrsky-cli` from source with `go build`,
2. starts two `postgres:16-alpine` databases and a MinIO in the runner,
3. seeds the source with one table and two sentinel rows,
4. runs `vrsky-cli backup` to MinIO, then `vrsky-cli restore` into the
   separate target,
5. checks that both rows arrived.

A green run shows the backup format and the restore command work end to end.
It doesn't cover the CronJob, its image or its Secret, the real schema and
migrations, Postgres 18 (what production runs), or any production storage, and
it says nothing about whether a production backup exists.

## Local dry-run (compose)
```sh
docker compose up -d postgres-management minio-test minio-init
docker compose run --rm backup            # backup the local mgmt DB → MinIO
docker compose run --rm backup list       # see it
# restore into a throwaway DB to rehearse (never the live one without intent)
```
