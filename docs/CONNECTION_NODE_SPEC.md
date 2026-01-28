# Connection Node RPC Specification

**Document Version**: 1.0  
**Status**: Design Specification  
**Date**: January 28, 2026

## Overview

Connection Nodes are RPC-based plugins that handle network protocol communication. They are responsible for:
- **Incoming Connections**: Receiving data from external systems
- **Outgoing Connections**: Sending data to external systems

Connection plugins run as separate subprocesses communicating via gRPC, providing natural process isolation and resource containment.

## Connection Node Interface

All Connection plugins must implement the following Go interface:

```go
package connector

import (
    "context"
    "time"
)

// Message represents a data message flowing through the pipeline
type Message struct {
    // ID: Unique message identifier
    ID string
    
    // Payload: The actual data (any format: JSON, XML, binary, etc.)
    Payload []byte
    
    // ContentType: MIME type of payload (e.g., "application/json")
    ContentType string
    
    // Metadata: Message metadata
    Metadata MessageMetadata
    
    // Headers: Protocol-specific headers
    Headers map[string]string
    
    // Timestamp: When message was created
    Timestamp time.Time
}

// MessageMetadata: Multi-tenancy and context information
type MessageMetadata struct {
    // CustomerID: Tenant customer identifier
    CustomerID string
    
    // TenantID: VRSky tenant (shared or dedicated)
    TenantID string
    
    // IntegrationID: Integration this message belongs to
    IntegrationID string
    
    // Source: Origin of message (connector name)
    Source string
    
    // Destination: Target of message (connector name)
    Destination string
    
    // CorrelationID: For tracing messages through the pipeline
    CorrelationID string
    
    // TraceID: Distributed tracing ID
    TraceID string
}

// ConnectionConfig: Configuration for the connection
type ConnectionConfig struct {
    // ID: Unique identifier for this connection instance
    ID string
    
    // Name: Human-readable name
    Name string
    
    // Type: Connection type (http, mqtt, ftp, salesforce, etc.)
    Type string
    
    // Settings: Connection-specific settings (varies by connector)
    Settings map[string]interface{}
    
    // Credentials: Sensitive credentials (should use secure secrets manager)
    Credentials map[string]string
    
    // Timeout: Default timeout for operations
    Timeout time.Duration
    
    // RetryPolicy: Retry configuration
    RetryPolicy RetryPolicy
}

// RetryPolicy: Configuration for retries
type RetryPolicy struct {
    // MaxRetries: Maximum number of retry attempts
    MaxRetries int
    
    // BackoffMultiplier: Exponential backoff multiplier (default: 2.0)
    BackoffMultiplier float64
    
    // InitialBackoff: Initial backoff duration (default: 1s)
    InitialBackoff time.Duration
    
    // MaxBackoff: Maximum backoff duration (default: 30s)
    MaxBackoff time.Duration
}

// ConnectionNode: Main interface for connection plugins
type ConnectionNode interface {
    // Validate: Validate the configuration and connectivity
    // Called when plugin is first loaded and when config changes
    Validate(ctx context.Context, config ConnectionConfig) error
    
    // Connect: Establish connection to external system
    // Called once at startup
    Connect(ctx context.Context, config ConnectionConfig) error
    
    // Disconnect: Close connection gracefully
    // Called on shutdown
    Disconnect(ctx context.Context) error
    
    // Send: Send a message to external system (for Outgoing connections)
    // Returns message ID or error
    Send(ctx context.Context, msg Message) (messageID string, err error)
    
    // Receive: Receive messages from external system (for Incoming connections)
    // Blocks until message available or context cancelled
    Receive(ctx context.Context) (Message, error)
    
    // Health: Check health status of connection
    // Returns nil if healthy, error otherwise
    Health(ctx context.Context) error
    
    // Metadata: Return plugin metadata and capabilities
    Metadata() PluginMetadata
}

// PluginMetadata: Information about the plugin
type PluginMetadata struct {
    // ID: Unique plugin identifier
    ID string
    
    // Name: Human-readable name
    Name string
    
    // Version: Semantic version
    Version string
    
    // ProtocolVersion: VRSky protocol version (for compatibility checks)
    ProtocolVersion int
    
    // SupportedTypes: Connection types this plugin supports
    // e.g., ["http-webhook", "http-rest"]
    SupportedTypes []string
    
    // Description: Plugin description
    Description string
    
    // Author: Plugin author/organization
    Author string
    
    // Documentation: Link to plugin documentation
    Documentation string
    
    // Capabilities: List of capabilities (see Capabilities section)
    Capabilities []string
    
    // ConfigSchema: JSON Schema for configuration
    ConfigSchema string
}

// Capabilities: What the connection can do
type Capabilities string

const (
    // CapabilityBiDirectional: Connection can both send and receive
    CapabilityBiDirectional Capabilities = "bidirectional"
    
    // CapabilityRetryable: Connection supports retries
    CapabilityRetryable Capabilities = "retryable"
    
    // CapabilityBatching: Connection supports batch operations
    CapabilityBatching Capabilities = "batching"
    
    // CapabilityStreaming: Connection supports streaming
    CapabilityStreaming Capabilities = "streaming"
    
    // CapabilityTLS: Connection supports TLS
    CapabilityTLS Capabilities = "tls"
    
    // CapabilityMTLS: Connection supports mutual TLS
    CapabilityMTLS Capabilities = "mtls"
)
```

