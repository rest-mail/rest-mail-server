# ── Stage 1: node module cache (shared by later stages) ──────────────
FROM node:20-alpine AS deps
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
FROM nginx:alpine AS prod
ARG NGINX_BASE_PATH=/
ENV NGINX_BASE_PATH=${NGINX_BASE_PATH}
COPY nginx.conf /etc/nginx/conf.d/default.conf.template
COPY docker-entrypoint.sh /docker-entrypoint.d/30-configure-base-path.sh
RUN chmod +x /docker-entrypoint.d/30-configure-base-path.sh
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
