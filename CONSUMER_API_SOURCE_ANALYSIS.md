# VRSky Consumer Architecture & API Source Integration Analysis

**Analysis Date**: March 16, 2026  
**Codebase**: `/home/ludvik/vrsky`  
**Scope**: Consumer implementation, configuration structure, and API source extension requirements

---

## 1. Current Consumer Types & Implementation

### 1.1 Existing Consumer Implementations

VRSky currently supports **3 consumer types** (sources):

| Consumer Type | Implementation | Location | Purpose |
|---------------|-----------------|----------|---------|
| **HTTP** | HTTPInput | `src/pkg/io/http_input.go` | Webhook receiver - listens on port, accepts POST requests |
| **File** | FileConsumer | `src/pkg/io/file_input.go` | Directory watcher - monitors for new files, processes them |
| **PostgreSQL CDC** | PostgresInput | `src/pkg/io/postgres_input.go` | Change Data Capture - polls database for table changes |

### 1.2 Component Interface Hierarchy

All consumers implement VRSky's standard **Component interface** (component.go):

```go
type Component interface {
    Name() string                           // Human-readable name
    Type() ComponentType                    // Returns: "consumer", "producer", etc.
    Version() string                        // Version info
    Start(ctx context.Context) error        // Initialize and start
    Stop(ctx context.Context) error         // Graceful shutdown
    Health() HealthStatus                   // Report health status
}
```

Additionally, consumers also implement the **Input interface**:

```go
type Input interface {
    Start(ctx context.Context) error        // Connect to source
    Read(ctx context.Context) *envelope.Envelope  // Get next message
    Close() error                           // Shutdown
}
```

### 1.3 Factory Pattern

Consumers are instantiated via **io/factory.go**:

```go
func NewInput(inputType string, configJSON json.RawMessage) (component.Input, error) {
    switch inputType {
    case "http":
        return NewHTTPInput(configJSON)
    case "nats":
        return NewNATSInput(configJSON)
    case "file":
        return NewFileConsumer(logger)
    default:
        return nil, fmt.Errorf("unknown input type: %s", inputType)
    }
}
```

---

## 2. Current Consumer Configuration Structure

### 2.1 Backend Configuration Format (Go)

Each consumer has a specific configuration struct in JSON format:

#### HTTP Consumer Config
```go
type HTTPInputConfig struct {
    Port string `json:"port"`  // e.g., "8000"
}

// Usage:
configJSON := []byte(`{"port":"8000"}`)
httpInput, err := NewHTTPInput(configJSON)
```

**Behavior**:
- Listens on `http://0.0.0.0:{port}/webhook`
- Accepts only POST requests
- Returns 202 Accepted (fire-and-forget)
- Extracts client IP from X-Forwarded-For, X-Real-IP, or RemoteAddr

#### File Consumer Config (Environment Variables)
```go
// No structured JSON config - uses environment variables
FILE_INPUT_DIR                    // Directory to watch
FILE_INPUT_PATTERN                // Glob pattern (default: "*")
FILE_INPUT_POLL_INTERVAL          // Poll frequency
FILE_INPUT_ARCHIVE_DIR            // Archive processed files
FILE_INPUT_ERROR_DIR              // Move failed files
FILE_INPUT_DELETE_AFTER_PROCESSING // Delete or archive
FILE_INPUT_MAX_RETRIES            // Retry failed files
FILE_INPUT_RETRY_BACKOFF_MS       // Exponential backoff
```

#### PostgreSQL CDC Consumer Config (Environment Variables)
```go
// Connection
POSTGRES_INPUT_HOST               // Database host
POSTGRES_INPUT_PORT               // Database port (default: 5432)
POSTGRES_INPUT_USER               // Database user
POSTGRES_INPUT_PASSWORD           // Database password (required)
POSTGRES_INPUT_DATABASE           // Target database (required)

// CDC Setup
POSTGRES_INPUT_REPLICATION_SLOT   // Slot name (default: vrsky_slot)
POSTGRES_INPUT_PUBLICATION        // Publication name
POSTGRES_INPUT_TABLES             // Comma-separated table whitelist

// Processing
POSTGRES_INPUT_BATCH_SIZE         // Batch size (default: 100)
POSTGRES_INPUT_BATCH_TIMEOUT_MS   // Batch timeout

// NATS Output
POSTGRES_INPUT_NATS_URL           // NATS server URL
POSTGRES_INPUT_SUBJECT            // NATS subject for changes

// Error Handling
POSTGRES_INPUT_DLQ_ENABLED        // Enable dead-letter queue
POSTGRES_INPUT_DLQ_SUBJECT        // DLQ subject
POSTGRES_INPUT_DLQ_MAX_RETRIES    // Max retries before DLQ
```

