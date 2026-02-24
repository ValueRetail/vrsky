FROM golang:1.21-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

COPY go.mod go.sum ./
COPY cmd/management-api/ ./cmd/management-api/
COPY pkg/managementapi/ ./pkg/managementapi/
COPY pkg/ ./pkg/
COPY internal/ ./internal/

RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o management-api \
    ./cmd/management-api

FROM alpine:3.18

WORKDIR /app

RUN apk add --no-cache ca-certificates curl postgresql-client

# Copy migration files
COPY infrastructure/migrations ./migrations/

# Copy the entrypoint script
COPY src/cmd/management-api/entrypoint.sh ./

# Copy the compiled binary
COPY --from=builder /build/management-api .

RUN chmod +x ./management-api ./entrypoint.sh

# Health check
HEALTHCHECK --interval=10s --timeout=5s --retries=5 \
    CMD curl -f http://localhost:3000/health || exit 1

EXPOSE 3000

ENTRYPOINT ["./entrypoint.sh"]
