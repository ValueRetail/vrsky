# PostgreSQL Deployment

This directory contains Kubernetes manifests for deploying PostgreSQL 18 as the VRSky platform database.

## Architecture

- **Type**: StatefulSet, 1 replica. Production on AKS runs this one.
- **Version**: PostgreSQL 18 Alpine
- **Resources**: limits 4 CPU, 8Gi RAM (requests: 250m CPU, 512Mi RAM, a
  scheduling floor for small nodes)
- **Storage**: one 50Gi volume. The manifest asks for `local-path` (k3d);
  `infrastructure/azure/deploy-azure.sh` swaps that for `managed-csi` on AKS,
  so production keeps it on a single Azure managed disk
- **Persistence**: one pod, no database replication. The production disk
  is StandardSSD ZRS, which survives a zone failure but not deletion or bad
  writes; recovery from those relies on the disk snapshots and the logical
  dumps (below, and [docs/DR.md](../../../docs/DR.md))

## High Availability (production) — #137

Production runs the single-replica `statefulset.yaml` (`postgresql` in
`vrsky-database`), applied by `infrastructure/azure/deploy-azure.sh` at the
first bring-up and not by the deploy job. It has **no HA**: a pod or node
failure is a control-plane outage until the pod is back. It has two backup
layers: daily Azure Disk Backup snapshots of its disk and the weekday
`vrsky-mgmt-backup` CronJob's logical dumps. [docs/DR.md](../../../docs/DR.md)
says how to check that both run.

No CNPG cluster or managed database runs today. To add HA, choose one of the
options below. Have a backup in place for the new database before moving:
the disk snapshots only cover the StatefulSet's disk.

### Option A — CloudNativePG (self-hosted HA)

A 3-instance cluster (primary + 2 replicas), automated failover, one
synchronous standby (zero data loss on promotion), and PgBouncer pooling.

```bash
# 1. Install the operator (once per cluster)
kubectl apply --server-side -f \
  https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.24/releases/cnpg-1.24.0.yaml

# 2. App role secret — keys MUST be username/password (CNPG convention)
kubectl -n vrsky-database create secret generic postgres-app-credentials \
  --from-literal=username=vrsky --from-literal=password='<strong-password>'

# 3. Schema ConfigMap (single-sources init-schema.sql)
kubectl -n vrsky-database create configmap vrsky-pg-init-schema \
  --from-file=init.sql=init-schema.sql

# 4. Apply the cluster + pooler
kubectl apply -f cnpg-cluster.yaml -f cnpg-pooler.yaml
```

Then point the app at the **pooler** (not the primary directly) by setting the
`connection_string` key in the `postgres-credentials` secret to:

```
postgres://vrsky:<password>@vrsky-pg-pooler-rw.vrsky-database.svc.cluster.local:5432/vrsky?sslmode=require
```

CNPG also exposes `vrsky-pg-rw` (primary), `vrsky-pg-ro` (replicas), `vrsky-pg-r`
(any). Use `-rw` if you skip the pooler.

**Pooling mode caveat:** the pooler uses **session** mode because the Go workers
use server-side prepared statements (lib/pq / pgx), which transaction pooling
breaks unless every client switches to the simple query protocol. Session
pooling still bounds backend connections. Switch `poolMode` to `transaction` in
`cnpg-pooler.yaml` only after confirming all DB clients tolerate it.

### Option B — Managed database (recommended for most)

Run **RDS / Cloud SQL / AlloyDB** and let the provider handle HA, failover, and
backups. Skip the CNPG manifests entirely and set `connection_string` to the
managed endpoint (`sslmode=require`). Optionally still front it with the pooler.

### Migration note

