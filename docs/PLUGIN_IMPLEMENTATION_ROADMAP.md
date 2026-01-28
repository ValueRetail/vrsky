# VRSky Plugin System - Implementation Roadmap

**Document Version**: 1.0  
**Status**: Planning  
**Date**: January 28, 2026

## Phase 1: Foundation (Weeks 1-4)

**Goal**: Establish the core RPC and WASM runtime infrastructure.

### 1.1. Go RPC Interface Definition
- [ ] Define `pkg/connector` interface in Go.
- [ ] Implement `gRPC` proto definitions.
- [ ] Create `HandshakeConfig` and shared constants.
- [ ] Build a "Reference Host" that can load a dummy plugin.

### 1.2. WASM Runtime Integration
- [ ] Integrate `tetratelabs/wazero` into `pkg/logic`.
- [ ] Implement basic Host Function API (Log, GetMetadata).
- [ ] Create a "Hello World" WASM runner (loading a pre-compiled `.wasm`).
- [ ] Benchmark startup time and memory overhead.

### 1.3. Connection Node Skeleton
- [ ] Create `cmd/connection-node` (The Host process).
- [ ] Implement plugin discovery (finding binaries in a dir).
- [ ] Implement lifecycle management (start, stop, restart plugin).

## Phase 2: Core Capabilities (Weeks 5-8)

**Goal**: Make plugins actually useful with real data flow.

### 2.1. Logic SDK (TypeScript)
- [ ] Create `@vrsky/sdk` npm package.
- [ ] Set up `Javy` or `AssemblyScript` build pipeline.
- [ ] Implement `Convert` and `Filter` interfaces.

### 2.2. HTTP Connection Plugin
- [ ] Build the first "Real" plugin: `connectors/http`.
- [ ] Support Incoming Webhooks (Receive).
- [ ] Support Outgoing Requests (Send).

### 2.3. Configuration & Metadata
- [ ] Implement `Customer-ID` propagation from NATS -> Node -> Plugin.
- [ ] Implement secure secret passing (Host -> Plugin).

## Phase 3: Developer Experience (Weeks 9-12)

**Goal**: Enable third-party developers to build plugins.

### 3.1. CLI Tooling
- [ ] `vrsky init`: Scaffolding for Go and TS projects.
- [ ] `vrsky build`: Wrapper for `go build` and `wasm-pack`/`javy`.
- [ ] `vrsky run`: Local runner simulating the platform environment.

### 3.2. Marketplace MVP
- [ ] Define Metadata Schema (`manifest.json`).
- [ ] Implement Plugin Registry API (Upload/List/Download).
- [ ] Implement Versioning logic (SemVer checks).

## Phase 4: Security & Hardening (Weeks 13-16)

**Goal**: Prepare for production and untrusted code.

### 4.1. Advanced Sandboxing
- [ ] Implement `NetworkPolicy` controller for K3s.
- [ ] Implement WASM instruction metering (timeout enforcement).
- [ ] Audit RPC interface for leakage.

### 4.2. Observability
- [ ] Distributed Tracing (OpenTelemetry) context propagation.
- [ ] Metrics aggregation (Plugin CPU/RAM -> Prometheus).

## Scope for POC (April 15, 2026)

For the Proof of Concept release, we will deliver:

1.  **RPC Host**: Able to load one local Go plugin.
2.  **HTTP Plugin**: A working HTTP In/Out connector.
3.  **WASM Runtime**: Able to run a simple JSON transformation.
4.  **End-to-End**: HTTP In -> WASM Transform -> HTTP Out pipeline.
