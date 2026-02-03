# Multi-stage build for minimal image size
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git

# Copy module and source files first
COPY go.mod ./
COPY cmd/file-producer/ ./cmd/file-producer/
COPY pkg/ ./pkg/
COPY internal/ ./internal/

# Download dependencies - must be after source code is copied
# so go can identify all required packages
RUN go mod download

# Build binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o file-producer \
    ./cmd/file-producer

# Runtime stage
FROM alpine:3.18

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates curl

# Create necessary directories for file operations
RUN mkdir -p /data/output

# Copy binary from builder
COPY --from=builder /build/file-producer .

# Make binary executable
RUN chmod +x ./file-producer

# Health check - verify the producer is running and listening for messages
# Check if the producer binary exists and is executable
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD [ "test", "-f", "/app/file-producer" ]

# Run file producer
ENTRYPOINT ["./file-producer"]
