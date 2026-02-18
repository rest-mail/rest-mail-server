# Multi-stage build for the Go REST API server

# Stage 1: Build
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Copy go.mod and go.sum first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the API binary
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/restmail-api ./cmd/api

# Build the seed binary
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/restmail-seed ./cmd/seed

# Stage 2: Runtime
FROM alpine:3.20

RUN apk add --no-cache ca-certificates curl

COPY --from=builder /bin/restmail-api /usr/local/bin/restmail-api
COPY --from=builder /bin/restmail-seed /usr/local/bin/restmail-seed
COPY docker/api-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=5s --retries=5 \
    CMD curl -sf http://localhost:8080/api/health || exit 1

ENTRYPOINT ["/entrypoint.sh"]
CMD ["restmail-api"]
