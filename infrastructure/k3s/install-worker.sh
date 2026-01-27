#!/bin/bash
# VRSky K3s Worker Node Installation Script
# This script installs K3s worker node and joins it to the cluster

set -euo pipefail

# Configuration
K3S_VERSION="${K3S_VERSION:-v1.35.0+k3s1}"
K3S_URL="${K3S_URL:-}"
K3S_TOKEN="${K3S_TOKEN:-}"
NODE_IP="${NODE_IP:-$(hostname -I | awk '{print $1}')}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

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
log "Starting VRSky K3s worker node installation..."
log "Node IP: $NODE_IP"
log "K3s Version: $K3S_VERSION"

if [[ $EUID -ne 0 ]]; then
	error "This script must be run as root"
	exit 1
fi

if [ -z "$K3S_URL" ]; then
	error "K3S_URL environment variable is required"
	error "Usage: export K3S_URL=https://<master-ip>:6443 && export K3S_TOKEN=<token> && ./install-worker.sh"
	exit 1
fi

if [ -z "$K3S_TOKEN" ]; then
	error "K3S_TOKEN environment variable is required"
	error "Usage: export K3S_URL=https://<master-ip>:6443 && export K3S_TOKEN=<token> && ./install-worker.sh"
	exit 1
fi

log "Master URL: $K3S_URL"

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
	util-linux

# Enable and start iscsid
systemctl enable iscsid
systemctl start iscsid

# Configure firewall
log "Configuring firewall rules..."
if command -v ufw &>/dev/null; then
	ufw allow 22/tcp
	ufw allow 80/tcp
	ufw allow 443/tcp
	ufw allow 10250/tcp
	ufw allow 9500:9504/tcp
	ufw allow 4222/tcp
	ufw allow 6222/tcp
	ufw allow 8222/tcp
	ufw allow 8472/udp
	ufw allow 30000:32767/tcp
	ufw --force enable
	log "Firewall configured and enabled"
else
	warn "ufw not found, firewall configuration skipped"
fi

# Disable swap
log "Disabling swap..."
swapoff -a
sed -i '/ swap / s/^/#/' /etc/fstab

# Configure system
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

# Install K3s agent
log "Installing K3s worker node..."

curl -sfL https://get.k3s.io |
	K3S_VERSION=$K3S_VERSION \
		K3S_URL=$K3S_URL \
		K3S_TOKEN=$K3S_TOKEN \
		INSTALL_K3S_EXEC="agent \
        --node-ip=$NODE_IP \
        --node-external-ip=$NODE_IP" \
		sh -

log "====================================="
log "K3s Worker Node Installation Complete!"
log "====================================="
log ""
log "Node IP: $NODE_IP"
log "Master URL: $K3S_URL"
log ""
log "Verify node joined cluster from master:"
log "  kubectl get nodes"
log ""
log "This worker node is now part of the VRSky cluster!"
