# Tenant NATS Deployment Templates

This directory contains templates and scripts for provisioning **Tenant-scoped NATS instances** - ephemeral, single-node NATS Core instances used for fast message transport within a tenant boundary.

## Architecture

- **Type**: Deployment (1 replica per instance)
- **NATS Mode**: Core only (no JetStream, no KV)
- **Resources**: 2 CPU, 4GB RAM per instance (requests: 1 CPU, 2GB RAM)
- **Storage**: None (fully ephemeral, in-memory only)
- **Isolation**: Kubernetes NetworkPolicy enforces tenant isolation

## Key Concepts

### Multi-Instance per Tenant

Each tenant can have multiple NATS instances:

- **Instance 1**: First 50 integrations
- **Instance 2**: Next 50 integrations (auto-provisioned when threshold reached)
- **Instance N**: Continues scaling as needed

### Naming Convention

```
nats-{tenant-id}-{instance-number}

Examples:
  nats-demo-tenant-1
  nats-acme-corp-1
  nats-acme-corp-2
```

### Auto-Scaling Triggers

New instances are provisioned when:

1. **Integration Count** ≥ 50 per instance
2. **Message Rate** > 100K msgs/sec sustained for 5 minutes
3. **Connection Count** > 500

## Files

1. **namespace.yaml** - Creates `vrsky-tenants` namespace
2. **deployment-template.yaml** - Deployment template (use with `envsubst`)
3. **service-template.yaml** - Service template
4. **networkpolicy-template.yaml** - Network isolation policy
5. **provision-tenant-nats.sh** - Script to create new NATS instance
6. **delete-tenant-nats.sh** - Script to delete NATS instance

## Provisioning a New Tenant NATS Instance

### Quick Provision

```bash
# Provision first instance for tenant "demo-tenant"
./provision-tenant-nats.sh demo-tenant 1

# Provision second instance (when scaling)
./provision-tenant-nats.sh demo-tenant 2
```

### Manual Provision (Using envsubst)

```bash
# Set environment variables
export TENANT_ID=demo-tenant
export INSTANCE_NUM=1

# Process templates
envsubst < deployment-template.yaml > deployment.yaml
envsubst < service-template.yaml > service.yaml
envsubst < networkpolicy-template.yaml > networkpolicy.yaml

# Apply manifests
kubectl apply -f namespace.yaml
kubectl apply -f deployment.yaml
kubectl apply -f service.yaml
kubectl apply -f networkpolicy.yaml

# Verify
kubectl get pods -n vrsky-tenants -l tenant-id=demo-tenant
```

### Using Helm (Alternative)

```bash
# Install Helm release
helm install nats-demo-tenant-1 . \
  --set tenantId=demo-tenant \
  --set instanceNum=1 \
  --namespace vrsky-tenants
```

## Verification

### Check Pod Status

```bash
# List all tenant NATS pods
kubectl get pods -n vrsky-tenants

# Get specific tenant's pods
kubectl get pods -n vrsky-tenants -l tenant-id=demo-tenant

# Check pod details
kubectl describe pod -n vrsky-tenants -l tenant-id=demo-tenant,instance-num=1
```

### Test NATS Connection

```bash
# Get service DNS name
SERVICE_DNS=nats-demo-tenant-1.vrsky-tenants.svc.cluster.local:4222

# Test from nats-box
kubectl run -it --rm nats-box \
  --image=natsio/nats-box \
  --restart=Never \
  -n vrsky-tenants \
  --labels="tenant-id=demo-tenant" \
  -- nats server info $SERVICE_DNS

# Expected output: Server info with version, connections, etc.
```

### Publish/Subscribe Test

```bash
# Terminal 1: Subscribe
kubectl run -it --rm nats-sub \
  --image=natsio/nats-box \
  --restart=Never \
  -n vrsky-tenants \
  --labels="tenant-id=demo-tenant" \
  -- nats sub "demo.>" --server=nats-demo-tenant-1.vrsky-tenants.svc.cluster.local:4222

# Terminal 2: Publish
kubectl run -it --rm nats-pub \
  --image=natsio/nats-box \
  --restart=Never \
  -n vrsky-tenants \
  --labels="tenant-id=demo-tenant" \
  -- nats pub "demo.test" "Hello World" --server=nats-demo-tenant-1.vrsky-tenants.svc.cluster.local:4222
```

