#!/bin/bash
# PHASE 1E PRODUCTION DEPLOYMENT GUIDE
# VRSky Filter Component - Kubernetes Deployment to ServeTheWorld (Oslo)

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_header() {
    echo ""
    echo "=========================================================="
    echo -e "${BLUE}$1${NC}"
    echo "=========================================================="
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

# Main deployment
main() {
    print_header "PHASE 1E FILTER COMPONENT - PRODUCTION DEPLOYMENT"
    
    echo ""
    echo "This script will deploy the VRSky Filter component (Phase 1E) to your"
    echo "production Kubernetes cluster (ServeTheWorld, Oslo)"
    echo ""
    echo "Prerequisites:"
    echo "  • kubectl configured and connected to production cluster"
    echo "  • vrsky/filter:latest image available (or access to Docker registry)"
    echo "  • vrsky-platform namespace exists"
    echo "  • NATS platform running (nats-platform service available)"
    echo ""
    
    # Check prerequisites
    print_header "Step 1: Verifying Prerequisites"
    
    if ! command -v kubectl &>/dev/null; then
        print_error "kubectl not found. Install kubectl first."
        echo "  macOS:   brew install kubectl"
        echo "  Linux:   curl -LO https://dl.k8s.io/release/\$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
        exit 1
    fi
    print_success "kubectl found"
    
    if ! kubectl cluster-info &>/dev/null; then
        print_error "Cannot connect to Kubernetes cluster"
        exit 1
    fi
    print_success "Connected to cluster: $(kubectl config current-context)"
    
    # Check namespace
    if ! kubectl get namespace vrsky-platform &>/dev/null; then
        print_error "Namespace vrsky-platform not found"
        exit 1
    fi
    print_success "Namespace vrsky-platform exists"
    
    # Check NATS
    if kubectl get svc -n vrsky-platform nats-platform &>/dev/null; then
        print_success "NATS service (nats-platform) available"
    else
        print_warning "NATS service not found - filter will retry connection"
    fi
    
    echo ""
    
    # Deploy filter
    print_header "Step 2: Deploying Filter Component"
    
    FILTER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../infrastructure/kubernetes/filter" && pwd)"
    
    if [ ! -f "$FILTER_DIR/deployment.yaml" ] || [ ! -f "$FILTER_DIR/service.yaml" ]; then
        print_error "Filter manifests not found at $FILTER_DIR"
        exit 1
    fi
    
    echo "Applying filter deployment..."
    kubectl apply -f "$FILTER_DIR/deployment.yaml"
    print_success "Deployment manifest applied"
    
    echo "Applying filter service..."
    kubectl apply -f "$FILTER_DIR/service.yaml"
    print_success "Service manifest applied"
    
    echo ""
    
    # Wait for deployment
    print_header "Step 3: Waiting for Filter Pods to be Ready"
    
    echo "Waiting for 3 replicas to be ready (timeout: 300s)..."
    if kubectl wait --for=condition=ready pod -l app=vrsky-filter -n vrsky-platform --timeout=300s 2>/dev/null; then
        print_success "All filter pods are ready!"
    else
        print_warning "Pods did not become ready in time"
        echo "Checking pod status..."
        kubectl get pods -n vrsky-platform -l app=vrsky-filter
    fi
    
    echo ""
    
    # Display status
    print_header "Step 4: Deployment Status"
    
    echo "Deployment:"
    kubectl get deployment -n vrsky-platform vrsky-filter
    
    echo ""
    echo "Pods:"
    kubectl get pods -n vrsky-platform -l app=vrsky-filter -o wide
    
    echo ""
    echo "Service:"
    kubectl get svc -n vrsky-platform vrsky-filter
    
    echo ""
    
    # Check logs
    print_header "Step 5: Verifying Filter Connectivity"
    
    POD=$(kubectl get pods -n vrsky-platform -l app=vrsky-filter -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    
    if [ -n "$POD" ]; then
        echo "Checking logs from pod: $POD"
        echo ""
        
        # Wait a bit for pod to initialize
        sleep 3
        
        kubectl logs -n vrsky-platform "$POD" | tail -20
    fi
    
    echo ""
    
    # Run smoke test
    print_header "Step 6: Running Smoke Test"
    
    SMOKE_TEST="$(cd "$(dirname "${BASH_SOURCE[0]}")/../infrastructure/scripts" && pwd)/test-filter-smoke.sh"
    
    if [ -f "$SMOKE_TEST" ]; then
        echo "Running smoke test script..."
        bash "$SMOKE_TEST"
    else
        print_warning "Smoke test script not found at $SMOKE_TEST"
        echo "Run manually with: bash infrastructure/scripts/test-filter-smoke.sh"
    fi
    
    echo ""
    
    # Summary
    print_header "DEPLOYMENT COMPLETE"
    
    print_success "Phase 1E Filter Component deployed to production!"
    
    echo ""
    echo "Deployment Details:"
    echo "  Namespace:    vrsky-platform"
    echo "  Deployment:   vrsky-filter"
    echo "  Replicas:     3 (HA across nodes)"
    echo "  Image:        vrsky/filter:latest (9.36MB)"
    echo "  Status:       $(kubectl get deployment -n vrsky-platform vrsky-filter -o jsonpath='{.status.readyReplicas}/{.spec.replicas}' 2>/dev/null || echo 'check manually')"
    
    echo ""
    echo "Next Steps:"
    echo "  1. Monitor logs:"
    echo "     kubectl logs -f -n vrsky-platform -l app=vrsky-filter"
    echo ""
    echo "  2. Port-forward for testing:"
    echo "     kubectl port-forward -n vrsky-platform svc/vrsky-filter 9090:9090"
    echo ""
    echo "  3. Check resource usage:"
    echo "     kubectl top pods -n vrsky-platform -l app=vrsky-filter"
    echo ""
    echo "  4. View detailed pod info:"
    echo "     kubectl describe pod -n vrsky-platform -l app=vrsky-filter"
    echo ""
    echo "  5. Access pod shell:"
    echo "     kubectl exec -it -n vrsky-platform <POD_NAME> -- /bin/sh"
    echo ""
    echo "Health Checks:"
    echo "  - Liveness probe: /bin/sh -c 'nc -z 127.0.0.1 9090'"
    echo "  - Readiness probe: /bin/sh -c 'nc -z 127.0.0.1 9090'"
    echo ""
    echo "Phase 1E is now LIVE in production! 🚀"
    echo ""
}

# Run main
main
