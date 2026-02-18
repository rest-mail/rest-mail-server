# rest-mail - Project Plan

## Overview

**rest-mail** is a new kind of mail server. From the outside it looks and behaves exactly like a traditional SMTP/IMAP/POP3 mail server -- any existing mail server can send to it, any IMAP client can read from it. But internally there is no Postfix, no Dovecot. Instead, a **protocol gateway** translates incoming SMTP/IMAP/POP3 commands into REST API calls against a Go backend, which stores and retrieves mail from PostgreSQL and the filesystem.

The key insight: when mail1.test sends an email to mail3.test, mail1's Postfix thinks it's talking to another SMTP server. It is -- but the "SMTP server" on the other end is actually a protocol translator that converts the SMTP conversation into a series of REST API calls, stores the message, and returns proper SMTP responses.

### RESTMAIL Protocol Upgrade

When two rest-mail servers talk to each other, they don't need to speak SMTP at all. The gateway advertises a custom EHLO capability:

```
S: 250-mail3.test
S: 250-PIPELINING
S: 250-SIZE 10240000
S: 250-STARTTLS
S: 250-RESTMAIL https://mail3.test/restmail
S: 250 8BITMIME
```

When a RESTMAIL-aware server sees the `RESTMAIL` capability in the 250 response, it:

1. **Drops the SMTP TCP connection** -- the SMTP handshake was only needed for discovery
2. **Opens an HTTPS connection** to the endpoint advertised in the capability (`https://mail3.test/restmail`)
3. **Delivers the message via RESTMAIL API calls** instead of continuing the SMTP conversation

This is a **connection upgrade** mechanism, similar in spirit to STARTTLS or HTTP's `Upgrade` header, but instead of upgrading the encryption layer it upgrades the entire protocol from SMTP to RESTMAIL.

```
Scenario: mail3.test (RESTMAIL) sends to mail4.test (also RESTMAIL)

  mail3 gateway                          mail4 gateway
      │                                      │
      │──EHLO mail3.test────────────────────►│
      │◄─250-mail4.test─────────────────────│
      │  250-PIPELINING                      │
      │  250-RESTMAIL https://mail4.test/restmail
      │  250 8BITMIME                        │
      │                                      │
      │  (!) Sees RESTMAIL capability        │
      │  Drops TCP socket                    │
      │                                      │
      │══HTTPS═══════════════════════════════►│
      │──POST /restmail/messages─────────────►│
      │◄─201 Created────────────────────────│
      │                                      │
      Done. No SMTP DATA phase needed.
```

If the remote server does **not** advertise the RESTMAIL capability, the gateway falls back to standard SMTP delivery -- full backwards compatibility is preserved.

**When both servers are RESTMAIL, the upgrade is mandatory.** If the outbound relay detects the `RESTMAIL` capability in the remote server's EHLO response, it **must** upgrade to HTTPS/REST delivery. There is no configuration option to force SMTP between two RESTMAIL servers. This keeps the protocol path simple and predictable: RESTMAIL siblings always use the native REST path, traditional servers always get SMTP.

```
Scenario: mail3.test (RESTMAIL) sends to mail1.test (traditional Postfix)

  mail3 gateway                          mail1 postfix
      │                                      │
      │──EHLO mail3.test────────────────────►│
      │◄─250-mail1.test─────────────────────│
      │  250-PIPELINING                      │
      │  250 8BITMIME                        │
      │                                      │
      │  No RESTMAIL capability.             │
      │  Continue with standard SMTP.        │
      │                                      │
      │──MAIL FROM:<alice@mail3.test>────────►│
      │──RCPT TO:<bob@mail1.test>────────────►│
      │──DATA ... ───────────────────────────►│
      │  (normal SMTP delivery)              │
```

### Design Philosophy: Indistinguishable from Traditional

The whole aim is to **pretend you're a traditional mail server while simultaneously not being one**. To the outside world, rest-mail must be indistinguishable from a Postfix/Dovecot setup. If other servers detect that we're not a traditional mail server, we risk:

- **Blacklisting** -- spam filters and blacklist services may flag non-standard implementations
- **Deliverability drops** -- receiving servers may score us lower if our SMTP behaviour is non-standard
- **Incompatibility** -- edge cases in SMTP/IMAP that real servers handle but we don't
- **Rejection** -- some servers actively reject connections from non-compliant implementations

The `RESTMAIL` EHLO capability is safe because traditional servers simply **ignore unknown extensions** -- it's part of the SMTP spec. Only RESTMAIL-aware servers will recognise it. Everyone else sees a normal-looking mail server with an extra capability they don't understand, which is completely standard behaviour.

Between RESTMAIL siblings, the servers can recognise each other and upgrade to native REST, keeping everyone else in the dark.

### Why Two Traditional Servers

This is why we set up mail1.test and mail2.test as a **dual traditional server pair**:

1. **mail1 ↔ mail2** should always work -- they are real Postfix/Dovecot. If this breaks, our test infrastructure is broken, not our software.
2. **mail1 → mail3** tests whether a traditional server can deliver to rest-mail without noticing anything unusual.
3. **mail3 → mail1** tests whether rest-mail can send to a traditional server without triggering any red flags.
4. Having **two** traditional servers means we can test cross-domain delivery as a baseline, and we have two independent "real world" servers to test against, reducing the chance of a quirk in one server masking an issue.

If mail3 can interop flawlessly with mail1 and mail2 -- which are proven working traditional servers -- then it can interop with the real world.

### What We're Building

To develop and prove this, we create a fully dockerised environment with three mail domains:

- **mail1.test** and **mail2.test** -- traditional Postfix/Dovecot mail servers. These are the **reference implementation** and **benchmark**. They represent the real world: existing mail infrastructure that rest-mail must interoperate with. mail1→mail2 delivery should work identically to any normal mail system.
- **mail3.test** -- the **rest-mail server**. This is the actual software we are creating. It has no Postfix or Dovecot. Instead it runs a **protocol gateway** (Go) that speaks SMTP/IMAP/POP3 on the wire but translates everything into REST API calls internally.

The project also includes:
- A **shared PostgreSQL database** used by Postfix/Dovecot on mail1/mail2 (for email addresses, aliases, domain config -- no flat files) and by the rest-mail backend for mail3
- A **Go REST API backend** -- the single backend that both the protocol gateway and the webmail frontend talk to
- A **React/Zustand/Tailwind webmail frontend** -- a modern, slick web UI for reading and composing mail
- A **Go TUI admin tool** (Bubble Tea) for direct administration of domains, users, passwords, inboxes, and composing mail across all three mail servers

### Test Scenarios

The three-server setup lets us verify all interop paths:

| From → To            | What it proves                                                         |
|-----------------------|------------------------------------------------------------------------|
| mail1 → mail2         | Baseline: traditional mail works (reference implementation)            |
| mail1 → mail3         | Inbound: real SMTP server can deliver to rest-mail without noticing    |
| mail3 → mail1         | Outbound: rest-mail sends to a real server without triggering flags    |
| mail3 → mail2         | Outbound: same, to a different traditional server                      |
| mail3 → mail3         | Upgrade: RESTMAIL siblings discover each other, upgrade to REST        |
| IMAP client → mail3   | Read: standard IMAP client can read mail from rest-mail normally       |
| webmail → API         | Native: React UI → REST API → Postgres (no gateway, no protocol xlat) |
| TUI → API             | Native: Bubble Tea → REST API → Postgres (no gateway)                  |

### Fully Dockerised

**Every component runs in Docker.** Nothing is installed on the host. The entire system is orchestrated via `docker-compose.yml` and can be brought up with a single `docker compose up`. This includes:

| Container(s)         | Count | Ports                        | Role                                                               |
|----------------------|-------|------------------------------|-------------------------------------------------------------------|
| `dnsmasq`            | 1     | 53                           | DNS resolver for `.test` domains, MX, and PTR records              |
| `postgres`           | 1     | 5432                         | Shared database for all services (persistent volume)               |
| `postfix-mail1`      | 1     | 25, 465, 587                 | SMTP server for mail1.test (traditional, TLS)                      |
| `dovecot-mail1`      | 1     | 143, 993, 110, 995           | IMAP/POP3 server for mail1.test (traditional, TLS, Maildir volume) |
| `postfix-mail2`      | 1     | 25, 465, 587                 | SMTP server for mail2.test (traditional, TLS)                      |
| `dovecot-mail2`      | 1     | 143, 993, 110, 995           | IMAP/POP3 server for mail2.test (traditional, TLS, Maildir volume) |
| `gateway`            | 1     | 25, 465, 587, 143, 993, 110, 995 | Protocol gateway for mail3.test -- SNI TLS, SMTP AUTH, queue   |
| `api` (Go backend)   | 1     | 8080 (HTTPS)                 | REST API backend -- shared by gateway, webmail, and TUI            |
| `webmail` (React)    | 1     | 443                          | Frontend served via nginx (HTTPS)                                  |
| **Total**            | **9** |                              |                                                                    |
| `rspamd` (optional)  | 0-1   | 11333                        | Spam scoring, DKIM, DMARC (profile: filter)                       |
| `clamav` (optional)  | 0-1   | 3310                         | Virus scanning (profile: filter)                                   |
| `fail2ban` (optional)| 0-1   | —                            | IP banning via iptables (profile: filter)                          |

The **Go TUI admin tool** runs on the host (not in a container) and connects to the API and database over the Docker network.

All containers share a single Docker network with a fixed subnet. Static IPs are assigned to mail-related containers so dnsmasq can provide stable A/MX records. Inter-service communication (SMTP relay, LMTP delivery, DB queries, API calls) all happens over this internal network.

### Persistent Storage (Docker Volumes)

Data must survive container restarts. Named Docker volumes are used for all persistent state:

| Volume                    | Mounted In             | Purpose                                              |
|---------------------------|------------------------|------------------------------------------------------|
| `postgres-data`           | `postgres`             | PostgreSQL data directory                            |
| `mail1-maildir`           | `dovecot-mail1`        | Maildir storage for mail1.test                       |
| `mail2-maildir`           | `dovecot-mail2`        | Maildir storage for mail2.test                       |
| `mail3-attachments`       | `api`                  | Attachment file storage for mail3.test               |
| `certs`                   | all mail containers    | TLS certificates and CA (generated on first startup) |

```yaml
volumes:
  postgres-data:
  mail1-maildir:
  mail2-maildir:
  mail3-attachments:
  certs:
```

Dovecot on mail1/mail2 uses **Maildir** format (`/var/mail/vhosts/domain/user/`). The Go API does **not** read Maildir directly -- all access to mail1/mail2 messages goes through Dovecot's IMAP interface or through the shared Postgres database for metadata. Attachments for mail3.test are stored on the `mail3-attachments` volume by the `extract_attachments` pipeline filter.

### Docker Healthchecks

Every container has a `HEALTHCHECK` directive so Docker Compose can report container health, enforce startup ordering (`depends_on: condition: service_healthy`), and restart unhealthy containers.

| Container        | Healthcheck                                                     | Interval | Timeout |
|------------------|-----------------------------------------------------------------|----------|---------|
| `postgres`       | `pg_isready -U restmail`                                        | 10s      | 5s      |
| `dnsmasq`        | `nslookup mail1.test 127.0.0.1`                                | 10s      | 5s      |
| `postfix-mail1`  | `postfix status`                                                | 15s      | 5s      |
| `postfix-mail2`  | `postfix status`                                                | 15s      | 5s      |
| `dovecot-mail1`  | `doveadm service status imap`                                   | 15s      | 5s      |
| `dovecot-mail2`  | `doveadm service status imap`                                   | 15s      | 5s      |
| `api`            | `curl -sf http://localhost:8080/api/health`                     | 10s      | 5s      |
| `gateway`        | `curl -sf http://localhost:8081/healthz` (internal health port) | 10s      | 5s      |
| `webmail`        | `curl -sf http://localhost:80/` (nginx) or Vite dev server      | 10s      | 5s      |

**Startup ordering via `depends_on`:**

```yaml
services:
  postgres:
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U restmail"]
      interval: 10s
      timeout: 5s
      retries: 5

  api:
    depends_on:
      postgres:
        condition: service_healthy
      dnsmasq:
        condition: service_healthy

  gateway:
    depends_on:
      api:
        condition: service_healthy

  postfix-mail1:
    depends_on:
      postgres:
        condition: service_healthy
      dnsmasq:
        condition: service_healthy
```

This ensures: Postgres is ready before the API starts, the API is ready before the gateway starts, and DNS is ready before any mail container starts.

### Full System Architecture

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                           Docker Network (.test)                             │
│                                                                              │
│  ┌──────────┐  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐        │
│  │ dnsmasq  │  │ mail1.test   │  │ mail2.test   │  │   postgres   │        │
│  │          │  │ postfix      │  │ postfix      │  │              │        │
│  │ DNS for  │  │ dovecot      │  │ dovecot      │  │  shared db   │        │
│  │ .test    │  │ (traditional)│  │ (traditional)│  │              │        │
│  └──────────┘  └──────┬───────┘  └──────┬───────┘  └───────┬──────┘        │
│                       │                  │                   │               │
│                       │  SMTP            │  SMTP             │               │
│                       │                  │                   │               │
│              ┌────────▼──────────────────▼────────┐          │               │
│              │         mail3.test                 │          │               │
│              │      Protocol Gateway (Go)         │          │               │
│              │                                    │          │               │
│              │  :25  SMTP handler ──┐             │          │               │
│              │  :587 SMTP submit ───┤   REST      │          │               │
│              │  :143 IMAP handler ──┼──calls──┐   │          │               │
│              │  :110 POP3 handler ──┘         │   │          │               │
│              └────────────────────────────────┼───┘          │               │
│                                               │              │               │
│                                    ┌──────────▼──────────┐   │               │
│                                    │   Go REST API       │   │               │
│                                    │   (shared backend)  ├───┘               │
│                                    └──────┬──────────────┘   SQL             │
│                                           │                                  │
│                                    ┌──────▼──────────────┐                   │
│                                    │   React Frontend    │                   │
│                                    │   Zustand + TW      │                   │
│                                    └─────────────────────┘                   │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘
        ▲
        │ REST API calls over Docker network
        │
┌───────┴──────────┐
│  Go TUI (host)   │
│  Bubble Tea      │
│  admin tool      │
└──────────────────┘
```

**Data flow example: mail1.test sends to user@mail3.test**

```
mail1 postfix                    mail3 gateway                Go REST API         Postgres
    │                                │                            │                  │
    │──EHLO mail1.test──────────────►│                            │                  │
    │◄─250 mail3.test───────────────│                            │                  │
    │──MAIL FROM:<bob@mail1.test>──►│                            │                  │
    │◄─250 OK──────────────────────│                            │                  │
    │──RCPT TO:<alice@mail3.test>──►│──GET /api/mailboxes?addr──►│──SELECT──────────►│
    │                                │◄─200 {exists: true}───────│◄─row─────────────│
    │◄─250 OK──────────────────────│                            │                  │
    │──DATA────────────────────────►│                            │                  │
    │──(message body)──────────────►│──POST /api/messages────────►│──INSERT──────────►│
    │                                │◄─201 Created──────────────│◄─ok──────────────│
    │◄─250 Message accepted────────│                            │                  │
    │──QUIT────────────────────────►│                            │                  │
    │◄─221 Bye─────────────────────│                            │                  │
```

---

## Phase 1: Reference Implementation

Bring up an existing, known-working dockerised Postfix + Dovecot mail server setup to serve as a reference. This gives us a working baseline to compare against as we build the custom solution.

### TODO
- [x] Research and select a suitable existing docker-based mail server image/compose setup (e.g. docker-mailserver, mailu, or similar)
- [x] Create a `reference/` directory with the chosen setup's docker-compose and config
- [x] Bring up the reference stack and verify it can send/receive mail locally
- [x] Document the key configuration files, ports, and architecture decisions observed

---

## Phase 2: Custom Postfix + Dovecot Container Build

Build our own dockerised Postfix and Dovecot setup from scratch, informed by the reference implementation.

### Architecture

Each mail domain runs as a **pair of containers**:
- **Postfix container** - handles SMTP (sending/receiving mail)
- **Dovecot container** - handles IMAP/POP3 (mailbox access)

For two domains (`mail1.test`, `mail2.test`) the topology is:

```
┌─────────────────────┐       ┌─────────────────────┐
│   mail1.test        │       │   mail2.test        │
│                     │       │                     │
│  ┌───────────────┐  │       │  ┌───────────────┐  │
│  │  Postfix      │  │ SMTP  │  │  Postfix      │  │
│  │  (SMTP)       │◄─┼──────►┼──│  (SMTP)       │  │
│  └───────┬───────┘  │       │  └───────┬───────┘  │
│          │ LMTP     │       │          │ LMTP     │
│  ┌───────▼───────┐  │       │  ┌───────▼───────┐  │
│  │  Dovecot      │  │       │  │  Dovecot      │  │
│  │  (IMAP/POP3)  │  │       │  │  (IMAP/POP3)  │  │
│  └───────────────┘  │       │  └───────────────┘  │
└─────────────────────┘       └─────────────────────┘
```

### Process Management: supervisord + rsyslogd

Each mail container uses **supervisord** as the process manager. This is the standard approach for running multiple daemons in a single Docker container (Postfix itself spawns multiple processes, and we also need rsyslogd for structured logging).

```
Postfix container:
  supervisord
    ├── rsyslogd         → captures Postfix mail logs
    ├── postfix (master) → SMTP daemon
    └── (cron if needed) → log rotation

Dovecot container:
  supervisord
    ├── rsyslogd         → captures Dovecot auth/delivery logs
    └── dovecot          → IMAP/POP3/LMTP daemon
```

rsyslogd captures mail.log, auth.log, and other syslog facilities that Postfix and Dovecot write to. Logs are written to stdout (for `docker logs`) and optionally to a log volume for persistence. This matches production server setups where rsyslogd is the standard logging infrastructure.

### Configuration Management

All configuration is templated and driven by environment variables:

| Variable              | Example              | Used By        | Purpose                         |
|-----------------------|----------------------|----------------|----------------------------------|
| `MAIL_DOMAIN`         | mail1.test           | Postfix, Dovecot| Primary mail domain             |
| `MAIL_HOSTNAME`       | mail1.test           | Postfix         | SMTP banner hostname            |
| `DB_HOST`             | postgres             | Postfix, Dovecot| PostgreSQL connection           |
| `DB_NAME`             | restmail             | Postfix, Dovecot| Database name                   |
| `DB_USER`             | restmail             | Postfix, Dovecot| Database user                   |
| `DB_PASS`             | (secret)             | Postfix, Dovecot| Database password               |
| `TLS_CERT_PATH`       | /certs/mail1.test.crt| Postfix, Dovecot| TLS certificate path            |
| `TLS_KEY_PATH`        | /certs/mail1.test.key| Postfix, Dovecot| TLS private key path            |

Entrypoint scripts read these variables, render the config templates (main.cf, master.cf, dovecot.conf, pgsql lookup configs), and start supervisord.

### TODO
- [x] Create Dockerfile for Postfix container (based on Debian/Alpine, install postfix + rsyslogd + supervisord)
- [x] Create Dockerfile for Dovecot container (install dovecot + rsyslogd + supervisord)
- [x] Write supervisord configs for both containers
- [x] Write rsyslogd config (capture mail.log, forward to stdout)
- [x] Write Postfix configuration templates (main.cf, master.cf)
- [x] Write Dovecot configuration templates
- [x] Create entrypoint scripts that render config from environment variables
- [ ] Test a single domain pair can send and receive mail internally

---

## Phase 3: DNS Resolution with dnsmasq

Docker's internal networking makes cross-container DNS resolution non-trivial. A dedicated dnsmasq container provides authoritative DNS for the `.test` domains and MX records so that Postfix instances can discover each other.

### Architecture

```
┌──────────────────┐
│    dnsmasq       │
│                  │
│  mail1.test ──► IP_A  (MX → postfix-mail1)
│  mail2.test ──► IP_B  (MX → postfix-mail2)
└──────────────────┘
        ▲
        │ DNS queries
        │
   All mail containers use
   dnsmasq as their resolver
```

### Required DNS Records

For each mail domain, the following DNS records are needed:

| Record Type | Name                           | Value                        | Purpose                         |
|-------------|--------------------------------|------------------------------|---------------------------------|
| A           | mail1.test                     | 172.20.0.x                   | Server hostname                 |
| MX          | mail1.test                     | 10 mail1.test                | Mail routing                    |
| PTR         | x.0.20.172.in-addr.arpa        | mail1.test                   | Reverse DNS (deliverability)    |
| TXT         | mail1.test                     | v=spf1 ip4:172.20.0.x -all  | SPF policy                      |
| TXT         | default._domainkey.mail1.test  | v=DKIM1; k=rsa; p=...       | DKIM public key                 |
| TXT         | _dmarc.mail1.test              | v=DMARC1; p=reject; ...     | DMARC policy                    |

### Pluggable DNS Provider System

In development, dnsmasq manages all DNS records statically. In production, DNS records need to be managed through real DNS providers (Cloudflare, Route53, etc.).

When a domain is added via the RESTMAIL API, the system needs to create/update DNS records (MX, SPF, DKIM, DMARC, PTR). Rather than hard-coding a single DNS provider, we use a **pluggable DNS provider adapter** system:

```
API: POST /api/admin/domains { name: "example.com" }
  │
  ▼
Domain Service
  │
  ├──► DNS Provider Adapter (interface)
  │       │
  │       ├── dnsmasq (dev)        - writes to dnsmasq config file, restarts container
  │       ├── externaldns (k8s)    - creates DNSEndpoint CRDs, externaldns picks them up
  │       ├── cloudflare           - calls Cloudflare API
  │       ├── route53              - calls AWS Route53 API
  │       ├── manual               - outputs required records for manual creation
  │       └── (custom)             - user implements the adapter interface
  │
  └──► Certificate Provisioning (Phase 6)
```

The adapter interface:

```go
type DNSProvider interface {
    // Create/update records for a domain
    EnsureRecords(ctx context.Context, domain string, records []DNSRecord) error
    // Remove all records for a domain
    RemoveRecords(ctx context.Context, domain string) error
    // Verify records are propagated
    VerifyRecords(ctx context.Context, domain string) ([]VerifyResult, error)
}

type DNSRecord struct {
    Type    string // A, MX, TXT, PTR, CNAME
    Name    string // e.g. "mail1.test", "_dmarc.mail1.test"
    Value   string
    TTL     int
    Priority int  // for MX records
}
```

When a domain is created, the system:
1. Generates the required DNS records (MX, SPF TXT, DKIM TXT, DMARC TXT)
2. Calls the configured DNS provider adapter to create them
3. Optionally verifies propagation
4. Reports the status via the API

The **manual** adapter simply returns the list of records that need to be created, so users with unsupported DNS providers know exactly what to set up. The API endpoint `GET /api/admin/domains/:id/dns` returns the required records regardless of which adapter is active.

### Key Considerations
- All mail containers must be configured to use the dnsmasq container as their DNS resolver (via docker `--dns` or compose `dns:` directive)
- Static IP addresses should be assigned to containers within the Docker network to keep DNS mappings stable
- MX records are essential so Postfix knows where to deliver mail for each domain
- A records must map domain names to the correct container IPs
- PTR records are needed for reverse DNS -- essential for deliverability
- SPF, DKIM, and DMARC TXT records are created automatically when a domain is added
- The DNS provider adapter is configured via environment variable (e.g. `DNS_PROVIDER=dnsmasq`)

### TODO
- [x] Create Dockerfile / config for dnsmasq container
- [x] Define a Docker network with a fixed subnet for static IP assignment
- [x] Configure dnsmasq with A, MX, PTR, and TXT records for mail1.test, mail2.test, mail3.test
- [x] Add SPF, DKIM, and DMARC TXT records to dnsmasq config
- [x] Set all mail containers to use dnsmasq as their DNS server
- [x] Write the master docker-compose.yml orchestrating all 5 containers (dnsmasq + 2x postfix + 2x dovecot)
- [ ] Verify cross-domain mail delivery: send from user@mail1.test to user@mail2.test and confirm receipt
- [x] Design the `DNSProvider` adapter interface (`internal/dns/provider.go`)
- [x] Implement dnsmasq adapter (dev: write config, reload)
- [x] Implement manual adapter (returns required records as JSON)
- [x] Implement externaldns adapter (k8s: create DNSEndpoint CRDs)
- [x] Add `GET /api/admin/domains/:id/dns` endpoint (show required records)
- [x] Add DNS record verification to domain health checks
- [x] Document how to implement a custom DNS provider adapter

---

## Phase 4: PostgreSQL Shared Database

A single PostgreSQL instance serves as the shared data store for the entire system. Postfix and Dovecot authenticate users and look up mailbox/domain info via SQL queries. The Go webmail backend reads and writes to the same database.

### Schema Design

The database has two logical layers: the **mail layer** (used by Postfix/Dovecot) and the **webmail layer** (used by the Go backend and frontend).

#### Mail Layer (Postfix + Dovecot)

```sql
CREATE TABLE domains (
    id              SERIAL PRIMARY KEY,
    name            VARCHAR(255) NOT NULL UNIQUE,    -- e.g. "mail1.test", "mail3.test"
    server_type     VARCHAR(20) NOT NULL DEFAULT 'traditional',  -- 'traditional' or 'restmail'
    active          BOOLEAN DEFAULT true,
    default_quota_bytes BIGINT DEFAULT 1073741824,   -- 1GB default for new mailboxes
    created_at      TIMESTAMP DEFAULT NOW(),
    updated_at      TIMESTAMP DEFAULT NOW()
);

CREATE TABLE mailboxes (
    id              SERIAL PRIMARY KEY,
    domain_id       INTEGER NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    local_part      VARCHAR(64) NOT NULL,            -- e.g. "alice" (before the @)
    address         VARCHAR(255) NOT NULL UNIQUE,    -- e.g. "alice@mail1.test" (full address, for lookups)
    password        VARCHAR(255) NOT NULL,           -- {BLF-CRYPT} bcrypt hash (Dovecot-compatible)
    display_name    VARCHAR(255),                    -- e.g. "Alice Smith"
    quota_bytes     BIGINT DEFAULT 1073741824,       -- 1GB default
    quota_used_bytes BIGINT DEFAULT 0,               -- incremental tracking
    active          BOOLEAN DEFAULT true,
    last_login_at   TIMESTAMP,
    created_at      TIMESTAMP DEFAULT NOW(),
    updated_at      TIMESTAMP DEFAULT NOW(),
    UNIQUE(domain_id, local_part)
);

CREATE INDEX idx_mailboxes_address ON mailboxes(address);
CREATE INDEX idx_mailboxes_domain ON mailboxes(domain_id);

CREATE TABLE aliases (
    id              SERIAL PRIMARY KEY,
    domain_id       INTEGER NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    source_address  VARCHAR(255) NOT NULL,           -- e.g. "info@mail1.test"
    destination_address VARCHAR(255) NOT NULL,       -- e.g. "alice@mail1.test"
    active          BOOLEAN DEFAULT true,
    created_at      TIMESTAMP DEFAULT NOW(),
    UNIQUE(source_address, destination_address)
);

CREATE INDEX idx_aliases_source ON aliases(source_address);
```

#### Webmail Layer (Go backend + React frontend)

```sql
CREATE TABLE webmail_accounts (
    id                  SERIAL PRIMARY KEY,
    primary_mailbox_id  INTEGER NOT NULL UNIQUE REFERENCES mailboxes(id) ON DELETE CASCADE,
    created_at          TIMESTAMP DEFAULT NOW(),
    updated_at          TIMESTAMP DEFAULT NOW()
);

CREATE TABLE linked_accounts (
    id                  SERIAL PRIMARY KEY,
    webmail_account_id  INTEGER NOT NULL REFERENCES webmail_accounts(id) ON DELETE CASCADE,
    mailbox_id          INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    display_name        VARCHAR(255),                -- override display name for this link
    created_at          TIMESTAMP DEFAULT NOW(),
    UNIQUE(webmail_account_id, mailbox_id)
);

CREATE TABLE messages (
    id              SERIAL PRIMARY KEY,
    mailbox_id      INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    folder          VARCHAR(255) NOT NULL DEFAULT 'INBOX',
    message_id      VARCHAR(995),                    -- RFC 5322 Message-ID header value
    in_reply_to     VARCHAR(995),                    -- In-Reply-To header (for threading)
    "references"    TEXT,                            -- References header (space-separated Message-IDs)
    thread_id       VARCHAR(995),                    -- computed thread root Message-ID
    sender          VARCHAR(255) NOT NULL,           -- From address
    sender_name     VARCHAR(255),                    -- From display name
    recipients_to   JSONB NOT NULL DEFAULT '[]',     -- [{name, address}] array
    recipients_cc   JSONB NOT NULL DEFAULT '[]',     -- [{name, address}] array
    subject         TEXT,
    body_text       TEXT,                            -- plain text body
    body_html       TEXT,                            -- HTML body
    headers         JSONB,                           -- all headers as JSON (for inspection/search)
    raw_message     TEXT,                            -- original RFC 2822 source (optional, for re-parsing)
    size_bytes      INTEGER NOT NULL DEFAULT 0,      -- total size for quota (subject + body + attachments)
    has_attachments BOOLEAN DEFAULT false,
    is_read         BOOLEAN DEFAULT false,
    is_flagged      BOOLEAN DEFAULT false,
    is_starred      BOOLEAN DEFAULT false,
    is_draft        BOOLEAN DEFAULT false,
    is_deleted      BOOLEAN DEFAULT false,           -- soft delete (trash)
    received_at     TIMESTAMP DEFAULT NOW(),         -- when we received it
    date_header     TIMESTAMP,                       -- Date header from the message itself
    created_at      TIMESTAMP DEFAULT NOW(),
    updated_at      TIMESTAMP DEFAULT NOW()
);

CREATE INDEX idx_messages_mailbox_folder ON messages(mailbox_id, folder, received_at DESC);
CREATE INDEX idx_messages_message_id ON messages(message_id);
CREATE INDEX idx_messages_thread_id ON messages(thread_id);
CREATE INDEX idx_messages_mailbox_unread ON messages(mailbox_id, folder) WHERE is_read = false;
CREATE INDEX idx_messages_search ON messages USING gin(to_tsvector('english', coalesce(subject, '') || ' ' || coalesce(body_text, '')));
```

#### Account Model

A **webmail account** is created from an existing mailbox. The login credentials (email address + password) **match** the mailbox credentials -- you log into the webmail with the same email and password you'd use for IMAP. When you create a webmail account, the system:

1. Takes an existing mailbox (e.g. `jane@mail1.test`)
2. Creates a `webmail_accounts` row pointing to that mailbox as the **primary account**
3. The user logs in with `jane@mail1.test` + their mailbox password

Additional mailboxes can then be **linked** to this webmail account as secondary accounts. The user configures these after login, providing the credentials for each additional mailbox they want access to.

```
mailboxes (already exist)
  ┌──────────────────────────────┐
  │ id: 5                        │
  │ address: jane@mail1.test     │
  │ password: (hashed)           │
  ├──────────────────────────────┤
  │ id: 12                       │
  │ address: marketing@mail1.test│
  │ password: (hashed)           │
  └──────────────────────────────┘

webmail_accounts
  ┌──────────────────────────────┐
  │ id: 1                        │
  │ primary_mailbox_id: 5        │  ──► jane@mail1.test (login with this)
  └──────────┬───────────────────┘
             │
linked_accounts (secondary accounts)
  ┌──────────▼───────────────────┐
  │ webmail_account_id: 1        │
  │ mailbox_id: 12               │  ──► marketing@mail1.test (secondary)
  └──────────────────────────────┘
```

This means:
- Jane logs into the webmail with `jane@mail1.test` + her password
- Her primary inbox (jane@mail1.test) loads by default
- She has also linked marketing@mail1.test as a secondary account
- She can switch to the marketing inbox without logging out
- When composing, she can choose which address to send from
- The webmail account **does not have its own separate credentials** -- it uses the primary mailbox's credentials

### Quota Management

Every mailbox has a storage quota. Quota usage is tracked incrementally — when a message is delivered or deleted, the used bytes are adjusted. This avoids expensive full-table scans to calculate usage.

**What counts toward quota:** Only data the user actually uses:
- Subject line (bytes)
- Message body — both text and HTML parts (bytes)
- Attachments (original file size, not base64-inflated)

**What does NOT count:** Headers, envelope metadata, pipeline logs, internal tracking fields.

```sql
-- Add to mailboxes table:
ALTER TABLE mailboxes ADD COLUMN quota_bytes BIGINT DEFAULT 1073741824;  -- 1GB default
ALTER TABLE mailboxes ADD COLUMN quota_used_bytes BIGINT DEFAULT 0;

