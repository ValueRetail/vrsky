# VRSky Plugin Architecture

**Document Version**: 1.0  
**Status**: Design Specification  
**Date**: January 28, 2026

## Overview

VRSky employs a **hybrid plugin system** combining **RPC-based Go plugins** for network protocols (Connection Nodes) and **WebAssembly** for user-defined logic (Logic Nodes). This design balances performance, security, and developer experience.

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────┐
│                    VRSky Service Node                        │
│                                                               │
│  ┌──────────────────────────────────────────────────────┐   │
│  │         Integration Pipeline (K3s Pod)              │   │
│  │                                                       │   │
│  │  [Incoming Conn] → [Converter] → [Filter] → [Outgoing] │
│  │                                                       │   │
│  └──────────────────────────────────────────────────────┘   │
│           ↓                    ↓               ↓              │
│      RPC Subprocess      WASM Runtime      RPC Subprocess    │
│   (Connection Plugin)   (Logic Plugins)  (Connection Plugin) │
│                                                               │
│      Isolated Process    Sandboxed VM      Isolated Process  │
│      (Go Binary)         (Process 0)        (Go Binary)       │
│                                                               │
└─────────────────────────────────────────────────────────────┘
```

## Plugin Types

### 1. Connection Nodes (RPC-Based)

**Used for**: Incoming Connections, Outgoing Connections

**Technology**: Go plugins running as separate subprocesses  
**Communication**: gRPC over local network  
**Sandboxing**: Interface-based restrictions  
**Capabilities**: Protocol handlers (HTTP, MQTT, FTP, Salesforce, etc.)

**Why RPC?**
- Plugins need OS-level access for network protocols
- RPC subprocess isolation prevents a plugin crash from crashing the host
- Easier to enforce resource limits per plugin (K3s can manage subprocess resources)
- Natural Go interface implementation for plugin authors

### 2. Logic Nodes (WASM-Based)

**Used for**: Converters, Filters

**Technology**: WebAssembly runtime (wazero)  
**Input Language**: TypeScript/JavaScript  
**Sandboxing**: Process-level isolation + WASM memory boundaries  
**Capabilities**: Data transformation, conditional routing, API calls through exposed functions

**Why WASM?**
- True memory isolation - WASM code cannot access host memory
- No direct filesystem or network access unless explicitly granted
- Language-agnostic - can support Python, Rust, etc. later by compiling to WASM
- Easier for untrusted user code (free tier customers)
- Predictable resource limits (WASM memory + execution time)

## Plugin Lifecycle

### Connection Node (RPC) Lifecycle

```
1. Discovery: Control Plane queries plugin registry
2. Verification: Check cryptographic signature + version compatibility
3. Launch: Control Plane spawns subprocess with plugin binary
4. Handshake: RPC connection established, capabilities negotiated
5. Operation: Service Node makes RPC calls to plugin
6. Shutdown: Plugin subprocess terminated gracefully or forcefully
7. Restart: Automatic restart on crash (configurable retry policy)
```

### Logic Node (WASM) Lifecycle

```
1. Upload: User provides TypeScript source code
2. Validation: VRSky validates code (syntax, imports)
3. Compilation: TypeScript → WASM (wasm-pack or tsc + wasm-bindgen)
4. Storage: WASM binary stored in object storage with version tracking
5. Deployment: WASM loaded into runtime when needed
6. Execution: WASM instance created per Customer-ID (isolation)
7. Caching: Compiled WASM cached in memory for performance
```

## Message Flow Through Pipeline

```
External System
      ↓
[Incoming Connection Node] (RPC subprocess)
      ↓ (Message over NATS)
[Converter Node] (WASM instance)
      ↓ (Data transformation)
[Filter Node] (WASM instance)
      ↓ (Conditional routing)
[Outgoing Connection Node] (RPC subprocess)
      ↓
