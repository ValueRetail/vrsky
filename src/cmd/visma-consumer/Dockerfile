# Multi-stage build for minimal image size
FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY src/go.mod src/go.sum ./
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

COPY src/ ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o visma-consumer \
    ./cmd/visma-consumer

FROM alpine:3.19

RUN apk --no-cache add ca-certificates curl

WORKDIR /app
COPY --from=builder /app/visma-consumer .

# The SDK runner serves /healthz + /readyz on HEALTH_PORT (default 8080).
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/healthz || exit 1

ENTRYPOINT ["./visma-consumer"]
