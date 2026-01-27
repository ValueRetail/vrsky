# VRSky Monitoring Stack

This directory contains Helm values and installation scripts for deploying the VRSky monitoring stack using **Prometheus** and **Grafana**.

## Architecture

### Components

- **Prometheus** (kube-prometheus-stack): Metrics collection and alerting
  - Prometheus Server: 2 CPU, 4GB RAM, 50GB storage
  - Alertmanager: Alert routing and management
  - Node Exporter: Node-level metrics
  - Kube State Metrics: Kubernetes resource metrics
- **Grafana**: Visualization and dashboards
  - Resources: 1 CPU, 1GB RAM, 10GB storage
  - Pre-installed dashboards for Kubernetes, NATS, PostgreSQL, MinIO

### Monitoring Targets

| Target            | Metrics Endpoint                 | Purpose                              |
| ----------------- | -------------------------------- | ------------------------------------ |
| **Platform NATS** | `:8222/metrics`                  | JetStream, KV bucket metrics         |
| **Tenant NATS**   | `:8222/metrics`                  | Message rate, connections per tenant |
| **PostgreSQL**    | `:9187` (postgres_exporter)      | Database performance                 |
| **MinIO**         | `:9000/minio/v2/metrics/cluster` | Storage usage, API calls             |
| **Kubernetes**    | kube-state-metrics               | Pod, node, deployment health         |
| **Nodes**         | node-exporter                    | CPU, memory, disk, network           |

## Installation

### Prerequisites

```bash
# Install Helm 3
curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash

# Verify Helm
helm version
```

### Quick Install

```bash
# Run installation script
./install-monitoring.sh

# Wait for all pods to be ready (takes 3-5 minutes)
kubectl wait --for=condition=ready pod --all -n vrsky-monitoring --timeout=10m
```

### Manual Install

```bash
# 1. Create namespace
kubectl apply -f namespace.yaml

# 2. Add Helm repositories
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update

# 3. Install Prometheus
helm install prometheus \
  prometheus-community/kube-prometheus-stack \
  --namespace vrsky-monitoring \
  --values prometheus-values.yaml \
  --wait

# 4. Install Grafana
helm install grafana \
  grafana/grafana \
  --namespace vrsky-monitoring \
  --values grafana-values.yaml \
  --wait

# 5. Verify installation
kubectl get pods -n vrsky-monitoring
```

## Accessing Dashboards

### Grafana

```bash
# Port-forward to local machine
kubectl port-forward -n vrsky-monitoring svc/grafana 3000:80

# Open browser: http://localhost:3000
# Login:
#   Username: admin
#   Password: changeme-grafana-password
```

### Prometheus

```bash
# Port-forward to local machine
kubectl port-forward -n vrsky-monitoring svc/prometheus-kube-prometheus-prometheus 9090:9090

# Open browser: http://localhost:9090
```

### Alertmanager

```bash
# Port-forward to local machine
kubectl port-forward -n vrsky-monitoring svc/prometheus-kube-prometheus-alertmanager 9093:9093

# Open browser: http://localhost:9093
```

## Pre-Installed Dashboards

Grafana comes with these pre-configured dashboards:

1. **Kubernetes Cluster** (ID: 7249) - Overall cluster health
2. **Node Exporter Full** (ID: 1860) - Detailed node metrics
3. **NATS Dashboard** (ID: 2279) - NATS server metrics
4. **PostgreSQL** (ID: 9628) - Database performance
5. **MinIO** (ID: 13502) - Object storage metrics

Access: Grafana → Dashboards → Browse → VRSky folder

## Key Metrics to Monitor

### Platform NATS

```promql
# JetStream stream size
sum(jetstream_stream_bytes) by (stream_name)

# KV bucket size
sum(jetstream_kv_bytes) by (bucket_name)

# Message rate
rate(nats_server_in_msgs[5m])

# Consumer lag
jetstream_consumer_num_pending
```

### Tenant NATS

