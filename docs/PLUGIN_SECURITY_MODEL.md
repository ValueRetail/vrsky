# VRSky Security Model & Threat Assessment

**Document Version**: 1.0  
**Status**: Draft  
**Date**: January 28, 2026

## 1. Introduction

This document outlines the security architecture and threat model for the VRSky integration platform. As a multi-tenant iPaaS executing user-defined logic, security is paramount. We adhere to a **"Defense in Depth"** strategy, assuming that any single layer may be compromised.

## 2. Core Security Principles

1.  **Zero Trust**: No component implicitly trusts another. All RPC calls are authenticated.
2.  **Least Privilege**: Plugins and Logic Nodes operate with the minimum permissions required.
3.  **Strict Isolation**: Tenant data must never cross boundaries without explicit authorization.
4.  **Ephemeral State**: No long-term storage of sensitive data in the core platform.

## 3. Threat Model

### 3.1. Attacker Profiles
-   **Malicious Tenant**: A user signing up for the free tier attempting to attack other tenants or the platform.
-   **Compromised Plugin**: A 3rd-party connection plugin containing malicious code.
-   **External Attacker**: Attempting to breach the API Gateway or NATS transport.

### 3.2. Attack Vectors & Mitigations

| Threat Scenario | Risk Level | Mitigation Strategy |
| :--- | :--- | :--- |
| **Tenant A accessing Tenant B's data** | Critical | • **NATS Accounts**: Strict subject segregation.<br>• **Metadata Enforcement**: Customer-ID checked at every node.<br>• **WASM Isolation**: Separate memory spaces per execution. |
| **Malicious Logic Node (Infinite Loop/Crypto Mining)** | High | • **WASM Metering**: Instruction counting / timeout enforcement.<br>• **Resource Quotas**: K3s CPU/Memory limits.<br>• **Behavior Analysis**: Alert on anomalous usage patterns. |
| **Connection Plugin "Phoning Home"** | High | • **Network Policies**: K3s NetworkPolicies blocking egress to non-whitelisted IPs.<br>• **Interface Sandboxing**: RPC only exposes specific host functions. |
| **Message Tampering in Transit** | Medium | • **mTLS**: All internal traffic (gRPC, NATS) encrypted.<br>• **Payload Encryption**: (Optional) Tenants can encrypt payloads at the edge. |
| **Secret Leakage (API Keys)** | High | • **Secret Manager Integration**: Secrets injected only at runtime.<br>• **Memory Scrubbing**: Logic Nodes clear memory after execution.<br>• **No Logging of Secrets**: Log scrubbers active. |

## 4. Multi-Tenancy Architecture

### 4.1. Shared Tenant Isolation
In the "Shared Tenant" model, isolation is **logical but strict**:
-   **NATS**: Users share a NATS Account but have distinct Subject spaces `tenant.{id}.>`.
-   **Runtime**: Service Nodes check `Customer-ID` metadata before processing.
-   **Logic**: WASM runtime creates a fresh, clean instance for every message processing event.

### 4.2. Dedicated Tenant Isolation
In the "Dedicated Tenant" model, isolation is **physical**:
-   **Kubernetes**: Dedicated Namespace per tenant.
-   **NATS**: Dedicated NATS Account.
-   **Compute**: Dedicated Pods for Service Nodes.

## 5. Plugin Security

### 5.1. Connection Node (RPC) Security
-   **Execution**: Runs as a non-privileged user (UID > 10000).
-   **Filesystem**: Read-only root filesystem, ephemeral `/tmp`.
-   **Capabilities**: Linux capabilities dropped (no `CAP_NET_ADMIN`, etc.).
-   **Signature Verification**: Plugins must be signed by VRSky or a trusted publisher.

### 5.2. Logic Node (WASM) Security
-   **Sandbox**: `wazero` provides a fault-isolated sandbox. A crash in WASM does not crash the host.
-   **API Surface**: Restricted to explicitly exported Host Functions.
    -   *No* `fs.open`
    -   *No* `net.dial` (raw sockets)
    -   *No* `os.exec`

## 6. Data Security

### 6.1. Transit
-   **TLS 1.3** required for all ingress/egress.
-   **mTLS** for all intra-service communication.

### 6.2. Temporary Storage (Reference Messaging)
-   **Encryption at Rest**: Object storage buckets encrypted (SSE-S3 or SSE-KMS).
-   **Short TTL**: Data automatically deleted after processing window (e.g., 15 mins).
-   **Access Control**: Pre-signed URLs with short expiration used for retrieval.

## 7. Operational Security

-   **Audit Logs**: All control plane actions (config changes, plugin deployments) are immutable.
-   **Vulnerability Scanning**: CI/CD pipelines scan Go binaries and base images (trivy/grype).
-   **Bug Bounty**: (Future) Program to reward responsible disclosure.

## 8. Next Steps

1.  Define specific K3s `NetworkPolicy` rules for Shared Tenant namespaces.
2.  Implement `wazero` instruction metering POC.
3.  Select a Secrets Management solution (HashiCorp Vault or K8s Secrets).
