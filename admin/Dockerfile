# Development stage
FROM node:20-alpine AS dev
WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . .
EXPOSE 3000
CMD ["npm", "run", "dev", "--", "--host", "0.0.0.0"]

# Build stage
FROM node:20-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . .
RUN npm run build

# Production stage with nginx
FROM nginx:alpine AS prod
ARG NGINX_BASE_PATH=/admin
COPY --from=build /app/dist /usr/share/nginx/html${NGINX_BASE_PATH}
RUN echo 'server { \
    listen 80; \
    location /admin { \
        alias /usr/share/nginx/html/admin; \
        try_files $uri $uri/ /admin/index.html; \
        add_header Cache-Control "no-cache"; \
    } \
}' > /etc/nginx/conf.d/default.conf
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