## Plugin Entry Point

Every Connection plugin must expose a `main` package with a `Serve` function:

```go
package main

import (
    "github.com/ValueRetail/vrsky/pkg/connector"
    "github.com/hashicorp/go-plugin"
)

// MyHTTPConnector: Implementation of ConnectionNode for HTTP
type MyHTTPConnector struct {
    // ... internal state
}

// Serve: Called by VRSky to start the plugin
func Serve() {
    plugin.Serve(&plugin.ServeConfig{
        HandshakeConfig: connector.Handshake,
        Plugins: map[string]plugin.Plugin{
            "connection": &connector.ConnectionNodePlugin{
                Impl: &MyHTTPConnector{},
            },
        },
    })
}

func main() {
    Serve()
}
```

Build as a plugin:
```bash
go build -o http-connector.so ./cmd/http-connector
```

## RPC Call Flow

### Incoming Connection Example

```
1. Control Plane discovers HTTP connector plugin
2. Spawns subprocess: ./http-connector
3. Establishes gRPC connection to subprocess
4. Calls ConnectionNode.Validate(config)
   ✓ Config is valid
5. Calls ConnectionNode.Connect(config)
   ✓ HTTP server started on :8080
6. Integration starts, calls ConnectionNode.Receive(ctx)
   ✓ Blocks waiting for webhook POST
7. External system sends POST /webhooks/orders
   ✓ Plugin receives, creates Message, returns
8. Message flows through converter → filter → outgoing
9. Repeat step 6 for each message
```

### Outgoing Connection Example

```
1. Control Plane discovers Salesforce connector plugin
2. Spawns subprocess: ./salesforce-connector
3. Establishes gRPC connection to subprocess
4. Calls ConnectionNode.Validate(config)
   ✓ Config is valid (credentials work)
5. Calls ConnectionNode.Connect(config)
   ✓ OAuth token obtained
6. Integration receives message from filter node
7. Calls ConnectionNode.Send(msg)
   ✓ Plugin formats as SOAP, sends to Salesforce API
   ✓ Returns success or error
8. If error and retryable, sleep then retry
9. Repeat for each message
```

## Error Handling

### Plugin Errors

Connection plugins should return typed errors:

```go
package connector

import "fmt"

// PluginError: Base error type
type PluginError struct {
    Code       string // "INVALID_CONFIG", "CONNECTION_FAILED", "SEND_FAILED"
    Message    string
    Recoverable bool // true = retry, false = dead-letter
}

func (e *PluginError) Error() string {
    return fmt.Sprintf("[%s] %s (recoverable=%v)", e.Code, e.Message, e.Recoverable)
}

// Example errors
func NewConnectionError(msg string) *PluginError {
    return &PluginError{
        Code:        "CONNECTION_FAILED",
        Message:     msg,
        Recoverable: true, // Can retry connection
    }
}

func NewInvalidConfigError(msg string) *PluginError {
    return &PluginError{
        Code:        "INVALID_CONFIG",
        Message:     msg,
        Recoverable: false, // Cannot recover from bad config
    }
}

func NewSendError(msg string, recoverable bool) *PluginError {
    return &PluginError{
        Code:        "SEND_FAILED",
        Message:     msg,
        Recoverable: recoverable,
    }
}
```

### Control Plane Error Handling

When Connection plugin returns error:

```
1. Check error.Recoverable flag
2. If Recoverable = true:
   - Apply RetryPolicy (exponential backoff)
   - Retry up to MaxRetries times
   - If all retries fail, send to dead-letter queue
3. If Recoverable = false:
   - Send to dead-letter queue immediately
   - Alert platform operator
   - Stop attempting to process this message
```

