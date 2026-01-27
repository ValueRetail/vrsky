#!/bin/bash
# VRSky Local Development Cluster Teardown (k3d)
# This script deletes the local k3d cluster.

set -euo pipefail

CLUSTER_NAME="vrsky-dev"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

log() { echo -e "${GREEN}[$(date +'%H:%M:%S')]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

log "====================================="
log "VRSky Local Cluster Teardown"
log "====================================="

if ! command -v k3d &>/dev/null; then
	error "k3d is not installed."
	exit 1
fi

if ! k3d cluster list | grep -q "^${CLUSTER_NAME}"; then
	log "Cluster '${CLUSTER_NAME}' does not exist. Nothing to do."
	exit 0
fi

read -p "Are you sure you want to delete the cluster '${CLUSTER_NAME}'? All local data will be lost. (y/N) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
	log "Deleting cluster '${CLUSTER_NAME}'..."
	k3d cluster delete "${CLUSTER_NAME}"
	log "✓ Cluster deleted successfully!"
else
	log "Operation cancelled."
fi
