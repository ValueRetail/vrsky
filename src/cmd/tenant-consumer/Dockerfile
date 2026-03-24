FROM golang:1.22-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

COPY src/go.mod src/go.sum ./

RUN go mod download

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
