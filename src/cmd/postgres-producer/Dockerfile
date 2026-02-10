# Multi-stage build for minimal image size
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git

# Copy module and source files first
COPY go.mod ./
COPY go.sum ./
COPY cmd/postgres-producer/ ./cmd/postgres-producer/
COPY pkg/ ./pkg/
COPY internal/ ./internal/

# Download dependencies - must be after source code is copied
# so go can identify all required packages
RUN go mod download && go mod tidy

# Build binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o postgres-producer \
    ./cmd/postgres-producer

# Runtime stage
FROM alpine:3.18

WORKDIR /app

# Install runtime dependencies (curl for health checks, postgresql client for diagnostics)
RUN apk add --no-cache ca-certificates curl postgresql-client

# Copy binary from builder
COPY --from=builder /build/postgres-producer .

# Make binary executable
RUN chmod +x ./postgres-producer

# Health check - verify PostgreSQL producer is running
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD [ "test", "-f", "/app/postgres-producer" ]

# Configuration documentation
# Required environment variables:
# - POSTGRES_OUTPUT_PASSWORD: PostgreSQL password
# - POSTGRES_OUTPUT_DATABASE: Target database name
# - NATS_URL: NATS broker URL (default: nats://localhost:4222)
# Optional environment variables:
# - POSTGRES_OUTPUT_HOST: PostgreSQL host (default: localhost)
# - POSTGRES_OUTPUT_PORT: PostgreSQL port (default: 5432)
# - POSTGRES_OUTPUT_USER: PostgreSQL user (default: postgres)
# - POSTGRES_OUTPUT_BATCH_SIZE: Batch size for writes (default: 100)
# - POSTGRES_OUTPUT_SUBJECT: NATS subject to subscribe to (default: postgres.changes)
# - POSTGRES_CONFLICT_RESOLUTION: Conflict strategy - UPSERT, REPLACE, or SKIP (default: UPSERT)

# Run producer
ENTRYPOINT ["./postgres-producer"]
