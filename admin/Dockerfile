# ── Stage 1: node module cache (shared by later stages) ──────────────
# Base images pinned by digest for reproducible builds (OSI-17).
FROM node:20-alpine@sha256:fb4cd12c85ee03686f6af5362a0b0d56d50c58a04632e6c0fb8363f609372293 AS deps
WORKDIR /app
# pnpm, activated by corepack at the version package.json pins in
# `packageManager` — so the image build, CI and a developer's shell all install
# with one version. Dependencies are installed HERE, at build time: a container
# never installs anything when it starts, it runs what the image contains.
# --frozen-lockfile fails rather than resolving something the lockfile does not
# pin (this stage used to run a plain `npm install`, which could drift).
COPY package.json pnpm-lock.yaml ./
RUN corepack enable pnpm && pnpm install --frozen-lockfile

# ── Stage 2: builder ─────────────────────────────────────────────────
FROM deps AS builder
COPY . .
RUN pnpm run build

# ── Stage 3: dev (hot reload via Vite HMR) ───────────────────────────
# Source is bind-mounted at /app by `task admin:up` (MODE=dev), with
# /app/node_modules kept as a named volume so the host tree does not shadow the
# modules. That volume is populated by `task admin:deps` — a throwaway container
# running only pnpm — before the app container starts, so this container never
# installs anything itself.
#
# No source is copied in: the bind mount supplies it, and copying would only
# bloat the image and invalidate its cache on every edit. The port and host come
# from vite.config.ts (3000, 0.0.0.0), not from flags here.
FROM deps AS dev
EXPOSE 3000
CMD ["pnpm", "run", "dev"]

# ── Stage 4: prod (nginx serving built assets) ────────────────────────
# The base path is resolved at container start, not baked into the image: the
# bundle always lands at the html root and nginx.conf is a template whose
# __BASE_PATH__ placeholders the entrypoint fills in from NGINX_BASE_PATH. Same
# mechanism as webmail — previously admin copied the bundle into a
# path-named subdirectory instead, so the same deployment question had two
# different answers and only webmail's could be changed at run time.
FROM nginxinc/nginx-unprivileged:1.27-alpine@sha256:65e3e85dbaed8ba248841d9d58a899b6197106c23cb0ff1a132b7bfe0547e4c0 AS prod
ARG NGINX_BASE_PATH=/admin
ENV NGINX_BASE_PATH=${NGINX_BASE_PATH}
# Serving config lives in a real file (was an inline `RUN echo`) so it can carry
# a multi-value Content-Security-Policy and the other security headers without
# shell-quoting hazards.
USER root
COPY nginx.conf /etc/nginx/conf.d/default.conf.template
COPY docker-entrypoint.sh /docker-entrypoint.d/30-configure-base-path.sh
RUN chmod +x /docker-entrypoint.d/30-configure-base-path.sh
COPY --from=builder /app/dist /usr/share/nginx/html
# Back to the unprivileged uid the base image ships with.
USER 101
EXPOSE 8080
CMD ["nginx", "-g", "daemon off;"]
