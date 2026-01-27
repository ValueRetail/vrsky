# AGENTS.md - Developer Guide for VRSky Integration Platform

**Last Updated**: January 27, 2026  
**Project Status**: Research Phase (Week 1 of 11) - No code exists yet  
**Target**: POC Release April 15, 2026

## Project Context

VRSky is a highly scalable, cloud-native integration platform (iPaaS) currently in the research phase. This is a **greenfield project** with no source code yet. Development begins February 10, 2026.

**Core Principles**:

- **Ephemeral by Design**: No persistent storage in platform core - messages live only during transit
- **Reference-Based Messaging**: Large payloads (>256KB) stored in object storage, NATS carries references
- **Multi-Tenant with Isolation**: Strong tenant isolation using NATS accounts
- **Component-Based**: Consumers, Producers, Converters, Filters as composable building blocks

**Key Documents**:

- Architecture & Vision: `docs/PROJECT_INCEPTION.md`
- Timeline & Milestones: `docs/ACCELERATED_TIMELINE.md`
- Research Tasks: `docs/tasks/README.md`

---

## Technology Stack

| Component             | Technology                 | Purpose                                    |
| --------------------- | -------------------------- | ------------------------------------------ |
| **Backend**           | Go 1.21+                   | Core platform services, high concurrency   |
| **Messaging**         | NATS + JetStream           | Message transport, 11M+ msgs/sec           |
| **Database**          | PostgreSQL 15+             | Metadata, tenant config, integration state |
| **Object Storage**    | MinIO (local) / S3 (cloud) | Large payload temporary storage            |
| **Container Runtime** | Docker + Kubernetes        | Orchestration and deployment               |
| **API Gateway**       | Kong                       | API management and routing                 |
| **Monitoring**        | Prometheus + Grafana       | Metrics and dashboards                     |
| **Logging**           | Loki                       | Structured log aggregation                 |
| **UI**                | React + TailwindCSS        | Admin dashboard                            |
| **CI/CD**             | GitHub Actions             | Automated build, test, deploy              |

---

## Build, Test, and Lint Commands

### Prerequisites

```bash
# Install Go 1.21+
go version

# Install development tools
go install golang.org/x/tools/cmd/goimports@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

### Build Commands

```bash
# Build all services
make build

# Build specific service
go build -o bin/api-gateway ./cmd/api-gateway

# Build with version info
go build -ldflags "-X main.Version=$(git describe --tags)" -o bin/service ./cmd/service

# Build Docker images
make docker-build

# Build for Linux (cross-compile from macOS)
GOOS=linux GOARCH=amd64 go build -o bin/service-linux ./cmd/service
```

### Test Commands

```bash
# Run all tests
make test
# or
go test ./...

# Run tests with coverage
go test -cover ./...
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out  # View coverage in browser

# Run tests with race detector
go test -race ./...

# Run specific test
go test ./pkg/messaging -run TestReferenceMessaging

# Run specific test function in a package
go test -v ./pkg/consumer -run TestHTTPConsumer/handles_webhook

# Run tests matching pattern
go test ./... -run ".*Integration.*"

# Run tests with timeout
go test -timeout 30s ./...

# Verbose output
go test -v ./pkg/...

# Run only short tests (unit tests, skip integration)
go test -short ./...

# Run integration tests only
go test -tags=integration ./...
```

### Lint and Format Commands

```bash
# Format all code (REQUIRED before commit)
gofmt -s -w .

# Organize imports
goimports -w .

# Run linter (CI will fail if this fails)
golangci-lint run

# Lint specific directory
golangci-lint run ./pkg/messaging/...

# Auto-fix issues where possible
golangci-lint run --fix

# Check for common mistakes
go vet ./...
```

### Local Development

```bash
# Start local environment (NATS, PostgreSQL, MinIO)
docker-compose up -d

# Run service locally
go run ./cmd/api-gateway

# Hot reload during development (install air first)
air

