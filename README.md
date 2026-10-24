# RESTMAIL

A full-featured mail server platform that exposes email functionality through a REST API while maintaining protocol-level indistinguishability from traditional Postfix/Dovecot mail servers. Built in Go with a React webmail frontend, RESTMAIL gives you programmatic control over every aspect of email delivery, filtering, and administration.

The application itself is deliberately thin: the protocol engines and email-authentication logic live in a family of focused, standalone Go libraries (published under the [`rest-mail`](https://github.com/rest-mail) org), and this repo composes them — implementing each library's `Backend` interface on top of the REST API.

## The REST Mail constellation

Every repository in the [`rest-mail`](https://github.com/rest-mail) organization
belongs to the same project family: the server and web apps, mobile client,
website, protocol libraries, reference images, testbed, deployment and distribution.
[`constellation.json`](constellation.json) records the membership rule and each
repository's role. New organization repositories are included automatically in
live inventories, even before their role is recorded in the manifest.

```bash
# Offline repository snapshot; Python 3 is the only dependency.
python3 scripts/constellation.py repos

# Current membership and the full open-issue queue; requires authenticated gh.
python3 scripts/constellation.py repos --live
python3 scripts/constellation.py issues --include-related --json
```

For a terminal overview of the checked-in core repository snapshot, use Bash,
authenticated `gh` and `jq` with these Chore tasks:

```bash
chore overview:summary  # One row per repository: issues, age and open/closed PR counts.
chore overview:list     # Issues with links to closing or mentioning pull requests.
chore overview:tsv      # Tab-separated issue rows.
```

The script also accepts project filters, for example
`scripts/am-overview --summary --project rest-mail-server`. Its repository list
is in `scripts/constellation-repositories.sh` and is checked against the manifest
by CI. Fetch failures produce a visible incomplete result and exit 4; rate
limits stop the run with exit 5. Use `repos --live` above to discover organization
repositories not yet recorded in the snapshot. The linked-PR view inspects up
to 20 closing references and 50 cross-reference events per issue.

Both repository and issue queries follow all GitHub pages. A failed query exits
nonzero instead of reporting an incomplete inventory as an empty queue. Archived
repositories remain visible and are marked in JSON output.

Related projects have explicit limits. Vertrag contributes only its registered
REST Mail contract-testing issues (#71, #73 and #74). The infrastructure repository
records only REST Mail deployment, DNS and certificate work; the current web-app
rollout is tracked in [server #365](https://github.com/rest-mail/rest-mail-server/issues/365).
Its Gitea issue tracker is not queried by this GitHub inventory. Adding a related
repository requires a scope description and specific issue numbers; it does not
add that repository's entire backlog. `repos --include-related --json` includes
these scope descriptions.

Agents should select one bounded issue from this queue, check dependencies and
existing worktrees, and verify its done conditions before closing it. Membership
does not authorize spawning workers, changing credentials or deploying services;
the owner's current agent and timebox instructions still apply. Work across
repositories uses separate worktrees and records any library release or downstream
version bump needed by the server or clients.

## Features

**Core Email**
- Full REST API for sending, receiving, searching, threading, and managing email
- SMTP, IMAP, and POP3 gateways that behave identically to Postfix/Dovecot
- MIME parsing with attachment handling, inline images, and iCalendar support
- Calendar invite detection, RSVP responses, and `.ics` composition
- Drafts, forwarding, folder management, and quota tracking

**Security and Authentication**
- TLS with SNI support and DB-backed certificate management
- DKIM signing, SPF verification, DMARC policy enforcement with rua aggregate reporting
- ARC chain verification, and an ARC seal on the mail REST Mail forwards
- MTA-STS and TLS-RPT (RFC 8460/8461), including outbound MTA-STS policy enforcement
- ACME/Let's Encrypt auto-renewal
- JWT authentication with bcrypt passwords (Dovecot-compatible `{BLF-CRYPT}` format)
- App passwords: named, revocable passwords for mail clients and automations, over IMAP, POP3, SMTP and the REST API
- Capability-based RBAC enforced on every admin route
- PROXY protocol support for reverse proxies (HAProxy, nginx)
- Connection rate limiting, auth ban tracking, fail2ban integration

**Pipeline Engine**
- 20+ built-in filters: email authentication (SPF/DKIM/DMARC/ARC), greylisting, rate and size limits, sender/recipient verification, allow/blocklists, header validation, vacation responder, and more
- Custom filter support via JavaScript (Node.js sidecar) and Sieve scripts
- Configurable inbound and outbound pipelines per domain
- Quarantine with release/delete management and periodic digest emails

**Administration**
- Domain, mailbox, and alias CRUD via REST API
- Queue management with retry, bounce, and bulk operations
- Delivery log queries and a hash-chained audit log
- IP ban management
- OpenAPI 3.1 spec (146 operations) with Swagger UI
- Console admin tool (bubbletea) with inbox, search, compose, and live status
- React webmail with rich text editor, contacts, vacation settings
- Prometheus metrics endpoint with Grafana dashboards
- Email client auto-configuration (Mozilla autoconfig, Microsoft Autodiscover)

## The Library Family

RESTMAIL follows an adopt-vs-extract rule: adopt mature external libraries where they are good; extract our own only where there is a real ecosystem gap. The result is eight focused libraries, all MIT-licensed and each usable entirely on its own:

| Library | What it is |
|---------|------------|
| [`rest-mail/go-pop3`](https://github.com/rest-mail/go-pop3) | RFC 1939 POP3 server engine — bring a `Backend`, it speaks the protocol |
| [`rest-mail/go-imap`](https://github.com/rest-mail/go-imap) | IMAP server engine with the same bring-a-`Backend` design |
| [`rest-mail/go-dkim`](https://github.com/rest-mail/go-dkim) | RFC 6376 DKIM signing + verification, zero external deps |
| [`rest-mail/go-spf`](https://github.com/rest-mail/go-spf) | RFC 7208 SPF evaluation, zero external deps |
| [`rest-mail/go-arc`](https://github.com/rest-mail/go-arc) | RFC 8617 ARC chain verification and sealing |
| [`rest-mail/go-dmarc`](https://github.com/rest-mail/go-dmarc) | RFC 7489 DMARC policy, alignment, and rua report XML |
| [`rest-mail/go-mtasts`](https://github.com/rest-mail/go-mtasts) | RFC 8461 MTA-STS policy fetch/parse/enforce |
| [`rest-mail/go-sieve`](https://github.com/rest-mail/go-sieve) | RFC 5228 Sieve parser/interpreter |

Where the ecosystem already has a good answer, we use it:

- **SMTP** runs on [`rest-mail/go-smtp`](https://github.com/rest-mail/go-smtp), a fork of [`emersion/go-smtp`](https://github.com/emersion/go-smtp) that adds an `ExtraCaps` hook so the gateway can advertise the custom `RESTMAIL` EHLO capability.
- **MIME** parsing is [`emersion/go-message`](https://github.com/emersion/go-message); **SASL** is [`emersion/go-sasl`](https://github.com/emersion/go-sasl).

This repo's gateways are thin adapters: `internal/gateway/pop3` and `internal/gateway/imap` implement each library's `Backend`/`Mailbox` interfaces by mapping REST API responses onto the library's neutral types, and `internal/gateway/smtp` provides go-smtp's backend/session over the same API.

## Quick Start

### Prerequisites

- Docker
- [`chore`](https://github.com/antimatter-studios/chore) — required (the repo is driven entirely by
  `chores.yml`, no docker-compose):

  ```bash
  brew install antimatter-studios/tap/chore
  ```

  It reads go-task's format but gives a task real arguments, which is why this repo needs it:
  `chore instance:up mail4.test`, or `chore instance:up --config mail4.test`. go-task can still
  parse the file, but it ignores `args:` — so a task that takes a parameter silently falls back to
  its default there.

### Start the Stack

The stack joins the `rest-mail/testbed` substrate (mailnet network + dnsmasq + certs volume). Bring the testbed up first, then start the restmail product stack.

```bash
# 1. First run only — clone the testbed into .workspace/
chore testbed:init

# 2. Bring up the testbed substrate (network, certs volume, dnsmasq)
chore testbed:up

# 3. Start the restmail.test product stack
chore instance:up   # CONFIG defaults to restmail.test
```

`chore instance:up` brings up PostgreSQL, the JS filter sidecar, the REST API, the SMTP/IMAP/POP3 gateways, webmail, and the admin UI, seeds the database, and finishes by printing `chore status` — the live view of what is running and on which URLs. Each container is driven by its own file under [`tasks/`](tasks/), included from the root [`chores.yml`](chores.yml).

### Seed Test Data

Seeding happens automatically during `instance:up`. To re-run or reset:

```bash
chore db:seed    # idempotent — seed sample mailboxes and RBAC (inside the api container)
chore db:reset   # destructive — drop the postgres volume, recreate, re-seed
```

### Access Services

HTTP services are routed by a reverse proxy on `:80` reading `docker-proxy.*` container labels — [`ddt`](https://github.com/antimatter-studios/docker-dev-tools) provides that proxy plus `*.localhost` DNS (`chore project:proxy:help` covers the setup, `chore project:check` verifies it). Mail protocol ports are published directly on the host.

| Service        | URL / Port                                |
|----------------|-------------------------------------------|
| REST API       | http://restmail.localhost/api (direct: http://localhost:8080) |
| Swagger UI     | http://localhost:8080/api/docs            |
| Webmail        | http://restmail.localhost/webmail         |
| Admin UI       | http://restmail.localhost/admin           |
| SMTP           | localhost:25 / 587 / 465                  |
| IMAP           | localhost:143 / 993                       |
| POP3           | localhost:110 / 995                       |
| Health check   | http://localhost:8080/api/health          |
| Metrics        | http://localhost:8080/metrics             |

`chore status` prints the authoritative table for your machine.

### Test Domains

This repo ships no committed instance — the primary test instance (**restmail.test**) is owned by the testbed (`configs/restmail`, which `chore testbed:init` copies into `config/restmail.test`), and further instances are scaffolded on demand under the same `config/<domain>/` layout (see [Instances](#instances)). For traditional reference instances (mail1.test, mail2.test, ...) running Postfix + Dovecot, see [`rest-mail/reference-mailserver`](https://github.com/rest-mail/reference-mailserver). Reference instances are launched one at a time, directly against that repo's Taskfile — you decide how many to run:

```bash
chore mailserver:init                                    # first run only — clone it
task -d .workspace/reference-mailserver up CONFIG=mail1
```

| Domain         | Type        | Description                        |
|----------------|-------------|------------------------------------|
| restmail.test  | RESTMAIL    | Go gateways backed by REST API     |

### Test Accounts

After seeding, the following accounts are available:

**The admin (for the console and the admin webmail):**
- Address: `admin@restmail.test` (an account like the others, signing in by its address)
- Password: `admin123!@`
- Role: `superadmin` at `*` (full access)

**restmail.test accounts:**
| Email | Password | Display Name |
|-------|----------|--------------|
| `eve@restmail.test` | `password123` | Eve Wilson |
| `frank@restmail.test` | `password123` | Frank Miller |
| `postmaster@restmail.test` | `password123` | Postmaster |

**restmail.test Aliases:**
- `info@restmail.test` → `eve@restmail.test`

**RBAC System:**
The server creates its roles on start, and every account's access is a grant
of one of them at a scope (`*`, `domain:<name>` or `mailbox:<address>`):
- **Roles**: `superadmin`, `admin`, `readonly`, and `owner` (read, write and send mail on one account, and manage it)
- **Capabilities**: permissions covering domains, mailboxes, users, pipelines, messages, queue, bans and mail — enforced per route
- See [internal/bootstrap/bootstrap.go](internal/bootstrap/bootstrap.go) for the complete capability list

## Admin Tools

### Console

Interactive terminal admin tool (built with bubbletea) for managing the mail server:

```bash
# Build the console (auto-detects your OS/architecture)
chore build:console

# Run the console (executes inside the api container)
chore run:console
```

**Features:**
- RBAC-aware access control (displays your role and capabilities)
- Domain, mailbox, and alias management
- Queue operations
- Live status monitoring

**Authentication:** Signs in as the admin account seeded by `chore db:seed` (`admin@<domain>`), by its address and password.

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

This repo ships the RESTMAIL product only. Postfix/Dovecot reference instances live in [`rest-mail/reference-mailserver`](https://github.com/rest-mail/reference-mailserver) and dnsmasq lives in [`rest-mail/testbed`](https://github.com/rest-mail/testbed). All three projects share the `mailnet` Docker network and the `certs` volume that the testbed provides.

The gateways implement no wire protocol of their own — each is a `Backend` implementation handed to an external server library, which speaks the protocol — and message parsing is delegated to `internal/mime` (go-message) rather than hand-rolled per gateway:

```
                          Clients
                    (Thunderbird, Outlook, curl)
                             |
              +--------------+--------------+
              |              |              |
         SMTP:25/465     IMAP:993       POP3:995    
              |              |              |
     +--------+-------+ +----+---------+ +--+-------------+
     | go-smtp engine | | rest-mail/   | | rest-mail/     |
     | (rest-mail     | | imap engine  | | pop3 engine    |
     |  fork)         | |              | |                |
     +--------+-------+ +----+---------+ +--+-------------+
     | Backend        | | Backend      | | Backend        |
     | adapter (this  | | adapter      | | adapter        |
     | repo)          | |              | |                |
     +--------+-------+ +----+---------+ +--+-------------+
              |              |              |
              +------+-------+------+-------+
                            |
                            v
                     +------+------+
                     | REST API    |
                     | :8080 (Go,  |
                     | chi + gorm) |
                     +------+------+
                            |
                     +------+------+
                     | Pipeline    |     filters use rest-mail/
                     | Engine      | --- dkim, arc, dmarc,
                     +------+------+     mtasts, sieve
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
     [testbed]              [reference-mailserver]      [website]
     - dnsmasq DNS          - Postfix + Dovecot         - marketing site
     - certs volume           (mail1.test, mail2.test)    (clone-on-demand)
     - mailnet network      - rspamd, fail2ban
                            - postgres
```

Inbound mail flows through the pipeline engine (authentication, policy, and action filters) before delivery; outbound mail is queued and delivered by SMTP queue workers, which upgrade to the RESTMAIL HTTP protocol when the peer advertises it. The API pushes real-time changes to clients over SSE (per-account event streams, access-token authenticated).

### Directory Layout

```
cmd/
  api/              REST API server (main entry point)
  smtp-gateway/     SMTP protocol gateway
  imap-gateway/     IMAP protocol gateway
  pop3-gateway/     POP3 protocol gateway
  console/          Terminal admin UI (bubbletea) with RBAC
  instance/         Config manifest renderer/scaffolder, instance cert issuance (see Instances)
  migrate/          Database migration runner
  rotate-key/       MASTER_KEY rotation tool
  seed/             Test data seeder (domain, accounts, the admin account)
  website/          Project website server
internal/
  acme/             ACME (Let's Encrypt) certificate auto-provisioning
  api/              Handlers, middleware, routes, SSE, response helpers
  auth/             JWT + bcrypt authentication
  config/           Environment variable loading
  crypto/           AES-256-GCM encryption helpers
  db/models/        GORM model structs (30+ models)
  digest/           Quarantine digest emails + quota reconciliation workers
  dmarcreport/      Periodic DMARC rua aggregate report worker
  dns/              Pluggable DNS providers (dnsmasq, externaldns, manual)
  gateway/          Backend adapters for the protocol libraries + queue worker
  instance/         Config manifest loading and rendering
  mail/             Message-ID generation
  metrics/          Prometheus instrumentation
  mime/             MIME handling over go-message, iCalendar support
  pipeline/         Processing engine, filter registry, 20+ built-in filters
  seed/             Seed fixture definitions
  console/          Console screens and components
webmail/            React frontend (Vite + TypeScript + Tailwind + shadcn/ui)
admin/              Admin UI (React)
projects/           Dockerfiles for the gateways, js-filter sidecar, dnsmasq fragments
helm/               Helm chart for restmail
monitoring/         Prometheus config, alerting rules, Grafana dashboards
tasks/              Per-service task files (one per container) included from root chores.yml
tests/e2e/          End-to-end test suite (14 stages)
.workspace/         Sibling repos cloned on demand (testbed, reference-mailserver, website) — gitignored
```

## Instances

Everything about a running RESTMAIL deployment — hostname, IPs, ports, credentials — comes from a manifest, not from hardcoded config.

Two words, kept distinct: a **config** is a runnable definition, and an **instance** is what exists once you run one. Definitions live under one authoritative layout, `config/<domain>/` (`manifest.yml` + the rendered `config.env`), and are authored with the `config:` verbs — `config:scaffold`, `config:render`, `config:check`. Running stacks are driven with the `instance:` verbs — `instance:up`, `instance:down`, `instance:restart`. `CONFIG` selects which definition to drive (default `restmail.test`, whose fixture is owned by the testbed and copied into `config/restmail.test` by `chore testbed:init`); `CONFIG_DIR` can point anywhere. `INSTANCE=`/`INSTANCE_DIR=` remain accepted as aliases.

Config is always local, never read out of `.workspace/`. Those clones exist so you can modify, push and build the sibling repos — they are build-time inputs, so nothing the running stack reads may live inside one; otherwise a branch switch changes how the stack boots and a `git clean` deletes your config. `chore project:check:runtime` enforces this: it fails if a config dir is a symlink, if the loaded `config.env` resolves under `.workspace/`, or if a running rest-mail container bind-mounts a clone.

New configs are **secure-by-default**: the scaffolded manifest sets
`internal_mtls: true` and `instance:new` provisions the gateway→API mTLS certs
automatically (see [Internal mTLS](#internal-mtls-gateway--api)).

```bash
# Spin up a second RESTMAIL instance end-to-end
# (scaffold → cert → mTLS → DNS → up → DKIM):
chore instance:new DOMAIN=mail4.test

# Drive an existing config's instance
CONFIG=mail4.test chore instance:up
CONFIG=mail4.test chore instance:down

# Manifest workflow
chore config:render     # manifest.yml → config.env (after editing a manifest)
chore config:check      # fail if config.env drifted from its manifest
```

## Development

### Prerequisites

- Go 1.25+
- Node.js 18+ and npm
- Docker
- [Task](https://taskfile.dev/)

### Setup

```bash
# Install Go and Node dependencies, verify build
chore setup
```

TLS certificates are provisioned per config into the shared `testbed_certs` volume (`chore instance:certs:issue`, run automatically by `instance:new`), and the internal mTLS ones into `<project>-internal-mtls` (`chore instance:mtls:issue`). Both hold a CA's private key, so no service mounts them: each service's `up` copies the files that service needs into a volume of its own, `<container>-certs` ([tasks/stage-certs.sh](tasks/stage-certs.sh)). To pull the dev CA out of the testbed for browser/IMAP-client trust:

```bash
cd .workspace/testbed && chore ca:fetch    # writes ./ca.crt
```

### Building

```bash
# Build all Go binaries (output to build/)
chore build

# Build individual components
chore build:api
chore build:gateways
chore build:console
chore build:tools
```

Build artifacts are written to `build/{api,gateways,console,tools}/`.

### Running Tests

```bash
# Unit tests
chore test

# Unit tests with coverage report
chore test:coverage

# Full e2e run: bring up the topology (testbed + reference mail1/mail2 +
# restmail.test), run the 13-stage suite, tear it down
chore e2e

# e2e against an already-running topology
chore test:e2e

# All tests
chore test:all
```

The e2e suite runs inside a container attached to the testbed mailnet with the testbed dnsmasq as its only resolver — it exercises the stack as just another user of the simulated internet. Its 14 stages cover infrastructure, traditional cross-domain mail, gateway inbound/outbound, protocol indistinguishability, the RESTMAIL upgrade, webmail and console flows, database consistency, email-auth verification, queue retry, bounce DSNs, IMAP IDLE, and app passwords.

### Local Development

```bash
# Start the full restmail.test product stack
chore instance:up               # CONFIG defaults to restmail.test

# Drive a single container
chore api:up                    # build + run the API
chore smtp-gateway:up           # build + run the SMTP gateway
chore webmail:up
chore admin:up
# ... same up/down/restart/logs pattern for every service

# Tail logs for any container
chore api:logs
chore smtp-gateway:logs

# Tear it all down
chore instance:down
```

The API and SMTP/IMAP/POP3 gateway Go dev containers run with a read-only root
filesystem. The source overlay at `/app` stays writable for development; Air's
executables use a separate 128 MiB tmpfs at `/app/tmp`. A 1 GiB tmpfs at `/tmp`
holds Go's build cache, module cache, checksum state, and temporary files. These
mounts count toward the existing container memory limits and reset when the
container is recreated. Go reads the image's preloaded module archive first,
then falls back to the public Go proxy and direct downloads for new dependencies.
Certificate, attachment, and log mounts retain their existing permissions and
locations. These dev flags do not change the `prod` launch commands.

The webmail and admin Vite dev containers also use a read-only root
filesystem, with a 128 MiB non-executable tmpfs at `/tmp`. Their source overlay
at `/app` and named `/app/node_modules` volume stay writable for generated
routes and Vite caches. The separate dependency installer still populates the
modules volume before startup. Scratch space counts toward the existing memory
limit and resets when the container is recreated.

Every container has its own `tasks/<service>.yml`, so you can iterate on one service without restarting the whole stack. `MODE=dev` (the default) builds dev-target images with hot-reload bind mounts; `MODE=prod` builds the prod target with no bind mounts.

### Code Quality

```bash
chore fmt       # Format Go code
chore vet       # Run go vet
chore lint      # Run golangci-lint
chore tidy      # Tidy and verify Go modules
```

## Configuration

All configuration is done via environment variables. The API, gateways, and tools all share the same config loader. In the containerized stack these are populated from the config's manifest (see [Instances](#instances)).

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
| `GATEWAY_HOSTNAME`            | `localhost`        | Hostname announced by gateways |
| `API_BASE_URL`                | `http://localhost:8080` | Internal API URL for gateways |
| `SMTP_PORT_INBOUND`           | `25`               | SMTP inbound port             |
| `SMTP_PORT_SUBMISSION`        | `587`              | SMTP submission port          |
| `SMTP_PORT_SUBMISSION_TLS`    | `465`              | SMTP implicit TLS port        |
| `SMTP_MAX_MESSAGE_SIZE`       | `10485760` (10 MiB) | Maximum accepted message size in bytes — drives both the EHLO `SIZE` advertisement and DATA enforcement. A maximum always exists: zero/negative values are a startup error, and values over 100 MiB log a startup warning (messages are buffered and parsed in memory) |
| `IMAP_PORT`                   | `143`              | IMAP port                     |
| `IMAP_TLS_PORT`               | `993`              | IMAP implicit TLS port        |
| `POP3_PORT`                   | `110`              | POP3 port                     |
| `POP3_TLS_PORT`               | `995`              | POP3 implicit TLS port        |
| `QUEUE_WORKERS`               | `4`                | Number of outbound queue workers |
| `QUEUE_POLL_INTERVAL`         | `5s`               | Queue polling interval        |
| `SUPPRESSION_TTL`             | `2160h` (90 days)  | How long a recipient that hard bounced stays suppressed for the sending domain |
| `MTASTS_ENFORCE`              | `true`             | Enforce recipient MTA-STS policies on outbound delivery |
| `CORS_ALLOWED_ORIGINS`        | `http://localhost:3000` | Comma-separated CORS origins |
| `PROXY_PROTOCOL_TRUSTED_CIDRS`| *(empty)*          | Trusted front-proxy CIDRs. Also gates HTTP `X-Forwarded-For`: a forwarded client IP is honored only when the direct peer is listed; empty = trust no proxy, use the real socket peer |
| `DNS_PROVIDER`                | `dnsmasq`          | DNS provider (`dnsmasq`, `externaldns`, `manual`) |

### Internal mTLS (gateway → API)

A few API routes are called by the protocol gateways with no user token —
recipient existence checks (`GET /api/mailboxes`) and inbound message delivery
(`POST /api/v1/messages/deliver`). These are machine-to-machine calls,
historically protected only by network isolation. Internal mTLS hardens that
trust boundary: an internal CA issues a client certificate to the gateways, and
the API verifies that certificate instead of a token — the cert is, in effect,
the gateway's password. It authenticates the gateway *service* (a machine
identity), separate from user JWTs and admin RBAC.

**Two-layer default, on purpose.** The compiled-in config default is **off**
(`INTERNAL_MTLS_ENABLED=false`): a bare binary — and any existing,
non-provisioned deployment — keeps the routes on the public listener, tokenless,
so an upgrade never hard-breaks. "On by default" is delivered one layer up, at
the **instance** layer: a freshly scaffolded manifest sets `internal_mtls: true`
and `chore instance:new` auto-provisions the certs, so every instance of a new config comes up
with the gateway→API handshake enforced. Existing manifests that don't set the
flag are unaffected. This is the secure-by-default-for-new-deployments posture
without breaking the bare binary.

**Production boot refuses the exposed posture.** Because those two routes trust a
caller-supplied `client_ip`/`helo_name` and are tokenless when served on the
public listener, running with `INTERNAL_MTLS_ENABLED=false` in production
(`ENVIRONMENT=production`) is refused at boot: the API exits with an error naming
the exposure. Enable internal mTLS, or — if a firewall / Kubernetes NetworkPolicy
already restricts the API port to trusted gateways — acknowledge that out-of-band
trust boundary with `INTERNAL_ROUTES_ALLOW_PUBLIC=true`. In development/test (the
testbed and e2e default) the same posture only logs a warning and boots, so local
use is unchanged.

When **enabled**, the API serves only those two routes on a dedicated listener
(`INTERNAL_MTLS_PORT`, default `8443`) that requires
`tls.RequireAndVerifyClientCert` against the internal CA, and withholds them
from the public listener. A missing / wrong-CA / expired client certificate is
rejected at the TLS handshake, before any HTTP handler runs.

Routing is **per-endpoint, not per-client**: only `CheckMailbox` and
`DeliverMessage` use the internal listener (`API_INTERNAL_BASE_URL`). `Login`
and every token-authenticated user route (folders, messages, quota, …) stay on
the public listener via `API_BASE_URL` — they are already authenticated by JWT
or user credentials and don't need mTLS, and submission/IMAP/POP3 users don't
hold gateway client certs. **Do not repoint `API_BASE_URL`** at the internal
listener, or those user routes would 404 and IMAP/POP3 retrieval and SMTP
submission would break.

| Variable                    | Default   | Side    | Description                              |
|-----------------------------|-----------|---------|------------------------------------------|
| `INTERNAL_MTLS_ENABLED`     | `false`   | both    | Master switch                            |
| `INTERNAL_ROUTES_ALLOW_PUBLIC` | `false` | API   | Acknowledge serving the two machine routes unauthenticated on the public listener (mTLS off); required to boot in production without mTLS |
| `INTERNAL_MTLS_PORT`        | `8443`    | API     | Dedicated internal listener port         |
| `API_BASE_URL`              | `http://localhost:8080` | gateway | PUBLIC listener (Login + user routes) — stays public |
| `API_INTERNAL_BASE_URL`     | *(empty)* | gateway | Internal mTLS listener (the two machine routes only) |
| `INTERNAL_MTLS_CA_CERT`     | *(empty)* | both    | Internal CA (anchors the trust domain)   |
| `INTERNAL_MTLS_SERVER_CERT` | *(empty)* | API     | Server cert for the internal listener    |
| `INTERNAL_MTLS_SERVER_KEY`  | *(empty)* | API     | Server key                               |
| `INTERNAL_MTLS_CLIENT_CERT` | *(empty)* | gateway | Gateway client cert (machine identity)   |
| `INTERNAL_MTLS_CLIENT_KEY`  | *(empty)* | gateway | Gateway client key                       |

Mint the material (dedicated CA + API server cert + gateway client cert) with
`instance mtls-issue` — idempotent and CA-preserving (it re-mints the server leaf
if the requested SANs drift, e.g. the api IP changes on a persistent volume):

```
go run ./cmd/instance mtls-issue -out /internal-mtls \
  -server-ip 127.0.0.1,<api-ip> -server-dns api,localhost,<hostname>
```

That directory then holds the internal CA's private key, so mount it into
nothing that serves: give the API the CA certificate and its server keypair,
and each gateway the CA certificate and the client keypair. `chore
instance:mtls:issue` does exactly that with its own volume, and the Helm chart
keeps no CA key at all.

When enabled, set the gateways' `API_INTERNAL_BASE_URL` to the https internal
listener (e.g. `https://<api-host>:8443`) and leave `API_BASE_URL` on the public
listener. Enabling it is fail-closed: if the feature is on but a cert path or
`API_INTERNAL_BASE_URL` is unset/unreadable, the gateway refuses to start; if
the API has it on but a gateway does not, the gateway's tokenless calls to the
now-absent public route return 404 and delivery fails visibly rather than
silently bypassing the check.

**New instances** get this for free: `chore config:scaffold` writes
`internal_mtls: true` into the manifest and `chore instance:new` runs
`chore instance:mtls:issue` automatically before bring-up, so a fresh instance is
mTLS-on with its certs already provisioned. An older manifest opts in by adding
`internal_mtls: true` and re-rendering.

**Testbed**: `chore e2e:up` provisions the certs (`chore instance:mtls:issue`) and
brings `restmail.test` up with internal mTLS forced on, so the e2e gate exercises
the real handshake. The `restmail.test` manifest is testbed-owned (external to
this repo); its `configs/restmail/manifest.yml` should also carry
`internal_mtls: true` for parity outside the e2e run.

## API Overview

The REST API exposes 146 operations across these resource groups:

- **Auth** -- Login, logout, token refresh
- **Accounts** -- List the accounts a session reaches, create one, link one by its password, unlink, app passwords
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
- **Admin: Logs** -- Delivery log queries
- **Admin: MTA-STS** -- Policy management per domain
- **Admin: TLS-RPT** -- View TLS report submissions
- **SSE** -- Real-time event stream per account
- **Health** -- `GET /api/health`
- **Metrics** -- `GET /metrics` (Prometheus)

Every route is guarded by capability-based RBAC — each requires the matching capability (e.g. `domains:write`) from the caller's grants, at a scope covering what it acts on.

### Documentation

- **Swagger UI**: http://localhost:8080/api/docs
- **OpenAPI spec**: http://localhost:8080/api/docs/openapi.yaml

### Authentication Example

```bash
# Login
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email": "eve@restmail.test", "password": "password123"}'

# Use the returned access token
curl http://localhost:8080/api/v1/accounts \
  -H "Authorization: Bearer <access_token>"
```

### Accounts

An account is a mailbox: its address is what signs in, and its id is the
`{id}` of every `/api/v1/accounts/{id}/…` route. It has 0..n passwords, an
authenticator if it enrolled one, and app passwords. What else a session
reaches comes from grants: `GET /api/v1/accounts` lists its own account first,
then every account it holds a mail grant on.

```bash
# Make an account (needs mailboxes:write on its domain). The creator owns it:
# reads, writes and sends its mail, and manages it. With no password, nothing
# can sign in to it until one is set or an app password is made for it.
curl -X POST http://localhost:8080/api/v1/accounts \
  -H "Authorization: Bearer <access_token>" -H "Content-Type: application/json" \
  -d '{"address": "chris@restmail.test", "display_name": "Chris"}'

# Link an account someone else made, by proving its password (and its 2FA code,
# as totp_code, if it has 2FA): the session's account becomes an owner of it
curl -X POST http://localhost:8080/api/v1/accounts/link \
  -H "Authorization: Bearer <access_token>" -H "Content-Type: application/json" \
  -d '{"address": "frank@restmail.test", "password": "password123"}'

# Unlink it: removes the session's own grants on it
curl -X DELETE http://localhost:8080/api/v1/accounts/<account id> -H "Authorization: Bearer <access_token>"
```

### App passwords

An account can give each mail client or automation an app password of its own:
`rm_<public id>_<secret>`, 59 letters, digits and underscores. It is shown once,
when it is made; the server keeps only a SHA-256 of the secret.

```bash
# Make one, signed in with a password (and a 2FA code, if the account has 2FA),
# entering that password again: for the session's own account, or one it owns
curl -X POST http://localhost:8080/api/v1/accounts/<account id>/credentials \
  -H "Authorization: Bearer <access_token>" -H "Content-Type: application/json" \
  -d '{"name": "Thunderbird", "expires_at": "2027-01-01T00:00:00Z", "password": "<your password>"}'

# Use it as the password in a mail client (IMAP, POP3, SMTP), or with HTTP Basic
curl -u 'eve@restmail.test:rm_…' http://localhost:8080/api/v1/accounts/<account id>/folders

# List them (never the secret), and revoke one: the next request made with it is refused
curl http://localhost:8080/api/v1/accounts/<account id>/credentials -H "Authorization: Bearer <access_token>"
curl -X DELETE http://localhost:8080/api/v1/accounts/<account id>/credentials/<id> -H "Authorization: Bearer <access_token>"
```

An app password reaches only its own account, to read, change and send mail. It
cannot sign in at `/api/v1/auth/login` (the app and the webmail), change a
password or 2FA, link an account, or list, make or revoke app passwords. An
account with 2FA signs mail clients in with app passwords only: its password
alone is refused over IMAP, POP3 and SMTP. A new
password for the account revokes all of them, as it ends every session. Making
one takes the session's own current password, entered again (`403 reauth_required`
without it, `403 invalid_password` when it is wrong, never `401`), so a session from before
a new password, still valid until its access token expires, can't make one
that outlives the change. Each creation, refused creation, revocation, refused
app password and sign-in over IMAP, POP3 or SMTP is recorded in the audit log.

## Pipeline Filters

The pipeline engine processes emails through configurable filter chains. Built-in filters:

| Filter | Description |
|--------|-------------|
| `spf_check` | SPF verification (via `rest-mail/go-spf`) |
| `dkim_verify` | DKIM signature verification (via `rest-mail/go-dkim`) |
| `dmarc_check` | DMARC policy enforcement (via `rest-mail/go-dmarc`) |
| `arc_verify` | ARC chain verification (via `rest-mail/go-arc`) |
| `greylist` | Greylisting with DB-backed tracking |
| `rate_limit` | Per-sender/domain rate limiting |
| `size_check` | Message size enforcement |
| `recipient_check` | Verify recipient exists |
| `sender_verify` | Sender domain verification |
| `domain_allowlist` | Domain-level allow/blocklist |
| `contact_whitelist` | Per-user contact allowlist |
| `header_validate` | Structural header validation |
| `header_cleanup` | Strip/rewrite internal headers |
| `extract_attachments` | Extract and store attachments |
| `vacation` | Auto-reply responder |
| `sieve` | Sieve script execution (via `rest-mail/go-sieve`) |
| `webhook` | HTTP webhook notifications |
| `duplicate` | Fork a copy of the message to a webhook |
| `javascript` | Custom JS filter via Node.js sidecar |
| `rspamd` | Rspamd spam scanning (adapter) |
| `clamav` | ClamAV virus scanning (adapter) |

## Optional Capabilities

Spam/virus scanning (rspamd) and fail2ban come from the reference mail server stack ([`rest-mail/reference-mailserver`](https://github.com/rest-mail/reference-mailserver)) — bring up a reference instance to get them (`task -d .workspace/reference-mailserver up CONFIG=mail1`). Monitoring lives in this repo:

```bash
chore monitoring:up      # Prometheus + Grafana + postgres-exporter
chore monitoring:down
```

| Stack         | Comes From                  | Tasks                            |
|---------------|-----------------------------|----------------------------------|
| Monitoring    | this repo                   | `chore monitoring:up\|down\|logs` |
| rspamd, fail2ban | `rest-mail/reference-mailserver` | `task -d .workspace/reference-mailserver up CONFIG=mail1` |

## RESTMAIL Protocol

RESTMAIL introduces an SMTP extension for HTTP-based mail delivery between RESTMAIL-capable servers. The SMTP gateway advertises `RESTMAIL https://<host>/restmail` in its EHLO response; when an outbound queue worker sees that capability on a peer, it upgrades the connection to HTTPS POST delivery, bypassing traditional SMTP data transfer. The protocol endpoints are:

- `GET /restmail/capabilities` -- Advertise RESTMAIL support
- `GET /restmail/mailboxes` -- Verify recipient mailbox exists
- `POST /restmail/messages` -- Deliver message via HTTP

## License

*TBD -- License information will be added here.*
