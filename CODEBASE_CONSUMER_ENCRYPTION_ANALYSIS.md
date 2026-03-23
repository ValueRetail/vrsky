# VRSky Codebase Analysis: Consumer Services, NATS Patterns & Encryption

## Executive Summary

The VRSky codebase has established patterns for consumer services, NATS integration, and authentication. Current implementation uses basic auth patterns without encryption for sensitive data storage in configuration. Here's the complete analysis:

---

## 1. CONSUMER ARCHITECTURE PATTERNS

### 1.1 Consumer Structure Overview

**File-Consumer (`src/cmd/file-consumer/main.go`):**
- **Simplest consumer pattern** - minimal setup required
- Creates a `FileConsumer` instance via `io.NewFileConsumer(logger)`
- Starts with `consumer.Start(context.Background())`
- Signal handling for graceful shutdown
- Calls `consumer.Close()` on termination

**Postgres-Consumer (`src/cmd/postgres-consumer/main.go`):**
- **Complex consumer pattern** - advanced lifecycle management
- Creates `PostgresInput` via `io.NewPostgresInput(logger, prometheus.DefaultRegisterer)`
- Includes metrics collection (Prometheus)
- Separate metrics server on configurable port
- Graceful shutdown with 5-second timeout for metrics server
- Connection pooling and replication slot management

### 1.2 Common Consumer Lifecycle

All consumers follow this pattern:
```go
1. Creation: NewConsumer(...) → creates consumer instance
2. Start:    consumer.Start(ctx) → starts background goroutines/connections
3. Reading:  consumer.Read(ctx) → retrieves next envelope from channel
4. Close:    consumer.Close() → graceful shutdown
```

---

## 2. NATS SUBSCRIPTION PATTERNS

### 2.1 File Consumer NATS Pattern

**Environment Configuration:**
```
FILE_INPUT_NATS_URL       = "nats://localhost:4222" (default)
FILE_INPUT_NATS_SUBJECT   = "file.input" (default)
```

**Implementation Pattern:**
```go
// From file_input.go lines 180-189
natsURL := os.Getenv("FILE_INPUT_NATS_URL")
if natsURL == "" {
    natsURL = nats.DefaultURL
}
nc, err := nats.Connect(natsURL)
// Publish envelopes to subject
if err := f.nc.Publish(f.subject, data); err != nil {
    // error handling
}
```

**Flow:**
1. Connect to NATS broker via URL
2. Poll for files at regular interval
3. Create Envelope for each file
4. Publish serialized envelope (JSON) to NATS subject
5. Store processed file hash to prevent reprocessing

### 2.2 Postgres Consumer NATS Pattern

**Environment Configuration:**
```
POSTGRES_INPUT_NATS_URL    = "nats://localhost:4222"
POSTGRES_INPUT_SUBJECT     = "postgres.changes"
```

**Implementation Pattern:**
```go
// From postgres_input.go lines 311-323
nc, err := nats.Connect(pi.natsURL)
pi.natsConn = nc

// Initialize DLQ publisher with NATS connection
pi.dlqPublisher = NewDLQPublisher(nc, pi.dlqPublisher.config, pi.logger)

// Publish to NATS
if pi.natsConn != nil {
    if payload, err := json.Marshal(env); err == nil {
        if err := pi.natsConn.Publish(pi.natsSubject, payload); err != nil {
            // error handling
        }
    }
}
```

**Flow:**
1. Connect to NATS
2. Setup replication slot and publication
3. Poll database for changes every 100ms
4. Batch changes (configurable batch size and timeout)
5. Publish batches to NATS subject
6. Support Dead Letter Queue (DLQ) for failed messages

### 2.3 Generic NATS Input/Output (pkg/io/nats_*.go)

**NATSInput (Consumer Pattern):**
```go
// From nats_input.go
type NATSInputConfig struct {
    URL     string // NATS server URL
    Topic   string // Topic pattern to subscribe to (supports wildcards)
    Timeout int    // Connection timeout in seconds
}

// Subscribe pattern
sub, err := conn.Subscribe(n.config.Topic, func(msg *nats.Msg) {
    select {
    case n.msgChan <- msg:
    case <-ctx.Done():
        return
    }
})
```

