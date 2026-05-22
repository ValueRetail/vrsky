FROM golang:1.22-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

COPY src/go.mod src/go.sum ./
COPY src/cmd/management-api/ ./cmd/management-api/
COPY src/pkg/ ./pkg/
COPY src/internal/ ./internal/

RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o management-api \
    ./cmd/management-api

FROM alpine:3.18

WORKDIR /app

RUN apk add --no-cache ca-certificates curl postgresql-client

# golang-migrate — runs SQL migrations from ./migrations on every start.
# Without this binary the entrypoint silently skipped migrations, which
# bit us when a fresh stack came up missing the secrets / tenant_quotas /
# oidc_config tables.
ARG MIGRATE_VERSION=v4.17.1
RUN set -eux; \
    arch="$(uname -m)"; \
    case "$arch" in \
      x86_64)  goarch=amd64 ;; \
      aarch64) goarch=arm64 ;; \
      *) echo "unsupported arch $arch" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/golang-migrate/migrate/releases/download/${MIGRATE_VERSION}/migrate.linux-${goarch}.tar.gz" \
        | tar -xz -C /usr/local/bin migrate; \
    chmod +x /usr/local/bin/migrate; \
    /usr/local/bin/migrate -version

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
