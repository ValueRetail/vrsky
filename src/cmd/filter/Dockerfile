FROM golang:1.21-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git

COPY go.mod go.sum ./

COPY cmd/ ./cmd/

COPY pkg/ ./pkg/

COPY internal/ ./internal/

RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o filter \
    ./cmd/filter

FROM alpine:3.18

WORKDIR /app

RUN apk add --no-cache ca-certificates curl

COPY --from=builder /build/filter .

RUN chmod +x ./filter

ENTRYPOINT ["./filter"]