# View logs
docker-compose logs -f

# Stop local environment
docker-compose down
```

---

## Project Structure (Planned)

```
vrsky/
├── cmd/                      # Application entry points
│   ├── api-gateway/         # API gateway service
│   ├── control-plane/       # Tenant & integration management
│   ├── data-plane/          # Message processing runtime
│   └── worker/              # Background job processor
├── pkg/                      # Public libraries (reusable)
│   ├── messaging/           # NATS client, reference messaging
│   ├── consumer/            # Consumer interface & runtime
│   ├── producer/            # Producer interface & runtime
│   ├── converter/           # Converter interface & runtime
│   ├── filter/              # Filter interface & runtime
│   └── storage/             # MinIO/S3 client wrapper
├── internal/                 # Private application code
│   ├── api/                 # HTTP/gRPC handlers
│   ├── models/              # Domain models
│   ├── repository/          # Database access layer
│   └── service/             # Business logic
├── connectors/               # Built-in connectors
│   ├── http/                # HTTP REST consumer/producer
│   ├── file/                # File consumer/producer
│   └── postgres/            # PostgreSQL consumer/producer
├── deployments/              # Kubernetes manifests, Helm charts
│   ├── k8s/                 # Raw Kubernetes YAML
│   ├── helm/                # Helm charts
│   └── docker-compose.yml   # Local development
├── scripts/                  # Build and deployment scripts
├── test/                     # Integration and E2E tests
├── docs/                     # Documentation
├── .github/                  # GitHub Actions workflows
│   └── workflows/
├── Makefile                  # Build automation
├── go.mod                    # Go dependencies
└── go.sum                    # Dependency checksums
```

---

## Code Style Guidelines

### General Go Conventions

- Follow [Effective Go](https://go.dev/doc/effective_go)
- Use `gofmt` and `goimports` before every commit
- Line length: 120 characters max (prefer 80-100)
- Use tabs for indentation (Go standard)

### Imports Organization

```go
import (
    // Standard library (alphabetical)
    "context"
    "fmt"
    "time"

    // Third-party packages (alphabetical)
    "github.com/nats-io/nats.go"
    "github.com/google/uuid"

    // Internal packages (alphabetical)
    "github.com/ValueRetail/vrsky/pkg/messaging"
    "github.com/ValueRetail/vrsky/internal/models"
)
```

### Naming Conventions

- **Packages**: Short, lowercase, single word (e.g., `messaging`, `consumer`, not `message_handler`)
- **Files**: Lowercase with underscores (e.g., `http_consumer.go`, `reference_message.go`)
- **Interfaces**: Noun or adjective (e.g., `Consumer`, `MessageHandler`, `Runnable`)
- **Types**: PascalCase (e.g., `IntegrationConfig`, `TenantID`)
- **Functions/Methods**: mixedCase (e.g., `processMessage`, `GetTenantByID`)
- **Constants**: PascalCase or ALL_CAPS for exported/local (e.g., `MaxRetryAttempts`, `defaultTimeout`)
- **Acronyms**: Keep consistent case (e.g., `HTTPConsumer`, `userID`, `apiURL`)

### Error Handling

```go
// REQUIRED: Always check errors immediately
data, err := fetchData()
if err != nil {
    return fmt.Errorf("fetch data: %w", err)  // Use %w to wrap errors
}

// Use custom error types for domain errors
var ErrTenantNotFound = errors.New("tenant not found")

// Structured errors with context
return fmt.Errorf("failed to process message (tenant=%s, integration=%s): %w",
    tenantID, integrationID, err)

// Don't ignore errors - use _ only if truly not needed
_ = file.Close()  // Document why error is ignored
```

### Type Usage

```go
// Prefer explicit types over 'var'
config := &IntegrationConfig{}  // Good
var config *IntegrationConfig = &IntegrationConfig{}  // Verbose

// Use type aliases for clarity
type TenantID string
type IntegrationID string