```promql
# Message rate per tenant
rate(nats_server_in_msgs{job="tenant-nats"}[5m])

# Connections per tenant
nats_server_connections{job="tenant-nats"}

# Memory usage per tenant
container_memory_usage_bytes{namespace="vrsky-tenants",pod=~"nats-.*"}
```

### PostgreSQL

```promql
# Active connections
pg_stat_database_numbackends{datname="vrsky"}

# Query duration
rate(pg_stat_statements_mean_exec_time_seconds[5m])

# Database size
pg_database_size_bytes{datname="vrsky"}
```

### MinIO

```promql
# Storage usage
minio_bucket_usage_total_bytes

# API request rate
rate(minio_s3_requests_total[5m])

# Upload/download bytes
rate(minio_s3_traffic_sent_bytes[5m])
rate(minio_s3_traffic_received_bytes[5m])
```

### Kubernetes

```promql
# Pod restarts
rate(kube_pod_container_status_restarts_total[15m]) > 0

# Node CPU usage
100 - (avg by (node) (irate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)

# Node memory usage
(1 - (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes)) * 100
```

## Alerting

### Configured Alerts

The Prometheus stack comes with default alerts for:

- Kubernetes resources (pod crashes, high CPU, out of memory)
- Node issues (disk full, high load)
- Prometheus itself (scrape failures, rule evaluation errors)

### Custom VRSky Alerts

Add custom alerts by creating a PrometheusRule:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: vrsky-alerts
  namespace: vrsky-monitoring
spec:
  groups:
    - name: vrsky
      interval: 30s
      rules:
        - alert: TenantNATSHighLoad
          expr: rate(nats_server_in_msgs{job="tenant-nats"}[5m]) > 80000
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: "Tenant NATS instance {{ $labels.tenant_id }}-{{ $labels.instance_num }} high load"
            description: "Message rate exceeds 80K/sec ({{ $value }}/sec)"

        - alert: PlatformNATSKVFull
          expr: jetstream_kv_bytes > 80e9 # 80GB
          for: 10m
          labels:
            severity: critical
          annotations:
            summary: "Platform NATS KV bucket {{ $labels.bucket_name }} nearly full"
            description: "KV bucket size: {{ $value | humanize }}B"

        - alert: DeadLetterQueueGrowing
          expr: jetstream_kv_keys{bucket_name="dead_letter_queue"} > 10000
          for: 15m
          labels:
            severity: warning
          annotations:
            summary: "Dead letter queue has {{ $value }} messages"
            description: "Investigate failed message patterns"
```

Apply:

```bash
kubectl apply -f vrsky-alerts.yaml
```

### Alert Notification Channels

Configure Alertmanager to send alerts via:

- Email
- Slack
- PagerDuty
- Webhook

Edit `prometheus-values.yaml` and add:

```yaml
alertmanager:
  config:
    global:
      resolve_timeout: 5m
    route:
      receiver: "slack-notifications"
      group_by: ["alertname", "cluster", "service"]
      group_wait: 10s
      group_interval: 10s
      repeat_interval: 12h
    receivers:
      - name: "slack-notifications"
        slack_configs:
          - api_url: "https://hooks.slack.com/services/YOUR/SLACK/WEBHOOK"
            channel: "#vrsky-alerts"
            title: "VRSky Alert"
```

## Storage & Retention

### Prometheus

- **Retention**: 15 days
- **Storage**: 50GB Longhorn volume
- **Scrape Interval**: 30 seconds

To increase retention:

```yaml
# Edit prometheus-values.yaml
prometheus:
  prometheusSpec:
    retention: 30d # Increase to 30 days
    retentionSize: "90GB" # Adjust storage accordingly
```

### Grafana

- **Storage**: 10GB Longhorn volume
- **Dashboards**: Stored in database
- **Backups**: Export dashboards as JSON

## Backup & Recovery

### Backup Grafana Dashboards

```bash
# Export all dashboards
kubectl exec -n vrsky-monitoring deployment/grafana -- \
  grafana-cli admin export-dashboard --all > grafana-dashboards-backup.json
