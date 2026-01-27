#!/bin/bash
# deploy-vrsky-platform.sh
# Main deployment script for VRSky platform on Kubernetes

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Functions
print_header() {
	echo ""
	echo "==========================================="
	echo "$1"
	echo "==========================================="
}

print_success() {
	echo -e "${GREEN}✓${NC} $1"
}

print_warning() {
	echo -e "${YELLOW}⚠${NC} $1"
}

print_error() {
	echo -e "${RED}✗${NC} $1"
}

check_prerequisites() {
	print_header "Checking Prerequisites"

	# Check kubectl
	if ! command -v kubectl &>/dev/null; then
		print_error "kubectl not found. Install kubectl first."
		exit 1
	fi
	print_success "kubectl found: $(kubectl version --client --short 2>/dev/null || kubectl version --client)"

	# Check helm
	if ! command -v helm &>/dev/null; then
		print_error "helm not found. Install helm first."
		exit 1
	fi
	print_success "helm found: $(helm version --short)"

	# Check cluster connectivity
	if ! kubectl cluster-info &>/dev/null; then
		print_error "Cannot connect to Kubernetes cluster. Check your kubeconfig."
		exit 1
	fi
	print_success "Connected to cluster: $(kubectl config current-context)"

	# Check Longhorn
	if ! kubectl get storageclass longhorn &>/dev/null; then
		print_warning "Longhorn storage class not found. Install Longhorn first or storage will fail."
	else
		print_success "Longhorn storage class found"
	fi
}

deploy_platform_nats() {
	print_header "Deploying Platform NATS"

	cd "$SCRIPT_DIR/platform-nats"

	kubectl apply -f namespace.yaml
	kubectl apply -f configmap.yaml
	kubectl apply -f service.yaml
	kubectl apply -f statefulset.yaml

	print_success "Platform NATS manifests applied"

	echo "Waiting for Platform NATS pods to be ready..."
	kubectl wait --for=condition=ready pod -l app=nats-platform -n vrsky-platform --timeout=300s || {
		print_error "Platform NATS pods did not become ready in time"
		kubectl get pods -n vrsky-platform
		exit 1
	}

	print_success "Platform NATS is ready"

	# Create KV buckets
	echo "Creating NATS KV buckets..."
	kubectl apply -f kv-setup-job.yaml
	kubectl wait --for=condition=complete job/nats-kv-setup -n vrsky-platform --timeout=120s || {
		print_warning "KV setup job did not complete. Check logs:"
		kubectl logs -n vrsky-platform job/nats-kv-setup
	}

	print_success "Platform NATS deployment complete"
}

deploy_postgresql() {
	print_header "Deploying PostgreSQL"

	cd "$SCRIPT_DIR/postgresql"

	kubectl apply -f namespace.yaml
	kubectl apply -f secret.yaml
	kubectl apply -f configmap.yaml

	# Create init script ConfigMap with embedded schema
	kubectl create configmap postgres-init-script \
		--from-file=init-schema.sql=init-schema.sql \
		-n vrsky-database \
		--dry-run=client -o yaml | kubectl apply -f -

	kubectl apply -f service.yaml
	kubectl apply -f statefulset.yaml

	print_success "PostgreSQL manifests applied"

	echo "Waiting for PostgreSQL pod to be ready..."
	kubectl wait --for=condition=ready pod -l app=postgresql -n vrsky-database --timeout=300s || {
		print_error "PostgreSQL pod did not become ready in time"
		kubectl get pods -n vrsky-database
		exit 1
	}

	print_success "PostgreSQL is ready"
	print_success "PostgreSQL deployment complete"
}

deploy_minio() {
	print_header "Deploying MinIO"

	cd "$SCRIPT_DIR/minio"

	kubectl apply -f namespace.yaml
	kubectl apply -f secret.yaml
	kubectl apply -f configmap.yaml
	kubectl apply -f deployment.yaml
	kubectl apply -f service.yaml

	print_success "MinIO manifests applied"

	echo "Waiting for MinIO pod to be ready..."
	kubectl wait --for=condition=ready pod -l app=minio -n vrsky-storage --timeout=300s || {
		print_error "MinIO pod did not become ready in time"
		kubectl get pods -n vrsky-storage
		exit 1
	}

	print_success "MinIO is ready"

	# Run setup job
	echo "Running MinIO setup job..."
	kubectl apply -f setup-job.yaml
	kubectl wait --for=condition=complete job/minio-setup -n vrsky-storage --timeout=120s || {
		print_warning "MinIO setup job did not complete. Check logs:"
		kubectl logs -n vrsky-storage job/minio-setup
	}

	print_success "MinIO deployment complete"
}

deploy_monitoring() {
	print_header "Deploying Monitoring Stack (Prometheus + Grafana)"

	cd "$SCRIPT_DIR/monitoring"

	# Check if user wants to install monitoring
	if [ "$SKIP_MONITORING" == "true" ]; then
		print_warning "Skipping monitoring installation (SKIP_MONITORING=true)"
		return
	fi

	echo "This will install Prometheus and Grafana using Helm."
	echo "Continue? (y/n)"
	read -r response
	if [[ ! "$response" =~ ^([yY][eE][sS]|[yY])$ ]]; then
		print_warning "Skipping monitoring installation"
		return
	fi

	./install-monitoring.sh || {
		print_warning "Monitoring installation failed. You can install it later manually."
	}

	print_success "Monitoring deployment complete"
}