External System
```

### Customer ID Propagation

Each message carries a `Customer-ID` in its metadata:

```go
type MessageMetadata struct {
    CustomerID    string
    IntegrationID string
    MessageID     string
    Timestamp     time.Time
    TenantID      string
}
```

**Connection Nodes**: Receive Customer-ID in metadata, use to load correct configuration  
**Logic Nodes**: WASM instance created per Customer-ID, ensuring isolation  
**Scaling**: Multiple WASM instances per Converter/Filter, each serving different customers

## Security Model

### Connection Node Sandboxing (RPC)

**What the plugin CAN do:**
- Implement the ConnectionNode interface methods
- Call host-provided functions (logging, metrics, configuration lookup)
- Read configuration specific to its CustomerID

**What the plugin CANNOT do:**
- Make arbitrary network calls (only through host interface)
- Access other customers' data (enforced by interface contract)
- Access the filesystem directly (only through host interface)
- Crash the main service (separate process)

**Enforcement:**
- Interface-based design (Go compiler enforces)
- RPC boundary (subprocess isolation)
- Process resource limits (K3s limits)

### Logic Node Sandboxing (WASM)

**What WASM code CAN do:**
- Transform message payload
- Call exposed host functions (HTTP, logging, Customer-ID access)
- Perform CPU-intensive calculations (within timeout)
- Access message metadata

**What WASM code CANNOT do:**
- Access the host filesystem
- Make arbitrary network calls (only through exposed host functions)
- Access other WASM instances or customer data
- Make syscalls
- Exceed memory limit or execution timeout

**Enforcement:**
- WASM memory isolation (runtime boundary)
- No direct access to host unless via explicit host function exports
- Execution timeout enforced by runtime
- Memory limits enforced by WASM linker + runtime

## Host Functions (API for Logic Nodes)

Logic Nodes (WASM) can call these host functions:

```typescript
// Data transformation helpers
export function Transform(input: any, transformType: string): any;

// HTTP calls (to external services)
export function HTTPCall(config: HTTPConfig): HTTPResponse;

// Logging
export function Log(level: string, message: string): void;

// Validation
export function Validate(data: any, schema: string): ValidationResult;

// Customer & Integration context
export function GetCustomerID(): string;
export function GetIntegrationID(): string;
export function GetMessageMetadata(): MessageMetadata;

// Error handling
export function RaiseError(code: string, message: string): void;
```

## Configuration & Deployment

### Connection Plugins

Configured in integration YAML:

```yaml
integration:
  id: invoice-sync
  incoming:
    type: http-webhook
    plugin: github.com/vrsky/connectors/http@v1.2.3
    config:
      endpoint: /webhooks/invoices
      tlsEnabled: true
  outgoing:
    type: salesforce
    plugin: github.com/vrsky/connectors/salesforce@v2.0.0
    config:
      instanceUrl: ${SECRET:salesforce_url}
```

### Logic Plugins

Configured inline or stored as separate files:

```yaml
integration:
  id: invoice-sync
  converter:
    type: wasm
    source: https://artifact-store.vrsky.io/converters/json-to-xml/v1.0.wasm
    config:
      timeout: 5s
      memoryLimit: 128Mi
  filter:
    type: wasm
    inline: |
      export function process(message: Message): Message {
        if (message.amount > 1000) {
          return message;
        }
      }
    config:
      timeout: 2s
      memoryLimit: 64Mi
```

## Versioning & Compatibility

### Plugin Versioning

Plugins follow semantic versioning: `MAJOR.MINOR.PATCH`

**Version Compatibility Rules:**
- Same MAJOR version: Backward compatible (new MINOR/PATCH versions can be used)
- Different MAJOR version: Breaking changes require explicit integration update
- Integration specifies allowed version ranges: `>=1.0.0,<2.0.0`

### Breaking Changes

When a plugin changes incompatibly:

1. **Detection**: Control Plane compares plugin version with integration requirements
2. **Logging**: Breaking changes are logged with:
   - Old version
   - New version
   - Integration ID affected
   - Timestamp
   - Action taken (blocked, warned, updated)
3. **Resolution**: 
   - If auto-upgrade enabled: Update silently and log
   - If strict: Block integration, alert platform operator
   - If warn: Log warning, continue with old version

### Protocol Versioning (Connection Nodes)

Connection plugins declare protocol version:

```go
type PluginManifest struct {
    ID              string
    Version         string
    ProtocolVersion int
    Capabilities    []string
    Metadata        map[string]string
}
```

**Protocol Version Mismatches:**
- If plugin's ProtocolVersion > Host's ProtocolVersion: **Block** (plugin too new)
- If plugin's ProtocolVersion < Host's ProtocolVersion: **Warn** (plugin outdated)
- If they match: **Allow**

## Resource Management

### Connection Nodes (RPC)

Resources controlled at K3s level:

```yaml
containers:
  - name: http-connector
    resources:
      limits:
        cpu: "500m"
        memory: "256Mi"
      requests:
        cpu: "100m"
        memory: "64Mi"