These manifests provision a **fresh** HA cluster (schema applied via the init
ConfigMap). To move data off the existing single-node StatefulSet, dump/restore
with the `vrsky-cli` backup/restore tooling (#86) into the new cluster before
cutting `connection_string` over.

## TLS (#453)

The connections from the platform to this Postgres are encrypted and the
server is checked: clients use `sslmode=verify-full`. Plan, client inventory
and rollout record: [plans/issue-453-postgres-tls.md](../../../plans/issue-453-postgres-tls.md).

| Piece | Where |
| --- | --- |
| Private CA and server certificate (cert-manager `Issuer`s and `Certificate`s in `vrsky-database`) | `tls.yaml` |
| `ssl = on`, certificate and key copied by an init container into memory with mode 0600 for uid 70 | `statefulset.yaml` |
| `pg_hba.conf` that refuses plain connections from other pods (ConfigMap `postgres-hba`, read through `hba_file`, #729) | `statefulset.yaml`, the first document |
| Expiry alerts on the server certificate (`PostgresCertExpiring`, `PostgresCertMetricsAbsent`) and the cert-manager scrape they need | `infrastructure/prometheus-rules.yml`, `monitoring/podmonitors.yaml` ([monitoring](../../../docs/operator/monitoring.md#postgres-server-certificate)) |
| The public CA, as ConfigMap `postgres-ca` in `vrsky-platform` and `vrsky-database`, mounted at `/etc/vrsky/postgres-ca` by every client | `infrastructure/azure/postgres-tls.sh ca-sync`; the mounts are in each client's manifest |
| Connection strings: `?sslmode=verify-full&sslrootcert=/etc/vrsky/postgres-ca/ca.crt` | built by `infrastructure/azure/rotate-secrets.sh`, changed in place by `postgres-tls.sh urls` |

Why these choices:

- **cert-manager, not a managed Postgres.** It is already in the cluster for
  the ingress. The CA key stays in the namespace; only the public `ca.crt`
  leaves it.
- **The CA is copied, not issued per namespace.** A Secret cannot be read
  across namespaces and trust-manager is not installed. `ca-sync` copies the
  public certificate into one ConfigMap per client namespace. It is public,
  so nothing secret moves. The ConfigMap volume is `optional`, so a pod starts
  before it exists.
- **An init container instead of `fsGroup`.** Postgres refuses a key that
  others can read, and a Secret volume is root's. `fsGroup` would fix the key
  but also make the data directory group-writable, which Postgres refuses to
  start on. The init container copies the pair into a memory volume,
  `chmod 0600`, `chown 70:70`; the key is never written to a disk.
- **Renewal is a restart.** The certificate lasts a year and cert-manager
  renews it 90 days before the end. The copy is made when the pod starts, so
  the new certificate is served from the next start. Production's pods all
  restart at every cluster start ([cost-parking](../../../docs/operator/cost-parking.md)),
  so that happens within a working day or two. A reload sidecar would pick
  it up without a restart, but it is another container to get wrong for a
  restart that happens anyway. If the cluster ever runs 90 days without a
  start, `postgres-tls.sh check` says so ("Postgres has an OLDER
  certificate") and a `kubectl rollout restart statefulset/postgresql -n
  vrsky-database` fixes it (a minute or more without a database: the disk
  detaches and re-attaches).
- **Plain connections were accepted during the rollout**, which made it safe
  in any order. Once every client was on TLS they were refused by hand
  (2026-11-03), and since #729 by the manifest: see
  [Plain connections are refused](#plain-connections-are-refused-729).

### Rolling out (production)

The deploy job never applies this directory; a human runs
`infrastructure/azure/postgres-tls.sh`, one step at a time. Every subcommand
except `check` prints the kube context and refuses to run without `CONFIRM=1`
(`DRY_RUN=1` shows what it would do without it). The order, with the check
after each step, is in the plan. In short: server TLS on, copy the
CA, merge (the deploy job rolls the connectors and the backup job with the CA
mounted), patch the three core Deployments, prove `verify-full` with the
canary pod, switch the strings, restart, check that no network connection is
without TLS.

```bash
infrastructure/azure/postgres-tls.sh check      # read-only; safe any time
```

`check` ends with the connection counts, which are also this query:

```sql
SELECT s.ssl, count(*) FROM pg_stat_ssl s JOIN pg_stat_activity a USING (pid)
WHERE a.backend_type = 'client backend' AND a.client_addr IS NOT NULL
GROUP BY s.ssl;
```

### Connecting by hand

`kubectl port-forward` and `kubectl exec` reach the server as `localhost`,
which the certificate lists, so `verify-full` works with the CA from the
Secret (public data):

```bash
kubectl get secret postgresql-tls -n vrsky-database -o 'jsonpath={.data.ca\.crt}' | base64 -d > /tmp/vrsky-pg-ca.crt
kubectl port-forward -n vrsky-database svc/postgresql 5432:5432 &
psql "host=localhost port=5432 user=vrsky dbname=vrsky sslmode=verify-full sslrootcert=/tmp/vrsky-pg-ca.crt"
```

### Rollback

A client that cannot verify the server (a CA or host name problem): switch
the strings to TLS without the check and restart. `urls disable` refuses
while the server turns plain connections away, because every client would
restart into a refusal; allow plain connections first (below) if you really
need it.

```bash
CONFIRM=1 infrastructure/azure/postgres-tls.sh urls require
CONFIRM=1 infrastructure/azure/postgres-tls.sh restart
```

If Postgres itself does not come back after `postgres-tls.sh server`:
`kubectl -n vrsky-database rollout undo statefulset/postgresql`, then
`kubectl -n vrsky-database delete pod postgresql-0` (a rolling update does
not replace a pod that never became Ready). The disk detaches and attaches
again, which can take longer than a minute.

Turning TLS off on the server as well is never needed for a client rollback.
If you want it anyway, revert the StatefulSet change in git and run
`postgres-tls.sh server` from that commit: it applies whatever the file says
(with the production storage class) and restarts Postgres.

### Plain connections are refused (#729)

Postgres reads its `pg_hba.conf` from the ConfigMap `postgres-hba` (the first
document in `statefulset.yaml`, started with `-c hba_file=/etc/postgresql/hba/pg_hba.conf`),
not from the data volume. The file is the image's own with the last rule made
`hostssl`:

- `local` and loopback (`127.0.0.1`, `::1`) stay `trust`: the probes
  (`pg_isready` over the socket), the image's first-start server, `kubectl exec
  psql` and `kubectl port-forward` use them.
- Everything else is `hostssl all all all scram-sha-256`: TLS and the password.
  A plain connection gets `no pg_hba.conf entry for host …, no encryption`.

Before #729 the same rule was a hand edit of `$PGDATA/pg_hba.conf` (2026-11-03),
which a snapshot restore or a new volume would not have had. The file on the
volume is still there and unused; it is what a rollback falls back to.

`TestPostgresTLS_RefusesPlainConnections` (`src/pkg/managementapi`) fails if a
rule lets another pod in without TLS or without a password, if the socket line
goes, or if the file stops coming from the ConfigMap.

#### Applying it (production, once)

The deploy job never applies this directory. Production already refuses plain
connections through the hand edit, so nothing changes for the clients; the
StatefulSet change restarts Postgres (a minute or more without a database while
the disk moves).

```bash
# 1. Every client on TLS: no "ssl=false" line under the connection counts.
infrastructure/azure/postgres-tls.sh check

# 2. What would change: the new ConfigMap postgres-hba, and in the StatefulSet
#    the hba_file argument and the hba volume. Nothing else.
DRY_RUN=1 infrastructure/azure/postgres-tls.sh server

# 3. Apply and wait for postgresql-0. It ends with ssl, hba_file and the number
#    of rules that let another pod in without TLS (want: on,
#    /etc/postgresql/hba/pg_hba.conf, 0).
CONFIRM=1 infrastructure/azure/postgres-tls.sh server

# 4. Clients back, all on TLS; the same three lines under "server".
infrastructure/azure/postgres-tls.sh check

# 5. A plain connection from another pod is refused. No password needed: the
#    refusal comes before authentication. Expect "no pg_hba.conf entry … no
#    encryption".
kubectl run pg-plain-test -n vrsky-platform --rm -i --restart=Never --image=postgres:18-alpine -- \
  psql "host=postgresql.vrsky-database.svc.cluster.local user=vrsky dbname=vrsky sslmode=disable connect_timeout=5" -c 'SELECT 1'
```

#### Undoing it

- **A client has to connect without TLS again (emergency).** No restart: edit
  the last line of the ConfigMap from `hostssl` to `host`, wait until the pod
  sees it (a minute or two; the volume is not a `subPath`), reload.

  ```bash
  kubectl -n vrsky-database edit configmap postgres-hba
  kubectl exec -n vrsky-database postgresql-0 -c postgresql -- tail -1 /etc/postgresql/hba/pg_hba.conf
  kubectl exec -n vrsky-database postgresql-0 -c postgresql -- psql -U vrsky -d vrsky -tAc 'SELECT pg_reload_conf()'
  ```

  The next `postgres-tls.sh server` puts the repo's file back. A file Postgres
  cannot parse is ignored on a reload (the log says `pg_hba.conf was not
  reloaded`), but stops Postgres at its next start, so check
  `SELECT * FROM pg_hba_file_rules WHERE error IS NOT NULL` after an edit.
- **Postgres does not come back after step 3.**
  `kubectl -n vrsky-database rollout undo statefulset/postgresql`, then
  `kubectl -n vrsky-database delete pod postgresql-0` (a rolling update does
  not replace a pod that never became Ready). Without `hba_file` Postgres reads
  `$PGDATA/pg_hba.conf` again, which in production says `hostssl` since the
  hand edit, so clients see no difference. The ConfigMap can stay.

## Components

### Files

1. **namespace.yaml** - Creates `vrsky-database` namespace
2. **secret.yaml** - PostgreSQL credentials (⚠️ CHANGE IN PRODUCTION!)
3. **configmap.yaml** - PostgreSQL environment configuration
4. **init-schema.sql** - Complete VRSky database schema
5. **init-configmap.yaml** - Schema initialization script
6. **statefulset.yaml** - the ConfigMap `postgres-hba` (`pg_hba.conf`, #729) and the PostgreSQL StatefulSet
7. **service.yaml** - ClusterIP service
8. **tls.yaml** - cert-manager `Issuer`s and `Certificate`s for the server's TLS (#453)

### Database Schema

The schema includes these tables:

| Table            | Purpose                                                  |
| ---------------- | -------------------------------------------------------- |
| `tenants`        | Multi-tenant account information                         |
| `nats_instances` | Tenant NATS instance tracking                            |
| `connectors`     | Available connector types (HTTP, PostgreSQL, File, etc.) |
| `integrations`   | Integration flow definitions                             |
| `message_log`    | Minimal message tracking (24hr TTL)                      |
| `api_keys`       | Tenant API authentication                                |
| `audit_log`      | Audit trail                                              |

**Seed Data Included**:

- 7 default connectors (HTTP, PostgreSQL, File consumers/producers + converters/filters)
- 1 demo tenant (`slug: demo`)

## Deployment

### Quick Deploy

```bash
kubectl apply -f namespace.yaml
kubectl apply -f secret.yaml
kubectl apply -f configmap.yaml
kubectl apply -f init-configmap.yaml
kubectl apply -f service.yaml
kubectl apply -f statefulset.yaml

# Wait for pod to be ready
kubectl wait --for=condition=ready pod -l app=postgresql -n vrsky-database --timeout=300s

# Verify schema initialization
kubectl logs -n vrsky-database postgresql-0 | grep "VRSky schema"
```

### Manual Deploy (Step-by-Step)

```bash
# 1. Create namespace
kubectl apply -f namespace.yaml

# 2. Create secrets (⚠️ CHANGE PASSWORDS FIRST!)
kubectl apply -f secret.yaml

# 3. Create ConfigMaps
kubectl apply -f configmap.yaml

# 4. Create init script ConfigMap with embedded schema
kubectl create configmap postgres-init-script \
  --from-file=init-schema.sql=init-schema.sql \
  -n vrsky-database

# 5. Create service
kubectl apply -f service.yaml

# 6. Deploy StatefulSet
kubectl apply -f statefulset.yaml

# 7. Watch rollout
kubectl rollout status statefulset/postgresql -n vrsky-database

# 8. Verify pod is running
kubectl get pods -n vrsky-database

# Expected output:
# NAME           READY   STATUS    RESTARTS   AGE
# postgresql-0   1/1     Running   0          2m
```

## Security: Change Passwords!

⚠️ **IMPORTANT**: The `secret.yaml` file contains default passwords. **You MUST change these before deploying to production!**

### Generate Secure Passwords

```bash
# Generate random passwords
POSTGRES_PASSWORD=$(openssl rand -base64 32)
VRSKY_PASSWORD=$(openssl rand -base64 32)

# Create secret with secure passwords
kubectl create secret generic postgres-credentials \
  --from-literal=postgres-password=$POSTGRES_PASSWORD \
  --from-literal=vrsky-username=vrsky \
  --from-literal=vrsky-password=$VRSKY_PASSWORD \
  --from-literal=vrsky-database=vrsky \
  -n vrsky-database \
  --dry-run=client -o yaml | kubectl apply -f -

# Save passwords securely (e.g., in a password manager)
echo "PostgreSQL root password: $POSTGRES_PASSWORD"
echo "VRSky user password: $VRSKY_PASSWORD"
```

## Verification

### Check Database Connectivity

```bash
# Port-forward to local machine
kubectl port-forward -n vrsky-database postgresql-0 5432:5432

# Connect from local machine (requires psql client)
PGPASSWORD=<vrsky-password> psql -h localhost -U vrsky -d vrsky

# Inside psql:
vrsky=# \dt  -- List all tables
vrsky=# SELECT * FROM tenants;  -- Check seed data
vrsky=# SELECT name, type FROM connectors;  -- List connectors
```

### Run Test Queries

```bash
# Execute SQL from kubectl
kubectl exec -n vrsky-database postgresql-0 -- \
  psql -U vrsky -d vrsky -c "SELECT COUNT(*) FROM connectors;"

# Expected output: 7 (default connectors)

kubectl exec -n vrsky-database postgresql-0 -- \
  psql -U vrsky -d vrsky -c "SELECT slug, name FROM tenants;"

# Expected output: demo | Demo Tenant
```

### Check Logs

```bash
# View PostgreSQL logs
kubectl logs -n vrsky-database postgresql-0 -f

# Check for schema initialization
kubectl logs -n vrsky-database postgresql-0 | grep "CREATE TABLE"
```

## Storage

### Persistent Volume

PostgreSQL data is stored in one 50Gi volume: `local-path` on k3d, an Azure
managed disk (`managed-csi`) in production.

```bash
# List PVCs
kubectl get pvc -n vrsky-database

# Check volume usage
kubectl exec -n vrsky-database postgresql-0 -- df -h /var/lib/postgresql/data
```

## Backup & Recovery

### Manual Backup

```bash
# Create backup
kubectl exec -n vrsky-database postgresql-0 -- \
  pg_dump -U vrsky -d vrsky -F c -f /tmp/vrsky-backup.dump

# Copy to local
kubectl cp vrsky-database/postgresql-0:/tmp/vrsky-backup.dump ./vrsky-backup.dump

# Backup with timestamp
BACKUP_FILE="vrsky-backup-$(date +%Y%m%d-%H%M%S).dump"
kubectl cp vrsky-database/postgresql-0:/tmp/vrsky-backup.dump ./$BACKUP_FILE
```

### Restore from Backup

```bash
# Copy backup to pod
kubectl cp ./vrsky-backup.dump vrsky-database/postgresql-0:/tmp/

# Restore
kubectl exec -n vrsky-database postgresql-0 -- \
  pg_restore -U vrsky -d vrsky -c /tmp/vrsky-backup.dump
```

### Automated Backups (CronJob)

Create a CronJob to backup daily:

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: postgres-backup
  namespace: vrsky-database
spec:
  schedule: "0 2 * * *" # 2 AM daily
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: backup
              image: postgres:18-alpine
              command:
                - /bin/sh
                - -c
                - |
                  BACKUP_FILE="/backups/vrsky-$(date +%Y%m%d-%H%M%S).dump"
                  pg_dump -h postgresql -U vrsky -d vrsky -F c -f $BACKUP_FILE
                  echo "Backup created: $BACKUP_FILE"
              env:
                - name: PGPASSWORD
                  valueFrom:
                    secretKeyRef:
                      name: postgres-credentials
                      key: vrsky-password
              volumeMounts:
                - name: backup-storage
                  mountPath: /backups
          volumes:
            - name: backup-storage
              persistentVolumeClaim:
                claimName: postgres-backups
          restartPolicy: OnFailure
```

## Monitoring

### Connection Stats

```bash
# Check active connections
kubectl exec -n vrsky-database postgresql-0 -- \
  psql -U vrsky -d vrsky -c "SELECT count(*) FROM pg_stat_activity;"

# Check database size
kubectl exec -n vrsky-database postgresql-0 -- \
  psql -U vrsky -d vrsky -c "SELECT pg_size_pretty(pg_database_size('vrsky'));"
```

### Performance Metrics

```bash
# Table sizes
kubectl exec -n vrsky-database postgresql-0 -- \
  psql -U vrsky -d vrsky -c "
    SELECT schemaname,tablename,pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) AS size
    FROM pg_tables
    WHERE schemaname = 'public'
    ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;"

# Index usage
kubectl exec -n vrsky-database postgresql-0 -- \
  psql -U vrsky -d vrsky -c "
    SELECT schemaname,tablename,indexname,idx_scan
    FROM pg_stat_user_indexes
    ORDER BY idx_scan DESC;"
```

## Troubleshooting

### Pod Not Starting

```bash
# Check pod events
kubectl describe pod postgresql-0 -n vrsky-database

# Check logs
kubectl logs postgresql-0 -n vrsky-database

# Common issues:
# - PVC Pending (not bound): `kubectl describe pvc postgres-data-postgresql-0 -n vrsky-database`
#   shows why. Check that the StorageClass exists (`local-path` on k3d,
#   `managed-csi` on AKS) and that the CSI driver pods are running
#   (`kubectl get pods -n kube-system | grep csi`)
# - Secret not found: Apply secret.yaml first
# - Init script failed: Check init-schema.sql syntax
```

### Schema Not Initialized

```bash
# Check if init script ran
kubectl logs postgresql-0 -n vrsky-database | grep "init-schema"

# Manually run schema
kubectl cp init-schema.sql vrsky-database/postgresql-0:/tmp/
kubectl exec -n vrsky-database postgresql-0 -- \
  psql -U vrsky -d vrsky -f /tmp/init-schema.sql
```

### Connection Refused

```bash
# Verify service exists
kubectl get svc -n vrsky-database

# Test connection from another pod
kubectl run -it --rm pg-test --image=postgres:18-alpine --restart=Never -n vrsky-database -- \
  psql -h postgresql.vrsky-database.svc.cluster.local -U vrsky -d vrsky
```

### High Memory Usage

```bash
# Check memory usage
kubectl top pod postgresql-0 -n vrsky-database

# Reduce shared_buffers if needed (edit StatefulSet)
# Default: Uses ~25% of available RAM
```

## Scaling

### Increase Storage

Production's volume is an Azure managed disk from the `managed-csi`
StorageClass. The claim is `postgres-data-postgresql-0`, 50Gi today.

```bash
# The StorageClass must allow expansion (prints "true" when it does)
kubectl get storageclass managed-csi -o jsonpath='{.allowVolumeExpansion}'

# Raise the claim's size. A claim cannot shrink.
kubectl patch pvc postgres-data-postgresql-0 -n vrsky-database \
  -p '{"spec":{"resources":{"requests":{"storage":"100Gi"}}}}'

# Watch the resize
kubectl get pvc postgres-data-postgresql-0 -n vrsky-database
kubectl describe pvc postgres-data-postgresql-0 -n vrsky-database
```

- Do not delete the PVC to resize it. The disk is dynamically provisioned, and
  its reclaim policy deletes the Azure disk when the claim goes
  ([docs/DR.md](../../../docs/DR.md) has the details).
- `volumeClaimTemplates` in `statefulset.yaml` sets the size of new claims
  only. Editing it does not resize the existing claim.
- If `describe` shows `FileSystemResizePending`, the disk is bigger but the
  filesystem has not grown yet. Check the Azure disk docs for whether your
  target size needs the disk detached. That means stopping Postgres, which is
  downtime with one replica.

### Increase Resources

Edit `statefulset.yaml`:

```yaml
resources:
  requests:
    cpu: 4
    memory: 8Gi
  limits:
    cpu: 8
    memory: 16Gi
```

Apply and restart:

```bash
kubectl apply -f statefulset.yaml
kubectl rollout restart statefulset/postgresql -n vrsky-database
```

### High Availability (Post-POC)

For production, consider:

- PostgreSQL replication (primary + replica)
- Patroni for automatic failover
- pgBouncer for connection pooling
- Crunchy Data PostgreSQL Operator

## Maintenance

### Vacuum Database

```bash
# Manual vacuum
kubectl exec -n vrsky-database postgresql-0 -- \
  psql -U vrsky -d vrsky -c "VACUUM ANALYZE;"

# Enable autovacuum (default: enabled)
```

### Update Statistics

```bash
kubectl exec -n vrsky-database postgresql-0 -- \
  psql -U vrsky -d vrsky -c "ANALYZE;"
```

### Clean Old Message Logs

```bash
# Delete completed messages older than 24 hours
kubectl exec -n vrsky-database postgresql-0 -- \
  psql -U vrsky -d vrsky -c "
    DELETE FROM message_log
    WHERE status = 'completed'
      AND completed_at < NOW() - INTERVAL '24 hours';"
```

## References

- [PostgreSQL Documentation](https://www.postgresql.org/docs/15/)
- [Kubernetes: expanding PersistentVolumeClaims](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#expanding-persistent-volumes-claims)
- [Azure Disks CSI driver on AKS](https://learn.microsoft.com/azure/aks/azure-disk-csi)
- [VRSky Database Schema](init-schema.sql)
