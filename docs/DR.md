# Disaster Recovery — management database (#86, #395)

This is the runbook for backing up and restoring the **management Postgres**,
the database that holds every tenant, connection/pipeline definition, encrypted
secret reference, OAuth provider/grant, notification target, and the audit log.
Losing it loses the platform's control plane.

> **Scope:** the management DB only. Tenant *data in flight* lives in NATS
> JetStream (72h limit, by design) and large payloads in object storage for
> minutes; neither is a system of record.

## What runs today

Production has two backup layers. The repo sets up part of the second; the
rest is a one-time human setup in
[Backup & restore](operator/backup.md#one-time-setup-human).

| Layer | What | Set up by | Retention |
|-------|------|-----------|-----------|
| 1. Azure Disk Backup | Daily incremental snapshot of the Postgres disk (`pvc-152a98c3-…`, node resource group) by the vault `vrsky-backup` | Human | 7 days, no weekly copies |
| 2. Logical dump | CronJob `vrsky-mgmt-backup`: `vrsky-cli backup`, weekdays 12:00 Oslo, encrypted, to the Blob container `vrsky-backups` | Repo (image, CronJob, alert) plus human (storage account, Secret) | 14 days (lifecycle rule) |

From the repo side, CI builds `vrskyprodacr.azurecr.io/vrsky/vrsky-cli`, and
the deploy job and Catch up apply the CronJob with
`infrastructure/azure/deploy-backup-azure.sh`. The CronJob stays **suspended**
until the `vrsky-backup-config` Secret exists, so no backup runs before the
human setup is done. Whether it has been done can't be told from the repo:

```bash
kubectl -n vrsky-database get cronjob vrsky-mgmt-backup          # SUSPEND False, LAST SCHEDULE recent
az dataprotection backup-instance list -g vrsky-prod --vault-name vrsky-backup -o table
```

There is no copy in a second region. Geo-redundant storage or object
replication to Norway West is a later option.

| Objective | Target | Status |
|-----------|--------|--------|
| **RPO** (max data loss) | **≤ 24h** | Layer 1 once the vault runs (daily). Layer 2 once its Secret exists: the last weekday noon; nothing changes while the cluster is parked. Not measured yet. |
| **RTO** (max time to restore) | **≤ 1h** | An estimate: a restore of the small management dump runs in minutes in the CI drill. Not measured on a production-sized dump or from a snapshot. |

Replace the Status column with measured numbers after the first real restore
from each layer.

> **The `ENCRYPTION_KEY` is not in the backup.** The dumps are encrypted with
> it, and the dump's `*_secret_id` ciphertext only decrypts with it. Keep a
> copy outside Azure, in a password manager. **Without it the dumps are
> useless.** A disk snapshot holds the same ciphertext, so it needs the same
> key once the cluster's own Secrets are gone.

## How a logical backup works

`vrsky-cli backup` (`src/cmd/vrsky-cli/`, image built from its `Dockerfile`
on a `postgres:18-alpine` base, so `pg_dump` matches the server):

1. `pg_dump --format=custom` of the management DB,
2. gzip,
3. encrypt with **AES-256-GCM** using the platform `ENCRYPTION_KEY` (the same
   master key that protects the secrets vault, see `SECURITY.md`),
4. upload to object storage (Azure in production; S3 and GCS also work, via
   `pkg/objectstore`) as
   `mgmt-backups/management_db-<UTC-timestamp>.dump.gz.enc`.

The four steps are one stream (#430): nothing holds the whole dump, so the
Job's memory stays at a few MiB plus the storage SDK's upload buffers
whatever the size of the database. The archive is encrypted in 64 KiB
chunks, each its own GCM seal with a counter and a last-chunk flag in the
nonce, behind a `VRSKYBK2` header. The chunks are sealed under a key made
for that archive alone, HKDF-SHA256 of the `ENCRYPTION_KEY` with a random
salt stored in the header, so the backups never use the secrets vault's key
directly (the format is documented in `src/cmd/vrsky-cli/stream.go`). A
cut-off, reordered or edited archive fails to decrypt; it never restores as
a shorter dump. If `pg_dump` fails, the upload is aborted before the last
chunk is written.

The header also names the object key the archive was uploaded under and
when the backup started (#549). Every chunk authenticates the header, and
`backup verify` and `restore` refuse an archive whose header names a
different key than the one they were given. So someone who can write to
the bucket can't copy an older backup over the newest key and have it
restored as if it were the newest. They can still delete backups; that is
for the storage account's access control and soft delete. The binding
covers the whole key, prefix included, but not the bucket: copying an
archive to another storage account under the same key works, renaming it
doesn't. To restore a renamed archive, copy it back to the key its header
names (the error shows it) and run `backup verify` on that key. The header
isn't proven until verify has read the archive, so a forged one can name
any key; verify then fails.

`vrsky-cli restore` also reads the two older formats: `VRSKYBK1` (#430,
the same chunks without the key in the header) and the archives made
before #430 (one GCM seal under the `ENCRYPTION_KEY` over the whole
gzipped dump). Neither says which backup it is, so both `verify` and
`restore` refuse them unless you pass `--allow-unbound`, and then print a
warning. Without that flag an attacker could put any old archive, from any
time the same `ENCRYPTION_KEY` was in use, under the newest key. The newest
backup is `VRSKYBK2` from the first CronJob run after the change is
deployed, so the flag is only for deliberately going back further than
that; the old formats age out of the 14-day retention after two weeks.

New archives are streamed into `pg_restore`; pre-#430 ones are read into
memory to decrypt, as before. A damaged chunked archive would only be
caught while `pg_restore` runs, so run `vrsky-cli backup verify <key>`
first, as the runbook below does: it reads the whole archive and fails on
any damage without touching a database.

Config is environment-driven: `BACKUP_DB_URL`, `ENCRYPTION_KEY`,
`BACKUP_PROVIDER`, `BACKUP_BUCKET`, and `BACKUP_AZURE_ACCOUNT_NAME` +
`BACKUP_AZURE_ACCOUNT_KEY` or `BACKUP_AZURE_CONNECTION_STRING` (the S3 and GCS
equivalents otherwise). The CronJob takes them from the `vrsky-backup-config`
Secret; see
[`infrastructure/kubernetes/backup/README.md`](https://github.com/ValueRetail/vrsky/blob/main/infrastructure/kubernetes/backup/README.md).

The CronJob runs as uid 70 with a read-only root filesystem, pulls `:latest`
at every run, and is watched by the alert `MgmtBackupMissing`: two failed
Jobs since the last good backup, or no scheduled run and no successful Job
for over four days of being active (seven at weekends and outside the
afternoon check), while it isn't suspended. `MgmtBackupSuspended` fires
when it has been suspended for over 7 days while `vrsky-backup-config` has
existed for over 7 days. `MgmtBackupCronJobAbsent` fires when the CronJob
has been gone for 30 minutes while `vrsky-backup-config` exists.

## Restore from a disk snapshot

Use this when the disk is lost or corrupted, or after a bad migration, and
the cluster still exists. It brings back the whole database as of the
snapshot. **Not yet rehearsed**: run it once against a scratch namespace
before you rely on it, and fix this page where reality differs.

```bash
SUB=8c020b54-b237-4d6e-82ba-190f6a415d1d
NRG=MC_vrsky-prod_vrsky-prod_norwayeast
STAMP=$(date +%Y%m%d)
```

1. **Stop everything that writes, the backup CronJob included.** A dump taken
   mid-restore is worse than none, and it would push a good dump out of the
   14 days sooner. Suspend the CronJob first:
   ```bash
   kubectl -n vrsky-database patch cronjob vrsky-mgmt-backup --type merge -p '{"spec":{"suspend":true}}'
   ```
   The next deploy that touches the backup, and every Catch up, set
   `suspend` from the `vrsky-backup-config` Secret again and would turn it
   back on. For a restore that may span one of those, rename the Secret
   out of the way (`kubectl -n vrsky-database get secret vrsky-backup-config
   -o json | jq '.metadata.name="vrsky-backup-config-paused" | del(.metadata.uid,.metadata.resourceVersion,.metadata.creationTimestamp)' | kubectl apply -f -`,
   then delete the original) and rename it back at the end, or expect the
   CronJob to resume. A suspend that outlasts 7 days with the Secret in
   place raises `MgmtBackupSuspended`; with the Secret renamed it stays
   quiet. Then record the replica counts and scale the platform
   and Postgres down:
   ```bash
   kubectl -n vrsky-platform get deploy -o jsonpath='{range .items[*]}{.metadata.name} {.spec.replicas}{"\n"}{end}' > replicas.txt
   kubectl -n vrsky-platform scale deploy --all --replicas=0
   kubectl -n vrsky-database scale statefulset postgresql --replicas=0
   ```
2. **Keep the old disk.** The dynamic PV deletes its disk when its claim goes,
   so switch it to Retain first:
   ```bash
   OLD_PV=$(kubectl -n vrsky-database get pvc postgres-data-postgresql-0 -o jsonpath='{.spec.volumeName}')
   kubectl patch pv "$OLD_PV" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}'
   ```
3. **Make a disk from the restore point.** From the vault (pick the point,
   restore as a new disk into the node resource group):
   ```bash
   az dataprotection recovery-point list -g vrsky-prod --vault-name vrsky-backup \
     --backup-instance-name <instance> -o table
   az dataprotection backup-instance restore initialize-for-data-recovery \
     --datasource-type AzureDisk --restore-location norwayeast --source-datastore OperationalStore \
     --recovery-point-id <id> \
     --target-resource-id "/subscriptions/$SUB/resourceGroups/$NRG/providers/Microsoft.Compute/disks/postgres-restored-$STAMP" \
     > restore.json
   az dataprotection backup-instance restore trigger -g vrsky-prod --vault-name vrsky-backup \
     --backup-instance-name <instance> --restore-request-object restore.json
   ```
   Or from a plain snapshot:
   ```bash
   az disk create -g "$NRG" -n "postgres-restored-$STAMP" --sku StandardSSD_ZRS \
     --source "$(az snapshot show -g "$NRG" -n <snapshot> --query id -o tsv)"
   ```
4. **Bind it to the Postgres claim.** Delete the old claim, create a PV for
   the new disk that is reserved for `postgres-data-postgresql-0`; the
   StatefulSet recreates the claim and it binds to this PV:
   ```bash
   kubectl -n vrsky-database delete pvc postgres-data-postgresql-0
   DISK_ID=$(az disk show -g "$NRG" -n "postgres-restored-$STAMP" --query id -o tsv)
   kubectl apply -f - <<EOF
   apiVersion: v1
   kind: PersistentVolume
   metadata:
     name: postgres-restored-$STAMP
   spec:
     capacity: { storage: 50Gi }
     accessModes: [ReadWriteOnce]
     persistentVolumeReclaimPolicy: Retain
     storageClassName: managed-csi
     claimRef: { namespace: vrsky-database, name: postgres-data-postgresql-0 }
     csi: { driver: disk.csi.azure.com, volumeHandle: "$DISK_ID" }
   EOF
   ```
5. **Start Postgres, check, then the platform.** Postgres replays its WAL like
   after a power cut.
   ```bash
   kubectl -n vrsky-database scale statefulset postgresql --replicas=1
   kubectl -n vrsky-database rollout status statefulset postgresql
   kubectl -n vrsky-database exec postgresql-0 -- psql -U vrsky -d vrsky -c \
     "SELECT t.slug, count(c.id) FROM tenants t LEFT JOIN connections c ON c.tenant_id = t.id::text GROUP BY t.slug ORDER BY 1"
   while read -r d n; do kubectl -n vrsky-platform scale deploy "$d" --replicas="$n"; done < replicas.txt
   ```
6. Re-point the vault's backup instance at the new disk (it protects the old
   disk ID), and delete the old disk once you're sure.
7. Resume the dumps: rename the Secret back if you moved it, then run Catch
   up or `kubectl -n vrsky-database patch cronjob vrsky-mgmt-backup --type merge -p '{"spec":{"suspend":false}}'`.

## Restore from a logical dump

Use this when the cluster or its resource group is gone, to restore into a
different server, or to recover one tenant's rows by hand. The target must be
Postgres 18 (a dump does not restore cleanly into an older major), and you
need the **same `ENCRYPTION_KEY`** as when the dump was taken.

1. **Stand up an empty Postgres 18.** In the cluster, or anywhere `vrsky-cli`
   can reach. For a rehearsal, a scratch pod is enough:
   ```bash
   kubectl -n vrsky-database run pg-restore-test --image=postgres:18-alpine \
     --env POSTGRES_USER=vrsky --env POSTGRES_DB=vrsky --env POSTGRES_PASSWORD=<scratch password> --port 5432
   kubectl -n vrsky-database expose pod pg-restore-test --port 5432
   ```
2. **Pick the dump:**
   ```bash
   IMAGE=vrskyprodacr.azurecr.io/vrsky/vrsky-cli:latest
   kubectl -n vrsky-database run vrsky-cli-list --rm -it --restart=Never --image="$IMAGE" \
     --overrides='{"spec":{"imagePullSecrets":[{"name":"acr-pull"}],"containers":[{"name":"c","image":"'"$IMAGE"'","args":["list"],"envFrom":[{"secretRef":{"name":"vrsky-backup-config"}}]}]}}'
   ```
3. **Verify the dump before you restore it:**
   ```bash
   KEY=mgmt-backups/management_db-<timestamp>.dump.gz.enc
   kubectl -n vrsky-database run vrsky-cli-verify --rm -it --restart=Never --image="$IMAGE" \
     --overrides='{"spec":{"imagePullSecrets":[{"name":"acr-pull"}],"containers":[{"name":"c","image":"'"$IMAGE"'","args":["backup","verify","'"$KEY"'"],"envFrom":[{"secretRef":{"name":"vrsky-backup-config"}}]}]}}'
   ```
   `backup verify` downloads the dump and decrypts and gunzips it without
   writing anything, then prints
   `backup ok: <key> (<n> bytes of dump; VRSKYBK2, made <time>)`. Check
   that the time is the one you expect for that key. A non-zero exit means
   the dump is damaged, cut off, under another `ENCRYPTION_KEY`, or was
   made under another key and copied or renamed to this one (the error
   names the key its header claims): pick another one. Don't skip this.
   `restore` would notice a damaged dump only partway through and roll
   back, so you'd find out later and with less detail.

   A dump made before `VRSKYBK2` (#549) is refused unless you add
   `--allow-unbound` after `verify` (and after `restore` in step 4). Those
   archives don't say which backup they are, so nothing proves the file
   under that key is the backup made then rather than an older one copied
   over it. Only pass the flag when you mean to restore a dump from before
   the change, and expect the warning it prints. They age out of the
   14-day retention two weeks after the change reaches production.
4. **Restore into it:**
   ```bash
   TARGET='postgres://vrsky:<scratch password>@pg-restore-test.vrsky-database.svc.cluster.local:5432/vrsky?sslmode=disable'
   kubectl -n vrsky-database run vrsky-cli-restore --rm -it --restart=Never --image="$IMAGE" \
     --overrides='{"spec":{"imagePullSecrets":[{"name":"acr-pull"}],"containers":[{"name":"c","image":"'"$IMAGE"'","args":["restore","--target-db-url","'"$TARGET"'","--confirm","'"$KEY"'"],"envFrom":[{"secretRef":{"name":"vrsky-backup-config"}}]}]}}'
   ```
   `restore` downloads, decrypts, gunzips and runs
   `pg_restore --clean --if-exists --single-transaction`. The single
   transaction makes it all or nothing: if the download breaks off, the
   archive turns out damaged (say it was replaced after you verified it) or
   any statement fails, everything rolls back, the `--clean` drops included,
   and the target is left as it was. It requires `--target-db-url` and
   `--confirm`, and refuses to overwrite `BACKUP_DB_URL` unless you also pass
   `--allow-source-db`.

   Without the cluster, run the same image anywhere with Docker and the
   environment from the [Secret](operator/backup.md#layer-2-the-vrsky-backup-config-secret),
   with `ENCRYPTION_KEY` from the password manager. Verify first with the
   same command ending in `backup verify <key>` (it needs no
   `BACKUP_DB_URL`), then restore:
   `docker run --rm -e ENCRYPTION_KEY -e BACKUP_PROVIDER=azure -e BACKUP_BUCKET=vrsky-backups -e BACKUP_AZURE_ACCOUNT_NAME -e BACKUP_AZURE_ACCOUNT_KEY -e BACKUP_DB_URL=<any URL other than the target> vrskyprodacr.azurecr.io/vrsky/vrsky-cli:latest restore --target-db-url <url> --confirm <key>`.
5. **Check row counts per tenant** against what you expect (the live
   database, if it still exists):
   ```sql
   SELECT t.slug, 'connections', count(c.id) FROM tenants t
     LEFT JOIN connections c ON c.tenant_id = t.id::text GROUP BY t.slug
   UNION ALL
   SELECT t.slug, 'secrets', count(s.id) FROM tenants t
     LEFT JOIN secrets s ON s.tenant_id = t.id GROUP BY t.slug
   ORDER BY 1, 2;
   ```
6. **Bring the schema up to date.** A dump is at whatever migration version
   was live when it was taken. The management-api runs the migrations on
   boot, so starting it against the restored database is enough.
7. **Switch the platform to it.** Update `connection_string` in the
   `postgres-credentials` Secrets (`vrsky-database` and `vrsky-platform`) to
   the restored database, restart the management-api and the connectors, and
   check: log in, list connections, open a connection's credentials (they
   decrypt only with the matching `ENCRYPTION_KEY`).

Clean up a rehearsal with
`kubectl -n vrsky-database delete pod,svc pg-restore-test`.

## The CI restore drill

`.github/workflows/dr-drill.yml` runs every Wednesday at 06:00 UTC, on
pushes and pull requests to `main` or `develop` that change
`src/cmd/vrsky-cli/`, `src/pkg/crypto/`, `src/pkg/objectstore/`,
`infrastructure/migrations/` or the workflow, and by hand. It:

1. builds the `vrsky-cli` image from its Dockerfile,
2. starts two `postgres:18-alpine` databases (`vrsky`/`vrsky`, like AKS) and
   Azurite, an Azure Blob emulator, with the container `vrsky-backups`,
3. applies every migration in `infrastructure/migrations` to the source and
   seeds users, tenants, connections and secrets for two tenants,
4. runs `vrsky-cli backup` with `BACKUP_PROVIDER=azure`, then
   `vrsky-cli restore` into the separate target, both as uid 70 with a
   read-only root filesystem, like the CronJob,
5. compares row counts per tenant on the target with the source and the seed.

A green run shows the image, the real schema, the Azure upload and the
restore work end to end. It doesn't touch the production storage account,
uses a connection string where production may use an account key, and says
nothing about whether a production backup exists. A red run on main opens an
issue (`ci-failure-alert.yml`).

## Local dry-run (compose)
```sh
docker compose up -d postgres-management minio-test minio-init
docker compose run --rm backup            # backup the local mgmt DB → MinIO
docker compose run --rm backup list       # see it
# restore into a throwaway DB to rehearse (never the live one without intent)
```
