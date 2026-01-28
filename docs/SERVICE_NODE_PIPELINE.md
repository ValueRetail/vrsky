# Service Node Pipeline Architecture

This document defines the architecture for Service Nodes, the fundamental processing units of the VRSky platform.

## Overview

A VRSky integration is a pipeline of Service Nodes connected via NATS. Every node is a scalable, multi-tenant aware service that processes messages based on a specific role.

### Message Lifecycle
1. **Incoming Connection Node**: Accepts external data (HTTP, Webhook, MQTT, etc.).
2. **Converter Node**: Transforms payload (e.g., XML to JSON).
3. **Filter Node**: Evaluates logic, routes, or drops messages.
4. **Outgoing Connection Node**: Dispatches data to external destinations.

## Service Node Types

### 1. Connection Nodes (Incoming/Outgoing)
*   **Technology**: Written in Go.
*   **Loading**: Dynamically loaded as Go plugins or via an RPC-based plugin system (e.g., HashiCorp `go-plugin`).
*   **Interface**: Must implement a standard Go interface for connection management, authentication, and data transmission.
*   **Scaling**: Horizontally scaled via K3s based on throughput or connection count.

### 2. Logic Nodes (Converters/Filters)
*   **Technology**: Go-based runner with an embedded execution engine.
*   **Execution**:
    *   **Built-in**: High-performance standard functions.
    *   **Scripting**: JavaScript/TypeScript support for custom user logic.
*   **Sandboxing**: Logic must be executed in a restricted environment to ensure tenant safety.

## Multi-Tenancy & Segregation

Service Nodes must support multiple customers simultaneously (Shared Tenant model).

- **Customer ID**: Every message carried by the platform must include a `Customer-ID` in its metadata.
- **Contextual Execution**: Nodes load the specific configuration/script associated with the `Customer-ID` before processing the message.
- **Resource Limits**: K3s-level limits ensure no single customer can starve others in a shared node.

## Scaling & Orchestration

- **K3s Managed**: Each node type is deployed as a Deployment/StatefulSet in K3s.
- **Configurable Scaling**: Users (or the platform) can configure HPA (Horizontal Pod Autoscaler) rules per node.
- **Namespace Isolation**:
    - **Shared**: Nodes live in a shared namespace, segregating by ID.
    - **Dedicated**: Nodes can be deployed in a dedicated namespace for hard isolation.