# MinIO Object Storage Deployment

This directory contains Kubernetes manifests for deploying MinIO as the S3-compatible object storage for VRSky large payloads (>256KB).

## Architecture

- **Type**: Deployment (1 replica for POC)
- **Storage Backend**: S3-compatible object storage
- **Resources**: 2 CPU, 4GB RAM (requests: 1 CPU, 2GB RAM)
- **Storage**: 100GB via Longhorn distributed storage
- **Access**: Internal cluster access only (ClusterIP)

## Components

### Files

1. **namespace.yaml** - Creates `vrsky-storage` namespace
2. **secret.yaml** - MinIO credentials (⚠️ CHANGE IN PRODUCTION!)
3. **configmap.yaml** - MinIO configuration
4. **deployment.yaml** - MinIO Deployment + PVC
5. **service.yaml** - ClusterIP service (API: 9000, Console: 9001)
6. **setup-job.yaml** - Job to create buckets and configure lifecycle policies

### Bucket Structure

```
vrsky-objects/                    # Main bucket
├── temp/                         # Temporary objects (15min TTL)
│   └── {tenant-id}/
│       └── messages/
│           └── {message-id}.bin
└── tenants/                      # Tenant-specific storage
    └── {tenant-id}/
        ├── attachments/
        └── exports/
```

**Lifecycle Policy**:

- Objects in `temp/` prefix: Auto-deleted after 1 day
- Default TTL for large message payloads: 15 minutes (application-managed)

## Deployment

### Quick Deploy

```bash
kubectl apply -f namespace.yaml
kubectl apply -f secret.yaml
kubectl apply -f configmap.yaml
kubectl apply -f deployment.yaml
kubectl apply -f service.yaml

# Wait for pod to be ready
kubectl wait --for=condition=ready pod -l app=minio -n vrsky-storage --timeout=300s

# Run setup job to create buckets
kubectl apply -f setup-job.yaml

# Check job logs
kubectl logs -n vrsky-storage job/minio-setup -f
```

### Manual Deploy (Step-by-Step)

```bash
# 1. Create namespace
kubectl apply -f namespace.yaml

# 2. Create secrets (⚠️ CHANGE CREDENTIALS FIRST!)
kubectl apply -f secret.yaml

# 3. Create ConfigMap
kubectl apply -f configmap.yaml

# 4. Create PVC and Deployment
kubectl apply -f deployment.yaml

# 5. Create Service
kubectl apply -f service.yaml

# 6. Wait for pod
kubectl rollout status deployment/minio -n vrsky-storage

# 7. Verify pod is running
kubectl get pods -n vrsky-storage

# Expected output:
# NAME                     READY   STATUS    RESTARTS   AGE
# minio-xxxxxxxxxx-xxxxx   1/1     Running   0          2m

# 8. Run setup job
kubectl apply -f setup-job.yaml

# 9. Verify bucket creation
kubectl logs -n vrsky-storage job/minio-setup
```

## Security: Change Credentials!

⚠️ **IMPORTANT**: The `secret.yaml` file contains default credentials. **You MUST change these before deploying to production!**

### Generate Secure Credentials

```bash
# Generate random access key and secret key
MINIO_ACCESS_KEY=$(openssl rand -hex 16)  # 32 characters
MINIO_SECRET_KEY=$(openssl rand -base64 32)  # Min 32 characters

# Create secret
kubectl create secret generic minio-credentials \
  --from-literal=accesskey=$MINIO_ACCESS_KEY \
  --from-literal=secretkey=$MINIO_SECRET_KEY \
  -n vrsky-storage \
  --dry-run=client -o yaml | kubectl apply -f -

# Save credentials securely
echo "MinIO Access Key: $MINIO_ACCESS_KEY"
echo "MinIO Secret Key: $MINIO_SECRET_KEY"
```

## Verification

### Access MinIO Console

```bash
# Port-forward to access web console
kubectl port-forward -n vrsky-storage svc/minio 9001:9001

# Open browser: http://localhost:9001
# Login with credentials from secret.yaml
```

### Test S3 API

