# ── Stage 1: node module cache (shared by later stages) ──────────────
# Base images pinned by digest for reproducible builds (OSI-17).
FROM node:20-alpine@sha256:fb4cd12c85ee03686f6af5362a0b0d56d50c58a04632e6c0fb8363f609372293 AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

# ── Stage 2: builder ─────────────────────────────────────────────────
FROM deps AS builder
COPY . .
RUN npm run build

# ── Stage 3: dev (hot reload via Vite HMR) ───────────────────────────
# Source code is volume-mounted at /app by docker-compose.override.yml.
# node_modules is preserved via anonymous volume in the override.
FROM deps AS dev
EXPOSE 3000
CMD ["npm", "run", "dev"]

# ── Stage 4: prod (nginx serving built assets) ────────────────────────
FROM nginx:1.27-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10 AS prod
ARG NGINX_BASE_PATH=/
ENV NGINX_BASE_PATH=${NGINX_BASE_PATH}
COPY nginx.conf /etc/nginx/conf.d/default.conf.template
COPY docker-entrypoint.sh /docker-entrypoint.d/30-configure-base-path.sh
RUN chmod +x /docker-entrypoint.d/30-configure-base-path.sh
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