// Use structs for configuration
type ConsumerConfig struct {
    URL         string        `json:"url"`
    Timeout     time.Duration `json:"timeout"`
    RetryCount  int           `json:"retry_count"`
}
```

### Documentation

```go
// Package documentation (in doc.go or main file)
// Package messaging provides NATS-based message transport with reference-based
// handling for large payloads (>256KB).
package messaging

// Public functions/types MUST have godoc comments
// Consumer defines the interface for data ingestion from external systems.
// Implementations include HTTP webhooks, file watchers, and database CDC.
type Consumer interface {
    // Start begins consuming messages and returns an error if startup fails.
    Start(ctx context.Context) error

    // Stop gracefully shuts down the consumer.
    Stop(ctx context.Context) error
}

// Private functions: optional but recommended for complex logic
// processLargePayload stores the payload in object storage and returns a reference
func processLargePayload(data []byte) (string, error) { ... }
```

### Concurrency Patterns

```go
// Use context for cancellation
func (c *HTTPConsumer) Start(ctx context.Context) error {
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case msg := <-c.messages:
            go c.processMessage(ctx, msg)  // Process in goroutine
        }
    }
}

// Always use sync.WaitGroup for goroutine coordination
var wg sync.WaitGroup
for _, item := range items {
    wg.Add(1)
    go func(item Item) {
        defer wg.Done()
        process(item)
    }(item)
}
wg.Wait()
```

---

## Development Workflow

### Git Workflow

- **Main branch**: `main` (protected, requires PR)
- **Feature branches**: `feature/short-description`
- **Bug fixes**: `fix/issue-description`
- **Commits**: Follow [Conventional Commits](https://www.conventionalcommits.org/)

### Commit Message Format

```
<type>(<scope>): <subject>

<body>

<footer>
```

**Types**: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`

**Examples**:

```
feat(messaging): add reference-based messaging for large payloads

Implements object storage integration for messages >256KB.
NATS now carries lightweight references instead of full payload.

Closes #12
```

### Pull Request Process

1. Create feature branch from `main`
2. Write tests for new functionality
3. Ensure all tests pass: `make test`
4. Run linter: `golangci-lint run`
5. Format code: `gofmt -s -w . && goimports -w .`
6. Commit with conventional message
7. Push and create PR with description
8. Address review comments
9. Squash merge to `main` after approval

---

## Testing Standards

### Test File Naming

- Unit tests: `file_test.go` (same package)
- Integration tests: `file_integration_test.go` (use build tag)

### Test Organization

```go
func TestHTTPConsumer_Start(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    string
        wantErr bool
    }{
        {"valid webhook", "payload", "success", false},
        {"invalid json", "bad", "", true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Test implementation
        })
    }
}
```

### Coverage Requirements

- **Minimum**: 70% overall coverage
- **Target**: 80%+ for `pkg/` packages
- **Critical paths**: 90%+ (auth, message routing, multi-tenancy)

### Integration Tests

```go
// +build integration

package messaging_test

func TestNATSReferenceMessaging(t *testing.T) {
    // Requires NATS and MinIO running
    // Run with: go test -tags=integration ./...
}
```

---

## Architecture-Specific Guidelines

### Multi-Tenancy

- Always validate `TenantID` in requests
- Use NATS accounts for message isolation
- Never leak data across tenants

### Reference-Based Messaging

- Threshold: Messages >256KB go to object storage
- Always set TTL on stored objects (default: 15 minutes)
- Clean up references after successful delivery

### Error Handling & Retries

- Use exponential backoff for retries (max 3 attempts)
- Send failed messages to dead letter queue after max retries
- Log all errors with structured context (tenant, integration, message ID)

---

## Additional Resources

- Go Code Review Comments: https://go.dev/wiki/CodeReviewComments
- NATS Documentation: https://docs.nats.io/
- Project Timeline: `docs/ACCELERATED_TIMELINE.md`
- Architecture Vision: `docs/PROJECT_INCEPTION.md`
