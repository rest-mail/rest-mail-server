# ── Stage 1: module cache (shared by all later stages) ───────────────
FROM golang:alpine AS deps
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# ── Stage 2: builder ─────────────────────────────────────────────────
FROM deps AS builder
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/smtp-gateway ./cmd/smtp-gateway

# ── Stage 3: dev (hot reload via air) ────────────────────────────────
# Source code is volume-mounted at /app by docker-compose.override.yml.
FROM deps AS dev
RUN go install github.com/air-verse/air@latest
EXPOSE 25 587 465
CMD ["air", "-c", "docker/smtp-gateway/.air.toml"]

# ── Stage 4: prod (minimal Alpine runtime) ───────────────────────────
FROM alpine:3.20 AS prod
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/smtp-gateway /usr/local/bin/smtp-gateway
COPY docker/smtp-gateway/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 25 587 465
HEALTHCHECK --interval=10s --timeout=5s --retries=3 \
    CMD nc -z localhost 25 || exit 1
ENTRYPOINT ["/entrypoint.sh"]
CMD ["smtp-gateway"]
