# ── Stage 1: module cache (shared by all later stages) ───────────────
# Base images are pinned by digest for reproducible, tamper-evident builds
# (OSI-17). Bump the tag+digest together when updating.
FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS deps
WORKDIR /app
# git is required so `go mod download` can fetch module versions that are not
# yet cached by the Go module proxy (it falls back to a direct VCS fetch).
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download

# ── Stage 2: builder ─────────────────────────────────────────────────
FROM deps AS builder
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
# air pinned (not @latest) so the dev image is reproducible and its Go floor
# stays aligned with the pinned golang base (OSI-17).
RUN go install github.com/air-verse/air@v1.67.2
EXPOSE 8080
CMD ["air", "-c", ".air.toml"]

# ── Stage 4: prod (minimal Alpine runtime, non-root) ─────────────────
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc AS prod
# Create an unprivileged runtime user (OSI-17). The entrypoint trusts the
# project CA at runtime via update-ca-certificates, so the CA anchor dirs are
# chowned to that user; /attachments is pre-created + owned so the named
# volume inherits non-root ownership on first mount. Binds :8080 (unprivileged).
RUN apk add --no-cache ca-certificates curl \
 && addgroup -S restmail && adduser -S -u 10001 -G restmail restmail \
 && mkdir -p /attachments \
 && chown -R restmail:restmail /attachments /usr/local/share/ca-certificates /etc/ssl
COPY --from=builder /bin/restmail-api  /usr/local/bin/restmail-api
COPY --from=builder /bin/restmail-seed /usr/local/bin/restmail-seed
COPY projects/api-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
USER restmail
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --retries=5 \
    CMD curl -sf http://localhost:8080/api/health || exit 1
ENTRYPOINT ["/entrypoint.sh"]
CMD ["restmail-api"]
