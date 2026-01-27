#!/bin/bash
# provision-tenant-nats.sh
# Creates a new NATS instance for a tenant

set -e

# Usage check
if [ "$#" -ne 2 ]; then
	echo "Usage: $0 <tenant-id> <instance-number>"
	echo "Example: $0 demo-tenant 1"
	exit 1
fi

TENANT_ID=$1
INSTANCE_NUM=$2

# Validate inputs
if [[ ! "$TENANT_ID" =~ ^[a-z0-9-]+$ ]]; then
	echo "Error: tenant-id must contain only lowercase letters, numbers, and hyphens"
	exit 1
fi

if [[ ! "$INSTANCE_NUM" =~ ^[0-9]+$ ]]; then
	echo "Error: instance-number must be a positive integer"
	exit 1
fi

echo "Provisioning NATS instance for tenant: $TENANT_ID (instance $INSTANCE_NUM)"

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEMP_DIR=$(mktemp -d)

# Process templates
echo "Processing templates..."

# Deployment
envsubst <"$SCRIPT_DIR/deployment-template.yaml" >"$TEMP_DIR/deployment.yaml" <<EOF
export TENANT_ID=$TENANT_ID
export INSTANCE_NUM=$INSTANCE_NUM
EOF

# Service
envsubst <"$SCRIPT_DIR/service-template.yaml" >"$TEMP_DIR/service.yaml" <<EOF
export TENANT_ID=$TENANT_ID
export INSTANCE_NUM=$INSTANCE_NUM
EOF

# NetworkPolicy
envsubst <"$SCRIPT_DIR/networkpolicy-template.yaml" >"$TEMP_DIR/networkpolicy.yaml" <<EOF
export TENANT_ID=$TENANT_ID
export INSTANCE_NUM=$INSTANCE_NUM
EOF

# Ensure namespace exists
echo "Ensuring namespace exists..."
kubectl apply -f "$SCRIPT_DIR/namespace.yaml"

# Apply manifests
echo "Applying Deployment..."
kubectl apply -f "$TEMP_DIR/deployment.yaml"

echo "Applying Service..."
kubectl apply -f "$TEMP_DIR/service.yaml"

echo "Applying NetworkPolicy..."
kubectl apply -f "$TEMP_DIR/networkpolicy.yaml"

# Wait for pod to be ready
echo "Waiting for NATS pod to be ready..."
kubectl wait --for=condition=ready pod \
	-l app=nats,tenant-id=$TENANT_ID,instance-num=$INSTANCE_NUM \
	-n vrsky-tenants \
	--timeout=120s

# Get pod name
POD_NAME=$(kubectl get pods -n vrsky-tenants \
	-l app=nats,tenant-id=$TENANT_ID,instance-num=$INSTANCE_NUM \
	-o jsonpath='{.items[0].metadata.name}')

echo "NATS instance provisioned successfully!"
echo ""
echo "Details:"
echo "  Tenant ID: $TENANT_ID"
echo "  Instance Number: $INSTANCE_NUM"
echo "  Pod Name: $POD_NAME"
echo "  Service DNS: nats-$TENANT_ID-$INSTANCE_NUM.vrsky-tenants.svc.cluster.local:4222"
echo ""
echo "Verify with:"
echo "  kubectl logs -n vrsky-tenants $POD_NAME"
echo "  kubectl exec -n vrsky-tenants $POD_NAME -- nats-server --signal stats"

# Cleanup temp directory
rm -rf "$TEMP_DIR"

exit 0
