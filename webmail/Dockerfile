# ── Stage 1: node module cache (shared by later stages) ──────────────
# Base images pinned by digest for reproducible builds (OSI-17).
FROM node:20-alpine@sha256:fb4cd12c85ee03686f6af5362a0b0d56d50c58a04632e6c0fb8363f609372293 AS deps
WORKDIR /app
# pnpm, activated by corepack at the version package.json pins in
# `packageManager` — so the image build, CI and a developer's shell all install
# with one version. Dependencies are installed HERE, at build time: a container
# never installs anything when it starts, it runs what the image contains.
COPY package.json pnpm-lock.yaml ./
RUN corepack enable pnpm && pnpm install --frozen-lockfile

# ── Stage 2: builder ─────────────────────────────────────────────────
FROM deps AS builder
COPY . .
RUN pnpm run build

# ── Stage 3: dev (hot reload via Vite HMR) ───────────────────────────
# Source is bind-mounted at /app by `task webmail:up` (MODE=dev), with
# /app/node_modules kept as a named volume so the host tree does not shadow the
# modules. That volume is populated by `task webmail:deps` — a throwaway
# container running only pnpm — before the app container starts, so this
# container never installs anything itself.
FROM deps AS dev
EXPOSE 3000
CMD ["pnpm", "run", "dev"]

# ── Stage 4: prod (nginx serving built assets) ────────────────────────
FROM nginxinc/nginx-unprivileged:1.27-alpine@sha256:65e3e85dbaed8ba248841d9d58a899b6197106c23cb0ff1a132b7bfe0547e4c0 AS prod
ARG NGINX_BASE_PATH=/
ENV NGINX_BASE_PATH=${NGINX_BASE_PATH}
USER root
COPY nginx.conf /etc/nginx/conf.d/default.conf.template
COPY docker-entrypoint.sh /docker-entrypoint.d/30-configure-base-path.sh
RUN chmod +x /docker-entrypoint.d/30-configure-base-path.sh
COPY --from=builder /app/dist /usr/share/nginx/html
# Back to the unprivileged uid the base image ships with.
USER 101
EXPOSE 8080
CMD ["nginx", "-g", "daemon off;"]
