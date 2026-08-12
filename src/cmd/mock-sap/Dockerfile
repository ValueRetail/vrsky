# Mock SAP S/4HANA OData server for local testing (not for production).
FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY src/go.mod src/go.sum ./
RUN go mod download
COPY src/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o mock-sap ./cmd/mock-sap

FROM alpine:3.19
RUN apk --no-cache add ca-certificates curl
WORKDIR /app
COPY --from=builder /app/mock-sap .
EXPOSE 8099
ENTRYPOINT ["./mock-sap"]
