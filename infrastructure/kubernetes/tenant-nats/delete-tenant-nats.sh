#!/bin/bash
# delete-tenant-nats.sh
# Deletes a NATS instance for a tenant

set -e

# Usage check
if [ "$#" -ne 2 ]; then
	echo "Usage: $0 <tenant-id> <instance-number>"
	echo "Example: $0 demo-tenant 1"
	exit 1
fi

TENANT_ID=$1
INSTANCE_NUM=$2

echo "Deleting NATS instance for tenant: $TENANT_ID (instance $INSTANCE_NUM)"

# Delete deployment
echo "Deleting Deployment..."
kubectl delete deployment nats-$TENANT_ID-$INSTANCE_NUM -n vrsky-tenants --ignore-not-found=true

# Delete service
echo "Deleting Service..."
kubectl delete service nats-$TENANT_ID-$INSTANCE_NUM -n vrsky-tenants --ignore-not-found=true

# Delete network policy
echo "Deleting NetworkPolicy..."
kubectl delete networkpolicy tenant-nats-isolation-$TENANT_ID -n vrsky-tenants --ignore-not-found=true

echo "NATS instance deleted successfully!"

exit 0
