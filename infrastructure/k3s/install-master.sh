#!/bin/bash
# VRSky K3s Master Node Installation Script
# This script installs K3s master node with embedded etcd and prepares for Longhorn storage

set -euo pipefail

# Configuration
K3S_VERSION="${K3S_VERSION:-v1.35.0+k3s1}"
CLUSTER_INIT="${CLUSTER_INIT:-true}"
NODE_IP="${NODE_IP:-$(hostname -I | awk '{print $1}')}"
CLUSTER_NAME="${CLUSTER_NAME:-vrsky-prod}"

# Colors for output
RED='\033[0:31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log() {
	echo -e "${GREEN}[$(date +'%Y-%m-%d %H:%M:%S')]${NC} $*"
}

error() {
	echo -e "${RED}[ERROR]${NC} $*" >&2
}

warn() {
	echo -e "${YELLOW}[WARN]${NC} $*"
}

# Pre-flight checks
log "Starting VRSky K3s master node installation..."
log "Node IP: $NODE_IP"
log "K3s Version: $K3S_VERSION"

# Check if running as root
if [[ $EUID -ne 0 ]]; then
	error "This script must be run as root"
	exit 1
fi

# Update system packages
log "Updating system packages..."
apt-get update -qq
apt-get upgrade -y -qq

# Install required packages for Longhorn
log "Installing prerequisites for Longhorn distributed storage..."
apt-get install -y -qq \
	open-iscsi \
	nfs-common \
	curl \
	jq \
	util-linux

# Enable and start iscsid (required for Longhorn)
systemctl enable iscsid
systemctl start iscsid

# Configure firewall (ufw)
log "Configuring firewall rules..."
if command -v ufw &>/dev/null; then
	# Allow SSH
	ufw allow 22/tcp

	# Allow HTTP/HTTPS
	ufw allow 80/tcp
	ufw allow 443/tcp

	# Allow K3s API server
	ufw allow 6443/tcp

	# Allow K3s metrics server
	ufw allow 10250/tcp

	# Allow Longhorn storage network
	ufw allow 9500:9504/tcp

	# Allow NATS ports
	ufw allow 4222/tcp # NATS client
	ufw allow 6222/tcp # NATS cluster
	ufw allow 8222/tcp # NATS monitoring

	# Allow flannel VXLAN
	ufw allow 8472/udp

	# Allow NodePort range
	ufw allow 30000:32767/tcp

	# Enable firewall
	ufw --force enable

	log "Firewall configured and enabled"
else
	warn "ufw not found, firewall configuration skipped"
fi

# Disable swap (required for Kubernetes)
log "Disabling swap..."
swapoff -a
sed -i '/ swap / s/^/#/' /etc/fstab

# Configure system for K3s
log "Configuring system parameters..."
cat >/etc/sysctl.d/99-kubernetes.conf <<EOF
net.bridge.bridge-nf-call-iptables = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward = 1
vm.overcommit_memory = 1
vm.panic_on_oom = 0
kernel.panic = 10
kernel.panic_on_oops = 1
kernel.keys.root_maxbytes = 25000000
EOF

sysctl --system

# Install K3s
log "Installing K3s master node..."

curl -sfL https://get.k3s.io |
	K3S_VERSION=$K3S_VERSION \
		INSTALL_K3S_EXEC="server \
        --cluster-init \
        --node-ip=$NODE_IP \
        --node-external-ip=$NODE_IP \
        --tls-san=$NODE_IP \
        --disable=traefik \
        --disable=servicelb \
        --write-kubeconfig-mode=644 \
        --kube-apiserver-arg=feature-gates=MixedProtocolLBService=true" \
		sh -

# Wait for K3s to be ready
log "Waiting for K3s to be ready..."
timeout=300
elapsed=0
while ! kubectl get nodes &>/dev/null; do
	if [ $elapsed -ge $timeout ]; then
		error "Timeout waiting for K3s to be ready"
		exit 1
	fi
	sleep 5
	elapsed=$((elapsed + 5))
done

log "K3s master node is ready!"

# Get node token for workers
NODE_TOKEN=$(cat /var/lib/rancher/k3s/server/node-token)
log "Node token for workers: $NODE_TOKEN"

# Save token to file
echo "$NODE_TOKEN" >/root/k3s-node-token
chmod 600 /root/k3s-node-token

# Install Helm
log "Installing Helm..."
curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash

# Add Helm repositories
log "Adding Helm repositories..."
helm repo add longhorn https://charts.longhorn.io
helm repo add nats https://nats-io.github.io/k8s/helm/charts/
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo add grafana https://grafana.github.io/helm-charts
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
helm repo update

log "Waiting for node to be ready before installing Longhorn..."
kubectl wait --for=condition=Ready node --all --timeout=300s

# Install Longhorn (distributed storage)
log "Installing Longhorn distributed storage..."
kubectl create namespace longhorn-system || true

helm upgrade --install longhorn longhorn/longhorn \
	--namespace longhorn-system \
	--set defaultSettings.defaultReplicaCount=3 \
	--set defaultSettings.defaultDataPath="/var/lib/longhorn" \
	--set persistence.defaultClass=true \
	--set persistence.defaultClassReplicaCount=3 \
	--set ingress.enabled=false \
	--wait

# Install Nginx Ingress Controller
log "Installing Nginx Ingress Controller..."
kubectl create namespace ingress-nginx || true

helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
	--namespace ingress-nginx \
	--set controller.service.type=NodePort \
	--set controller.service.nodePorts.http=30080 \
	--set controller.service.nodePorts.https=30443 \
	--wait

# Create kubeconfig for external access
log "Creating kubeconfig for external access..."
cp /etc/rancher/k3s/k3s.yaml /root/kubeconfig
sed -i "s/127.0.0.1/$NODE_IP/g" /root/kubeconfig
chmod 600 /root/kubeconfig

log "====================================="
log "K3s Master Node Installation Complete!"
log "====================================="
log ""
log "Node IP: $NODE_IP"
log "Node Token: $NODE_TOKEN"
log "Node Token saved to: /root/k3s-node-token"
log "Kubeconfig saved to: /root/kubeconfig"
log ""
log "To add worker nodes, run on each worker:"
log "  export K3S_URL=https://$NODE_IP:6443"
log "  export K3S_TOKEN=$NODE_TOKEN"
log "  curl -sfL https://get.k3s.io | sh -"
log ""
log "To access the cluster from your local machine:"
log "  scp root@$NODE_IP:/root/kubeconfig ~/.kube/config-vrsky"
log "  export KUBECONFIG=~/.kube/config-vrsky"
log "  kubectl get nodes"
log ""
log "Longhorn UI (after port-forward):"
log "  kubectl port-forward -n longhorn-system svc/longhorn-frontend 8080:80"
log "  Open: http://localhost:8080"
log ""
log "Next steps:"
log "  1. Install worker nodes using the token above"
log "  2. Verify all nodes are ready: kubectl get nodes"
log "  3. Deploy VRSky platform using Helm charts"