```bash
# Port-forward API endpoint
kubectl port-forward -n vrsky-storage svc/minio 9000:9000

# Install AWS CLI or mc (MinIO client)
brew install minio/stable/mc  # macOS
# or
apt install minio-client      # Ubuntu

# Configure mc client
mc alias set vrsky-local http://localhost:9000 vrsky-minio-access changeme-minio-secret-key-min-32-chars

# List buckets
mc ls vrsky-local

# Expected output:
# [2026-01-27 16:00:00 UTC]     0B vrsky-objects/

# Upload test file
echo "test data" > test.txt
mc cp test.txt vrsky-local/vrsky-objects/temp/test.txt

# List objects
mc ls vrsky-local/vrsky-objects/temp/

# Download test file
mc cp vrsky-local/vrsky-objects/temp/test.txt downloaded.txt

# Delete test file
mc rm vrsky-local/vrsky-objects/temp/test.txt
```

### Test from Pod

```bash
# Run mc client from pod
kubectl run -it --rm mc-test --image=minio/mc --restart=Never -n vrsky-storage -- /bin/sh

# Inside pod:
mc alias set vrsky http://minio.vrsky-storage.svc.cluster.local:9000 $MINIO_ROOT_USER $MINIO_ROOT_PASSWORD
mc ls vrsky
mc stat vrsky/vrsky-objects
```

### Verify Lifecycle Policies

```bash
# Check lifecycle rules
kubectl run -it --rm mc-test --image=minio/mc --restart=Never -n vrsky-storage -- \
  sh -c 'mc alias set vrsky http://minio.vrsky-storage.svc.cluster.local:9000 vrsky-minio-access changeme-minio-secret-key-min-32-chars && mc ilm ls vrsky/vrsky-objects'
```

## Storage

### Persistent Volume

MinIO data is stored in a 100GB Longhorn volume.

```bash
# List PVCs
kubectl get pvc -n vrsky-storage

# Check volume usage
kubectl exec -n vrsky-storage deployment/minio -- df -h /data
```

### Bucket Statistics

```bash
# Get bucket size
kubectl exec -n vrsky-storage deployment/minio -- \
  mc du vrsky-minio/vrsky-objects --alias vrsky-minio=http://localhost:9000
```

## Monitoring

### Check MinIO Health

```bash
# Health endpoint
kubectl exec -n vrsky-storage deployment/minio -- \
  curl -s http://localhost:9000/minio/health/live

# Expected: HTTP 200 OK
```

### Metrics

MinIO exposes Prometheus metrics on `/minio/v2/metrics/cluster`.

```bash
# Port-forward and scrape metrics
kubectl port-forward -n vrsky-storage svc/minio 9000:9000
curl http://localhost:9000/minio/v2/metrics/cluster
```

### Server Info

```bash
# Get server info via mc
kubectl run -it --rm mc-test --image=minio/mc --restart=Never -n vrsky-storage -- \
  sh -c 'mc alias set vrsky http://minio.vrsky-storage.svc.cluster.local:9000 vrsky-minio-access changeme-minio-secret-key-min-32-chars && mc admin info vrsky'
```

## Backup & Recovery

### Backup Bucket Data

```bash
# Mirror bucket to local directory
mc mirror vrsky-minio/vrsky-objects /path/to/backup/

# Backup with timestamp
BACKUP_DIR="minio-backup-$(date +%Y%m%d-%H%M%S)"
mc mirror vrsky-minio/vrsky-objects ./$BACKUP_DIR
```

### Restore Bucket Data

```bash
# Mirror local directory to bucket
mc mirror /path/to/backup/ vrsky-minio/vrsky-objects
```

### Backup Entire PVC

```bash
# Create snapshot via Longhorn
kubectl annotate pvc minio-data -n vrsky-storage \
  snapshot.storage.kubernetes.io/snapshot-name=minio-backup-$(date +%Y%m%d-%H%M%S)
```

## Troubleshooting

### Pod Not Starting

```bash
# Check pod events
kubectl describe pod -l app=minio -n vrsky-storage

# Check logs
kubectl logs -l app=minio -n vrsky-storage

# Common issues:
# - PVC not bound: Verify Longhorn is running
# - Secret not found: Apply secret.yaml first
# - Port conflicts: Ensure ports 9000/9001 are available
```

### Cannot Access Console

```bash
# Verify service
kubectl get svc minio -n vrsky-storage

# Port-forward
kubectl port-forward -n vrsky-storage svc/minio 9001:9001

# Check browser console logs
# Login credentials from secret.yaml
```

