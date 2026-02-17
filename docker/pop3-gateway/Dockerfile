FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/pop3-gateway ./cmd/pop3-gateway

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/pop3-gateway /usr/local/bin/pop3-gateway
EXPOSE 110 995
CMD ["pop3-gateway"]
