FROM golang:1.22 AS builder

WORKDIR /build

RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*

COPY src/go.mod src/go.sum ./
COPY src/cmd/converter/ ./cmd/converter/
COPY src/pkg/ ./pkg/
COPY src/internal/ ./internal/

RUN go mod download

RUN GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o converter \
    ./cmd/converter

FROM alpine:3.18

WORKDIR /app

RUN apk add --no-cache ca-certificates curl

COPY --from=builder /build/converter .

RUN chmod +x ./converter

ENTRYPOINT ["./converter"]
