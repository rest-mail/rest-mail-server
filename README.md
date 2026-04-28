# RESTMAIL

A full-featured mail server platform that exposes email functionality through a REST API while maintaining protocol-level indistinguishability from traditional Postfix/Dovecot mail servers. Built in Go with a React webmail frontend, RESTMAIL gives you programmatic control over every aspect of email delivery, filtering, and administration.

## Features

**Core Email**
- Full REST API for sending, receiving, searching, threading, and managing email
- SMTP, IMAP, and POP3 gateways that behave identically to Postfix/Dovecot
- MIME parsing with attachment handling, inline images, and iCalendar support
- Calendar invite detection, RSVP responses, and `.ics` composition
- Drafts, forwarding, folder management, and quota tracking

**Security and Authentication**
- TLS with SNI support and DB-backed certificate management
- DKIM signing, SPF verification, DMARC policy enforcement
- ARC sealing for forwarded messages
- MTA-STS and TLS-RPT (RFC 8460/8461)
- ACME/Let's Encrypt auto-renewal
- JWT authentication with bcrypt passwords (Dovecot-compatible `{BLF-CRYPT}` format)
- PROXY protocol support for reverse proxies (HAProxy, nginx)
- Connection rate limiting, auth ban tracking, fail2ban integration

**Pipeline Engine**
- 16+ built-in filters: spam scoring, virus scanning, greylisting, DKIM signing, attachment dedup, header rewriting, recipient verification, sender allow/blocklists, vacation responder, and more
- Custom filter support via JavaScript (Node.js sidecar) and Sieve scripts
- Configurable inbound and outbound pipelines per domain
- Quarantine with release/delete management

**Administration**
- Domain, mailbox, and alias CRUD via REST API
- Queue management with retry, bounce, and bulk operations
- Delivery and activity log queries
- IP ban management
- OpenAPI 3.1 spec (108 operations) with Swagger UI
- Console admin tool (bubbletea) with inbox, search, compose, and live status
- React webmail with rich text editor, contacts, vacation settings
- Prometheus metrics endpoint with Grafana dashboards
- Email client auto-configuration (Mozilla, Microsoft, Apple)

## Quick Start

### Prerequisites

