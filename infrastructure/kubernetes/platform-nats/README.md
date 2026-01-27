# Platform NATS Deployment

This directory contains Kubernetes manifests for deploying the **Platform NATS Cluster** - the high-availability, JetStream-enabled NATS cluster used for VRSky state tracking and platform-wide operations.

## Architecture

- **Type**: StatefulSet (3 replicas)
- **NATS Mode**: JetStream + NATS KV enabled
- **Resources**: 4 CPU, 8GB RAM per pod (requests: 2 CPU, 4GB RAM)
- **Storage**: 100GB per pod via Longhorn distributed storage
- **Replication**: R3 (3-way replication)

## Components

### Files

1. **namespace.yaml** - Creates `vrsky-platform` namespace
2. **configmap.yaml** - NATS server configuration
3. **statefulset.yaml** - 3-node NATS cluster with JetStream
4. **service.yaml** - ClusterIP service + Headless service for StatefulSet
5. **kv-setup-job.yaml** - Job to create NATS KV buckets after deployment

### KV Buckets Created

| Bucket Name         | TTL        | Max Size | Purpose                               |
| ------------------- | ---------- | -------- | ------------------------------------- |
| `message_state`     | 15 minutes | 10 GB    | Message processing status tracking    |
| `integration_locks` | 5 minutes  | 1 GB     | Prevent duplicate message processing  |
| `retry_queue`       | 1 hour     | 50 GB    | Messages awaiting retry after failure |
| `dead_letter_queue` | 7 days     | 100 GB   | Failed messages after max retries     |

## Deployment

### Quick Deploy

```bash
kubectl apply -f namespace.yaml
kubectl apply -f configmap.yaml
kubectl apply -f service.yaml
kubectl apply -f statefulset.yaml

# Wait for all pods to be ready
kubectl wait --for=condition=ready pod -l app=nats-platform -n vrsky-platform --timeout=300s

# Create KV buckets
kubectl apply -f kv-setup-job.yaml

# Check job logs
kubectl logs -n vrsky-platform job/nats-kv-setup -f
```

### Manual Deploy (Step-by-Step)

```bash
# 1. Create namespace
kubectl apply -f namespace.yaml

# 2. Create NATS configuration
kubectl apply -f configmap.yaml

# 3. Create services
kubectl apply -f service.yaml

# 4. Deploy StatefulSet
kubectl apply -f statefulset.yaml

# 5. Watch rollout
kubectl rollout status statefulset/nats-platform -n vrsky-platform

# 6. Verify all pods are running
kubectl get pods -n vrsky-platform -l app=nats-platform

# Expected output:
# NAME              READY   STATUS    RESTARTS   AGE
# nats-platform-0   1/1     Running   0          2m
# nats-platform-1   1/1     Running   0          1m
# nats-platform-2   1/1     Running   0          30s

# 7. Create KV buckets
kubectl apply -f kv-setup-job.yaml

# 8. Verify KV buckets
kubectl logs -n vrsky-platform job/nats-kv-setup
```

## Verification

### Check Cluster Health

```bash
# Get pods
kubectl get pods -n vrsky-platform

# Check logs
kubectl logs -n vrsky-platform nats-platform-0

# Port-forward to monitoring interface
kubectl port-forward -n vrsky-platform nats-platform-0 8222:8222

# Access monitoring at http://localhost:8222/varz
curl http://localhost:8222/varz | jq .
```

### Test NATS Connection

```bash
# Run nats-box for testing
kubectl run -it --rm nats-box --image=natsio/nats-box --restart=Never -n vrsky-platform

# Inside the pod:
nats-box:~$ nats server info nats://nats-platform.vrsky-platform.svc.cluster.local:4222

# Test JetStream
nats-box:~$ nats stream ls --server=nats://nats-platform.vrsky-platform.svc.cluster.local:4222

# Test KV
nats-box:~$ nats kv ls --server=nats://nats-platform.vrsky-platform.svc.cluster.local:4222
```

### Verify KV Buckets

```bash
# List all KV buckets
kubectl run -it --rm nats-box --image=natsio/nats-box --restart=Never -n vrsky-platform -- \
  nats kv ls --server=nats://nats-platform.vrsky-platform.svc.cluster.local:4222

# Get bucket details
kubectl run -it --rm nats-box --image=natsio/nats-box --restart=Never -n vrsky-platform -- \
  nats kv status message_state --server=nats://nats-platform.vrsky-platform.svc.cluster.local:4222
```

