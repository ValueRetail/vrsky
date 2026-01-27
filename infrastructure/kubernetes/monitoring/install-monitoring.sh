#!/bin/bash
# install-monitoring.sh
# Installs Prometheus and Grafana using Helm

set -e

echo "Installing VRSky Monitoring Stack (Prometheus + Grafana)"

# Check if Helm is installed
if ! command -v helm &>/dev/null; then
	echo "Error: Helm is not installed. Install with:"
	echo "  curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash"
	exit 1
fi

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Create namespace
echo "Creating namespace..."
kubectl apply -f "$SCRIPT_DIR/namespace.yaml"

# Add Helm repositories
echo "Adding Helm repositories..."
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update

# Install Prometheus (kube-prometheus-stack)
echo "Installing Prometheus..."
helm upgrade --install prometheus \
	prometheus-community/kube-prometheus-stack \
	--namespace vrsky-monitoring \
	--values "$SCRIPT_DIR/prometheus-values.yaml" \
	--wait \
	--timeout 10m

echo "Prometheus installed successfully!"

# Install Grafana
echo "Installing Grafana..."
helm upgrade --install grafana \
	grafana/grafana \
	--namespace vrsky-monitoring \
	--values "$SCRIPT_DIR/grafana-values.yaml" \
	--wait \
	--timeout 5m

echo "Grafana installed successfully!"

# Get Grafana admin password
echo ""
echo "============================================"
echo "Monitoring Stack Installation Complete!"
echo "============================================"
echo ""
echo "Grafana Admin Credentials:"
echo "  Username: admin"
echo "  Password: changeme-grafana-password"
echo ""
echo "Access Grafana:"
echo "  kubectl port-forward -n vrsky-monitoring svc/grafana 3000:80"
echo "  Open: http://localhost:3000"
echo ""
echo "Access Prometheus:"
echo "  kubectl port-forward -n vrsky-monitoring svc/prometheus-kube-prometheus-prometheus 9090:9090"
echo "  Open: http://localhost:9090"
echo ""
echo "⚠️  IMPORTANT: Change Grafana password before production deployment!"
echo ""

exit 0
