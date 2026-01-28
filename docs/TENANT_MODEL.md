# VRSky Tenant Model

VRSky employs a tiered multi-tenancy model to balance cost-efficiency for free users and strict isolation for enterprise customers.

## Tenant Types

### 1. Shared Tenants (Standard)
*   **Target**: Free tier users and small integrations.
*   **Resource Sharing**: Multiple customers share the same Service Nodes, NATS resources, and infrastructure.
*   **Segregation**: Logical segregation using `Customer-ID` in message headers and metadata.
*   **Limitations**: Fixed resource quotas, limited configurability, shared IP space.

### 2. Dedicated Tenants (Premium)
*   **Target**: Enterprise customers and high-throughput integrations.
*   **Isolation**: Dedicated Service Node instances, potentially in separate K3s namespaces.
*   **Configurability**: Full control over scaling parameters, dedicated IPs, and custom resources.
*   **Guarantees**: Hardware-level resource isolation (CPU/Memory) via K3s quotas.

## Customer Migration

A core requirement is the ability to move customers between tenants without disruption.

### Migration Scenarios
- **Shared -> Shared**: Balancing capacity when a shared tenant reaches its designed limit.
- **Shared -> Dedicated**: Promoting a customer after a subscription upgrade.
- **Dedicated -> Self-Hosted**: Exporting a configuration for a single-tenant deployment.

### Mechanism
Migration is achieved by:
1.  **Metadata Update**: Updating the Control Plane to re-map the Customer ID to a different set of NATS subjects or namespaces.
2.  **State Transfer**: Moving any persistent state (if the Storage-as-a-Service add-on is used).
3.  **Draining**: Allowing current messages to complete processing before switching the ingress point.

## Self-Hosting

The VRSky stack is designed to be "Single-Tenant" ready. A self-hosted instance is essentially a Dedicated Tenant deployment where the customer manages the underlying K3s cluster.