**NATSOutput (Producer Pattern):**
```go
// From nats_output.go
type NATSOutputConfig struct {
    URL     string // NATS server URL
    Subject string // Subject to publish to
    Timeout int    // Connection timeout
}

// Publish pattern
envJSON, err := json.Marshal(env)
msg := &nats.Msg{
    Subject: n.config.Subject,
    Data:    envJSON,
}
if err := conn.PublishMsg(msg); err != nil {
    // error handling
}
conn.Flush() // Ensure delivery
```

---

## 3. ENVELOPE STRUCTURE & INTEGRATION

### 3.1 Envelope Structure (pkg/envelope/envelope.go)

```go
type Envelope struct {
    // Core identifiers
    ID            string
    TenantID      string
    IntegrationID string
    
    // Payload handling (inline or reference)
    Payload     []byte // For payloads < 256KB
    PayloadRef  string // MinIO reference for large payloads
    PayloadSize int64
    ContentType string
    
    // Pipeline tracking
    Source      string
    CurrentStep int
    StepHistory []string
    
    // Metadata - arbitrary key-value pairs
    Metadata map[string]interface{}
    
    // Timestamps
    CreatedAt time.Time
    ExpiresAt time.Time (15-minute default TTL)
    
    // Error handling
    RetryCount int
    LastError  string
}
```

**Key Finding: No IntegrationID field directly set during envelope creation**, but structure supports it for multi-tenant message routing.

---

## 4. CONFIGURATION & SECRETS HANDLING

### 4.1 Current Configuration Pattern (Node struct)

**From `pkg/managementapi/models.go` lines 12-20:**
```go
type Node struct {
    ID         string          // Unique node ID
    Type       string          // "consumer", "filter", "converter", "producer"
    Config     json.RawMessage // Type-specific configuration (stored as JSON)
    Enabled    bool
    Checkpoint *ComponentCheckpoint
}
```

**Key Finding: Config field is `json.RawMessage`, meaning:**
- Raw JSON bytes stored
- Type-specific at deserialization time
- Currently stores unencrypted credentials in JSON

### 4.2 Authentication Structures (Current Implementation)

**From `pkg/io/api_input.go` lines 42-50:**
```go
type APIAuth struct {
    Type     string // "bearer", "apikey", "basic", "none"
    Token    string // Bearer token - STORED IN PLAIN TEXT
    APIKey   string // API key value - STORED IN PLAIN TEXT
    KeyName  string // API key header name
    Username string // Basic auth username
    Password string // Basic auth password - STORED IN PLAIN TEXT
}
```

**From `pkg/managementapi/models.go` lines 176-199:**
```go
type AuthConfig struct {
    Type   string
    Basic  *BasicAuthConfig
    Bearer *BearerAuthConfig
    APIKey *APIKeyAuthConfig
}

type BasicAuthConfig struct {
    Username string
    Password string  // PLAIN TEXT
}

type BearerAuthConfig struct {
    Token string  // PLAIN TEXT
}

type APIKeyAuthConfig struct {
    HeaderName string
    Key        string  // PLAIN TEXT
}
```

**CRITICAL ISSUE:** All credentials are stored in plain text in the Node.Config JSON.

### 4.3 JWT Authentication (Management API)

**From `cmd/management-api/auth.go` lines 95-150:**
```go
// Custom HMAC-SHA256 JWT implementation
func ValidateJWT(tokenString string, jwtConfig *JWTConfig) (*JWTClaims, error) {
    // Split token: header.payload.signature
    parts := strings.Split(tokenString, ".")
    
    // Decode payload from base64
    payload, err := base64.RawURLEncoding.DecodeString(parts[1])
    
    // Parse claims
    claims := &JWTClaims{}
    json.Unmarshal(payload, claims)
    
    // Verify HMAC-SHA256 signature
    expectedSignature := hmac.New(sha256.New, []byte(jwtConfig.Secret))
    expectedSignature.Write([]byte(message))
    
    if !hmac.Equal(signature, expectedSig) {
        return nil, fmt.Errorf("invalid signature")
    }
}
```

**JWT Config from environment:**
```
JWT_ENABLED  = "true/false"
JWT_SECRET   = "<secret-key>"  (used for HMAC-SHA256)
JWT_ISSUER   = "<issuer>"
JWT_AUDIENCE = "<audience>"
```

---

## 5. ENCRYPTION LIBRARIES AVAILABLE

### 5.1 go.mod Crypto Dependencies

