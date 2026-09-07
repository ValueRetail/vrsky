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

# cloudflared powers the on-demand quick tunnel (/tunnel/start), a LOCAL
# DEVELOPMENT affordance that gives a laptop a temporary public URL. Deployed
# pipelines use the permanent /webhook route from the webhook ingress instead.
#
# Pinned to an exact release and checksum-verified. This used to fetch
# `releases/latest`, which meant every image build pulled whatever Cloudflare
# had published that morning, unverified, straight into a production image:
# builds were not reproducible and the contents of a prod binary depended on
# the build date.
#
# To update: pick a tag from https://github.com/cloudflare/cloudflared/releases,
# then
#   curl -sSL https://github.com/cloudflare/cloudflared/releases/download/<TAG>/cloudflared-linux-amd64 | sha256sum
# and put both values here.
ARG CLOUDFLARED_VERSION=2026.8.3
ARG CLOUDFLARED_SHA256=f29324fe934d1e100617484c78deef803c4dc2cd351d645bbde42e96b4fccc5e
RUN curl -fsSL -o /usr/local/bin/cloudflared \
      "https://github.com/cloudflare/cloudflared/releases/download/${CLOUDFLARED_VERSION}/cloudflared-linux-amd64" && \
    echo "${CLOUDFLARED_SHA256}  /usr/local/bin/cloudflared" | sha256sum -c - && \
    chmod +x /usr/local/bin/cloudflared

WORKDIR /app
COPY --from=builder /app/webhook-consumer .

# 8080 = SDK health port (HEALTH_PORT); 9100 = aux HTTP port (WORKER_HTTP_PORT,
# serves /webhook, /sample-data, /tunnel/*).
EXPOSE 8080 9100

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/healthz || exit 1

ENTRYPOINT ["./webhook-consumer"]
