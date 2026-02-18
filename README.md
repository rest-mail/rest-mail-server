# REST MAIL

A modern email server built in Go that extends traditional SMTP/IMAP with a REST API, pipeline-based message processing, and a React webmail client.

## Architecture

REST MAIL consists of several services orchestrated via Docker Compose:

| Service | Container IP | Ports | Description |
|---------|-------------|-------|-------------|
| **API** | 172.20.0.20 | 8080 | REST API server (chi router, GORM/PostgreSQL) |
| **PostgreSQL** | 172.20.0.2 | 5432 | Primary database |
| **dnsmasq** | 172.20.0.3 | 53 | Local DNS for dev domains |
| **Postfix (mail1)** | 172.20.0.11 | 25, 587 | Traditional MTA for mail1.test |
| **Postfix (mail2)** | 172.20.0.12 | 25, 587 | Traditional MTA for mail2.test |
| **Postfix (mail3)** | 172.20.0.13 | 25, 587 | RESTMAIL-enabled MTA for mail3.test |
| **Dovecot (mail1)** | 172.20.0.14 | 143, 993 | IMAP/POP3 for mail1.test |
| **Dovecot (mail2)** | 172.20.0.17 | 143, 993 | IMAP/POP3 for mail2.test |
| **SMTP Gateway** | — | 2525, 2587, 2465 | Go SMTP gateway (submission, STARTTLS, implicit TLS) |
| **IMAP Gateway** | 172.20.0.15 | 1143, 1993 | Go IMAP gateway |
| **POP3 Gateway** | 172.20.0.16 | 1110, 1995 | Go POP3 gateway |
| **Webmail** | 172.20.0.21 | 3000 | React SPA |

### Mail Domains

- **mail1.test** — Traditional Postfix/Dovecot domain
- **mail2.test** — Traditional Postfix/Dovecot domain
- **mail3.test** — RESTMAIL protocol-enabled domain (API-native)

## Tech Stack

- **Go 1.24+** — API server, gateways, TUI, tools
- **chi** — HTTP router
- **GORM** — ORM with PostgreSQL
- **JWT** — Authentication (`golang-jwt/jwt/v5`)
- **bcrypt** — Passwords prefixed with `{BLF-CRYPT}` for Dovecot compatibility
- **React 18 + TypeScript** — Webmail SPA
- **Tailwind CSS + shadcn/ui** — UI components
- **Postfix** — MTA
- **Dovecot** — IMAP/POP3 server

## Project Structure

```
cmd/
  api/            REST API server
  smtp-gateway/   SMTP gateway service
  imap-gateway/   IMAP gateway service
  pop3-gateway/   POP3 gateway service
  tui/            Terminal UI admin tool
  certgen/        TLS/DKIM certificate generator
  migrate/        Database migration runner
  seed/           Test data seeder
  rotate-key/     Master key rotation tool
  website/        Project website

internal/
  api/            HTTP handlers, routes, middleware
  api/respond/    Response helpers
  acme/           ACME (Let's Encrypt) client
  auth/           JWT + bcrypt authentication
  config/         Environment variable loading
  crypto/         AES-256-GCM encryption helpers
  db/models/      GORM model structs
  digest/         Quarantine digest email worker
  dns/            Pluggable DNS provider adapters
  gateway/        Protocol gateway internals
    smtp/         SMTP session, PROXY protocol
    imap/         IMAP session handler
    pop3/         POP3 session handler
    queue/        Outbound queue worker (MX lookup, backoff, RESTMAIL upgrade)
    apiclient/    API client for gateways
  mail/           Message-ID generation, helpers
  metrics/        Prometheus metrics collectors
  mime/           RFC 2822 parser, iCalendar support
  pipeline/       Filter engine, registry, 16+ built-in filters
    filters/      Individual filter implementations
  tui/            TUI views and models

docker/
  certs/          Certificate generation Dockerfile
  dnsmasq/        DNS server configuration
  dovecot/        Dovecot IMAP/POP3 configuration
  postfix/        Postfix MTA configuration
  smtp-gateway/   SMTP gateway Dockerfile
  imap-gateway/   IMAP gateway Dockerfile
  pop3-gateway/   POP3 gateway Dockerfile
  fail2ban/       Fail2ban sidecar
  js-filter-sidecar/  Node.js JavaScript filter runtime
  rspamd/         Rspamd spam filter sidecar

webmail/          React SPA frontend
website/          Project website
tests/e2e/        End-to-end test suite (13 stages)
```

## Getting Started

### Prerequisites

- Docker & Docker Compose
- Go 1.24+
- Node.js 18+ (for webmail development)

### Quick Start

```bash
# Generate TLS certificates and start all services
docker compose up -d

# Wait for services to be healthy
curl -sf http://localhost:8080/health

# Seed test data
go run ./cmd/seed

# Access the webmail
open http://localhost:3000
```