### 2.2 Frontend Configuration (TypeScript)

The UI uses type-safe configurations defined in `ui/src/types/models.ts`:

```typescript
export type SourceType = 'http' | 'file' | 'database'

export interface HttpSourceConfig {
  type: 'http'
  url: string
  method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  headers?: Record<string, string>
  auth?: {
    type: 'bearer' | 'basic'
    credentials: string
  }
  timeout?: number
  retry?: {
    max_attempts: number
    backoff_ms: number
  }
}

export interface FileSourceConfig {
  type: 'file'
  path: string
  format: 'json' | 'csv' | 'xml'
  encoding?: string
  watch?: boolean
  poll_interval_ms?: number
}

export interface DatabaseSourceConfig {
  type: 'database'
  connection_string: string
  query: string
  polling_interval_ms?: number
}

export type SourceConfig = 
  | HttpSourceConfig 
  | FileSourceConfig 
  | DatabaseSourceConfig
```

### 2.3 PropertyEditor Component

The React PropertyEditor (`ui/src/components/Pipeline/PropertyEditor.tsx`) currently handles consumer configuration editing:

```typescript
// Current consumer type options:
{
  { value: 'http', label: 'HTTP Webhook' },
  { value: 'file', label: 'File Watcher' },
  { value: 'database', label: 'Database CDC' }
}

// HTTP consumer config section:
{config.type === 'http' && (
  <>
    <StyledInput label="Webhook URL" ... />
    <StyledSelect label="HTTP Method" ... />
  </>
)}

// File consumer config section:
{config.type === 'file' && (
  <StyledInput label="Watch Directory" ... />
)}
```

**Current UI Flow**:
1. User selects source type from dropdown
2. Type-specific fields appear
3. User fills in configuration
4. "Save Configuration" button updates node data

---

## 3. PropertyEditor Consumer Config Handling

### 3.1 Current Implementation Details

Located in: `ui/src/components/Pipeline/PropertyEditor.tsx` (lines 196-257)

**Key Features**:
- Dynamically renders form fields based on `config.type`
- Uses styled components for consistent UX
- Stores config as nested object: `{ type: string, http?: {...}, file?: {...} }`
- Updates parent node on save

**Data Flow**:
```
PropertyEditor
  ↓ (receives)
Node { data: { label, config: {type, http/file/database} } }
  ↓ (renders)
Type-specific inputs
  ↓ (user edits)
setConfig() updates local state
  ↓ (on save)
onUpdate(config) → parent component stores
```

### 3.2 Consumer Config Structure Example

```typescript
// HTTP Consumer Config Object
{
  type: 'http',
  http: {
    url: 'https://example.com/webhook',
    method: 'POST'
  }
}

// File Consumer Config Object
{
  type: 'file',
  file: {
    path: '/tmp/input'
  }
}
```

---

## 4. API Source Support Requirements

### 4.1 What an "API Source" Consumer Needs

An API source consumer would **pull data from external APIs** rather than receive webhooks. This requires:

#### Core Requirements
- **Polling** - Periodically call API endpoints
- **Authentication** - Support multiple auth schemes (bearer, API key, basic auth)
- **Request Configuration** - Method, headers, query params, body templates
- **Response Parsing** - Extract data from various response formats
- **Error Handling** - Retries, backoff strategies, DLQ for failed requests
- **State Management** - Track pagination, cursors, last-seen timestamps
- **Rate Limiting** - Respect API rate limits, implement backoff
- **Message Batching** - Combine API responses into batches

#### Optional Features
- OAuth 2.0 token refresh
- Webhook callbacks (inverse - register webhook with API)
- GraphQL queries
- Message transformation (API response → envelope)

### 4.2 API Source Configuration Fields

**Required**:
```typescript
interface ApiSourceConfig {
  type: 'api'
  url: string                           // Base API endpoint
  method: 'GET' | 'POST'               // HTTP method
  
  // Authentication
  auth: {
    type: 'none' | 'bearer' | 'api_key' | 'basic' | 'oauth2'
    credentials?: string                 // bearer token or API key
    headerName?: string                  // custom header (for API key)
    username?: string                    // for basic auth
    password?: string
    // OAuth2 config
    tokenUrl?: string
    clientId?: string
    clientSecret?: string
  }
}
```

