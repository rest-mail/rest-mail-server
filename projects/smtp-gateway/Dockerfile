# ── Stage 1: module cache (shared by all later stages) ───────────────
# Base images pinned by digest for reproducible builds (OSI-17).
FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS deps
WORKDIR /app
# git is required so `go mod download` can fetch module versions not yet cached
# by the Go module proxy (direct VCS fallback). `safe.directory` lets Go's
# buildvcs stamping run on the bind-mounted source in the dev/air image, whose
# files are owned by a different uid than the container user.
RUN apk add --no-cache git \
 && git config --system --add safe.directory '*'
COPY go.mod go.sum ./
RUN go mod download

# ── Stage 2: builder ─────────────────────────────────────────────────
FROM deps AS builder
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/smtp-gateway ./cmd/smtp-gateway

# ── Stage 3: dev (hot reload via air) ────────────────────────────────
# Source code is volume-mounted at /app by docker-compose.override.yml.
FROM deps AS dev
# air pinned (not @latest) for reproducible dev images (OSI-17).
RUN go install github.com/air-verse/air@v1.67.2
EXPOSE 25 587 465
CMD ["air", "-c", "projects/smtp-gateway/.air.toml"]

# ── Stage 4: prod (minimal Alpine runtime) ───────────────────────────
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc AS prod
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/smtp-gateway /usr/local/bin/smtp-gateway
COPY projects/smtp-gateway/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 25 587 465
HEALTHCHECK --interval=10s --timeout=5s --retries=3 \
    CMD nc -z localhost 25 || exit 1
ENTRYPOINT ["/entrypoint.sh"]
CMD ["smtp-gateway"]
