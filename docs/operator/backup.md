# Backup & restore

The durable state you must protect is the **management database** (connections,
secrets, tenants, audit log, OAuth grants) and the **`ENCRYPTION_KEY`**. NATS
JetStream is in-flight data and is bounded/replayable, not a backup target.

## What to back up

| Item | Why |
|------|-----|
| Management Postgres | All connection/tenant/secret/audit state |
| `ENCRYPTION_KEY` | Without it, encrypted secrets in the DB are unrecoverable |
| Per-tenant source/target DBs | If VRSky manages them; otherwise the customer's concern |

## Tooling

`vrsky-cli` provides `backup` / `list` / `restore`. Backups are encrypted
(`pkg/crypto` `EncryptBytes`/`DecryptBytes`). The bucket, credentials and
source database come from `BACKUP_*` environment variables and
`ENCRYPTION_KEY`, not from flags (see the [disaster-recovery guide](../DR.md)).

```bash
vrsky-cli backup     # dump, encrypt, upload as mgmt-backups/management_db-<timestamp>.dump.gz.enc
vrsky-cli list       # keys, sizes, ages
vrsky-cli restore --target-db-url 'postgres://…/management_db' --confirm \
  mgmt-backups/management_db-20260610T020000Z.dump.gz.enc
```

A Dockerfile (`src/cmd/vrsky-cli/Dockerfile`) and a daily Kubernetes CronJob
(`infrastructure/kubernetes/backup/`) exist, but nothing in CI builds or
publishes the image (only the compose `backup` profile builds it locally) and
nothing applies the CronJob. As far as the repo shows, no
scheduled backup runs in production; the
[disaster-recovery guide](../DR.md#what-runs-today) has the details.

## Restore drill

A restore is only real if it's been tested. CI runs a **restore drill**
(`.github/workflows/dr-drill.yml`) when the backup code changes: it backs up a
seeded test database to a MinIO in the runner, restores into a second
database, and checks the rows arrived. It tests the commands, not your backup
target or a production backup, so rehearse a restore against your own target
before you rely on it.

Full procedures, RPO/RTO targets and what the drill leaves out are in the
[disaster-recovery guide](../DR.md).
