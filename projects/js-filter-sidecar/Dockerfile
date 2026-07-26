# Sandboxed JS mail-filter runner (OSI-6). Base image pinned by digest (OSI-17).
# Runs as a dedicated unprivileged user; the runtime (tasks/js-filter.yml) adds
# a read-only rootfs, all-caps-dropped, no-new-privileges, and tight
# pid/mem/cpu limits to contain admin-authored filter scripts.
FROM node:22-alpine@sha256:16e22a550f3863206a3f701448c45f7912c6896a62de43add43bb9c86130c3e2

RUN addgroup -S jsfilter && adduser -S jsfilter -G jsfilter

WORKDIR /app

COPY server.js .
COPY worker.js .
COPY package.json .

USER jsfilter

EXPOSE 3100

CMD ["node", "--max-old-space-size=64", "server.js"]
