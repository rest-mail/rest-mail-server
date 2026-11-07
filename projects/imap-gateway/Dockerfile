# ── Stage 1: module cache (shared by all later stages) ───────────────
# Base images pinned by digest for reproducible builds (OSI-17).
# This stage and the builder run on the build machine's own platform, whatever
# the target: the modules are the same files everywhere and Go cross-compiles,
# so no step here runs under emulation (#1855). Only the prod stage is built per
# platform. The dev stage inherits this stage's platform, which is right for an
# image built for the machine running it.
FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine@sha256:a2012b4a1d1f9f7e9f44e0d39b805a901e856b1dd80b4e6ee4338db547050308 AS deps
WORKDIR /app
# git is required so `go mod download` can fetch module versions not yet cached
# by the Go module proxy (direct VCS fallback). `safe.directory` lets Go's
# buildvcs stamping run on the bind-mounted source in the dev/air image, whose
# files are owned by a different uid than the container user.
RUN apk add --no-cache git \
 && git config --system --add safe.directory '*'
COPY go.mod go.sum ./
COPY third_party/go-imap ./third_party/go-imap
RUN go mod download

# ── Stage 2: builder ─────────────────────────────────────────────────
FROM deps AS builder
COPY . .
# Set by BuildKit to the platform this image is being built for.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /bin/imap-gateway ./cmd/imap-gateway

# ── Stage 3: dev (hot reload via air) ────────────────────────────────
# Source code is volume-mounted at /app by docker-compose.override.yml.
FROM deps AS dev
ENV ENVIRONMENT=development
# air pinned (not @latest) for reproducible dev images (OSI-17).
RUN go install github.com/air-verse/air@v1.67.2
# The same unprivileged user as the prod image (#193). Go's build cache goes in
# its home directory, and air builds into /app/tmp, which the dev task mounts as
# a tmpfs that user owns.
RUN addgroup -S -g 10001 restmail && adduser -S -u 10001 -G restmail restmail
USER 10001:10001
EXPOSE 993
CMD ["air", "-c", "projects/imap-gateway/.air.toml"]

# ── Stage 4: prod (minimal Alpine runtime, non-root) ─────────────────
# 3.24, not 3.20: an end-of-life release gets no fixes, so its image scan stays
# quiet while its packages age (#1947).
FROM alpine:3.24.4@sha256:93b41ef40b1677fce8fcc64ff4af09daddfacf5040afde618bc46c1e2d1cc7d4 AS prod
# An unprivileged runtime user (#193), the API image's uid. It needs no
# capability for the ports below 1024: the chart has the gateway listen on
# unprivileged ports and its Service maps the public ones onto them, and Docker
# lets any user bind any port in a container (net.ipv4.ip_unprivileged_port_start=0).
RUN apk add --no-cache ca-certificates \
 && addgroup -S -g 10001 restmail && adduser -S -u 10001 -G restmail restmail
COPY --from=builder /bin/imap-gateway /usr/local/bin/imap-gateway
# 0755, not +x: the mode must not depend on how the builder's checkout set the
# read bits, or the unprivileged user below cannot read its own entrypoint.
COPY --chmod=0755 projects/imap-gateway/entrypoint.sh /entrypoint.sh
# Numeric, so Kubernetes can tell it is not root without being told the uid.
USER 10001:10001
EXPOSE 993
HEALTHCHECK --interval=10s --timeout=5s --retries=3 \
    CMD nc -z localhost 993 || exit 1
ENTRYPOINT ["/entrypoint.sh"]
CMD ["imap-gateway"]
