# VRSky Converter Kubernetes Deployment Guide

## Overview

This directory contains Kubernetes manifests for deploying the VRSky Converter component in production environments. The Converter is responsible for transforming data from any source format to any target format using rule engines, field mapping, and pluggable functions.

## Prerequisites

- Kubernetes 1.20+
- Docker image: `localhost:5000/vrsky/converter:latest` (or your registry)
- NATS cluster deployed (see `../platform-nats/`)
- PostgreSQL instance (see `../postgresql/`)
- MinIO instance (see `../minio/`)
- ConfigService available at `http://config-service:8080`

## Quick Start

### 1. Deploy Converter Components

```bash
# Apply all converter manifests
kubectl apply -f converter-namespace.yaml
kubectl apply -f configmap.yaml
kubectl apply -f deployment.yaml
kubectl apply -f service.yaml

# Or apply all at once
kubectl apply -f ./

# Verify deployment
kubectl get deployments -n vrsky-platform -l app=vrsky-converter
kubectl get pods -n vrsky-platform -l app=vrsky-converter
kubectl get svc -n vrsky-platform -l app=vrsky-converter
```

### 2. Check Converter Status

```bash
# View pod status
kubectl get pods -n vrsky-platform -l app=vrsky-converter -o wide

# Check logs
kubectl logs -n vrsky-platform -l app=vrsky-converter -f

# Describe deployment
kubectl describe deployment vrsky-converter -n vrsky-platform

# Check metrics endpoint
kubectl port-forward -n vrsky-platform svc/vrsky-converter 8080:8080
curl http://localhost:8080/metrics
```

### 3. Verify Health

```bash
# Check health endpoint
kubectl exec -it -n vrsky-platform deployment/vrsky-converter -- curl http://localhost:8080/health

# Check readiness endpoint
kubectl exec -it -n vrsky-platform deployment/vrsky-converter -- curl http://localhost:8080/ready
```

## Manifest Details

### converter-namespace.yaml
- Creates the `vrsky-platform` namespace
- Labels for easy filtering and resource organization

### configmap.yaml
- Default converter configuration
- Values can be overridden via environment variables
- Per-tenant configuration loaded from ConfigService API

### deployment.yaml
- **Replicas**: 3 for high availability
- **Strategy**: Rolling update with maxSurge=1, maxUnavailable=0
- **Affinity**: Pod anti-affinity to spread replicas across nodes
- **Resources**:
  - Request: 1000m CPU, 2Gi memory
  - Limit: 2000m CPU, 4Gi memory
- **Probes**:
  - Liveness: HTTP GET /health (30s initial delay, 10s interval)
  - Readiness: HTTP GET /ready (10s initial delay, 5s interval)
- **Security**:
  - Non-root user (UID 1000)
  - Read-only root filesystem
  - All capabilities dropped
- **Volumes**: EmptyDir for /tmp, cache, and WASM modules

### service.yaml
- **Type**: ClusterIP (internal-only)
- **Ports**:
  - 8080: HTTP metrics and health endpoints
  - 9090: gRPC service for transformations

## Configuration

### Environment Variables

#### Server Configuration
- `CONVERTER_PORT`: Server port (default: 8080)
- `GRACEFUL_SHUTDOWN_TIMEOUT`: Graceful shutdown timeout in seconds (default: 15)
- `HEALTH_CHECK_INTERVAL`: Health check interval in seconds (default: 30)

#### Logging
- `LOG_LEVEL`: Log level (debug/info/warn/error, default: info)
- `LOG_FORMAT`: Log format (json/text, default: json)

#### Performance
- `MAX_CONCURRENT_REQUESTS`: Maximum concurrent requests (default: 1000)
- `REQUEST_TIMEOUT`: Request timeout in seconds (default: 30)
- `CACHE_TTL`: Cache TTL in seconds (default: 300)

#### Validation
- `VALIDATION_MODE`: Validation mode (strict/lenient, default: strict)
- `VALIDATION_ERROR_STRATEGY`: Error strategy (skip/coerce/fail, default: fail)

#### Database
- `POSTGRES_URL`: PostgreSQL connection string
- `POSTGRES_MAX_CONNECTIONS`: Maximum database connections (default: 25)
- `POSTGRES_CONNECTION_TIMEOUT`: Connection timeout in seconds (default: 5)

#### Storage
- `MINIO_ENDPOINT`: MinIO endpoint
- `MINIO_REGION`: MinIO region (default: us-east-1)
- `MINIO_BUCKET`: MinIO bucket name (default: vrsky-converter)

#### NATS
- `NATS_URL`: NATS server URL
- `NATS_REQUEST_TIMEOUT`: NATS request timeout in seconds (default: 30)
- `NATS_TOPICS_PREFIX`: NATS topics prefix (default: vrsky)

#### WASM
- `WASM_MODULE_DIR`: WASM module directory (default: /app/wasm-modules)
- `WASM_MEMORY_PAGES`: WASM memory pages (default: 256)
- `WASM_SANDBOX_ENABLED`: Enable WASM sandboxing (default: true)

### ConfigService API

Per-tenant configuration can be loaded from ConfigService API:

