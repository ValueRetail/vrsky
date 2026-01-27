# VRSky Infrastructure

Infrastructure-as-Code for deploying VRSky integration platform on ServeTheWorld (Norway) using K3s Kubernetes.

## Overview

This infrastructure deploys:

- **3-node K3s cluster** on ServeTheWorld GP316 VPS
- **Longhorn distributed storage** (3-way replication)
- **Nginx Ingress Controller** for HTTP/HTTPS routing
- **Firewall configuration** (ufw) with minimal ports open
- **Cloudflare DNS** load balancing and SSL termination

**Location**: Oslo, Norway (ISO 27001 certified datacenter)  
**Cost**: 441 NOK/mo (~$42/month) first 3 months, then 882 NOK/mo (~$83/month)  
**SLA**: 99.99% uptime guarantee

---

## Quick Start

### Prerequisites

1. **Order 3× GP316 VPS** from ServeTheWorld:
   - https://my.servetheworld.net/order/vps-gp3
   - Plan: GP316 (8 vCPU, 16GB RAM, 480GB NVMe)
   - OS: Ubuntu 22.04 LTS
   - Location: Oslo, Norway

2. **Install tools** on your local machine:

   ```bash
   # macOS
   brew install terraform kubectl helm

   # Linux
   sudo apt-get install terraform kubectl
   curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
   ```

3. **Set up SSH access** to all VPS instances

### Deploy Cluster

```bash
# From this directory (infrastructure/)
./scripts/provision-cluster.sh \
  --master <MASTER_IP> \
  --workers <WORKER1_IP>,<WORKER2_IP> \
  --ssh-key ~/.ssh/id_rsa

# Expected time: ~15-20 minutes
```

### Verify Deployment

```bash
export KUBECONFIG=$(pwd)/kubeconfig
kubectl get nodes
kubectl get pods -A
```

---

## Directory Structure

```
infrastructure/
├── terraform/              # Terraform configs for Cloudflare DNS
│   ├── main.tf            # Main Terraform config
│   ├── cloudflare.tf      # Cloudflare DNS & load balancer
│   └── terraform.tfvars.example
│
├── k3s/                    # K3s installation scripts
│   ├── install-master.sh  # Master node setup
│   └── install-worker.sh  # Worker node setup
│
├── kubernetes/             # Kubernetes manifests & Helm charts
│   ├── platform-nats/     # Platform NATS cluster (HA)
│   ├── tenant-nats/       # Tenant NATS instances
│   ├── postgresql/        # PostgreSQL database
│   ├── minio/             # MinIO object storage
│   ├── monitoring/        # Prometheus, Grafana, Loki
│   └── ingress/           # Ingress rules
│
├── cloudflare/             # Cloudflare-specific configs
├── scripts/                # Automation scripts
│   └── provision-cluster.sh  # Main provisioning script
│
└── docs/                   # Documentation
    └── DEPLOYMENT_GUIDE.md  # Complete deployment guide
```

---

## Deployment Modes

### 1. Fully Automated (Recommended)

Single command deploys everything:

```bash
./scripts/provision-cluster.sh --master 1.2.3.4 --workers 1.2.3.5,1.2.3.6
```

### 2. Semi-Automated

Deploy cluster, then services separately:

```bash
# 1. Deploy K3s cluster
./scripts/provision-cluster.sh -m <IP> -w <IPs>

# 2. Deploy VRSky services
cd kubernetes
./deploy-vrsky.sh
```

### 3. Manual Step-by-Step

See `docs/DEPLOYMENT_GUIDE.md` for detailed instructions.

---

## Key Features

### Distributed Storage (Longhorn)

- **3-way replication** across all nodes
- **Automatic failover** if node goes down
- **Snapshot support** for backups
- **Web UI** for management

```bash
# Access Longhorn UI
kubectl port-forward -n longhorn-system svc/longhorn-frontend 8080:80
# Open: http://localhost:8080
```

### Firewall Configuration

Minimal ports open for security:

| Port        | Purpose          | Access                  |
| ----------- | ---------------- | ----------------------- |
| 22          | SSH              | Public                  |
| 80/443      | HTTP/HTTPS       | Public (via Cloudflare) |
| 6443        | K3s API          | Restricted              |
| 4222        | NATS Client      | Restricted              |
| 8222        | NATS Monitoring  | Restricted              |
| 9500-9504   | Longhorn Storage | Internal only           |
| 30000-32767 | NodePort         | Public (selective)      |

All firewall rules configured via `ufw` during installation.

### High Availability

- **3-node cluster**: Survives 1 node failure
- **Longhorn storage**: Data replicated across 3 nodes
- **Platform NATS**: 3-pod HA deployment
- **Automatic pod rescheduling**: Kubernetes handles failures

---

## Costs

### Monthly Recurring (Norway)