-- Quota tracking table for precise accounting
CREATE TABLE quota_usage (
    mailbox_id    INTEGER PRIMARY KEY REFERENCES mailboxes(id),
    subject_bytes BIGINT DEFAULT 0,
    body_bytes    BIGINT DEFAULT 0,
    attachment_bytes BIGINT DEFAULT 0,
    total_bytes   BIGINT GENERATED ALWAYS AS (subject_bytes + body_bytes + attachment_bytes) STORED,
    message_count INTEGER DEFAULT 0,
    updated_at    TIMESTAMP DEFAULT NOW()
);
```

**Quota enforcement points:**

1. **`recipient_check` pipeline filter (inbound):** Before accepting a message, check if the recipient's mailbox has space. If `quota_used_bytes + incoming_message_size > quota_bytes`, reject with `452 4.2.2 Mailbox full` (temporary failure so the sender retries later, giving the recipient time to clean up).

2. **Message delivery (API):** When a message is stored, atomically increment `quota_used_bytes` by the subject + body + attachment sizes.

3. **Message deletion (API):** When a message is deleted, atomically decrement `quota_used_bytes` by the same amounts.

4. **IMAP gateway:** The IMAP `STATUS` and `GETQUOTA` commands (RFC 2087) return quota info. When quota is exceeded, the gateway responds with `OVERQUOTA` to IMAP `APPEND` commands.

5. **Admin API:** Quota can be configured per-mailbox via `PATCH /api/admin/mailboxes/:id { quota_bytes: ... }`. A domain-wide default quota is configurable via `PATCH /api/admin/domains/:id { default_quota_bytes: ... }`.

```
Quota check in the inbound pipeline:

  [recipient_check] ──► Does recipient exist? YES
                    ──► Is mailbox over quota?
                          │
                          ├── NO  → continue (accept message)
                          └── YES → reject 452 "Mailbox full"
```

**Quota usage API:**

```
GET /api/accounts/:id/quota           - Get quota usage for logged-in user
  Response: { quota_bytes, used_bytes, subject_bytes, body_bytes, attachment_bytes, percentage }

GET /api/admin/mailboxes/:id/quota    - Admin: get quota details for any mailbox
PATCH /api/admin/mailboxes/:id        - Admin: set quota_bytes
PATCH /api/admin/domains/:id          - Admin: set default_quota_bytes for new mailboxes
```

The webmail UI shows a quota bar (e.g. "Using 340 MB of 1 GB") in the account sidebar.

### Connection Pooling

Multiple services connect to the same PostgreSQL instance (Postfix, Dovecot, Go API, gateway queue workers). Each opens its own connections. Under load, this can exhaust PostgreSQL's `max_connections` (default: 100).

**Go services:** Use `database/sql`'s built-in connection pool. Configure via environment variables:

| Variable              | Default | Purpose                                     |
|-----------------------|---------|---------------------------------------------|
| `DB_MAX_OPEN_CONNS`   | 25      | Maximum open connections to Postgres         |
| `DB_MAX_IDLE_CONNS`   | 10      | Maximum idle connections in pool             |
| `DB_CONN_MAX_LIFETIME`| 5m      | Max time a connection can be reused          |

**Postfix/Dovecot:** These use short-lived connections for each lookup query. Connection overhead is minimal for small deployments. For high-volume deployments, **PgBouncer** (a lightweight connection pooler) can sit between all clients and PostgreSQL, multiplexing hundreds of short-lived connections onto a smaller pool of real database connections.

### PostgreSQL High Availability (Future)

For the initial release, a single PostgreSQL instance is sufficient — this is designed for small businesses, not hundreds of thousands of accounts.

When HA becomes necessary, the path is:
- **Patroni** — manages PostgreSQL streaming replication with automatic failover. Runs as a sidecar alongside each Postgres instance.
- **PgBouncer** — sits in front of the Patroni cluster, handles connection routing to the current primary.
- All services connect to PgBouncer (single endpoint), which routes to the active primary. On failover, PgBouncer reconnects to the new primary automatically.

```
Services ──► PgBouncer ──► Patroni ──► Postgres Primary
                                   ──► Postgres Replica (read-only, failover target)
```

This is a standard PostgreSQL HA pattern and doesn't require any application code changes — the Go services just see a single database endpoint.

### Shared Database, Different Message Storage

The key insight: **all three mail servers share the same PostgreSQL database for account data** (domains, mailboxes, aliases, credentials), but they differ in how they store message content:

- **mail1/mail2 (traditional):** Postfix accepts SMTP delivery and hands messages to Dovecot via LMTP. Dovecot stores message content in **Maildir format** on persistent Docker volumes (`/var/mail/vhosts/domain/user/`). Postfix and Dovecot read domain, mailbox, and alias configuration from the shared Postgres via SQL lookups — this is a standard database-driven virtual mail setup. The API does not read Maildir directly; it reads account/domain metadata from Postgres and can probe message content via IMAP.

- **mail3 (rest-mail):** The gateway accepts SMTP delivery, the API stores message content directly in **PostgreSQL** (body text in the `messages` table, attachments on the filesystem). There is no Maildir. The database schema for accounts, domains, and aliases is identical (or near-identical) to what mail1/mail2 use — the same tables serve both traditional and rest-mail servers.

This shared schema means: create a domain or mailbox via the API, and both Postfix/Dovecot (mail1/mail2) and the rest-mail gateway (mail3) see it immediately via the same Postgres queries.

### Key Considerations
- Postfix uses `pgsql` lookup tables (virtual_mailbox_domains, virtual_mailbox_maps, virtual_alias_maps) pointing at the shared Postgres
- Dovecot uses its `pgsql` auth driver to authenticate users and locate mail storage against the same database
- Dovecot stores messages in Maildir on Docker volumes; the API stores messages in Postgres — both share the same account tables
- The Go backend connects directly to Postgres for user management, mailbox listing, and message retrieval
- Webmail authentication verifies credentials against the **primary mailbox** in the `mailboxes` table -- there is no separate webmail password
- Creating a webmail account is essentially "enabling webmail access" for an existing mailbox
- **Database ORM:** GORM (`gorm.io/gorm`) is the database layer. GORM provides struct-to-table mapping, query building, and **AutoMigrate** for schema migrations. Models are defined as Go structs (e.g. `type Domain struct { ... }`) and GORM handles `CREATE TABLE`, `ALTER TABLE`, and index creation. For complex migrations (data transforms, multi-step changes), GORM's migration interface is extended with versioned migration files
- Quota tracking is incremental (add/subtract on delivery/deletion) — never recalculated from scratch during normal operation. A periodic reconciliation task can be run to fix any drift.
- Go services use `database/sql` connection pooling with configurable limits. PgBouncer available for high-volume deployments.

### TODO
- [x] Add a PostgreSQL container to docker-compose.yml
- [x] Design and finalise the shared database schema (domains, mailboxes, aliases, webmail_accounts, linked_accounts, messages)
- [x] Define GORM model structs for all tables (domains, mailboxes, aliases, webmail_accounts, linked_accounts, messages, quota_usage)
- [x] Implement GORM AutoMigrate for initial schema creation
- [x] Configure Postfix to use pgsql lookup tables for virtual domains/mailboxes/aliases
- [x] Configure Dovecot to authenticate and locate mailboxes via pgsql
- [x] Write seed data script: test domains (mail1.test, mail2.test), test mailboxes, webmail accounts with primary + secondary linked accounts
- [x] Verify Postfix + Dovecot still deliver mail correctly with the database backend
- [x] Create `quota_usage` table and add `quota_bytes` / `quota_used_bytes` columns to `mailboxes`
- [x] Implement incremental quota tracking (adjust on message delivery and deletion)
- [x] Add quota check to `recipient_check` pipeline filter (reject 452 when over quota)
- [x] Add quota API endpoints (user: GET usage, admin: GET/SET per-mailbox and domain default)
- [x] Implement IMAP `GETQUOTA` / `GETQUOTAROOT` commands in gateway (RFC 2087)
- [x] Add quota bar to webmail UI (account sidebar)
- [x] Add periodic quota reconciliation task (fix any drift from crashes/bugs)

---

## Phase 5: Go Backend (RESTMAIL Protocol API)

The RESTMAIL Protocol API **is** the mail server. Everything a traditional mail server does -- authenticating users, accepting inbound mail, delivering outbound mail, storing messages, managing mailboxes -- is expressed as REST endpoints. There is no separate mail daemon; the API _is_ the daemon.

The webmail frontend consumes the same operations that any mail client would use (login, list folders, read messages, send mail), just natively over REST instead of through IMAP/SMTP protocol translation. The protocol gateway translates SMTP/IMAP/POP3 commands into the same API calls. The TUI admin tool uses the same API plus admin-only endpoints.

### API Layers

```
┌─────────────────────────────────────────────────────────────────┐
│                    RESTMAIL Protocol API                         │
│                                                                  │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │  MAIL SERVER OPERATIONS                                    │  │
│  │  (what any mail server does -- the core of RESTMAIL)       │  │
│  │                                                            │  │
│  │  • Authenticate users          (≈ IMAP LOGIN / SMTP AUTH) │  │
│  │  • Accept inbound delivery     (≈ SMTP RCPT TO + DATA)    │  │
│  │  • Relay outbound delivery     (≈ SMTP client)            │  │
│  │  • List folders                (≈ IMAP LIST)              │  │
│  │  • List/read/search messages   (≈ IMAP FETCH/SEARCH)     │  │
│  │  • Compose and send            (≈ SMTP submission)        │  │
│  │  • Move/delete/flag messages   (≈ IMAP STORE/COPY)       │  │
│  └───────────────────────────────────────────────────────────┘  │
│       ▲              ▲              ▲                            │
│       │              │              │                            │
│    Webmail        Protocol       TUI admin                      │
│    (native)       Gateway        (native)                       │
│                   (translated)                                   │
│                                                                  │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │  SERVER-TO-SERVER (RESTMAIL upgrade path)                  │  │
│  │  • Receive mail from another RESTMAIL server               │  │
│  │  • Verify recipient exists                                 │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                  │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │  ADMINISTRATION (manage all domains)                       │  │
│  │  • Domains, mailboxes, aliases, webmail accounts CRUD      │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                  │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │  TESTING & DIAGNOSTICS                                     │  │
│  │  • Health probes, DB inspection, delivery verification     │  │
│  └───────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

The **mail server operations** layer is the heart of the RESTMAIL protocol. It's what the webmail and TUI consume directly. It's also what the protocol gateway translates SMTP/IMAP/POP3 into. Every operation maps 1:1 to something a traditional mail server would do:

### API Versioning

All API endpoints are versioned under `/api/v1/`. When breaking changes are needed in the future, a new version (`/api/v2/`) is introduced while the old version continues to work for a deprecation period. The server-to-server `/restmail/` endpoints are versioned separately via the `RESTMAIL` capability string (e.g. `RESTMAIL/1.0 https://...`).

Throughout this document, `/api/` is shorthand for `/api/v1/`.

### Standard Response Format

All API responses follow a consistent JSON structure. This ensures the frontend, gateway, TUI, and test suite can handle responses uniformly.

**Success responses:**

```json
// Single resource (GET /api/v1/messages/42)
{
  "data": {
    "id": 42,
    "subject": "Hello",
    "sender": "alice@mail1.test",
    ...
  }
}

// Collection (GET /api/v1/accounts/1/folders/inbox/messages)
{
  "data": [
    { "id": 42, "subject": "Hello", ... },
    { "id": 43, "subject": "Re: Hello", ... }
  ],
  "pagination": {
    "cursor": "eyJpZCI6NDN9",
    "has_more": true,
    "total": 156
  }
}

// Action with no body (DELETE /api/v1/messages/42)
HTTP 204 No Content
```

**Error responses:**

```json
// Client error (400, 401, 403, 404, 409, 422, 429)
{
  "error": {
    "code": "mailbox_not_found",
    "message": "No mailbox found with address nobody@mail3.test",
    "details": {}
  }
}

// Validation error (422)
{
  "error": {
    "code": "validation_failed",
    "message": "Request body failed validation",
    "details": {
      "fields": {
        "address": "must be a valid email address",
        "password": "must be at least 8 characters"
      }
    }
  }
}

// Server error (500)
{
  "error": {
    "code": "internal_error",
    "message": "An unexpected error occurred"
  }
}
```

**Standard error codes:**

| HTTP Status | Error Code | When |
|-------------|-----------|------|
| 400 | `bad_request` | Malformed request body or parameters |
| 401 | `unauthorized` | Missing or invalid JWT token |
| 403 | `forbidden` | Valid token but insufficient permissions |
| 404 | `not_found` / `mailbox_not_found` / `message_not_found` / `domain_not_found` | Resource doesn't exist |
| 409 | `conflict` / `already_exists` | Duplicate resource (e.g. domain already exists) |
| 422 | `validation_failed` | Request body fails validation |
| 429 | `rate_limited` | Too many requests (infrastructure-level) |
| 452 | `mailbox_full` | Quota exceeded (SMTP delivery) |
| 500 | `internal_error` | Unexpected server error |

**Pagination** uses cursor-based pagination (not offset-based) for stable results when new messages arrive. The `cursor` is an opaque base64-encoded string. The client passes it back as `?cursor=...` to get the next page.

### API Surface

```
═══════════════════════════════════════════════════════════════════════════
 MAIL SERVER OPERATIONS (core RESTMAIL protocol)
 Used by: webmail, TUI, protocol gateway
 These map 1:1 to traditional mail server operations
═══════════════════════════════════════════════════════════════════════════

Authentication                                    ≈ IMAP LOGIN / SMTP AUTH
  POST   /api/auth/login                 - Authenticate, return JWT (access + refresh tokens)
  POST   /api/auth/logout                - Invalidate session / revoke tokens
  POST   /api/auth/refresh               - Refresh access token using refresh token

Linked Accounts (multi-mailbox access)            ≈ IMAP with multiple accounts
  GET    /api/accounts                   - List linked mailboxes for logged-in user
  GET    /api/accounts/:id               - Get details of a linked mailbox
  POST   /api/accounts                   - Link additional mailbox (secondary)
  DELETE /api/accounts/:id               - Unlink a secondary mailbox
  PATCH  /api/accounts/:id               - Set as primary, update display name

Folders                                            ≈ IMAP LIST / CREATE / DELETE / RENAME
  GET    /api/accounts/:id/folders                     - List folders (inbox, sent, drafts, trash, custom)
  POST   /api/accounts/:id/folders                     - Create folder
  PATCH  /api/accounts/:id/folders/:folder             - Rename folder
  DELETE /api/accounts/:id/folders/:folder             - Delete folder (must be empty or cascade)

Messages (read)                                    ≈ IMAP SELECT + FETCH + SEARCH
  GET    /api/accounts/:id/folders/:folder/messages    - List messages (cursor-based pagination)
  GET    /api/messages/:id                             - Get full message (headers + body + attachment refs)
  GET    /api/messages/:id/raw                         - Get original RFC 2822 raw message source
  GET    /api/messages/:id/attachments                 - List attachments for a message
  GET    /api/messages/:id/attachments/:aid            - Download attachment (streamed from storage)


Messages (write)                                   ≈ SMTP submission + IMAP STORE
  POST   /api/accounts/:id/messages                    - Compose and send
  DELETE /api/messages/:id                             - Move to trash / permanent delete
  PATCH  /api/messages/:id                             - Mark read/unread, flag, star, move to folder
  POST   /api/messages/:id/move                        - Move message to a different folder
  POST   /api/messages/:id/forward                     - Forward message (preserves attachments)

Drafts                                             ≈ IMAP APPEND to Drafts
  POST   /api/accounts/:id/drafts                      - Save new draft
  PUT    /api/drafts/:id                               - Update existing draft (auto-save)
  DELETE /api/drafts/:id                               - Discard draft
  POST   /api/drafts/:id/send                          - Send a saved draft

Search                                             ≈ IMAP SEARCH
  GET    /api/accounts/:id/search?q=...                - Full-text search across messages (Postgres FTS)
  GET    /api/accounts/:id/search?q=...&folder=inbox   - Search within a specific folder
  GET    /api/accounts/:id/search?from=...&after=...   - Advanced filters (from, to, subject, date range, has:attachment)

Threads (conversation view)                        ≈ IMAP THREAD
  GET    /api/accounts/:id/folders/:folder/threads     - List messages grouped by thread (using References/In-Reply-To)
  GET    /api/threads/:id                              - Get all messages in a thread, ordered chronologically

  Threading is built from the `References` and `In-Reply-To` headers (RFC 5256).
  The API groups messages into threads automatically. The webmail can toggle
  between flat message list and threaded conversation view per user preference.

Contacts (auto-collected)                          ≈ Address book
  GET    /api/accounts/:id/contacts                    - List collected contacts (auto-populated from sent mail)
  GET    /api/accounts/:id/contacts/suggest?q=...      - Autocomplete suggestions for compose "To" field
  DELETE /api/accounts/:id/contacts/:cid               - Remove a collected contact

Inbound delivery                                   ≈ SMTP RCPT TO + DATA
  GET    /api/mailboxes?address=...                    - Verify recipient exists
  POST   /api/messages/deliver                         - Deliver message to a local mailbox

Real-time (SSE)                                    ≈ IMAP IDLE
  GET    /api/accounts/:id/events                      - SSE (Server-Sent Events) stream

  SSE was chosen over WebSocket because email notifications are unidirectional
  (server → client). SSE has built-in browser auto-reconnect, works through any
  HTTP proxy/load balancer, uses normal HTTP auth, and is trivial to implement
  in Go. The client uses REST calls for all actions -- no bidirectional channel needed.

  Event types:
    - message.new        { folder, message_id, from, subject }
    - message.updated    { message_id, changes }
    - message.deleted    { message_id, folder }
    - folder.updated     { folder, unread_count }
    - queue.delivered    { queue_id, recipient }
    - queue.bounced      { queue_id, recipient, reason }

═══════════════════════════════════════════════════════════════════════════
 SERVER-TO-SERVER (RESTMAIL upgrade path)
 Used by: protocol gateway, other RESTMAIL servers
 Authentication: NONE for inbound delivery (same as SMTP — any server
 can deliver mail to you). Identity is verified by DKIM signatures,
 SPF records, and DMARC policy — the same mechanisms that authenticate
 traditional SMTP delivery. The TLS certificate proves the server's
 hostname. No API keys or mTLS needed.
 Outbound submission (sending FROM a mailbox) always requires JWT auth
 via the standard /api/auth/login flow.
═══════════════════════════════════════════════════════════════════════════

  POST   /restmail/messages              - Receive message from another RESTMAIL server (unauthenticated — verified by DKIM/SPF/DMARC)
  GET    /restmail/mailboxes?address=... - Verify a mailbox exists (recipient validation, unauthenticated)
  GET    /restmail/capabilities          - Return server capabilities (version, supported features, unauthenticated)

═══════════════════════════════════════════════════════════════════════════
 ADMINISTRATION (manage ALL servers -- mail1, mail2, mail3)
 Used by: TUI, webmail (admin panel), test harness
═══════════════════════════════════════════════════════════════════════════

Domains
  GET    /api/admin/domains              - List all domains (across all servers)
  POST   /api/admin/domains              - Create domain (triggers DNS record creation + cert provisioning)
  GET    /api/admin/domains/:id          - Get domain details (status, record count, cert status)
  PATCH  /api/admin/domains/:id          - Update domain config (default pipeline, settings)
  DELETE /api/admin/domains/:id          - Delete domain (cascade: mailboxes, aliases, certs, pipelines)
  GET    /api/admin/domains/:id/dns      - Get required DNS records for this domain
  POST   /api/admin/domains/:id/verify   - Verify DNS records are correctly configured

Mailboxes
  GET    /api/admin/mailboxes            - List all mailboxes (filter by domain, pagination)
  POST   /api/admin/mailboxes            - Create mailbox on any domain (e.g. bob@mail1.test)
  GET    /api/admin/mailboxes/:id        - Get mailbox details (quota usage, message count, last login)
  PATCH  /api/admin/mailboxes/:id        - Update password, quota, display name, etc.
  DELETE /api/admin/mailboxes/:id        - Delete mailbox (cascade: messages, linked accounts)

Aliases
  GET    /api/admin/aliases              - List all aliases (filter by domain)
  POST   /api/admin/aliases              - Create alias (e.g. info@mail1.test → bob@mail1.test)
  GET    /api/admin/aliases/:id          - Get alias details
  PATCH  /api/admin/aliases/:id          - Update alias destination
  DELETE /api/admin/aliases/:id          - Delete alias

Webmail Accounts
  GET    /api/admin/webmail-accounts     - List webmail accounts
  POST   /api/admin/webmail-accounts     - Create webmail account (link primary mailbox)
  GET    /api/admin/webmail-accounts/:id - Get webmail account details (linked accounts, last login)
  DELETE /api/admin/webmail-accounts/:id - Delete webmail account

Certificates (see Phase 6)
  GET    /api/admin/certs                     - List all certificates
  GET    /api/admin/certs/:domain             - Get cert details (expiry, issuer)
  POST   /api/admin/certs/:domain             - Upload manual cert (PEM)
  POST   /api/admin/certs/:domain/provision   - Trigger Let's Encrypt provisioning
  POST   /api/admin/certs/:domain/renew       - Force renewal
  DELETE /api/admin/certs/:domain             - Remove cert

DKIM Keys
  GET    /api/admin/dkim                      - List all DKIM keys
  GET    /api/admin/dkim/:domain              - Get DKIM key(s) for domain
  POST   /api/admin/dkim/:domain              - Generate new DKIM key pair
  DELETE /api/admin/dkim/:domain/:selector    - Revoke a DKIM key
  GET    /api/admin/dkim/:domain/dns          - Get DNS TXT record to publish for DKIM

Queue Management (see Phase 8)
  GET    /api/admin/queue                     - List queued messages (status filter)
  GET    /api/admin/queue/:id                 - Get queue entry details
  POST   /api/admin/queue/:id/retry           - Force immediate retry
  POST   /api/admin/queue/:id/bounce          - Force bounce
  DELETE /api/admin/queue/:id                 - Remove from queue silently
  GET    /api/admin/queue/stats               - Queue statistics

Pipelines (see Phase 11)
  GET    /api/admin/pipelines                           - List all pipelines
  GET    /api/admin/pipelines/:domain/:direction        - Get pipeline
  PUT    /api/admin/pipelines/:domain/:direction        - Create/update pipeline
  DELETE /api/admin/pipelines/:domain/:direction        - Delete pipeline (revert to default)
  POST   /api/admin/pipelines/:domain/:direction/test   - Test pipeline with sample email

Filters (see Phase 11)
  GET    /api/admin/filters                             - List all custom filters
  POST   /api/admin/filters                             - Create custom filter
  GET    /api/admin/filters/:id                         - Get custom filter
  PUT    /api/admin/filters/:id                         - Update custom filter
  DELETE /api/admin/filters/:id                         - Delete custom filter
  POST   /api/admin/filters/:id/test                    - Test single filter
  GET    /api/admin/filters/builtin                     - List built-in + adapter filters with docs
  GET    /api/admin/filters/builtin/:name               - Get docs + config schema for a filter

System Logs
  GET    /api/admin/logs                                - Query structured logs (filter by level, service, time range)
  GET    /api/admin/logs/delivery                       - Delivery log (message, from, to, status, timestamps)
  GET    /api/admin/logs/pipeline/:message_id           - Pipeline execution log for a specific message

═══════════════════════════════════════════════════════════════════════════
 TESTING & DIAGNOSTICS
 Used by: test harness, TUI, CI/CD
═══════════════════════════════════════════════════════════════════════════

Health & Status
  GET    /api/health                     - API health check (DB, queue worker, cert expiry)
  GET    /api/health/servers             - Status of all mail servers (SMTP/IMAP connectivity)
  GET    /api/health/servers/:domain     - Status of a specific server (detailed probe results)

Database Inspection
  GET    /api/test/db/domains            - Raw dump of domains table
  GET    /api/test/db/mailboxes          - Raw dump of mailboxes table
  GET    /api/test/db/messages           - Raw dump of messages table (with filters)
  GET    /api/test/db/queue              - Raw dump of outbound queue

Delivery Testing
  POST   /api/test/send                  - Send a test email (specify from, to, subject, body)
  GET    /api/test/mailbox/:address      - Read all messages for a mailbox (shortcut for testing)
  POST   /api/test/verify-delivery       - Send email and poll until delivered (or timeout)

Server Probing
  POST   /api/test/smtp-probe/:domain    - Open SMTP connection, return EHLO capabilities, TLS status
  POST   /api/test/imap-probe/:domain    - Open IMAP connection, attempt login, return status
  POST   /api/test/pop3-probe/:domain    - Open POP3 connection, attempt login, return status

DNS Probing
  GET    /api/test/dns/:domain           - Query all DNS records for domain (A, MX, PTR, TXT)
  GET    /api/test/dns/:domain/spf       - Parse and verify SPF record
  GET    /api/test/dns/:domain/dkim/:selector - Verify DKIM DNS record matches published key
  GET    /api/test/dns/:domain/dmarc     - Parse and verify DMARC record

Test Environment Management
  POST   /api/test/reset                 - Reset all test data (truncate test tables, clean up)
  POST   /api/test/seed                  - Seed standard test data (domains, mailboxes, aliases)
  POST   /api/test/snapshot              - Save current DB state as a snapshot
  POST   /api/test/restore/:snapshot     - Restore DB to a saved snapshot
```

### What the Webmail Actually Uses

The webmail frontend consumes only the **mail server operations** layer -- the same operations any mail client performs, just natively over REST:

| Webmail action        | RESTMAIL API call                              | Traditional equivalent         |
|-----------------------|------------------------------------------------|--------------------------------|
| Login                 | `POST /api/auth/login`                         | IMAP `LOGIN`                   |
| Switch account        | `GET /api/accounts`                            | Disconnect + reconnect         |
| View inbox            | `GET /api/accounts/:id/folders/inbox/messages` | IMAP `SELECT INBOX`+`FETCH`    |
| Read email            | `GET /api/messages/:id`                        | IMAP `FETCH (BODY[])`          |
| Download attachment   | `GET /api/messages/:id/attachments/:aid`       | IMAP `FETCH (BODY[2])` by part |
| Send email            | `POST /api/accounts/:id/messages`              | SMTP `MAIL FROM`+`DATA`        |
| Save draft            | `POST /api/accounts/:id/drafts`                | IMAP `APPEND` to Drafts        |
| Send draft            | `POST /api/drafts/:id/send`                    | SMTP submission                |
| Delete email          | `DELETE /api/messages/:id`                     | IMAP `STORE +FLAGS \Deleted`   |
| Mark as read          | `PATCH /api/messages/:id`                      | IMAP `STORE +FLAGS \Seen`      |
| Move to folder        | `POST /api/messages/:id/move`                  | IMAP `COPY` + `STORE \Deleted` |
| Search                | `GET /api/accounts/:id/search?q=...`           | IMAP `SEARCH`                  |
| Thread view           | `GET /api/accounts/:id/folders/:f/threads`     | IMAP `THREAD`                  |
| Create folder         | `POST /api/accounts/:id/folders`               | IMAP `CREATE`                  |
| Autocomplete address  | `GET /api/accounts/:id/contacts/suggest?q=...` | (not available in IMAP)        |
| Real-time updates     | `GET /api/accounts/:id/events` (SSE)           | IMAP `IDLE`                    |

There is no protocol translation happening here -- the webmail speaks RESTMAIL natively. This is the point of the project: **REST is the native protocol, SMTP/IMAP are just compatibility layers.**

### The API as Test Data Factory

Because the RESTMAIL API manages data for **all** domains (not just mail3), it doubles as the test data factory and verification tool. You use the same admin endpoints to seed test data into Postgres, then connect to the actual mail servers (Postfix, Dovecot, or the gateway) to verify that data is readable via the traditional protocols. This means:

- **Create** test domains, mailboxes, aliases, and messages via the RESTMAIL API
- **Verify** by connecting to Postfix/Dovecot/gateway via SMTP/IMAP to read the data back
- **Inspect** the database directly via the test endpoints to check consistency

No separate test fixtures or seed scripts needed -- the API itself is the fixture system.

```
Example: E2E test that mail1.test Postfix/Dovecot works correctly

 SETUP (via RESTMAIL API -- create test data)
──────────────────────────────────────────────
1. POST /api/admin/domains        { name: "mail1.test" }
2. POST /api/admin/mailboxes      { address: "testuser@mail1.test", password: "test123" }
3. POST /api/admin/mailboxes      { address: "sender@mail2.test", password: "test456" }

 ACTION (trigger real mail delivery)
──────────────────────────────────────────────
4. POST /api/test/send  { from: "sender@mail2.test", to: "testuser@mail1.test", subject: "hello" }
   → mail2's Postfix sends a real SMTP delivery to mail1's Postfix

 VERIFY (read back via traditional protocols + API)
──────────────────────────────────────────────
5. POST /api/test/verify-delivery  { address: "testuser@mail1.test", timeout: "10s" }
   → Polls DB until the message arrives (or times out)

6. POST /api/test/imap-probe/mail1.test  { user: "testuser@mail1.test", password: "test123" }
   → Connects to mail1's Dovecot via IMAP, logs in, reads inbox -- confirms it works

7. GET /api/test/mailbox/testuser@mail1.test
   → Returns messages via API -- assert subject = "hello"

 RESULT
──────────────────────────────────────────────
  ✓ API created the test data in Postgres
  ✓ Postfix on mail1 picked up the domain + mailbox from Postgres
  ✓ Postfix on mail2 successfully relayed via SMTP to mail1
  ✓ Postfix on mail1 handed the message to Dovecot (LMTP)
  ✓ Dovecot stored the message in Postgres
  ✓ Dovecot IMAP auth works against the API-created user
  ✓ The API can read back the same data Dovecot stored
  ✓ The entire chain is verified using RESTMAIL as both the factory and the inspector
```

### OpenAPI Specification

The API is documented using an **OpenAPI 3.1 spec** that serves as both documentation and a testing contract:

- The spec is the **single source of truth** for all API endpoints, request/response schemas, and error codes
- Generated from Go handler annotations using a tool like `swag` or hand-maintained as `openapi.yaml`
- Serves interactive Swagger UI at `/api/docs` in development
- **Used by the test suite** to validate that every endpoint responds with the correct status codes, headers, and response shapes -- the OpenAPI spec is the contract the tests assert against
- Frontend TypeScript types can be auto-generated from the spec (e.g. `openapi-typescript`)
- The TUI client can also be generated from the spec

```
openapi.yaml (or generated)
    │
    ├──► Swagger UI at /api/docs (interactive browser)
    ├──► Go test suite: validate responses match spec
    ├──► TypeScript types: auto-generated for React frontend
    └──► API client: auto-generated for TUI
```

### Go Web Framework: chi