**From `src/go.mod` line 64:**
```
golang.org/x/crypto v0.17.0 // indirect
```

**Available via transitive dependencies:**
- `golang.org/x/crypto` - provides:
  - `crypto/sha256` (SHA-256 hashing)
  - `crypto/hmac` (HMAC signing)
  - `crypto/aes` (AES encryption)
  - `crypto/cipher` (block ciphers)
  - `crypto/rand` (cryptographically secure random)

### 5.2 Standard Library Crypto Usage in Codebase

**Currently Used:**
```go
import (
    "crypto/sha256"      // File hash calculation (file_input.go)
    "crypto/hmac"        // JWT signature verification (auth.go)
    "encoding/base64"    // JWT encoding/decoding (auth.go)
    "crypto/rand"        // For random number generation (filter/transformations.go)
)
```

**Hashing Pattern (file_input.go lines 296-312):**
```go
func (f *FileConsumer) calculateFileHash(filePath string) (string, error) {
    hash := sha256.New()
    limitedReader := io.LimitReader(file, 64*1024)
    io.Copy(hash, limitedReader)
    return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
```

**Base64 Encoding (api_input.go line 695):**
```go
credentials := a.config.Auth.Username + ":" + a.config.Auth.Password
encoded := base64.StdEncoding.EncodeToString([]byte(credentials))
req.Header.Set("Authorization", "Basic "+encoded)
```

---

## 6. CURRENT SECRET HANDLING ASSESSMENT

### 6.1 Security Posture: WEAK

**What's Currently Done:**
- JWT tokens validated with HMAC-SHA256 ✓
- Bearer tokens passed via Authorization header ✓
- Basic auth uses base64 encoding (not encryption) ✗

**What's NOT Done:**
- Credentials in Node.Config NOT encrypted ✗
- Sensitive data in json.RawMessage stored as plain text ✗
- No encryption at rest for configuration ✗
- Credentials exposed in database ✗
- No key derivation (using raw secrets) ✗
- No rotation mechanism ✗
- No masking in logs (likely logs raw values) ✗

### 6.2 Where Secrets Are Exposed

1. **Management API Configuration:**
   - `Node.Config` JSON contains plaintext credentials
   - Stored in PostgreSQL database unencrypted
   - Exposed in HTTP responses during config retrieval

2. **Consumer Configurations:**
   - APIAuth struct fields stored as json.RawMessage
   - Passwords, API keys, tokens all plain text
   - Accessible to any consumer component

3. **Logs:**
   - Likely logs contain credentials (not masked)
   - Full auth config logged during startup

4. **Memory:**
   - Credentials held in memory throughout consumer lifetime
   - Not cleared after use
   - Visible in memory dumps

---

## 7. RECOMMENDED ENCRYPTION IMPLEMENTATION PATTERNS

### 7.1 Recommended Approach: AES-256-GCM

**Why:**
- Available via `golang.org/x/crypto` (already in go.mod)
- AEAD cipher (authenticated encryption)
- Industry standard for at-rest encryption
- Can encrypt entire Node.Config JSON

**Implementation Pattern (based on existing codebase):**

```go
import (
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "encoding/json"
    "fmt"
    "io"
)

// Encrypt struct fields or entire Config JSON
func encryptConfig(plaintext []byte, encryptionKey []byte) ([]byte, error) {
    block, err := aes.NewCipher(encryptionKey)
    if err != nil {
        return nil, err
    }
    
    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, err
    }
    
    nonce := make([]byte, gcm.NonceSize())
    if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
        return nil, err
    }
    
    return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt to retrieve plaintext
func decryptConfig(ciphertext []byte, encryptionKey []byte) ([]byte, error) {
    block, err := aes.NewCipher(encryptionKey)
    if err != nil {
        return nil, err
    }
    
    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, err
    }
    
    nonce, ciphertext := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
    return gcm.Open(nil, nonce, ciphertext, nil)
}
```

---

## 8. IMPLEMENTATION CONSIDERATIONS FOR APIENDPOINT

### 8.1 Node Structure Supports Encrypted Tokens

**Current Structure Supports:**
- Node.Config as `json.RawMessage` can hold ANY JSON structure
- Can modify APIAuth to include encrypted fields
- Can add `EncryptedConfig` field as base64-encoded ciphertext

**Proposed Modification Pattern:**