```bash
# Example: Get converter config for a tenant
curl http://config-service:8080/v1/converter-config/tenant-id/converter-id

# Response format:
{
  "converter_id": "conv-001",
  "tenant_id": "tenant-001",
  "input_topic": "webhook.received",
  "output_topic": "webhook.converted",
  "error_topic": "webhook.errors",
  "transformations": [...],
  "error_handling": {
    "missing_fields": "skip",
    "type_mismatch": "coerce",
    "validation_error": "fail"
  },
  "max_retries": 3,
  "retry_backoff": "exponential"
}
```

## Scaling and Performance

### Horizontal Scaling

Increase replicas for higher throughput:

```bash
# Scale to 5 replicas
kubectl scale deployment vrsky-converter -n vrsky-platform --replicas=5

# Or edit deployment
kubectl edit deployment vrsky-converter -n vrsky-platform
```

### Resource Tuning

Adjust resource requests/limits based on workload:

```yaml
# For low-latency transformations
resources:
  requests:
    cpu: 2000m      # Higher CPU for faster processing
    memory: 2Gi
  limits:
    cpu: 4000m
    memory: 4Gi

# For high-volume transformations
resources:
  requests:
    cpu: 1000m
    memory: 4Gi     # More memory for caching
  limits:
    cpu: 2000m
    memory: 8Gi
```

## Monitoring

### Prometheus Metrics

The converter exposes Prometheus metrics on port 8080 at `/metrics`:

- `vrsky_converter_messages_received_total` - Total messages received
- `vrsky_converter_messages_succeeded_total` - Successfully transformed messages
- `vrsky_converter_messages_failed_total` - Failed transformations
- `vrsky_converter_transformation_duration_seconds` - Transformation latency histogram
- `vrsky_converter_retry_attempts_total` - Total retry attempts

### ServiceMonitor (Optional)

For Prometheus Operator integration:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: vrsky-converter
  namespace: vrsky-platform
spec:
  selector:
    matchLabels:
      app: vrsky-converter
  endpoints:
  - port: http-metrics
    interval: 30s
    path: /metrics
```

## Troubleshooting

### Pods Not Starting

```bash
# Check pod status and events
kubectl describe pod -n vrsky-platform -l app=vrsky-converter

# Check logs for errors
kubectl logs -n vrsky-platform -l app=vrsky-converter

# Check resource availability
kubectl top nodes
kubectl top pods -n vrsky-platform
```

### High Memory Usage

- Reduce `CACHE_TTL` if cache is growing too large
- Reduce `MAX_CONCURRENT_REQUESTS` to limit in-flight transformations
- Increase memory limits if load is legitimate

### Transformation Failures

- Check ConfigService connectivity: `curl http://config-service:8080/health`
- Verify schema validation: Check if validation mode is too strict
- Review application logs for transformation errors
- Check PostgreSQL connectivity for lookup functions

### Pod Crashing (OOMKilled)

```bash
# Check event reason
kubectl describe pod <pod-name> -n vrsky-platform

# Increase memory limit
kubectl set resources deployment vrsky-converter -n vrsky-platform --limits=memory=6Gi
```

## Deployment Checklist

- [ ] NATS cluster running and accessible
- [ ] PostgreSQL instance running with proper permissions
- [ ] MinIO configured with appropriate bucket and policies
- [ ] ConfigService deployed and healthy
- [ ] Docker image built and pushed to registry
- [ ] Converter manifests reviewed and customized
- [ ] Resource requests/limits appropriate for workload
- [ ] Health check endpoints implemented in converter
- [ ] Prometheus monitoring configured
- [ ] Alerting rules defined for converter component
- [ ] Backup strategy for converter state (if applicable)
- [ ] Disaster recovery plan documented

## Multi-Tenant Deployment

For multi-tenant deployments, deploy separate converter instances:

```bash
# Deploy converter for tenant-1
kubectl set env deployment/vrsky-converter -n vrsky-platform \
  TENANT_ID=tenant-1 \
  CONVERTER_ID=conv-tenant1

# Deploy converter for tenant-2
kubectl set env deployment/vrsky-converter -n vrsky-platform \
  TENANT_ID=tenant-2 \
  CONVERTER_ID=conv-tenant2
```

Or use Helm to manage multiple instances with different values.

## Upgrade Procedure

```bash
# 1. Build new image
docker build -t localhost:5000/vrsky/converter:v1.1 .

# 2. Update deployment image
kubectl set image deployment/vrsky-converter \
  converter=localhost:5000/vrsky/converter:v1.1 \
  -n vrsky-platform

# 3. Monitor rollout progress
kubectl rollout status deployment/vrsky-converter -n vrsky-platform

# 4. Rollback if needed
kubectl rollout undo deployment/vrsky-converter -n vrsky-platform
```

## References

- [Converter Architecture](../../../docs/converter/CONVERTER_IMPLEMENTATION_GUIDE.md)
- [Configuration Reference](../../../docs/converter/CONVERTER_CONFIGURATION_REFERENCE.md)
- [Functions Reference](../../../docs/converter/CONVERTER_FUNCTIONS_REFERENCE.md)
- [Kubernetes Documentation](https://kubernetes.io/docs/)
