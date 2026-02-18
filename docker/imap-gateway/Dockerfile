# ── Stage 1: module cache (shared by all later stages) ───────────────
FROM golang:1.24-alpine AS deps
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# ── Stage 2: builder ─────────────────────────────────────────────────
FROM deps AS builder
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/imap-gateway ./cmd/imap-gateway

# ── Stage 3: dev (hot reload via air) ────────────────────────────────
# Source code is volume-mounted at /app by docker-compose.override.yml.
FROM deps AS dev
RUN go install github.com/air-verse/air@latest
EXPOSE 143 993
CMD ["air", "-c", "docker/imap-gateway/.air.toml"]

# ── Stage 4: prod (minimal Alpine runtime) ───────────────────────────
FROM alpine:3.20 AS prod
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/imap-gateway /usr/local/bin/imap-gateway
COPY docker/imap-gateway/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 143 993
HEALTHCHECK --interval=10s --timeout=5s --retries=3 \
    CMD nc -z localhost 143 || exit 1
ENTRYPOINT ["/entrypoint.sh"]
CMD ["imap-gateway"]