## Security & Sandboxing

### What Connection Plugins CAN Access

1. **Configuration**: Only their own configuration (passed via Connect)
2. **Customer Context**: CustomerID from message metadata (read-only)
3. **Logging**: StandardLibrary log package (logs sent to host)
4. **Host Functions**: Limited set of helper functions (via gRPC)

### What Connection Plugins CANNOT Access

1. **Other Customers' Data**: No cross-tenant data leakage
2. **Host Filesystem**: No direct file I/O
3. **Other Processes**: Cannot inspect or modify other plugins
4. **Arbitrary Network**: Only to configured endpoints
5. **Host Memory**: Process isolation ensures this

### Security Best Practices

```go
// ✅ GOOD: Use credentials passed in config
func (c *MyConnector) Connect(ctx context.Context, config ConnectionConfig) error {
    apiKey := config.Credentials["api_key"]
    // Use apiKey to authenticate
}

// ❌ BAD: Don't hardcode credentials
func (c *MyConnector) Connect(ctx context.Context, config ConnectionConfig) error {
    apiKey := "hardcoded-secret-key" // SECURITY ISSUE
}

// ✅ GOOD: Log important events without exposing secrets
func (c *MyConnector) Send(ctx context.Context, msg Message) (string, error) {
    log.Printf("Sending message for customer %s", msg.Metadata.CustomerID)
    // Send message...
}

// ❌ BAD: Don't log sensitive data
func (c *MyConnector) Send(ctx context.Context, msg Message) (string, error) {
    log.Printf("Sending payload: %s", string(msg.Payload)) // May contain secrets
}
```

## Host Functions Available to Plugins

Plugins can call back to the host for common operations:

```go
// HostFunctions: Interface for host capabilities
type HostFunctions interface {
    // GetSecret: Retrieve a secret by name from secrets manager
    GetSecret(ctx context.Context, name string) (string, error)
    
    // Log: Send log message to host
    Log(ctx context.Context, level string, message string) error
    
    // RecordMetric: Record a metric (for monitoring)
    RecordMetric(ctx context.Context, name string, value float64, tags map[string]string) error
    
    // GetConfiguration: Get plugin configuration
    GetConfiguration(ctx context.Context) (ConnectionConfig, error)
}
```

## Resource Limits

Connection plugins run with K3s resource limits:

```yaml
# Applied by Control Plane when spawning plugin subprocess
resources:
  requests:
    cpu: "100m"
    memory: "64Mi"
  limits:
    cpu: "500m"
    memory: "256Mi"
```

**Plugin responsibilities:**
- Respect timeout in ConnectionConfig
- Implement Health() to detect problems
- Gracefully handle resource exhaustion
- Log warnings when approaching limits

## Testing Your Plugin

```go
package main

import (
    "context"
    "testing"
    
    "github.com/ValueRetail/vrsky/pkg/connector"
)

func TestMyConnectorValidate(t *testing.T) {
    c := &MyHTTPConnector{}
    config := connector.ConnectionConfig{
        ID:   "test-http",
        Type: "http-webhook",
        Settings: map[string]interface{}{
            "port": 8080,
        },
    }
    
    err := c.Validate(context.Background(), config)
    if err != nil {
        t.Fatalf("Validate failed: %v", err)
    }
}

func TestMyConnectorConnect(t *testing.T) {
    c := &MyHTTPConnector{}
    config := connector.ConnectionConfig{
        ID:   "test-http",
        Type: "http-webhook",
    }
    
    err := c.Connect(context.Background(), config)
    if err != nil {
        t.Fatalf("Connect failed: %v", err)
    }
    defer c.Disconnect(context.Background())
}
```

## Plugin Development Checklist

- [ ] Implement all ConnectionNode interface methods
- [ ] Handle Customer-ID segregation correctly
- [ ] Use credentials from config, not hardcoded
- [ ] Implement proper error types (Recoverable flag)
- [ ] Add logging for debugging
- [ ] Implement Health() for monitoring
- [ ] Handle context cancellation (timeouts)
- [ ] Test with multiple concurrent messages
- [ ] Document configuration schema
- [ ] Test graceful shutdown (Disconnect)
- [ ] Benchmark performance characteristics
- [ ] Add security review checklist

## Example: HTTP Webhook Connector

See `examples/connectors/http-webhook/` in the VRSky repository for a complete working example.
