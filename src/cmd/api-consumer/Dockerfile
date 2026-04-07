# Multi-stage build for minimal image size
FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY src/go.mod src/go.sum ./
RUN go mod download

COPY src/ ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o api-consumer \
    ./cmd/api-consumer

FROM alpine:3.19

RUN apk --no-cache add ca-certificates curl

WORKDIR /app
COPY --from=builder /app/api-consumer .

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD [ "test", "-f", "/app/api-consumer" ]

ENTRYPOINT ["./api-consumer"]
