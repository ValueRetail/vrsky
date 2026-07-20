FROM golang:1.22 AS builder

WORKDIR /build

RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*

COPY src/go.mod src/go.sum ./
COPY src/cmd/converter/ ./cmd/converter/
COPY src/pkg/ ./pkg/
COPY src/internal/ ./internal/

RUN go mod download

# The converter needs CGO (wasmtime WASM engine), so it cannot be cross-compiled
# without a matching cross toolchain. Build for the build platform's native arch
# (BuildKit sets GOARCH via TARGETARCH) instead of hardcoding amd64 — that let the
# image build on arm64 hosts (e.g. Apple Silicon / k3d) and keeps CI multi-arch.
ARG TARGETARCH
RUN CGO_ENABLED=1 GOOS=linux GOARCH=${TARGETARCH} go build \
    -ldflags="-w -s" \
    -o converter \
    ./cmd/converter

# wasmtime links against glibc (not musl), so the runtime base must be glibc —
# an alpine/musl runtime would fail to load the CGO binary at startup.
FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*

COPY --from=builder /build/converter .

RUN chmod +x ./converter

ENTRYPOINT ["./converter"]
