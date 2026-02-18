# ── Stage 1: module cache (shared by all later stages) ───────────────
FROM golang:alpine AS deps
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# ── Stage 2: builder ─────────────────────────────────────────────────
FROM deps AS builder
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/restmail-api  ./cmd/api \
 && CGO_ENABLED=0 GOOS=linux go build -o /bin/restmail-seed ./cmd/seed

# ── Stage 3: dev (hot reload via air) ────────────────────────────────
# Source code is volume-mounted at /app by docker-compose.override.yml.
# Air watches *.go files, rebuilds, and restarts the binary automatically.
FROM deps AS dev
RUN go install github.com/air-verse/air@latest
EXPOSE 8080
CMD ["air", "-c", ".air.toml"]

# ── Stage 4: prod (minimal Alpine runtime) ───────────────────────────
FROM alpine:3.20 AS prod
RUN apk add --no-cache ca-certificates curl
COPY --from=builder /bin/restmail-api  /usr/local/bin/restmail-api
COPY --from=builder /bin/restmail-seed /usr/local/bin/restmail-seed
COPY docker/api-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --retries=5 \
    CMD curl -sf http://localhost:8080/api/health || exit 1
ENTRYPOINT ["/entrypoint.sh"]
CMD ["restmail-api"]
