# Management DB backups (#86, #395)

An encrypted logical backup of the management Postgres to Azure Blob storage,
taken by `vrsky-cli` from a CronJob. This is layer 2 of the production backup;
layer 1 is Azure Disk Backup of the Postgres disk. The runbook, retention and
restore steps are in [`docs/DR.md`](../../../docs/DR.md); the one-time setup
is in [`docs/operator/backup.md`](../../../docs/operator/backup.md).

## What it does

`vrsky-cli backup` runs `pg_dump` (custom format), gzips the dump, encrypts it
with AES-256-GCM using the platform `ENCRYPTION_KEY`, and uploads it to the
container `vrsky-backups` as
`mgmt-backups/management_db-<UTC timestamp>.dump.gz.enc`.

`cronjob.yaml`:

- image `vrskyprodacr.azurecr.io/vrsky/vrsky-cli:latest`, pulled at every run
  with the `acr-pull` secret. CI builds it into ACR when `src/cmd/vrsky-cli`
  or shared Go code changes.
- weekdays at 12:00 `Europe/Oslo`, while the cluster runs. It is parked
  16:00 to 07:00 and at weekends, and nothing changes then.
  `startingDeadlineSeconds: 3600` stops a run missed while parked from firing
  hours late at start-up.
- `concurrencyPolicy: Forbid`, one run at most an hour, two retries.
- runs as uid 70 (`postgres` in the image) with a read-only root filesystem
  and an `emptyDir` at `/tmp`.
- every credential comes from the `vrsky-backup-config` Secret. The manifest
  has none.

## How it gets applied

`infrastructure/azure/deploy-backup-azure.sh`, from the deploy job (when this
directory or the script changes) and from Catch up. It:

1. copies `acr-pull` from `vrsky-platform` into `vrsky-database`;
2. applies `cronjob.yaml`, which is suspended in the file;
3. unsuspends it when `vrsky-backup-config` exists with `BACKUP_DB_URL`,
   `ENCRYPTION_KEY`, `BACKUP_PROVIDER` and `BACKUP_BUCKET`, and otherwise
   leaves it suspended and logs which keys are missing.

The script reads the Secret's key names only. It never creates, changes or
prints the Secret.

## The Secret (a human creates it)

Values come from you, typed or read from the existing secrets, never from a
file in this repo. On AKS the database and its user are both `vrsky`.

```sh
kubectl -n vrsky-database create secret generic vrsky-backup-config \
  --from-literal=BACKUP_DB_URL='postgres://vrsky:<DB_PASSWORD>@postgresql.vrsky-database.svc.cluster.local:5432/vrsky?sslmode=disable' \
  --from-literal=ENCRYPTION_KEY='<the platform ENCRYPTION_KEY, 64 hex characters>' \
  --from-literal=BACKUP_PROVIDER=azure \
  --from-literal=BACKUP_BUCKET=vrsky-backups \
  --from-literal=BACKUP_AZURE_ACCOUNT_NAME='<storage account name>' \
  --from-literal=BACKUP_AZURE_ACCOUNT_KEY='<storage account key>'
```

`BACKUP_AZURE_CONNECTION_STRING` can replace the account name and key. The
database URL is the same one as `connection_string` in the
`postgres-credentials` Secret in `vrsky-database`.

Then activate the CronJob: run Catch up (Actions, Catch up, Run workflow), or

```sh
kubectl -n vrsky-database patch cronjob vrsky-mgmt-backup --type merge -p '{"spec":{"suspend":false}}'
```

## Operate

```sh
# Is it active? SUSPEND should be False, LAST SCHEDULE recent.
kubectl -n vrsky-database get cronjob vrsky-mgmt-backup

# A backup now, outside the schedule
kubectl -n vrsky-database create job --from=cronjob/vrsky-mgmt-backup backup-manual-$(date +%s)
kubectl -n vrsky-database logs -f job/<that job>

# List backups
IMAGE=vrskyprodacr.azurecr.io/vrsky/vrsky-cli:latest
kubectl -n vrsky-database run vrsky-cli-list --rm -it --restart=Never --image="$IMAGE" \
  --overrides='{"spec":{"imagePullSecrets":[{"name":"acr-pull"}],"containers":[{"name":"c","image":"'"$IMAGE"'","args":["list"],"envFrom":[{"secretRef":{"name":"vrsky-backup-config"}}]}]}}'
```

A suspend by hand lasts until the next deploy that touches this directory or
the next Catch up; both set `suspend` from the Secret again.

The alert `MgmtBackupMissing` (`infrastructure/prometheus-rules.yml`) fires
when two of its Jobs have failed since the last good backup, or when it hasn't
been scheduled for over four days, and stays quiet while it is suspended.

> **The `ENCRYPTION_KEY` is not in the backup.** The dump is encrypted with
> it, and the connector credentials inside it are too. Keep a copy outside
> Azure, in a password manager. Without the key the dumps are useless.