### Bucket Creation Failed

```bash
# Check setup job logs
kubectl logs -n vrsky-storage job/minio-setup

# Manually create bucket
kubectl run -it --rm mc-test --image=minio/mc --restart=Never -n vrsky-storage -- \
  sh -c 'mc alias set vrsky http://minio.vrsky-storage.svc.cluster.local:9000 vrsky-minio-access changeme-minio-secret-key-min-32-chars && mc mb vrsky/vrsky-objects'
```

### S3 Connection Errors from App

```bash
# Test connectivity from app namespace
kubectl run -it --rm s3-test --image=amazon/aws-cli --restart=Never -n vrsky-platform -- \
  s3 --endpoint-url=http://minio.vrsky-storage.svc.cluster.local:9000 \
     --no-sign-request ls

# Verify DNS resolution
kubectl run -it --rm dns-test --image=busybox --restart=Never -n vrsky-platform -- \
  nslookup minio.vrsky-storage.svc.cluster.local
```

## Scaling

### Increase Storage

```bash
# Edit PVC
kubectl edit pvc minio-data -n vrsky-storage

# Change storage: 100Gi to 200Gi
# Longhorn will automatically expand the volume
```

### Increase Resources

Edit `deployment.yaml`:

```yaml
resources:
  requests:
    cpu: 2
    memory: 4Gi
  limits:
    cpu: 4
    memory: 8Gi
```

Apply and restart:

```bash
kubectl apply -f deployment.yaml
kubectl rollout restart deployment/minio -n vrsky-storage
```

### Distributed Mode (Post-POC)

For production HA, deploy MinIO in distributed mode (4+ nodes):

```bash
# Use MinIO Operator
kubectl apply -k github.com/minio/operator

# Create MinIO Tenant (distributed)
kubectl minio tenant create vrsky-minio \
  --servers 4 \
  --volumes 16 \
  --capacity 400Gi \
  --storage-class longhorn
```

## Integration with VRSky

### Go Client Example

```go
package main

import (
    "github.com/minio/minio-go/v7"
    "github.com/minio/minio-go/v7/pkg/credentials"
)

func main() {
    endpoint := "minio.vrsky-storage.svc.cluster.local:9000"
    accessKeyID := "vrsky-minio-access"
    secretAccessKey := "changeme-minio-secret-key-min-32-chars"
    useSSL := false

    // Initialize MinIO client
    minioClient, err := minio.New(endpoint, &minio.Options{
        Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
        Secure: useSSL,
    })
    if err != nil {
        log.Fatal(err)
    }

    // Upload object
    info, err := minioClient.FPutObject(context.Background(),
        "vrsky-objects",
        "temp/tenant-a/messages/msg-123.bin",
        "/path/to/file",
        minio.PutObjectOptions{})
}
```

### Environment Variables for VRSky Services

```yaml
env:
  - name: S3_ENDPOINT
    value: "http://minio.vrsky-storage.svc.cluster.local:9000"
  - name: S3_ACCESS_KEY
    valueFrom:
      secretKeyRef:
        name: minio-credentials
        key: accesskey
  - name: S3_SECRET_KEY
    valueFrom:
      secretKeyRef:
        name: minio-credentials
        key: secretkey
  - name: S3_BUCKET
    value: "vrsky-objects"
  - name: S3_USE_SSL
    value: "false"
```

## Lifecycle Management

### Temporary Object Cleanup

Objects in `temp/` are auto-deleted after 1 day by lifecycle policy.

Application should also delete objects manually after successful delivery:

```go
// Delete after successful delivery
err := minioClient.RemoveObject(ctx, "vrsky-objects", objectKey, minio.RemoveObjectOptions{})
```

### Manual Cleanup

```bash
# Remove all temp objects older than 1 hour
mc rm --recursive --force --older-than 1h vrsky-minio/vrsky-objects/temp/
```

## References

- [MinIO Documentation](https://min.io/docs/)
- [MinIO Go Client](https://github.com/minio/minio-go)
- [S3 API Reference](https://docs.aws.amazon.com/AmazonS3/latest/API/)
- [VRSky NATS Architecture](../../../docs/NATS_ARCHITECTURE.md) (Reference-based messaging)
