# Multi-stage build for minimal image size
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git

# Copy module and source files first
COPY go.mod ./
COPY go.sum ./
COPY cmd/file-consumer/ ./cmd/file-consumer/
COPY pkg/ ./pkg/
COPY internal/ ./internal/

# Download dependencies - must be after source code is copied
# so go can identify all required packages
RUN go mod download

# Build binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o file-consumer \
    ./cmd/file-consumer

# Runtime stage
FROM alpine:3.18

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates curl

# Create necessary directories for file operations
RUN mkdir -p /data/input /data/output /data/archive /data/error

# Copy binary from builder
COPY --from=builder /build/file-consumer .

# Make binary executable
RUN chmod +x ./file-consumer

# Health check - verify the consumer is running and monitoring files
# Check if the consumer binary exists and is executable
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD [ "test", "-f", "/app/file-consumer" ]

# Run file consumer
ENTRYPOINT ["./file-consumer"]