**Advanced**:
```typescript
interface ApiSourceConfig {
  // Polling
  pollInterval: number                  // ms between requests
  pollOffset?: number                   // initial delay
  
  // Request customization
  headers?: Record<string, string>
  queryParams?: Record<string, string>
  body?: string | Record<string, unknown>
  
  // Response handling
  dataPath?: string                     // JSONPath to data (e.g., "data.items")
  pageSize?: number
  pagination?: {
    type: 'offset' | 'cursor' | 'link'
    pageParam?: string                  // "page", "skip", etc.
    limitParam?: string
    cursorField?: string
  }
  
  // Error handling
  retry?: {
    maxAttempts: number
    backoffMs: number
    backoffMultiplier: number
  }
  
  // Rate limiting
  rateLimit?: {
    requestsPerSecond: number
  }
  
  // Batching
  batchSize: number
  batchTimeoutMs: number
}
```

---

## 5. Implementing API Source Consumer

### 5.1 Backend Implementation (Go)

**File**: `src/pkg/io/api_input.go`

```go
package io

import (
    "context"
    "encoding/json"
    "net/http"
    "time"
)

// APIInputConfig holds API source configuration
type APIInputConfig struct {
    URL            string                 `json:"url"`
    Method         string                 `json:"method"`
    Headers        map[string]string      `json:"headers,omitempty"`
    QueryParams    map[string]string      `json:"query_params,omitempty"`
    Auth           AuthConfig             `json:"auth"`
    PollInterval   time.Duration          `json:"poll_interval"`
    DataPath       string                 `json:"data_path,omitempty"`
    Pagination     PaginationConfig       `json:"pagination,omitempty"`
    Retry          RetryConfig            `json:"retry,omitempty"`
    BatchSize      int                    `json:"batch_size"`
    BatchTimeout   time.Duration          `json:"batch_timeout"`
}

type AuthConfig struct {
    Type            string `json:"type"` // "none", "bearer", "api_key", "basic"
    Credentials     string `json:"credentials,omitempty"`
    HeaderName      string `json:"header_name,omitempty"`
    Username        string `json:"username,omitempty"`
    Password        string `json:"password,omitempty"`
}

type PaginationConfig struct {
    Type            string `json:"type"` // "offset", "cursor", "link"
    PageParam       string `json:"page_param,omitempty"`
    LimitParam      string `json:"limit_param,omitempty"`
    CursorField     string `json:"cursor_field,omitempty"`
}

// APIInput implements Input interface for API polling
type APIInput struct {
    config          APIInputConfig
    httpClient      *http.Client
    messages        chan *envelope.Envelope
    mu              sync.Mutex
    closed          bool
    ctx             context.Context
    cancel          context.CancelFunc
    
    // State tracking
    lastCursor      string
    lastOffset      int
    lastTimestamp   time.Time
    failedRequests  int
}

// NewAPIInput creates new API input from config
func NewAPIInput(configJSON json.RawMessage) (*APIInput, error) {
    // ... implementation
}

// Start begins polling the API
func (a *APIInput) Start(ctx context.Context) error {
    // ... implementation
}

// Read returns next message from API
func (a *APIInput) Read(ctx context.Context) (*envelope.Envelope, error) {
    // ... implementation
}

// Close shuts down poller
func (a *APIInput) Close() error {
    // ... implementation
}
```

**Integration with factory**:

```go
// In factory.go, add to NewInput():
case "api":
    return NewAPIInput(configJSON)
```

### 5.2 Frontend Implementation (TypeScript)

**File**: `ui/src/types/models.ts` - Add:

```typescript
export interface ApiSourceConfig {
  type: 'api'
  url: string
  method: 'GET' | 'POST'
  headers?: Record<string, string>
  queryParams?: Record<string, string>
  auth?: {
    type: 'none' | 'bearer' | 'api_key' | 'basic'
    credentials?: string
    headerName?: string
    username?: string
    password?: string
  }
  pollInterval: number
  dataPath?: string
  batchSize?: number
  batchTimeoutMs?: number
  retry?: {
    maxAttempts: number
    backoffMs: number
  }
}

export type SourceConfig = 
  | HttpSourceConfig 
  | FileSourceConfig 
  | DatabaseSourceConfig 
  | ApiSourceConfig  // ADD THIS
```

**UI Component Update**: `ui/src/components/Pipeline/PropertyEditor.tsx`

