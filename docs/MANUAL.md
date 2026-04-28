# RESTMAIL Manual

This document consolidates every plan, stage document, and reference guide that previously lived across `docs/`, `docs/plans/`, and `docs/stages/`. It is the single source of truth for what the project is, what works today, how it was built, and what remains.

Originals are preserved under `docs/plans/archive/` for historical reference.

---

## 1. Overview

RESTMAIL is a Go-based mail server platform that exposes full email functionality through a REST API while remaining protocol-indistinguishable from Postfix/Dovecot at the network layer. It ships with a React webmail, a separate React admin UI, SMTP/IMAP/POP3 gateways, a pluggable pipeline engine (16+ built-in filters plus JavaScript and Sieve custom filters), DKIM/SPF/DMARC/ARC/MTA-STS/TLS-RPT support, and a standalone CLI for mail server auditing.

The development stack simulates three parallel mail domains on one host — `mail1.test` and `mail2.test` run traditional Postfix + Dovecot, while `mail3.test` routes through the RESTMAIL gateways. They share one PostgreSQL instance for account data and exist to verify cross-domain delivery, anti-spoofing, and protocol compatibility in a realistic environment.

### Key design pillars

- **Protocol gateway model** — SMTP/IMAP/POP3 gateways translate wire protocol into REST calls backed by PostgreSQL. Clients see a conventional mail server; internally it is all HTTP.
- **REST-native inter-server delivery** — When two RESTMAIL servers discover each other via the `RESTMAIL` SMTP extension, they drop SMTP and upgrade to HTTPS POST, bypassing DATA phase entirely. Fallback to SMTP for non-RESTMAIL peers.
- **Pluggable everywhere** — DNS providers (dnsmasq / manual / externaldns / cloudflare / route53), pipeline filters, certificate management, and authentication backends are all swappable.
- **Dual-DB compatibility** — Passwords stored in Dovecot-compatible `{BLF-CRYPT}` format so mail1/mail2 and mail3 share one accounts table.

### Top-level directory layout

```
cmd/                  Go binaries (api, smtp-gateway, imap-gateway, pop3-gateway,
                      console, migrate, seed, certgen, instantmailcheck, website)
internal/             Shared packages (api, auth, config, crypto, db, dns, gateway,
                      mail, mailcheck, metrics, mime, pipeline, console)
webmail/              React end-user webmail (Vite + TS + Tailwind)
admin/                React admin UI (TanStack Router + Zustand + Tailwind v4)
website/              Static project landing page
docker/               Dockerfiles and config templates (postfix, dovecot, dnsmasq,
                      gateways, js-filter-sidecar, etc.)
docker-compose.yml    Core stack (+ override and profile files)
Taskfile.yml          Task runner for dev/build/test workflows
tests/e2e/            End-to-end integration test suite
docs/                 This manual + reference docs + archived plans
```

### Development environment notes

