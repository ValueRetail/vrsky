FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY src/go.mod src/go.sum ./
RUN go mod download

COPY src/ ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o webhook-consumer \
    ./cmd/webhook-consumer

FROM alpine:3.19

RUN apk --no-cache add ca-certificates curl

# Install cloudflared for tunnel support
RUN curl -L https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64 -o /usr/local/bin/cloudflared && \
    chmod +x /usr/local/bin/cloudflared

WORKDIR /app
COPY --from=builder /app/webhook-consumer .

# 8080 = SDK health port (HEALTH_PORT); 9100 = aux HTTP port (WORKER_HTTP_PORT,
# serves /webhook, /sample-data, /tunnel/*).
EXPOSE 8080 9100

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

ENTRYPOINT ["./webhook-consumer"]
