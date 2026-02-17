FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/smtp-gateway ./cmd/smtp-gateway

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/smtp-gateway /usr/local/bin/smtp-gateway
EXPOSE 25 587 465
CMD ["smtp-gateway"]
