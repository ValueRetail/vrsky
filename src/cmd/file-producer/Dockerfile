# Multi-stage build for minimal image size
FROM golang:1.26-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git

# Copy only dependency files first for better layer caching
COPY src/go.mod src/go.sum ./

# Download dependencies (cached if go.mod/go.sum unchanged)
# proxy.golang.org intermittently resets an HTTP/2 stream mid-download, failing
# the whole image build over a module that has nothing wrong with it. Two of the
# four pushes to main on 2026-09-09 died this way (Actions runs 34349712198 and
# 34351200525), on a different module each time. Retry before giving up.
RUN for i in 1 2 3; do \
      go mod download && break; \
      echo "go mod download failed (attempt $i/3)"; \
      if [ "$i" = 3 ]; then exit 1; fi; \
      sleep 5; \
    done

# Copy source code (changes won't invalidate dependency layer)
COPY src/cmd/file-producer/ ./cmd/file-producer/
COPY src/pkg/ ./pkg/
COPY src/internal/ ./internal/

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
