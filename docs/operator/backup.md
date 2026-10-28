# Backup & restore

The durable state you must protect is the **management database** (connections,
secrets, tenants, audit log, OAuth grants) and the **`ENCRYPTION_KEY`**. NATS
JetStream is in-flight data and is bounded/replayable, not a backup target.

## What to back up

| Item | Why |
|------|-----|
| Management Postgres | All connection/tenant/secret/audit state |
| `ENCRYPTION_KEY` | Without it, encrypted secrets in the DB and the logical dumps are unrecoverable |
| Per-tenant source/target DBs | If VRSky manages them; otherwise the customer's concern |

## Production (AKS): two layers

| Layer | What | Runs | Kept |
|-------|------|------|------|
| 1. Azure Disk Backup | Incremental snapshot of the Postgres disk, by a backup vault | Daily, on Azure's side, also while the cluster is parked | 7 days, no weekly copies |
| 2. Logical dump | `vrsky-cli backup` from the CronJob `vrsky-mgmt-backup`, encrypted, to Blob storage | Weekdays 12:00 Oslo, while the cluster runs | 14 days (lifecycle rule) |

Layer 1 covers a lost or corrupted disk and a bad migration. Snapshots live in
the node resource group, so deleting the cluster deletes them too. Layer 2
lives in a storage account outside that group, so it survives losing the whole
cluster, and a dump can be restored into any Postgres 18.

There is no copy in a second region. Geo-redundant storage or object
replication to Norway West is a later option.

What the repo sets up for layer 2: CI builds `vrsky-cli` into ACR, and the
deploy job applies the CronJob through
`infrastructure/azure/deploy-backup-azure.sh`. The CronJob stays suspended
until the `vrsky-backup-config` Secret below exists. Everything else is the
one-time human setup in the next section. The repo can't show whether that
setup has been done; check with `kubectl -n vrsky-database get cronjob
vrsky-mgmt-backup` (SUSPEND) and `az dataprotection backup-instance list -g
vrsky-prod --vault-name vrsky-backup -o table`.

## One-time setup (human)

These need Owner (or User Access Administrator) on the subscription and
access to the cluster. Agents don't run them.

### Layer 1: backup vault for the Postgres disk

The commands are in `plans/prod-backup.md` (Layer 1). Use a daily schedule
and a retention of `P7D`, with no weekly rule. Take one manual snapshot first.

### Layer 2: storage account, container, lifecycle

```bash
RG=vrsky-prod
SA=<storage account name>   # 3-24 lowercase letters and digits, globally unique

az storage account create -g "$RG" -n "$SA" -l norwayeast \
  --kind StorageV2 --sku Standard_ZRS \
  --allow-blob-public-access false --min-tls-version TLS1_2

# Soft delete and versioning, so an overwrite or delete can be undone for 7 days
az storage account blob-service-properties update -g "$RG" --account-name "$SA" \
  --enable-delete-retention true --delete-retention-days 7 --enable-versioning true

# The private container the CronJob writes to
az storage container create --account-name "$SA" -n vrsky-backups \
  --public-access off --auth-mode key

# Delete dumps (and their old versions) 14 days after they were written
cat > lifecycle.json <<'JSON'
{"rules": [{"enabled": true, "name": "delete-dumps-after-14-days", "type": "Lifecycle",
  "definition": {
    "filters": {"blobTypes": ["blockBlob"], "prefixMatch": ["vrsky-backups/mgmt-backups/"]},
    "actions": {
      "baseBlob": {"delete": {"daysAfterModificationGreaterThan": 14}},
      "version":  {"delete": {"daysAfterCreationGreaterThan": 14}}}}}]}
JSON
az storage account management-policy create -g "$RG" --account-name "$SA" --policy @lifecycle.json
```

A time-based immutability policy on the container would stop a leaked account
key from deleting the backups. If you add one, keep its period at or below the
14 days, or the lifecycle rule can't delete.

Access to this account is access to every tenant's data, including the
encrypted connector credentials. Keep it as tight as access to the database.

### Layer 2: the `vrsky-backup-config` Secret

Paste each value at a silent prompt, so none of them lands in your shell
history or on screen, and never put them in a file in the repo:

