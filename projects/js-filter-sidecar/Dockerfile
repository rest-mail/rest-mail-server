# JS mail-filter runner (OSI-6). Base image pinned by digest (OSI-17).
# Scripts run in QuickJS compiled to WebAssembly (worker.js, #160), one fresh
# engine per run. The process runs as a dedicated unprivileged user; the
# runtime (tasks/js-filter.yml, the chart) adds a read-only rootfs,
# all-caps-dropped, no-new-privileges, and tight pid/mem/cpu limits.

# The install runs on the build machine's own platform, whatever the target:
# QuickJS is WebAssembly, the lockfile has no native package, and the scripts are
# ignored, so node_modules is the same files everywhere and no Node process runs
# under emulation (#1855). A native dependency would have to install in the prod
# stage instead.
FROM --platform=$BUILDPLATFORM node:22-alpine@sha256:16e22a550f3863206a3f701448c45f7912c6896a62de43add43bb9c86130c3e2 AS deps
WORKDIR /app
# The lockfile pins QuickJS by version and integrity hash.
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts

FROM node:22-alpine@sha256:16e22a550f3863206a3f701448c45f7912c6896a62de43add43bb9c86130c3e2 AS prod

# Fixed numeric UID/GID so an orchestrator enforcing runAsNonRoot can verify the
# user is non-root — a NAMED user cannot be verified and fails to start under a
# runAsNonRoot policy (e.g. Kubernetes CreateContainerConfigError).
RUN addgroup -S -g 10001 jsfilter && adduser -S -u 10001 -G jsfilter jsfilter

WORKDIR /app

COPY package.json package-lock.json ./
COPY --from=deps /app/node_modules ./node_modules

COPY server.js .
COPY worker.js .

USER 10001

EXPOSE 3100

CMD ["node", "--max-old-space-size=64", "server.js"]