### Check Metrics

```bash
# Port-forward monitoring endpoint
kubectl port-forward -n vrsky-tenants svc/nats-demo-tenant-1 8222:8222

# Get server stats
curl http://localhost:8222/varz | jq .

# Get connection stats
curl http://localhost:8222/connz | jq .
```

## Deleting a Tenant NATS Instance

### Quick Delete

```bash
# Delete instance
./delete-tenant-nats.sh demo-tenant 1
```

### Manual Delete

```bash
# Delete resources
kubectl delete deployment nats-demo-tenant-1 -n vrsky-tenants
kubectl delete service nats-demo-tenant-1 -n vrsky-tenants
kubectl delete networkpolicy tenant-nats-isolation-demo-tenant -n vrsky-tenants
```

## Network Isolation

### NetworkPolicy Enforcement

Each tenant's NATS instance has a NetworkPolicy that:

**Ingress (Allowed)**:

- Connections from pods with same `tenant-id` label (port 4222)
- Monitoring from `vrsky-monitoring` namespace (port 8222)

**Egress (Allowed)**:

- DNS resolution (kube-system namespace, UDP 53)
- All outbound connections (for POC simplicity)

**Denied**:

- Cross-tenant access (pods without matching `tenant-id` label)
- Direct external access

### Testing Network Isolation

```bash
# This SHOULD work (same tenant label)
kubectl run -it --rm test-allowed \
  --image=natsio/nats-box \
  --restart=Never \
  -n vrsky-tenants \
  --labels="tenant-id=demo-tenant" \
  -- nats server info nats-demo-tenant-1.vrsky-tenants.svc.cluster.local:4222

# This SHOULD fail (different tenant label)
kubectl run -it --rm test-denied \
  --image=natsio/nats-box \
  --restart=Never \
  -n vrsky-tenants \
  --labels="tenant-id=other-tenant" \
  -- nats server info nats-demo-tenant-1.vrsky-tenants.svc.cluster.local:4222

# Expected: Connection timeout or refused
```

## Integration with VRSky Platform

### Service Discovery

When a new NATS instance is provisioned:

1. **DNS Entry Auto-Created**:

   ```
   nats-{tenant-id}-{instance-num}.vrsky-tenants.svc.cluster.local:4222
   ```

2. **Control Plane Tracking**:

   ```sql
   INSERT INTO nats_instances (tenant_id, instance_number, dns_name, status)
   VALUES ('demo-tenant', 1, 'nats-demo-tenant-1.vrsky-tenants.svc.cluster.local:4222', 'active');
   ```

3. **Worker Discovery**:

   ```go
   // Workers query control plane for all tenant NATS instances
   instances := controlPlane.GetNATSInstances(tenantID)
   // Returns: ["nats-demo-tenant-1:4222", "nats-demo-tenant-2:4222"]

   // Connect to all instances (NATS client handles failover)
   nc, err := nats.Connect(strings.Join(instances, ","))
   ```

### Worker Connection Example

```go
package main

import (
    "github.com/nats-io/nats.go"
    "strings"
)

func connectToTenantNATS(tenantID string) (*nats.Conn, error) {
    // Fetch all NATS instances for this tenant from control plane
    instances := []string{
        "nats-demo-tenant-1.vrsky-tenants.svc.cluster.local:4222",
        "nats-demo-tenant-2.vrsky-tenants.svc.cluster.local:4222",
    }

    // Connect to all instances (NATS handles round-robin and failover)
    nc, err := nats.Connect(
        strings.Join(instances, ","),
        nats.Name("worker-"+tenantID),
        nats.ReconnectWait(2*time.Second),
        nats.MaxReconnects(-1), // Infinite reconnects
    )

    return nc, err
}

func main() {
    nc, err := connectToTenantNATS("demo-tenant")
    if err != nil {
        log.Fatal(err)
    }
    defer nc.Close()

    // Subscribe to tenant subjects
    nc.Subscribe("demo-tenant.webhook.*", handleWebhook)
    nc.Subscribe("demo-tenant.convert.*", handleConversion)
}
```

## Monitoring

### Key Metrics to Track

