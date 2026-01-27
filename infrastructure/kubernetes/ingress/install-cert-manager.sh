#!/bin/bash
# install-cert-manager.sh
# Installs cert-manager for automatic TLS certificate management

set -e

echo "Installing cert-manager..."

# Check if Helm is installed
if ! command -v helm &>/dev/null; then
	echo "Error: Helm is not installed"
	exit 1
fi

# Add Jetstack Helm repository
echo "Adding Jetstack Helm repository..."
helm repo add jetstack https://charts.jetstack.io
helm repo update

# Install cert-manager
echo "Installing cert-manager CRDs and controller..."
helm upgrade --install cert-manager jetstack/cert-manager \
	--namespace cert-manager \
	--create-namespace \
	--version v1.13.3 \
	--set installCRDs=true \
	--wait

echo "cert-manager installed successfully!"

# Wait for cert-manager to be ready
echo "Waiting for cert-manager to be ready..."
kubectl wait --for=condition=ready pod \
	-l app.kubernetes.io/instance=cert-manager \
	-n cert-manager \
	--timeout=120s

echo "cert-manager is ready!"

exit 0
