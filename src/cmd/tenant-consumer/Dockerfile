FROM golang:1.22-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

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

COPY src/cmd/tenant-consumer/ ./cmd/tenant-consumer/
COPY src/pkg/ ./pkg/
COPY src/internal/ ./internal/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o tenant-consumer \
    ./cmd/tenant-consumer

FROM alpine:3.18

WORKDIR /app

RUN apk add --no-cache ca-certificates curl

COPY --from=builder /build/tenant-consumer .

RUN chmod +x ./tenant-consumer

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD [ "test", "-f", "/app/tenant-consumer" ]

ENTRYPOINT ["./tenant-consumer"]
