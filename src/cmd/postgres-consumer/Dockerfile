# Multi-stage build for minimal image size
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git

# Copy module and source files first
COPY src/go.mod ./
COPY src/go.sum ./
COPY src/cmd/postgres-consumer/ ./cmd/postgres-consumer/
COPY src/pkg/ ./pkg/
COPY src/internal/ ./internal/

# Download dependencies - must be after source code is copied
# so go can identify all required packages
RUN go mod download

# Build binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o postgres-consumer \
    ./cmd/postgres-consumer

# Runtime stage
FROM alpine:3.18

WORKDIR /app

# Install runtime dependencies (curl for health checks, postgresql client for diagnostics)
RUN apk add --no-cache ca-certificates curl postgresql-client

# Copy binary from builder
COPY --from=builder /build/postgres-consumer .

# Make binary executable
RUN chmod +x ./postgres-consumer

# Health check - verify PostgreSQL consumer is running
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD [ "test", "-f", "/app/postgres-consumer" ]

# Configuration documentation
# Required environment variables:
# - POSTGRES_INPUT_PASSWORD: PostgreSQL password
# - POSTGRES_INPUT_DATABASE: Source database name
# - NATS_URL: NATS broker URL (default: nats://localhost:4222)
# Optional environment variables:
# - POSTGRES_INPUT_HOST: PostgreSQL host (default: localhost)
# - POSTGRES_INPUT_PORT: PostgreSQL port (default: 5432)
# - POSTGRES_INPUT_USER: PostgreSQL user (default: postgres)
# - POSTGRES_INPUT_REPLICATION_SLOT: Replication slot name (default: vrsky_slot)
# - POSTGRES_INPUT_PUBLICATION: Publication name (default: vrsky_publication)
# - POSTGRES_INPUT_TABLES: Comma-separated table whitelist (optional)
# - POSTGRES_INPUT_BATCH_SIZE: Batch size for changes (default: 100)
# - POSTGRES_INPUT_SUBJECT: NATS subject for publishing (default: postgres.changes)

# Run consumer
ENTRYPOINT ["./postgres-consumer"]