The API uses **[chi](https://github.com/go-chi/chi)** (`github.com/go-chi/chi/v5`) as the HTTP router. chi was chosen over alternatives because:

- **stdlib-compatible** — built on top of `net/http`, not a replacement. All standard `http.Handler` and `http.HandlerFunc` signatures work directly. No framework lock-in.
- **Middleware-friendly** — chi's middleware stack is just `func(http.Handler) http.Handler`, the standard Go pattern. Our JWT auth, CORS, request logging, and rate limiting middleware compose naturally.
- **Lightweight** — zero dependencies beyond the Go standard library. No reflection, no code generation.
- **Route groups** — clean nested route groups with per-group middleware (e.g. `/api/admin/*` gets admin-only auth middleware, `/api/test/*` gets dev-only restriction).
- **URL parameters** — `chi.URLParam(r, "id")` for path parameters like `/api/messages/:id`.

```go
r := chi.NewRouter()

// Global middleware
r.Use(middleware.RequestID)
r.Use(middleware.Logger)
r.Use(middleware.Recoverer)
r.Use(cors.Handler(corsOptions))

// Public routes
r.Post("/api/v1/auth/login", handlers.Login)
r.Post("/api/v1/auth/refresh", handlers.RefreshToken)

// Authenticated routes
r.Group(func(r chi.Router) {
    r.Use(auth.JWTMiddleware)
    r.Get("/api/v1/accounts", handlers.ListAccounts)
    r.Get("/api/v1/accounts/{id}/folders", handlers.ListFolders)
    // ... all authenticated endpoints
})

// Admin routes
r.Group(func(r chi.Router) {
    r.Use(auth.JWTMiddleware)
    r.Use(auth.AdminOnly)
    r.Get("/api/v1/admin/domains", handlers.ListDomains)
    // ... all admin endpoints
})

// Unauthenticated public endpoints
r.Get("/mail/config-v1.1.xml", handlers.MozillaAutoconfig)
r.Get("/.well-known/mta-sts.txt", handlers.MTASTSPolicy)

// Server-to-server (RESTMAIL)
r.Post("/restmail/messages", handlers.RestmailDeliver)
```

### Key Considerations
- The API is the **single source of truth** for managing data across all three mail domains
- All endpoints are versioned under `/api/v1/` (see API Versioning above)
- When the API creates a mailbox on mail1.test, it writes to the same Postgres tables that mail1's Postfix/Dovecot read from -- no separate provisioning needed
- For mail3.test (rest-mail), the API handles everything natively. For mail1/mail2 (traditional), the API writes to Postgres and the traditional servers pick up changes on their next query
- The `/restmail/` endpoints are public-facing (used by other RESTMAIL servers). The `/api/test/` endpoints should be restricted to development/testing environments
- For outbound mail from mail3.test, the API triggers the gateway to relay outbound via SMTP (or RESTMAIL upgrade if the remote supports it)
- For outbound mail from mail1/mail2 linked accounts, the API submits to the appropriate Postfix over SMTP
- Authentication uses JWT with the following specifics:
  - **Access token:** Short-lived (15 minutes). Sent in `Authorization: Bearer <token>` header. Contains claims: `sub` (mailbox ID), `email` (address), `webmail_account_id`, `iss` ("restmail"), `iat`, `exp`. Signed with HMAC-SHA256 using `JWT_SECRET`.
  - **Refresh token:** Long-lived (7 days). Stored in an **HTTP-only, Secure, SameSite=Strict cookie** (`restmail_refresh`). Not accessible to JavaScript. Used only by `POST /api/v1/auth/refresh` to issue a new access token.
  - **Token revocation:** Access tokens are short-lived enough that revocation isn't needed — they expire in 15 minutes. Refresh tokens are invalidated on logout (`POST /api/v1/auth/logout` clears the cookie). If a user loses their refresh cookie, they simply log in again.
  - **Password hashing:** `{BLF-CRYPT}` (bcrypt) with cost factor **10**. This is Dovecot-compatible — Dovecot, Postfix, and the Go backend all read/write the same bcrypt hashes from the `mailboxes.password` column.
- The backend should be stateless and horizontally scalable (single instance is fine for this project)
- **Logging:** All Go services write structured JSON logs to **stdout only**. No custom log storage, no log files, no log rotation inside the containers. External systems handle log collection — Kubernetes uses the container stdout stream, `docker compose logs` works in development, and users can pipe to whatever log aggregation they want (Loki, ELK, Datadog, etc.). The structured JSON format (with `level`, `event`, `ts`, and contextual fields) is the standard — consistent enough for fail2ban parsing, human-readable enough for `docker compose logs -f`. Go's `log/slog` package (stdlib, structured, JSON output) is the logging library.
- Configuration via environment variables: `DB_HOST`, `DB_NAME`, `DB_USER`, `DB_PASS`, `API_PORT`, `TLS_CERT_PATH`, `TLS_KEY_PATH`, `LOG_LEVEL`, `DNS_PROVIDER`, `JWT_SECRET`

### TODO
- [x] Initialise Go module (`go mod init`)
- [x] Set up project structure (cmd/, internal/api, internal/db, internal/mail, etc.)
- [x] Create `Taskfile.yml` (Go Task) with common development tasks (see Development Tooling below)
- [x] Set up GORM connection with PostgreSQL driver (`gorm.io/driver/postgres`)
- [x] Implement AutoMigrate for all GORM models at startup
- [x] Write OpenAPI 3.1 spec (`openapi.yaml`) for all endpoints
- [x] Set up Swagger UI serving at `/api/docs` in dev mode
- [x] Implement RESTMAIL server-to-server endpoints (`/restmail/messages`, `/restmail/mailboxes`, `/restmail/capabilities`)
- [x] Implement auth endpoints (login/logout/refresh with JWT)
- [x] Implement linked accounts endpoints (list, add, remove, set primary)
- [x] Implement folder CRUD (list, create, rename, delete)
- [x] Implement message listing with cursor-based pagination
- [x] Implement message retrieval (full message, raw source, attachments)
- [x] Implement attachment download (stream from storage ref)
- [x] Implement compose/send endpoint (route to gateway or Postfix depending on domain)
- [x] Implement message actions (delete, mark read/unread, flag, star, move)
- [x] Implement draft endpoints (save, auto-save, discard, send)
- [x] Implement search endpoint (Postgres full-text search, folder/date/sender filters)
- [x] Implement thread grouping (References/In-Reply-To based threading)
- [x] Implement contacts (auto-collect from sent mail, autocomplete suggestions)
- [x] Implement SSE endpoint for real-time events (new message, folder update, queue status)
- [x] Implement admin endpoints (domains, mailboxes, aliases, webmail accounts, DNS records)
- [x] Implement certificate management endpoints (list, upload, provision, renew)
- [x] Implement DKIM key management endpoints (generate, list, revoke, DNS record)
- [x] Implement queue management endpoints (list, retry, bounce, stats)
- [x] Implement pipeline/filter management endpoints (CRUD, test)
- [x] Implement log query endpoints (structured logs, delivery log, pipeline log)
- [x] Implement health/status endpoints (API health, per-server probe, cert expiry)
- [x] Implement test endpoints (send, verify, probe SMTP/IMAP/POP3/DNS, reset, seed, snapshot/restore)
- [x] Add middleware (CORS, JWT auth, request logging, error handling, rate limiting)
- [x] Add Dockerfile for the Go backend
- [x] Add to docker-compose.yml
- [x] Generate TypeScript types from OpenAPI spec for the React frontend

---

## Phase 6: TLS & Certificate Management

TLS is not optional -- it's a hard requirement. The RESTMAIL upgrade path explicitly opens an HTTPS connection. SMTP STARTTLS is expected by all modern servers. IMAPS (993) and SMTPS (465) are the default ports for modern clients. Without TLS, the system fails at multiple levels.

### Two Modes

**Development (.test environment):** A self-signed CA generates certificates for all `.test` domains at container build time. All containers trust this CA.

**Production:** Let's Encrypt issues real certificates. Because we control the TLS termination layer in Go (not Postfix/Dovecot), we can leverage ACME (Let's Encrypt) directly -- the Go backend requests and renews certificates automatically.

### SNI-Based Certificate Selection

This is where being a non-traditional mail server gives us a real advantage. Traditional mail servers serve a single TLS certificate per IP address. When one server handles multiple domains (mail3.test, example.com, company.org), it can only present one cert -- usually for the "primary" hostname. Dovecot has SNI support but it's limited and fiddly to configure.

Because our gateway terminates TLS in Go, we have full programmatic control via `tls.Config.GetCertificate`. When a client connects:

1. The Go TLS listener inspects the **SNI hostname** from the TLS ClientHello
2. Looks up the correct certificate for that domain from the cert store
3. Serves the matching certificate
4. The client sees a valid cert for exactly the domain it connected to

```
Client connects to port 993 (IMAPS)
  │
  │──TLS ClientHello (SNI: mail3.test)──────────►│
  │                                                │  Gateway TLS listener:
  │                                                │  1. Read SNI from ClientHello
  │                                                │  2. Lookup cert for "mail3.test"
  │                                                │  3. Serve that cert
  │◄─TLS ServerHello (cert: mail3.test)───────────│
  │                                                │
  │  TLS established for mail3.test               │
  │──IMAP LOGIN ...───────────────────────────────►│
```

This means a single gateway instance can serve **unlimited domains**, each with its own valid TLS certificate, selected dynamically at connection time. No restart, no config reload -- add a domain via the API, upload or auto-provision a cert, and it's live.

### Certificate Storage

Certificates are stored in the database alongside their domains:

```sql
CREATE TABLE certificates (
    id              SERIAL PRIMARY KEY,
    domain_id       INTEGER REFERENCES domains(id),
    cert_pem        TEXT NOT NULL,           -- PEM-encoded certificate chain
    key_pem         TEXT NOT NULL,           -- PEM-encoded private key (encrypted at rest)
    issuer          VARCHAR(255),            -- "letsencrypt", "self-signed", "manual"
    not_before      TIMESTAMP NOT NULL,
    not_after       TIMESTAMP NOT NULL,
    auto_renew      BOOLEAN DEFAULT true,
    created_at      TIMESTAMP DEFAULT NOW(),
    updated_at      TIMESTAMP DEFAULT NOW()
);
```

The gateway loads certs from the database at startup and caches them in memory. When a cert is added/renewed via the API, the cache is invalidated (via Postgres LISTEN/NOTIFY or a polling interval).

### Let's Encrypt Integration (Production)

The Go backend includes an ACME client that:

