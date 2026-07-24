# ── Stage 1: module cache (shared by all later stages) ───────────────
# Base images pinned by digest for reproducible builds (OSI-17).
FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS deps
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
# air pinned (not @latest) for reproducible dev images (OSI-17).
RUN go install github.com/air-verse/air@v1.67.2
EXPOSE 110 995
CMD ["air", "-c", "projects/pop3-gateway/.air.toml"]

# ── Stage 4: prod (minimal Alpine runtime) ───────────────────────────
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc AS prod
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/pop3-gateway /usr/local/bin/pop3-gateway
COPY projects/pop3-gateway/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 110 995
HEALTHCHECK --interval=10s --timeout=5s --retries=3 \
    CMD nc -z localhost 110 || exit 1
ENTRYPOINT ["/entrypoint.sh"]
CMD ["pop3-gateway"]