```typescript
// Add to source type options:
{
  { value: 'http', label: 'HTTP Webhook' },
  { value: 'file', label: 'File Watcher' },
  { value: 'database', label: 'Database CDC' },
  { value: 'api', label: 'API Source' }  // NEW
}

// Add new config section:
{config.type === 'api' && (
  <>
    <StyledInput
      label="API URL"
      placeholder="https://api.example.com/data"
      value={(config.api as any)?.url || ''}
      onChange={(value) =>
        setConfig({
          ...config,
          api: { ...(config.api as any), url: value },
        })
      }
    />
    <StyledSelect
      label="HTTP Method"
      value={(config.api as any)?.method || 'GET'}
      onChange={(value) =>
        setConfig({
          ...config,
          api: { ...(config.api as any), method: value },
        })
      }
      options={[
        { value: 'GET', label: 'GET' },
        { value: 'POST', label: 'POST' },
      ]}
    />
    <StyledSelect
      label="Authentication"
      value={(config.api as any)?.auth?.type || 'none'}
      onChange={(value) =>
        setConfig({
          ...config,
          api: {
            ...(config.api as any),
            auth: { ...(config.api as any)?.auth, type: value },
          },
        })
      }
      options={[
        { value: 'none', label: 'None' },
        { value: 'bearer', label: 'Bearer Token' },
        { value: 'api_key', label: 'API Key' },
        { value: 'basic', label: 'Basic Auth' },
      ]}
    />
    {(config.api as any)?.auth?.type === 'bearer' && (
      <StyledInput
        label="Bearer Token"
        type="password"
        value={(config.api as any)?.auth?.credentials || ''}
        onChange={(value) =>
          setConfig({
            ...config,
            api: {
              ...(config.api as any),
              auth: { ...(config.api as any)?.auth, credentials: value },
            },
          })
        }
      />
    )}
    <StyledInput
      label="Poll Interval (ms)"
      type="number"
      value={(config.api as any)?.pollInterval?.toString() || '5000'}
      onChange={(value) =>
        setConfig({
          ...config,
          api: { ...(config.api as any), pollInterval: parseInt(value) },
        })
      }
    />
  </>
)}
```

### 5.3 Configuration Flow

```
User selects "API Source" in PropertyEditor
  ↓
UI renders API-specific fields (URL, auth, poll interval, etc.)
  ↓
User fills in configuration
  ↓
Clicks "Save Configuration"
  ↓
Config object sent to backend: { type: 'api', api: {...} }
  ↓
Backend routes to: NewInput("api", configJSON)
  ↓
factory.go creates: NewAPIInput(configJSON)
  ↓
APIInput.Start() begins polling
  ↓
APIInput.Read() returns envelopes with API response data
  ↓
Messages flow to NATS/downstream consumers
```

---

## 6. Detailed Extension Checklist for API Source

### 6.1 Backend Changes

**Files to create/modify**:

- [ ] `src/pkg/io/api_input.go` - Main API input implementation
- [ ] `src/pkg/io/api_input_test.go` - Unit tests
- [ ] `src/pkg/io/factory.go` - Add "api" case to NewInput()
- [ ] `src/pkg/io/http_client.go` (optional) - Shared HTTP client utilities

**Key implementation considerations**:

```go
// Polling loop structure
func (a *APIInput) startPolling() {
    ticker := time.NewTicker(a.config.PollInterval)
    defer ticker.Stop()
    
    for {
        select {
        case <-a.ctx.Done():
            return
        case <-ticker.C:
            // 1. Make HTTP request with auth
            // 2. Extract data using dataPath (JSONPath)
            // 3. Handle pagination (track cursor/offset)
            // 4. Batch responses
            // 5. Send to message channel
            // 6. Retry on failure with backoff
        }
    }
}

// Authentication implementation
func (a *APIInput) addAuthHeaders(req *http.Request) {
    switch a.config.Auth.Type {
    case "bearer":
        req.Header.Set("Authorization", "Bearer "+a.config.Auth.Credentials)
    case "api_key":
        headerName := a.config.Auth.HeaderName
        if headerName == "" {
            headerName = "X-API-Key"
        }
        req.Header.Set(headerName, a.config.Auth.Credentials)
    case "basic":
        req.SetBasicAuth(a.config.Auth.Username, a.config.Auth.Password)
    }
}
```

### 6.2 Frontend Changes

**Files to modify**:

- [ ] `ui/src/types/models.ts` - Add ApiSourceConfig interface
- [ ] `ui/src/components/Pipeline/PropertyEditor.tsx` - Add API config UI section
- [ ] `ui/src/utils/typeGuards.ts` - Add isApiSourceConfig() type guard
- [ ] `ui/src/utils/validation.ts` - Add API config validation