```

### Logic Nodes (WASM)

Resources configured per plugin instance:

```yaml
converter:
  timeout: 5s              # Max execution time
  memoryLimit: 128Mi       # WASM instance memory
  cpuWeight: 1.0           # Relative CPU scheduling
```

**Timeout Enforcement:**
- WASM execution interrupted if exceeds timeout
- Timeout errors logged and message sent to dead-letter queue
- Alert platform operator if repeated timeouts

**Memory Enforcement:**
- WASM runtime prevents allocation exceeding memoryLimit
- Allocation failure → RaiseError() called
- Plugin must handle gracefully or timeout occurs

## Plugin Registry & Discovery

**Storage**: Object storage (S3) + metadata database (PostgreSQL)

**Plugin Catalog Structure**:
```
s3://vrsky-plugins/
├── connectors/
│   ├── http/
│   │   ├── v1.0.0/
│   │   │   ├── manifest.json
│   │   │   ├── binary.so
│   │   │   └── checksum.sha256
│   │   └── v1.1.0/
│   │       └── ...
│   └── salesforce/
├── converters/
│   ├── json-to-xml/
│   │   ├── v1.0.wasm
│   │   └── manifest.json
│   └── csv-parser/
└── filters/
    ├── conditional-router/
    └── ...
```

**Discovery Query**:
```sql
SELECT id, version, checksum, downloadUrl 
FROM plugins 
WHERE type='connector' AND name='http' AND version LIKE '1.%'
ORDER BY version DESC
```

## Error Handling

### Connection Node Errors

RPC errors are caught and logged:

```go
type ConnectionError struct {
    PluginID   string
    ErrorCode  string
    Message    string
    Recoverable bool // true = retry, false = dead-letter
}
```

**Retry Policy** (configurable per integration):
- Max retries: 3 (default)
- Backoff: Exponential (1s, 2s, 4s)
- Timeout per attempt: 30s

### Logic Node Errors

WASM runtime errors trigger graceful degradation:

```
1. WASM execution fails (timeout or error)
2. Error logged with context (Customer-ID, Integration-ID)
3. Message routed to dead-letter queue
4. Alert if repeated errors from same WASM instance
5. Consider restarting WASM instance or triggering circuit breaker
```

## Performance Characteristics

### Latency

Expected per-message latency:

| Node Type | Operation | Latency | Notes |
|-----------|-----------|---------|-------|
| Connection (RPC) | Single RPC call | 1-5ms | Over localhost |
| Converter (WASM) | Data transformation | 0.5-2ms | Depends on complexity |
| Filter (WASM) | Conditional logic | 0.5-1ms | Usually simple checks |
| **Total Pipeline** | Full message flow | 5-15ms | For typical integration |

### Throughput

- **Single Service Node Pod**: ~1,000-5,000 messages/sec (depends on plugin complexity)
- **Scaled Deployment (10 pods)**: ~10,000-50,000 messages/sec
- **Bottleneck**: Usually network I/O to external systems, not plugin processing

## Next Steps

1. **Research Task #5**: Finalize Connection Node RPC interface specification
2. **Research Task #6**: Finalize WASM runtime and host function API
3. **Research Task #7**: Security audit of sandboxing mechanisms
4. **Proof of Concept**: Build sample HTTP connector (RPC) + sample converter (WASM)