```

### Backup Prometheus Data

```bash
# Create Longhorn snapshot
kubectl annotate pvc prometheus-prometheus-kube-prometheus-prometheus-db-prometheus-kube-prometheus-prometheus-0 \
  -n vrsky-monitoring \
  snapshot.storage.kubernetes.io/snapshot-name=prometheus-backup-$(date +%Y%m%d-%H%M%S)
```

### Restore Grafana Dashboards

```bash
# Import dashboards
kubectl exec -n vrsky-monitoring deployment/grafana -- \
  grafana-cli admin import < grafana-dashboards-backup.json
```

## Troubleshooting

### Prometheus Not Scraping Targets

```bash
# Check Prometheus targets
kubectl port-forward -n vrsky-monitoring svc/prometheus-kube-prometheus-prometheus 9090:9090

# Open: http://localhost:9090/targets
# Look for targets with status "Down"

# Check service discovery
# Open: http://localhost:9090/service-discovery

# Verify pod labels match scrape config
kubectl get pods -n vrsky-platform --show-labels
```

### Grafana Dashboard Not Loading

```bash
# Check Grafana logs
kubectl logs -n vrsky-monitoring deployment/grafana

# Verify Prometheus datasource
kubectl exec -n vrsky-monitoring deployment/grafana -- \
  curl http://prometheus-kube-prometheus-prometheus.vrsky-monitoring.svc.cluster.local:9090/api/v1/query?query=up

# Check datasource config in Grafana UI:
# Configuration → Data sources → Prometheus
```

### High Prometheus Memory Usage

```bash
# Check memory usage
kubectl top pod -n vrsky-monitoring -l app.kubernetes.io/name=prometheus

# Reduce retention or increase memory limits
# Edit prometheus-values.yaml and apply
helm upgrade prometheus prometheus-community/kube-prometheus-stack \
  -n vrsky-monitoring \
  -f prometheus-values.yaml
```

### Missing Metrics

```bash
# Check if exporter is running
kubectl get pods -n vrsky-monitoring | grep exporter

# Verify service endpoints
kubectl get endpoints -n vrsky-monitoring

# Test metric endpoint directly
kubectl run -it --rm curl --image=curlimages/curl --restart=Never -- \
  curl http://minio.vrsky-storage.svc.cluster.local:9000/minio/v2/metrics/cluster
```

## Scaling

### Increase Prometheus Storage

```bash
# Edit PVC
kubectl edit pvc prometheus-prometheus-kube-prometheus-prometheus-db-prometheus-kube-prometheus-prometheus-0 \
  -n vrsky-monitoring

# Change storage: 50Gi → 100Gi
# Longhorn will auto-expand
```

### Increase Prometheus Resources

Edit `prometheus-values.yaml`:

```yaml
prometheus:
  prometheusSpec:
    resources:
      requests:
        cpu: 2
        memory: 4Gi
      limits:
        cpu: 4
        memory: 8Gi
```

Apply:

```bash
helm upgrade prometheus prometheus-community/kube-prometheus-stack \
  -n vrsky-monitoring \
  -f prometheus-values.yaml
```

## Upgrading

### Upgrade Prometheus

```bash
# Update Helm repo
helm repo update

# Check new version
helm search repo prometheus-community/kube-prometheus-stack

# Upgrade
helm upgrade prometheus prometheus-community/kube-prometheus-stack \
  -n vrsky-monitoring \
  -f prometheus-values.yaml
```

### Upgrade Grafana

```bash
helm repo update
helm upgrade grafana grafana/grafana \
  -n vrsky-monitoring \
  -f grafana-values.yaml
```

## Uninstall

```bash
# Uninstall Grafana
helm uninstall grafana -n vrsky-monitoring

# Uninstall Prometheus
helm uninstall prometheus -n vrsky-monitoring

# Delete PVCs (optional - will delete all data!)
kubectl delete pvc -n vrsky-monitoring --all

# Delete namespace
kubectl delete namespace vrsky-monitoring
```

## References

- [Prometheus Documentation](https://prometheus.io/docs/)
- [Grafana Documentation](https://grafana.com/docs/)
- [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack)
- [NATS Prometheus Exporter](https://docs.nats.io/running-a-nats-service/nats_admin/monitoring)