| Item               | Cost (NOK) | Cost (USD) | Notes            |
| ------------------ | ---------- | ---------- | ---------------- |
| 3× GP316 VPS       | 882        | ~$83       | Regular price    |
| **First 3 months** | **441**    | **~$42**   | **50% discount** |
| Cloudflare Free    | 0          | $0         | DNS + CDN        |
| All software       | 0          | $0         | Open-source      |

### One-Time Costs

- None (unless purchasing domain)

### Scaling Costs

**50 tenants**: Add 3 more VPS (+882 NOK/mo) OR upgrade to dedicated server (2819 NOK/mo)  
**100+ tenants**: Dedicated server(s) (2819-5963 NOK/mo)

---

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                  Cloudflare (Global CDN)                │
│            DNS Load Balancer + SSL Termination          │
└────────────────────┬────────────────────────────────────┘
                     │
                     ▼
    ┌────────────────────────────────────────────┐
    │     Nginx Ingress Controller (K3s)         │
    └────────────────────────────────────────────┘
                     │
         ┌───────────┼───────────┐
         ▼           ▼           ▼
    ┌────────┐  ┌────────┐  ┌────────┐
    │ Node 1 │  │ Node 2 │  │ Node 3 │
    │ Master │  │ Worker │  │ Worker │
    │ Oslo   │  │ Oslo   │  │ Oslo   │
    └────────┘  └────────┘  └────────┘
         │           │           │
         └───────────┴───────────┘
                     │
            Longhorn Storage Network
         (Distributed, 3-way replicated)
```

---

## Monitoring

### Included Monitoring

- **Longhorn metrics**: Storage health, volume usage
- **K3s metrics**: Pod, node, deployment metrics
- **Node metrics**: CPU, RAM, disk, network

### Optional (Install Separately)

```bash
# Prometheus + Grafana stack
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm install kube-prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace

# Access Grafana
kubectl port-forward -n monitoring svc/kube-prometheus-grafana 3000:80
# Open: http://localhost:3000 (admin/prom-operator)
```

---

## Networking

### Internal (Pod-to-Pod)

- **CNI**: Flannel (default K3s CNI)
- **Network**: 10.42.0.0/16 (pod network)
- **Service Network**: 10.43.0.0/16

### External Access

1. **NodePort** (30000-32767): Direct access to services
2. **LoadBalancer** (via Cloudflare): Distributed across nodes
3. **Ingress** (Nginx): HTTP/HTTPS routing with SSL

---

## Security

### Data Sovereignty

- ✅ All data stored in Norway (Oslo datacenter)
- ✅ Norwegian jurisdiction (strong privacy laws)
- ✅ GDPR compliant by design
- ✅ ISO 27001 certified datacenter

### Network Security

- ✅ Firewall enabled on all nodes (ufw)
- ✅ Minimal ports open (least privilege)
- ✅ DDoS protection via Cloudflare
- ✅ SSL/TLS termination at Cloudflare edge

### Cluster Security

- ✅ RBAC enabled (Kubernetes default)
- ✅ Pod security policies (configurable)
- ✅ Network policies (configurable)
- ✅ Secrets encrypted at rest (optional)

---

## Troubleshooting

### Quick Diagnostics

```bash
# Check all nodes
kubectl get nodes

# Check all pods
kubectl get pods -A

# Check Longhorn health
kubectl get pods -n longhorn-system

# Check firewall on node
ssh root@<NODE_IP> "ufw status"

# View K3s logs on node
ssh root@<NODE_IP> "journalctl -u k3s -f"
```

### Common Issues

**Issue**: Nodes not joining cluster  
**Fix**: Check firewall allows port 6443

**Issue**: Longhorn pods CrashLooping  
**Fix**: Verify open-iscsi is running: `systemctl status iscsid`

**Issue**: Can't access from local machine  
**Fix**: Check kubeconfig has correct master IP

See `docs/DEPLOYMENT_GUIDE.md` for complete troubleshooting guide.

---

## Maintenance

### Backup Cluster State

```bash
# Backup etcd (K3s embedded)
ssh root@<MASTER_IP> "k3s etcd-snapshot save"

# Download snapshot
scp root@<MASTER_IP>:/var/lib/rancher/k3s/server/db/snapshots/* ./backups/
```

### Update K3s Version

```bash
# Update master
ssh root@<MASTER_IP> "curl -sfL https://get.k3s.io | sh -"

# Update workers (one at a time)
ssh root@<WORKER_IP> "curl -sfL https://get.k3s.io | sh -"
```

### Add New Node

```bash
# Get token from master
TOKEN=$(ssh root@<MASTER_IP> "cat /var/lib/rancher/k3s/server/node-token")

# Install on new node
./scripts/provision-cluster.sh --add-worker <NEW_IP> --token $TOKEN
```

---

## Support

**Documentation**: `docs/DEPLOYMENT_GUIDE.md`  
**GitHub Issues**: https://github.com/ValueRetail/vrsky/issues  
**ServeTheWorld Support**: support@servetheworld.net, +47 22 22 28 80

---

## License

See root LICENSE file.

---

**Ready to deploy?** Start with `./scripts/provision-cluster.sh --help`