```bash
read -rs DB_URL;  echo   # postgres://vrsky:<DB_PASSWORD>@postgresql.vrsky-database.svc.cluster.local:5432/vrsky?sslmode=disable
read -rs ENC_KEY; echo   # the platform ENCRYPTION_KEY, 64 hex characters
read -rs SA_NAME; echo   # the storage account name
read -rs SA_KEY;  echo   # az storage account keys list -g vrsky-prod -n <account> --query "[0].value" -o tsv

kubectl -n vrsky-database create secret generic vrsky-backup-config \
  --from-literal=BACKUP_DB_URL="$DB_URL" \
  --from-literal=ENCRYPTION_KEY="$ENC_KEY" \
  --from-literal=BACKUP_PROVIDER=azure \
  --from-literal=BACKUP_BUCKET=vrsky-backups \
  --from-literal=BACKUP_AZURE_ACCOUNT_NAME="$SA_NAME" \
  --from-literal=BACKUP_AZURE_ACCOUNT_KEY="$SA_KEY"

unset DB_URL ENC_KEY SA_NAME SA_KEY
```

- `BACKUP_DB_URL` is the `connection_string` of the `postgres-credentials`
  Secret in `vrsky-database`.
- `ENCRYPTION_KEY` is the `encryption_key` of `postgres-credentials` in
  `vrsky-platform`. The two copies must match. If the key is ever rotated,
  update this Secret too.
- `BACKUP_AZURE_CONNECTION_STRING` can replace the account name and key.

### Activate and try it

```bash
# Unsuspend (or run Catch up, which does the same)
kubectl -n vrsky-database patch cronjob vrsky-mgmt-backup --type merge -p '{"spec":{"suspend":false}}'

# One backup now, then read its log
kubectl -n vrsky-database create job --from=cronjob/vrsky-mgmt-backup backup-manual-$(date +%s)
kubectl -n vrsky-database logs -f job/<that job>
```

The log ends with `backup complete: mgmt-backups/management_db-<timestamp>.dump.gz.enc`.
Then restore that dump once into a scratch Postgres 18 and compare row counts
per tenant ([runbook](../DR.md#restore-from-a-logical-dump)). A backup nobody
has restored is not yet a backup.

### Keep the `ENCRYPTION_KEY` outside Azure

Store a copy in a password manager. The dumps are encrypted with it, and the
connector credentials inside the database are too. If the cluster and its
Secrets are lost and the key isn't somewhere else, the dumps are useless.

## Tooling

`vrsky-cli` provides `backup` / `list` / `restore`. The bucket, credentials
and source database come from `BACKUP_*` environment variables and
`ENCRYPTION_KEY`, not from flags.

```bash
vrsky-cli backup     # dump, encrypt, upload as mgmt-backups/management_db-<timestamp>.dump.gz.enc
vrsky-cli list       # keys, sizes, ages
vrsky-cli restore --target-db-url 'postgres://…/vrsky' --confirm \
  mgmt-backups/management_db-20261029T110000Z.dump.gz.enc
```

## Monitoring

`MgmtBackupMissing` (warning) fires when two of the CronJob's Jobs have failed
since the last good backup, or when neither a scheduled run nor a successful
Job (a manual one counts) has happened for over four days of being active
(checked weekday afternoons), or for over seven days at any time. It is
silent while the CronJob is suspended and right after it is unsuspended.

`MgmtBackupSuspended` (warning) covers the suspend that is never undone: it
fires when the CronJob has been suspended for over 7 days while
`vrsky-backup-config` exists and is over 7 days old. A restore that suspends
it for a few hours doesn't count, and nights the cluster is parked count as
suspended. Unsuspend it (above) or run Catch up. If it is suspended again
after that, the deploy log names the key the Secret is missing. Before the
Secret exists the CronJob is suspended on purpose and this alert is silent.
It reads the Secret's age from kube-state-metrics (`kube_secret_created`),
so recreating the Secret starts the 7 days again.

Both sit outside the `vrsky-*` rule groups, so they reach Teams but don't
fail a deploy's smoke check. A single failed Job also shows up as the
chart's `KubeJobFailed`.

## Restore drill

`.github/workflows/dr-drill.yml` runs weekly and on changes to the backup
code. It builds the `vrsky-cli` image, applies the real migrations to a
Postgres 18, seeds two tenants, backs up to an Azure Blob emulator, restores
into a second Postgres 18, and compares row counts per tenant. It tests the
image and the commands, not the production storage account or a production
backup.

Full procedures, RPO/RTO and both restore runbooks are in the
[disaster-recovery guide](../DR.md).