### Development

```bash
# Build all Go binaries
go build -o build/api/api ./cmd/api
go build -o build/gateways/smtp-gateway ./cmd/smtp-gateway
go build -o build/gateways/imap-gateway ./cmd/imap-gateway
go build -o build/gateways/pop3-gateway ./cmd/pop3-gateway
go build -o build/tui/tui ./cmd/tui
go build -o build/tools/certgen ./cmd/certgen
go build -o build/tools/rotate-key ./cmd/rotate-key

# Run tests
go test ./...
go test -race ./internal/...

# Run E2E tests (requires Docker stack running)
go test -v -count=1 ./tests/e2e/

# Webmail development
cd webmail
npm install
npm run dev
```

## Pipeline Filters

The pipeline engine processes emails through configurable filter chains. Built-in filters:

| Filter | Type | Description |
|--------|------|-------------|
| `spf_check` | Check | SPF record validation |
| `dkim_verify` | Check | DKIM signature verification |
| `dkim_sign` | Transform | DKIM signature generation |
| `dmarc_check` | Check | DMARC policy enforcement |
| `arc_verify` | Check | ARC chain verification |
| `arc_seal` | Transform | ARC seal generation |
| `spam_score` | Check | Spam scoring with configurable thresholds |
| `rate_limit` | Check | Per-sender/domain rate limiting |
| `size_limit` | Check | Message size enforcement |
| `attachment_check` | Check | Blocked file type/extension checking |
| `greylist` | Check | Greylisting with DB-backed tracking |
| `vacation` | Action | Auto-reply responder |
| `sieve` | Action | Sieve script execution |
| `webhook` | Action | HTTP webhook notifications |
| `duplicate` | Action | Fork message to webhook/queue |
| `javascript` | Action | Custom JS filter via Node.js sidecar |
| `rspamd` | Adapter | Rspamd spam scanning |
| `clamav` | Adapter | ClamAV virus scanning |

## API Overview

The REST API is documented via OpenAPI 3.1 spec, available at `/api/docs` in development mode.

### Key Endpoints

- `POST /api/v1/auth/login` — Authenticate and get JWT
- `GET /api/v1/accounts` — List linked email accounts
- `GET /api/v1/accounts/:id/folders/:folder/messages` — List messages
- `GET /api/v1/messages/:id` — Get message detail
- `POST /api/v1/messages/send` — Send a message
- `POST /api/v1/messages/deliver` — Deliver inbound message (gateway)
- `GET /api/v1/admin/domains` — List domains
- `GET /api/v1/admin/pipelines` — List pipeline configurations
- `POST /api/v1/admin/pipelines/test` — Test a pipeline with sample email
- `GET /api/v1/admin/queue` — View outbound queue
- `GET /.well-known/mta-sts.txt` — MTA-STS policy
- `POST /.well-known/smtp-tlsrpt` — TLS-RPT report ingestion

## RESTMAIL Protocol

REST MAIL introduces the RESTMAIL SMTP extension for HTTP-based mail delivery between RESTMAIL-capable servers. When an outbound queue worker detects RESTMAIL support via EHLO, it upgrades the connection to HTTPS POST delivery, bypassing traditional SMTP data transfer.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | — | PostgreSQL connection string |
| `JWT_SECRET` | — | JWT signing secret |
| `MASTER_KEY` | — | AES-256 key for encrypting private keys |
| `SMTP_PORT_INBOUND` | 25 | SMTP inbound port |
| `SMTP_PORT_SUBMISSION` | 587 | SMTP submission port |
| `SMTP_PORT_IMPLICIT_TLS` | 465 | SMTP implicit TLS port |
| `QUEUE_WORKERS` | 4 | Outbound queue worker count |
| `QUEUE_POLL_INTERVAL` | 5s | Queue polling interval |
| `ACME_ENABLED` | false | Enable Let's Encrypt auto-provisioning |
| `ACME_EMAIL` | — | ACME contact email |
| `PROXY_PROTOCOL_TRUSTED_CIDRS` | — | Trusted CIDRs for PROXY protocol |
| `CORS_ALLOWED_ORIGINS` | http://localhost:3000 | CORS origins |

## E2E Test Suite

The test suite (`tests/e2e/`) has 13 stages:

1. **Health** — Service connectivity
2. **Auth** — Login, registration, JWT
3. **Domains** — CRUD operations
4. **Mailboxes** — Create, quota, password
5. **Folders** — CRUD, system folders
6. **Messages** — Send, receive, search
7. **Queue** — Outbound delivery
8. **Pipeline** — Filter execution
9. **Certificates** — TLS cert management
10. **Pipeline Filters** — Individual filter tests
11. **Queue Retry** — 4xx retry with backoff
12. **Bounce DSN** — 5xx bounce delivery
13. **IMAP IDLE** — Push notifications

## License

MIT
