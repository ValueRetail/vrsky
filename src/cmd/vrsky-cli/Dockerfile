FROM golang:1.22-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

COPY src/go.mod src/go.sum ./
COPY src/cmd/vrsky-cli/ ./cmd/vrsky-cli/
COPY src/pkg/ ./pkg/
COPY src/internal/ ./internal/

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