| Metric             | Description        | Threshold                      |
| ------------------ | ------------------ | ------------------------------ |
| `msg_rate`         | Messages/sec       | Alert if > 80K (80% capacity)  |
| `connection_count` | Active connections | Alert if > 500                 |
| `memory_usage`     | Memory usage (MB)  | Alert if > 3GB (75% of 4GB)    |
| `cpu_usage`        | CPU usage (%)      | Alert if > 150% (75% of 2 CPU) |

### Prometheus Scrape Config

```yaml
scrape_configs:
  - job_name: "tenant-nats"
    kubernetes_sd_configs:
      - role: pod
        namespaces:
          names:
            - vrsky-tenants
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_app]
        action: keep
        regex: nats
      - source_labels: [__meta_kubernetes_pod_label_tenant_id]
        target_label: tenant_id
      - source_labels: [__meta_kubernetes_pod_label_instance_num]
        target_label: instance_num
      - source_labels: [__address__]
        target_label: __address__
        replacement: $1:8222
    metrics_path: /metrics
```

### Grafana Dashboard Queries

```promql
# Message rate per tenant
rate(nats_server_in_msgs{job="tenant-nats"}[5m])

# Connection count
nats_server_connections{job="tenant-nats"}

# Memory usage
container_memory_usage_bytes{namespace="vrsky-tenants",pod=~"nats-.*"}
```

## Scaling Scenarios

### Scenario 1: Integration Count Threshold

```bash
# Tenant has 50 integrations on instance 1
# Control plane detects threshold reached
# Auto-provision instance 2
./provision-tenant-nats.sh demo-tenant 2

# Assign new integrations to instance 2
# Update integration records in PostgreSQL
UPDATE integrations
SET nats_instance_id = (SELECT id FROM nats_instances WHERE tenant_id='demo-tenant' AND instance_number=2)
WHERE tenant_id='demo-tenant' AND id IN (SELECT id FROM integrations WHERE nats_instance_id IS NULL LIMIT 10);
```

### Scenario 2: High Message Rate

```bash
# Monitoring detects sustained 100K+ msgs/sec
# Auto-provision instance 2
./provision-tenant-nats.sh demo-tenant 2

# Rebalance integrations across instances
# Move 25 integrations from instance 1 to instance 2
```

### Scenario 3: Scale Down

```bash
# If instance 2 drops below 25 integrations
# Migrate all integrations to instance 1
# Delete instance 2
./delete-tenant-nats.sh demo-tenant 2
```

## Troubleshooting

### Pod Not Starting

```bash
# Check pod events
kubectl describe pod -n vrsky-tenants -l tenant-id=demo-tenant,instance-num=1

# Check logs
kubectl logs -n vrsky-tenants -l tenant-id=demo-tenant,instance-num=1

# Common issues:
# - Image pull errors: Check internet connectivity
# - Resource limits: Check node capacity
# - Port conflicts: Verify no other NATS on same ports
```

### Cannot Connect from Worker

```bash
# Verify service exists
kubectl get svc -n vrsky-tenants | grep demo-tenant

# Test DNS resolution
kubectl run -it --rm dns-test \
  --image=busybox \
  --restart=Never \
  -n vrsky-tenants \
  -- nslookup nats-demo-tenant-1.vrsky-tenants.svc.cluster.local

# Verify NetworkPolicy allows worker
# Worker pods MUST have label: tenant-id=demo-tenant
```

### High Memory Usage

```bash
# Check memory usage
kubectl top pod -n vrsky-tenants -l tenant-id=demo-tenant

# If consistently > 3GB, provision new instance
# NATS Core should use minimal memory (ephemeral)
```

## Best Practices

1. **Always use NetworkPolicy**: Enforce tenant isolation
2. **Label worker pods**: Include `tenant-id` label for NetworkPolicy
3. **Monitor capacity**: Track integration count and message rate
4. **Auto-scale proactively**: Provision before hitting limits
5. **Delete unused instances**: Clean up when below 25 integrations
6. **Track in PostgreSQL**: Keep `nats_instances` table updated
7. **Health checks**: Regularly verify `/healthz` endpoint

## References

- [NATS Documentation](https://docs.nats.io/)
- [NATS Core vs JetStream](https://docs.nats.io/nats-concepts/jetstream)
- [Kubernetes NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/)
- [VRSky NATS Architecture](../../../docs/NATS_ARCHITECTURE.md)
