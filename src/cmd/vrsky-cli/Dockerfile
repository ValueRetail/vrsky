FROM golang:1.22-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

COPY src/go.mod src/go.sum ./
COPY src/cmd/vrsky-cli/ ./cmd/vrsky-cli/
COPY src/pkg/ ./pkg/
COPY src/internal/ ./internal/

RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o vrsky-cli \
    ./cmd/vrsky-cli

# Runtime is the postgres image so pg_dump/pg_restore/psql ship at the same
# major version family as the management DB (forward-compatible with older
# servers). This is what makes `vrsky-cli backup`/`restore` work.
FROM postgres:18-alpine

RUN apk add --no-cache ca-certificates

COPY --from=builder /build/vrsky-cli /usr/local/bin/vrsky-cli

# Override the postgres image's server entrypoint — this image is the CLI.
ENTRYPOINT ["/usr/local/bin/vrsky-cli"]
CMD ["--help"]
