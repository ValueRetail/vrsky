FROM golang:1.21-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

COPY src/go.mod src/go.sum ./
COPY src/cmd/converter/ ./cmd/converter/
COPY src/pkg/ ./pkg/
COPY src/internal/ ./internal/

RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o converter \
    ./cmd/converter

FROM alpine:3.18

WORKDIR /app

RUN apk add --no-cache ca-certificates curl

COPY --from=builder /build/converter .

RUN chmod +x ./converter

ENTRYPOINT ["./converter"]
