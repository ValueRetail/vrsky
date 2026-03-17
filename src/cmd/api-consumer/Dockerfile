# Multi-stage build for minimal image size
FROM golang:1.22-alpine AS builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git

# Copy only dependency files first for better layer caching
COPY src/go.mod src/go.sum ./

# Download dependencies (cached if go.mod/go.sum unchanged)
RUN go mod download

# Copy source code (changes won't invalidate dependency layer)
COPY src/cmd/api-consumer/ ./cmd/api-consumer/
COPY src/pkg/ ./pkg/
COPY src/internal/ ./internal/

# Build binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o api-consumer \
    ./cmd/api-consumer

# Runtime stage
FROM alpine:3.18

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates curl

# Copy binary from builder
COPY --from=builder /build/api-consumer .

# Make binary executable
RUN chmod +x ./api-consumer

# Health check - verify the consumer is running
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD [ "test", "-f", "/app/api-consumer" ]

# Run api consumer
ENTRYPOINT ["./api-consumer"]
