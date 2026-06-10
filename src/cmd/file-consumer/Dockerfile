# Multi-stage build for minimal image size
FROM golang:1.22-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

COPY src/go.mod src/go.sum ./

RUN go mod download

COPY src/cmd/file-consumer/ ./cmd/file-consumer/
COPY src/pkg/ ./pkg/
COPY src/internal/ ./internal/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o file-consumer \
    ./cmd/file-consumer

# Runtime stage
FROM alpine:3.18

WORKDIR /app

RUN apk add --no-cache ca-certificates curl

# Create data directories
RUN mkdir -p /data/input

COPY --from=builder /build/file-consumer .

RUN chmod +x ./file-consumer

# 8080 = SDK health port (HEALTH_PORT); 9200 = aux HTTP port (WORKER_HTTP_PORT,
# serves /upload, /events, /sample-data).
EXPOSE 8080 9200

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/healthz || exit 1

ENTRYPOINT ["./file-consumer"]