```go
type Node struct {
    ID                    string
    Type                  string
    Config                json.RawMessage // Keep existing
    EncryptedCredentials  string          // NEW: base64-encoded AES-256-GCM encrypted
    CredentialsNonce      string          // NEW: nonce for this encryption
    Enabled               bool
    Checkpoint            *ComponentCheckpoint
}

// Versioning field to support different encryption schemes
type EncryptedField struct {
    Version   int    `json:"version"`    // 1 = AES-256-GCM
    Algorithm string `json:"algorithm"` // "aes-256-gcm"
    Ciphertext string `json:"ciphertext"` // base64
}
```

### 8.2 Integration with Existing Pattern

**Where to Add Encryption:**
1. **APIAuth struct** - add encrypted token/key fields
2. **BasicAuthConfig** - add encrypted password
3. **Node.Config** - encrypt entire JSON before storing

**Pattern Aligns With:**
- Existing json.RawMessage usage ✓
- Current error handling patterns ✓
- Database storage (PostgreSQL JSONB) ✓
- NATS message envelope (doesn't store secrets) ✓

---

## 9. MESSAGE ROUTING & NATS TOPICS

### 9.1 Current Topic Patterns

| Consumer | Default Subject | Environment Variable |
|----------|-----------------|----------------------|
| File     | `file.input` | `FILE_INPUT_NATS_SUBJECT` |
| Postgres | `postgres.changes` | `POSTGRES_INPUT_SUBJECT` |
| HTTP API | `*` (wildcard) | `NATS_INPUT_TOPIC` |

### 9.2 No IntegrationID in Published Messages

**Current Behavior (file_input.go line 595-616):**
```go
env := envelope.New()
env.ID = uuid.New().String()
env.Source = "FileConsumer"
env.Payload = content
env.PayloadSize = int64(len(content))
env.ContentType = f.detectContentType(filePath)
// NOTE: IntegrationID NOT SET HERE

// Publish to NATS
data, err := envelope.Marshal(env)
f.nc.Publish(f.subject, data)
```

**IntegrationID would be set by:**
1. Management API when creating connection
2. Should be passed to consumer startup
3. Currently assumed empty (multi-tenant isolation via NATS accounts)

---

## 10. KEY PATTERNS SUMMARY TABLE

| Aspect | Pattern | Implementation |
|--------|---------|-----------------|
| **Consumer Creation** | Factory function | `NewFileConsumer()`, `NewPostgresInput()` |
| **Consumer Lifecycle** | Start/Read/Close | Goroutine with channels |
| **Configuration** | Environment variables | Env vars with defaults in code |
| **NATS Connection** | Lazy initialization | On `Start()` call |
| **Message Publishing** | Fire-and-forget | `nc.Publish(subject, data)` |
| **Message Format** | JSON envelope | `json.Marshal(envelope)` |
| **Error Handling** | Backoff + retries | Exponential backoff 1s-16s |
| **Secrets Storage** | Plain text JSON | **VULNERABLE** |
| **Credential Types** | Bearer, APIKey, Basic | Struct fields in Config |
| **Available Crypto** | SHA256, HMAC, crypto/rand | Via golang.org/x/crypto |

---

## 11. IMPLEMENTATION READINESS

### For Encrypted Token Storage:

**What exists:**
- ✓ Go crypto libraries available
- ✓ JSON marshaling infrastructure
- ✓ Environment variable configuration pattern
- ✓ Node.Config as json.RawMessage (flexible)
- ✓ Error handling patterns established

**What's needed:**
- ✗ Encryption/decryption wrapper functions
- ✗ Key management (rotation, versioning)
- ✗ Decrypt-on-use in consumer initialization
- ✗ Masking in logs/responses
- ✗ Database migration for encrypted fields
- ✗ Key versioning support

**Estimated Complexity:** MEDIUM
- Core encryption simple (3-4 functions)
- Integration with existing patterns straightforward
- Database/API changes moderate
- Testing requirements moderate

---

## CONCLUSION

VRSky has a solid foundation for consumer services with well-established NATS patterns. The architecture supports encrypted secrets via the flexible `json.RawMessage` Config field. Current implementation stores credentials in plain text—this is a security gap that encryption would address using standard AES-256-GCM, already available in dependencies.

The Envelope structure doesn't set IntegrationID during publishing (handled upstream by management API), which aligns with the design for multi-tenant isolation via NATS accounts.
