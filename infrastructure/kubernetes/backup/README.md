# Management DB backups (Phase 3C — #86)

Daily encrypted backup of the management Postgres to object storage, via the
`vrsky-cli` CronJob. See [`docs/DR.md`](../../../docs/DR.md) for the full
disaster-recovery runbook (restore steps, RPO/RTO, the encryption-key caveat).

## What it does
`vrsky-cli backup` runs `pg_dump` (custom format) → gzip → encrypt with
AES-256-GCM (the shared `ENCRYPTION_KEY`) → upload to the configured bucket as
`mgmt-backups/management_db-<timestamp>.dump.gz.enc`. The CronJob runs at
02:00 UTC daily, so the recovery point objective (RPO) is ≤ 24h.

## Setup
1. Build & push the CLI image (handled by CI as `vrsky-cli`), or build locally.
2. Create the config Secret with the object-store target + the master key:

```sh
kubectl -n vrsky-database create secret generic vrsky-backup-config \
  --from-literal=BACKUP_DB_URL='postgres://postgres:PASSWORD@postgresql.vrsky-database.svc.cluster.local:5432/management_db?sslmode=disable' \
  --from-literal=ENCRYPTION_KEY='<the same 64-hex key the platform uses>' \
  --from-literal=BACKUP_PROVIDER='s3' \
  --from-literal=BACKUP_BUCKET='vrsky-mgmt-backups' \
  --from-literal=BACKUP_REGION='eu-north-1' \
  --from-literal=BACKUP_ACCESS_KEY_ID='<key>' \
  --from-literal=BACKUP_SECRET_ACCESS_KEY='<secret>'
  # Azure/GCS: use BACKUP_AZURE_* / BACKUP_GCS_CREDENTIALS_JSON instead.
```

3. Apply the CronJob:

```sh
kubectl apply -f infrastructure/kubernetes/backup/cronjob.yaml
```

## Verify / operate
```sh
# Trigger an out-of-schedule backup
kubectl -n vrsky-database create job --from=cronjob/vrsky-mgmt-backup backup-manual-$(date +%s)

# List backups
kubectl -n vrsky-database run vrsky-cli-list --rm -it --restart=Never \
  --image=ghcr.io/valueretail/vrsky/vrsky-cli:latest \
  --overrides='{"spec":{"containers":[{"name":"c","image":"ghcr.io/valueretail/vrsky/vrsky-cli:latest","args":["list"],"envFrom":[{"secretRef":{"name":"vrsky-backup-config"}}]}]}}'
```

> ⚠️ **The `ENCRYPTION_KEY` is not in the backup.** Backups are encrypted with
> it and the dump's `*_secret_id` ciphertext can only be decrypted with it — store
> the key in a separate secret manager. A backup without the key is unrecoverable.
