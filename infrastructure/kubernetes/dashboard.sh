#!/bin/bash

# Kubernetes Dashboard Helper Script
# Manages deployment and access to the Kubernetes Dashboard with extended session timeout

set -e

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

DASHBOARD_NAMESPACE="kubernetes-dashboard"
DASHBOARD_SERVICE="kubernetes-dashboard"
ADMIN_USER="admin-user"

# Helper functions
print_header() {
    echo -e "${BLUE}=== $1 ===${NC}"
}

print_success() {
    echo -e "${GREEN}✓ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠ $1${NC}"
}

print_error() {
    echo -e "${RED}✗ $1${NC}"
}

# Check if kubectl is available
check_kubectl() {
    if ! command -v kubectl &> /dev/null; then
        print_error "kubectl not found. Please install kubectl first."
        exit 1
    fi
    print_success "kubectl found"
}

# Check cluster connectivity
check_cluster() {
    if ! kubectl cluster-info &> /dev/null; then
        print_error "Cannot connect to Kubernetes cluster"
        exit 1
    fi
    print_success "Connected to cluster"
}

# Deploy the dashboard
deploy_dashboard() {
    print_header "Deploying Kubernetes Dashboard"
    
    SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
    MANIFEST="${SCRIPT_DIR}/kubernetes-dashboard.yaml"
    
    if [ ! -f "$MANIFEST" ]; then
        print_error "kubernetes-dashboard.yaml not found in $SCRIPT_DIR"
        exit 1
    fi
    
    print_warning "Applying kubernetes-dashboard.yaml..."
    kubectl apply -f "$MANIFEST"
    
    print_success "Dashboard manifests applied"
    print_warning "Waiting for deployment to be ready (this may take 30-60 seconds)..."
    
    kubectl wait --for=condition=available --timeout=300s \
        deployment/kubernetes-dashboard -n $DASHBOARD_NAMESPACE 2>/dev/null || true
    
    # Give it a moment to fully initialize
    sleep 5
    
    print_success "Dashboard deployment complete"
}

# Create admin user
create_admin_user() {
    print_header "Creating Admin User"
    
    # Check if admin user already exists
    if kubectl get serviceaccount $ADMIN_USER -n $DASHBOARD_NAMESPACE &> /dev/null; then
        print_warning "Admin user already exists"
        return
    fi
    
    kubectl apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: $ADMIN_USER
  namespace: $DASHBOARD_NAMESPACE

---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: $ADMIN_USER
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-admin
subjects:
- kind: ServiceAccount
  name: $ADMIN_USER
  namespace: $DASHBOARD_NAMESPACE
EOF

    print_success "Admin user created"
}

# Get access token
get_token() {
    print_header "Dashboard Access Token"
    
    # Check if service exists
    if ! kubectl get serviceaccount $ADMIN_USER -n $DASHBOARD_NAMESPACE &> /dev/null; then
        print_error "Admin user not found. Run 'dashboard.sh create-admin' first"
        return 1
    fi
    
    # Try to create a 24-hour token, but handle clusters that don't support this duration
    if ! TOKEN=$(kubectl -n "$DASHBOARD_NAMESPACE" create token "$ADMIN_USER" --duration=24h 2>/dev/null); then
        echo -e "${YELLOW}Warning: Failed to create 24-hour token. Your cluster may not support this duration. Retrying with cluster default duration...${NC}"
        if ! TOKEN=$(kubectl -n "$DASHBOARD_NAMESPACE" create token "$ADMIN_USER" 2>/dev/null); then
            print_error "Failed to create access token. Please check your cluster version and permissions."
            return 1
        fi
        DURATION_INFO="with cluster default duration"
    else
        DURATION_INFO="valid for 24 hours"
    fi
    
    if [ -z "$TOKEN" ]; then
        print_error "Token creation command succeeded but returned an empty token."
        return 1
    fi
    
    echo ""
    echo -e "${GREEN}Token (${DURATION_INFO}):${NC}"
    echo -e "${YELLOW}${TOKEN}${NC}"
    echo ""
    
    # Also save to a securely created temporary file
    # Set restrictive umask to ensure file is created with 600 permissions from the start
    # This eliminates race condition window where file might be readable by other users
    old_umask=$(umask)
    umask 077
    TOKEN_FILE=$(mktemp "${TMPDIR:-/tmp}/k8s-dashboard-token.XXXXXX")
    umask "$old_umask"

    if [ -z "$TOKEN_FILE" ] || [ ! -f "$TOKEN_FILE" ]; then
        print_error "Failed to create temporary file for token."
        return 1
    fi

    if ! chmod 600 "$TOKEN_FILE"; then
        print_error "Failed to set secure permissions on token file: $TOKEN_FILE"
        return 1
    fi

    printf '%s\n' "$TOKEN" > "$TOKEN_FILE"
    print_success "Token saved to $TOKEN_FILE (file permissions set to 600)"
}