## Monitoring

### Metrics Endpoints

- **HTTP Monitoring**: http://nats-platform.vrsky-platform.svc.cluster.local:8222
- **Endpoints**:
  - `/varz` - General server information
  - `/connz` - Connection information
  - `/routez` - Cluster routing information
  - `/subsz` - Subscription information
  - `/jsz` - JetStream information

### Prometheus Metrics

```bash
# Scrape metrics
curl http://nats-platform-0.nats-platform.vrsky-platform.svc.cluster.local:8222/metrics
```

## Storage

### Persistent Volumes

Each NATS pod has a 100GB Longhorn volume mounted at `/data/jetstream`.

```bash
# List PVCs
kubectl get pvc -n vrsky-platform

# Check volume usage
kubectl exec -n vrsky-platform nats-platform-0 -- df -h /data/jetstream
```

## Troubleshooting

### Pod Not Starting

```bash
# Check pod events
kubectl describe pod nats-platform-0 -n vrsky-platform

# Check logs
kubectl logs nats-platform-0 -n vrsky-platform

# Common issues:
# - PVC not bound: Check Longhorn storage class exists
# - Config errors: Validate configmap.yaml syntax
```

### Cluster Not Forming

```bash
# Check cluster routes
kubectl exec -n vrsky-platform nats-platform-0 -- nats-server --signal routes

# Verify cluster connectivity
kubectl exec -n vrsky-platform nats-platform-0 -- ncat -zv nats-platform-1.nats-platform.vrsky-platform.svc.cluster.local 6222
```

### KV Bucket Creation Failed

```bash
# Check job logs
kubectl logs -n vrsky-platform job/nats-kv-setup

# Manually create bucket
kubectl run -it --rm nats-box --image=natsio/nats-box --restart=Never -n vrsky-platform -- \
  nats kv add message_state --server=nats://nats-platform.vrsky-platform.svc.cluster.local:4222 --replicas=3 --ttl=15m
```

### High Memory Usage

```bash
# Check JetStream memory usage
kubectl exec -n vrsky-platform nats-platform-0 -- nats-server --signal jsz

# Reduce max_mem in configmap if needed
# Default: 4GB per pod
```

## Scaling

### Horizontal Scaling (Add More Nodes)

```bash
# Scale to 5 nodes
kubectl scale statefulset nats-platform -n vrsky-platform --replicas=5

# Update configmap to add new routes
# Edit configmap.yaml and add:
#   nats://nats-platform-3.nats-platform.vrsky-platform.svc.cluster.local:6222
#   nats://nats-platform-4.nats-platform.vrsky-platform.svc.cluster.local:6222

kubectl apply -f configmap.yaml

# Restart pods to pick up new config
kubectl rollout restart statefulset/nats-platform -n vrsky-platform
```

### Vertical Scaling (Increase Resources)

Edit `statefulset.yaml` and update resources:

```yaml
resources:
  requests:
    cpu: 4
    memory: 8Gi
  limits:
    cpu: 8
    memory: 16Gi
```

Then apply:

```bash
kubectl apply -f statefulset.yaml
kubectl rollout restart statefulset/nats-platform -n vrsky-platform
```

## Backup & Recovery

### Backup JetStream Data

```bash
# Backup from pod
kubectl exec -n vrsky-platform nats-platform-0 -- tar czf /tmp/jetstream-backup.tar.gz /data/jetstream

# Copy to local
kubectl cp vrsky-platform/nats-platform-0:/tmp/jetstream-backup.tar.gz ./jetstream-backup.tar.gz
```

### Restore JetStream Data

```bash
# Copy backup to pod
kubectl cp ./jetstream-backup.tar.gz vrsky-platform/nats-platform-0:/tmp/

# Restore
kubectl exec -n vrsky-platform nats-platform-0 -- tar xzf /tmp/jetstream-backup.tar.gz -C /
```

## References

- [NATS Documentation](https://docs.nats.io/)
- [JetStream Design](https://docs.nats.io/nats-concepts/jetstream)
- [NATS KV Store](https://docs.nats.io/nats-concepts/jetstream/key-value-store)
- [VRSky NATS Architecture](../../../docs/NATS_ARCHITECTURE.md)