- Docker
- [Task](https://taskfile.dev/) — required (the repo is driven entirely by Taskfiles, no docker-compose)

### Start the Stack

The stack joins the `rest-mail/testbed` substrate (mailnet network + dnsmasq + certs volume). Bring the testbed up first, then start the restmail product stack.

```bash
# 1. First run only — clone the testbed into .workspace/
task testbed:init

# 2. Bring up the testbed substrate (network, certs volume, dnsmasq)
task testbed:up

# 3. Start the restmail (mail3.test) product stack
task dev   # alias for restmail:mail3:up
```

`task dev` brings up PostgreSQL, the JS filter sidecar, the REST API, the SMTP/IMAP/POP3 gateways, webmail, admin UI, and the project website. Each container is driven by its own file under [`tasks/`](tasks/), included from the root [`Taskfile.yml`](Taskfile.yml).

### Seed Test Data

```bash
# Run migrations and seed sample domains, mailboxes, and messages
task db:reset

# Or without Task
go run ./cmd/migrate && go run ./cmd/seed
```

### Access Services

| Service        | URL / Port                          |
|----------------|-------------------------------------|
| REST API       | http://localhost:8080               |
| Swagger UI     | http://localhost:8080/api/docs      |
| Webmail        | http://localhost:3000               |
| Website        | http://localhost:8090               |
| SMTP           | localhost:25 / 587 / 465            |
| IMAP           | localhost:143 / 993                 |
| POP3           | localhost:110 / 995                 |
| Health check   | http://localhost:8080/api/health    |
| Metrics        | http://localhost:8080/metrics       |

### Test Domains

This repo only ships **mail3.test** (the RESTMAIL product). For traditional reference instances (mail1.test, mail2.test, ...) running Postfix + Dovecot, see [`rest-mail/reference-mailserver`](https://github.com/rest-mail/reference-mailserver). Reference instances are launched one at a time — `task mailserver:mail1:up`, `task mailserver:mail2:up`, etc. — and you decide how many to run.

| Domain       | Type        | Description                        |
|--------------|-------------|------------------------------------|
| mail3.test   | RESTMAIL    | Go gateways backed by REST API     |

### Test Accounts

After running `task db:seed`, the following accounts are available:

**Admin User (for Console tool):**
- Username: `admin`
- Password: `admin123!@`
- Role: `superadmin` (full access)

**Mail3.test Mailboxes:**
| Email | Password | Display Name |
|-------|----------|--------------|
| `eve@mail3.test` | `password123` | Eve Wilson |
| `frank@mail3.test` | `password123` | Frank Miller |
| `postmaster@mail3.test` | `password123` | Postmaster |

**Mail3.test Aliases:**
- `info@mail3.test` → `eve@mail3.test`
- `admin@mail3.test` → `eve@mail3.test`

**RBAC System:**
The seed command also creates a complete Role-Based Access Control system:
- **Roles**: `superadmin`, `admin`, `readonly`
- **Capabilities**: 19 permissions covering domains, mailboxes, users, pipelines, messages, queue, and bans
- See [cmd/seed/main.go](cmd/seed/main.go) for the complete capability list

## Admin Tools

### Console

Interactive terminal admin tool (built with bubbletea) for managing the mail server:

```bash
# Build the console (auto-detects your OS/architecture)
task build:console

# Run the console (executes inside the api container)
task run:console
```

**Features:**
- RBAC-aware access control (displays your role and capabilities)
- Domain, mailbox, and alias management
- Queue operations
- Live status monitoring

**Authentication:** Uses the admin credentials (username/password) seeded by `task db:seed`.

### Instant Mail Check

Standalone CLI tool for comprehensive mail server diagnostics, security auditing, and deliverability testing. Lives in its own repo: [`rest-mail/instantmailcheck`](https://github.com/rest-mail/instantmailcheck).

```bash
# Homebrew (macOS / Linux)
brew install antimatter-studios/tap/instantmailcheck

# Or via Go
go install github.com/rest-mail/instantmailcheck@latest

# Or download a pre-built binary
# https://github.com/rest-mail/instantmailcheck/releases
```

Run against any mail-serving domain:

```bash
instantmailcheck example.com                                    # Tier 1: public audit
instantmailcheck example.com --send-to alice@example.com         # Tier 2: send test
instantmailcheck example.com --user alice@example.com \          # Tier 3: round-trip
                             --pass secret --send-to alice@example.com
instantmailcheck example.com --security-audit                    # Tier 4: security
```

See the [upstream README](https://github.com/rest-mail/instantmailcheck#readme) for the full reference.

## Architecture

This repo ships **mail3.test only** — the RESTMAIL product. Postfix/Dovecot reference instances live in [`rest-mail/reference-mailserver`](https://github.com/rest-mail/reference-mailserver) and dnsmasq lives in [`rest-mail/testbed`](https://github.com/rest-mail/testbed). All three projects share the `mailnet` Docker network and the `certs` volume that the testbed provides.

```
                          Clients
                    (Thunderbird, Outlook, curl)
                             |
              +--------------+--------------+
              |              |              |
         SMTP:25/587    IMAP:143/993   POP3:110/995
              |              |              |
     +--------+--------+     |    +--------+--------+
     | SMTP Gateway    |     |    | POP3 Gateway    |
     | (Go, mail3.test)|     |    | (Go, mail3.test)|
     +--------+--------+     |    +--------+--------+
              |         +----+----+         |
              |         | IMAP GW |         |
              |         | (Go)    |         |
              |         +----+----+         |
              |              |              |
              +------+-------+------+-------+
                            |
                            v
                     +------+------+
                     | REST API    |
                     | :8080 (Go)  |
                     +------+------+
                            |
                     +------+------+
                     | Pipeline    |
                     | Engine      |
                     +------+------+
                            |
                     +------+------+
                     | PostgreSQL  |
                     | :5432       |
                     +-------------+

     +-------------+    +-----------+    +-----------+
     | Webmail     |    | Admin UI  |    | Console   |
     | (React)     |    | (React)   |    | (bubbletea)|
     +-------------+    +-----------+    +-----------+

  External (separate repos, joined via mailnet):
     [testbed]              [reference-mailserver]
     - dnsmasq DNS          - Postfix + Dovecot (mail1.test, mail2.test, ...)
     - certs volume         - rspamd, fail2ban
     - mailnet network      - postgres
```

### Directory Layout

```
cmd/
  api/              REST API server (main entry point)
  smtp-gateway/     SMTP protocol gateway
  imap-gateway/     IMAP protocol gateway
  pop3-gateway/     POP3 protocol gateway
  console/          Terminal admin UI (bubbletea) with RBAC
  migrate/          Database migration runner
  certgen/          TLS/DKIM certificate generator
  seed/             Test data and RBAC seeder (domains, mailboxes, admin user)
  website/          Project website server
internal/
  mailcheck/        Mail server diagnostic checks (DNS, SMTP, IMAP, security, deliverability)
  api/              Handlers, middleware, routes, SSE, response helpers
  auth/             JWT + bcrypt authentication
  config/           Environment variable loading
  crypto/           AES-256-GCM encryption helpers
  db/models/        GORM model structs (30+ models)
  dns/              Pluggable DNS providers (dnsmasq, externaldns, manual)
  gateway/          SMTP, IMAP, POP3 implementations + queue worker
  mail/             Message-ID generation
  metrics/          Prometheus instrumentation
  mime/             MIME parser, iCalendar support
  pipeline/         Processing engine, filter registry, 16+ built-in filters
  console/          Console screens and components
webmail/            React frontend (Vite + TypeScript + Tailwind + shadcn/ui)
admin/              Admin UI (React)
docker/             Dockerfiles for the API, gateways, webmail, admin, website
helm/               Helm chart for restmail (mail3 only)
monitoring/         Prometheus config, alerting rules, Grafana dashboards
tasks/              Per-service Taskfiles (one per container) included from root Taskfile.yml
tests/e2e/          End-to-end test suite (10 stages)
.workspace/         Sibling repos cloned on demand (testbed, reference-mailserver) — gitignored
```

## Development

### Prerequisites

- Go 1.24+
- Node.js 18+ and npm
- Docker
- [Task](https://taskfile.dev/)

### Setup

```bash
# Install Go and Node dependencies, verify build
task setup
```

TLS certificates are provisioned on demand by the gateway containers (their dev-target images run `certgen` against the shared `testbed_certs` volume). To pull the dev CA out of the testbed for browser/IMAP-client trust:

```bash
cd .workspace/testbed && task ca:fetch    # writes ./ca.crt
```

### Building

```bash
# Build all Go binaries (output to build/)
task build

# Build individual components
task build:api
task build:gateways
task build:console
task build:tools
```

Build artifacts are written to `build/{api,gateways,console,tools}/`.

### Running Tests

```bash
# Unit tests
task test

# Unit tests with coverage report
task test:coverage

# End-to-end tests (requires running containers)
task test:e2e

# All tests
task test:all
```

### Local Development

```bash
# Start the full restmail (mail3.test) product stack
task dev                       # alias for restmail:mail3:up

# Drive a single container
task api:up                    # build + run the API
task smtp-gateway:up           # build + run the SMTP gateway
task webmail:up
task admin:up
# ... same up/down/restart/logs pattern for every service

# Tail logs for any container
task api:logs
task smtp-gateway:logs

# Tear it all down
task restmail:mail3:down
```

Every container has its own `tasks/<service>.yml`, so you can iterate on one service without restarting the whole stack. `MODE=dev` (the default) builds dev-target images with hot-reload bind mounts; `MODE=prod` builds the prod target with no bind mounts.

### Code Quality

```bash
task fmt       # Format Go code
task vet       # Run go vet
task lint      # Run golangci-lint
task tidy      # Tidy and verify Go modules
```

## Configuration

All configuration is done via environment variables. The API, gateways, and tools all share the same config loader.

### Core Variables

| Variable            | Default                        | Description                          |
|---------------------|--------------------------------|--------------------------------------|
| `DB_HOST`           | `localhost`                    | PostgreSQL host                      |
| `DB_PORT`           | `5432`                         | PostgreSQL port                      |
| `DB_NAME`           | `restmail`                     | Database name                        |
| `DB_USER`           | `restmail`                     | Database user                        |
| `DB_PASS`           | `restmail`                     | Database password                    |
| `API_PORT`          | `8080`                         | REST API listen port                 |
| `API_HOST`          | `0.0.0.0`                      | REST API bind address                |
| `JWT_SECRET`        | `dev-secret-change-in-production` | JWT signing key (required in prod) |
| `MASTER_KEY`        | *(empty)*                      | AES key for encrypting private keys at rest (required in prod) |
| `ENVIRONMENT`       | `development`                  | `development`, `production`, or `test` |
| `LOG_LEVEL`         | `info`                         | Logging level (`debug`, `info`, `warn`, `error`) |

### TLS and Certificates

| Variable            | Default | Description                                    |
|---------------------|---------|------------------------------------------------|
| `TLS_CERT_PATH`     | *(empty)* | Path to TLS certificate file                 |
| `TLS_KEY_PATH`      | *(empty)* | Path to TLS private key file                 |
| `TLS_CERT_DIR`      | *(empty)* | Directory with per-domain cert/key pairs for SNI |
| `ACME_ENABLED`      | `false`   | Enable ACME/Let's Encrypt auto-renewal       |
| `ACME_EMAIL`        | *(empty)* | Contact email for ACME account               |
| `ACME_STAGING`      | `false`   | Use Let's Encrypt staging directory          |

### Gateway Variables

| Variable                      | Default           | Description                   |
|-------------------------------|--------------------|-------------------------------|
| `GATEWAY_HOSTNAME`            | `mail3.test`       | Hostname announced by gateways |
| `API_BASE_URL`                | `http://localhost:8080` | Internal API URL for gateways |
| `SMTP_PORT_INBOUND`           | `25`               | SMTP inbound port             |
| `SMTP_PORT_SUBMISSION`        | `587`              | SMTP submission port          |
| `SMTP_PORT_SUBMISSION_TLS`    | `465`              | SMTP implicit TLS port        |
| `IMAP_PORT`                   | `143`              | IMAP port                     |
| `IMAP_TLS_PORT`               | `993`              | IMAP implicit TLS port        |
| `POP3_PORT`                   | `110`              | POP3 port                     |
| `POP3_TLS_PORT`               | `995`              | POP3 implicit TLS port        |
| `QUEUE_WORKERS`               | `4`                | Number of outbound queue workers |
| `QUEUE_POLL_INTERVAL`         | `5s`               | Queue polling interval        |
| `CORS_ALLOWED_ORIGINS`        | `http://localhost:3000` | Comma-separated CORS origins |
| `PROXY_PROTOCOL_TRUSTED_CIDRS`| *(empty)*          | Comma-separated CIDRs for PROXY protocol |
| `DNS_PROVIDER`                | `dnsmasq`          | DNS provider (`dnsmasq`, `externaldns`, `manual`) |

## API Overview

The REST API exposes 108 operations across these resource groups:

- **Auth** -- Login, logout, token refresh
- **Accounts** -- Link/unlink mail accounts, test connections
- **Messages** -- List, read, send, reply, forward, delete, search, thread
- **Folders** -- Create, rename, delete, list
- **Drafts** -- Save, update, send
- **Attachments** -- Download, list per message
- **Contacts** -- CRUD, block senders, import, autocomplete
- **Vacation** -- Get/set/disable auto-responder
- **Sieve** -- Upload, validate, delete filter scripts
- **Calendar** -- RSVP to invites, list calendar events
- **Quarantine** -- List, release, delete quarantined messages
- **Admin: Domains** -- CRUD, DNS check
- **Admin: Mailboxes** -- CRUD
- **Admin: Aliases** -- CRUD
- **Admin: Pipelines** -- CRUD, test filters, view logs
- **Admin: Custom Filters** -- CRUD, validate, test
- **Admin: Queue** -- List, retry, bounce, bulk operations
- **Admin: DKIM** -- Key management
- **Admin: Certificates** -- TLS certificate CRUD
- **Admin: Bans** -- List, create, delete IP bans
- **Admin: Logs** -- Delivery and activity log queries
- **Admin: MTA-STS** -- Policy management per domain
- **Admin: TLS-RPT** -- View TLS report submissions
- **SSE** -- Real-time event stream per account
- **Health** -- `GET /api/health`
- **Metrics** -- `GET /metrics` (Prometheus)

### Documentation

- **Swagger UI**: http://localhost:8080/api/docs
- **OpenAPI spec**: http://localhost:8080/api/docs/openapi.yaml

### Authentication Example

```bash
# Login
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email": "admin@mail1.test", "password": "password"}'

# Use the returned access token
curl http://localhost:8080/api/v1/accounts \
  -H "Authorization: Bearer <access_token>"
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
| `recipient_check` | Check | Verify recipient exists |
| `sender_verify` | Check | Sender domain verification |
| `domain_allowlist` | Check | Domain-level allow/blocklist |
| `contact_whitelist` | Check | Per-user contact allowlist |
| `vacation` | Action | Auto-reply responder |
| `sieve` | Action | Sieve script execution |
| `webhook` | Action | HTTP webhook notifications |
| `duplicate` | Action | Fork message to webhook/queue |
| `javascript` | Action | Custom JS filter via Node.js sidecar |
| `rspamd` | Adapter | Rspamd spam scanning |
| `clamav` | Adapter | ClamAV virus scanning |

## Optional Capabilities

Spam/virus scanning, fail2ban, and rspamd come from the reference mail server stack ([`rest-mail/reference-mailserver`](https://github.com/rest-mail/reference-mailserver)) — bring up a reference instance to get them. Monitoring lives in this repo:

```bash
task monitoring:up      # Prometheus + Grafana + postgres-exporter
task monitoring:down
```

| Stack         | Comes From                  | Tasks                            |
|---------------|-----------------------------|----------------------------------|
| Monitoring    | this repo                   | `task monitoring:up\|down\|logs` |
| rspamd, fail2ban | `rest-mail/reference-mailserver` | `task mailserver:mail1:up` (etc.) |

## RESTMAIL Protocol

RESTMAIL introduces an SMTP extension for HTTP-based mail delivery between RESTMAIL-capable servers. When an outbound queue worker detects RESTMAIL support via EHLO, it upgrades the connection to HTTPS POST delivery, bypassing traditional SMTP data transfer. The protocol endpoints are:

- `GET /restmail/capabilities` -- Advertise RESTMAIL support
- `GET /restmail/mailboxes` -- Verify recipient mailbox exists
- `POST /restmail/messages` -- Deliver message via HTTP

## License

*TBD -- License information will be added here.*