1. When a domain is added, automatically requests a certificate from Let's Encrypt
2. Uses HTTP-01 or DNS-01 challenge (DNS-01 is preferred for mail servers since port 80 may not be open)
3. Stores the cert in the database
4. Schedules renewal before expiry (Let's Encrypt certs last 90 days, renew at 60 days)

```
API                         ACME Client              Let's Encrypt
 │                              │                          │
 │──POST /api/admin/domains──►│                          │
 │  { name: "example.com" }   │                          │
 │                              │──Request cert────────────►│
 │                              │  (DNS-01 challenge)      │
 │                              │◄─Challenge token─────────│
 │                              │──Create DNS TXT record──►│ (via DNS provider API)
 │                              │──Verify challenge────────►│
 │                              │◄─Certificate─────────────│
 │                              │                          │
 │◄─201 Created───────────────│                          │
 │  (domain + cert ready)     │                          │
```

### Self-Signed CA (Development)

For the `.test` environment, we generate a local Certificate Authority at first startup:

```
docker/certs/
  ca.key              - CA private key
  ca.crt              - CA certificate (trusted by all containers)
  mail1.test.key      - Server private key
  mail1.test.crt      - Server certificate (signed by CA)
  mail2.test.key
  mail2.test.crt
  mail3.test.key
  mail3.test.crt
```

An init container or entrypoint script generates these on first run. All containers mount `ca.crt` as a trusted root. The Go gateway and API trust it too.

### Kubernetes Considerations

In a Kubernetes deployment, the SNI-based cert selection opens up interesting possibilities:

- A single gateway deployment serves all tenant domains
- Certificates stored in Kubernetes Secrets or the database
- cert-manager can handle Let's Encrypt provisioning
- The gateway pod inspects SNI to route to the correct tenant's data -- true multi-tenant mail hosting from a single ingress point

### Certificate Management API

```
═══════════════════════════════════════════════════════════════════════════
 CERTIFICATE MANAGEMENT
 Added to: ADMINISTRATION layer of RESTMAIL Protocol API
═══════════════════════════════════════════════════════════════════════════

  GET    /api/admin/certs                     - List all certificates
  GET    /api/admin/certs/:domain             - Get cert details for a domain (expiry, issuer, etc.)
  POST   /api/admin/certs/:domain             - Upload manual cert (PEM cert + key)
  POST   /api/admin/certs/:domain/provision   - Trigger Let's Encrypt provisioning
  POST   /api/admin/certs/:domain/renew       - Force renewal
  DELETE /api/admin/certs/:domain             - Remove cert (domain will use default/fallback)
```

### DKIM Keys (related)

DKIM signing keys are per-domain crypto material, similar to TLS certs. They should live alongside certs in the database:

```sql
CREATE TABLE dkim_keys (
    id              SERIAL PRIMARY KEY,
    domain_id       INTEGER REFERENCES domains(id),
    selector        VARCHAR(63) NOT NULL,     -- e.g. "2026", "default"
    private_key_pem TEXT NOT NULL,             -- RSA or Ed25519 private key
    public_key_pem  TEXT NOT NULL,             -- for publishing in DNS
    algorithm       VARCHAR(20) DEFAULT 'rsa-sha256',
    key_size        INTEGER DEFAULT 2048,
    active          BOOLEAN DEFAULT true,
    created_at      TIMESTAMP DEFAULT NOW(),
    UNIQUE(domain_id, selector)
);
```

The `dkim_sign` pipeline filter reads the active key for the sending domain. The DNS TXT record for `selector._domainkey.domain` is published via the dnsmasq config (dev) or real DNS (production).

### Private Key Encryption (Master Key)

TLS certificate private keys and DKIM signing private keys are stored in PostgreSQL (in the `certificates` and `dkim_keys` tables). Storing private keys in plaintext in the database would mean anyone who gains database access (SQL injection, backup theft, compromised DB credentials) also gets all the private keys. To prevent this, we encrypt all private keys at rest using **AES-256-GCM** before storing them in the database.

**How it works:**

1. A **master key** is a 256-bit (32-byte) secret provided via the `MASTER_KEY` environment variable
2. When the API writes a private key to the database, it encrypts the PEM-encoded key using AES-256-GCM with the master key
3. When the API or gateway reads a private key from the database, it decrypts it in memory
4. The master key itself never touches the database — it lives only in the environment

```
                Environment                      PostgreSQL
                ───────────                      ──────────
  MASTER_KEY="base64-encoded-32-byte-secret"
                    │
                    ▼
            ┌───────────────┐
   PEM ────►│ AES-256-GCM   │────► encrypted blob ────► key_pem column
            │   encrypt     │
            └───────────────┘
            ┌───────────────┐
   PEM ◄────│ AES-256-GCM   │◄──── encrypted blob ◄──── key_pem column
            │   decrypt     │
            └───────────────┘
```

**AES-256-GCM** is an authenticated encryption mode — it provides both confidentiality (nobody can read the key) and integrity (nobody can tamper with it without detection).

**In the dev environment:** The master key is set in `docker-compose.yml` as a hardcoded value (fine for `.test` domains). **In production:** The master key should come from a secrets manager (Vault, AWS Secrets Manager, etc.) or be injected by the deployment platform.

**Key rotation:** If you need to rotate the master key, a migration script re-encrypts all stored private keys with the new key. Both old and new keys are provided during rotation.

### Key Considerations
- The gateway **must** support: STARTTLS on ports 25/587, implicit TLS on ports 465/993, HTTPS on the API port
- All Go TLS listeners use `tls.Config.GetCertificate` for dynamic SNI-based selection
- Private keys are encrypted at rest in the database (AES-256-GCM with a master key from environment) — see above
- Certificate expiry monitoring: the health endpoint reports certs expiring within 30 days
- For the `.test` dev environment, the self-signed CA is generated once and reused across restarts (persisted in a Docker volume)
- Postfix and Dovecot on mail1/mail2 also need TLS -- they get their certs mounted from the same generated cert directory

### TODO
- [x] Create self-signed CA generation script (run once at first startup)
- [x] Generate per-domain certs signed by the dev CA (mail1.test, mail2.test, mail3.test)
- [x] Mount CA cert as trusted root in all containers
- [x] Configure Postfix STARTTLS with generated certs (smtpd_tls_cert_file, smtpd_tls_key_file)
- [x] Configure Dovecot TLS with generated certs (ssl_cert, ssl_key)
- [x] Implement SNI-based `tls.Config.GetCertificate` in the gateway (with fsnotify hot-reload)
- [x] Implement certificate storage in database (certificates table)
- [x] Implement certificate management API endpoints
- [x] Implement cert caching with invalidation (Postgres LISTEN/NOTIFY or polling)
- [x] Implement DKIM key storage and management (dkim_keys table)
- [x] Add DKIM key management API endpoints
- [x] Implement ACME client for Let's Encrypt (production mode)
- [x] Add cert expiry monitoring to health endpoints
- [x] Create database migrations for `certificates` and `dkim_keys` tables
- [x] Add cert generation to docker-compose init / entrypoint flow
- [x] Implement AES-256-GCM encryption/decryption helper for private key storage
- [x] Add `MASTER_KEY` environment variable to docker-compose.yml (hardcoded for dev)
- [x] Implement master key rotation script (re-encrypt all keys with new master key)

---

## Phase 7: React Webmail Frontend

A modern, slick single-page webmail application built with React, Zustand for state management, and Tailwind CSS for styling.

### Tech Stack
- **React** (with Vite for dev/build tooling)
- **Zustand** for global state (auth, mailbox, message selection)
- **Tailwind CSS** for utility-first styling
- **shadcn/ui** for composable, accessible UI components (Button, Input, DropdownMenu, Dialog, ScrollArea, Separator, Badge, Tooltip, Sheet, etc.) — standard primitives built on Radix UI, fully customizable via Tailwind
- **TypeScript** throughout

### UI Layout

The webmail fills the entire browser viewport — no page-level scrolling. Two-panel layout: a narrow **left sidebar** and a wide **right panel**.

```
┌─────────────┬────────────────────────────────────────────────────┐
│             │  [ Compose ] [ Get Mail ]              user@… [▼]  │
│  REST MAIL  ├────────────────────────────────────────────────────┤
│  (logo)     │                                                    │
│             │  ★  alice@mail1.test     Re: Project plan    10:32 │
│ ▼ alice@…   │     bob@mail2.test       Invoice #4021       09:15 │
│   Inbox (3) │     charlie@mail3.test   Meeting notes       Yesterday│
│   Sent      │     dave@example.com     Quick question      Yesterday│
│   Drafts    │                                                    │
│   Trash     │                                                    │
│   Archive   │                                                    │
│             ├────────────────────────────────────────────────────┤
│ ▶ bob@… (5) │  From: alice@mail1.test                            │
│             │  Date: 17 Feb 2026 10:32                           │
│             │  Subject: Re: Project plan                         │
│             │  ──────────────────────────────────────────         │
│             │                                                    │
│             │  Hi Chris,                                         │
│             │                                                    │
│             │  Here's the updated project plan. Let me know      │
│             │  if you have any questions.                        │
│             │                                                    │
│             │  Best regards,                                     │
│             │  Alice                                             │
│             │                                                    │
│ [Add Account│                                                    │
│     ]       │                                                    │
└─────────────┴────────────────────────────────────────────────────┘
```

#### Left Sidebar

- **Header:** "REST MAIL" logo/text at the top
- **Account tree:** Lists all accounts linked to the user's webmail account. The **primary account** is always the first item. Each account is a collapsible tree node:
  - **Collapsed:** Shows the account email address and the **unread inbox count** in a badge (e.g. `bob@… (5)`)
  - **Expanded:** Shows the account's folders underneath (Inbox, Sent, Drafts, Trash, Archive, custom folders). Each folder shows its own unread count. The account-level count disappears when expanded — the per-folder counts replace it
- **Add Account button:** Pinned to the bottom of the sidebar. Always visible regardless of scroll position. Clicking it opens the Add Account view in the right panel
- Clicking a folder in the tree loads that folder's message list into the right panel

#### Right Panel — Top Bar

The top bar spans the full width of the right panel.

- **Left side:** `[ Compose ]` button and `[ Get Mail ]` button
- **Right side:** User display (shows the logged-in user's email address). Clicking it opens a dropdown menu:

```
┌─────────────────────┐
│ View Account Details │
├─────────────────────┤
│ Theme            ▶  │
│  ┌─────────────────┐│
│  │ ○ Light         ││
│  │ ● Dark          ││
│  └─────────────────┘│
├─────────────────────┤
│ Logout              │
└─────────────────────┘
```

- **View Account Details:** Opens an account details view in the right panel (profile info, linked accounts, quota usage)
- **Theme:** Expandable sub-menu with color scheme options (Light, Dark). Selection is persisted in local storage and applied immediately
- **Logout:** Clears the refresh cookie and redirects to the login page

#### Right Panel — Mail List (top half)

- Displays messages for the currently selected folder
- Each row shows: read/unread indicator (bold for unread), starred/flagged icon, sender name, subject line, date/time
- Clicking a message selects it and displays it in the mail viewer below
- Supports keyboard navigation (up/down arrows to move between messages, Enter to open)
- Pagination: infinite scroll or "Load more" at the bottom (cursor-based pagination from the API)

#### Right Panel — Mail Viewer (bottom half)

- **Header area:** Displays the message metadata — From (name + address), Date, Subject, To, CC (if any)
- **Body area:** Renders the email body below the header, separated by a horizontal rule. HTML emails are rendered safely (sanitized). Plain text emails are displayed in a monospace font
- **Actions:** Reply, Reply All, Forward, Delete, Move to folder, Mark as read/unread, Star/unstar — accessible via toolbar buttons or keyboard shortcuts

#### Add Account View

When the user clicks "Add Account", the right panel swaps to show the account setup form:

```
┌──────────────────────────────────────────────────────┐
│  Add Account                                         │
├──────────────────────────────────────────────────────┤
│                                                      │
│  Display Name:    [ Alice Smith                    ] │
│  Email Address:   [ alice@example.com              ] │
│  IMAP Server:     [ imap.example.com               ] │
│  IMAP Port:       [ 993          ] [✓] Use SSL      │
│  SMTP Server:     [ smtp.example.com               ] │
│  SMTP Port:       [ 465          ] [✓] Use SSL      │
│  Username:        [ alice@example.com              ] │
│  Password:        [ ••••••••                       ] │
│                                                      │
│  [ Test Connection ]    [ Cancel ]    [ Save ]       │
│                                                      │
│  ✓ IMAP connection successful                        │
│  ✓ SMTP authentication successful                    │
│                                                      │
└──────────────────────────────────────────────────────┘
```

- **Test Connection button:** Validates the IMAP and SMTP credentials against the remote server before saving. Shows success/failure status for each protocol
- **Cancel:** Returns to the previous mail list view
- **Save:** Creates the linked account via the API and adds it to the sidebar tree

#### Compose View

When the user clicks "Compose", the right panel swaps to the compose view (see TipTap editor section below for the full compose UI). The compose view has the standard fields: From (dropdown of linked accounts), To, CC, BCC, Subject, and the rich text editor body. When the user sends the email, the API call is made immediately and the view returns to the mail list.

#### New Email Notifications

The webmail uses **Server-Sent Events (SSE)** via the existing `/api/v1/accounts/:id/events` endpoint to receive real-time push notifications when new email arrives:

- **Desktop notification:** Browser `Notification` API — shows sender and subject line as a system notification (requires user permission grant)
- **In-app badge:** The unread count on the relevant folder in the sidebar updates in real-time
- **Visual indicator:** A brief toast/snackbar appears at the top of the mail list ("1 new message from alice@mail1.test") that the user can click to jump to the new message
- **Sound (optional):** A subtle notification sound can be enabled in settings (off by default)

### Key Features
- **Full-viewport two-panel layout** — left sidebar (accounts + folders) and right panel (top bar + mail list + mail viewer). No page-level scrolling
- **Login screen** authenticating against webmail accounts
- **Account tree sidebar** — collapsible tree nodes for each linked account, showing folder structure with per-folder unread counts
- **Add Account flow** — right panel form with IMAP/SMTP fields and a "Test Connection" button to validate before saving
- **Compose view** — replaces the right panel with "From" address selector (populated from linked accounts), To/CC/BCC/Subject fields, and **rich text editor** with plain text fallback (see TipTap section below)
- **Get Mail button** — explicitly queries the server for new messages for the active account
- **User dropdown menu** — View Account Details, Theme switcher (Light/Dark), Logout
- **Real-time notifications** — SSE-powered desktop notifications, in-app badge updates, and toast messages for new email
- **Theme support** — Light and Dark color schemes, user preference persisted in local storage
- Responsive design for smaller viewports

### Compose View: Rich Text Editor (TipTap)

The compose view supports both **HTML (rich text)** and **plain text** modes. Users can toggle between them. HTML mode is the default — most modern email is HTML.

The rich text editor uses **[TipTap](https://tiptap.dev/)** (`@tiptap/react`), a headless, extensible editor built on ProseMirror. TipTap was chosen because:

- **Headless** — we control the UI entirely with Tailwind CSS, no forced theme
- **Extensible** — modular extensions for exactly the features we need
- **ProseMirror foundation** — battle-tested editing engine used by Notion, GitLab, and many others
- **React-native** — first-class React integration via `@tiptap/react`

**Editor features for email composition:**

| Feature | TipTap Extension | Why |
|---------|-----------------|-----|
| Bold, italic, underline | `StarterKit` (built-in) | Basic formatting |
| Headings (H1-H3) | `StarterKit` (built-in) | Structure |
| Bullet / numbered lists | `StarterKit` (built-in) | Common in email |
| Links | `@tiptap/extension-link` | URLs with preview |
| Images (inline) | `@tiptap/extension-image` | Embed images in email body |
| Text color / highlight | `@tiptap/extension-color`, `@tiptap/extension-highlight` | Email formatting |
| Text alignment | `@tiptap/extension-text-align` | Left/center/right |
| Blockquotes | `StarterKit` (built-in) | Reply quoting |
| Horizontal rule | `StarterKit` (built-in) | Visual separator |
| Placeholder | `@tiptap/extension-placeholder` | "Write your message..." |

```
┌──────────────────────────────────────────────────────────────┐
│  Compose                                               [x]   │
├──────────────────────────────────────────────────────────────┤
│  From: [ alice@mail3.test      ▼ ]                           │
│  To:   [ bob@mail1.test          ]                           │
│  Cc:   [                          ]                          │
│  Subject: [ Project update         ]                         │
├──────────────────────────────────────────────────────────────┤
│  [B] [I] [U] | [H1][H2] | [•][1.] | [🔗][📎] | [HTML|Text] │
├──────────────────────────────────────────────────────────────┤
│                                                              │
│  Hi Bob,                                                     │
│                                                              │
│  Here's the **project update** for this week:                │
│                                                              │
│  • Phase 1 complete                                          │
│  • Phase 2 in progress                                       │
│                                                              │
│  Best regards,                                               │
│  Alice                                                       │
│                                                              │
├──────────────────────────────────────────────────────────────┤
│  [Attach files]              [ Save Draft ] [ Send ]         │
└──────────────────────────────────────────────────────────────┘
```

**Plain text mode:** When the user toggles to plain text, the editor switches to a standard `<textarea>`. The API always sends both `text/plain` and `text/html` parts in a `multipart/alternative` MIME structure — the HTML is generated from TipTap, the plain text is either typed directly or auto-generated by stripping HTML tags.

**Reply quoting:** When replying, the original message body is inserted as a blockquote (`<blockquote>`) with a "On {date}, {sender} wrote:" prefix. The cursor is positioned above the quote for the user's response.

### TODO
- [x] Scaffold React + Vite + TypeScript project
- [x] Install and configure Tailwind CSS (with Light and Dark theme support)
- [x] Install Zustand, set up stores (auth, activeAccount, mail, ui, theme)
- [x] Build login page
- [x] Build full-viewport two-panel layout shell (left sidebar + right panel)
- [x] Build left sidebar header with "REST MAIL" logo
- [x] Build account tree component (collapsible nodes, primary account first, per-folder unread counts, inbox count badge when collapsed)
- [x] Build "Add Account" button pinned to sidebar bottom
- [x] Build right panel top bar (Compose button, Get Mail button, user dropdown)
- [x] Build user dropdown menu (View Account Details, Theme sub-menu with Light/Dark, Logout)
- [x] Build theme switcher (Light/Dark schemes, persist selection in local storage, apply immediately)
- [x] Build message list component (top half of right panel, click to select, keyboard navigation, cursor-based pagination)
- [x] Build mail viewer component (bottom half of right panel, message header + body rendering, HTML sanitization, plain text monospace)
- [x] Build mail viewer action buttons (Reply, Reply All, Forward, Delete, Move, Mark read/unread, Star/unstar)
- [x] Build Add Account view (replaces right panel: display name, email, IMAP/SMTP server/port/SSL fields, username, password)
- [x] Implement "Test Connection" button for Add Account (validates IMAP + SMTP credentials, shows per-protocol success/failure)
- [x] Build compose view (replaces right panel: From dropdown, To/CC/BCC/Subject fields)
- [x] Install TipTap (`@tiptap/react`, `@tiptap/starter-kit`, extensions for link, image, color, text-align, placeholder)
- [x] Build rich text editor component with formatting toolbar (bold, italic, underline, headings, lists, links, images, color, alignment)
- [x] Implement HTML ↔ plain text toggle in compose view
- [x] Implement reply quoting (blockquote with "On {date}, {sender} wrote:" prefix)
- [x] Ensure compose sends both `text/plain` and `text/html` parts (multipart/alternative)
- [x] Implement "Get Mail" button (queries server for new messages for active account)
- [x] Implement View Account Details panel (profile info, linked accounts, quota usage)
- [x] Set up SSE connection to `/api/v1/accounts/:id/events` for real-time updates (multi-account SSE with reconnect backoff)
- [x] Implement desktop notifications for new email (Browser Notification API, requires permission grant)
- [x] Implement in-app notification: sidebar unread count badge updates in real-time
- [x] Implement in-app notification: toast/snackbar for new messages ("1 new message from …")
- [x] Wire up API calls to Go backend
- [x] Add Dockerfile (multi-stage: build with Node, serve with nginx)
- [x] Add to docker-compose.yml
- [x] Style and polish the UI

---

## Phase 8: Protocol Gateways (mail3.test)

This is the core of the project. The **protocol gateways** are separate Go services — one per protocol — that listen on standard mail ports and translate SMTP/IMAP/POP3 wire protocol into REST API calls against the shared Go backend. There is no Postfix or Dovecot involved in mail3.test -- it's pure Go.

Each gateway is a **protocol translator**, not a proxy. It parses incoming protocol commands, makes the equivalent REST API call, and constructs the correct protocol response.

### Gateway Architecture — Separate Per-Protocol Services

Instead of a single monolithic gateway binary, each protocol runs as its own service/container:

| Service            | Binary              | Ports          | Responsibility                                        |
|--------------------|----------------------|----------------|-------------------------------------------------------|
| **smtp-gateway**   | `cmd/smtp-gateway/`  | 25, 465, 587   | Inbound MTA, submission with AUTH, outbound queue      |
| **imap-gateway**   | `cmd/imap-gateway/`  | 143, 993       | IMAP4rev1 with IDLE, STARTTLS, implicit TLS           |
| **pop3-gateway**   | `cmd/pop3-gateway/`  | 110, 995       | POP3 with STARTTLS, implicit TLS                      |

**Benefits of separate services:**
- **Independent scaling**: IMAP connections are long-lived (IDLE); SMTP is bursty. Scale each independently.
- **Isolated failure domains**: A bug or crash in the POP3 handler doesn't take down SMTP delivery.
- **Smaller targeted codebases**: Each binary is focused on one protocol, easier to test and maintain.
- **Flexible deployment**: Run all three on one host, or spread across machines. Each targets the shared REST API pool.

All three services share a common internal library (`internal/gateway/`) for REST API client, TLS config, and connection throttling.

```
┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
│  smtp-gateway   │  │  imap-gateway   │  │  pop3-gateway   │
│  :25 :465 :587  │  │  :143 :993      │  │  :110 :995      │
│                 │  │                 │  │                 │
│  SMTP inbound   │  │  IMAP4rev1      │  │  POP3           │
│  SMTP AUTH      │  │  IDLE → SSE     │  │  STAT/LIST/RETR │
│  Outbound queue │  │  GETQUOTA       │  │                 │
│  + retry worker │  │                 │  │                 │
└────────┬────────┘  └────────┬────────┘  └────────┬────────┘
         │                    │                    │
         │        HTTP/REST (shared API pool)      │
         └──────────────┬─┬───────────────────────┘
                        │ │
                        ▼ ▼
              ┌──────────────────┐
              │  Go REST API     │
              │  (api container) │
              │  → Postgres      │
              └──────────────────┘
```

**docker-compose.yml** runs each as a separate container:

```yaml
smtp-gateway:
  build: { context: ., dockerfile: docker/smtp-gateway/Dockerfile }
  ports: ["25:25", "465:465", "587:587"]
  environment:
    API_BASE_URL: http://api:8080
  networks:
    restmail: { ipv4_address: 172.20.0.14 }

imap-gateway:
  build: { context: ., dockerfile: docker/imap-gateway/Dockerfile }
  ports: ["143:143", "993:993"]
  environment:
    API_BASE_URL: http://api:8080
  networks:
    restmail: { ipv4_address: 172.20.0.15 }

pop3-gateway:
  build: { context: ., dockerfile: docker/pop3-gateway/Dockerfile }
  ports: ["110:110", "995:995"]
  environment:
    API_BASE_URL: http://api:8080
  networks:
    restmail: { ipv4_address: 172.20.0.16 }
```

### Protocol Translation Examples

**SMTP inbound** (mail1.test delivers to mail3.test):
| SMTP Command                     | REST API Call                                     |
|----------------------------------|---------------------------------------------------|
| `EHLO mail1.test`                | (no API call -- gateway responds directly)        |
| `MAIL FROM:<bob@mail1.test>`     | (stash sender in session state)                   |
| `RCPT TO:<alice@mail3.test>`     | `GET /api/mailboxes?address=alice@mail3.test`     |
| `DATA` + message body            | `POST /api/messages` with parsed headers + body   |
| `QUIT`                           | (close connection)                                |

**IMAP** (client reads mailbox on mail3.test):
| IMAP Command                     | REST API Call                                     |
|----------------------------------|---------------------------------------------------|
| `LOGIN alice password`           | `POST /api/auth/login`                            |
| `LIST "" *`                      | `GET /api/accounts/:id/folders`                   |
| `SELECT INBOX`                   | `GET /api/accounts/:id/folders/inbox/messages`    |
| `FETCH 1 (BODY[])`              | `GET /api/messages/:id`                           |
| `SEARCH FROM "alice"`           | `GET /api/accounts/:id/search?from=alice`         |
| `STORE 1 +FLAGS (\Seen)`       | `PATCH /api/messages/:id { is_read: true }`       |
| `COPY 1 "Archive"`             | `POST /api/messages/:id/move { folder: "Archive" }` |
| `CREATE "Projects"`            | `POST /api/accounts/:id/folders { name: "Projects" }` |
| `IDLE`                          | `GET /api/accounts/:id/events` (SSE stream)       |
| `GETQUOTA`                      | `GET /api/accounts/:id/quota`                     |
| `LOGOUT`                        | `POST /api/auth/logout` + close connection        |

**POP3** (client retrieves mail on mail3.test):
| POP3 Command                     | REST API Call                                     |
|----------------------------------|---------------------------------------------------|
| `USER alice@mail3.test`          | (stash username in session state)                 |
| `PASS secret`                    | `POST /api/auth/login`                            |
| `STAT`                           | `GET /api/accounts/:id/folders/inbox/messages` → count + total size |
| `LIST`                           | `GET /api/accounts/:id/folders/inbox/messages` → per-message sizes |
| `UIDL`                           | `GET /api/accounts/:id/folders/inbox/messages` → message IDs as UIDs |
| `RETR 1`                         | `GET /api/messages/:id/raw` (full RFC 2822 source) |
| `TOP 1 10`                       | `GET /api/messages/:id/raw` (return headers + first 10 body lines) |
| `DELE 1`                         | `DELETE /api/messages/:id` (mark for deletion)    |
| `NOOP`                           | (no API call — gateway responds directly)         |
| `RSET`                           | (unmark deletions in session state)               |
| `QUIT`                           | Apply pending deletions, close connection         |

POP3 is simpler than IMAP — it operates on a single mailbox (INBOX) with no folder support, no flags, and no real-time push. The gateway's POP3 handler follows the same pattern as IMAP: translate each POP3 command into the equivalent REST API call.

### Three Ways to Talk to rest-mail

| Client                    | Protocol Path                                                       |
|---------------------------|---------------------------------------------------------------------|
| Traditional mail server   | SMTP → **Protocol Gateway** → REST API → Postgres                   |
| Another RESTMAIL server   | SMTP EHLO → sees `RESTMAIL` cap → **drops TCP** → HTTPS REST API   |
| External IMAP/POP3 client | IMAP/POP3 → **Protocol Gateway** → REST API → Postgres              |
| Webmail frontend          | HTTP → **REST API directly** → Postgres (native RESTMAIL)           |
| TUI admin tool            | HTTP → **REST API directly** → Postgres (native RESTMAIL)           |

The webmail and TUI speak native RESTMAIL -- direct REST API calls. Traditional mail servers go through the protocol gateway. But when two RESTMAIL servers meet, they discover each other via the SMTP EHLO capability advertisement and **upgrade** to direct HTTPS/REST communication, skipping the SMTP protocol entirely.

### SMTP AUTH (Submission Port)

Port 587 (and 465) require **SMTP AUTH** before accepting mail for relay. This is how mail clients (Thunderbird, Apple Mail, mobile clients) authenticate before sending. Without AUTH, the gateway is an open relay.

The SMTP handler on submission ports:
1. Requires STARTTLS (587) or implicit TLS (465) before AUTH
2. Advertises `AUTH PLAIN LOGIN` in EHLO capabilities
3. Accepts `AUTH PLAIN` or `AUTH LOGIN` commands
4. Validates credentials via `POST /api/auth/login`
5. Only allows `MAIL FROM` addresses that match the authenticated user (or their linked accounts)

```
SMTP submission (port 587):
  EHLO client ──► 250-STARTTLS, 250-AUTH PLAIN LOGIN
  STARTTLS     ──► 220 Ready (upgrade to TLS)
  AUTH PLAIN   ──► 235 Authenticated (via REST API auth check)
  MAIL FROM    ──► 250 OK (only if sender matches authenticated user)
  RCPT TO      ──► 250 OK
  DATA         ──► 354 → 250 Queued
```

### Outbound Mail Queue

Traditional mail servers queue outbound messages and retry delivery on temporary failures. We do the same, but our queue is a database table -- which means we can inspect, manage, and reconstitute the email stream on demand.

When mail3.test sends an outbound email:

1. The message is inserted into the **outbound queue** in Postgres
2. A background worker picks it up and attempts delivery (SMTP relay or RESTMAIL upgrade)
3. If the remote server responds with a **4xx** (temporary failure), the message stays in the queue with an exponential backoff schedule
4. If the remote responds with **5xx** (permanent failure), a **bounce message (DSN)** is generated and delivered to the sender's inbox
5. If delivery succeeds (**2xx**), the queue entry is marked complete
6. After a configurable max retry period (default: 5 days), undelivered messages get a final bounce and are removed from the queue

```sql
CREATE TABLE outbound_queue (
    id              SERIAL PRIMARY KEY,
    message_id      INTEGER REFERENCES messages(id),
    sender          VARCHAR(255) NOT NULL,
    recipient       VARCHAR(255) NOT NULL,
    domain          VARCHAR(255) NOT NULL,       -- destination domain for MX lookup
    status          VARCHAR(20) NOT NULL DEFAULT 'pending',
                    -- pending, delivering, deferred, delivered, bounced, expired
    attempts        INTEGER DEFAULT 0,
    last_attempt    TIMESTAMP,
    next_attempt    TIMESTAMP DEFAULT NOW(),
    last_error      TEXT,                         -- last SMTP error response
    last_error_code INTEGER,                      -- last SMTP status code (421, 450, etc.)
    max_retries     INTEGER DEFAULT 30,           -- over 5 days with backoff
    created_at      TIMESTAMP DEFAULT NOW(),
    expires_at      TIMESTAMP DEFAULT NOW() + INTERVAL '5 days'
);
```

The retry schedule uses exponential backoff: 1 min, 5 min, 15 min, 30 min, 1 hour, 2 hours, 4 hours, etc.

```
Queue Worker Lifecycle:

  ┌─────────────┐
  │  New message │
  │  queued      │
  └──────┬──────┘
         │
         ▼
  ┌──────────────┐     2xx      ┌──────────┐
  │  Attempt      │────────────►│ Delivered │
  │  delivery     │             └──────────┘
  └──────┬───────┘
         │
    4xx  │  5xx
    ┌────┴────┐
    │         │
    ▼         ▼
┌────────┐  ┌─────────┐     ┌────────────┐
│Deferred│  │ Bounced  │────►│ Generate   │
│(retry) │  │          │     │ DSN to     │
└───┬────┘  └──────────┘     │ sender     │
    │                         └────────────┘
    │ (backoff timer)
    ▼
  Retry → attempt delivery again
    │
    │ (max retries or expired)
    ▼
  ┌──────────┐
  │ Expired  │──► Generate final bounce DSN
  └──────────┘
```

### Bounce Messages (DSN / NDR)

When delivery permanently fails, the gateway generates a **Delivery Status Notification** (bounce) conforming to RFC 3464. The bounce is delivered to the original sender's inbox as a new message.

A bounce message looks like a regular email:
- **From:** `mailer-daemon@mail3.test`
- **To:** the original sender
- **Subject:** `Undelivered Mail Returned to Sender`
- **Body:** human-readable explanation + machine-readable DSN attachment

The bounce is created via `POST /api/messages/deliver` to the sender's local mailbox -- it uses the same delivery pipeline as any inbound message.

### Queue Management API

```
═══════════════════════════════════════════════════════════════════════════
 QUEUE MANAGEMENT
 Added to: ADMINISTRATION layer of RESTMAIL Protocol API
═══════════════════════════════════════════════════════════════════════════

  GET    /api/admin/queue                     - List queued messages (with status filter)
  GET    /api/admin/queue/:id                 - Get queue entry details (attempts, errors, next retry)
  POST   /api/admin/queue/:id/retry           - Force immediate retry
  POST   /api/admin/queue/:id/bounce          - Force bounce (give up and notify sender)
  DELETE /api/admin/queue/:id                 - Remove from queue silently (no bounce)
  GET    /api/admin/queue/stats               - Queue statistics (pending, deferred, delivered, bounced counts)
```

### Connection Throttling & fail2ban Integration

The gateway needs to defend itself at the TCP connection level — before any SMTP conversation even begins. This is the first line of defence against spam bots, brute-force attacks, and resource exhaustion.

#### Go-Level Connection Limits (built-in)

The gateway's `net.Listener` enforces basic connection limits directly in Go:

| Limit                        | Default | Configurable via          | What it does                                                  |
|------------------------------|---------|---------------------------|---------------------------------------------------------------|
| Max concurrent connections   | 500     | `SMTP_MAX_CONNECTIONS`    | Total simultaneous SMTP connections across all source IPs     |
| Max connections per IP       | 20      | `SMTP_MAX_PER_IP`        | Limits a single IP from hogging all connections               |
| Connection rate per IP       | 10/min  | `SMTP_RATE_PER_IP`       | New connections per minute per source IP                      |
| Max AUTH failures per IP     | 5       | `SMTP_MAX_AUTH_FAILURES`  | Failed login attempts before temporary ban (10 min)           |
| SMTP command timeout         | 300s    | `SMTP_COMMAND_TIMEOUT`    | Idle timeout per SMTP command (RFC 5321 recommends 300s)      |
| Max message size             | 25MB    | `SMTP_MAX_MESSAGE_SIZE`   | Reject at DATA if exceeded                                    |
| Max recipients per message   | 100     | `SMTP_MAX_RECIPIENTS`     | Reject additional RCPT TO commands beyond limit               |

These are lightweight in-process checks using Go's `sync.Map` and `rate.Limiter` (from `golang.org/x/time/rate`). They stop the most obvious abuse but don't persist across gateway restarts and can't do IP reputation analysis.

#### fail2ban Integration (external, persistent)

For persistent banning and more sophisticated pattern detection, we integrate with **fail2ban**. fail2ban monitors log files for suspicious patterns and creates firewall rules (iptables/nftables) to ban offending IPs.

**How it works with our gateway:**

```
Gateway (Go)                       fail2ban                         iptables/nftables
    │                                   │                                │
    │──structured log ─────────────────►│ (reads log file/journal)       │
    │  "smtp_auth_failed ip=1.2.3.4"   │                                │
    │  "smtp_auth_failed ip=1.2.3.4"   │  pattern matched: 5 failures   │
    │  "smtp_auth_failed ip=1.2.3.4"   │  within 10 minutes             │
    │  "smtp_auth_failed ip=1.2.3.4"   │                                │
    │  "smtp_auth_failed ip=1.2.3.4"   │──ban ip=1.2.3.4 for 1 hour───►│
    │                                   │                                │──DROP packets from 1.2.3.4
    │◄─ connection refused (kernel) ────┼────────────────────────────────│
    │   (never reaches Go)              │                                │
```

The key advantage: banned IPs are blocked at the kernel level (iptables) — the connection never reaches the Go process. This is far more efficient than rejecting in Go, especially under DDoS conditions.

**Gateway log format for fail2ban parsing:**

The gateway emits structured JSON logs that fail2ban can parse:

```json
{"level":"warn","event":"smtp_auth_failed","ip":"1.2.3.4","user":"admin@mail3.test","ts":"2026-02-17T10:30:00Z"}
{"level":"warn","event":"smtp_connection_rejected","ip":"1.2.3.4","reason":"rate_exceeded","ts":"2026-02-17T10:30:01Z"}
{"level":"warn","event":"smtp_unknown_recipient","ip":"1.2.3.4","rcpt":"nobody@mail3.test","ts":"2026-02-17T10:30:02Z"}
{"level":"warn","event":"imap_auth_failed","ip":"1.2.3.4","user":"admin@mail3.test","ts":"2026-02-17T10:30:03Z"}
```

**fail2ban jail configuration** (`/etc/fail2ban/jail.d/restmail.conf`):

```ini
[restmail-smtp-auth]
enabled  = true
port     = 25,465,587
filter   = restmail-smtp-auth
logpath  = /var/log/restmail/gateway.log
maxretry = 5
findtime = 600
bantime  = 3600

[restmail-imap-auth]
enabled  = true
port     = 143,993
filter   = restmail-imap-auth
logpath  = /var/log/restmail/gateway.log
maxretry = 5
findtime = 600
bantime  = 3600

[restmail-smtp-abuse]
enabled  = true
port     = 25,465,587
filter   = restmail-smtp-abuse
logpath  = /var/log/restmail/gateway.log
maxretry = 3
findtime = 300
bantime  = 86400
```

**fail2ban filter** (`/etc/fail2ban/filter.d/restmail-smtp-auth.conf`):

```ini
[Definition]
failregex = "event":"smtp_auth_failed","ip":"<HOST>"
            "event":"imap_auth_failed","ip":"<HOST>"

ignoreregex =
```

**fail2ban filter** (`/etc/fail2ban/filter.d/restmail-smtp-abuse.conf`):

```ini
[Definition]
failregex = "event":"smtp_connection_rejected","ip":"<HOST>","reason":"rate_exceeded"
            "event":"smtp_too_many_unknown_recipients","ip":"<HOST>"

ignoreregex =
```

**Docker deployment:** fail2ban runs as a sidecar container (or on the host) with access to the gateway's log volume and the host's iptables:

```yaml
fail2ban:
  image: crazymax/fail2ban
  profiles: ["filter"]
  network_mode: host           # needs host networking for iptables
  cap_add:
    - NET_ADMIN                 # needed for iptables manipulation
    - NET_RAW
  volumes:
    - ./fail2ban/jail.d:/etc/fail2ban/jail.d:ro
    - ./fail2ban/filter.d:/etc/fail2ban/filter.d:ro
    - gateway-logs:/var/log/restmail:ro    # read gateway logs
```

**Ban status API:** The admin API can query fail2ban status:

```
GET /api/v1/admin/bans                - List currently banned IPs (reads fail2ban client)
DELETE /api/v1/admin/bans/:ip         - Unban an IP
```

### Key Considerations
- Each protocol gateway is a **separate Go binary** (`cmd/smtp-gateway/`, `cmd/imap-gateway/`, `cmd/pop3-gateway/`) running in its own container
- All three share `internal/gateway/` for the REST API client, TLS config, and connection throttling logic
- All TLS listeners use SNI-based certificate selection from Phase 6
- Each protocol handler is stateful per-connection (SMTP session state, IMAP selected mailbox, etc.) but translates everything into stateless REST calls
- Port 587/465 requires **SMTP AUTH** before accepting mail for relay -- authenticated sender must match a valid mailbox
- Outbound delivery goes through the **queue**, not direct relay -- this ensures retry logic and bounce generation
- The queue worker runs as a goroutine pool within the smtp-gateway process (it owns outbound delivery)
- Bounce messages conform to RFC 3464 (Delivery Status Notifications)
- **RESTMAIL capability advertisement**: the smtp-gateway inbound handler includes `RESTMAIL https://mail3.test/restmail` in its 250 EHLO response
- **RESTMAIL capability detection**: when the smtp-gateway outbound relay connects to a remote server, it checks the EHLO response for a `RESTMAIL` capability. If found, it drops the TCP connection and delivers via HTTPS to the advertised endpoint instead
- dnsmasq must have A, MX, and **PTR** records for mail3.test
- Each gateway must implement enough of its protocol to satisfy real-world clients and servers (not necessarily the full RFC, but enough for interop)
- Go-level connection limits are the first line of in-process defence; fail2ban provides persistent IP banning at the kernel level

### TODO
- [x] Design the gateway Go project structure (cmd/smtp-gateway/, cmd/imap-gateway/, cmd/pop3-gateway/, internal/gateway/)
- [x] Implement SMTP inbound handler (receive mail from external servers, translate to REST API calls)
- [x] Implement SMTP AUTH on submission ports (587/465) -- AUTH PLAIN, AUTH LOGIN
- [x] Implement STARTTLS for ports 25/587/143/110
- [x] Implement implicit TLS for ports 465/993/995
- [x] Advertise `RESTMAIL <endpoint>` in SMTP EHLO 250 capabilities response
- [x] Implement outbound mail queue (database table + worker goroutines in smtp-gateway)
- [x] Implement retry scheduler with exponential backoff
- [x] Implement bounce message generation (RFC 3464 DSN)
- [x] Implement queue management API endpoints (list, retry, bounce, delete, stats, bulk ops)
- [x] Implement SMTP outbound relay (smtp-gateway acts as SMTP client to deliver queued messages)
- [x] Implement RESTMAIL capability detection in outbound relay: check EHLO response, upgrade to HTTPS if present (with capability caching)
- [x] Implement RESTMAIL delivery endpoint (`/restmail/messages`) for server-to-server REST delivery
- [x] Implement IMAP handler (authenticate, list folders, fetch messages -- all via REST API)
- [x] Implement IMAP IDLE (push notification for new mail)
- [x] Implement IMAP GETQUOTA / GETQUOTAROOT commands (RFC 2087 — return quota info via REST API)
- [x] Implement POP3 handler (authenticate, list/retrieve messages -- all via REST API)
- [x] Create REST API client package shared by all gateway services
- [x] Add mail3.test to dnsmasq config (A + MX + PTR records)
- [x] Split single gateway container into three per-protocol containers in docker-compose.yml
- [x] Create database migration for `outbound_queue` table
- [x] Verify mail delivery: mail1.test → mail3.test (SMTP inbound, no upgrade)
- [x] Verify mail delivery: mail3.test → mail1.test (SMTP outbound via queue, no upgrade -- fallback)
- [x] Verify mail delivery: mail3.test → mail3.test (RESTMAIL upgrade path)
- [x] Verify SMTP AUTH on 587 (authenticate, send, verify relay)
- [x] Verify IMAPS on 993 (connect with implicit TLS, authenticate, read)
- [x] Verify queue retry: simulate 4xx response, confirm retry with backoff
- [x] Verify bounce: simulate 5xx response, confirm DSN delivered to sender
- [x] Verify IMAP IDLE: connect, IDLE, deliver message, confirm push notification
- [x] Implement Go-level connection limits (max concurrent, max per IP, rate per IP, auth failure tracking)
- [x] Emit structured JSON log events for fail2ban parsing (auth failures, rate exceeded, abuse patterns)
- [x] Write fail2ban jail configs for SMTP auth, IMAP auth, and SMTP abuse patterns
- [x] Write fail2ban filter configs (regex patterns matching gateway JSON log format)
- [x] Add fail2ban sidecar container to docker-compose.yml (profile: security)
- [x] Add ban management admin API endpoints (list bans, unban IP)
- [x] Document fail2ban setup and tuning for production deployments

---

## Phase 9: Go TUI Admin Tool (Bubble Tea)

A terminal-based admin interface built with Go and [Bubble Tea](https://github.com/charmbracelet/bubbletea) for direct administration of the entire rest-mail system. Runs on the host machine and connects to the API/database over the Docker network.

### Capabilities
- Manage **domains** (list, add, remove)
- Manage **users/mailboxes** (list, create, delete, reset passwords)
- **Read a user's inbox** -- browse and view messages
- **Compose and send mail** as any user

### UI Layout

```
┌──────────────────────────────────────────────────────────────────────┐
│  rest-mail admin                                        esc: back   │
├──────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  Main content area                                                   │
│  (domain list / user list / inbox view / compose form)               │
│                                                                      │
│                                                                      │
│                                                                      │
│                                                                      │
├────────────────────┬─────────────────────┬───────────────────────────┤
│  mail1.test        │  mail2.test         │  mail3.test (rest-mail)   │
│  ● 3 users         │  ● 2 users          │  ● 4 users               │
│  ● 12 messages     │  ● 8 messages       │  ● 15 messages           │
│  ● status: ok      │  ● status: ok       │  ● status: ok            │
└────────────────────┴─────────────────────┴───────────────────────────┘
```

The bottom three columns provide an at-a-glance dashboard for each mail server, always visible regardless of what view is active in the main content area.

### Key Considerations
- Connects to the Go REST API for all operations (same API the webmail uses)
- May also connect directly to Postgres for admin-only queries not exposed via the API
- Runs on the host, not in a container -- requires the Docker stack to be running
- Built as a separate Go binary in the same module (`cmd/tui/`)

### Building & Running

Build for your current machine (auto-detects OS/arch):
```bash
task build:tui
# Binary output: build/tui/tui-<os>-<arch>
```

Or cross-compile for a specific platform:
```bash
task build:tui:darwin-arm64   # macOS Apple Silicon
task build:tui:darwin-amd64   # macOS Intel
task build:tui:linux-arm64    # Linux ARM64
task build:tui:all            # All platforms
```

Run (point at the API):
```bash
./build/tui/tui-darwin-arm64 -api http://localhost:8080 -user eve@mail3.test -pass password123
# Or use env vars:
export RESTMAIL_ADMIN_EMAIL=eve@mail3.test
export RESTMAIL_ADMIN_PASSWORD=password123
./build/tui/tui-darwin-arm64 -api http://localhost:8080
```

### TODO
- [x] Set up Bubble Tea project structure (cmd/tui/, internal/tui/)
- [x] Install Bubble Tea, Lip Gloss, Bubbles dependencies
- [x] Build main layout with bottom three-column server status bar
- [x] Build domain management view (list, add, remove)
- [x] Build user management view (list, create, delete, reset password)
- [x] Build inbox browser view (select user → list messages → read message)
- [x] Build compose view (select sender, enter recipient, subject, body, send)
- [x] Wire up API client to Go REST API
- [x] Cross-compile build tasks (Taskfile.yml: `build:tui`, per-platform, all)
- [x] Add real-time status polling for the bottom server status columns

---

## Phase 10: Test Suite

A comprehensive, API-driven test suite that validates the entire system. Tests are written as Go test files using the RESTMAIL API as both the data factory and the verification tool.

**Critical principle: validate the reference implementation first.** If the traditional Postfix/Dovecot servers aren't working correctly, we have no baseline to prove interop against. The test suite is structured as a dependency chain -- each stage must pass before the next can run.

### Test Suite Structure

```
tests/
  suite_test.go               - Test runner, suite setup/teardown
  stage1_infrastructure_test.go
  stage2_traditional_mail_test.go      ← MUST PASS before stage 3+
  stage3_gateway_inbound_test.go
  stage4_gateway_outbound_test.go
  stage5_indistinguishability_test.go  ← mail3 must look like a real server
  stage6_restmail_upgrade_test.go
  stage7_webmail_test.go
  stage8_tui_test.go
  stage9_consistency_test.go
```

### Stage 1: Infrastructure Health (prerequisite for everything)

Verify all containers are up and reachable. No mail operations yet.

```
Test: AllContainersHealthy
  GET /api/health → 200

Test: SmtpReachable_Mail1
  POST /api/test/smtp-probe/mail1.test → EHLO capabilities returned

Test: SmtpReachable_Mail2
  POST /api/test/smtp-probe/mail2.test → EHLO capabilities returned

Test: ImapReachable_Mail1
  POST /api/test/imap-probe/mail1.test → connection accepted

Test: ImapReachable_Mail2
  POST /api/test/imap-probe/mail2.test → connection accepted

Test: SmtpReachable_Mail3_Gateway
  POST /api/test/smtp-probe/mail3.test → EHLO capabilities include RESTMAIL

Test: DnsResolution
  All domains resolve to expected IPs via dnsmasq

Test: PostgresConnectivity
  GET /api/test/db/domains → 200 (database is accessible)
```

### Stage 2: Traditional Mail Validation (the reference implementation)

**This is the most important stage.** If these tests fail, we stop -- there is no point testing the rest-mail gateway against broken reference servers.

```
SETUP: Create test data via API
  POST /api/admin/domains     → mail1.test, mail2.test
  POST /api/admin/mailboxes   → alice@mail1.test, bob@mail2.test

Test: Mail1_PostfixAcceptsSmtp
  POST /api/test/smtp-probe/mail1.test → verify EHLO, MAIL FROM, RCPT TO accepted

Test: Mail2_PostfixAcceptsSmtp
  POST /api/test/smtp-probe/mail2.test → same

Test: Mail1_DovecotAuth
  POST /api/test/imap-probe/mail1.test { user: alice@mail1.test } → login succeeds

Test: Mail2_DovecotAuth
  POST /api/test/imap-probe/mail2.test { user: bob@mail2.test } → login succeeds

Test: Mail1_to_Mail2_Delivery
  POST /api/test/send { from: alice@mail1.test, to: bob@mail2.test }
  POST /api/test/verify-delivery { address: bob@mail2.test, timeout: 10s }
  GET  /api/test/mailbox/bob@mail2.test → message present, subject matches

Test: Mail2_to_Mail1_Delivery
  POST /api/test/send { from: bob@mail2.test, to: alice@mail1.test }
  POST /api/test/verify-delivery { address: alice@mail1.test, timeout: 10s }
  GET  /api/test/mailbox/alice@mail1.test → message present

Test: Mail1_ImapReadback
  POST /api/test/imap-probe/mail1.test { user: alice@mail1.test, read_inbox: true }
  → messages readable via IMAP match what API shows

Test: Mail2_ImapReadback
  POST /api/test/imap-probe/mail2.test { user: bob@mail2.test, read_inbox: true }
  → same

Test: Mail1_ImapFetchContent
  IMAP LOGIN alice@mail1.test, SELECT INBOX, FETCH <n> (BODY[])
  → verify the delivered message subject and body are readable through Dovecot IMAP
  → compare content with what API returns for the same message

Test: Mail2_Pop3Readback
  POP3 USER bob@mail2.test, PASS, STAT, RETR 1
  → verify a delivered message is readable through Dovecot POP3
  → message body contains expected content

Test: Mail1_SmtpSubmissionAuth
  Connect to mail1 port 587 (submission)
  EHLO → verify AUTH advertised
  STARTTLS → upgrade to TLS
  AUTH PLAIN (alice@mail1.test credentials)
  MAIL FROM / RCPT TO / DATA → send message via authenticated submission
  → verify delivery to bob@mail2.test via API readback
  → proves Dovecot SASL auth works for SMTP submission

Test: Mail1_Aliases
  POST /api/admin/aliases { source: info@mail1.test, destination: alice@mail1.test }
  POST /api/test/send { from: bob@mail2.test, to: info@mail1.test }
  POST /api/test/verify-delivery { address: alice@mail1.test } → arrives via alias

Test: ApiCreatedUser_VisibleToPostfix
  POST /api/admin/mailboxes { address: newuser@mail1.test, password: "test" }
  POST /api/test/send { from: bob@mail2.test, to: newuser@mail1.test }
  POST /api/test/verify-delivery { address: newuser@mail1.test }
  → proves Postfix picks up API-created data from Postgres without restart

Test: ApiCreatedUser_VisibleToDovecot
  POST /api/test/imap-probe/mail1.test { user: newuser@mail1.test, password: "test" }
  → proves Dovecot authenticates API-created users from Postgres

RESULT: Traditional mail infrastructure is fully validated.
        We now have a proven reference implementation to test against.
```

### Stage 3: Gateway SMTP Inbound (mail1 → mail3)

Traditional server delivers to rest-mail via SMTP. The gateway translates to REST API calls.

```
SETUP:
  POST /api/admin/mailboxes → testuser@mail3.test

Test: Mail1_to_Mail3_SmtpDelivery
  POST /api/test/send { from: alice@mail1.test, to: testuser@mail3.test }
  POST /api/test/verify-delivery { address: testuser@mail3.test }
  → message stored via gateway → REST API → Postgres

Test: Mail2_to_Mail3_SmtpDelivery
  POST /api/test/send { from: bob@mail2.test, to: testuser@mail3.test }
  POST /api/test/verify-delivery { address: testuser@mail3.test }

Test: Mail3_RejectsUnknownRecipient
  POST /api/test/send { from: alice@mail1.test, to: nobody@mail3.test }
  → gateway should reject at RCPT TO stage (550)

Test: Mail3_MessageIntegrity
  Send message with specific headers, body, attachments
  GET /api/messages/:id → verify all fields preserved through gateway translation

Test: Mail3_SmtpSubmissionAuth
  Connect to mail3 port 587 (gateway submission)
  EHLO → verify AUTH advertised
  AUTH PLAIN (testuser@mail3.test credentials) → 235 success
  MAIL FROM / RCPT TO / DATA → send message via authenticated submission
  → verify delivery via API readback
  → proves Go SMTP gateway AUTH PLAIN works on submission port

Test: Mail3_SmtpSubmissionRequiresAuth
  Connect to mail3 port 587
  EHLO, MAIL FROM (without AUTH) → 530 "Authentication required"
  → proves submission port enforces authentication

Test: Mail3_ImapFetchContent
  IMAP LOGIN testuser@mail3.test on mail3 IMAP gateway
  SELECT INBOX, FETCH <n> (BODY[])
  → verify delivered message body is fully readable through IMAP gateway
  → subject and body text match what was sent via SMTP

Test: Mail3_Pop3RetrMessage
  POP3 USER testuser@mail3.test, PASS on mail3 POP3 gateway
  STAT → verify message count > 0
  RETR 1 → retrieve full message
  → verify message content is present and RFC 2822 formatted
```

### Stage 4: Gateway SMTP Outbound (mail3 → mail1)

Rest-mail sends via the gateway, which acts as SMTP client to traditional servers.

```
Test: Mail3_to_Mail1_SmtpRelay
  POST /api/accounts/:id/messages { to: alice@mail1.test, subject: "from restmail" }
  POST /api/test/verify-delivery { address: alice@mail1.test }
  → gateway relayed via SMTP to mail1's Postfix

Test: Mail3_to_Mail2_SmtpRelay
  POST /api/accounts/:id/messages { to: bob@mail2.test }
  POST /api/test/verify-delivery { address: bob@mail2.test }

Test: Mail3_OutboundFallback
  Verify EHLO with mail1 does NOT show RESTMAIL capability
  Confirm delivery used standard SMTP (not upgrade path)
```

### Stage 5: Indistinguishability (mail3 looks like a real server)

**The stealth test.** mail3 must be indistinguishable from mail1/mail2 to a traditional server. We compare the SMTP/IMAP behaviour of mail3 against the proven reference servers and verify they match.

```
Test: EhloCapabilities_MatchTraditional
  Probe mail1 and mail3 EHLO capabilities
  → mail3 must advertise the same standard capabilities as mail1
    (PIPELINING, SIZE, 8BITMIME, etc.)
  → the extra RESTMAIL capability is fine (traditional servers ignore unknown extensions)
  → mail3 must NOT be missing any standard capabilities that mail1 advertises

Test: EhloResponse_Format
  → mail3 EHLO response format (greeting, spacing, line endings) matches RFC 5321
  → banner line matches expected format (220 mail3.test ESMTP ...)

Test: SmtpConversation_IdenticalBehaviour
  Run the exact same SMTP conversation against mail1 and mail3:
    EHLO, MAIL FROM, RCPT TO, DATA, QUIT
  → Response codes must be identical (250, 354, 250, 221)
  → Response timing must be similar (no suspiciously long pauses)
  → Error responses must match (unknown recipient → 550 on both)

Test: SmtpEdgeCases
  → RSET mid-conversation works the same as mail1
  → NOOP returns 250 OK
  → VRFY returns appropriate response
  → Pipelining works correctly
  → Oversized message rejected appropriately

Test: SmtpStarttls_Mail3
  Connect to mail3 port 25
  EHLO → verify STARTTLS advertised
  STARTTLS → TLS handshake succeeds (with InsecureSkipVerify)
  Re-EHLO over TLS → capabilities still present
  MAIL FROM / RCPT TO / DATA → send message over TLS
  → verify delivery via API readback

Test: SmtpSizeEnforcement_Mail3
  Connect to mail3 port 25
  EHLO → verify SIZE 10240000 advertised
  MAIL FROM, RCPT TO, DATA → send message exceeding 10MB
  → gateway returns 552 "Message exceeds maximum size"

Test: ImapStarttls_Mail3
  Connect to mail3 IMAP port 143
  → greeting includes STARTTLS in CAPABILITY
  STARTTLS → TLS handshake succeeds
  LOGIN over TLS → succeeds
  SELECT INBOX → works normally over TLS

Test: Pop3Stls_Mail3
  Connect to mail3 POP3 port 110
  CAPA → verify STLS advertised
  STLS → TLS handshake succeeds
  USER/PASS over TLS → succeeds
  STAT → works normally over TLS

Test: ImapBehaviour_MatchTraditional
  Compare IMAP session behaviour between mail1 and mail3:
  → LOGIN response format identical
  → LIST returns same folder structure
  → SELECT response fields match (EXISTS, RECENT, FLAGS, etc.)
  → FETCH response format is RFC-compliant
  → LOGOUT response matches

Test: ImapEdgeCases
  → NOOP works
  → CAPABILITY command returns standard capabilities
  → Invalid commands return proper error codes
  → Concurrent connections handled

Test: Pop3Behaviour_MatchTraditional
  Compare POP3 session behaviour between mail1 and mail3:
  → USER/PASS auth flow identical
  → STAT/LIST/RETR responses match RFC format
  → DELE/QUIT behaviour matches

Test: MessageHeaders_NoLeaks
  Send mail from mail3 to mail1
  Inspect received headers on mail1 side
  → No headers revealing rest-mail internals (no X-RestMail, no REST API URLs)
  → Received header looks like a normal MTA (not "Received: from rest-api")
  → Message-ID format is standard

Test: TraditionalServer_DoesNotReject
  → mail1's Postfix accepts mail from mail3 without warnings in logs
  → No "milter-reject" or "policy-reject" entries
  → No "non-compliant" warnings
```

### Stage 6: RESTMAIL Protocol Upgrade (mail3 → mail3)

Two RESTMAIL-aware endpoints discover each other and upgrade from SMTP to REST.

```
Test: Mail3_EhloAdvertisesRestmail
  POST /api/test/smtp-probe/mail3.test
  → 250 capabilities include "RESTMAIL https://mail3.test/restmail"

Test: Mail3_to_Mail3_UpgradePath
  POST /api/accounts/:id/messages { to: other@mail3.test }
  POST /api/test/verify-delivery { address: other@mail3.test }
  → verify delivery used RESTMAIL endpoint, not SMTP DATA

Test: RestmailEndpoint_DirectDelivery
  POST /restmail/messages { ... }
  → direct server-to-server REST delivery works

Test: TraditionalServer_IgnoresRestmailCap
  Probe mail3 from mail1's perspective
  → mail1 can deliver to mail3 without any issues
  → mail1 does not log any errors about unknown RESTMAIL extension
```

### Stage 7: Webmail Flows

```
Test: WebmailLogin
Test: WebmailListFolders
Test: WebmailReadMessage
Test: WebmailComposeAndSend
Test: WebmailReply
Test: WebmailAccountSwitching
Test: WebmailDeleteMessage
Test: WebmailMarkAsRead
```

### Stage 8: TUI Flows

```
Test: TuiDomainManagement
Test: TuiUserCreation
Test: TuiPasswordReset
Test: TuiInboxBrowsing
Test: TuiComposeMail
Test: TuiServerStatus
```

### Stage 9: Database Consistency

```
Test: MessageCountsMatchAcrossAPIs
  Compare /api/test/db/messages counts with /api/accounts/:id/folders/:f/messages counts

Test: NoOrphanedMailboxes
  All mailboxes belong to an existing domain

Test: NoOrphanedLinkedAccounts
  All linked_accounts reference existing mailboxes and webmail_accounts

Test: PostfixDovecotSeeApiData
  Create data via API, verify Postfix/Dovecot serve the same data via SMTP/IMAP
```

### TODO
- [x] Set up Go test framework and test runner (`tests/` directory)
- [x] Implement shared test helpers (API client, assertions, setup/teardown)
- [x] Write Stage 1 tests: infrastructure health
- [x] Write Stage 2 tests: traditional mail validation (reference implementation proof)
- [x] Write Stage 3 tests: gateway SMTP inbound
- [x] Write Stage 4 tests: gateway SMTP outbound
- [x] Write Stage 5 tests: indistinguishability (mail3 looks like a real server)
- [x] Write Stage 6 tests: RESTMAIL protocol upgrade
- [x] Write Stage 7 tests: webmail flows
- [x] Write Stage 8 tests: TUI flows
- [x] Write Stage 9 tests: database consistency
- [x] Set up test runner that enforces stage ordering (stage 2 must pass before 3+)
- [x] Enhance Stage 2: SMTP AUTH submission (port 587 + STARTTLS + AUTH PLAIN), IMAP full message FETCH content readback, POP3 RETR message readback
- [x] Enhance Stage 3: gateway SMTP AUTH submission, submission-requires-auth check, IMAP FETCH BODY[] readback, POP3 RETR readback
- [x] Enhance Stage 5: SMTP STARTTLS send, SIZE enforcement (10MB limit), IMAP STARTTLS, POP3 STLS
- [x] Document the full setup and usage in a README

---

## Phase 11: Mail Processing Pipeline (JSON Milters)

Traditional mail servers use Postfix milters (mail filters) -- opaque C/C++ programs that hook into the mail flow at specific points. They work, but they're hard to configure, impossible to test in isolation, and completely invisible to the user. You write a milter, compile it, wire it into `main.cf`, restart Postfix, send a test email, and hope.

We're replacing this with a **JSON-based pipeline system** -- a visual, testable, composable chain of processing steps. Every email that enters or leaves the system flows through a pipeline. Each step in the pipeline receives a JSON representation of the email, processes it, and outputs a modified JSON representation. Users can visually build these pipelines, test them with sample data, and see exactly what happens at each stage.

This is effectively **milters upgraded to JSON**. But better: users can create custom **input filters** and **output filters**, allowing businesses to build pipelines tailored to their needs.

### How It Works

Every email is represented as a JSON document as it flows through the pipeline. The JSON schema must handle real-world MIME complexity -- emails are deeply nested structures with multipart bodies, alternative representations (plain text + HTML), inline images, and nested RFC 822 attachments.

```json
{
  "envelope": {
    "mail_from": "alice@mail1.test",
    "rcpt_to": ["bob@mail3.test"],
    "client_ip": "172.20.0.10",
    "client_hostname": "mail1.test",
    "helo": "mail1.test",
    "tls": true,
    "direction": "inbound"
  },
  "headers": {
    "from": [{ "name": "Alice", "address": "alice@mail1.test" }],
    "to": [{ "name": "Bob", "address": "bob@mail3.test" }],
    "cc": [],
    "bcc": [],
    "subject": "Hello",
    "date": "Tue, 17 Feb 2026 10:30:00 +0000",
    "message-id": "<abc123@mail1.test>",
    "in-reply-to": null,
    "references": [],
    "dkim-signature": "v=1; a=rsa-sha256; d=mail1.test; ...",
    "raw": { /* all headers as-is, including duplicates and ordering */ }
  },
  "body": {
    "content_type": "multipart/alternative",
    "parts": [
      {
        "content_type": "text/plain",
        "content": "Hi Bob, please see the attached report."
      },
      {
        "content_type": "text/html",
        "content": "<html><body><p>Hi Bob, please see the attached report.</p></body></html>"
      }
    ]
  },
  "attachments": [
    {
      "filename": "report.pdf",
      "content_type": "application/pdf",
      "size": 5242880,
      "disposition": "attachment",
      "content": "JVBERi0xLjQK... (base64, before extraction)"
    }
  ],
  "inline": [
    {
      "content_id": "<logo@mail1.test>",
      "filename": "logo.png",
      "content_type": "image/png",
      "size": 24576,
      "disposition": "inline",
      "content": "iVBORw0KGgo... (base64, before extraction)"
    }
  ]
}
```

The JSON structure mirrors the MIME tree:
- **`body.parts`** supports recursive nesting (`multipart/mixed` containing `multipart/alternative` containing `text/plain` + `text/html`)
- **`headers.from/to/cc`** are arrays of `{name, address}` objects (not flat strings) -- handles multiple recipients, display names, and groups
- **`headers.bcc`** is populated in the envelope only (stripped from outbound headers as per RFC)
- **`headers.in-reply-to`** and **`headers.references`** support threading/conversation view
- **`headers.raw`** preserves the original header block for filters that need exact header inspection
- **`inline`** is separate from **`attachments`** -- inline images referenced by `Content-ID` in HTML bodies
- The `extract_attachments` transform filter processes both `attachments` and `inline`, replacing their `content` with storage references

The RFC 2822 → JSON parser (`internal/mime/`) must handle:
- Nested `multipart/*` structures (arbitrary depth)
- Character encoding detection and conversion (ISO-8859-1, Windows-1252, UTF-8, etc.)
- Base64 and quoted-printable content transfer encoding
- RFC 2047 encoded headers (=?UTF-8?B?...?=)
- Malformed emails (real-world mail is often non-compliant -- the parser must be tolerant)

A **pipeline** is an ordered list of **filters**. Each filter receives this JSON and does one of two things:

1. **Action filter** -- makes a decision about the email: accept, reject, quarantine, discard. It does not modify the email itself. Think of it as a gate.
2. **Transform filter** -- returns a modified version of the email JSON that **replaces** the input. The email is changed as it flows through. Think of it as a processor.

Both types can coexist in the same pipeline. The email flows through them in order.

```
Inbound Pipeline (email arriving at mail3.test):

  ┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐
  │ SPF      │───►│ DKIM     │───►│ Spam     │───►│ Recipient│───►│ Extract  │───►│ Custom   │──► Accept
  │ Check    │    │ Verify   │    │ Score    │    │ Check    │    │ Attach.  │    │ Filters  │   / Reject
  │ (action) │    │ (xform)  │    │ (action) │    │ (action) │    │ (xform)  │    │ (either) │
  └──────────┘    └──────────┘    └──────────┘    └──────────┘    └──────────┘    └──────────┘

Outbound Pipeline (email leaving mail3.test):

  ┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐    ┌──────────┐
  │ Sender   │───►│ Rate     │───►│ Header   │───►│ DKIM     │───►│ Custom   │──► Send
  │ Verify   │    │ Limit    │    │ Cleanup  │    │ Sign     │    │ Filters  │
  │ (action) │    │ (action) │    │ (xform)  │    │ (xform)  │    │ (either) │
  └──────────┘    └──────────┘    └──────────┘    └──────────┘    └──────────┘
```

### The Two Filter Types

Every filter in the pipeline is one of two types:

#### Action Filters (gates)

An action filter inspects the email and makes a **decision** -- it does not modify the email. It's a gate: the email either passes through unchanged or gets stopped.

```
Input JSON ──► [Action Filter] ──► same JSON (if accepted)
                     │
                     └──► reject / quarantine / discard (if not)
```

Action filter output:
```json
{
  "type": "action",
  "action": "continue",
  "log": { "filter": "spf_check", "result": "pass", "detail": "IP authorised for domain" }
}
```

Possible actions: `continue`, `reject`, `quarantine`, `discard`, `defer`

#### Transform Filters (processors)

A transform filter takes the email JSON and returns a **modified version** that replaces the input. The email changes shape as it flows through.

```
Input JSON ──► [Transform Filter] ──► modified JSON (replaces input for next filter)
```

Transform filter output:
```json
{
  "type": "transform",
  "action": "continue",
  "message": { /* modified email JSON -- this replaces the input */ },
  "log": { "filter": "extract_attachments", "result": "transformed", "detail": "3 attachments extracted to storage" }
}
```

A transform filter can also reject (e.g. a transform filter that tries to decode an attachment and finds it corrupt).

### The Attachment Extraction Pattern

This is a critical transform filter that demonstrates why the action/transform split matters. When an email arrives with large attachments, the `extract_attachments` filter:

1. Takes the email JSON (which includes inline attachment data -- potentially megabytes of base64)
2. Extracts each attachment to **filesystem storage** (or an object store if configured)
3. Replaces the inline attachment data with **file references**
4. Returns a much smaller email JSON

```
Before extract_attachments:
{
  "body": { "content": "Please see attached report." },
  "attachments": [
    {
      "filename": "report.pdf",
      "content_type": "application/pdf",
      "size": 5242880,
      "content": "JVBERi0xLjQKJcfs... (5MB of base64)"
    },
    {
      "filename": "photo.jpg",
      "content_type": "image/jpeg",
      "size": 2097152,
      "content": "/9j/4AAQSkZJRg... (2MB of base64)"
    }
  ]
}

After extract_attachments:
{
  "body": { "content": "Please see attached report." },
  "attachments": [
    {
      "filename": "report.pdf",
      "content_type": "application/pdf",
      "size": 5242880,
      "storage": "filesystem",
      "ref": "/data/attachments/2026/02/17/a3f8c9d1-report.pdf"
    },
    {
      "filename": "photo.jpg",
      "content_type": "image/jpeg",
      "size": 2097152,
      "storage": "filesystem",
      "ref": "/data/attachments/2026/02/17/b7e2a4f0-photo.jpg"
    }
  ]
}
```

The result: **100% of the email text body is stored in PostgreSQL** (enabling full text search, indexing, and fast queries), while the heavy binary attachments live on the filesystem or object store. The JSON in the database is small and fast to query. When a client needs an attachment, the API streams it from the file reference.

If an object store is enabled (e.g. S3-compatible via MinIO in Docker), the filter can write there instead:

```json
{
  "storage": "s3",
  "ref": "s3://restmail-attachments/2026/02/17/a3f8c9d1-report.pdf"
}
```

This is configurable per-domain in the pipeline configuration.

#### Attachment Deduplication (Checksum-Based)

The `extract_attachments` filter computes a **SHA-256 checksum** of each attachment before writing it to storage. Before writing a new file, it checks the `attachments` table for an existing entry with the same checksum. If a match is found, the filter reuses the existing `storage_ref` instead of writing a duplicate copy.

```
Attachment arrives (5MB PDF, SHA-256: abc123...)
    │
    ├── Query: SELECT storage_ref FROM attachments WHERE checksum = 'abc123...'
    │
    ├── EXISTS → reuse storage_ref, skip file write
    │              (save 5MB disk, new attachments row points to same file)
    │
    └── NOT EXISTS → write to storage, record new storage_ref + checksum
```

This is particularly effective for:
- **Forwarded chains** — the same attachment forwarded through multiple people is stored once
- **Mailing lists** — identical attachments sent to many recipients share one copy
- **Reply-all threads** — the same inline images appearing in every reply

The `attachments` table already has a `checksum` column (VARCHAR(64) for SHA-256 hex). An index on `checksum` makes dedup lookups fast:

```sql
CREATE INDEX idx_attachments_checksum ON attachments(checksum);
```

**Deletion safety:** When a message is deleted, its attachment rows are removed. But the underlying file is only deleted when **no other attachment rows reference the same `storage_ref`**. A reference-count check (`SELECT COUNT(*) FROM attachments WHERE storage_ref = ?`) prevents premature deletion. Alternatively, a periodic garbage collection job scans for unreferenced files.

### Filter Types

There are four categories of filters: **built-in** (our own Go implementations), **adapter** (wrappers around existing well-known software), **scriptable** (Sieve and JavaScript programs), and **custom** (user-defined JSON configurations).

#### Built-in Filters (our Go implementations)

| Filter                 | Type      | Direction | What it does                                                        |
|------------------------|-----------|-----------|---------------------------------------------------------------------|
| `spf_check`            | Action    | Inbound   | Verify sender IP against SPF DNS records                            |
| `dkim_verify`          | Transform | Inbound   | Verify DKIM signature. Add `Authentication-Results` header          |
| `dkim_sign`            | Transform | Outbound  | Sign outgoing mail with domain's DKIM private key                   |
| `dmarc_check`          | Action    | Inbound   | Evaluate DMARC policy using SPF + DKIM results. Enforce reject/quarantine/none. Add `Authentication-Results` |
| `header_validate`      | Action    | Both      | Check required headers present, format valid, no injection attempts |
| `sender_verify`        | Action    | Outbound  | Verify sender is authorised to send from this address               |
| `recipient_check`      | Action    | Inbound   | Verify recipient exists, check aliases, check quota                 |
| `rate_limit`           | Action    | Outbound  | Enforce sending rate limits per user/domain                         |
| `header_cleanup`       | Transform | Outbound  | Remove internal headers, add standard outbound headers              |
| `size_check`           | Action    | Both      | Enforce maximum message size                                        |
| `arc_verify`           | Transform | Inbound   | Verify ARC chain (RFC 8617); trust forwarded auth results if chain valid |
| `arc_seal`             | Transform | Outbound  | Add ARC seal when relaying (AAR + AMS + AS headers, using DKIM key) |
| `extract_attachments`  | Transform | Inbound   | Extract attachments to disk/object store, replace with file refs    |
| `webhook`              | Action    | Both      | POST email JSON to an external URL, act on response                 |
| `contact_whitelist`    | Action    | Inbound   | Check sender against recipient's contacts; skip spam scoring if known |
| `domain_allowlist`     | Action    | Inbound   | Check sender against domain-wide allow/block lists                  |
| `greylist`             | Action    | Inbound   | Temporarily reject first delivery from unknown senders              |

The `dmarc_check` filter **must** run after `spf_check` and `dkim_verify` in the pipeline because DMARC evaluates their results. It reads the domain's DMARC policy from DNS (`_dmarc.domain` TXT record), checks alignment of the From header domain with SPF/DKIM domains, and enforces the policy (`p=reject`, `p=quarantine`, or `p=none`). It also generates DMARC aggregate reports if configured.

The `webhook` filter is particularly powerful: it POSTs the email JSON to a user-configured URL and acts on the response. This enables external integrations -- a Slack notification when invoices arrive, a CRM lookup when a customer emails, or triggering business workflows based on email patterns. The webhook endpoint receives the full email JSON and returns an action (`continue`, `reject`, etc.) and optional tags/metadata.

**ARC (Authenticated Received Chain — RFC 8617)** solves the problem of forwarded mail. When emails pass through mailing lists or forwarding services, SPF and DKIM often break because the forwarder isn't in the original sender's SPF record and may modify the message (breaking DKIM). ARC preserves authentication results across hops by adding three headers per hop:

- **ARC-Authentication-Results (AAR)** — snapshot of authentication results at this hop
- **ARC-Message-Signature (AMS)** — DKIM-like signature over the message body + headers
- **ARC-Seal (AS)** — signature over the chain of all previous ARC headers (seals the chain)

The `arc_verify` inbound filter parses the ARC chain, verifies each seal and message signature (using the same DKIM crypto infrastructure), and reports whether the chain is valid. When SPF/DKIM fail but the ARC chain is valid, the `dmarc_check` filter can use the ARC results as a trusted signal instead of rejecting — this is how Gmail and Microsoft handle forwarded mail. The `arc_seal` outbound filter adds a new ARC set when relaying mail, using the domain's DKIM private key (same key, same signing code).

#### Adapter Filters (wrapping existing software)

We are **not** reimplementing rspamd, ClamAV, or any other battle-tested mail processing software. These are complex, mature projects with years of development. Instead, we wrap them as **adapter filters** -- thin Go wrappers that convert the email JSON into whatever format the external tool expects, call it, and translate the response back into our standard filter output.

Each adapter runs the real software in a sidecar Docker container and communicates via the tool's native protocol:

| Filter                 | Type      | Direction | Wraps                | Protocol                          |
|------------------------|-----------|-----------|----------------------|-----------------------------------|
| `rspamd`               | Action    | Inbound   | rspamd               | HTTP API (rspamd worker)          |
| `clamav`               | Action    | Both      | ClamAV (clamd)       | TCP socket (clamd protocol)       |
| `spamassassin`         | Action    | Inbound   | SpamAssassin (spamd) | TCP socket (spamd protocol)       |
| `dcc`                  | Action    | Inbound   | DCC (Distributed Checksum Clearinghouse) | dccifd socket    |

```
How an adapter filter works:

  Pipeline Engine                    Adapter Filter (Go)              External Service (Docker)
      │                                   │                                │
      │──email JSON──────────────────────►│                                │
      │                                   │──convert to native format─────►│
      │                                   │  (e.g. rspamd HTTP POST       │
      │                                   │   or clamd INSTREAM)          │
      │                                   │◄─native response──────────────│
      │                                   │  (e.g. rspamd score/action    │
      │                                   │   or clamd FOUND/OK)          │
      │◄─standard FilterResult───────────│                                │
      │  { type: "action",                │                                │
      │    action: "continue"/"reject",   │                                │
      │    log: { result: "clean" } }     │                                │
```

The adapter hides all the complexity of the external tool's protocol. To the pipeline engine, it looks exactly like any other filter -- JSON in, FilterResult out. Users can drag rspamd or ClamAV into their pipeline just like any built-in filter.

**Docker sidecar containers** for adapter services are optional -- they're only started if the adapter is used in at least one pipeline:

| Container              | Image                 | Purpose                              | Optional |
|------------------------|-----------------------|--------------------------------------|----------|
| `rspamd`               | `rspamd/rspamd`       | Spam scoring, DKIM, ARC, DMARC      | Yes      |
| `clamav`               | `clamav/clamav`       | Virus/malware scanning               | Yes      |

These would be added to `docker-compose.yml` with profiles so they only start when needed:

```yaml
rspamd:
  image: rspamd/rspamd
  profiles: ["filter"]
  # only starts with: docker compose --profile filter up

clamav:
  image: clamav/clamav
  profiles: ["filter"]
  # only starts with: docker compose --profile filter up
```

**Note:** rspamd itself can handle DKIM verification, DMARC, ARC, and spam scoring. If you use the rspamd adapter, you may not need the built-in `dkim_verify` or `spf_check` filters -- rspamd does all of that. The built-in filters exist for lightweight setups that don't want to run rspamd. Users choose: use our lightweight built-ins, or plug in rspamd for the full-featured experience. Or both -- the pipeline is composable.

#### Scriptable Filters (Sieve and JavaScript)

Beyond JSON-configured custom filters, users can write full programs to implement complex filtering logic. We support two scripting languages:

**Sieve (RFC 5228)** — the standard mail filtering language. Sieve is purpose-built for server-side mail filtering and is widely understood by mail administrators. Our Sieve filter runs a Sieve interpreter over the email JSON, supporting the standard Sieve commands (`keep`, `fileinto`, `redirect`, `discard`, `reject`) plus common extensions (`vacation`, `notify`, `body`, `regex`).

```sieve
# Route invoices to a folder, auto-reply to no-reply addresses, reject large emails
require ["fileinto", "reject", "body", "vacation"];

if header :contains "Subject" ["invoice", "INV-", "receipt"] {
    fileinto "Invoices";
}

if address :is "from" "noreply@example.com" {
    discard;
    stop;
}

if size :over 25M {
    reject "Message too large -- 25MB limit";
    stop;
}

# Auto-respond when on vacation
vacation :days 7 :subject "Out of office"
    "I'm currently out of the office. I'll respond when I return.";
```

Sieve scripts are stored per-mailbox and execute as a transform filter in the inbound pipeline. This gives users familiar, standard filtering without writing code.

| Filter     | Type      | Direction | What it does                                                   |
|------------|-----------|-----------|----------------------------------------------------------------|
| `sieve`    | Transform | Inbound   | Run user's Sieve script (fileinto, redirect, reject, discard)  |

##### Vacation / Auto-Reply (RFC 3834 compliance)

The Sieve `vacation` extension is the primary mechanism for auto-replies and out-of-office messages. However, auto-replies are dangerous if not handled carefully — they can cause **reply storms** (auto-reply replying to auto-reply infinitely) or generate backscatter spam.

Our implementation enforces **RFC 3834** rules at the system level, regardless of what the user's Sieve script says:

**Never auto-reply when:**
1. The incoming message has `Auto-Submitted: auto-replied` or `Auto-Submitted: auto-generated` header (RFC 3834 §3.1.3)
2. The `Precedence` header is `bulk`, `list`, or `junk` (mailing list or bulk mail)
3. The sender is `MAILER-DAEMON`, empty (`<>`), or contains `noreply` / `no-reply`
4. The message has a `List-Id`, `List-Unsubscribe`, or `X-Mailing-List` header (mailing list)
5. The sender was already auto-replied to within the configured time window (Sieve `vacation :days N`)

**Auto-reply tracking:**
```sql
CREATE TABLE vacation_responses (
    id            SERIAL PRIMARY KEY,
    mailbox_id    INTEGER REFERENCES mailboxes(id),
    sender        VARCHAR(255) NOT NULL,
    responded_at  TIMESTAMP DEFAULT NOW(),
    UNIQUE(mailbox_id, sender)
);
```

When a vacation response is sent, the (mailbox, sender) pair is recorded. Subsequent messages from the same sender within the `:days` window are silently ignored (no duplicate replies). The table is periodically cleaned of expired entries.

**Auto-reply messages always include:**
- `Auto-Submitted: auto-replied` header (so receiving servers know not to auto-reply back)
- `In-Reply-To` referencing the original message (threads correctly)
- `References` header (for thread view)

**Beyond Sieve:** The webmail UI also provides a simple vacation toggle (without requiring users to write Sieve):

```
PUT /api/v1/accounts/:id/vacation
{
  "enabled": true,
  "subject": "Out of office",
  "body": "I'm away until March 1st. For urgent matters, contact support@company.com.",
  "start_date": "2026-02-20T00:00:00Z",    // optional: auto-enable/disable
  "end_date": "2026-03-01T00:00:00Z"
}

GET /api/v1/accounts/:id/vacation           - Get vacation status
DELETE /api/v1/accounts/:id/vacation        - Disable vacation
```

Under the hood, this generates a Sieve `vacation` rule that's prepended to the user's Sieve script. If the user has no Sieve script, a minimal one is created. If they have one, the vacation block is injected before the user's rules (and removed when vacation is disabled).

**JavaScript filters** — for users who need full programmatic control. A sandboxed JavaScript runtime (using [goja](https://github.com/nicholasgasior/goja) — a pure-Go ES5.1+ interpreter) executes user-provided scripts against the email JSON. This enables arbitrarily complex logic: regex matching, external API calls, conditional transforms, string manipulation, date parsing, etc.

```javascript
// Custom JS filter: detect and tag emails from known CRM systems
function filter(email) {
    const crmDomains = ["salesforce.com", "hubspot.com", "pipedrive.com"];
    const fromDomain = email.from[0].address.split("@")[1];

    if (crmDomains.includes(fromDomain)) {
        email.headers["X-Source"] = "crm";
        email.deliver_to_folder = "CRM";
        return { type: "transform", action: "continue", message: email };
    }

    // Extract reference numbers from subject
    const refMatch = email.headers.subject.match(/REF-(\d+)/);
    if (refMatch) {
        email.headers["X-Reference"] = refMatch[1];
        return { type: "transform", action: "continue", message: email };
    }

    return { type: "action", action: "continue" };
}
```

```javascript
// Custom JS filter: reject emails during maintenance windows
function filter(email) {
    const hour = new Date().getHours();
    if (hour >= 2 && hour < 4) {
        return {
            type: "action",
            action: "defer",
            log: { detail: "Maintenance window: 02:00-04:00 UTC" }
        };
    }
    return { type: "action", action: "continue" };
}
```

| Filter       | Type           | Direction | What it does                                             |
|--------------|----------------|-----------|----------------------------------------------------------|
| `javascript` | Action/Transform | Both    | Run sandboxed JS script with full programmatic control   |

**Security for JS filters:**
- Sandboxed execution via goja (no filesystem, network, or OS access by default)
- Execution timeout (configurable, default 500ms per email)
- Memory limit per execution
- No `require`/`import` — self-contained scripts only
- Optional: allowlisted HTTP fetch capability for webhook-like integrations (admin-enabled per filter)
- Script validation on save (syntax check + dry-run against sample email)

The pipeline supports mixing all filter types freely. A single pipeline could run: built-in `spf_check` → adapter `rspamd` → Sieve script → custom JSON filter → JavaScript filter → built-in `extract_attachments`. Each filter type uses the same JSON in / FilterResult out contract.

#### Custom Filters (user-created JSON input/output filters)

Users can create their own action or transform filters that plug into the pipeline. A custom filter is defined as a JSON configuration:

**Action filter example** (gate: block dangerous attachments):
```json
{
  "name": "block_executables",
  "description": "Reject emails with executable attachments",
  "type": "action",
  "direction": "inbound",
  "condition": {
    "match": "attachments[*].filename",
    "pattern": "*.{exe,bat,cmd,scr,pif}"
  },
  "action": "reject",
  "reject_message": "550 Executable attachments are not permitted"
}
```

**Transform filter example** (processor: add disclaimer to outbound):
```json
{
  "name": "marketing_disclaimer",
  "description": "Add disclaimer to outbound marketing emails",
  "type": "transform",
  "direction": "outbound",
  "condition": {
    "match": "envelope.mail_from",
    "pattern": "*@marketing.mail3.test"
  },
  "transform": {
    "append_body": "\n\n---\nThis email was sent by the marketing department."
  }
}
```

**Transform filter example** (processor: auto-route to folder):
```json
{
  "name": "auto_tag_invoices",
  "description": "Route invoice emails to a dedicated folder",
  "type": "transform",
  "direction": "inbound",
  "condition": {
    "match": "headers.subject",
    "pattern": "*invoice*|*INV-*|*receipt*"
  },
  "transform": {
    "add_header": { "X-Category": "billing" },
    "deliver_to_folder": "Invoices"
  }
}
```

**Webhook filter example** (action: notify Slack when support emails arrive):
```json
{
  "name": "support_slack_notify",
  "description": "Post to Slack when support@ receives mail",
  "type": "action",
  "direction": "inbound",
  "condition": {
    "match": "envelope.rcpt_to[*]",
    "pattern": "support@*"
  },
  "action": "continue",
  "webhook": {
    "url": "https://hooks.slack.com/services/T.../B.../xxx",
    "method": "POST",
    "payload_template": {
      "text": "New support email from {{envelope.mail_from}}: {{headers.subject}}"
    }
  }
}
```

### Trust-Based Filtering (Spamina-Inspired)

Inspired by Spamina's anti-spam appliance technology (a Spanish company, circa 2005-2013, later acquired by Secucloud), we implement a **trust-first filtering approach** that inverts the traditional spam problem. Instead of asking "is this spam?" for every message, we ask "is this sender known?" first. Known senders get a fast path that skips expensive content analysis; unknown senders get the full filtering gauntlet.

Spamina's server blade sat in front of mail servers as a transparent SMTP proxy, filtering inbound email before relay. Their key innovation was **contact-based whitelisting**: users maintained a contacts list (auto-populated from sent mail), and emails from known contacts bypassed spam scoring entirely. This dramatically reduced false positives (your colleague's email never lands in spam) and computational load (no content analysis for 80% of legitimate mail from known senders).

We implement this concept as pipeline filters:

#### `contact_whitelist` Action Filter (built-in)

Checks whether the sender is in the recipient's contacts list (which is already auto-populated from sent mail — see the Contacts API endpoints). If the sender is known, downstream spam scoring is skipped.

```
Pipeline flow:
  [spf_check] → [dkim_verify] → [contact_whitelist] → [rspamd] → ...
                                       │
                                       ├── sender known → skip rspamd, continue
                                       └── sender unknown → continue to rspamd
```

The filter uses the pipeline skip mechanism:

```json
{
  "type": "action",
  "action": "continue",
  "skip_filters": ["rspamd"],
  "log": {
    "filter": "contact_whitelist",
    "result": "pass",
    "detail": "Sender alice@partner.com is in recipient's contact list (auto-collected)"
  }
}
```

When the pipeline engine sees `skip_filters` in an action result, it skips those named filters. This is both a performance optimisation and a false-positive reducer.

#### Contact Trust Levels

Extend the contacts system with explicit trust levels:

| Trust Level | Source                          | Effect on filtering                                    |
|-------------|----------------------------------|--------------------------------------------------------|
| `auto`      | Auto-collected from sent mail   | Skip spam scoring (but not virus/malware checks)       |
| `trusted`   | Explicitly whitelisted by user  | Skip spam scoring + greylisting                        |
| `blocked`   | Explicitly blocked by user      | Reject immediately (personal blocklist)                |

```sql
CREATE TABLE contacts (
    id          SERIAL PRIMARY KEY,
    mailbox_id  INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    email       VARCHAR(255) NOT NULL,
    name        VARCHAR(255),
    trust_level VARCHAR(20) DEFAULT 'auto',  -- 'auto', 'trusted', 'blocked'
    source      VARCHAR(20) DEFAULT 'sent',  -- 'sent' (auto-collected), 'manual', 'import'
    created_at  TIMESTAMP DEFAULT NOW(),
    updated_at  TIMESTAMP DEFAULT NOW(),
    UNIQUE(mailbox_id, email)
);

CREATE INDEX idx_contacts_mailbox_email ON contacts(mailbox_id, email);
CREATE INDEX idx_contacts_trust_level ON contacts(mailbox_id, trust_level);
```

The `contact_whitelist` filter looks up the sender's email in this table for the recipient's mailbox. The `source` column tracks how the contact was added — `sent` means auto-collected from outgoing mail.

Additional contacts API endpoints:

```
POST   /api/v1/accounts/:id/contacts           - Add contact manually (explicit whitelist)
PUT    /api/v1/accounts/:id/contacts/:cid       - Update contact trust level
POST   /api/v1/accounts/:id/contacts/import     - Bulk import from vCard, CSV
POST   /api/v1/accounts/:id/contacts/block      - Block a sender
```

#### `domain_allowlist` Action Filter (built-in)

Admin-level whitelist/blocklist applied across all mailboxes in a domain:

```json
{
  "name": "domain_allowlist",
  "type": "action",
  "direction": "inbound",
  "config": {
    "on_allow": "continue_skip_spam",
    "on_block": "reject",
    "reject_message": "550 Sender blocked by domain policy"
  }
}
```

```sql
CREATE TABLE domain_sender_rules (
    id          SERIAL PRIMARY KEY,
    domain_id   INTEGER NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    pattern     VARCHAR(255) NOT NULL,   -- email address or domain (e.g. "spam@evil.com" or "@evil.com")
    list_type   VARCHAR(10) NOT NULL,    -- 'allow' or 'block'
    reason      TEXT,                    -- admin note for why this entry exists
    created_by  INTEGER REFERENCES mailboxes(id),
    created_at  TIMESTAMP DEFAULT NOW(),
    UNIQUE(domain_id, pattern, list_type)
);

CREATE INDEX idx_domain_sender_rules_lookup ON domain_sender_rules(domain_id, list_type, pattern);
```

The `pattern` column supports both full email addresses (`spam@evil.com`) and domain-wide patterns (`@evil.com`). The filter checks `block` rules first, then `allow` rules.

Managed via admin API:

```
GET    /api/v1/admin/domains/:id/allowlist       - List allowed senders/domains
POST   /api/v1/admin/domains/:id/allowlist       - Add to allowlist
DELETE /api/v1/admin/domains/:id/allowlist/:eid   - Remove from allowlist
GET    /api/v1/admin/domains/:id/blocklist       - List blocked senders/domains
POST   /api/v1/admin/domains/:id/blocklist       - Add to blocklist
DELETE /api/v1/admin/domains/:id/blocklist/:eid   - Remove from blocklist
```

#### `greylist` Action Filter (built-in)

Greylisting temporarily rejects the first delivery attempt from unknown senders. Legitimate mail servers retry after a delay; spam bots typically don't.

```json
{
  "name": "greylist",
  "type": "action",
  "direction": "inbound",
  "config": {
    "delay_minutes": 5,
    "whitelist_after_pass": true,
    "skip_if_trusted_sender": true
  }
}
```

The filter:
1. Checks if the (sender, recipient, IP) triple has been seen before
2. If new: returns `defer` action → gateway issues SMTP 451 tempfail
3. If seen before and delay elapsed: allows through
4. If sender is in contacts whitelist: skips greylisting entirely

Requires a `greylist_entries` table:

```sql
CREATE TABLE greylist_entries (
    id SERIAL PRIMARY KEY,
    sender VARCHAR(255) NOT NULL,
    recipient VARCHAR(255) NOT NULL,
    source_ip INET NOT NULL,
    first_seen TIMESTAMP DEFAULT NOW(),
    retry_after TIMESTAMP NOT NULL,
    passed BOOLEAN DEFAULT false,
    created_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(sender, recipient, source_ip)
);
```

#### Quarantine & Digest System

Rather than silently rejecting borderline spam, quarantine it and let users review:

```sql
CREATE TABLE quarantine (
    id              SERIAL PRIMARY KEY,
    mailbox_id      INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    sender          VARCHAR(255) NOT NULL,
    subject         TEXT,
    body_preview    TEXT,                    -- first ~200 chars of body for digest
    raw_message     TEXT NOT NULL,           -- full RFC 2822 message (needed for release)
    spam_score      REAL,                    -- score from rspamd adapter filter (if enabled)
    quarantine_reason VARCHAR(50) NOT NULL,  -- 'spam', 'dmarc_fail', 'greylist_timeout', etc.
    received_at     TIMESTAMP DEFAULT NOW(),
    expires_at      TIMESTAMP NOT NULL,      -- auto-delete after retention period (default 30 days)
    released        BOOLEAN DEFAULT false
);

CREATE INDEX idx_quarantine_mailbox ON quarantine(mailbox_id, released, received_at DESC);
CREATE INDEX idx_quarantine_expiry ON quarantine(expires_at) WHERE released = false;
```

Quarantined messages are stored in the database (not the filesystem) so they don't count toward mailbox quota and can be managed atomically. The `raw_message` column holds the complete RFC 2822 message so it can be re-injected into the pipeline on release. A background job purges expired entries based on `expires_at`.

```
GET    /api/v1/accounts/:id/quarantine                - List quarantined messages
POST   /api/v1/accounts/:id/quarantine/:mid/release   - Release to inbox
POST   /api/v1/accounts/:id/quarantine/:mid/whitelist  - Release + add sender to contacts
DELETE /api/v1/accounts/:id/quarantine/:mid            - Permanently delete
GET    /api/v1/accounts/:id/quarantine/digest          - Generate digest summary (JSON)
```

The webmail UI provides a quarantine review panel where users can scan held messages, release legitimate ones, and whitelist senders with a single click. An optional **daily digest email** summarises what's in quarantine (configurable per-user: daily, weekly, or off).

#### Updated Built-in Filters Table (with trust-based additions)

| Filter                 | Type      | Direction | What it does                                                        |
|------------------------|-----------|-----------|---------------------------------------------------------------------|
| `contact_whitelist`    | Action    | Inbound   | Check sender against recipient's contacts; skip spam scoring if known |
| `domain_allowlist`     | Action    | Inbound   | Check sender against domain-wide allow/block lists                  |
| `greylist`             | Action    | Inbound   | Temporarily reject first delivery from unknown senders              |

These filters sit in the pipeline **after** authentication (SPF/DKIM/DMARC) and **before** content-based spam scoring:

```
Recommended inbound pipeline order:
  1. size_check          (reject oversized messages immediately)
  2. spf_check           (verify sender IP)
  3. dkim_verify         (verify DKIM signature)
  4. arc_verify          (verify ARC chain — used when SPF/DKIM fail on forwarded mail)
  5. dmarc_check         (evaluate DMARC policy, considering ARC results)
  6. domain_allowlist    (admin allow/block lists)
  7. contact_whitelist   (personal contacts whitelist — may skip 9-10)
  8. greylist            (delay unknown senders — may skip if whitelisted)
  9. rspamd             (content-based spam scoring — skipped for known senders, requires rspamd sidecar)
  10. clamav             (virus scanning — never skipped)
  11. header_validate    (header sanity checks)
  12. recipient_check    (verify recipient exists)
  13. extract_attachments (move attachments to storage)
  14. sieve              (user's personal Sieve rules)
```

### Pipeline Skip Logic

The pipeline engine supports a `skip_filters` mechanism in action filter results. When a filter returns `skip_filters: ["filter_name_1", "filter_name_2"]`, those filters are bypassed for the remainder of the pipeline execution. This enables the trust-based fast path:

- `contact_whitelist` → skip `rspamd` (known sender, no need to score)
- `domain_allowlist` → skip `rspamd`, `greylist` (admin-approved sender)

**Important:** Some filters should **never** be skipped regardless of trust level. Virus scanning (`clamav`) and authentication checks (`spf_check`, `dkim_verify`) always run. The pipeline configuration can mark filters as `unskippable: true`.

### Pipeline Configuration

Pipelines are stored in the database and managed via the RESTMAIL API. Each domain can have its own inbound and outbound pipelines.

```json
{
  "domain": "mail3.test",
  "direction": "inbound",
  "filters": [
    { "name": "size_check",          "enabled": true,  "unskippable": true, "config": { "max_size_mb": 25 } },
    { "name": "spf_check",           "enabled": true,  "unskippable": true, "config": { "fail_action": "tag" } },
    { "name": "dkim_verify",         "enabled": true,  "unskippable": true, "config": { "fail_action": "tag" } },
    { "name": "arc_verify",          "enabled": true,  "unskippable": true, "config": {} },
    { "name": "dmarc_check",         "enabled": true,  "unskippable": true, "config": { "fail_action": "quarantine" } },
    { "name": "domain_allowlist",    "enabled": true,  "config": {} },
    { "name": "contact_whitelist",   "enabled": true,  "config": {} },
    { "name": "greylist",            "enabled": true,  "config": { "delay_minutes": 5, "ttl_days": 36 } },
    { "name": "rspamd",              "enabled": true,  "config": { "threshold": 5.0 } },
    { "name": "clamav",              "enabled": true,  "unskippable": true, "config": {} },
    { "name": "header_validate",     "enabled": true,  "config": {} },
    { "name": "recipient_check",     "enabled": true,  "unskippable": true, "config": {} },
    { "name": "extract_attachments", "enabled": true,  "config": {} },
    { "name": "sieve",               "enabled": true,  "config": {} }
  ]
}
```

This matches the recommended pipeline order above. Filters marked `unskippable: true` always run even when a trust-based filter returns `skip_filters`. Filters can be reordered, enabled/disabled, and configured per-domain. The pipeline is evaluated top-to-bottom. If any action filter rejects the message, processing stops and the rejection is returned.

### Filter Output Contract

Every filter returns a standardised result. The shape depends on whether it's an action or transform filter:

**Action filter** (gate -- pass or block, email unchanged):
```json
{
  "type": "action",
  "action": "continue",
  "log": {
    "filter": "spf_check",
    "duration_ms": 12,
    "result": "pass",
    "detail": "SPF pass for mail1.test: IP 172.20.0.10 is authorised"
  }
}
```

**Transform filter** (processor -- email is replaced with the returned version):
```json
{
  "type": "transform",
  "action": "continue",
  "message": { /* the modified email JSON -- this replaces the input for the next filter */ },
  "log": {
    "filter": "extract_attachments",
    "duration_ms": 340,
    "result": "transformed",
    "detail": "3 attachments (7.2MB) extracted to filesystem, replaced with refs"
  }
}
```

Possible actions (both types):
- `continue` -- pass to next filter (action: unchanged, transform: with modified email)
- `reject` -- reject the message (return SMTP 5xx to sender)
- `quarantine` -- accept but store in quarantine instead of inbox
- `discard` -- silently drop (accept from sender's perspective but don't deliver)
- `defer` -- temporary failure (return SMTP 4xx, sender will retry)
- `duplicate` -- continue processing AND fork a copy to another destination (webhook, queue, etc.)

### Bidirectional Power: Inbound AND Outbound Filters

Filters work on both inbound and outbound pipelines. This is important because outbound filtering is just as valuable as inbound:

**Inbound examples** (email arriving):
- SPF/DKIM verification, spam scoring, virus scanning
- Extract attachments to storage
- Auto-route invoices to a folder
- Notify Slack when support@ receives mail
- Forward to an AI agent for classification

**Outbound examples** (email leaving):
- DKIM signing
- Sender verification (prevent spoofing)
- Rate limiting
- Add legal disclaimers or company footers
- Log all outbound mail for compliance
- Duplicate to an archive/audit system
- Trigger CRM updates when sales team sends to customers
- Route through an AI agent to auto-generate summaries for the CRM

### External Integration: Webhooks, n8n, and AI Workflows

The `webhook` filter type and the `duplicate` action unlock powerful external integrations. Because every email is a JSON document flowing through a pipeline, it's trivial to:

1. **POST the email JSON to an n8n workflow** that runs AI classification, entity extraction, or sentiment analysis
2. **Duplicate inbound emails to an AI agent cluster** that processes them asynchronously -- extracting data, triggering business logic, updating external systems
3. **Route outbound emails through a compliance check** via an external webhook before allowing them to leave
4. **Feed emails into a search/analytics pipeline** (Elasticsearch, etc.) by duplicating them on both inbound and outbound

```
Example: n8n AI workflow integration (inbound)

  Inbound email ──► [SPF] ──► [DKIM] ──► [rspamd] ──► [webhook: n8n] ──► [accept]
                                                                │
                                                    POST email JSON to n8n
                                                                │
                                                    ┌───────────▼──────────┐
                                                    │  n8n workflow:        │
                                                    │  1. AI classify email │
                                                    │  2. Extract entities  │
                                                    │  3. Update CRM       │
                                                    │  4. Trigger Slack     │
                                                    └──────────────────────┘

Example: Outbound compliance + CRM (outbound)

  Compose ──► [sender_verify] ──► [webhook: compliance] ──► [dkim_sign] ──► [duplicate: CRM] ──► Send
                                         │                                        │
                                   External compliance                    POST to CRM API
                                   check returns allow/block              for contact tracking
```

The pipeline doesn't need to know what's on the other end of the webhook -- it just POSTs JSON and acts on the response. This makes the mail server a **first-class integration point** for business workflows, AI agents, and external systems.

### Visual Pipeline Testing

This is where the system becomes truly powerful. The webmail admin UI (and the TUI) include a **pipeline test mode** where you can:

1. **Compose or paste a sample email** (headers + body, or use a template)
2. **Select a pipeline** (inbound or outbound for a domain)
3. **Run the email through the pipeline step by step**
4. **See the JSON state after each filter** -- what changed, what was added, what was flagged
5. **See the final result** -- accepted, rejected, quarantined, and why

```
┌──────────────────────────────────────────────────────────────────────┐
│  Pipeline Tester: mail3.test inbound                                 │
├──────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  Input Email:                    │  Pipeline Steps:                  │
│  ┌─────────────────────────┐     │                                   │
│  │ From: spammer@bad.test  │     │  ✓ size_check      2ms  pass     │
│  │ To: bob@mail3.test      │     │  ✓ spf_check      45ms  fail     │
│  │ Subject: BUY NOW!!!     │     │    → Added: Received-SPF: fail   │
│  │                         │     │  ✓ dkim_verify    23ms  none     │
│  │ Amazing deals await...  │     │    → No DKIM signature found     │
│  └─────────────────────────┘     │  ✓ header_validate  1ms  pass    │
│                                  │  ✓ recipient_check  5ms  pass    │
│  ▼ JSON representation:         │  ✗ rspamd          12ms  REJECT  │
│  ┌─────────────────────────┐     │    → Score: 8.2 (threshold: 5.0) │
│  │ {                       │     │    → SPF fail (+3), caps (+2),   │
│  │   "envelope": {         │     │      exclamation (+1.5), ...     │
│  │     "mail_from": ...    │     │                                   │
│  │   },                    │     │  Result: REJECTED at rspamd       │
│  │   "headers": { ... },   │     │  Response: 550 Message rejected  │
│  │   "body": { ... }       │     │  as spam (score 8.2)             │
│  │ }                       │     │                                   │
│  └─────────────────────────┘     │  [Re-run] [Edit filters] [Save]  │
│                                  │                                   │
└──────────────────────────────────┴───────────────────────────────────┘
```

Users can click on any pipeline step to inspect the full JSON state at that point -- seeing exactly what the filter added, removed, or changed. This makes debugging mail processing issues trivial compared to trawling through Postfix logs.

### Pipeline API Endpoints

```
═══════════════════════════════════════════════════════════════════════════
 PIPELINE MANAGEMENT
 Added to: ADMINISTRATION layer of RESTMAIL Protocol API
═══════════════════════════════════════════════════════════════════════════

Pipelines
  GET    /api/admin/pipelines                           - List all pipelines
  GET    /api/admin/pipelines/:domain/:direction        - Get pipeline for domain + direction
  PUT    /api/admin/pipelines/:domain/:direction        - Create/update pipeline
  DELETE /api/admin/pipelines/:domain/:direction        - Delete pipeline (revert to default)

Filters (custom)
  GET    /api/admin/filters                             - List all custom filters
  POST   /api/admin/filters                             - Create custom filter
  GET    /api/admin/filters/:id                         - Get custom filter
  PUT    /api/admin/filters/:id                         - Update custom filter
  DELETE /api/admin/filters/:id                         - Delete custom filter

Pipeline Testing
  POST   /api/admin/pipelines/:domain/:direction/test   - Run sample email through pipeline
    Request:  { "email": { envelope, headers, body } }
    Response: { "steps": [ { filter, duration, result, state_after } ], "final_action": "reject" }

  POST   /api/admin/filters/:id/test                    - Test a single filter against sample input
    Request:  { "email": { envelope, headers, body } }
    Response: { "action": "continue", "message": { ... }, "log": { ... } }

Built-in Filters (read-only listing)
  GET    /api/admin/filters/builtin                     - List all built-in filters with docs
  GET    /api/admin/filters/builtin/:name               - Get docs + schema for a built-in filter
```

### Database Schema (additions)

```sql
-- Pipeline configuration per domain
CREATE TABLE pipelines (
    id            SERIAL PRIMARY KEY,
    domain_id     INTEGER REFERENCES domains(id),
    direction     VARCHAR(10) NOT NULL CHECK (direction IN ('inbound', 'outbound')),
    filters       JSONB NOT NULL DEFAULT '[]',   -- ordered array of filter references + config
    created_at    TIMESTAMP DEFAULT NOW(),
    updated_at    TIMESTAMP DEFAULT NOW(),
    UNIQUE(domain_id, direction)
);

-- Custom filter definitions
CREATE TABLE custom_filters (
    id            SERIAL PRIMARY KEY,
    name          VARCHAR(255) NOT NULL UNIQUE,
    description   TEXT,
    type          VARCHAR(10) NOT NULL CHECK (type IN ('action', 'transform')),
    direction     VARCHAR(10) NOT NULL CHECK (direction IN ('inbound', 'outbound', 'both')),
    condition     JSONB NOT NULL,                  -- matching rules
    action        VARCHAR(20) NOT NULL,            -- continue, reject, quarantine, discard, defer, duplicate
    transform     JSONB,                           -- modifications to apply (if type = transform)
    webhook       JSONB,                           -- webhook config (url, method, payload_template)
    reject_message VARCHAR(255),                   -- SMTP rejection message (if action = reject)
    created_at    TIMESTAMP DEFAULT NOW(),
    updated_at    TIMESTAMP DEFAULT NOW()
);

-- Attachment storage references (created by extract_attachments filter)
CREATE TABLE attachments (
    id            SERIAL PRIMARY KEY,
    message_id    INTEGER REFERENCES messages(id),
    filename      VARCHAR(255) NOT NULL,
    content_type  VARCHAR(127) NOT NULL,
    size          BIGINT NOT NULL,
    storage_type  VARCHAR(20) NOT NULL CHECK (storage_type IN ('filesystem', 's3')),
    storage_ref   TEXT NOT NULL,                    -- file path or S3 URI
    checksum      VARCHAR(64),                      -- SHA-256 for integrity verification
    created_at    TIMESTAMP DEFAULT NOW()
);

-- Pipeline execution log (for diagnostics and the visual tester)
CREATE TABLE pipeline_logs (
    id            SERIAL PRIMARY KEY,
    message_id    INTEGER REFERENCES messages(id),
    domain_id     INTEGER REFERENCES domains(id),
    direction     VARCHAR(10) NOT NULL,
    steps         JSONB NOT NULL,                  -- array of { filter, type, duration_ms, result, detail }
    final_action  VARCHAR(20) NOT NULL,
    executed_at   TIMESTAMP DEFAULT NOW()
);
```

### Storage Model: Email Body in DB, Files on Disk

The `extract_attachments` transform filter enables a clean split:

```
┌───────────────────────────────┐     ┌─────────────────────────────┐
│         PostgreSQL            │     │    Filesystem / Object Store │
│                               │     │                             │
│  messages table:              │     │  /data/attachments/         │
│  ┌─────────────────────────┐  │     │    2026/02/17/              │
│  │ id: 42                  │  │     │      a3f8c9d1-report.pdf   │
│  │ subject: "Q4 Report"   │  │     │      b7e2a4f0-photo.jpg    │
│  │ body: "Please see..."  │  │     │                             │
│  │ headers: { ... }       │  │     │  OR (if object store):      │
│  │ (small, searchable)    │  │     │  s3://restmail-attachments/ │
│  └─────────────────────────┘  │     │    2026/02/17/...          │
│                               │     │                             │
│  attachments table:           │     │                             │
│  ┌─────────────────────────┐  │     │                             │
│  │ message_id: 42          │──┼────►│  (file ref points here)     │
│  │ filename: report.pdf    │  │     │                             │
│  │ storage_ref: /data/...  │  │     │                             │
│  └─────────────────────────┘  │     │                             │
└───────────────────────────────┘     └─────────────────────────────┘
```

Benefits:
- **Full-text search** over all email bodies (they're just text in Postgres)
- **Fast queries** -- message listings don't load attachment blobs
- **Efficient storage** -- deduplication possible via checksum matching
- **Flexible backends** -- filesystem for dev, object store for production

### Integration with the Gateway

The pipeline sits between the gateway's protocol handlers and the REST API's message storage:

```
Inbound flow (SMTP → pipeline → storage):

  SMTP Handler                 Pipeline Engine              REST API
      │                             │                          │
      │──parsed email JSON────────►│                          │
      │                             │──filter 1 (SPF)────────►│ (DNS lookup)
      │                             │◄─result─────────────────│
      │                             │──filter 2 (DKIM)───────►│ (crypto verify)
      │                             │◄─result─────────────────│
      │                             │──filter N ...            │
      │                             │                          │
      │                             │──final: accept──────────►│──POST /api/messages/deliver
      │◄─250 OK────────────────────│                          │
      │                             │                          │
  OR: │◄─550 Rejected──────────────│ (if any filter rejects)  │

Outbound flow (compose → pipeline → SMTP relay):

  REST API                     Pipeline Engine              SMTP Relay
      │                             │                          │
      │──POST /api/.../messages───►│                          │
      │                             │──filter 1 (sender)──────►│
      │                             │──filter 2 (DKIM sign)───►│
      │                             │──filter N ...            │
      │                             │                          │
      │                             │──final: send─────────────►│──SMTP to remote
      │◄─201 Created──────────────│                          │
```

### Key Considerations
- The pipeline engine is a Go package (`internal/pipeline/`) used by both the gateway and the API
- Built-in filters are compiled Go functions with a standard interface: `func(ctx, email JSON) → (action, modified JSON, log)`
- Custom filters are interpreted from their JSON configuration at runtime -- no compilation needed
- Pipeline execution is **synchronous** -- the SMTP handler waits for the full pipeline to complete before responding (same as Postfix milters)
- Pipeline logs are stored for every message, enabling full audit trail and the visual tester to replay real messages
- The visual pipeline tester in the webmail admin UI calls `POST /api/admin/pipelines/:domain/:direction/test` with sample data
- Default pipelines are created when a domain is added (sensible defaults matching what Postfix would do out of the box)
- DKIM key management: domain DKIM private keys are stored in the database (or mounted as secrets), the `dkim_sign` filter reads them at runtime

### TODO
- [x] Design the pipeline engine interface with action/transform filter types (`internal/pipeline/engine.go`)
- [x] Define the filter interface: `type Filter interface { Execute(ctx, email JSON) → FilterResult }`
- [x] Implement email-to-JSON serialisation (RFC 2822 → JSON) and deserialisation (JSON → RFC 2822)
- [x] Implement built-in action filters: `spf_check`, `dmarc_check`, `header_validate`, `sender_verify`, `recipient_check`, `rate_limit`, `size_check`
- [x] Implement built-in transform filters: `dkim_verify`, `dkim_sign`, `header_cleanup`, `extract_attachments`
- [x] Implement ARC verification filter (`arc_verify`): parse ARC chain, verify seals + message signatures (reuses DKIM crypto)
- [x] Implement ARC sealing filter (`arc_seal`): add AAR + AMS + AS headers when relaying, using domain's DKIM key
- [x] Wire `dmarc_check` to consider ARC results when SPF/DKIM fail on forwarded mail
- [x] Implement the `webhook` filter (POST email JSON to external URL, act on response)
- [x] Design adapter filter interface (`internal/pipeline/adapter.go`) -- standard wrapper for external services
- [x] Implement rspamd adapter filter (HTTP API client, JSON → rspamd format → FilterResult)
- [x] Implement ClamAV adapter filter (clamd TCP protocol, INSTREAM command → FilterResult)
- [x] Add optional rspamd and clamav sidecar containers to docker-compose.yml (using profiles)
- [x] Document adapter vs built-in filter choice (lightweight built-ins vs full-featured adapters)
- [x] Implement the `duplicate` action (fork email to webhook/queue while continuing pipeline)
- [x] Implement attachment storage backend: filesystem writer + S3-compatible writer
- [x] Implement SHA-256 checksum computation in `extract_attachments` filter
- [x] Implement checksum-based attachment deduplication (reuse existing `storage_ref` if checksum matches)
- [x] Add `idx_attachments_checksum` index to `attachments` table
- [x] Implement reference-count safe deletion (only delete file when no other attachment rows reference the same `storage_ref`)
- [x] Implement attachment retrieval: API endpoint to stream attachment from storage ref
- [x] Implement custom filter interpreter (evaluate JSON condition matching + transforms at runtime)
- [x] Implement Sieve filter: embed a Sieve interpreter (or write one) that operates on email JSON
- [x] Support standard Sieve commands: `keep`, `fileinto`, `redirect`, `discard`, `reject`
- [x] Support Sieve extensions: `vacation`, `notify`, `body`, `regex`, `envelope`
- [x] Add Sieve script management API (per-mailbox CRUD, validation endpoint)
- [x] Implement vacation/auto-reply with RFC 3834 loop prevention (never reply to auto-replies, mailing lists, MAILER-DAEMON)
- [x] Create `vacation_responses` table for tracking sent auto-replies (dedup within time window)
- [x] Add vacation API endpoints (PUT/GET/DELETE `/api/v1/accounts/:id/vacation`)
- [x] Implement vacation toggle in webmail UI (simple on/off with subject, body, optional date range)
- [x] Ensure auto-reply messages include `Auto-Submitted: auto-replied` header
- [x] Implement JavaScript filter: Node.js sidecar container with sandboxed vm module (replaced goja per architecture decision)
- [x] Add JS filter security: execution timeout, memory limit, no filesystem/network access by default
- [x] Add JS script validation endpoint (syntax check + dry-run against sample email JSON)
- [x] Add JavaScript filter management API (CRUD, enable/disable, test)
- [x] Add optional allowlisted HTTP fetch for JS filters (admin-controlled)
- [x] Implement `contact_whitelist` action filter (lookup sender in recipient's contacts, set skip_filters)
- [x] Implement `domain_allowlist` action filter (admin-managed allow/block lists per domain)
- [x] Implement `greylist` action filter (sender/recipient/IP triple tracking, defer on first attempt)
- [x] Create `greylist_entries` database table and migrations
- [x] Implement pipeline skip logic (`skip_filters` in action result, `unskippable` filter flag)
- [x] Add contact trust levels (`auto`, `trusted`, `blocked`) to contacts table
- [x] Add contact management API: manual add, bulk import (vCard/CSV), block sender
- [x] Add domain allowlist/blocklist admin API endpoints
- [x] Implement quarantine system (hold borderline spam for user review)
- [x] Add quarantine API endpoints (list, release, whitelist-and-release, delete, digest)
- [x] Add quarantine review panel to webmail UI
- [x] Implement optional quarantine digest email (daily/weekly summary of held messages)
- [x] Implement pipeline execution engine (run filters in order, respect action/transform types, handle reject/quarantine/discard/duplicate)
- [x] Add pipeline management API endpoints (CRUD for pipelines and custom filters)
- [x] Add pipeline testing API endpoint (run sample email through pipeline, return step-by-step results with JSON state at each stage)
- [x] Add single-filter testing API endpoint (test one filter in isolation)
- [x] Integrate pipeline into the gateway's inbound SMTP handler (between parse and storage)
- [x] Integrate pipeline into the API's outbound message sending (between compose and relay)
- [x] Create database migrations for `pipelines`, `custom_filters`, `attachments`, `pipeline_logs` tables
- [x] Build default pipeline templates (sensible inbound + outbound defaults per domain)
- [x] Build visual pipeline tester UI component in the React webmail admin panel
- [x] Build pipeline configuration UI (drag-and-drop filter ordering, enable/disable, per-filter config)
- [x] Add pipeline management views to the TUI admin tool
- [x] Add Stage 10 to the test suite: pipeline filter tests (each built-in filter + custom filter evaluation + full pipeline execution + attachment extraction + webhook integration)

---

## Go Project Structure

All Go code lives in a single Go module. The project follows the standard Go project layout with `cmd/` for binaries and `internal/` for private packages.

```
rest-mail/
├── cmd/
│   ├── api/                    # REST API server binary (Phase 5)
│   │   └── main.go
│   ├── gateway/                # Protocol gateway binary (Phase 8)
│   │   └── main.go
│   ├── tui/                    # Terminal UI admin tool (Phase 9)
│   │   └── main.go
│   ├── migrate/                # Database migration runner
│   │   └── main.go
│   ├── seed/                   # Test data seeder
│   │   └── main.go
│   ├── certgen/                # TLS/DKIM certificate generator
│   │   └── main.go
│   └── openapi-gen/            # OpenAPI spec generator
│       └── main.go
│
├── internal/
│   ├── api/                    # REST API handlers, middleware, routes
│   │   ├── handlers/           # HTTP handler functions grouped by resource
│   │   ├── middleware/         # JWT auth, request logging, CORS
│   │   └── routes.go          # Route registration (/api/v1/...)
│   │
│   ├── db/                     # Database layer (GORM)
│   │   ├── models/            # GORM model structs (Domain, Mailbox, Message, etc.)
│   │   ├── migrations/        # Versioned migrations (for complex data transforms)
│   │   └── db.go              # GORM connection setup, AutoMigrate
│   │
│   ├── mail/                   # Mail operations (compose, send, store)
│   │
│   ├── mime/                   # RFC 2822 MIME parser (email ↔ JSON)
│   │
│   ├── pipeline/               # Mail processing pipeline engine (Phase 11)
│   │   ├── engine.go          # Pipeline executor (run filters in order)
│   │   ├── filter.go          # Filter interface definition
│   │   ├── adapter.go         # Adapter interface for external services
│   │   ├── builtin/           # Built-in filters (spf, dkim, dmarc, arc, etc.)
│   │   └── adapters/          # External service adapters (rspamd, clamav)
│   │
│   ├── gateway/                # Protocol gateway internals (Phase 8)
│   │   ├── smtp/              # SMTP inbound + outbound + AUTH
│   │   ├── imap/              # IMAP handler (translate to REST API calls)
│   │   └── pop3/              # POP3 handler (translate to REST API calls)
│   │
│   ├── dns/                    # Pluggable DNS provider adapters (Phase 3)
│   │   ├── provider.go        # DNSProvider interface
│   │   ├── dnsmasq.go         # Dev adapter (write dnsmasq config, reload)
│   │   ├── manual.go          # Returns required records as JSON
│   │   └── externaldns.go     # k8s: create DNSEndpoint CRDs
│   │
│   ├── tui/                    # Terminal UI components (Bubble Tea)
│   │
│   ├── auth/                   # JWT token creation/validation, bcrypt
│   │
│   ├── config/                 # Environment variable loading, defaults
│   │
│   └── metrics/                # Prometheus metrics registration + helpers
│
├── webmail/                    # React frontend (Phase 7)
│   ├── src/
│   ├── public/
│   ├── package.json
│   ├── vite.config.ts
│   ├── Dockerfile              # Production (nginx serving built assets)
│   └── Dockerfile.dev          # Development (Vite dev server with HMR)
│
├── docker/                     # Docker configuration
│   ├── postfix/               # Postfix config templates + entrypoint
│   ├── dovecot/               # Dovecot config templates + entrypoint
│   ├── dnsmasq/               # dnsmasq config
│   ├── fail2ban/              # fail2ban jails + filters
│   ├── prometheus/            # prometheus.yml
│   └── grafana/               # Dashboard JSON + datasource provisioning
│
├── docs/
│   └── openapi.yaml            # Generated OpenAPI 3.1 spec
│
├── docker-compose.yml          # Production-like compose
├── docker-compose.override.yml # Dev overrides (air, Vite HMR, volume mounts)
├── Dockerfile                  # Multi-stage Go build (API)
├── Dockerfile.dev              # Dev Dockerfile (air for hot reload)
├── Taskfile.yml                # Go Task runner (replaces Makefile)
├── .air.toml                   # Hot reload config for air
├── go.mod
└── go.sum
```

**Binary purposes:**

| Binary | Description | Runs as |
|--------|-------------|---------|
| `api` | REST API server — all `/api/v1/` and `/restmail/` endpoints | Docker container |
| `gateway` | Protocol gateway — SMTP/IMAP/POP3 ↔ REST translation | Docker container |
| `tui` | Terminal admin tool — Bubble Tea UI for managing servers | Host (not containerised) |
| `migrate` | Database migration runner — applies/rolls back SQL migrations | `task db:migrate` |
| `seed` | Test data seeder — populates dev database with test accounts/messages | `task db:seed` |
| `certgen` | Certificate generator — creates self-signed CA + per-domain certs, DKIM keys | `task certs:generate` |
| `openapi-gen` | OpenAPI spec generator — scans code annotations, outputs `openapi.yaml` | `task docs:openapi` |

**Key internal packages:**

| Package | Used by | Purpose |
|---------|---------|---------|
| `internal/api` | `cmd/api` | HTTP handlers, routing, middleware |
| `internal/db` | `cmd/api`, `cmd/gateway`, `cmd/migrate` | GORM models, AutoMigrate, connection setup |
| `internal/mail` | `cmd/api` | Compose, store, retrieve messages |
| `internal/mime` | `cmd/api`, `cmd/gateway` | RFC 2822 MIME ↔ JSON conversion |
| `internal/pipeline` | `cmd/api`, `cmd/gateway` | Mail processing pipeline engine + all filters |
| `internal/gateway` | `cmd/gateway` | SMTP/IMAP/POP3 protocol handlers |
| `internal/dns` | `cmd/api` | Pluggable DNS record management |
| `internal/tui` | `cmd/tui` | Bubble Tea views and components |
| `internal/auth` | `cmd/api`, `cmd/gateway` | JWT + bcrypt authentication |
| `internal/config` | All binaries | Environment variable loading |
| `internal/metrics` | `cmd/api`, `cmd/gateway` | Prometheus metrics |

---

## Development Tooling: Go Task (Taskfile.yml)

We use **[Go Task](https://taskfile.dev)** instead of Make as our task runner. Go Task is a modern alternative written in Go that supports:

- **Task dependencies** — declare which tasks depend on others; Go Task resolves the DAG automatically
- **Cross-platform** — works identically on Linux, macOS, and Windows (no shell quirks)
- **YAML syntax** — cleaner than Makefile syntax, easy to read and maintain
- **Variables and `.env` support** — first-class variable interpolation and dotenv loading
- **Watch mode** — `task --watch` re-runs tasks on file changes (useful for hot reload during dev)

### Installation

```bash
# macOS
brew install go-task

# Linux (snap)
sudo snap install task --classic

# Go install
go install github.com/go-task/task/v3/cmd/task@latest
```

### Taskfile.yml

```yaml
version: "3"

dotenv: [".env"]

vars:
  GO_MODULE: github.com/restmail/restmail
  BINARY_NAME: restmail-api
  GATEWAY_BINARY: restmail-gateway

tasks:
  # ── Build ──────────────────────────────────────────────
  build:
    desc: Build all Go binaries
    deps: [build:api, build:gateway]

  build:api:
    desc: Build the REST API server
    cmds:
      - go build -o bin/{{.BINARY_NAME}} ./cmd/api
    sources:
      - cmd/api/**/*.go
      - internal/**/*.go
    generates:
      - bin/{{.BINARY_NAME}}

  build:gateway:
    desc: Build the protocol gateway
    cmds:
      - go build -o bin/{{.GATEWAY_BINARY}} ./cmd/gateway
    sources:
      - cmd/gateway/**/*.go
      - internal/**/*.go
    generates:
      - bin/{{.GATEWAY_BINARY}}

  # ── Test ───────────────────────────────────────────────
  test:
    desc: Run all tests
    deps: [test:unit, test:integration]

  test:unit:
    desc: Run unit tests
    cmds:
      - go test -race -count=1 ./internal/...

  test:integration:
    desc: Run integration tests (requires running containers)
    deps: [up]
    cmds:
      - go test -race -count=1 -tags=integration ./tests/...

  test:e2e:
    desc: Run end-to-end tests against the full stack
    deps: [up]
    cmds:
      - go test -race -count=1 -tags=e2e ./tests/e2e/...

  test:openapi:
    desc: Validate API responses against OpenAPI spec
    deps: [openapi:generate]
    cmds:
      - go test -race -count=1 -tags=contract ./tests/contract/...

  lint:
    desc: Run linters
    cmds:
      - golangci-lint run ./...

  # ── Docker Compose ─────────────────────────────────────
  up:
    desc: Start all containers
    cmds:
      - docker compose up -d

  up:full:
    desc: Start all containers including optional sidecars (rspamd, clamav)
    cmds:
      - docker compose --profile filter up -d

  down:
    desc: Stop all containers
    cmds:
      - docker compose down

  down:clean:
    desc: Stop containers and remove volumes (destructive)
    cmds:
      - docker compose down -v
    prompt: This will delete all data volumes. Continue?

  logs:
    desc: Tail logs from all containers
    cmds:
      - docker compose logs -f --tail=100

  logs:api:
    desc: Tail logs from the API container
    cmds:
      - docker compose logs -f --tail=100 mail3

  ps:
    desc: Show running containers
    cmds:
      - docker compose ps

  # ── Database ───────────────────────────────────────────
  db:migrate:
    desc: Run database migrations
    deps: [up]
    cmds:
      - go run ./cmd/migrate up

  db:rollback:
    desc: Rollback last database migration
    cmds:
      - go run ./cmd/migrate down 1

  db:seed:
    desc: Seed database with test data
    deps: [db:migrate]
    cmds:
      - go run ./cmd/seed

  db:reset:
    desc: Reset database (drop + migrate + seed)
    cmds:
      - task: db:rollback
      - task: db:migrate
      - task: db:seed

  db:psql:
    desc: Open psql shell to the database
    cmds:
      - docker compose exec postgres psql -U restmail -d restmail

  # ── Certificates ───────────────────────────────────────
  certs:generate:
    desc: Generate self-signed CA and dev certificates
    cmds:
      - go run ./cmd/certgen --ca --domain="*.test"
    generates:
      - certs/ca.pem
      - certs/ca-key.pem

  certs:dkim:
    desc: Generate DKIM key pair for a domain
    cmds:
      - go run ./cmd/certgen --dkim --domain={{.CLI_ARGS}}

  # ── Code Generation ────────────────────────────────────
  openapi:generate:
    desc: Generate OpenAPI spec from code annotations
    cmds:
      - go run ./cmd/openapi-gen > docs/openapi.yaml
    sources:
      - internal/api/**/*.go
    generates:
      - docs/openapi.yaml

  openapi:validate:
    desc: Validate the OpenAPI spec
    deps: [openapi:generate]
    cmds:
      - npx @redocly/cli lint docs/openapi.yaml

  openapi:types:
    desc: Generate TypeScript types from OpenAPI spec
    deps: [openapi:generate]
    cmds:
      - npx openapi-typescript docs/openapi.yaml -o webmail/src/types/api.d.ts

  # ── Webmail Frontend ───────────────────────────────────
  webmail:install:
    desc: Install webmail frontend dependencies
    dir: webmail
    cmds:
      - npm ci

  webmail:dev:
    desc: Start webmail dev server with hot reload
    dir: webmail
    deps: [webmail:install]
    cmds:
      - npm run dev

  webmail:build:
    desc: Build webmail for production
    dir: webmail
    deps: [webmail:install]
    cmds:
      - npm run build

  # ── Development Workflow ───────────────────────────────
  dev:
    desc: Start full dev environment (containers + API with hot reload)
    deps: [up, db:migrate]
    cmds:
      - task --parallel webmail:dev dev:watch

  dev:watch:
    desc: Watch Go files and rebuild/restart API on change (using air)
    cmds:
      - air -c .air.toml

  # ── Release ────────────────────────────────────────────
  docker:build:
    desc: Build all Docker images
    cmds:
      - docker compose build

  # ── Housekeeping ───────────────────────────────────────
  clean:
    desc: Remove build artifacts
    cmds:
      - rm -rf bin/
      - rm -rf webmail/dist/

  fmt:
    desc: Format all Go source files
    cmds:
      - gofmt -w .
      - goimports -w .

  tidy:
    desc: Tidy Go modules
    cmds:
      - go mod tidy

  check:
    desc: Run all checks (format, lint, test)
    cmds:
      - task: fmt
      - task: lint
      - task: test
```

### Common Workflows

```bash
# First time setup
task up db:migrate db:seed certs:generate

# Daily development
task dev                    # starts everything with hot reload

# Run tests
task test                   # unit + integration
task test:e2e               # full end-to-end
task test:openapi           # contract tests against OpenAPI spec

# Before committing
task check                  # fmt + lint + test

# Generate TypeScript types from API
task openapi:types

# Database operations
task db:psql                # interactive SQL shell
task db:reset               # wipe and re-seed

# View logs
task logs                   # all containers
task logs:api               # just the API
```

### Task Dependencies in Action

Go Task automatically resolves dependencies. For example:

```
task test:openapi
  └── openapi:generate      (generates spec first)
        └── (source check)   (only regenerates if Go files changed)
```

```
task dev
  ├── up                     (starts Docker containers)
  ├── db:migrate             (runs migrations after containers are up)
  │     └── up               (already satisfied, skipped)
  └── webmail:dev + dev:watch (run in parallel)
```

This means you never need to remember prerequisite steps — just run the task you want and Go Task handles the rest.

### Hot Reload (Development Mode)

In development, **nothing should require rebuilding a Docker container** just because you changed code. Both the Go backend and the React frontend support live hot reload:

#### Go Backend: air

[air](https://github.com/air-verse/air) watches Go source files, automatically rebuilds, and restarts the binary on every change. No manual `go build` / restart cycle.

**.air.toml** (project root):
```toml
root = "."
tmp_dir = "tmp"

[build]
  cmd = "go build -o ./tmp/restmail-api ./cmd/api"
  bin = "./tmp/restmail-api"
  include_ext = ["go", "tpl", "tmpl", "html"]
  exclude_dir = ["tmp", "vendor", "webmail", "node_modules"]
  delay = 1000

[log]
  time = false

[color]
  main = "magenta"
  watcher = "cyan"
  build = "yellow"
  runner = "green"
```

Install:
```bash
go install github.com/air-verse/air@latest
```

The Go API container in dev mode mounts the host source code as a volume and runs `air` instead of the compiled binary:

```yaml
# docker-compose.override.yml (dev overrides)
services:
  mail3:
    build:
      context: .
      dockerfile: Dockerfile.dev   # includes air, uses go mod cache
    volumes:
      - .:/app                      # mount source code
      - go-mod-cache:/go/pkg/mod    # persist Go module cache
    command: air -c .air.toml
    environment:
      - CGO_ENABLED=0
```

This means: edit a `.go` file → air detects the change → rebuilds in ~1-2 seconds → restarts the server. No Docker rebuild needed.

#### React Webmail: Vite HMR

Vite's built-in **Hot Module Replacement (HMR)** provides instant in-browser updates without losing component state. This is automatic with the Vite dev server -- no configuration needed beyond the standard Vite setup.

In development, the webmail container runs the Vite dev server instead of nginx:

```yaml
# docker-compose.override.yml (dev overrides)
services:
  webmail:
    build:
      context: ./webmail
      dockerfile: Dockerfile.dev    # node:20, runs npm run dev
    volumes:
      - ./webmail/src:/app/src      # mount source files for HMR
    ports:
      - "5173:5173"                 # Vite dev server port
    command: npm run dev -- --host 0.0.0.0
```

Edit a React component → Vite patches the module in the browser instantly (sub-second). CSS changes apply without any page reload. Full page reload only happens if you edit code outside the HMR boundary (rare).

#### Development Architecture

```
docker-compose.yml              (base: production-like config)
docker-compose.override.yml     (dev: auto-loaded, adds volume mounts + hot reload)

task dev                        (starts everything in dev mode)
  ├── docker compose up -d      (picks up both yml files automatically)
  │     ├── mail3: air          (Go hot reload)
  │     ├── webmail: vite dev   (React HMR)
  │     ├── postgres            (persistent volume)
  │     ├── dnsmasq             (DNS)
  │     ├── mail1-postfix       (traditional mail)
  │     ├── mail1-dovecot       (traditional mail)
  │     ├── mail2-postfix       (traditional mail)
  │     └── mail2-dovecot       (traditional mail)
  └── db:migrate                (ensures schema is current)
```

The key principle: **`docker-compose.override.yml` is automatically loaded** by Docker Compose when it exists alongside `docker-compose.yml`. In production, you explicitly specify only the base file. In development, the override kicks in automatically, swapping compiled binaries for hot-reload dev servers and mounting source code volumes.

### TODO

- [x] Create `Taskfile.yml` at project root with all tasks above
- [x] Create `.air.toml` configuration for Go hot reload
- [x] Create `Dockerfile.dev` for Go API (includes air, mounts source)
- [x] Create `webmail/Dockerfile.dev` for React (runs Vite dev server)
- [x] Create `docker-compose.override.yml` with dev volume mounts and hot reload commands
- [x] Add Go Task and air installation to project README prerequisites
- [x] Ensure all CI/CD pipelines use `task` commands instead of raw shell commands
- [x] Add `task --list` output to developer onboarding docs
- [x] Consider adding a `task setup` meta-task that installs Go toolchain dependencies (golangci-lint, goimports, air, etc.)

---

## Monitoring: Prometheus + Grafana

The Go services (API and gateway) expose a **`/metrics` endpoint** in Prometheus exposition format. Prometheus scrapes these endpoints and stores the time-series data. Grafana provides dashboards and alerting.

### Architecture

```
Go API ──► /metrics ◄──┐
                        │
Go Gateway ──► /metrics ◄──── Prometheus ──── Grafana
                        │    (scrapes every   (dashboards +
Postgres ──► exporter ◄─┘     15 seconds)      alerting)
```

All three run as Docker containers:

```yaml
prometheus:
  image: prom/prometheus
  profiles: ["monitoring"]
  volumes:
    - ./monitoring/prometheus.yml:/etc/prometheus/prometheus.yml:ro
    - prometheus-data:/prometheus
  ports:
    - "9090:9090"

grafana:
  image: grafana/grafana
  profiles: ["monitoring"]
  volumes:
    - grafana-data:/var/lib/grafana
    - ./monitoring/dashboards:/etc/grafana/provisioning/dashboards:ro
    - ./monitoring/datasources:/etc/grafana/provisioning/datasources:ro
  ports:
    - "3000:3000"
  environment:
    - GF_SECURITY_ADMIN_PASSWORD=admin

postgres-exporter:
  image: prometheuscommunity/postgres-exporter
  profiles: ["monitoring"]
  environment:
    - DATA_SOURCE_NAME=postgresql://restmail:password@postgres:5432/restmail?sslmode=disable
```

Started with: `docker compose --profile monitoring up -d`

### Application Metrics (Go API + Gateway)

The Go services use the `prometheus/client_golang` library to expose metrics. Each service registers collectors at startup and serves them on an internal metrics port.

**API metrics:**

| Metric                                    | Type      | What it measures                                      |
|-------------------------------------------|-----------|-------------------------------------------------------|
| `restmail_api_requests_total`             | Counter   | Total API requests by method, path, status code       |
| `restmail_api_request_duration_seconds`   | Histogram | Request latency distribution                          |
| `restmail_api_active_connections`         | Gauge     | Current active HTTP connections                       |
| `restmail_messages_delivered_total`       | Counter   | Messages delivered (by domain, direction)             |
| `restmail_messages_rejected_total`        | Counter   | Messages rejected (by filter, reason)                 |
| `restmail_messages_quarantined_total`     | Counter   | Messages sent to quarantine                           |
| `restmail_search_queries_total`           | Counter   | Full-text search queries                              |
| `restmail_sse_active_connections`         | Gauge     | Current active SSE event streams                      |

**Gateway metrics:**

| Metric                                    | Type      | What it measures                                      |
|-------------------------------------------|-----------|-------------------------------------------------------|
| `restmail_smtp_connections_total`         | Counter   | SMTP connections by source IP country / port           |
| `restmail_smtp_active_connections`        | Gauge     | Current active SMTP sessions                          |
| `restmail_imap_connections_total`         | Counter   | IMAP connections                                      |
| `restmail_imap_active_connections`        | Gauge     | Current active IMAP sessions                          |
| `restmail_auth_failures_total`           | Counter   | Authentication failures (by protocol, IP)             |
| `restmail_queue_depth`                    | Gauge     | Current outbound queue size (by status)               |
| `restmail_queue_delivery_duration_seconds`| Histogram | Time from queue insertion to delivery                 |
| `restmail_queue_retries_total`           | Counter   | Delivery retry attempts                               |
| `restmail_queue_bounces_total`           | Counter   | Bounce messages generated                             |
| `restmail_pipeline_duration_seconds`      | Histogram | Pipeline execution time (by domain, direction)        |
| `restmail_pipeline_filter_duration_seconds`| Histogram| Individual filter execution time (by filter name)     |
| `restmail_tls_cert_expiry_seconds`       | Gauge     | Seconds until certificate expiry (by domain)          |

**Postgres exporter metrics** (automatic via postgres-exporter):
- Connection pool usage, query duration, row counts, table sizes, replication lag, cache hit ratio

### Grafana Dashboards

Pre-built dashboards provisioned automatically:

1. **Mail Overview** — messages delivered/rejected/queued over time, active connections, error rate
2. **Queue Health** — queue depth, delivery latency, retry rate, bounce rate
3. **Pipeline Performance** — per-filter execution times, reject reasons, throughput
4. **Security** — auth failures, banned IPs, spam scores distribution, quarantine volume
5. **Infrastructure** — Postgres connections, query latency, CPU/memory (if node exporter added)

### Alerting Rules (Prometheus)

```yaml
# monitoring/alerts.yml
groups:
  - name: restmail
    rules:
      - alert: QueueBacklog
        expr: restmail_queue_depth{status="pending"} > 100
        for: 10m
        annotations:
          summary: "Outbound queue has {{ $value }} pending messages"

      - alert: CertExpiringSoon
        expr: restmail_tls_cert_expiry_seconds < 604800  # 7 days
        annotations:
          summary: "Certificate for {{ $labels.domain }} expires in < 7 days"

      - alert: HighAuthFailureRate
        expr: rate(restmail_auth_failures_total[5m]) > 1
        for: 5m
        annotations:
          summary: "High authentication failure rate: {{ $value }}/sec"

      - alert: HighErrorRate
        expr: rate(restmail_api_requests_total{status=~"5.."}[5m]) / rate(restmail_api_requests_total[5m]) > 0.05
        for: 5m
        annotations:
          summary: "API error rate above 5%"
```

### TODO

- [x] Add `prometheus/client_golang` dependency to Go API and gateway
- [x] Implement `/metrics` endpoint on API (internal port, not exposed to mail clients)
- [x] Implement `/metrics` endpoint on gateway (internal port)
- [x] Register all application-level Prometheus collectors (counters, gauges, histograms)
- [x] Create `monitoring/prometheus.yml` scrape config
- [x] Create pre-built Grafana dashboards (JSON provisioning)
- [x] Add Prometheus, Grafana, and postgres-exporter to docker-compose.yml (profile: monitoring)
- [x] Create alerting rules for queue backlog, cert expiry, auth failure rate, error rate
- [x] Document how to access Grafana (default credentials, dashboard layout)

---

## Rate Limiting Strategy

Rate limiting operates at **two distinct levels** in the restmail architecture, and they serve different purposes:

### API Rate Limiting (Infrastructure Layer)

API rate limiting is **not the mail server's responsibility**. Since every API endpoint requires a JWT token, the server already knows who's calling. But defending against thousands of connections, brute-force login attempts, or API abuse is best handled by infrastructure in front of the mail server:

```
Client ──► [Reverse Proxy / API Gateway] ──► restmail API
            │
            ├── Connection rate limiting (per IP)
            ├── Request rate limiting (per JWT subject)
            ├── Brute-force protection (failed login throttling)
            └── DDoS mitigation
```

In production, this is typically:
- **Kubernetes**: Ingress controller rate limiting (nginx-ingress `rate-limiting` annotations, or Traefik middleware)
- **Standalone**: nginx/Caddy/HAProxy rate limiting in front of the API
- **Cloud**: Cloudflare / AWS WAF / similar edge protection

The mail server should **not** implement its own HTTP request rate limiting. This avoids duplicating logic that infrastructure does better, and keeps the API code focused on mail operations.

### Email Sending Rate Limiting (Pipeline Layer)

The `rate_limit` **pipeline filter** is different — it limits how many emails a user or domain can *send* per time window. This prevents a compromised account from sending spam and protects domain reputation:

```
Outbound email ──► [rate_limit filter] ──► continue / defer
```

This is mail-specific business logic that belongs in the pipeline, not in infrastructure. It checks per-user and per-domain sending quotas (e.g. 100 emails/hour per mailbox, 10,000/day per domain) and returns `defer` if exceeded.

### Summary

| Concern                    | Where it lives       | Why                                                       |
|----------------------------|----------------------|-----------------------------------------------------------|
| HTTP request rate limiting | Infrastructure       | Generic concern; reverse proxy / ingress does this better |
| Login brute-force          | Infrastructure       | IP-based throttling at edge                               |
| DDoS protection            | Infrastructure       | Needs to be handled before requests reach the app         |
| Email sending quotas       | Pipeline filter      | Mail-specific business rule, per-user/per-domain          |
| SMTP connection throttling | Gateway + fail2ban   | Go-level limits (in-process) + fail2ban (kernel-level IP bans) |

---

## Environment Variable Reference

All environment variables consolidated in one place. These configure Go services (API, gateway), traditional mail containers (Postfix, Dovecot), and infrastructure (monitoring, CI/CD).

### Application Configuration (API + Gateway)

| Variable | Default | Used By | Purpose |
|----------|---------|---------|---------|
| `DB_HOST` | `postgres` | API, Gateway | PostgreSQL hostname |
| `DB_NAME` | `restmail` | API, Gateway | PostgreSQL database name |
| `DB_USER` | `restmail` | API, Gateway | PostgreSQL username |
| `DB_PASS` | (secret) | API, Gateway | PostgreSQL password |
| `DB_MAX_OPEN_CONNS` | `25` | API, Gateway | Max open DB connections |
| `DB_MAX_IDLE_CONNS` | `10` | API, Gateway | Max idle DB connections |
| `DB_CONN_MAX_LIFETIME` | `5m` | API, Gateway | Max connection reuse time |
| `API_PORT` | `8080` | API | HTTPS listen port |
| `JWT_SECRET` | (secret) | API, Gateway | HMAC-SHA256 key for JWT signing |
| `MASTER_KEY` | (secret) | API, Gateway | AES-256 key for private key encryption |
| `LOG_LEVEL` | `info` | API, Gateway | Log level: `debug`, `info`, `warn`, `error` |
| `DNS_PROVIDER` | `dnsmasq` | API | DNS provider adapter: `dnsmasq`, `cloudflare`, `route53`, `externaldns`, `manual` |
| `TLS_CERT_PATH` | `/certs/mail3.test.crt` | API, Gateway | TLS certificate path (dev) |
| `TLS_KEY_PATH` | `/certs/mail3.test.key` | API, Gateway | TLS private key path (dev) |

### SMTP Gateway Limits

| Variable | Default | Used By | Purpose |
|----------|---------|---------|---------|
| `SMTP_MAX_CONNECTIONS` | `500` | Gateway | Max simultaneous SMTP connections |
| `SMTP_MAX_PER_IP` | `20` | Gateway | Max connections per source IP |
| `SMTP_RATE_PER_IP` | `10/min` | Gateway | New connections per minute per IP |
| `SMTP_MAX_AUTH_FAILURES` | `5` | Gateway | Failed logins before temp ban (10 min) |
| `SMTP_COMMAND_TIMEOUT` | `300s` | Gateway | Idle timeout per SMTP command |
| `SMTP_MAX_MESSAGE_SIZE` | `25MB` | Gateway | Reject at DATA if exceeded |
| `SMTP_MAX_RECIPIENTS` | `100` | Gateway | Max RCPT TO per message |

### PROXY Protocol (behind load balancer)

| Variable | Default | Used By | Purpose |
|----------|---------|---------|---------|
| `SMTP_PROXY_PROTOCOL` | `false` | Gateway | Enable PROXY protocol on SMTP ports (25, 465, 587) |
| `IMAP_PROXY_PROTOCOL` | `false` | Gateway | Enable PROXY protocol on IMAP ports (143, 993) |
| `POP3_PROXY_PROTOCOL` | `false` | Gateway | Enable PROXY protocol on POP3 ports (110, 995) |
| `PROXY_TRUSTED_CIDRS` | (empty) | Gateway | Comma-separated CIDRs allowed to send PROXY headers |

### Traditional Mail Containers (Postfix, Dovecot)

| Variable | Default | Used By | Purpose |
|----------|---------|---------|---------|
| `MAIL_DOMAIN` | `mail1.test` | Postfix, Dovecot | Primary mail domain |
| `MAIL_HOSTNAME` | `mail1.test` | Postfix | SMTP banner hostname |
| `DB_HOST` | `postgres` | Postfix, Dovecot | PostgreSQL hostname (shared with API) |
| `DB_NAME` | `restmail` | Postfix, Dovecot | PostgreSQL database name |
| `DB_USER` | `restmail` | Postfix, Dovecot | PostgreSQL username |
| `DB_PASS` | (secret) | Postfix, Dovecot | PostgreSQL password |
| `TLS_CERT_PATH` | `/certs/mail1.test.crt` | Postfix, Dovecot | TLS certificate path |
| `TLS_KEY_PATH` | `/certs/mail1.test.key` | Postfix, Dovecot | TLS private key path |

### Monitoring (optional, profile: monitoring)

| Variable | Default | Used By | Purpose |
|----------|---------|---------|---------|
| `GF_SECURITY_ADMIN_PASSWORD` | `admin` | Grafana | Grafana admin password |
| `DATA_SOURCE_NAME` | (connection string) | postgres-exporter | Postgres connection for metrics |

### CI/CD Secrets (GitHub Actions)

| Variable | Where Stored | Purpose |
|----------|-------------|---------|
| `MASTER_KEY` | GitHub Environment secrets | Private key encryption key |
| `STAGING_HOST` | GitHub repo secrets | SSH connection for staging deploy |
| `PRODUCTION_HOST` | GitHub Environment secrets | SSH connection for production deploy |
| `GITHUB_TOKEN` | Automatic | Push Docker images to GHCR |

---

## CI/CD Pipeline

Fully automated build, test, and deploy pipeline using **GitHub Actions**. Every push triggers the pipeline; every merge to `main` produces deployable Docker images. The pipeline uses `task` commands throughout to ensure consistency with local development.

### Pipeline Architecture

```
                    ┌─────────────────────────────────────────────────┐
                    │                GitHub Actions                    │
                    │                                                  │
  push/PR ────────►│  ┌──────────┐  ┌──────────┐  ┌──────────────┐  │
                    │  │  Lint &   │  │  Build   │  │    Test      │  │
                    │  │  Check    │──►│  All     │──►│  (3 stages) │  │
                    │  └──────────┘  └──────────┘  └──────┬───────┘  │
                    │                                      │          │
                    │                           ┌──────────▼───────┐  │
                    │                           │  Docker Build    │  │
                    │                           │  + Push to GHCR  │  │
                    │                           └──────────┬───────┘  │
                    │                                      │          │
  main merge ──────►│                           ┌──────────▼───────┐  │
                    │                           │  Deploy          │  │
                    │                           │  (staging/prod)  │  │
                    │                           └──────────────────┘  │
                    └─────────────────────────────────────────────────┘
```

### Workflow: CI (runs on every push and PR)

```yaml
# .github/workflows/ci.yml
name: CI

on:
  push:
    branches: [main, develop]
  pull_request:
    branches: [main]

env:
  GO_VERSION: '1.22'
  NODE_VERSION: '20'
  REGISTRY: ghcr.io
  IMAGE_PREFIX: ghcr.io/${{ github.repository }}

jobs:
  # ─── Stage 1: Lint & Static Analysis ───────────────────────────
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ env.GO_VERSION }}
      - uses: arduino/setup-task@v2
      - name: Go module cache
        uses: actions/cache@v4
        with:
          path: ~/go/pkg/mod
          key: go-mod-${{ hashFiles('go.sum') }}
      - run: task fmt -- --check     # verify formatting (no changes needed)
      - run: task lint                # golangci-lint
      - run: task tidy -- --check    # go mod tidy (verify no changes)

  lint-webmail:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: ${{ env.NODE_VERSION }}
          cache: 'npm'
          cache-dependency-path: webmail/package-lock.json
      - run: cd webmail && npm ci
      - run: cd webmail && npm run lint
      - run: cd webmail && npm run typecheck

  # ─── Stage 2: Build ────────────────────────────────────────────
  build-go:
    needs: [lint]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ env.GO_VERSION }}
      - uses: arduino/setup-task@v2
      - run: task build
      - uses: actions/upload-artifact@v4
        with:
          name: go-binaries
          path: bin/

  build-webmail:
    needs: [lint-webmail]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: ${{ env.NODE_VERSION }}
          cache: 'npm'
          cache-dependency-path: webmail/package-lock.json
      - run: task webmail:build
      - uses: actions/upload-artifact@v4
        with:
          name: webmail-dist
          path: webmail/dist/

  # ─── Stage 3: Test ─────────────────────────────────────────────
  test-unit:
    needs: [build-go]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ env.GO_VERSION }}
      - uses: arduino/setup-task@v2
      - run: task test:unit
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: unit-test-results
          path: test-results/

  test-integration:
    needs: [build-go]
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16
        env:
          POSTGRES_USER: restmail
          POSTGRES_PASSWORD: restmail
          POSTGRES_DB: restmail_test
        ports:
          - 5432:5432
        options: >-
          --health-cmd pg_isready
          --health-interval 10s
          --health-timeout 5s
          --health-retries 5
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ env.GO_VERSION }}
      - uses: arduino/setup-task@v2
      - run: task db:migrate
        env:
          DB_HOST: localhost
          DB_USER: restmail
          DB_PASS: restmail
          DB_NAME: restmail_test
      - run: task test:integration
        env:
          DB_HOST: localhost
          DB_USER: restmail
          DB_PASS: restmail
          DB_NAME: restmail_test

  test-e2e:
    needs: [build-go, build-webmail]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ env.GO_VERSION }}
      - uses: arduino/setup-task@v2
      - uses: actions/download-artifact@v4
        with:
          name: go-binaries
          path: bin/
      - run: chmod +x bin/*
      - name: Start full stack
        run: task up
      - name: Wait for healthy
        run: task health:wait  # polls healthchecks until all containers are healthy
      - name: Run e2e test suite (Stages 1-9)
        run: task test:e2e
      - name: Collect logs on failure
        if: failure()
        run: task logs > e2e-logs.txt 2>&1
      - uses: actions/upload-artifact@v4
        if: failure()
        with:
          name: e2e-failure-logs
          path: e2e-logs.txt
      - run: task down

  test-openapi:
    needs: [build-go]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ env.GO_VERSION }}
      - uses: arduino/setup-task@v2
      - run: task openapi:validate

  # ─── Stage 4: Security Scanning ────────────────────────────────
  security:
    needs: [build-go]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ env.GO_VERSION }}
      - name: Go vulnerability check
        run: go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...
      - name: Dependency audit (webmail)
        run: cd webmail && npm audit --audit-level=high
```

### Workflow: Docker Build & Publish (runs on main merge)

```yaml
# .github/workflows/docker.yml
name: Docker Build & Publish

on:
  push:
    branches: [main]
    tags: ['v*']

env:
  REGISTRY: ghcr.io
  IMAGE_PREFIX: ghcr.io/${{ github.repository }}

jobs:
  docker:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    strategy:
      matrix:
        image:
          - { name: api,      dockerfile: Dockerfile,            context: . }
          - { name: gateway,  dockerfile: Dockerfile.gateway,    context: . }
          - { name: webmail,  dockerfile: webmail/Dockerfile,    context: webmail }
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: docker/build-push-action@v6
        with:
          context: ${{ matrix.image.context }}
          file: ${{ matrix.image.dockerfile }}
          push: true
          tags: |
            ${{ env.IMAGE_PREFIX }}/${{ matrix.image.name }}:latest
            ${{ env.IMAGE_PREFIX }}/${{ matrix.image.name }}:${{ github.sha }}
            ${{ env.IMAGE_PREFIX }}/${{ matrix.image.name }}:${{ github.ref_name }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

### Workflow: Deploy (runs after Docker build on tags)

```yaml
# .github/workflows/deploy.yml
name: Deploy

on:
  workflow_run:
    workflows: ["Docker Build & Publish"]
    types: [completed]
    branches: [main]

jobs:
  deploy-staging:
    if: ${{ github.event.workflow_run.conclusion == 'success' }}
    runs-on: ubuntu-latest
    environment: staging
    steps:
      - uses: actions/checkout@v4
      - name: Deploy to staging
        run: |
          # SSH to staging server, pull new images, restart
          ssh ${{ secrets.STAGING_HOST }} << 'DEPLOY'
            cd /opt/restmail
            docker compose pull
            docker compose up -d --remove-orphans
            docker compose exec api task db:migrate
            # Wait for health
            timeout 120 bash -c 'until docker compose ps | grep -q "healthy"; do sleep 5; done'
          DEPLOY
      - name: Smoke test staging
        run: |
          curl -sf https://staging.restmail.example.com/api/health

  deploy-production:
    needs: [deploy-staging]
    runs-on: ubuntu-latest
    environment:
      name: production
      url: https://restmail.example.com
    steps:
      - uses: actions/checkout@v4
      - name: Deploy to production
        run: |
          ssh ${{ secrets.PRODUCTION_HOST }} << 'DEPLOY'
            cd /opt/restmail
            docker compose pull
            docker compose up -d --remove-orphans
            docker compose exec api task db:migrate
            timeout 120 bash -c 'until docker compose ps | grep -q "healthy"; do sleep 5; done'
          DEPLOY
      - name: Smoke test production
        run: |
          curl -sf https://restmail.example.com/api/health
```

### Branch Strategy

```
main ──────────────────────────────────────────────► (production releases)
  │                                                    ▲
  └── feature/xyz ──► PR ──► review ──► merge ─────────┘
  └── fix/abc ──────► PR ──► review ──► merge ─────────┘
```

- **`main`** is always deployable. Every merge triggers Docker build + staging deploy.
- **Feature branches** open PRs against `main`. CI runs on every push to a PR.
- **Tags** (`v1.0.0`, `v1.1.0`) trigger production deployment after staging passes.
- **No `develop` branch** — keep it simple. Feature flags or short-lived branches.

### Environment Configuration

| Environment | Trigger | Docker Images | DB Migrations | Manual Approval |
|-------------|---------|---------------|---------------|-----------------|
| CI          | Every push/PR | Built locally | Test DB in GitHub Actions | No |
| Staging     | Merge to `main` | From GHCR (`:latest`) | Auto-applied | No |
| Production  | Git tag (`v*`) | From GHCR (`:v1.x.x`) | Auto-applied | Yes (GitHub Environment protection) |

### Secrets Management

| Secret | Where | Purpose |
|--------|-------|---------|
| `MASTER_KEY` | GitHub Environment secrets | AES-256 key for private key encryption |
| `STAGING_HOST` | GitHub repo secrets | SSH connection string for staging |
| `PRODUCTION_HOST` | GitHub Environment secrets | SSH connection string for production |
| `GHCR token` | Automatic (`GITHUB_TOKEN`) | Push Docker images to GitHub Container Registry |

### Additional CI Tasks

```yaml
# .github/workflows/scheduled.yml
name: Scheduled Checks

on:
  schedule:
    - cron: '0 6 * * 1'  # Every Monday at 06:00 UTC

jobs:
  dependency-update:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - run: go get -u ./... && go mod tidy
      - run: cd webmail && npm update
      - uses: peter-evans/create-pull-request@v6
        with:
          title: 'chore: weekly dependency update'
          branch: chore/dependency-update
          commit-message: 'chore: update dependencies'

  security-audit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - run: govulncheck ./...
      - run: cd webmail && npm audit
```

### TODO
- [x] Create `.github/workflows/ci.yml` (lint, build, test pipeline)
- [x] Create `.github/workflows/docker.yml` (Docker build + push to GHCR)
- [x] Create `.github/workflows/deploy.yml` (staging + production deploy)
- [x] Create `.github/workflows/scheduled.yml` (weekly dependency updates + security audit)
- [x] Add `task health:wait` command to Taskfile.yml (poll healthchecks until all pass)
- [x] Add `task fmt -- --check` mode (verify formatting without modifying files)
- [x] Add `task tidy -- --check` mode (verify go.mod is tidy)
- [x] Create `Dockerfile.gateway` (multi-stage build for gateway binary)
- [x] Set up GitHub Environments (staging, production) with protection rules
- [x] Configure GitHub Container Registry (GHCR) for image storage
- [x] Add branch protection rules (require CI pass, require review)
- [x] Ensure all CI/CD pipelines use `task` commands instead of raw shell commands

---

## Email Client Autoconfig / Autodiscover

Email clients (Thunderbird, Outlook, Apple Mail, etc.) can automatically discover IMAP/SMTP server settings when a user enters just their email address. We support three standards to cover all major clients:

### Standards Supported

| Standard | Clients | Discovery Method |
|----------|---------|-----------------|
| **Autoconfig** (Mozilla) | Thunderbird, many Linux clients | `GET https://autoconfig.{domain}/mail/config-v1.1.xml` |
| **Autodiscover** (Microsoft) | Outlook, Windows Mail | `POST https://autodiscover.{domain}/autodiscover/autodiscover.xml` |
| **MobileConfig** (Apple) | Apple Mail, iOS | `GET https://{domain}/.well-known/autoconfig/mail/config-v1.1.xml` |

### How It Works

The API server handles all three endpoints. No additional containers needed — the API inspects the requested domain, looks up the mail server configuration from the database, and returns the appropriate XML.

```
User enters: alice@mail3.test
    │
    Client queries: https://autoconfig.mail3.test/mail/config-v1.1.xml
    │
    ▼
┌──────────┐     ┌──────────┐
│  dnsmasq │◄────│  Client  │  DNS: autoconfig.mail3.test → gateway IP
└──────────┘     └──────────┘
                      │
                      ▼ HTTPS
                 ┌──────────┐     ┌──────────┐
                 │ gateway  │────►│   API    │  Reverse-proxy or direct
                 └──────────┘     └──────────┘
                                       │
                                       ▼
                              Generate XML response
                              with IMAP/SMTP settings
                              for mail3.test
```

### Autoconfig Response (Mozilla XML)

```xml
<?xml version="1.0" encoding="UTF-8"?>
<clientConfig version="1.1">
  <emailProvider id="mail3.test">
    <domain>mail3.test</domain>
    <displayName>mail3.test Mail</displayName>
    <displayShortName>mail3</displayShortName>

    <incomingServer type="imap">
      <hostname>mail3.test</hostname>
      <port>993</port>
      <socketType>SSL</socketType>
      <authentication>password-cleartext</authentication>
      <username>%EMAILADDRESS%</username>
    </incomingServer>

    <incomingServer type="pop3">
      <hostname>mail3.test</hostname>
      <port>995</port>
      <socketType>SSL</socketType>
      <authentication>password-cleartext</authentication>
      <username>%EMAILADDRESS%</username>
    </incomingServer>

    <outgoingServer type="smtp">
      <hostname>mail3.test</hostname>
      <port>465</port>
      <socketType>SSL</socketType>
      <authentication>password-cleartext</authentication>
      <username>%EMAILADDRESS%</username>
    </outgoingServer>
  </emailProvider>
</clientConfig>
```

### API Endpoints

```
GET  /mail/config-v1.1.xml                          - Mozilla Autoconfig
POST /autodiscover/autodiscover.xml                  - Microsoft Autodiscover
GET  /.well-known/autoconfig/mail/config-v1.1.xml    - Apple / well-known fallback
```

These endpoints are **unauthenticated** (clients query them before the user has logged in). They only reveal the server hostname and port — no sensitive information.

### DNS Requirements

For each domain, autoconfig needs a DNS record:

```
autoconfig.mail3.test.    A    <gateway-ip>
autodiscover.mail3.test.  A    <gateway-ip>
```

These are added automatically by the DNS provider adapter when a domain is created, alongside the existing MX, SPF, DKIM, and PTR records.

### TODO
- [x] Implement Mozilla Autoconfig XML generation (`GET /mail/config-v1.1.xml`)
- [x] Implement Microsoft Autodiscover XML generation (`POST /autodiscover/autodiscover.xml`)
- [x] Implement Apple well-known fallback (`GET /.well-known/autoconfig/mail/config-v1.1.xml`)
- [x] Add `autoconfig.{domain}` and `autodiscover.{domain}` DNS records to DNS provider adapters
- [x] Add autoconfig DNS records to dnsmasq dev config
- [ ] Test with Thunderbird, Outlook, and Apple Mail

---

## MTA-STS (RFC 8461) & TLS-RPT (RFC 8460)

**MTA-STS** (Mail Transfer Agent Strict Transport Security) tells other mail servers "you must use TLS when delivering to my domains — if the TLS handshake fails, don't fall back to plaintext." This prevents TLS downgrade attacks where a man-in-the-middle strips STARTTLS from the connection.

**TLS-RPT** (TLS Reporting) lets other servers send you reports about TLS failures they encountered when delivering to your domain. This is how you detect misconfigurations or attacks.

### How MTA-STS Works

1. Sending server looks up `_mta-sts.mail3.test` TXT record in DNS
2. If present, it fetches the MTA-STS policy from `https://mta-sts.mail3.test/.well-known/mta-sts.txt`
3. The policy says "enforce TLS" or "test mode (report only)"
4. Sending server respects the policy when connecting via SMTP

### DNS Records Required

```
_mta-sts.mail3.test.    TXT  "v=STSv1; id=20260217T000000"
_smtp._tls.mail3.test.  TXT  "v=TLSRPTv1; rua=mailto:tls-reports@mail3.test"
mta-sts.mail3.test.     A    <gateway-ip>
```

The `id` field changes whenever the policy changes (convention: timestamp). This is how sending servers know to re-fetch the policy.

### MTA-STS Policy

Served by the API at `https://mta-sts.mail3.test/.well-known/mta-sts.txt`:

```
version: STSv1
mode: enforce
mx: mail3.test
max_age: 86400
```

| Field | Value | Meaning |
|-------|-------|---------|
| `mode` | `enforce` | Reject delivery if TLS fails (use `testing` initially to collect reports) |
| `mx` | `mail3.test` | Only deliver to this MX hostname |
| `max_age` | `86400` | Cache the policy for 24 hours |

### TLS-RPT Processing

Other servers send TLS failure reports as JSON (RFC 8460) to the `rua` address. The inbound pipeline detects these reports (Content-Type: `application/tlsrpt+json`) and stores them for admin review rather than delivering to a regular inbox.

### API Endpoints

```
GET    /api/v1/admin/domains/:id/mta-sts           - Get current MTA-STS policy
PUT    /api/v1/admin/domains/:id/mta-sts           - Update policy (mode, max_age)
GET    /api/v1/admin/domains/:id/tls-reports        - List received TLS-RPT reports
GET    /.well-known/mta-sts.txt                      - Public policy endpoint (unauthenticated)
```

### TODO
- [x] Implement MTA-STS policy serving (`GET /.well-known/mta-sts.txt`)
- [x] Implement MTA-STS admin API endpoints (get/set policy per domain)
- [x] Add `_mta-sts`, `_smtp._tls`, and `mta-sts` DNS records to DNS provider adapters
- [x] Add MTA-STS DNS records to dnsmasq dev config
- [x] Implement TLS-RPT report ingestion (detect `application/tlsrpt+json`, store for admin)
- [x] Add TLS reports viewer to webmail admin panel
- [x] Implement MTA-STS policy checking in outbound SMTP relay (respect other servers' policies)

---

## SMTP PROXY Protocol (HAProxy PROXY v2)

When the gateway sits behind a load balancer or reverse proxy (HAProxy, nginx, AWS NLB), the gateway sees the proxy's IP address instead of the real client IP. This breaks SPF checks, fail2ban, connection throttling, and logging.

The **PROXY protocol** (v1/v2, originally by HAProxy) solves this by prepending the real client IP to the TCP connection. The gateway reads this header before the SMTP handshake begins.

### How It Works

```
Client (1.2.3.4) ──► Load Balancer ──PROXY v2 header──► Gateway
                                                          │
                                                          ├── Reads PROXY header: real IP = 1.2.3.4
                                                          ├── Uses 1.2.3.4 for SPF check
                                                          ├── Uses 1.2.3.4 for fail2ban logging
                                                          └── Uses 1.2.3.4 for rate limiting
```

### Configuration

PROXY protocol is **opt-in** per listener. Enabled via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `SMTP_PROXY_PROTOCOL` | `false` | Enable PROXY protocol on SMTP ports (25, 465, 587) |
| `IMAP_PROXY_PROTOCOL` | `false` | Enable PROXY protocol on IMAP ports (143, 993) |
| `POP3_PROXY_PROTOCOL` | `false` | Enable PROXY protocol on POP3 ports (110, 995) |
| `PROXY_TRUSTED_CIDRS` | (empty) | Comma-separated list of CIDRs allowed to send PROXY headers (e.g. `10.0.0.0/8,172.16.0.0/12`) |

When enabled, the gateway expects a PROXY protocol v1 or v2 header as the first bytes on the connection. If the header is missing or comes from an untrusted IP, the connection is dropped. This prevents spoofing.

### Implementation

The Go `net.Listener` is wrapped with a PROXY protocol decoder (using `github.com/pires/go-proxyproto`):

```go
// Wrap the listener with PROXY protocol support
proxyListener := &proxyproto.Listener{
    Listener: tcpListener,
    Policy: func(upstream net.Addr) (proxyproto.Policy, error) {
        if isTrusted(upstream) {
            return proxyproto.REQUIRE, nil   // must have PROXY header
        }
        return proxyproto.REJECT, nil        // reject untrusted sources
    },
}
```

After decoding, the real client IP is available via `conn.RemoteAddr()` and flows naturally into SPF checks, fail2ban log entries, and rate limiting — no other code needs to change.

### TODO
- [x] Add `github.com/pires/go-proxyproto` dependency
- [x] Wrap SMTP/IMAP/POP3 listeners with PROXY protocol decoder (opt-in via env var)
- [x] Implement trusted CIDR validation for PROXY headers
- [x] Ensure real client IP propagates to SPF checks, fail2ban logs, and rate limiting
- [x] Document HAProxy / nginx PROXY protocol configuration examples
- [x] Add PROXY protocol test cases (valid header, missing header, untrusted source)

---

## Message-ID Generation

Every outgoing email must have a globally unique `Message-ID` header (RFC 5322 Section 3.6.4). This is critical for threading (In-Reply-To / References), deduplication, and deliverability — missing or poorly formatted Message-IDs can trigger spam filters.

### Format

```
Message-ID: <{unique-id}@{sending-domain}>
```

**Example:** `<f47ac10b-58cc-4372-a567-0e02b2c3d479@mail3.test>`

### Generation Strategy

| Component | Source | Example |
|-----------|--------|---------|
| Unique part | UUID v7 (time-ordered, random) | `0190a5e3-7c4a-7b12-8c9d-1a2b3c4d5e6f` |
| Domain part | Sending domain from the `From` header | `mail3.test` |

**UUID v7** is preferred over UUID v4 because it encodes the timestamp, which makes Message-IDs naturally sortable and aids debugging. The Go standard library (`crypto/rand` + encoding) generates these.

### When to Generate

- **Compose/send via API:** API generates Message-ID before storing the message and passing to the outbound pipeline
- **Compose/send via SMTP submission:** If the client provides a Message-ID, keep it. If not, the gateway generates one before passing to the pipeline
- **Never overwrite:** If an incoming message already has a Message-ID, preserve it. Only generate for outbound messages that lack one.

### Threading Support

The generated Message-ID is stored in the `messages` table and used for threading:

```
Original:    Message-ID: <aaa@mail3.test>
Reply:       Message-ID: <bbb@mail3.test>
             In-Reply-To: <aaa@mail3.test>
             References: <aaa@mail3.test>

Reply chain: Message-ID: <ccc@mail3.test>
             In-Reply-To: <bbb@mail3.test>
             References: <aaa@mail3.test> <bbb@mail3.test>
```

### TODO
- [x] Implement UUID v7 Message-ID generation in `internal/mail/`
- [x] Generate Message-ID in compose/send API endpoint (before pipeline)
- [x] Generate Message-ID in gateway SMTP submission handler (if client omits it)
- [x] Store Message-ID in messages table, use for In-Reply-To / References threading
- [x] Never overwrite existing Message-ID on inbound messages

---

## Calendar Invite Support (iCalendar / RFC 5545)

Email is the primary transport for calendar invitations. When someone sends a meeting invite, the email contains an `iCalendar` attachment (`text/calendar`; `.ics` file). We support receiving, displaying, and responding to calendar invites — but we are **not building a full calendar server** (that's CalDAV territory).

### What We Support

| Feature | Supported | Notes |
|---------|-----------|-------|
| **Receive** calendar invites | Yes | Parse `text/calendar` MIME parts, display in webmail |
| **Display** invite details | Yes | Date/time, location, attendees, description |
| **Respond** (Accept/Decline/Tentative) | Yes | Generate iTIP REPLY and send back to organizer |
| **Send** calendar invites | Yes | Compose view supports attaching `.ics` with iTIP REQUEST method |
| **Free/busy queries** | No | Requires CalDAV server — out of scope |
| **Calendar storage** | No | We store emails, not calendar events. Use a CalDAV server for that |

### iCalendar in Email (iTIP / RFC 6047)

Calendar invites travel as email messages with a `text/calendar` MIME part:

```
Content-Type: multipart/mixed; boundary="----boundary"

------boundary
Content-Type: text/html; charset=UTF-8

<p>You're invited to a meeting!</p>

------boundary
Content-Type: text/calendar; charset=UTF-8; method=REQUEST
Content-Disposition: attachment; filename="invite.ics"

BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//restmail//EN
METHOD:REQUEST
BEGIN:VEVENT
UID:unique-event-id@mail3.test
DTSTART:20260301T100000Z
DTEND:20260301T110000Z
SUMMARY:Project Kickoff
LOCATION:Conference Room A
ORGANIZER;CN=Alice:mailto:alice@mail3.test
ATTENDEE;CN=Bob;RSVP=TRUE:mailto:bob@mail3.test
DESCRIPTION:Let's discuss the project roadmap.
END:VEVENT
END:VCALENDAR

------boundary--
```

### Webmail UI for Calendar Invites

When the webmail displays a message containing a `text/calendar` part, it renders a **calendar invite card** instead of (or alongside) the regular message body:

```
┌─────────────────────────────────────────────────────┐
│  📅 Meeting Invitation                               │
│                                                      │
│  Project Kickoff                                     │
│  March 1, 2026 · 10:00 AM – 11:00 AM (UTC)         │
│  Conference Room A                                   │
│                                                      │
│  Organizer: Alice <alice@mail3.test>                │
│  Attendees: Bob <bob@mail3.test>                    │
│                                                      │
│  Let's discuss the project roadmap.                  │
│                                                      │
│  [ Accept ]  [ Tentative ]  [ Decline ]              │
└─────────────────────────────────────────────────────┘
```

Clicking Accept/Decline/Tentative generates an iTIP REPLY email sent to the organizer:

```
METHOD:REPLY
ATTENDEE;CN=Bob;PARTSTAT=ACCEPTED:mailto:bob@mail3.test
```

### iCalendar Parsing

The `text/calendar` MIME type is handled by the MIME parser (`internal/mime/`). The parsed iCalendar data is included in the email JSON representation:

```json
{
  "calendar": {
    "method": "REQUEST",
    "events": [
      {
        "uid": "unique-event-id@mail3.test",
        "summary": "Project Kickoff",
        "dtstart": "2026-03-01T10:00:00Z",
        "dtend": "2026-03-01T11:00:00Z",
        "location": "Conference Room A",
        "organizer": { "name": "Alice", "email": "alice@mail3.test" },
        "attendees": [
          { "name": "Bob", "email": "bob@mail3.test", "rsvp": true, "partstat": "NEEDS-ACTION" }
        ],
        "description": "Let's discuss the project roadmap."
      }
    ]
  }
}
```

### TODO
- [x] Add iCalendar parser to `internal/mime/` (parse `text/calendar` MIME parts into structured JSON)
- [x] Include parsed calendar data in the email JSON representation
- [x] Build calendar invite card component in webmail React UI
- [x] Implement Accept/Decline/Tentative response (generate iTIP REPLY email)
- [x] Support composing emails with `.ics` attachments (iTIP REQUEST method)
- [x] Handle calendar updates (METHOD:CANCEL, METHOD:REQUEST with updated SEQUENCE)

---

## Phase 12: Project README and Documentation

Write a comprehensive README.md at the project root that serves as the entry point for understanding the project. The README should cover:

### Content
1. **Project Overview** — What rest-mail is, its architecture (3 domains, REST API, protocol gateway, webmail, TUI)
2. **Quick Start** — How to get the entire system running with `docker compose up`
3. **Prerequisites** — Go 1.24+, Node 20+, Docker, Docker Compose
4. **Architecture Diagram** — ASCII or text description of how domains, servers, and services connect
5. **Service Map** — Table of all services with their container names, IPs, ports, and roles
6. **Starting Individual Services** — How to run the API, gateway, webmail, and TUI independently for development
7. **Testing** — How to run the e2e test suite (`go test ./tests/e2e/ -v`), what each stage tests, and the stage dependency chain
8. **Project Structure** — Directory layout with descriptions of each major package (`cmd/`, `internal/api/`, `internal/gateway/`, `internal/pipeline/`, `internal/tui/`, `webmail/`, `docker/`, `tests/`)
9. **API Reference** — Summary of key API endpoints (auth, admin, messages, folders, search, restmail, health)
10. **Configuration** — Environment variables and their defaults
11. **Development Workflow** — How to add a new API endpoint, add a pipeline filter, modify the TUI, or update the webmail frontend

### TODO
- [x] Write the project README.md at the root
- [x] Include service map with IPs, ports, and container names
- [x] Document the e2e test suite and stage ordering
- [x] Document the development workflow for each component

---

## Phase 13: Project Website

Build a static marketing/landing website for Rest Mail. The site explains the project, its architecture, and lets visitors try the webmail.

### Concept & Aesthetic Direction

- **Tagline:** "Because the mail server protocol needs to take a break..."
- **Theme:** Grey/white palette with soft gradients, evoking a heavenly/ethereal vibe. The visual metaphor: traditional mail server protocols (SMTP, IMAP, POP3) are being sent to heaven — ascending into the clouds, replaced by something better, easier, and modern.
- **Typography:** Elegant serif display font for headings (gravitas, memorial quality) paired with a clean sans-serif body font.
- **Backgrounds:** Cloud-like radial gradients, warm golden/amber light accents (sunbeams breaking through), generous whitespace.
- **Motion:** Gentle floating animations, subtle parallax, ascending visual elements.

### Pages & Sections

1. **Landing / Hero** — Project name, tagline, call-to-action buttons (Try Webmail, View on GitHub)
2. **What is Rest Mail?** — Brief explanation of the problem with traditional mail servers and how Rest Mail replaces them with a REST API while remaining fully compatible with the outside world
3. **How It Works** — Architecture overview: protocol gateway translates SMTP/IMAP/POP3 into REST API calls. Diagram showing the three mail domains (traditional ↔ rest-mail interop)
4. **The RESTMAIL Protocol** — How two Rest Mail servers discover each other via EHLO and upgrade from SMTP to native REST delivery
5. **Features** — Key features: full protocol compatibility, pipeline/filter engine, Sieve support, webmail, TUI admin, real-time SSE, attachment dedup, quota management, etc.
6. **Architecture** — Tech stack breakdown: Go backend, PostgreSQL, chi router, React webmail, Bubble Tea TUI, Docker Compose orchestration
7. **About** — Information about the creator/maintainer
8. **Footer** — GitHub repo link, webmail login link, license

### Links

- **GitHub:** Link to the project repository (placeholder until public)
- **Webmail:** Link to the hosted webmail instance so visitors can try it out (also serves as an integration test for the full system)

### Implementation

- Static HTML/CSS/JS site (no framework needed — it's a marketing page)
- Lives in `website/` directory at project root
- Dockerfile + nginx for serving in Docker Compose
- Added as a service in `docker-compose.yml`

### TODO

- [x] Design and build the landing page (hero, tagline, CTA)
- [x] Build the "What is Rest Mail?" and "How It Works" sections
- [x] Build the RESTMAIL Protocol explanation section with diagrams
- [x] Build the Features section
- [x] Build the Architecture section
- [x] Build the About page
- [x] Create Go static file server for the website (`cmd/website/main.go`)
- [x] Add website service to docker-compose.yml
- [x] Add Taskfile entries for building/serving the website

---

## Phase 14: (Future — awaiting further input)

_Additional requirements will be added here as they are provided._

---

## Resolved Decisions

These were previously open questions, now resolved:

- **TLS:** Required. Self-signed CA for `.test` dev environment, Let's Encrypt for production. SNI-based cert selection in Go. See Phase 6.
- **Message storage (mail1/mail2):** Maildir format on persistent Docker volumes. Dovecot reads/writes Maildir, API reads metadata from shared Postgres.
- **Persistent storage:** All stateful data uses named Docker volumes (Postgres, Maildir, attachments, certs).
- **Password hashing:** Must be consistent across Dovecot, Postfix, and Go backend. Decision: use Dovecot-compatible `{BLF-CRYPT}` (bcrypt) scheme with prefix so all three can read/write the same hashes.
- **SMTP AUTH:** Required on submission ports (587/465). AUTH PLAIN and AUTH LOGIN supported.
- **Outbound queue:** Database-backed queue with exponential backoff retry. Bounce messages (RFC 3464 DSN) generated on permanent failure.
- **MIME parsing:** Full recursive MIME tree parsing, not flat. Handles multipart/alternative, nested structures, character encoding, RFC 2047 headers.
- **DMARC:** Implemented as a built-in action filter (`dmarc_check`). Runs after SPF and DKIM in the pipeline. Also available via rspamd adapter for full-featured setups.
- **Reverse DNS (PTR):** Required. dnsmasq provides PTR records in dev. Pluggable DNS provider adapter for production (externaldns, Cloudflare, Route53, manual).
- **CC/BCC:** Fully supported. Headers use structured `{name, address}` arrays. BCC is in envelope only (stripped from outbound headers per RFC).
- **IMAP IDLE:** Supported. Gateway implements IDLE for push notifications. Webmail uses SSE (`/api/accounts/:id/events`) for real-time updates.
- **Drafts:** Full draft lifecycle: save, auto-save, send draft. API endpoints and webmail UI support.
- **Search:** Postgres full-text search over message bodies. API supports query, folder filter, advanced filters (from, to, date range, has:attachment).
- **Folder CRUD:** Full folder management: create, rename, delete. Both in API and IMAP gateway.
- **Attachment download:** Explicit API endpoints for listing and downloading attachments streamed from storage refs.
- **Process management:** Postfix and Dovecot containers use supervisord + rsyslogd for process management and structured logging.
- **Configuration:** Postfix/Dovecot configured via environment variables rendered into config templates by entrypoint scripts. Go services configured via environment variables (DB connection, ports, TLS paths, log level, DNS provider).
- **Test data cleanup:** `POST /api/test/reset` truncates test data. Snapshot/restore support for repeatable test runs.
- **Real-time updates:** SSE (Server-Sent Events) via `/api/v1/accounts/:id/events`. Unidirectional server→client, auto-reconnect, proxy-friendly. No WebSocket.
- **Email threading:** Supported via References/In-Reply-To headers (RFC 5256). Thread view endpoints in API. UI can enable/disable.
- **API versioning:** All endpoints prefixed with `/api/v1/`. Future versions at `/api/v2/` etc. Server-to-server versioned via RESTMAIL capability string.
- **OpenAPI:** OpenAPI 3.1 spec generated from code annotations. Used for Swagger UI, contract testing, and TypeScript type generation.
- **Task runner:** Go Task (taskfile.dev) with `Taskfile.yml`. No Make. Supports task dependencies, cross-platform, YAML syntax.
- **Hot reload (dev):** Go backend uses air for live rebuild/restart. React frontend uses Vite HMR. Source mounted as Docker volumes via `docker-compose.override.yml`. No container rebuilds during development.
- **API rate limiting:** Handled at infrastructure layer (reverse proxy / ingress controller), not in the mail server. Email sending rate limits remain as a pipeline filter.
- **Sieve support:** RFC 5228 Sieve scripts as pipeline transform filters. Per-mailbox scripts with standard commands and extensions.
- **JavaScript filters:** Sandboxed JS scripts via Node.js sidecar container (replaced goja with containerized runtime per architecture decision). Execution timeout, memory limits, no filesystem/network access. Full programmatic control over email filtering.
- **Import/export:** Not planned for initial release. May revisit later.
- **Multi-recipient storage:** Each recipient gets their own copy of the message. Simple, correct, avoids reference-counting complexity and permission bugs.
- **Greylisting:** Supported as a built-in `greylist` pipeline action filter. Temporarily rejects first delivery from unknown senders. Skipped for whitelisted contacts.
- **Trust-based filtering:** Spamina-inspired contact whitelisting. Known senders (auto-collected from sent mail) skip spam scoring. Contacts have trust levels (`auto`, `trusted`, `blocked`). Domain-wide allow/block lists for admin control. Quarantine system with digest for borderline spam.
- **ARC (RFC 8617):** Supported. `arc_verify` (inbound) and `arc_seal` (outbound) filters. Reuses DKIM key infrastructure. DMARC considers ARC results when SPF/DKIM fail on forwarded mail.
- **Mailing lists:** Not planned for initial release. No distribution list / group expansion system. May revisit later.
- **Vacation / auto-reply:** Supported via Sieve `vacation` extension and a simple webmail toggle API. RFC 3834 loop prevention enforced at system level: never auto-reply to auto-replies, mailing lists, MAILER-DAEMON, or the same sender twice within the configured window. `vacation_responses` table tracks sent replies.
- **Error handling / graceful degradation:** Deferred. HA (high availability) deployment would solve most degradation scenarios. Not planned for initial single-instance release. May revisit when adding Kubernetes deployment support.
- **SMTP connection throttling:** Two layers. Go-level in-process limits (max connections, per-IP rate, auth failure tracking). fail2ban for persistent kernel-level IP banning via iptables — reads gateway structured logs, bans repeat offenders. fail2ban runs as optional Docker sidecar.
- **Quota management:** Per-mailbox storage quotas tracked incrementally. Counts subject, body, and attachment bytes only (user-facing data). `recipient_check` rejects delivery when over quota. IMAP `GETQUOTA` supported. Admin API to set per-mailbox and domain-default quotas.
- **Concurrency model:** Single Go server instance for initial release. The API and gateway are each one Go process. Go's goroutine model handles concurrent connections naturally (one goroutine per SMTP/IMAP/POP3 connection, one per HTTP request). This is sufficient for small business deployments (hundreds of accounts, not millions). Horizontal scaling (multiple API/gateway replicas behind a load balancer) is a future concern — the design is stateless enough to support it, but we don't implement it now.
- **Private key encryption:** All private keys (TLS certificates, DKIM signing keys) are encrypted at rest in the database using AES-256-GCM. A master key is provided via the `MASTER_KEY` environment variable. This prevents database compromise from exposing private keys.
- **CI/CD:** GitHub Actions with three workflows: CI (lint, build, test on every push/PR), Docker build (push to GHCR on main merge), Deploy (staging auto-deploy on main, production on git tags with manual approval). All jobs use `task` commands for consistency. Security scanning (govulncheck, npm audit) included.
- **Backup & restore:** Deferred to a separate plan (`BACKUP_PLAN.md`). Covers pg_dump (simple) and WAL archiving (production), attachment volume snapshots, MASTER_KEY backup procedures, and automated restore verification.
- **Autoconfig / Autodiscover:** Supported. Three standards: Mozilla Autoconfig, Microsoft Autodiscover, Apple well-known. API serves XML responses for automatic email client configuration. DNS records (`autoconfig.{domain}`, `autodiscover.{domain}`) added automatically by DNS provider adapters.
- **MTA-STS (RFC 8461):** Supported. Publishes MTA-STS policy at `/.well-known/mta-sts.txt` to enforce TLS for inbound delivery. TLS-RPT (RFC 8460) ingests TLS failure reports from other servers. Outbound relay respects remote MTA-STS policies.
- **SMTP PROXY protocol:** Supported (opt-in). HAProxy PROXY v1/v2 for preserving real client IP behind load balancers. Configured per-listener via env vars. Required for correct SPF checks, fail2ban, and rate limiting behind a proxy.
- **Message-ID generation:** UUID v7-based Message-IDs generated for all outbound messages that lack one. Format: `<uuid-v7@sending-domain>`. Existing Message-IDs on inbound messages are never overwritten. Used for In-Reply-To / References threading.
- **Internationalized email (EAI):** Deferred to a separate plan (`EAI_PLAN.md`). Covers SMTPUTF8 (RFC 6531), UTF-8 headers (RFC 6532), IMAP/POP3 UTF-8 support, IDN domain handling, and message downgrading for non-EAI recipients.
- **Calendar invites (iCalendar):** Supported. Parse `text/calendar` MIME parts (RFC 5545), display invite cards in webmail (date, location, attendees), respond with Accept/Decline/Tentative (iTIP REPLY). Compose view supports attaching `.ics` invites. Not a full calendar server — no CalDAV, no free/busy queries.
- **Go web framework:** chi (`github.com/go-chi/chi/v5`). stdlib-compatible (built on `net/http`), lightweight (zero dependencies), clean middleware composition, and route grouping with per-group middleware. Chosen over fiber (fasthttp-based, not stdlib-compatible) and echo/gin (heavier, more opinionated).
- **Compose view:** Both HTML (rich text) and plain text modes. Default is HTML using TipTap (`@tiptap/react`), a headless ProseMirror-based editor. Users can toggle to plain text. API always sends both `text/plain` and `text/html` in `multipart/alternative` MIME structure.
- **RESTMAIL server-to-server authentication:** No separate authentication (no API keys, no mTLS). Inbound delivery to `/restmail/messages` is open — the same model as SMTP, where any server can deliver to you. Identity is verified by DKIM signatures, SPF records, and DMARC policy. The TLS certificate proves the server hostname. Outbound submission (sending FROM a mailbox) requires JWT authentication.
- **RESTMAIL upgrade:** Mandatory between RESTMAIL servers. When the outbound relay detects the `RESTMAIL` capability in the remote server's EHLO response, it must upgrade to HTTPS/REST delivery. No configuration option to force SMTP between RESTMAIL siblings.
- **Mail1/mail2 storage model:** Traditional Maildir format on Docker volumes for message content. Account data (domains, mailboxes, aliases, credentials) stored in the shared PostgreSQL database — the same tables serve both traditional (Postfix/Dovecot) and rest-mail (mail3) servers. The API does not read Maildir; it queries Postgres for account metadata and can probe messages via IMAP.
- **Attachment deduplication:** Yes. The `extract_attachments` filter computes SHA-256 checksums before writing. If an identical checksum already exists in the `attachments` table, the existing `storage_ref` is reused (no duplicate file written). Deletion uses reference-count checks to prevent premature file removal.
- **Core DDLs:** Explicit CREATE TABLE definitions added for all six core tables: `domains`, `mailboxes`, `aliases`, `webmail_accounts`, `linked_accounts`, and `messages`. Includes indexes for message lookup, threading, unread counts, and full-text search (GIN).
- **Standard API response format:** All API responses follow a consistent structure. Success: `{ "data": {...} }` (single) or `{ "data": [...], "pagination": {...} }` (collection). Errors: `{ "error": { "code": "...", "message": "...", "details": {} } }`. Cursor-based pagination. Standard error codes (400–500).
- **Logging:** Structured JSON to stdout only via Go's `log/slog` package. No custom log storage, no log files, no log rotation inside the server. External systems (K8s, Docker Compose logs, Loki, ELK) handle collection and aggregation.
- **JWT authentication details:** Access tokens: 15-minute expiry, HMAC-SHA256, sent via `Authorization: Bearer` header. Claims include `sub`, `email`, `webmail_account_id`, `iss`, `iat`, `exp`. Refresh tokens: 7-day expiry, stored in HTTP-only Secure SameSite=Strict cookie (`restmail_refresh`). No refresh cookie = user must log in again. No token revocation needed (short-lived access tokens).
- **Spam scoring:** No built-in `spam_score` filter. Rspamd (optional sidecar) handles all content-based spam scoring. If rspamd is not deployed, no spam scoring occurs. A basic built-in filter may be added in the future if requested.
- **Environment variable reference:** Consolidated reference table covering ~38 env vars across six categories: Application Configuration, SMTP Gateway Limits, PROXY Protocol, Traditional Mail Containers, Monitoring, and CI/CD Secrets.
- **Database ORM:** GORM (`gorm.io/gorm`) for all database operations and schema management. Model structs define the schema, `AutoMigrate` handles migrations. No raw SQL migration files.
- **Thumbnail endpoint:** Removed from API surface. Thumbnail generation is webmail client functionality, not server responsibility.
- **POP3 gateway:** Full POP3 protocol handler added to the gateway. Translates 11 POP3 commands (USER, PASS, STAT, LIST, UIDL, RETR, TOP, DELE, NOOP, RSET, QUIT) to REST API calls. Same architecture as the IMAP gateway handler.
- **IMAP GETQUOTA:** GETQUOTA and GETQUOTAROOT (RFC 2087) added to the IMAP gateway command table and TODO. Returns quota info via REST API.
- **Bcrypt cost factor:** Cost factor 10. Consistent across Dovecot, Postfix, and Go backend. Stored with `{BLF-CRYPT}` prefix.
- **Docker filter profile:** All filtering-related containers (rspamd, clamav, fail2ban) use `profiles: ["filter"]`. Single `--profile filter` flag enables the full filtering stack.

## Open Questions

_All open questions have been resolved. See Resolved Decisions above._
