FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/imap-gateway ./cmd/imap-gateway

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/imap-gateway /usr/local/bin/imap-gateway
EXPOSE 143 993
CMD ["imap-gateway"]
