# Phase 1E Filter Component - Kubernetes Deployment Guide

## Overview

This guide covers deploying the VRSky Filter Component (Phase 1E) to your production Kubernetes cluster at ServeTheWorld (Oslo).

**Current Status**: ✅ READY FOR PRODUCTION

---

## Prerequisites

Before deploying, ensure you have:

1. **kubectl** installed and configured
   ```bash
   kubectl version --client
   kubectl cluster-info
   ```

2. **Connected to production cluster**
   ```bash
   kubectl config current-context
   ```

3. **vrsky-platform namespace exists**
   ```bash
   kubectl get namespace vrsky-platform
   ```

4. **NATS platform running**
   ```bash
   kubectl get svc -n vrsky-platform nats-platform
   ```

5. **Docker image available**
   ```bash
   docker images vrsky/filter:latest
   ```

---

## Quick Deployment (Automated)

### Option 1: Automated Deployment Script

```bash
bash /home/ludvik/vrsky/infrastructure/kubernetes/deploy-filter.sh
```

This script will:
- ✅ Verify prerequisites
- ✅ Apply deployment and service manifests
- ✅ Wait for 3 replicas to be ready
- ✅ Display deployment status
- ✅ Run smoke tests

### Option 2: Manual kubectl Commands

```bash
# Navigate to filter directory
cd /home/ludvik/vrsky/infrastructure/kubernetes/filter

# Apply manifests
kubectl apply -f deployment.yaml
kubectl apply -f service.yaml

# Wait for pods to be ready
kubectl wait --for=condition=ready pod -l app=vrsky-filter -n vrsky-platform --timeout=300s

# Verify deployment
kubectl get deployment -n vrsky-platform vrsky-filter
kubectl get pods -n vrsky-platform -l app=vrsky-filter
```

---

## Deployment Files

### deployment.yaml

**Key Configuration**:
- **Replicas**: 3 (HA across nodes)
- **Image**: `vrsky/filter:latest`
- **CPU Requests**: 1 core / **Limits**: 2 cores
- **Memory Requests**: 2Gi / **Limits**: 4Gi
- **Pod Anti-Affinity**: Spreads replicas across nodes (preferred)
- **Health Probes**: 
  - Liveness: every 30s (timeout 5s)
  - Readiness: every 10s (timeout 3s)
- **Security Context**: 
  - Non-root user (1000)
  - Read-only root filesystem
  - No privilege escalation

**Environment Variables**:
```yaml
NATS_URL: nats://nats-platform:4222
FILTER_ID: vrsky-filter
INPUT_TOPIC: postgres.changes
OUTPUT_TOPIC: filter.output
REJECTION_TOPIC: filter.rejection
LOG_LEVEL: info
```

### service.yaml

**Type**: ClusterIP (internal only)
**Port**: 9090
**Protocol**: TCP

---

## Verification Steps

### 1. Check Deployment Status

```bash
# View deployment
kubectl get deployment -n vrsky-platform vrsky-filter

# View pods
kubectl get pods -n vrsky-platform -l app=vrsky-filter

# View service
kubectl get svc -n vrsky-platform vrsky-filter
```

**Expected Output**:
```
NAME           READY   UP-TO-DATE   AVAILABLE   AGE
vrsky-filter   3/3     3            3           1m

NAME                       READY   STATUS    RESTARTS   AGE
vrsky-filter-xxxxx-yyyyy   1/1     Running   0          1m
vrsky-filter-xxxxx-zzzzz   1/1     Running   0          1m
vrsky-filter-xxxxx-wwwww   1/1     Running   0          1m

NAME           TYPE        CLUSTER-IP     EXTERNAL-IP   PORT(S)    AGE
vrsky-filter   ClusterIP   10.43.xxx.xxx  <none>        9090/TCP   1m
```

### 2. Run Smoke Tests

```bash
bash /home/ludvik/vrsky/infrastructure/scripts/test-filter-smoke.sh
```

**Tests Include**:
- ✅ Pod running and healthy (all 3 replicas)
- ✅ NATS connectivity verified
- ✅ No errors in logs
- ✅ All 3 priorities framework ready (Gating, Routing, Rate Limiting)
- ✅ Resource usage verification
- ✅ Pod restart stability

### 3. Check Logs

```bash
# All filter pods
kubectl logs -f -n vrsky-platform -l app=vrsky-filter

# Specific pod
kubectl logs -f -n vrsky-platform vrsky-filter-xxxxx

# Last 50 lines
kubectl logs -n vrsky-platform -l app=vrsky-filter --tail=50
```

**Expected Log Output**:
```json
{
  "time": "2026-02-17T13:00:00Z",
  "level": "INFO",
  "msg": "Filter configuration loaded",
  "filter_id": "vrsky-filter",
  "input_topic": "postgres.changes",
  "output_topic": "filter.output"
}
{
  "time": "2026-02-17T13:00:00Z",
  "level": "INFO",
  "msg": "Connecting to NATS",
  "url": "nats://nats-platform:4222"
}
{
  "time": "2026-02-17T13:00:00Z",
  "level": "INFO",
  "msg": "Connected to NATS"
}
```

### 4. Monitor Resource Usage

```bash
# Real-time metrics
kubectl top pods -n vrsky-platform -l app=vrsky-filter

# Describe pod (includes events)
kubectl describe pod -n vrsky-platform -l app=vrsky-filter
```

