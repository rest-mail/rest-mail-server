FROM node:22-alpine

RUN addgroup -S jsfilter && adduser -S jsfilter -G jsfilter

WORKDIR /app

COPY server.js .
COPY package.json .

USER jsfilter

EXPOSE 3100

CMD ["node", "--max-old-space-size=64", "server.js"]
