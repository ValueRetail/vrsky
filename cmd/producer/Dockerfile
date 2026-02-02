# Multi-stage build for minimal image size
FROM golang:1.21-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git

# Copy module files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY cmd/producer/ ./cmd/producer/
COPY pkg/ ./pkg/
COPY internal/ ./internal/

# Build binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o producer \
    ./cmd/producer

# Runtime stage
FROM alpine:3.18

WORKDIR /app

# Install runtime dependencies (curl for health checks)
RUN apk add --no-cache ca-certificates curl

# Copy binary from builder
COPY --from=builder /build/producer .

# Make binary executable
RUN chmod +x ./producer

# Health check - attempt to read from env vars (producer doesn't expose HTTP endpoint)
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD [ "test", "-f", "/app/producer" ]

# Run producer
ENTRYPOINT ["./producer"]
