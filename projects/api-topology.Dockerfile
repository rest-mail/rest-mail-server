ARG API_RUNTIME_IMAGE=ghcr.io/rest-mail/api:dev
FROM --platform=$BUILDPLATFORM golang:1.26.11-alpine@sha256:ecb67f987aa1a57794c91d19ade7ae0209f2f0f5496aaef9e6ae85ddf92d02ab AS tools
WORKDIR /app
RUN apk add --no-cache git \
 && git config --system --add safe.directory '*'
COPY go.mod go.sum ./
COPY third_party/go-imap ./third_party/go-imap
COPY third_party/go-pop3 ./third_party/go-pop3
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /bin/restmail-fixtures ./tests/contract/fixtures \
 && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /bin/restmail-seed ./cmd/seed \
 && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /bin/restmail-migrate ./cmd/migrate \
 && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o /bin/restmail-instance ./cmd/instance

FROM ${API_RUNTIME_IMAGE}
COPY --from=tools /bin/restmail-fixtures /usr/local/bin/restmail-fixtures
COPY --from=tools /bin/restmail-seed /usr/local/bin/restmail-seed
COPY --from=tools /bin/restmail-migrate /usr/local/bin/restmail-migrate
COPY --from=tools /bin/restmail-instance /usr/local/bin/restmail-instance
LABEL rest-mail.topology-tools=true
