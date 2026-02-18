# ── Stage 1: module cache (shared by all later stages) ───────────────
FROM golang:alpine AS deps
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# ── Stage 2: builder ─────────────────────────────────────────────────
FROM deps AS builder
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/pop3-gateway ./cmd/pop3-gateway

# ── Stage 3: dev (hot reload via air) ────────────────────────────────
# Source code is volume-mounted at /app by docker-compose.override.yml.
FROM deps AS dev
RUN go install github.com/air-verse/air@latest
EXPOSE 110 995
CMD ["air", "-c", "docker/pop3-gateway/.air.toml"]

# ── Stage 4: prod (minimal Alpine runtime) ───────────────────────────
FROM alpine:3.20 AS prod
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/pop3-gateway /usr/local/bin/pop3-gateway
COPY docker/pop3-gateway/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 110 995
HEALTHCHECK --interval=10s --timeout=5s --retries=3 \
    CMD nc -z localhost 110 || exit 1
ENTRYPOINT ["/entrypoint.sh"]
CMD ["pop3-gateway"]