- The shared docker network `rest-mail_mailnet` uses subnet **`10.99.0.0/16`** (moved from `172.20.0.0/16` on 2026-04-22 to avoid host-level collisions with docker's default auto-allocation pool).
- Static IPs are load-bearing, not defensive: `dnsmasq` publishes A records at specific IPs, SPF records embed literal IPs (`v=spf1 ip4:10.99.0.11 -all`), and Postfix `mynetworks` uses the CIDR. Do not switch to docker service-name DNS without reworking the mail-internet simulation.
- Every `start:*` task in `Taskfile.yml` is marked `run: once`. Without this, parallel `docker compose up --force-recreate` invocations race on transitive dependencies and fail with "container name already in use."

---

## 2. Changelog

Dates below reflect when the corresponding feature/plan was merged, implemented, or last verified.

### 2026-04-22 — Dev-env hardening
- Moved docker network `mailnet` subnet from `172.20.0.0/16` to `10.99.0.0/16` across `docker-compose.yml`, `Taskfile.yml`, `docker/postfix/conf/main.cf.tmpl`, `docker/dnsmasq/dnsmasq.conf`, all `website/`/`admin/`/`webmail/`/`monitoring/` compose files, e2e test suite, and `docs/proxy-protocol.md` / `docs/dns-providers.md`.
- Added `run: once` to all 20 `start:*` tasks in `Taskfile.yml` to dedupe parallel docker compose invocations (fixes race on `start:postgres-mail3`, `start:api`, etc.).
- Fixed webmail links on project landing page ([website/index.html:66](../website/index.html#L66), [:514](../website/index.html#L514)) — now use `/webmail` through the reverse proxy instead of raw `localhost:8080`.

### 2026-02-23 — Stage 4: Queue Management UI complete
- Multi-select with "select all / clear" across filtered results.
- Bulk retry and bulk delete with inline confirmation and sequential execution (prevents API overload).
- Raw message viewer component ([admin/src/components/queue/RawMessageViewer.tsx](../admin/src/components/queue/RawMessageViewer.tsx)) — collapsible, monospace, copy-to-clipboard.
- Auto-refresh every 15 s, pauses when tab hidden (Page Visibility API), manual refresh button.
- Toast notification system (success/error/warning/info, 5 s auto-dismiss).
- Updated [admin/src/lib/stores/queueStore.ts](../admin/src/lib/stores/queueStore.ts) with selection state and bulk actions; enhanced [admin/src/routes/queue/index.tsx](../admin/src/routes/queue/index.tsx) and [admin/src/routes/queue/$id.tsx](../admin/src/routes/queue/$id.tsx).

### 2026-02-22 — Instant Mail Check CLI shipped
- Standalone diagnostic binary at [cmd/instantmailcheck/main.go](../cmd/instantmailcheck/main.go) with internal package [internal/mailcheck/](../internal/mailcheck/).
- Four tiers: public probe (no credentials) → send test → authenticated round-trip → exploit simulation.
- 40+ individual checks across DNS, SMTP, IMAP, POP3, TLS, security, reputation, headers, round-trip.
- Three output modes: terminal (lipgloss), JSON (`--json`), Markdown (`--markdown --output`).
- Weighted scoring; exit code 2 if `--threshold` not met.
- `task build:instantmailcheck` and `task build:instantmailcheck:all` (cross-compile).

### 2026-02-18 — Multiple plan batches drafted
Design and implementation documents for "Batch 2" (attachment ownership + dedup, quota accounting, connection limiter, fail2ban), "Batch 3" (contact auto-populate, vacation filter, queue bulk ops, SSE refinements), "Critical Items" (9 integration gaps), and "Webmail themes + settings" (6 palettes, user menu, settings page). Status of each item is tracked in §5 Outstanding — some have since been implemented; others remain on the backlog.

### 2026-02-17 — Bugfixes & endpoint wiring plans drafted
Design docs for 7 pipeline/gateway integration fixes and 5 API endpoint wiring tasks (attachments, contacts, drafts, threads, SSE events). Status tracked in §5.

### Pre-2026-02 — Foundation
- PLAN.md defines 11-phase architecture. Phases 1–5 landed: infrastructure, Postfix/Dovecot integration, dnsmasq + PostgreSQL, Go backend foundation (auth, chi router, GORM models), and core REST endpoints. Phase 6+ (TLS/cert management, Let's Encrypt, advanced admin) is partial.
- ADMIN_PLAN.md defines 8-phase admin UI roadmap. Phases 1–4 largely done (foundation, dashboard, domain management, mailbox CRUD, Stage 4 queue). Phases 5–8 (pipelines UI, RBAC backend, settings, polish) outstanding.
- Reference docs published: [adapter-filters.md](adapter-filters.md), [dns-providers.md](dns-providers.md), [fail2ban-setup.md](fail2ban-setup.md), [proxy-protocol.md](proxy-protocol.md).
- BACKUP_PLAN.md and EAI_PLAN.md drafted as deferred future work.

---

## 3. Architecture

### 3.1 Three-server testing model
| Domain | Server type | Services |
|--------|-------------|----------|
| `mail1.test` | Traditional | Postfix + Dovecot, shared PostgreSQL backend |
| `mail2.test` | Traditional | Postfix + Dovecot, shared PostgreSQL backend |
| `mail3.test` | RESTMAIL | Go gateways → REST API → PostgreSQL |

All three share one `restmail` database, which holds `domains`, `mailboxes`, `aliases`, `messages`, `webmail_accounts`, `linked_accounts`, `quota_usage`, and ancillary tables.

### 3.2 Database schema (selected core tables)
- `domains` — name, `server_type` (traditional|restmail), active, default_quota_bytes
- `mailboxes` — domain_id, local_part, address, password `{BLF-CRYPT}`, display_name, quota_bytes, quota_used_bytes, active, last_login_at
- `aliases` — domain_id, source_address, destination_address, active
- `webmail_accounts` — primary_mailbox_id
- `linked_accounts` — webmail_account_id, mailbox_id
- `messages` — mailbox_id, folder, message_id, subject, body_text, body_html, sender, recipients_to/cc (JSONB), size_bytes, is_read / is_flagged / is_starred / is_draft / is_deleted, received_at, date_header, thread_id, in_reply_to, references
- `quota_usage` — mailbox_id, subject_bytes, body_bytes, attachment_bytes, total_bytes (GENERATED), message_count
- `attachments` — message_id, filename, content_type, size, checksum (indexed for dedup), storage_type, storage_ref, is_inline
- `outbound_queue` — sender, recipient, domain, raw_message, status, attempts, last_error, expires_at (72 h default), max_retries (30 default)
- `vacation_configs` — mailbox_id (unique), enabled, subject, body, start_date, end_date
- `bans` — ip, reason, protocol (smtp|imap|pop3|all), created_by, expires_at (NULL = permanent)

### 3.3 Auth
- Password hashing: bcrypt cost 10, stored with `{BLF-CRYPT}` prefix (Dovecot-compatible). Implementation at [internal/auth/auth.go](../internal/auth/auth.go).
- JWT: 15-min access token (memory only in browser), 7-day refresh token in HTTP-only `Secure SameSite=Strict` cookie. HS256 signed with `JWT_SECRET`. Claims: `sub`, `email`, `webmail_account_id`, `iss=restmail`, `iat`, `exp`, `capabilities[]`.
- Refresh flow: axios response interceptor in admin / webmail detects 401 → `POST /api/v1/auth/refresh` → retry request → logout on refresh failure.
- RBAC: JWT carries `capabilities[]`. Wildcard `*` = superadmin. Capabilities use `resource:action` format (e.g. `domains:write`, `users:delete`). Server enforces; client-side checks are UX only.

### 3.4 Web framework & runtime
- Go: chi router, zero external deps beyond stdlib for middleware (`func(http.Handler) http.Handler`).
- Logging: `log/slog` JSON to stdout only. External systems collect logs.
- DB pooling: `database/sql` with `DB_MAX_OPEN_CONNS` (default 25), `DB_MAX_IDLE_CONNS` (10), `DB_CONN_MAX_LIFETIME` (5 m). PgBouncer recommended for production HA.

### 3.5 Docker stack
Default `docker compose up` starts 9 core services: API, webmail, website, admin, SMTP/IMAP/POP3 gateways, Postfix+Dovecot for mail1/mail2, PostgreSQL (one instance per test domain), dnsmasq. Optional profiles:
- `scanning` — rspamd + ClamAV + clamav-rest proxy
- `monitoring` — Prometheus + Grafana + postgres-exporter
- `security` — fail2ban sidecar

All containers use dnsmasq (10.99.0.3) as resolver. Data persistence via named volumes: `postgres-data`, `mail1-maildir`, `mail2-maildir`, `mail3-attachments`, `certs`.

---

## 4. Implemented Features

### 4.1 REST API

API at `http://localhost:8080` (or `/api` through the reverse proxy). All endpoints under `/api/v1/`; server-to-server RESTMAIL endpoints under `/restmail/` (unversioned).

#### Auth / session
- `POST /api/v1/auth/login` — returns access token + sets refresh cookie
- `POST /api/v1/auth/logout` — revoke session
- `POST /api/v1/auth/refresh` — rotate access token

#### User-level mail (authenticated)
- `GET /api/v1/accounts` — list linked mailboxes
- `GET /api/v1/accounts/:id/folders`
- `GET /api/v1/accounts/:id/folders/:folder/messages` — cursor pagination
- `GET /api/v1/accounts/:id/folders/:folder/threads` — grouped by `thread_id`
- `GET /api/v1/accounts/:id/search?q=` — Postgres FTS
- `GET /api/v1/messages/:id` — full message + headers + attachments
- `POST /api/v1/accounts/:id/messages` — send
- `PATCH /api/v1/messages/:id` — read/unread, flag, star, move
- `DELETE /api/v1/messages/:id` — move to trash / hard delete
- `POST /api/v1/accounts/:id/drafts` — save draft
- `PUT /api/v1/messages/draft/:id` — update draft
- `POST /api/v1/messages/draft/:id/send` — send draft
- `DELETE /api/v1/drafts/:id`
- `GET /api/v1/messages/:id/attachments/:aid` — streamed download (with ownership check, see §4.2)
- `GET /api/v1/attachments/:id`
- `GET /api/v1/messages/:id/attachments`
- `GET /api/v1/accounts/:id/contacts` — auto-collected on send
- `POST /api/v1/accounts/:id/contacts` — CRUD
- `PATCH /api/v1/accounts/:id/contacts/:cid`
- `DELETE /api/v1/accounts/:id/contacts/:cid`
- `POST /api/v1/accounts/:id/contacts/block`
- `POST /api/v1/accounts/:id/contacts/import`
- `GET /api/v1/accounts/:id/events` — SSE stream (unidirectional). Events: `message.new`, `message.updated`, `message.deleted`, `message_sent`, `folder.updated`, `queue.delivered`, `queue.bounced`. Supports `Last-Event-ID` header for replay from ring buffer (64 events/mailbox).

#### Admin (capability-gated)
- Domains: `GET/POST /api/v1/admin/domains`, `GET/PATCH/DELETE /:id`, `GET /:id/dns` (required records), `POST /:id/verify`
- Mailboxes: standard CRUD + quota preload
- Aliases: standard CRUD
- Webmail accounts: list, create, link primary mailbox, delete
- Queue: list + filters (status, domain, sender, recipient, attempts), `GET /:id`, `POST /:id/retry`, `POST /:id/bounce`, `DELETE /:id`, plus bulk endpoints `POST /bulk-retry`, `POST /bulk-bounce`, `DELETE /bulk-delete` (accept `ids[]` up to 1000 or `filter{status, domain, sender, recipient}`)
- Pipelines: `GET /pipelines`, `GET/:domain/:direction`, `PUT /:domain/:direction` (upsert), `DELETE /:domain/:direction`, `POST /:domain/:direction/test`
- Custom filters: `GET/POST /filters`, `GET/PUT/DELETE /:id`, `POST /:id/test`, `GET /filters:builtin`, `GET /filters:builtin/:name`
- DKIM: `GET /dkim`, `GET/POST/DELETE /:domain`, `DELETE /:domain/:selector`, `GET /:domain/dns`
- Certificates: `GET /certs`, `GET/POST/DELETE /:domain`, `POST /:domain/provision` (ACME), `POST /:domain/renew`
- IP bans: `GET /bans?protocol=&active=`, `POST`, `DELETE /:id`, `DELETE /ip/:ip`
- Logs: `GET /logs` — structured / delivery / pipeline queries
- MTA-STS: `GET /domains/:id/mta-sts`, `PUT`, `DELETE`
- TLS-RPT: `GET /tls-reports?domain_id=&policy_type=&reporting_org=`
- Stats: `GET /stats` (dashboard)
- Admin-user management: **endpoints missing — see §5 Outstanding**

#### Server-to-server (RESTMAIL protocol, unauthenticated body verified by DKIM/SPF/DMARC)
- `GET /restmail/capabilities`
- `GET /restmail/mailboxes?address=` — recipient verification
- `POST /restmail/messages` — deliver

#### Health & test helpers
- `GET /api/health` — DB + queue + cert-expiry
- `GET /api/v1/health/servers`, `GET /api/v1/health/servers/:domain`
- `GET /api/v1/test/db/{domains,mailboxes,messages,queue}` — raw dumps (dev only)
- `POST /api/v1/test/send`, `GET /api/v1/test/mailbox/:address`
- `POST /api/v1/test/verify-delivery`
- `POST /api/v1/test/{smtp,imap,pop3}-probe/:domain`
- `GET /api/v1/test/dns/:domain`, `.../spf`, `.../dkim/:selector`, `.../dmarc`
- `POST /api/v1/test/reset`, `POST /api/v1/test/seed`, `POST /api/v1/test/snapshot`, `POST /api/v1/test/restore/:snapshot`
- `GET /metrics` — Prometheus

OpenAPI 3.1 spec: `/api/docs/openapi.yaml`; Swagger UI at `/api/docs`. 108 documented operations.

### 4.2 Pipeline engine & filters

Per-domain, per-direction (inbound / outbound) filter chains. Configured via `/api/v1/admin/pipelines`. Filter registry at [internal/pipeline/](../internal/pipeline/); built-in filter implementations at [internal/pipeline/filters/](../internal/pipeline/filters/).

Each filter returns one of: `continue`, `reject`, `quarantine`, `discard`, `defer`. SMTP gateway maps these to reply codes 250 / 550 / silent discard / 200 / 451 respectively.

#### Built-in filters (≥ 20)

| Filter | Type | Notes |
|--------|------|-------|
| `spf_check` | Check | SPF record validation |
| `dkim_verify` | Check | Verify incoming signatures |
| `dkim_sign` | Transform | Sign outbound with active DKIM key |
| `dmarc_check` | Check | DMARC policy enforcement |
| `arc_verify` | Check | ARC chain verification |
| `arc_seal` | Transform | ARC seal generation |
| `spam_score` | Check | Configurable thresholds |
| `rate_limit` | Check | Per-sender/domain |
| `size_limit` / `size_check` | Check | Message size ceiling |
| `attachment_check` | Check | Blocked file types |
| `greylist` | Check | DB-backed |
| `recipient_check` | Check | Verify recipient exists + quota |
| `sender_verify` | Check | Sender domain callback |
| `domain_allowlist` | Check | Domain-level allow/blocklist |
| `contact_whitelist` | Check | Per-user allowlist |
| `extract_attachments` | Action | Extract to filesystem, create `attachments` row, write `storage_ref` back to message |
| `vacation` | Action | DB-backed; reads `vacation_configs`, queues reply via `outbound_queue` with RFC 3834 headers: `Auto-Submitted: auto-replied`, `Precedence: bulk`, `In-Reply-To`, `References`, `X-Auto-Response-Suppress: All` |
| `sieve` | Action | Sieve script execution |
| `webhook` | Action | HTTP POST notification |
| `duplicate` | Action | Fork message to webhook/queue |
| `javascript` | Action | Runs in [docker/js-filter-sidecar](../docker/js-filter-sidecar/) (Node.js, `vm.runInNewContext` with whitelisted globals, `--max-old-space-size=64`) |
| `rspamd` | Adapter | HTTP POST to `rspamd:11333`, 5 s timeout, maps action enum |
| `clamav` | Adapter | HTTP POST to `clamav-rest:3000`, 30 s timeout, clean / infected |

DB-backed filters (greylist, vacation, domain_allowlist, contact_whitelist, recipient_check, sender_verify) are explicitly `pipeline.DefaultRegistry.Register()`'d in [internal/api/routes.go](../internal/api/routes.go) after their blank imports fire `init()`.

#### Pipeline DB context
Pipeline context carries `*gorm.DB` via `context.WithValue()`; filters access via `pipeline.DBFromContext()`. This lets filters query tables (e.g. `extract_attachments` checks the `attachments.checksum` index to dedup).

Adapter interface: `pipeline.ExternalAdapter` with `Name()`, `Scan()`, `Healthy()`. Write a custom adapter by implementing the interface and registering it.

Recommended order (inbound): `size_check → {spf,dkim,arc,dmarc}_check → domain_allowlist → rspamd → clamav → greylist → recipient_check → extract_attachments → sieve → vacation`.

### 4.3 Mail gateways

Implementations at [internal/gateway/smtp/](../internal/gateway/smtp/), [internal/gateway/imap/](../internal/gateway/imap/), [internal/gateway/pop3/](../internal/gateway/pop3/).

- SMTP: inbound (25), submission (587), implicit TLS (465). Wired for outbound queue — inserts into `outbound_queue` on non-local RCPT in `handleDATA` with `ExpiresAt: now+72h`, `MaxRetries: 30`.
- IMAP: 143 / 993. EXPUNGE tracks `\Deleted` per session and emits `* N EXPUNGE` in descending sequence order per RFC 3501. CREATE validates folder name (length, reserved names, special chars). SEARCH supports `ALL`, `UNSEEN`, `SEEN`, `FLAGGED`, `UNFLAGGED`, `FROM`/`TO`/`SUBJECT` substring, `SINCE`/`BEFORE`/`ON`, UID sets, `NOT`/`OR` combinators.
- POP3: 110 / 995. Basic STAT/LIST/RETR/DELE/QUIT.

#### Outbound queue worker
At [internal/gateway/queue/worker.go](../internal/gateway/queue/worker.go). Polls `outbound_queue`, attempts delivery, parses SMTP errors via `SMTPError{Code, Enhanced, Message}` + `parseSMTPError()`, and:
- 5xx permanent failure → immediately generate RFC 3464 bounce DSN (multipart/report: text/plain human-readable + message/delivery-status + text/rfc822-headers), insert bounce into queue back to original sender
- 4xx temporary → increment attempts, retry with exponential backoff until `ExpiresAt`

#### RESTMAIL protocol upgrade
- Outbound worker probes each recipient domain: if EHLO response contains `RESTMAIL https://...`, drops SMTP and delivers via `POST /restmail/messages` over HTTPS.
- Capability cache table with atomic upserts (`Clauses(clause.OnConflict{...})`) to avoid races between concurrent probes.

#### TLS / SNI
- Per-domain cert/key pairs via `TLS_CERT_DIR`. SNI-based lookup at handshake time.
- Hot reload via `fsnotify`: [internal/gateway/tlsutil/sni.go](../internal/gateway/tlsutil/sni.go) watches cert directory, invalidates cache entry on WRITE/CREATE. `StartWatching()` / `Stop()` lifecycle called from each gateway's main.go.

#### PROXY protocol
Enabled via `PROXY_PROTOCOL_TRUSTED_CIDRS` (comma-separated). Uses pires/go-proxyproto. Trusted connections: parse header, rewrite `RemoteAddr` to real client IP. Untrusted or missing header: ignored. Safe in mixed environments. SMTP/IMAP/POP3 gateways all support it. See [docs/proxy-protocol.md](proxy-protocol.md) for HAProxy / nginx examples.

#### Connection limiter (`internal/gateway/connlimiter/`)
Per-IP + global atomic counters. Per-IP auth-failure window with exponential backoff ban. Defaults: `MaxPerIP=20`, `MaxGlobal=1000`, `AuthMaxFails=5`, `AuthBanWindow=10m`, `AuthBanDuration=30m`. Methods: `Accept(ip)`, `Release(ip)`, `RecordAuthFail(ip)`, `ResetAuth(ip)`, `IsBanned(ip)`. Wired into each gateway's acceptLoop before goroutine spawn, plus auth handlers on success/failure.

Two-layer ban system: in-memory limiter (fast) + persistent `bans` table (durable). `bancheck.Wire()` attaches a DB-backed `BanChecker` function to the limiter. Admin API at `/api/v1/admin/bans` for manual management. Optional fail2ban sidecar watches gateway JSON logs for `"event":"smtp_auth_failed"` patterns and calls the ban API; enabled via the `security` compose profile. See [docs/fail2ban-setup.md](fail2ban-setup.md).

### 4.4 DNS providers

Pluggable `Provider` interface at [internal/dns/](../internal/dns/) with methods `EnsureRecords()`, `RemoveRecords()`, `VerifyRecords()`. Factory `NewProvider(name, opts...)` selects by `DNS_PROVIDER` env var.

| Provider | Value | Use case | Behavior |
|----------|-------|----------|----------|
| Manual | `manual` | Production with external DNS | Logs required records; no auto-apply |
| Dnsmasq | `dnsmasq` (default) | Docker Compose dev | Writes to dnsmasq config file (`address=`, `mx-host=`, `txt-record=`) |
| ExternalDNS | `externaldns` | Kubernetes with external-dns controller | Writes `DNSEndpoint` YAMLs to `/etc/externaldns/`; verify does live lookups |

`RequiredRecords(domain, ip)` generates the standard 4-record set: A, MX, TXT (SPF), TXT (DMARC). Custom providers implement the interface and register in the factory. See [docs/dns-providers.md](dns-providers.md).

### 4.5 Diagnostic tool: Instant Mail Check

Standalone CLI at [cmd/instantmailcheck/main.go](../cmd/instantmailcheck/main.go) — zero dependency on RESTMAIL API/DB, so it can audit any mail server. Raw TCP implementations of SMTP/IMAP/POP3 (not library-based) to control exact response codes and support intentional protocol misuse (e.g. SMTP smuggling tests). Uses `miekg/dns` for advanced DNS queries (TLSA, CAA, DNSSEC AD flag) that stdlib `net.Resolver` can't handle.

#### Tiers
- **Tier 1 (public probe)** — no credentials. 19 DNS checks (MX, SPF, DKIM with common selectors or `--dkim-selector`, DMARC, MTA-STS, TLS-RPT, PTR, DANE/TLSA, DNSSEC, CAA, BIMI, Forward-Confirmed rDNS, IPv6 readiness, client autoconfig), SMTP banner/STARTTLS/cert/submission/SMTPS/extensions, IMAPS/POP3S cert, open relay test, banner info leak, VRFY/EXPN, plaintext ports (110/143), TLS minimum version, self-signed cert, plaintext AUTH, auth mechanisms, DNSBL (12 major lists), domain blacklist (Spamhaus DBL, SURBL, URIBL).
- **Tier 2 (`--send-to`)** — unauthenticated SMTP delivery test via port 25.
- **Tier 3 (`--user` + `--pass`)** — password strength, IMAP capabilities / IDLE / quota (GETQUOTAROOT INBOX), IMAP + POP3 login, authenticated SMTP send (587), email round-trip (send + fetch + verify intact), header analysis (Authentication-Results, Received chain, DKIM sig, SPF alignment, spam score, ARC).
- **Tier 4 (`--security-audit`)** — user enumeration via RCPT TO and VRFY, brute-force protection (3 rapid bad AUTH), SMTP smuggling (CVE-2023-51766), connection rate limiting.

#### Scoring & exit codes
- Weighted checks: critical (10 pts) — open relay, blacklists, SPF, DKIM, DMARC, SMTP TLS cert, round-trip; important (5–8 pts) — STARTTLS, submission, PTR, DANE; nice-to-have (1–3 pts) — POP3S, PIPELINING, BIMI.
- Score = sum(passing weights) / sum(all weights) × 100. Warn = 50 % credit, Fail/Skip/Error = 0 %.
- Exit codes: `0` = pass, `1` = invalid args, `2` = score below `--threshold` (default 50 %).

#### Usage
```bash
./instantmailcheck example.com                                 # Tier 1
./instantmailcheck example.com --dkim-selector default
./instantmailcheck example.com --send-to test@example.com      # Tier 2
./instantmailcheck example.com --user x@example.com --pass s --send-to x@example.com --security-audit -v
./instantmailcheck example.com --json
./instantmailcheck example.com --markdown --output report.md
./instantmailcheck example.com --checks dns                    # filter category (dns|smtp|tls|imap|pop3|security|reputation|headers|roundtrip)
./instantmailcheck example.com --threshold 80                  # default 50
```

Build: `task build:instantmailcheck` (auto-detects OS/arch). Cross-compile: `task build:instantmailcheck:all`.

See [docs/INSTANT-MAIL-CHECK.md](INSTANT-MAIL-CHECK.md) for the complete reference (architecture, every check explained, scoring table, CI integration).

### 4.6 Webmail (React / Vite / Tailwind / shadcn)

Located at [webmail/](../webmail/). Features: rich text editor, contacts, drafts, threads, attachments, SSE live updates, vacation settings. Served on `:3000` (production build) or `:3001` (Vite HMR dev).

#### SSE reconnect strategy
[webmail/src/hooks/useSSE.ts](../webmail/src/hooks/useSSE.ts) — manual reconnect with exponential backoff (1 s → 2× → 30 s max, reset on successful open). Events handled: `message.new`, `message.updated`, `message.deleted`, `message_sent`, `folder.updated`.

### 4.7 Admin UI (React / TanStack Router / Zustand / Tailwind v4)

Located at [admin/](../admin/). Served on `:3002` or through `/admin` on the reverse proxy.

#### Tech stack (actual, not as PLAN.md originally described)
- TanStack Router (React 19), not TanStack Start
- Swiss Clean Design System, not shadcn/ui
- Zustand as primary state store; TanStack Query for fetch/cache invalidation
- React Hook Form + Zod validation
- Axios client with JWT refresh interceptor

#### Structure
```
admin/app/
├── routes/
│   ├── __root.tsx, index.tsx, login.tsx
│   ├── dashboard/index.tsx
│   ├── domains/{index,$id,new}.tsx
│   ├── mailboxes/{index,$id,new}.tsx
│   ├── aliases/{index,new}.tsx           (frontend planned, see §5)
│   ├── queue/{index,$id}.tsx             ✅ Stage 4 complete
│   ├── pipelines/...                     (see §5)
│   ├── admin-users/...                   (frontend done, backend missing — §5)
│   ├── settings/...                      (see §5)
│   └── logs/{activity,delivery}.tsx
├── components/ui, layout, auth, domains, mailboxes, queue, ...
├── lib/
│   ├── api/ (client.ts + per-resource modules)
│   ├── stores/ (authStore, domainStore, mailboxStore, aliasStore, queueStore,
│   │            pipelineStore, adminUserStore, uiStore, etc.)
│   ├── hooks/ (useAuth, useCapabilities, useApiMutation)
│   └── types/
└── router.tsx, ssr.tsx
```

#### Implemented features
- **Dashboard** — metric cards (domains, mailboxes, queue counts), Recharts 3.7.0 installed. *Partial: volume chart + recent activity feed need real data integration.*
- **Domains** — full CRUD. *Partial: DNS status check component is template-only.*
- **Mailboxes** — full CRUD, filter by domain, search, last login. *Partial: quota visualization basic, bulk ops not implemented.*
- **Queue** — complete (Stage 4, see §2 Changelog).

#### RBAC hook
`useCapabilities()` returns `hasCapability(cap)` and `hasAnyCapability([caps])`. Pattern: `{canCreate && <Button>New Domain</Button>}`. Superadmin `*` bypass. Server-side enforcement is authoritative.

### 4.8 Console admin tool

Terminal UI built with bubbletea at [cmd/console/main.go](../cmd/console/main.go). Features: inbox viewer, search, compose, live status, RBAC-aware capability display. Authenticates with seeded admin credentials (`admin` / `admin123!@`, superadmin). Build: `task build:console` (auto-detects platform) or `task build:console:all`. Run: `task console` or `task run:console`.

### 4.9 Metrics & monitoring

Prometheus metrics at `/metrics`. Grafana dashboards under [monitoring/](../monitoring/). Enable with `docker compose --profile monitoring up -d` (Prometheus at `:9090`, Grafana at `:3001`, postgres-exporter included).

### 4.10 E2E test suite

13-stage test suite under [tests/e2e/](../tests/e2e/) covering infrastructure through cross-domain round-trip delivery. Run with `task test:e2e` (requires stack up). `task test:all` combines unit + integration + e2e.

---

## 5. Outstanding Work

### 5.1 Critical blockers

#### Admin-user backend (`AdminUserHandler`) — blocks Stage 6 RBAC UI
Frontend is 100 % complete (`adminUserStore`, routes at `admin/app/routes/admin-users/`, forms), but the API returns 404 for all calls.

**To unblock, add to [internal/api/handlers/admin_user.go](../internal/api/handlers/admin_user.go) (new file):**
- `ListAdminUsers`, `GetAdminUser`, `CreateAdminUser`, `UpdateAdminUser`, `DeleteAdminUser`, `ListRoles`, `ListCapabilities`
- Response types include roles array; password hashing via `auth.HashPassword()`; role assignment via `repo.AssignRoles()`

**Routes to register in [internal/api/routes.go](../internal/api/routes.go):**
```
GET    /api/v1/admin/admin-users
POST   /api/v1/admin/admin-users
GET    /api/v1/admin/admin-users/{id}
PATCH  /api/v1/admin/admin-users/{id}
DELETE /api/v1/admin/admin-users/{id}
GET    /api/v1/admin/roles
GET    /api/v1/admin/capabilities
```

**Capability middleware** at [internal/api/middleware/capability.go](../internal/api/middleware/capability.go) (new):
```go
func RequireCapability(capability string) func(http.Handler) http.Handler
func RequireAnyCapability(capabilities ...string) func(http.Handler) http.Handler
```

Standard capability set: `users:read`, `users:write`, `users:delete`, plus the wildcard `*`.

Verify repo methods exist: `List()`, `GetByID()`, `Create()`, `Update()`, `UpdatePassword()`, `Delete()`, `GetRoles()`, `AssignRoles()`, `ListRoles()`, `ListCapabilities()`, `GetCapabilities()`.

Estimated effort: 2–3 days backend, 1–2 days frontend integration.

### 5.2 High-priority next

#### Stage 5 — Pipelines & Filters UI (frontend only; backend ready)
Backend APIs already work. Need:
- Stores: `pipelineStore`, `customFilterStore`, `filterRegistryStore` (static metadata for 20+ built-ins)
- Routes: `/pipelines/{,new,$id,$id/test,logs}`, `/custom-filters/{,new,$id,$id/test}`
- Visual filter builder (drag-drop) using `@dnd-kit/core` + `@dnd-kit/sortable`
- Code editor for JS filters using `@monaco-editor/react`
- Test panel with sample-email builder
- Logs viewer with filtering + pagination

Phased plan (7 days): Day 1 stores → Days 2–3 3-panel editor → Day 3 drag-drop → Day 4 Monaco → Day 5 test UI → Day 6 logs → Day 7 polish.

#### Stage 2 — DNS status component
- Backend: new endpoint `GET /api/v1/admin/domains/{id}/dns` with real-time lookups (MX, SPF, DKIM, DMARC, MTA-STS), 5 s timeout, status per record (valid / invalid / missing)
- Frontend: `DomainDNS.tsx` component — record table, status badges, refresh button, last-checked timestamp; integrate into `/domains/$id.tsx`
- Dashboard auto-refresh (30 s polling + Visibility API)
- Message-volume chart real data integration
- Recent-activity feed via activity-logs API

#### Stage 3 — Alias management + mailbox polish
- Entirely missing on frontend: `aliasStore`, routes `/aliases/{index,new,$id}`
- Backend alias endpoints already functional (verify at `/api/v1/admin/aliases`)
- Mailbox detail: `QuotaBreakdown.tsx` (stacked bar: attachments / body / metadata), `PasswordStrength.tsx`
- `BulkImport.tsx` (CSV mailbox import), `BulkQuotaUpdate.tsx`
- Backend: `Preload("QuotaUsage")` in mailbox list/get handlers

#### Stage 7 — Settings (5 subsections; frontend only)
All subsections share the pattern: new Zustand store + route + form.

1. **DKIM** (`/settings/dkim`) — `dkimStore.ts`; list per domain, create/update keys, view public key for DNS, delete. Client-side RSA generation via Web Crypto API or node-forge.
2. **Certificates** (`/settings/certificates`) — `certificateStore.ts`; upload PEM, `getDaysUntilExpiry()` + `getExpiryStatus()` (red < 7 d, orange < 30 d, green ≥ 30 d), delete.
3. **IP bans** (`/settings/bans`) — `banStore.ts`; add ban with duration parser (`"24h"`, `"7d"` → Go duration), filter by protocol + active flag, gray out expired.
4. **TLS-RPT reports** (`/settings/tls-reports`) — `tlsReportStore.ts`; filter by domain/org, expand to see `failure_details` JSONB.
5. **MTA-STS** (`/settings/mta-sts`) — `mtastsStore.ts`; domain selector, mode (none / testing / enforce), MX hosts, max-age presets (1d/7d/30d), `generatePolicyPreview()` for `.well-known/mta-sts.txt`.

Estimated effort: 4–5 days.

#### Stage 8 — Polish & testing (10–14 days)
- Global `ErrorBoundary.tsx`, `parseApiError` utility, form-error mapping
- Skeleton components (`Skeleton`, `TableSkeleton`, `CardSkeleton`, `FormSkeleton`); button-loading states; optimistic updates
- Zod schemas for every form; integrate with React Hook Form via `zodResolver`
- Global toast system: `useToastStore` + `ToastContainer` + animations (currently only local to admin queue page)
- Responsive (sm/md/lg/xl breakpoints); test iPhone SE, iPad, 1920px
- WCAG 2.1 AA accessibility audit (semantic HTML, heading hierarchy, ARIA labels, focus management, 4.5:1 contrast, keyboard nav)
- Playwright E2E: `playwright.config.ts`, auth/domain/mailbox/queue/admin-user specs, fixtures, setup/teardown
- Performance: bundle < 500 KB gzipped, route-based code splitting (TanStack Router), `React.lazy` + Suspense, image optimization (AVIF + srcSet), Web Vitals tracking
- Security: CSP headers, DOMPurify if needed, CSRF token in header, 429 handling, secure-cookie settings
- Documentation: `USER_GUIDE.md`, `API_INTEGRATION.md`, `DEPLOYMENT.md`

### 5.3 Feb-2026 plan items — status uncertain, need code verification before treating as "done"

The following were drafted as design + implementation plans on 2026-02-17 and 2026-02-18. Some may have landed since; a few are probably still backlog. Each should be verified in code before being moved to §4.

#### 2026-02-17 bugfixes — 7 integration gaps
1. Pipeline execution in `DeliverMessage` (before DB insert) — [internal/api/handlers/messages.go](../internal/api/handlers/messages.go), [internal/api/routes.go](../internal/api/routes.go)
2. Explicit registration of DB-backed filters in `routes.go`
3. SMTP outbound queue writes in gateway — [internal/gateway/smtp/](../internal/gateway/smtp/)
4. `SendMessage` completion — serialize MIME, populate `RawMessage` + `MessageID` on queue entry
5. IMAP EXPUNGE (descending sequence, RFC 3501 compliant)
6. IMAP CREATE validation
7. IMAP SEARCH (ALL/UNSEEN/SEEN/FLAGGED/UNFLAGGED/FROM/TO/SUBJECT/SINCE/BEFORE/ON/UID sets, NOT/OR combinators)

#### 2026-02-17 endpoint wiring — 5 tasks
1. Wire existing attachment handlers (`GET /attachments/:id`, `GET /messages/:id/attachments`)
2. Contacts CRUD routes (6 endpoints)
3. Draft save / update / send (reuse `Message` model with `is_draft` flag; add `resolveSenderMailbox` helper)
4. Thread retrieval endpoint (`WHERE thread_id = ?`)
5. SSE `message_updated`, `message_deleted` event publishing

#### Batch 2 — attachments, quota, connlimiter, fail2ban (13 tasks)
- Attachment ownership check — JOIN `attachments → messages → mailboxes`, compare `mailbox.account_id`; return **404** (not 403) to avoid leaking existence. Both `GetAttachment` and `ListAttachments`.
- Checksum dedup — `SELECT storage_ref FROM attachments WHERE checksum = ? AND storage_type = ?`; add `idx_attachments_checksum`.
- Quota accounting — include attachment bytes in `quota_used_bytes`; `GREATEST(quota_used_bytes - ?, 0)` on delete to prevent underflow; check before `DeliverMessage` (422 if full); skip Sent folder on SendMessage quota overflow.
- Connection limiter package at `internal/gateway/connlimiter/` — structure exists in plan, verify in code.
- fail2ban sidecar — `docker/fail2ban/` Dockerfile + `jail.local` (3 jails) + filter.d regexes. Activated via `profile: security`.
- E2E tests — `stage3_gateway_inbound_test.go`, `stage4_gateway_outbound_test.go`: `TestIMAPGetQuota`, `TestAttachmentDownload`, `TestQuotaEnforcement`.

#### Batch 3 — contacts, vacation, queue bulk, SSE (8 tasks)
- Contact auto-populate on `SendMessage` via `FirstOrCreate` with unique `(mailbox_id, email)` index, `source="sent"`, `trust_level="auto"`.
- `VacationConfig` model migration — move from handler to [internal/db/models/pipeline.go](../internal/db/models/pipeline.go), add to central AutoMigrate.
- Vacation handler ownership check — `resolveMailboxID()` via `middleware.GetClaims()` on Get/Set/Disable.
- Vacation filter rewrite — read DB; queue actual reply via `outbound_queue` with RFC 3834 headers.
- Queue bulk ops — `POST /admin/queue/{bulk-retry,bulk-bounce}`, `DELETE /admin/queue/bulk-delete`; `bulkQueueRequest{ids[], filter{status, domain}}`; max 1000 IDs; `buildBulkQuery()` helper.
- SSE event IDs + ring buffer — per-mailbox counter, 64-event ring buffer, `SubscribeWithReplay()` reads `Last-Event-ID`.
- React SSE reconnect with exponential backoff — already in [webmail/src/hooks/useSSE.ts](../webmail/src/hooks/useSSE.ts); verify wiring.
- SSE `message_sent` event handler in [webmail/src/App.tsx](../webmail/src/App.tsx).

#### Critical Items — 9 integration fixes (14 tasks)
- `SMTPError` type + `parseSMTPError()` + `IsPermanent()` for 5xx-immediate bounce logic.
- RFC 3464 bounce generation (multipart/report with text/plain + message/delivery-status + text/rfc822-headers).
- `Clauses(clause.OnConflict{...})` atomic upsert for RESTMAIL capability cache.
- SMTP queue `ExpiresAt` + `MaxRetries` population in `handleDATA`.
- SMTP reply-code mapping: APIError 403 → 550, 503 → 451, else 451.
- `parseRawMessage()` extracts `To`, `Cc`, `InReplyTo`, `References` (space-split) plus `extractEmailFromHeader()` helper.
- SNI hot reload via fsnotify — `StartWatching()`/`Stop()`/`Invalidate()` on [internal/gateway/tlsutil/sni.go](../internal/gateway/tlsutil/sni.go).
- SSE `message_sent` after send; `folder_update` with unread_count after delivery/move.
- Draft `InReplyTo`/`References` propagation into `SendDraft`.
- Sent-message `threadID` = `InReplyTo` (or own MessageID); delivered-message thread root = first `References` entry, fallback to `InReplyTo`, fallback to own ID.
- Attachment DB rows — iterate `FinalEmail.Attachments` + `.Inline` after pipeline runs; create `models.Attachment` for each with storage_type / storage_ref / checksum.
- JS filter sidecar: replace `vm2` (CVE-heavy) with `vm.runInNewContext`, whitelist safe globals, `--max-old-space-size=64`.

#### Webmail themes + settings (9 tasks)
- Six palette classes in [webmail/src/index.css](../webmail/src/index.css): `dawn`, `linen`, `slate`, `dusk`, `midnight`, `forest` using `oklch()` variables.
- Theme type expanded in [webmail/src/stores/uiStore.ts](../webmail/src/stores/uiStore.ts) with `applyTheme()` that strips other classes + adds `.dark` for dark themes.
- New `settingsStore` — reading pane (`bottom` default / `right` / `off`), density (`comfortable` default / `compact`), auto-save drafts, desktop notifications, new-mail sound; persist to localStorage.
- TopBar rewrite — avatar + ChevronDown trigger, theme submenu grouped Light / Mid / Dark with swatch chips, Settings menu item.
- Sidebar gear icon on hover — click → `setSelectedAccountId` + `setView('accountSettings')`.
- `SettingsView` — 3 tabs (General / Accounts / Notifications).
- `AccountSettingsView` — 3 tabs (Details / Vacation / Danger Zone). Danger Zone disabled for primary account.
- `App.tsx` — new views `settings`, `accountSettings`; pass `readingPane`/`density` down to `MailView`.
- Density variant in `MessageList` (conditional row-padding class).

### 5.4 Future / deferred

#### Backup & restore (BACKUP_PLAN.md)
Design complete, nothing implemented. Targets:
- **Simple (dev / single-instance):** daily `pg_dump -Fc`, 7-day retention, backup container in docker-compose.
- **Production:** WAL archiving + pgBackRest or WAL-G; base backup weekly, continuous WAL to S3/GCS/Azure Blob, `archive_timeout = 60`. Near-zero RPO.
- Attachments: tar snapshot daily or S3 versioning.
- MASTER_KEY backup procedure — **critical; losing the key means losing access to every encrypted TLS/DKIM private key**. Not yet documented.
- Weekly restore verification to a temp env with schema validation + attachment reference sampling + Prometheus alert.
- Tasks to add: `task backup:db`, `task backup:attachments`, `task restore:db`, `task restore:attachments`.
- Monitoring: backup-missed critical (> 25 h gap), WAL lag warning (> 5 min), verification-failed critical, storage-full warning (< 20 % free), MASTER_KEY-not-set critical at API start.
- Open questions: storage target? Acceptable RPO? Backup encryption? Cross-region replication?

#### Internationalized Email (EAI_PLAN.md)
Four phases, none implemented. Only SMTPUTF8 capability detection exists in [internal/mailcheck/smtp.go](../internal/mailcheck/smtp.go).
- **Phase A — IDN domains** (lowest risk): `golang.org/x/net/idna`, store Unicode, convert to Punycode for DNS/SMTP.
- **Phase B — SMTPUTF8 gateway** (medium risk): advertise capability, accept UTF-8 in MAIL FROM / RCPT TO / DATA, MIME parser RFC 6532 update, outbound capability detection + RFC 6857 message downgrading.
- **Phase C — IMAP/POP3**: advertise `UTF8=ACCEPT` (IMAP) and `UTF8` (POP3), return raw UTF-8 headers when enabled, UTF-8 folder names.
- **Phase D — full-stack validation**: API regex update, Unicode normalization (NFC), EAI test cases, interop testing with Gmail / Outlook.com.
- Relevant RFCs: 6530 (overview), 6531 (SMTPUTF8), 6532 (headers), 6533 (DSN), 6855 (IMAP), 6856 (POP3), 6857 (downgrade), 5891 (IDNA 2008).
- Risk: most servers don't support SMTPUTF8 → bounce non-ASCII local parts where remote can't accept.
- Database already UTF-8; consider ICU collation for sorting, `pg_trgm`/ICU-aware `LOWER()` indexes.

#### Phase-6+ items from original PLAN.md
- Cross-domain delivery verification (user@mail1.test → user@mail2.test)
- `externaldns` adapter for k8s
- Periodic quota reconciliation task (fix drift)
- Certificate management: Let's Encrypt full integration (only self-signed for `.test` exists today)
- OpenAPI → TypeScript type generation pipeline
- Log query endpoints (partial — some exist, full delivery + pipeline log queries incomplete)

---

## 6. Configuration reference

### 6.1 Core env vars
| Variable | Default | Purpose |
|----------|---------|---------|
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | |
| `DB_NAME` | `restmail` | |
| `DB_USER` | `restmail` | |
| `DB_PASS` | `restmail` | |
| `DB_MAX_OPEN_CONNS` | `25` | |
| `DB_MAX_IDLE_CONNS` | `10` | |
| `DB_CONN_MAX_LIFETIME` | `5m` | |
| `API_PORT` | `8080` | |
| `API_HOST` | `0.0.0.0` | |
| `JWT_SECRET` | `dev-secret-change-in-production` | **Required in prod** |
| `MASTER_KEY` | *(empty)* | AES-256-GCM key for encrypting private keys at rest. **Required in prod; losing it means losing all DKIM/TLS private keys.** |
| `ENVIRONMENT` | `development` | `development`, `production`, `test` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

### 6.2 TLS / certs
| Variable | Default | Purpose |
|----------|---------|---------|
| `TLS_CERT_PATH` | *(empty)* | Single cert file |
| `TLS_KEY_PATH` | *(empty)* | Single key file |
| `TLS_CERT_DIR` | *(empty)* | Per-domain cert/key directory for SNI |
| `ACME_ENABLED` | `false` | Let's Encrypt auto-renewal |
| `ACME_EMAIL` | *(empty)* | Contact email |
| `ACME_STAGING` | `false` | Use staging directory |

### 6.3 Gateways
| Variable | Default | Purpose |
|----------|---------|---------|
| `GATEWAY_HOSTNAME` | `mail3.test` | Hostname announced by gateways |
| `API_BASE_URL` | `http://localhost:8080` | Internal API URL |
| `SMTP_PORT_INBOUND` | `25` | |
| `SMTP_PORT_SUBMISSION` | `587` | |
| `SMTP_PORT_SUBMISSION_TLS` | `465` | |
| `IMAP_PORT` | `143` | |
| `IMAP_TLS_PORT` | `993` | |
| `POP3_PORT` | `110` | |
| `POP3_TLS_PORT` | `995` | |
| `QUEUE_WORKERS` | `4` | |
| `QUEUE_POLL_INTERVAL` | `5s` | |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:3000` | Comma-separated |
| `PROXY_PROTOCOL_TRUSTED_CIDRS` | *(empty)* | Comma-separated CIDRs; empty disables |
| `DNS_PROVIDER` | `dnsmasq` | `dnsmasq`, `externaldns`, `manual`, `cloudflare`, `route53` |

### 6.4 Connection limiter defaults
| Variable | Default | Purpose |
|----------|---------|---------|
| `CONNLIMIT_MAX_PER_IP` | `20` | Concurrent conns per source IP |
| `CONNLIMIT_MAX_GLOBAL` | `1000` | Total concurrent conns per gateway |
| `CONNLIMIT_AUTH_MAX_FAILS` | `5` | Auth failures before ban |
| `CONNLIMIT_AUTH_BAN_WINDOW` | `10m` | Rolling window for failure count |
| `CONNLIMIT_AUTH_BAN_DURATION` | `30m` | Ban duration |

### 6.5 Admin / webmail (Vite)
| Variable | Default | Purpose |
|----------|---------|---------|
| `VITE_API_URL` | `http://localhost:3000/api/v1` | Admin UI API base |
| `VITE_API_BASE_URL` | `http://localhost:3000` | Some admin calls |

---

## 7. Operational playbooks

### 7.1 Bringing up the stack
```bash
task dev                 # full hot-reload dev env (air + Vite)
task up                  # same as dev, without hot-reload
task down                # stop + remove containers
task reset               # full teardown + prune stale names
task reset:hard          # teardown + volumes (clean slate)
task db:reset            # migrate + seed (admin/admin123!@ + mail3.test accounts)
task logs                # tail all container logs
```

Access: website `http://localhost/`, webmail `/webmail`, API `/api`, API docs `/api/docs`. Direct (bypass reverse proxy): webmail `http://localhost:3001` (Vite HMR), API `http://localhost:8080`.

### 7.2 Building
```bash
task build                          # everything
task build:api
task build:gateways
task build:console                  # auto-detects OS/arch
task build:instantmailcheck
task build:instantmailcheck:all     # cross-compile all platforms
task build:tools                    # certgen, migrate, seed, website, instantmailcheck
```

### 7.3 Testing
```bash
task test                # unit
task test:coverage
task test:e2e            # requires stack up
task test:all
task fmt / vet / lint / tidy
```

### 7.4 PROXY protocol load-balancer examples

**HAProxy:**
```
backend smtp
    server smtp1 10.99.0.13:25 send-proxy-v2
```

**nginx stream:**
```
upstream smtp {
    server 10.99.0.13:25;
}
server {
    listen 25;
    proxy_protocol on;
    proxy_pass smtp;
}
```

**Compose override (HAProxy at 10.99.0.30):**
```yaml
services:
  smtp-gateway:
    environment:
      PROXY_PROTOCOL_TRUSTED_CIDRS: "10.99.0.30/32"
```

Use `/32` CIDRs; never `0.0.0.0/0`. Firewall gateway ports so only the proxy can reach them. Prefer PROXY v2.

### 7.5 Enabling fail2ban
`docker compose --profile security up -d` starts the sidecar. It watches gateway JSON logs for `"event":"smtp_auth_failed"` (and IMAP/POP3 equivalents) and calls `POST /api/v1/admin/bans` with a duration. Default jails: 3 (SMTP / IMAP / POP3), 5 retries, 30 m ban.

Recommended ban durations: SMTP brute (5 fails) = 1 h; IMAP brute (10 fails) = 30 m; known spam = 720 h; persistent abuser = permanent.

### 7.6 Enabling scanning
`docker compose --profile scanning up -d` starts rspamd (11333) and ClamAV REST proxy (3000). Configure filters on relevant pipelines:
```json
{"name": "rspamd", "type": "action", "enabled": true,
 "config": {"url": "http://rspamd:11333", "timeout_ms": 5000, "fallback_action": "continue"}}
{"name": "clamav", "type": "action", "enabled": true,
 "config": {"url": "http://clamav-rest:3000", "timeout_ms": 30000, "fallback_action": "continue"}}
```

### 7.7 RESTMAIL inter-server protocol
When outbound worker probes recipient MX and sees `RESTMAIL https://restmail.example.com/` in EHLO, it upgrades:
1. `GET /restmail/capabilities` — confirm support + version
2. `GET /restmail/mailboxes?address=user@example.com` — recipient verification
3. `POST /restmail/messages` — deliver (body verified at receiver by DKIM/SPF/DMARC)

Capability cache (`restmail_capabilities` table) avoids re-probing; atomic upsert via `Clauses(clause.OnConflict{...})`. Probes expire after TTL to re-check for disabled endpoints.

### 7.8 Test accounts (seeded by `task db:seed`)
**Admin:** `admin` / `admin123!@` — role `superadmin`, wildcard `*` capability.

**mail3.test mailboxes** (all password `password123`):
- `eve@mail3.test` (Eve Wilson)
- `frank@mail3.test` (Frank Miller)
- `postmaster@mail3.test`

**Aliases on mail3.test:**
- `info@mail3.test → eve@mail3.test`
- `admin@mail3.test → eve@mail3.test`

---

## 8. Test domain IPs (dev stack on `10.99.0.0/16`)

| Service | IP |
|---------|-----|
| dnsmasq | `10.99.0.3` |
| api | `10.99.0.20` |
| postfix-mail1 | `10.99.0.11` |
| postfix-mail2 | `10.99.0.12` |
| mail3.test (gateway) A record | `10.99.0.13` |
| dovecot-mail1 | `10.99.0.14` |
| smtp-gateway | `10.99.0.13` (primary) |
| imap-gateway | `10.99.0.15` |
| pop3-gateway | `10.99.0.16` |
| dovecot-mail2 | `10.99.0.17` |
| postgres-mail1 | `10.99.0.41` |
| postgres-mail2 | `10.99.0.42` |
| postgres-mail3 | `10.99.0.43` |
| webmail | `10.99.0.22` |
| website | `10.99.0.23` |
| certgen | `10.99.0.99` |

SPF records in [docker/dnsmasq/dnsmasq.conf](../docker/dnsmasq/dnsmasq.conf) publish literal IPs per-domain.

---

## 9. Reference appendix — original docs mapping

For historical reference, source documents were:

| Current section | Source file(s) |
|-----------------|----------------|
| §1 Overview, §2 Changelog, §3 Architecture | PLAN.md, ADMIN_PLAN.md |
| §4.1 REST API | PLAN.md, ADMIN_PLAN.md, 2026-02-17-wire-endpoints.md |
| §4.2 Pipeline | PLAN.md, adapter-filters.md, 2026-02-17-bugfixes-wiring-design.md |
| §4.3 Gateways | PLAN.md, 2026-02-17-bugfixes, 2026-02-18-batch2, 2026-02-18-critical-items |
| §4.3 PROXY protocol | proxy-protocol.md |
| §4.3 Connection limiter / fail2ban | 2026-02-18-batch2-design/implementation, fail2ban-setup.md |
| §4.4 DNS providers | dns-providers.md |
| §4.5 Instant Mail Check | INSTANT-MAIL-CHECK.md, 2026-02-22-instant-mail-check.md |
| §4.6 Webmail | 2026-02-18-webmail-themes-settings-design/implementation |
| §4.7 Admin UI | ADMIN_PLAN.md, STAGE_2..STAGE_8 |
| §4.10 E2E | PLAN.md |
| §5.1 Blockers (admin-user backend) | STAGE_6_ADMIN_USERS_RBAC.md, ADMIN_PLAN.md |
| §5.2 Stage 5 | STAGE_5_PIPELINES_FILTERS.md |
| §5.2 Stage 2 | STAGE_2_DASHBOARD_DOMAINS.md |
| §5.2 Stage 3 | STAGE_3_MAILBOXES_ALIASES.md |
| §5.2 Stage 7 | STAGE_7_SETTINGS.md |
| §5.2 Stage 8 | STAGE_8_POLISH_TESTING.md |
| §5.3 Feb-2026 plans | 2026-02-17-*, 2026-02-18-* |
| §5.4 Backup | BACKUP_PLAN.md |
| §5.4 EAI | EAI_PLAN.md |
| Stage 4 complete (§2 Changelog) | STAGE_4_CHECKLIST.md, STAGE_4_FEATURES.md, STAGE_4_IMPLEMENTATION_COMPLETE.md, STAGE_4_SUMMARY.md, STAGE_4_TESTING_GUIDE.md, STAGE_4_QUEUE_MANAGEMENT.md |

All of the above now live at `docs/plans/archive/` (see §10).

---

## 10. Document maintenance

- This manual supersedes the archived plan documents; update it when a feature lands or a gap is identified.
- When a Feb-2026 design doc item (§5.3) is verified in code, move it into §4 with concrete file paths.
- When Stage 6 admin-user backend lands, move it from §5.1 to §4.1 and §4.7.
- `docs/INSTANT-MAIL-CHECK.md`, `docs/adapter-filters.md`, `docs/dns-providers.md`, `docs/fail2ban-setup.md`, `docs/proxy-protocol.md` remain as-is — they are operational guides summarized in §4 but still useful at full length.