deploy_ingress() {
	print_header "Deploying Ingress & TLS"

	cd "$SCRIPT_DIR/ingress"

	# Check if user wants to install cert-manager
	if [ "$SKIP_INGRESS" == "true" ]; then
		print_warning "Skipping Ingress installation (SKIP_INGRESS=true)"
		return
	fi

	echo "This will install cert-manager and create Ingress rules."
	echo "Have you updated the domain names in ingress.yaml? (y/n)"
	read -r response
	if [[ ! "$response" =~ ^([yY][eE][sS]|[yY])$ ]]; then
		print_warning "Please update domain names in infrastructure/kubernetes/ingress/ingress.yaml first"
		print_warning "Skipping Ingress installation"
		return
	fi

	# Install cert-manager
	./install-cert-manager.sh || {
		print_warning "cert-manager installation failed. You can install it later manually."
		return
	}

	# Create ClusterIssuers
	kubectl apply -f cert-issuers.yaml

	# Note: Ingress will be applied after API Gateway is deployed
	print_warning "Ingress rules NOT applied yet (API Gateway service doesn't exist)"
	print_warning "Apply manually after deploying API Gateway:"
	print_warning "  kubectl apply -f infrastructure/kubernetes/ingress/ingress.yaml"

	print_success "Ingress & TLS setup complete (rules not applied)"
}

provision_demo_tenant() {
	print_header "Provisioning Demo Tenant NATS"

	cd "$SCRIPT_DIR/tenant-nats"

	echo "Provision demo tenant NATS instance? (y/n)"
	read -r response
	if [[ ! "$response" =~ ^([yY][eE][sS]|[yY])$ ]]; then
		print_warning "Skipping demo tenant provisioning"
		return
	fi

	./provision-tenant-nats.sh demo-tenant 1 || {
		print_warning "Demo tenant provisioning failed"
	}

	print_success "Demo tenant NATS provisioned"
}

print_summary() {
	print_header "Deployment Summary"

	echo ""
	echo "Namespaces created:"
	kubectl get namespaces | grep vrsky

	echo ""
	echo "Pods running:"
	kubectl get pods -n vrsky-platform
	kubectl get pods -n vrsky-database
	kubectl get pods -n vrsky-storage
	kubectl get pods -n vrsky-tenants 2>/dev/null || echo "  (no tenant NATS yet)"

	echo ""
	echo "Services:"
	kubectl get svc -n vrsky-platform
	kubectl get svc -n vrsky-database
	kubectl get svc -n vrsky-storage

	echo ""
	echo "Storage (PVCs):"
	kubectl get pvc -A | grep -E "vrsky|NAME"

	echo ""
	print_success "VRSky Platform deployment complete!"
	echo ""
	echo "Next Steps:"
	echo "1. Verify all pods are running:"
	echo "     kubectl get pods -A | grep vrsky"
	echo ""
	echo "2. Access Grafana:"
	echo "     kubectl port-forward -n vrsky-monitoring svc/grafana 3000:80"
	echo "     Open: http://localhost:3000 (admin/changeme-grafana-password)"
	echo ""
	echo "3. Test PostgreSQL connection:"
	echo "     kubectl port-forward -n vrsky-database svc/postgresql 5432:5432"
	echo "     psql -h localhost -U vrsky -d vrsky"
	echo ""
	echo "4. Test MinIO console:"
	echo "     kubectl port-forward -n vrsky-storage svc/minio 9001:9001"
	echo "     Open: http://localhost:9001"
	echo ""
	echo "5. Deploy VRSky application services (API Gateway, Data Plane)"
	echo ""
	echo "6. Apply Ingress rules (after API Gateway is deployed):"
	echo "     kubectl apply -f infrastructure/kubernetes/ingress/ingress.yaml"
	echo ""
}

# Main execution
main() {
	print_header "VRSky Platform Deployment"
	echo "This script will deploy the VRSky platform infrastructure to Kubernetes."
	echo ""
	echo "Components to be deployed:"
	echo "  1. Platform NATS (3-node cluster with JetStream)"
	echo "  2. PostgreSQL (single instance with Longhorn storage)"
	echo "  3. MinIO (S3-compatible object storage)"
	echo "  4. Monitoring (Prometheus + Grafana) [optional]"
	echo "  5. Ingress & TLS (cert-manager + Let's Encrypt) [optional]"
	echo "  6. Demo Tenant NATS [optional]"
	echo ""
	echo "Press Enter to continue or Ctrl+C to cancel..."
	read -r

	check_prerequisites

	deploy_platform_nats

	deploy_postgresql

	deploy_minio

	deploy_monitoring

	deploy_ingress

	provision_demo_tenant

	print_summary
}

# Run main function
main

exit 0