---

## Troubleshooting

### Pods Not Starting

**Check pod status**:
```bash
kubectl describe pod -n vrsky-platform <POD_NAME>
```

**Common Issues**:
1. Image not found → Ensure `vrsky/filter:latest` is available in registry
2. NATS not available → Verify `nats-platform` service is running
3. Insufficient resources → Check node capacity: `kubectl top nodes`

### Pod Crashing

**Check logs**:
```bash
kubectl logs -n vrsky-platform <POD_NAME> --previous
```

**Check events**:
```bash
kubectl get events -n vrsky-platform --sort-by='.lastTimestamp'
```

### No Connectivity to NATS

**Verify NATS is running**:
```bash
kubectl get pods -n vrsky-platform -l app=nats-platform
kubectl get svc -n vrsky-platform nats-platform
```

**Test connectivity from pod**:
```bash
kubectl exec -it -n vrsky-platform <POD_NAME> -- /bin/sh
# Inside pod:
nc -zv nats-platform 4222
```

### Performance Issues

**Check CPU/Memory**:
```bash
kubectl top pod -n vrsky-platform <POD_NAME>
```

**Check resource limits**:
```bash
kubectl get pod -n vrsky-platform <POD_NAME> -o yaml | grep -A 5 "resources:"
```

---

## Scaling

### Scale Replicas

```bash
# Scale to 5 replicas
kubectl scale deployment vrsky-filter -n vrsky-platform --replicas=5

# View scaling status
kubectl get deployment -n vrsky-platform vrsky-filter -w
```

### Update Resource Limits

```bash
# Edit deployment
kubectl edit deployment -n vrsky-platform vrsky-filter
```

Modify the `resources` section:
```yaml
resources:
  requests:
    cpu: 2000m          # Increase from 1000m
    memory: 4Gi         # Increase from 2Gi
  limits:
    cpu: 4000m          # Increase from 2000m
    memory: 8Gi         # Increase from 4Gi
```

---

## Rollback

### Rollback to Previous Version

```bash
# View rollout history
kubectl rollout history deployment -n vrsky-platform vrsky-filter

# Rollback to previous version
kubectl rollout undo deployment -n vrsky-platform vrsky-filter

# Rollback to specific revision
kubectl rollout undo deployment -n vrsky-platform vrsky-filter --to-revision=1
```

---

## Monitoring

### Prometheus Metrics

Filter pods export Prometheus metrics on port 9090:

```bash
# Port forward for local testing
kubectl port-forward -n vrsky-platform svc/vrsky-filter 9090:9090

# Curl metrics endpoint
curl http://localhost:9090/metrics
```

**Available Metrics**:
- `filter_gating_accept_total` - Total accepted messages
- `filter_gating_reject_total` - Total rejected messages
- `filter_routing_transform_duration_seconds` - Transform duration
- `filter_ratelimit_queue_depth` - Current queue depth
- `filter_ratelimit_dropped_total` - Dropped messages due to rate limit

### Grafana Dashboard

Access Grafana (if monitoring is installed):

```bash
kubectl port-forward -n vrsky-monitoring svc/grafana 3000:80
# Open http://localhost:3000
# Login: admin/changeme-grafana-password
```

---

## Health Endpoints

### Liveness Probe

```bash
# Command: nc -z 127.0.0.1 9090
# Tests basic connectivity to filter service
```

### Readiness Probe

```bash
# Command: nc -z 127.0.0.1 9090
# Tests if filter is accepting connections
```

---

## Environment Variables

All environment variables can be modified in `deployment.yaml`:

| Variable | Default | Purpose |
|----------|---------|---------|
| `NATS_URL` | `nats://nats-platform:4222` | NATS broker URL |
| `FILTER_ID` | `vrsky-filter` | Unique filter identifier |
| `INPUT_TOPIC` | `postgres.changes` | Input message topic |
| `OUTPUT_TOPIC` | `filter.output` | Output for accepted messages |
| `REJECTION_TOPIC` | `filter.rejection` | Output for rejected messages |
| `LOG_LEVEL` | `info` | Logging level (debug/info/warn/error) |

---

## Phase 1E Status

✅ **All Acceptance Criteria Met**:
- ✅ Gating logic (Priority 1) - 40+ unit tests
- ✅ Conditional routing (Priority 2) - 28+ unit tests
- ✅ Rate limiting (Priority 3) - 20+ unit tests
- ✅ 88+ total tests passing
- ✅ Docker image built (9.36MB)
- ✅ Kubernetes manifests created
- ✅ Deployment automation
- ✅ Smoke tests
- ✅ Security hardened
- ✅ Production-ready

---

## Support

For issues or questions:

1. Check logs: `kubectl logs -f -n vrsky-platform -l app=vrsky-filter`
2. Run smoke test: `bash infrastructure/scripts/test-filter-smoke.sh`
3. Check deployment: `kubectl describe deployment -n vrsky-platform vrsky-filter`
4. Review this guide: Troubleshooting section above

---

## Next Steps

1. ✅ Deploy filter component (Phase 1E complete)
2. 🔄 Deploy postgres-consumer (reads PostgreSQL CDC)
3. 🔄 Deploy postgres-producer (writes processed data)
4. 🔄 Deploy API Gateway
5. 🔄 Deploy Data Plane services

Phase 1E is **PRODUCTION READY** 🚀