# Port forward to access dashboard
port_forward() {
    local PORT="${1:-8443}"
    
    print_header "Port Forwarding Dashboard"
    
    if ! kubectl get service $DASHBOARD_SERVICE -n $DASHBOARD_NAMESPACE &> /dev/null; then
        print_error "Dashboard service not found"
        return 1
    fi
    
    print_warning "Starting port-forward..."
    echo ""
    echo -e "${GREEN}Access dashboard at: https://localhost:${PORT}${NC}"
    echo ""
    echo "Steps to access:"
    echo "1. Open https://localhost:${PORT} in your browser"
    echo "2. Accept the self-signed certificate warning"
    echo "3. Click 'Skip' button or paste a token"
    echo "4. Run 'dashboard.sh token' in another terminal to get a fresh token"
    echo ""
    print_warning "Forwarding... (Press Ctrl+C to stop)"
    
    kubectl port-forward -n $DASHBOARD_NAMESPACE \
        svc/$DASHBOARD_SERVICE $PORT:443
}

# Check dashboard status
status() {
    print_header "Dashboard Status"
    
    # Check namespace
    if kubectl get namespace $DASHBOARD_NAMESPACE &> /dev/null; then
        print_success "Namespace exists: $DASHBOARD_NAMESPACE"
    else
        print_error "Namespace not found: $DASHBOARD_NAMESPACE"
        return 1
    fi
    
    # Check deployment
    if kubectl get deployment kubernetes-dashboard -n $DASHBOARD_NAMESPACE &> /dev/null; then
        print_success "Deployment exists"
        
        READY=$(kubectl get deployment kubernetes-dashboard -n $DASHBOARD_NAMESPACE \
            -o jsonpath='{.status.readyReplicas}')
        DESIRED=$(kubectl get deployment kubernetes-dashboard -n $DASHBOARD_NAMESPACE \
            -o jsonpath='{.status.replicas}')
        
        if [ "$READY" = "$DESIRED" ] && [ ! -z "$READY" ]; then
            print_success "Deployment ready: $READY/$DESIRED replicas"
        else
            print_warning "Deployment not ready: $READY/$DESIRED replicas"
        fi
    else
        print_error "Deployment not found"
        return 1
    fi
    
    # Check service
    if kubectl get service $DASHBOARD_SERVICE -n $DASHBOARD_NAMESPACE &> /dev/null; then
        print_success "Service exists: $DASHBOARD_SERVICE"
    else
        print_error "Service not found"
        return 1
    fi
    
    # Check admin user
    if kubectl get serviceaccount $ADMIN_USER -n $DASHBOARD_NAMESPACE &> /dev/null; then
        print_success "Admin user exists: $ADMIN_USER"
    else
        print_warning "Admin user not created yet (run 'dashboard.sh create-admin')"
    fi
    
    echo ""
    print_header "Pod Status"
    kubectl get pods -n $DASHBOARD_NAMESPACE -l k8s-app=kubernetes-dashboard
}

# Show logs
logs() {
    print_header "Dashboard Logs"
    kubectl logs -n $DASHBOARD_NAMESPACE -l k8s-app=kubernetes-dashboard -f
}

# Delete dashboard
delete() {
    print_header "Uninstalling Kubernetes Dashboard"
    
    read -p "Are you sure? (y/N) " -n 1 -r
    echo
    
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        kubectl delete namespace $DASHBOARD_NAMESPACE
        print_success "Dashboard uninstalled"
    else
        print_warning "Cancelled"
    fi
}

# Usage information
usage() {
    cat <<EOF
${BLUE}Kubernetes Dashboard Helper${NC}

Usage: ./dashboard.sh [COMMAND] [OPTIONS]

Commands:
  deploy              Deploy the Kubernetes Dashboard
  create-admin        Create admin user for dashboard access
  token               Get access token (valid for 24 hours)
  port-forward [PORT] Start port-forward (default: 8443)
                      Access at: https://localhost:[PORT]
  status              Check dashboard status
  logs                Show dashboard logs (follow mode)
  delete              Uninstall dashboard
  help                Show this help message

Examples:
  # Deploy dashboard
  ./dashboard.sh deploy

  # Create admin user
  ./dashboard.sh create-admin

  # Get token
  ./dashboard.sh token

  # Start port forward on custom port
  ./dashboard.sh port-forward 9443

  # Check status
  ./dashboard.sh status

Quick Start:
  ./dashboard.sh deploy
  ./dashboard.sh create-admin
  ./dashboard.sh token
  ./dashboard.sh port-forward

Then open https://localhost:8443 in your browser and paste the token.

Session Configuration:
  - Token TTL: 24 hours
  - Session Timeout: 8 hours
  - No need to re-enter token daily!

EOF
}

# Main script
main() {
    if [ $# -eq 0 ]; then
        usage
        exit 0
    fi
    
    COMMAND="$1"
    
    case "$COMMAND" in
        deploy)
            check_kubectl
            check_cluster
            deploy_dashboard
            ;;
        create-admin)
            check_kubectl
            check_cluster
            create_admin_user
            ;;
        token)
            check_kubectl
            check_cluster
            get_token
            ;;
        port-forward)
            check_kubectl
            check_cluster
            PORT="${2:-8443}"
            port_forward "$PORT"
            ;;
        status)
            check_kubectl
            check_cluster
            status
            ;;
        logs)
            check_kubectl
            check_cluster
            logs
            ;;
        delete)
            check_kubectl
            check_cluster
            delete
            ;;
        help|--help|-h)
            usage
            ;;
        *)
            print_error "Unknown command: $COMMAND"
            echo ""
            usage
            exit 1
            ;;
    esac
}

main "$@"
