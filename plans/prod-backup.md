# Production backup for the management Postgres

## Open questions (for Ludvik)

1. Retention: 7 daily + 4 weekly snapshots, and 30 days of logical dumps.
   Enough, or does a customer contract say otherwise?
2. Where `ENCRYPTION_KEY` is kept outside the cluster (password manager or
   Key Vault). The logical dumps are encrypted with it, and the stored
   connector credentials are too. Lose the key and both are useless.
3. Is a copy in a second region wanted (Norway West), or is zone-redundant
   storage in Norway East enough for now?

## Goal

Production can lose the management Postgres (disk deleted, bad migration,
operator error, a tenant's data overwritten) and get back to a known state
from no more than a day ago, with a restore that has been tried at least
once.

Only the management Postgres needs backing up. NATS JetStream holds messages
in transit (72h limit, by design), and MinIO holds large payloads for
15 minutes. Neither is a system of record.

## What exists today (checked 2026-10-28, read-only)

- The Postgres data is one Azure managed disk:
  `pvc-152a98c3-c335-4003-85d6-a949a3758c35`, StandardSSD_ZRS, 50 GiB, in
  `MC_vrsky-prod_vrsky-prod_norwayeast` (PVC `postgres-data-postgresql-0`,
  namespace `vrsky-database`). ZRS protects against a zone failure, not
  against deletion or bad writes.
- No snapshot of that disk exists. The only snapshots are the four
  `*-before-shrink-20261022` ones for NATS and MinIO.
- No Azure Backup vault exists in `vrsky-prod`.
- `infrastructure/kubernetes/backup/cronjob.yaml` (daily `vrsky-cli backup`)
  is not applied by anything. Its image
  (`ghcr.io/valueretail/vrsky/vrsky-cli:latest`) is built by no workflow, and
  AKS pulls only from ACR. Its schedule is 02:00 UTC, which falls while the
  cluster is parked (16:00 to 07:00 Oslo, and weekends), so it would never
  run even if applied.
- `vrsky-cli backup` already supports Azure Blob
  (`BACKUP_PROVIDER=azure`, `BACKUP_AZURE_*`), encrypts with `ENCRYPTION_KEY`,
  and `vrsky-cli restore` exists. The DR drill (`dr-drill.yml`) restores a
  2-row table on PG16 in CI. Production runs PG18, and the drill has no
  schedule.

## Approach: two layers

### Layer 1: Azure Disk Backup of the Postgres disk (human, today)

An Azure Backup vault takes daily incremental snapshots of the disk on
Azure's side. It doesn't need the cluster to be running, which matters
because the cluster is stopped most of the time. Snapshots are
crash-consistent. Postgres recovers from those through its WAL like after a
power cut. Snapshots taken while the cluster is parked are of a cleanly
stopped disk.

This layer covers a lost or corrupted disk and a bad migration, at whole-
database granularity. It does not survive deleting the resource group or
the subscription.

First, take one snapshot by hand now, while the disk is unattached:

```bash
NRG=MC_vrsky-prod_vrsky-prod_norwayeast
DISK=pvc-152a98c3-c335-4003-85d6-a949a3758c35
az snapshot create -g "$NRG" -n postgres-manual-20261028 \
  --source "$(az disk show -g "$NRG" -n "$DISK" --query id -o tsv)" \
  --incremental true --sku Standard_ZRS
```

Then set up the vault and the daily policy. This needs Owner or User Access
Administrator for the role assignments.

```bash
SUB=8c020b54-b237-4d6e-82ba-190f6a415d1d
NRG=MC_vrsky-prod_vrsky-prod_norwayeast
DISK=pvc-152a98c3-c335-4003-85d6-a949a3758c35
DISK_ID=$(az disk show -g "$NRG" -n "$DISK" --query id -o tsv)

# 1. Vault with a system-assigned identity
az dataprotection backup-vault create -g vrsky-prod --vault-name vrsky-backup \
  -l norwayeast --type SystemAssigned \
  --storage-settings datastore-type=OperationalStore type=LocallyRedundant
VAULT_MSI=$(az dataprotection backup-vault show -g vrsky-prod --vault-name vrsky-backup \
  --query identity.principalId -o tsv)

# 2. The vault may read the disk and write snapshots into the node resource group
az role assignment create --assignee-object-id "$VAULT_MSI" --assignee-principal-type ServicePrincipal \
  --role "Disk Backup Reader" --scope "$DISK_ID"
az role assignment create --assignee-object-id "$VAULT_MSI" --assignee-principal-type ServicePrincipal \
  --role "Disk Snapshot Contributor" --scope "/subscriptions/$SUB/resourceGroups/$NRG"

# 3. Policy: daily, keep 7 days (tune with open question 1)
az dataprotection backup-policy get-default-policy-template --datasource-type AzureDisk > disk-policy.json
#    edit disk-policy.json: the schedule's repeatingTimeIntervals to a daily
#    time (e.g. R/2026-10-29T03:00:00+00:00/P1D) and the retention to P7D
az dataprotection backup-policy create -g vrsky-prod --vault-name vrsky-backup \
  -n postgres-daily --policy disk-policy.json

# 4. Protect the disk
az dataprotection backup-instance initialize --datasource-type AzureDisk \
  --datasource-id "$DISK_ID" -l norwayeast \
  --policy-id "$(az dataprotection backup-policy show -g vrsky-prod --vault-name vrsky-backup -n postgres-daily --query id -o tsv)" \
  > disk-instance.json
#    set snapshot_resource_group_name in disk-instance.json to $NRG
az dataprotection backup-instance create -g vrsky-prod --vault-name vrsky-backup \
  --backup-instance disk-instance.json

# 5. Check it
az dataprotection backup-instance list -g vrsky-prod --vault-name vrsky-backup -o table
```

Rough cost: incremental snapshots bill only for changed data, which here is
far below the 50 GiB disk. Expect a few NOK a month.

Gotchas:
- The snapshot lives in the node resource group. `az aks delete` deletes
  that group and the snapshots with it. Layer 2 covers that case.
- If the PVC is ever re-created (a new disk), the backup instance has to be
  re-pointed at the new disk.

### Layer 2: encrypted logical dumps to Blob storage (agent PRs plus human setup)

`vrsky-cli backup` runs `pg_dump -Fc`, gzips the dump, encrypts it with
`ENCRYPTION_KEY`, and uploads it to an Azure Blob container in a storage
account outside the node resource group. This layer covers losing the whole
cluster or its resource group, gives a portable dump (restore into any PG18),
and lets you restore one tenant's rows by hand.

Work, split by who can do it:

Agent PRs (all `agent:needs-human`, because they touch workflows, manifests
or `infrastructure/azure`):
1. Build `vrsky-cli` into ACR. Add it to `build-push-acr.sh`, `affected.sh`
   and the `build-push.yml` matrix, so the image is
   `vrskyprodacr.azurecr.io/vrsky/vrsky-cli`.
2. Rewrite `infrastructure/kubernetes/backup/cronjob.yaml`:
   - the ACR image
   - `schedule: "0 12 * * 1-5"` with `timeZone: Europe/Oslo`, while the
     cluster is up
   - `startingDeadlineSeconds` so a missed run doesn't fire at start-up
   - `imagePullSecrets: acr-pull`
   - a Secret example with the real AKS database and user (`vrsky`), and
     `BACKUP_PROVIDER=azure`
3. Add a deploy step (or a script the deploy job calls) that applies the
   CronJob and the `acr-pull` secret in `vrsky-database`. It must never
   overwrite `vrsky-backup-config`.
4. Add an alert: no successful `vrsky-mgmt-backup` Job within the last two
   weekdays the cluster ran. Use the kube-state-metrics Job metrics already in
   Prometheus.
5. Update the DR drill: PG18, a real schema (run the migrations, seed rows
   across two tenants), the built image rather than `go build`, and a weekly
   `schedule:`.
6. Docs: `docs/DR.md` and `docs/operator/backup.md` describe what now runs,
   and the RPO/RTO rows show measured numbers.

Human setup (needs keys and Owner rights):
- A storage account (StorageV2, ZRS, `--allow-blob-public-access false`,
  `--min-tls-version TLS1_2`) in `vrsky-prod`, a private container
  `vrsky-backups`, blob soft delete and versioning on, plus a lifecycle rule
  that deletes dumps older than 30 days. Consider a time-based immutability
  policy on the container so a stolen key can't delete the backups.
- The `vrsky-backup-config` Secret in `vrsky-database`, created with
  `kubectl create secret generic` from values you type yourself, never from
  a file in the repo:
  - `BACKUP_DB_URL`
  - `BACKUP_PROVIDER=azure`
  - `BACKUP_BUCKET=vrsky-backups`
  - `BACKUP_AZURE_ACCOUNT_NAME` and `BACKUP_AZURE_ACCOUNT_KEY`, or a
    connection string
  - `ENCRYPTION_KEY`
  Better, later: workload identity instead of an account key, the same
  pattern as #359.

### Restore runbooks (in `docs/DR.md`)

- **From a disk snapshot:**
  1. Stop the API and the connectors.
  2. Create a disk from the snapshot.
  3. Point a new PV at it, bind it to `postgres-data-postgresql-0`.
  4. Start Postgres, then the API.
  The exact commands get written and tried once against a scratch namespace.
- **From a dump:** `vrsky-cli restore --target-db-url ... --confirm <key>`
  into a fresh database, check row counts per tenant, then switch the API's
  `postgres-credentials` to it.

## Files the agent PRs touch

- `infrastructure/azure/build-push-acr.sh`
- `infrastructure/azure/affected.sh`
- `.github/workflows/build-push.yml`
- `.github/workflows/dr-drill.yml`
- `infrastructure/kubernetes/backup/cronjob.yaml` and `README.md`
- a new deploy step for the backup CronJob
- `infrastructure/monitoring` alert rules plus a `promtool` test
- `docs/DR.md`, `docs/operator/backup.md`
- guard tests in `src/pkg/managementapi/nodeconfig_test.go` (schedule, time
  zone, image registry, no secrets in the manifest)

## Risks

- **Backups no one has restored are not backups.** Done means one real
  restore from each layer into a scratch database, with row counts checked.
- **A lost `ENCRYPTION_KEY` makes the dumps unreadable.** Keep it outside
  Azure too (open question 2).
- **The backup holds every tenant's data,** including the encrypted
  credentials. Access to the storage account must be as tight as access to
  the database.
- **Weekday-noon dumps miss weekends.** That's acceptable because the
  cluster is parked then and nothing changes. Layer 1 runs every day
  regardless.
- **CPU budget:** the CronJob pod counts toward
  `TestCPURequestsStayWithinBudget`.

## How we know it's done

- [ ] A manual snapshot of the Postgres disk exists (Layer 1, first step).
- [ ] The backup vault shows a successful daily backup of the disk for
      3 days running.
- [ ] One restore from a snapshot into a scratch namespace, with row counts
      matching.
- [ ] `vrsky-mgmt-backup` runs from ACR on a weekday and writes an
      encrypted dump to the container.
- [ ] One `vrsky-cli restore` of that dump into a scratch PG18, with row
      counts per tenant matching.
- [ ] The alert fires when the Job is suspended for two running weekdays
      (`promtool` test), and is quiet otherwise.
- [ ] The DR drill runs weekly on PG18 against the real schema.
- [ ] `docs/DR.md` RPO/RTO rows show measured numbers; #389 merged or
      superseded.
