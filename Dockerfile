# ── Stage 1: module cache (shared by all later stages) ───────────────
FROM golang:alpine AS deps
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# ── Stage 2: builder ─────────────────────────────────────────────────
FROM deps AS builder
RUN apk add --no-cache git
COPY . .
ARG VERSION=dev
RUN COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown") \
 && BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
 && LDFLAGS="-X github.com/restmail/restmail/internal/version.Version=${VERSION} \
             -X github.com/restmail/restmail/internal/version.Commit=${COMMIT} \
             -X github.com/restmail/restmail/internal/version.BuildDate=${BUILD_DATE}" \
 && CGO_ENABLED=0 GOOS=linux go build -ldflags "${LDFLAGS}" -o /bin/restmail-api  ./cmd/api \
 && CGO_ENABLED=0 GOOS=linux go build -ldflags "${LDFLAGS}" -o /bin/restmail-seed ./cmd/seed

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
COPY projects/api-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --retries=5 \
    CMD curl -sf http://localhost:8080/api/health || exit 1
ENTRYPOINT ["/entrypoint.sh"]
CMD ["restmail-api"]
