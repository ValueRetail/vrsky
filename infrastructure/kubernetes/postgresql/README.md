# PostgreSQL Deployment

This directory contains Kubernetes manifests for deploying PostgreSQL 18 as the VRSky platform database.

## Architecture

- **Type**: StatefulSet (1 replica for POC)
- **Version**: PostgreSQL 18 Alpine
- **Resources**: 4 CPU, 8GB RAM (requests: 2 CPU, 4GB RAM)
- **Storage**: 50GB via Longhorn distributed storage
- **Persistence**: Full durability via Longhorn replication

## High Availability (production) — #137

The single-replica `statefulset.yaml` has **no HA**: a pod/node failure is a
control-plane outage and risks data loss. It is now **dev/single-node only**.
For production, choose one of:

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

## Components

### Files

1. **namespace.yaml** - Creates `vrsky-database` namespace
2. **secret.yaml** - PostgreSQL credentials (⚠️ CHANGE IN PRODUCTION!)
3. **configmap.yaml** - PostgreSQL environment configuration
4. **init-schema.sql** - Complete VRSky database schema
5. **init-configmap.yaml** - Schema initialization script
6. **statefulset.yaml** - PostgreSQL StatefulSet
7. **service.yaml** - ClusterIP service

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

PostgreSQL data is stored in a 50GB Longhorn volume.

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
# - PVC not bound: Verify Longhorn is running
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

```bash
# Edit PVC (requires Longhorn volume expansion support)
kubectl edit pvc postgres-data-postgresql-0 -n vrsky-database

# Change storage: 50Gi to 100Gi
# Longhorn will automatically expand the volume
```

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
- [Longhorn Storage](https://longhorn.io/docs/)
- [VRSky Database Schema](init-schema.sql)
