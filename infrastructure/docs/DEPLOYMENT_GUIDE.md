# VRSky Infrastructure Deployment Guide

**Target**: ServeTheWorld VPS (Norway)  
**Deployment**: K3s Kubernetes Cluster with Longhorn Storage  
**Timeline**: Ready for POC (April 2026)

---

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Quick Start](#quick-start)
3. [Step-by-Step Deployment](#step-by-step-deployment)
4. [Post-Deployment Configuration](#post-deployment-configuration)
5. [Troubleshooting](#troubleshooting)
6. [Cost Breakdown](#cost-breakdown)

---

## Prerequisites

### 1. ServeTheWorld VPS Instances

Order **3× GP316 VPS** from ServeTheWorld:

- Go to: https://my.servetheworld.net/order/vps-gp3
- Select: **GP316 plan**
  - 8 vCPU
  - 16GB RAM
  - 480GB NVMe storage
  - 1 Gbit/s network
  - 25TB traffic
- Quantity: **3**
- OS: **Ubuntu 22.04 LTS** or **AlmaLinux 9**
- Location: **Oslo, Norway**
- Apply **50% discount code** if available (first 3 months)

**Expected Cost**:

- First 3 months: 294 NOK/mo × 3 = **882 NOK/mo** (with 50% discount: **441 NOK/mo** ~$42/month)
- After discount: **882 NOK/mo** (~$83/month)

**After ordering**, note down:

- ✅ IP addresses of all 3 VPS instances
- ✅ Root password or upload your SSH public key
- ✅ VPS names (e.g., vps1, vps2, vps3)

### 2. Local Machine Requirements

Install on your local machine:

```bash
# macOS
brew install terraform kubectl helm

# Linux (Ubuntu/Debian)
sudo apt-get update
sudo apt-get install -y terraform kubectl

# Install Helm
curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
```

### 3. Cloudflare Account (Optional but Recommended)

For DNS load balancing and SSL:

1. Create free Cloudflare account: https://dash.cloudflare.com/sign-up
2. Add your domain to Cloudflare
3. Get API token:
   - Go to: https://dash.cloudflare.com/profile/api-tokens
   - Create Token → "Edit zone DNS" template
   - Save the token
4. Get Zone ID:
   - Dashboard → Select your domain → Overview → Zone ID (right sidebar)

### 4. SSH Key Setup

Generate SSH key if you don't have one:

```bash
ssh-keygen -t rsa -b 4096 -f ~/.ssh/vrsky_rsa -C "vrsky@yourdomain.com"
```

Upload public key to ServeTheWorld VPS during order or add to VPS after creation:

```bash
ssh-copy-id -i ~/.ssh/vrsky_rsa.pub root@<VPS_IP>
```

---

## Quick Start

**For impatient users** - deploy everything in ~30 minutes:

```bash
# 1. Clone the repository
git clone https://github.com/ValueRetail/vrsky.git
cd vrsky/infrastructure

# 2. Run automated provisioning
./scripts/provision-cluster.sh \
  --master <MASTER_IP> \
  --workers <WORKER1_IP>,<WORKER2_IP> \
  --ssh-key ~/.ssh/vrsky_rsa

# 3. Verify cluster
export KUBECONFIG=$(pwd)/kubeconfig
kubectl get nodes

# 4. Deploy VRSky platform (coming in next step)
cd kubernetes
./deploy-vrsky.sh
```

**Done!** Your VRSky cluster is running.

---

## Step-by-Step Deployment

### Step 1: Prepare VPS IP Addresses

After ordering 3 VPS instances from ServeTheWorld, you'll receive:

```
VPS 1 (Master): 1.2.3.4
VPS 2 (Worker 1): 1.2.3.5
VPS 3 (Worker 2): 1.2.3.6
```

**Test SSH access**:

```bash
ssh -i ~/.ssh/vrsky_rsa root@1.2.3.4
ssh -i ~/.ssh/vrsky_rsa root@1.2.3.5
ssh -i ~/.ssh/vrsky_rsa root@1.2.3.6
```

### Step 2: Install K3s on Master Node

**Option A: Automated (Recommended)**

```bash
cd vrsky/infrastructure
./scripts/provision-cluster.sh \
  --master 1.2.3.4 \
  --workers 1.2.3.5,1.2.3.6 \
  --ssh-key ~/.ssh/vrsky_rsa
```

**Option B: Manual**

```bash
# Copy script to master node
scp -i ~/.ssh/vrsky_rsa k3s/install-master.sh root@1.2.3.4:/tmp/

# SSH to master and run
ssh -i ~/.ssh/vrsky_rsa root@1.2.3.4
bash /tmp/install-master.sh

# Save the node token shown at the end
```

### Step 3: Install K3s on Worker Nodes

The automated script does this for you, or manually:

```bash
# Get node token from master
ssh root@1.2.3.4 "cat /var/lib/rancher/k3s/server/node-token"

# Copy to workers
scp -i ~/.ssh/vrsky_rsa k3s/install-worker.sh root@1.2.3.5:/tmp/
scp -i ~/.ssh/vrsky_rsa k3s/install-worker.sh root@1.2.3.6:/tmp/

# Install on each worker
ssh root@1.2.3.5 "export K3S_URL=https://1.2.3.4:6443 && export K3S_TOKEN=<TOKEN> && bash /tmp/install-worker.sh"
ssh root@1.2.3.6 "export K3S_URL=https://1.2.3.4:6443 && export K3S_TOKEN=<TOKEN> && bash /tmp/install-worker.sh"
```

### Step 4: Download Kubeconfig

```bash
# Download from master node
scp -i ~/.ssh/vrsky_rsa root@1.2.3.4:/root/kubeconfig ./kubeconfig

# Set as active kubeconfig
export KUBECONFIG=$(pwd)/kubeconfig

# Verify cluster
kubectl get nodes
```

**Expected output**:

```
NAME       STATUS   ROLES                       AGE   VERSION
vps1       Ready    control-plane,master        5m    v1.35.0+k3s1
vps2       Ready    <none>                      3m    v1.35.0+k3s1
vps3       Ready    <none>                      3m    v1.35.0+k3s1
```

### Step 5: Verify Longhorn Storage

```bash
# Check Longhorn pods
kubectl get pods -n longhorn-system

# All pods should be Running
# This may take 2-3 minutes
```

**Check storage classes**:

```bash
kubectl get storageclass
```

**Expected output**:

```
NAME                 PROVISIONER          RECLAIMPOLICY   VOLUMEBINDINGMODE
longhorn (default)   driver.longhorn.io   Delete          Immediate
```

### Step 6: Configure Cloudflare DNS (Optional)

```bash
cd terraform

# Copy example config
cp terraform.tfvars.example terraform.tfvars

# Edit with your values
nano terraform.tfvars
```

**Update these values**:

```hcl
master_node_ip = "1.2.3.4"
worker_node_ips = ["1.2.3.5", "1.2.3.6"]
cloudflare_api_token = "your-token-here"
cloudflare_zone_id = "your-zone-id"
domain_name = "vrsky.yourdomain.com"
```

**Apply Terraform**:

```bash
terraform init
terraform plan
terraform apply
```

### Step 7: Deploy VRSky Platform

(Helm charts coming in next section)

```bash
cd ../kubernetes
./deploy-vrsky.sh
```

---

## Post-Deployment Configuration

### Access Longhorn UI

```bash
kubectl port-forward -n longhorn-system svc/longhorn-frontend 8080:80
```

Open: http://localhost:8080

### Access Kubernetes Dashboard (Optional)

```bash
helm repo add kubernetes-dashboard https://kubernetes.github.io/dashboard/
helm install kubernetes-dashboard kubernetes-dashboard/kubernetes-dashboard \
  --namespace kube-dashboard --create-namespace

kubectl port-forward -n kube-dashboard svc/kubernetes-dashboard-kong-proxy 8443:443
```

Open: https://localhost:8443

### Firewall Verification

Check firewall status on each node:

```bash
ssh root@<NODE_IP> "ufw status numbered"
```

**Required open ports**:

- 22 (SSH)
- 80 (HTTP)
- 443 (HTTPS)
- 6443 (K3s API)
- 4222, 6222, 8222 (NATS)
- 9500-9504 (Longhorn)
- 30000-32767 (NodePort range)

---

## Troubleshooting

### Nodes Not Ready

```bash
# Check node status
kubectl describe node <NODE_NAME>

# Check kubelet logs on problematic node
ssh root@<NODE_IP> "journalctl -u k3s -f"
```

### Longhorn Not Starting

```bash
# Check if open-iscsi is running
ssh root@<NODE_IP> "systemctl status iscsid"

# Restart if needed
ssh root@<NODE_IP> "systemctl restart iscsid"
```

### Firewall Blocking Traffic

```bash
# Temporarily disable for testing (NOT for production!)
ssh root@<NODE_IP> "ufw disable"

# Re-run firewall configuration
ssh root@<NODE_IP> "bash /tmp/install-master.sh"  # or install-worker.sh
```

### Can't Access Cluster from Local Machine

```bash
# Verify kubeconfig server URL
cat kubeconfig | grep server

# Should be: https://<MASTER_IP>:6443

# Test connectivity
curl -k https://<MASTER_IP>:6443
```

### SSH Connection Issues

```bash
# Test SSH with verbose output
ssh -v -i ~/.ssh/vrsky_rsa root@<NODE_IP>

# If permission denied, check key permissions
chmod 600 ~/.ssh/vrsky_rsa
```

---

## Cost Breakdown

### Monthly Recurring Costs (Norway Hosting)

**Infrastructure** (3× ServeTheWorld GP316 VPS):

| Item                         | Quantity | Unit Price | Total (NOK) | Total (USD) |
| ---------------------------- | -------- | ---------- | ----------- | ----------- |
| GP316 VPS                    | 3        | 294 NOK/mo | 882 NOK     | ~$83/mo     |
| **First 3 months (50% OFF)** | 3        | 147 NOK/mo | **441 NOK** | **~$42/mo** |

**Software** (All Open-Source, $0):

- K3s (Kubernetes): Free
- Longhorn (Storage): Free
- Nginx Ingress: Free
- Prometheus/Grafana: Free
- NATS: Free
- PostgreSQL: Free (self-hosted)
- MinIO: Free (self-hosted)

**Optional Add-Ons**:

- Cloudflare Free: $0/mo (included)
- Managed PostgreSQL (if preferred): ~200 NOK/mo (~$19/mo)

**Total POC Cost**:

- **First 3 months**: 441 NOK/mo (~$42/month) ✅
- **After discount**: 882 NOK/mo (~$83/month) ✅

**Production Scaling** (50 tenants):

- Add 3 more GP316 VPS: +882 NOK/mo
- Or upgrade to dedicated server: 2819 NOK/mo (~$265/mo) for 50-100 tenants

---

## What's Included in This Deployment

✅ 3-node K3s cluster (HA capable)  
✅ Longhorn distributed storage (3-way replication)  
✅ Nginx Ingress Controller  
✅ Firewall configured (ufw)  
✅ SSL/TLS ready (Let's Encrypt via cert-manager, install separately)  
✅ Monitoring ready (Prometheus stack installable)  
✅ Norwegian datacenter (Oslo, ISO 27001)  
✅ 99.99% SLA  
✅ GDPR compliant  
✅ Green energy (90-95% hydropower)

---

## Next Steps

1. ✅ **Cluster deployed** - You have a working K3s cluster
2. 🔄 **Deploy VRSky platform** - Install Platform NATS, services, etc.
3. 🔄 **Configure monitoring** - Prometheus, Grafana, Loki
4. 🔄 **Set up CI/CD** - GitHub Actions for automated deployment
5. 🔄 **Create first tenant** - Test end-to-end integration

---

## Support & Resources

**Documentation**:

- K3s: https://docs.k3s.io
- Longhorn: https://longhorn.io/docs/
- ServeTheWorld: https://stw.no

**Community**:

- VRSky GitHub: https://github.com/ValueRetail/vrsky
- Issues: https://github.com/ValueRetail/vrsky/issues

**Commercial Support**:

- ServeTheWorld: support@servetheworld.net (Norwegian support)
- Phone: +47 22 22 28 80

---

**Last Updated**: January 27, 2026  
**Version**: 1.0.0  
**Status**: Ready for POC Deployment
