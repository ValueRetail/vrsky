# Multi-stage build for minimal image size
FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY src/go.mod src/go.sum ./
RUN go mod download

COPY src/ ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o cloud-storage-producer \
    ./cmd/cloud-storage-producer

FROM alpine:3.19

RUN apk --no-cache add ca-certificates curl

WORKDIR /app
COPY --from=builder /app/cloud-storage-producer .

# The SDK runner serves /health on HEALTH_PORT (default 8080).
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

ENTRYPOINT ["./cloud-storage-producer"]