**Validation logic needed**:

```typescript
// Validate API config
if (config.type === 'api') {
  // URL is required
  if (!config.api?.url) {
    errors.push("API URL is required")
  }
  
  // If bearer auth, token required
  if (config.api?.auth?.type === 'bearer' && !config.api?.auth?.credentials) {
    errors.push("Bearer token required")
  }
  
  // Poll interval must be positive
  if (config.api?.pollInterval && config.api.pollInterval <= 0) {
    errors.push("Poll interval must be > 0")
  }
}
```

### 6.3 Testing Requirements

**Unit tests needed**:

```go
// api_input_test.go
TestAPIInput_Start_Success          // Happy path
TestAPIInput_Authentication_Bearer  // Bearer token
TestAPIInput_Authentication_ApiKey  // API key header
TestAPIInput_Authentication_Basic   // Basic auth
TestAPIInput_Pagination_Offset      // Offset pagination
TestAPIInput_Pagination_Cursor      // Cursor pagination
TestAPIInput_DataPath_Extraction    // JSONPath extraction
TestAPIInput_Retry_Backoff          // Exponential backoff
TestAPIInput_Batching               // Batch size/timeout
TestAPIInput_RateLimit              // Rate limiting
TestAPIInput_Close_Graceful         // Graceful shutdown
TestAPIInput_Context_Cancellation   // Context handling
```

---

## 7. Additional Considerations

### 7.1 Rate Limiting & Throttling

```go
// Simple token bucket implementation
type RateLimiter struct {
    requestsPerSecond float64
    lastRequest       time.Time
    mu                sync.Mutex
}

func (r *RateLimiter) Wait() {
    r.mu.Lock()
    defer r.mu.Unlock()
    
    now := time.Now()
    minInterval := time.Duration(float64(time.Second) / r.requestsPerSecond)
    
    if elapsed := now.Sub(r.lastRequest); elapsed < minInterval {
        time.Sleep(minInterval - elapsed)
    }
    
    r.lastRequest = time.Now()
}
```

### 7.2 State Persistence

For stateful polling (cursors, offsets):

```go
// Store in NATS KV store
type StateStore interface {
    GetCursor(ctx context.Context, key string) (string, error)
    SetCursor(ctx context.Context, key string, value string) error
}

// Usage in poller
cursor, _ := a.stateStore.GetCursor(ctx, "api_cursor")
// ... make request with cursor ...
a.stateStore.SetCursor(ctx, "api_cursor", newCursor)
```

### 7.3 Monitoring & Observability

```go
// Prometheus metrics
type APIMetrics struct {
    RequestsTotal    prometheus.Counter
    RequestErrors    prometheus.Counter
    RequestLatency   prometheus.Histogram
    MessagesProduced prometheus.Counter
}
```

---

## 8. Summary Matrix

| Aspect | Current | API Source | Notes |
|--------|---------|-----------|-------|
| **Config Type** | JSON struct | JSON struct | Same pattern |
| **UI Fields** | 2-3 fields | 8-15 fields | More options |
| **Authentication** | None | 4 types | Bearer, API key, basic, OAuth |
| **Rate Limiting** | None | Yes | Essential for APIs |
| **State Tracking** | None | Needed | Cursors, pagination |
| **Batching** | Yes | Yes | Already supported |
| **Error Handling** | Basic | Advanced | Retries, DLQ, backoff |
| **Polling** | Not applicable | Required | Poll interval config |
| **Factory Pattern** | Used | Used | Consistent |
| **Testing** | Extensive | Needed | 10+ test cases |

---

## 9. Reference Files

**Backend**:
- Consumer interface: `src/pkg/component/io.go` (37 lines)
- HTTP implementation: `src/pkg/io/http_input.go` (194 lines)
- File implementation: `src/pkg/io/file_input.go` (694 lines)
- PostgreSQL implementation: `src/pkg/io/postgres_input.go` (919 lines)
- Factory pattern: `src/pkg/io/factory.go` (36 lines)

**Frontend**:
- Pipeline types: `ui/src/types/pipeline.ts` (22 lines)
- Models/configs: `ui/src/types/models.ts` (217 lines)
- PropertyEditor: `ui/src/components/Pipeline/PropertyEditor.tsx` (449 lines)
- Type guards: `ui/src/utils/typeGuards.ts` (167 lines)

**Documentation**:
- Consumer overview: `README_CONSUMER.md` (418 lines)
- Architecture: `ARCHITECTURE_ANALYSIS.md` (925 lines)

