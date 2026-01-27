#!/bin/bash
# VRSky Local Development Cluster Setup (k3d)
# This script creates a local Kubernetes cluster using k3d that mirrors the production K3s environment.

set -euo pipefail

# Configuration
CLUSTER_NAME="vrsky-dev"
K3S_VERSION="v1.35.0-k3s1"
IMAGE="rancher/k3s:${K3S_VERSION}"

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

# Check prerequisites and install if missing
check_prereq() {
	if ! command -v docker &>/dev/null; then
		error "Docker is not installed. Please install Docker first: https://docs.docker.com/get-docker/"
		exit 1
	fi

	if ! command -v k3d &>/dev/null; then
		warn "k3d not found. Attempting to install k3d..."
		curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash
		if ! command -v k3d &>/dev/null; then
			error "k3d installation failed. Please install it manually: https://k3d.io/#installation"
			exit 1
		fi
		log "✓ k3d installed successfully"
	fi

	if ! command -v kubectl &>/dev/null; then
		warn "kubectl not found. Attempting to install kubectl..."
		local OS_TYPE=$(uname -s | tr '[:upper:]' '[:lower:]')
		local ARCH_TYPE=$(uname -m)
		if [ "$ARCH_TYPE" == "x86_64" ]; then ARCH_TYPE="amd64"; fi
		if [ "$ARCH_TYPE" == "aarch64" ]; then ARCH_TYPE="arm64"; fi

		curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/${OS_TYPE}/${ARCH_TYPE}/kubectl"
		chmod +x ./kubectl
		sudo mv ./kubectl /usr/local/bin/kubectl
		if ! command -v kubectl &>/dev/null; then
			error "kubectl installation failed. Please install it manually: https://kubernetes.io/docs/tasks/tools/"
			exit 1
		fi
		log "✓ kubectl installed successfully"
	fi

	if ! command -v helm &>/dev/null; then
		if [ -f "./bin/helm" ]; then
			log "✓ helm found in ./bin"
		else
			warn "helm not found. Attempting to install helm locally..."
			mkdir -p bin
			curl -L https://get.helm.sh/helm-v3.20.0-linux-amd64.tar.gz | tar -xz
			mv linux-amd64/helm bin/helm
			rm -rf linux-amd64
			log "✓ helm installed locally to ./bin"
		fi
	fi
}

# Install Longhorn in the cluster
install_longhorn() {
	log "Skipping Longhorn for local development due to iSCSI/musl compatibility issues."
	log "Using K3s local-path-provisioner instead."
}

log "====================================="
log "VRSky Local Cluster Setup (k3d)"
log "====================================="

check_prereq

# Check if cluster already exists
if k3d cluster list | grep -q "^${CLUSTER_NAME}"; then
	warn "Cluster '${CLUSTER_NAME}' already exists."
	read -p "Do you want to recreate it? (y/N) " -n 1 -r
	echo
	if [[ $REPLY =~ ^[Yy]$ ]]; then
		info "Deleting existing cluster..."
		k3d cluster delete "${CLUSTER_NAME}"
	else
		info "Using existing cluster."
		exit 0
	fi
fi

log "Creating k3d cluster '${CLUSTER_NAME}' using config..."
k3d cluster create --config infrastructure/kubernetes/k3d-config.yaml --wait

log "✓ Cluster created successfully!"

# Update kubeconfig
info "Updating kubeconfig..."
k3d kubeconfig merge "${CLUSTER_NAME}" --kubeconfig-switch-context

log "Verifying nodes..."
kubectl get nodes

install_longhorn

log "====================================="
log "Setup Complete! 🎉"
log "====================================="
info "Cluster: ${CLUSTER_NAME}"
info "K3s Version: ${K3S_VERSION}"
log ""
info "Next Steps:"
info "  1. Deploy VRSky core components:"
info "     cd infrastructure/kubernetes"
info "     ./deploy-vrsky-platform.sh"
log ""
info "Happy coding! 🚀"
