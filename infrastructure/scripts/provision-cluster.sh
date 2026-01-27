#!/bin/bash
# VRSky Cluster Provisioning Script
# This script automates the deployment of K3s cluster on ServeTheWorld VPS instances

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log() { echo -e "${GREEN}[$(date +'%H:%M:%S')]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
info() { echo -e "${BLUE}[INFO]${NC} $*"; }

usage() {
	cat <<EOF
Usage: $0 [OPTIONS]

Provision VRSky K3s cluster on ServeTheWorld VPS instances.

OPTIONS:
    -m, --master IP         Master node IP address
    -w, --workers IP1,IP2   Worker node IP addresses (comma-separated)
    -k, --ssh-key PATH      Path to SSH private key (default: ~/.ssh/id_rsa)
    -u, --user USER         SSH user (default: root)
    -h, --help              Show this help message

EXAMPLES:
    # Provision cluster with 1 master and 2 workers
    $0 --master 1.2.3.4 --workers 1.2.3.5,1.2.3.6

    # Use custom SSH key
    $0 -m 1.2.3.4 -w 1.2.3.5,1.2.3.6 -k ~/.ssh/vrsky_rsa

PREREQUISITES:
    1. Order VPS instances from ServeTheWorld (3× GP316 recommended)
    2. Ensure SSH access to all nodes with root or sudo user
    3. Have SSH key ready for authentication
    4. Note down all VPS IP addresses

EOF
	exit 1
}

# Parse arguments
MASTER_IP=""
WORKER_IPS=""
SSH_KEY="$HOME/.ssh/id_rsa"
SSH_USER="root"

while [[ $# -gt 0 ]]; do
	case $1 in
	-m | --master)
		MASTER_IP="$2"
		shift 2
		;;
	-w | --workers)
		WORKER_IPS="$2"
		shift 2
		;;
	-k | --ssh-key)
		SSH_KEY="$2"
		shift 2
		;;
	-u | --user)
		SSH_USER="$2"
		shift 2
		;;
	-h | --help)
		usage
		;;
	*)
		error "Unknown option: $1"
		usage
		;;
	esac
done

# Validate inputs
if [ -z "$MASTER_IP" ]; then
	error "Master IP is required"
	usage
fi

if [ -z "$WORKER_IPS" ]; then
	error "Worker IPs are required"
	usage
fi

if [ ! -f "$SSH_KEY" ]; then
	error "SSH key not found: $SSH_KEY"
	exit 1
fi

# Convert worker IPs to array
IFS=',' read -ra WORKERS <<<"$WORKER_IPS"

log "====================================="
log "VRSky Cluster Provisioning"
log "====================================="
info "Master Node: $MASTER_IP"
info "Worker Nodes: ${WORKERS[*]}"
info "SSH User: $SSH_USER"
info "SSH Key: $SSH_KEY"
log ""

# Test SSH connectivity
test_ssh() {
	local ip=$1
	info "Testing SSH connection to $ip..."
	if ssh -i "$SSH_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=10 "$SSH_USER@$ip" "echo 'SSH OK'" &>/dev/null; then
		log "✓ SSH connection to $ip successful"
		return 0
	else
		error "✗ SSH connection to $ip failed"
		return 1
	fi
}

log "Step 1: Testing SSH connectivity to all nodes..."
test_ssh "$MASTER_IP" || exit 1
for worker in "${WORKERS[@]}"; do
	test_ssh "$worker" || exit 1
done
log "✓ All nodes are accessible via SSH"
log ""

# Install master node
log "Step 2: Installing K3s on master node ($MASTER_IP)..."
scp -i "$SSH_KEY" "$ROOT_DIR/k3s/install-master.sh" "$SSH_USER@$MASTER_IP:/tmp/"
ssh -i "$SSH_KEY" "$SSH_USER@$MASTER_IP" "bash /tmp/install-master.sh"
log "✓ Master node installation complete"
log ""

# Get node token from master
log "Step 3: Retrieving node token from master..."
NODE_TOKEN=$(ssh -i "$SSH_KEY" "$SSH_USER@$MASTER_IP" "cat /var/lib/rancher/k3s/server/node-token")
log "✓ Node token retrieved"
log ""

# Install worker nodes
log "Step 4: Installing K3s on worker nodes..."
for i in "${!WORKERS[@]}"; do
	worker="${WORKERS[$i]}"
	log "Installing worker node $((i + 1)): $worker"

	scp -i "$SSH_KEY" "$ROOT_DIR/k3s/install-worker.sh" "$SSH_USER@$worker:/tmp/"

	ssh -i "$SSH_KEY" "$SSH_USER@$worker" \
		"export K3S_URL=https://$MASTER_IP:6443 && \
         export K3S_TOKEN=$NODE_TOKEN && \
         bash /tmp/install-worker.sh"

	log "✓ Worker node $((i + 1)) installation complete"
done
log ""

# Download kubeconfig
log "Step 5: Downloading kubeconfig from master..."
KUBECONFIG_PATH="$ROOT_DIR/kubeconfig"
scp -i "$SSH_KEY" "$SSH_USER@$MASTER_IP:/root/kubeconfig" "$KUBECONFIG_PATH"
chmod 600 "$KUBECONFIG_PATH"
log "✓ Kubeconfig saved to: $KUBECONFIG_PATH"
log ""

# Verify cluster
log "Step 6: Verifying cluster status..."
export KUBECONFIG="$KUBECONFIG_PATH"

if ! command -v kubectl &>/dev/null; then
	warn "kubectl not found locally, skipping verification"
	warn "Install kubectl: https://kubernetes.io/docs/tasks/tools/"
else
	log "Waiting for all nodes to be ready..."
	sleep 10
	kubectl get nodes -o wide
	log ""

	log "Checking Longhorn storage..."
	kubectl get pods -n longhorn-system
	log ""
fi

log "====================================="
log "Cluster Provisioning Complete! 🎉"
log "====================================="
log ""
info "Cluster Details:"
info "  Master:  $MASTER_IP"
info "  Workers: ${WORKERS[*]}"
info "  Kubeconfig: $KUBECONFIG_PATH"
log ""
info "Next Steps:"
info "  1. Export kubeconfig:"
info "     export KUBECONFIG=$KUBECONFIG_PATH"
info ""
info "  2. Verify cluster:"
info "     kubectl get nodes"
info "     kubectl get pods -A"
info ""
info "  3. Deploy VRSky platform:"
info "     cd $ROOT_DIR/kubernetes"
info "     ./deploy-vrsky.sh"
info ""
info "  4. Configure Cloudflare DNS (optional):"
info "     cd $ROOT_DIR/terraform"
info "     terraform init"
info "     terraform apply"
log ""
log "Happy deploying! 🚀"
