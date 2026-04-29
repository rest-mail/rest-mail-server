# 06 — Operations Gaps

What you need to keep this running for paying customers.

## 1. Observability

### 1.1 Metrics

`internal/metrics/` exists. The `monitoring/` setup ships Prometheus + Grafana + postgres-exporter. Solid.

**Gaps:**

- **Per-domain metrics**: messages received/sent/queued/failed should be labelled by domain. Otherwise "deliveries dropped" is a global signal that doesn't tell you which customer is in trouble.
- **Histograms not counters**: SMTP transaction duration, IMAP command latency — should be Prometheus histograms with sensible buckets, not gauges or counters.
- **Pipeline filter timing**: each filter should report its own duration. When the JS sandbox filter starts taking 5s/message, you want to see it.
- **Queue depth and age**: a "queue is 50,000 deep, oldest is 6 hours old" alert has saved more reputations than any other metric.
- **Connection pool saturation**: postgres pool, API client pool, etc.

### 1.2 Logs

I see `slog` usage and structured logging. **Gaps:**

- **Correlation IDs**: an inbound SMTP transaction → message → pipeline → storage → SSE event to webmail should share a trace id. Gateway logs and API logs should be joinable.
- **Log levels**: confirm prod runs at `info`, not `debug` (a previous greg reveals dev defaults of `MAIL3_LOG_LEVEL: debug`).
- **PII redaction**: log statements that include user emails, message subjects, recipient lists need a redactor, especially in environments where logs flow to a third-party service.

### 1.3 Tracing

I don't see OpenTelemetry instrumentation. For a system with 3 protocol gateways + REST API + DB + multiple sidecar processes, distributed tracing is the difference between "I think the bottleneck is in the API" and "the bottleneck is in the JS-filter sidecar's HTTP call which is timing out because the DNS lookup is slow."

OpenTelemetry SDK in each Go binary, OTLP exporter to Tempo or Jaeger. ~2 days to instrument the hot paths.

### 1.4 Alerts

The `monitoring/alerting-rules.yml` file exists. Verify it covers:

- Queue depth > 1000 messages
- Queue oldest > 1 hour
- Postgres connection failures
- Pipeline filter error rate > 1%
- API 5xx rate > 1%
- IMAP/SMTP connection rate suddenly drops to 0 (a service health check)
- Disk usage > 80% for the postgres volume, attachments volume, certs volume
- Cert expiry < 14 days (for everything in the certs volume)
- DKIM key age > rotation period

## 2. Runbooks

I see no `docs/runbooks/`. Symptoms that need runbooks:

### 2.1 "Mail is queueing but not delivering"

Diagnostics:
1. `task queue:status` (doesn't exist; should) — show queue depth, oldest entry, distinct destinations
2. `task queue:retry-one --id=<id>` — manually trigger one retry to see the actual error
3. `task queue:purge --hard-bounce-only` — clear the queue of definitively-bad sends
4. Network: can the gateway resolve and connect to the recipient's MX from inside the container?

### 2.2 "We're blocklisted on Spamhaus / Spamcop / Barracuda"

1. Identify the listing: which IP, which list, why
2. Identify the cause: which customer / message pattern
3. Suspend the bad sender: `task customer:suspend DOMAIN=<x> REASON=blocklist`
4. Submit delisting request (each blocklist has a different process)
5. Watch for re-listing

### 2.3 "Postgres volume is corrupted / lost"

1. Stop the API and gateways (they'll start retrying connections, that's fine)
2. Restore from the last backup
3. Replay the queue (the postgres has a `queue` table; you may have lost in-flight sends, that's life)
4. Verify by sending test mail from each domain
5. Resume customer traffic

A backup-and-restore runbook needs `task backup:create` and `task backup:restore` tasks. Currently neither exists.

### 2.4 "Certs are expiring tomorrow"

1. `task certs:list` — shows expiry per cert
2. `task certs:renew --domain=<x>` — force-renews via ACME
3. After renew, the gateways need to reload the cert from disk (do they?) — verify

### 2.5 "API is slow"

1. Postgres: `task db:slow-queries` (should exist; doesn't)
2. Pipeline: which filter is the bottleneck (per-filter histograms — see 1.1)
3. SSE: how many open connections are there? (file descriptor count)

### 2.6 "Spam in customer's inbox"

1. Look at the message
2. Look at the pipeline output for the message — what did each filter score it as?
3. Adjust the filter for that domain (raise threshold, add to blocklist, etc.)
4. Re-test by sending a similar message

A "view the pipeline trace" feature in the admin UI would be huge. Today this is `psql` work.

### 2.7 "Customer is reporting bouncing mail"

1. Find the message in the queue (`/api/queue?from=<addr>` or similar)
2. View the bounce category and DSN text
3. If hard-bounce: address is invalid, customer needs to fix
4. If soft-bounce: temporary issue, will retry; tell them when retry is scheduled
5. If reputation block: investigate broader issue (see 2.2)

## 3. Disaster recovery

### 3.1 Backups

What needs backing up:
- Postgres (master_key-encrypted DKIM keys, all messages metadata, queue state)
- Attachments volume (if attachments aren't in postgres — verify; large attachments usually live on disk)
- Certs volume (CA + per-domain certs)
- Master key (out-of-band, in a key vault)
- DKIM private keys (already encrypted in postgres if S3 above is correct)

What probably exists today:
- A docker volume per data thing
- No backup automation

What's needed:
- Daily `pg_dump` to S3 (or equivalent), encrypted with a separate key
- Daily attachment-volume snapshot to S3
- Weekly full-system backup
- Monthly restore drill (it's not a backup until you've restored from it)

### 3.2 Recovery time / point objectives

For each customer:
- RTO (recovery time objective): "how long can mail be down" → for v1, document "up to 4 hours" and design backups to that
- RPO (recovery point objective): "how much data can we afford to lose" → "up to 5 minutes" requires WAL streaming; "up to 24 hours" requires daily snapshots

### 3.3 Multi-region not on the roadmap (yet)

For early-stage product, single-region is fine. The architectural decision worth documenting: "we currently don't support multi-region. Latency-sensitive deployments should pin themselves to a single region."

## 4. Performance / capacity

### 4.1 Load testing

I don't see load tests. Some pragmatic options:

- **Smtp-source / smtp-sink** (postfix's built-in tools) for SMTP load
- **k6** or **vegeta** for the REST API
- **A custom IMAP load tool** since IMAP is stateful and harder to load-test

Targets: in dev, 100 msg/sec inbound, 50 msg/sec outbound, 1000 concurrent IMAP connections, 100 RPS API. Numbers should match your scale ambitions.

### 4.2 Capacity planning

Per-mailbox storage growth: median user gets ~1MB/day of mail. 1000 users × 1MB × 365 = 365GB/year. That's an assumption; measure your actuals.

Per-domain message rate: limits start at "around what your IP can sustain to a single receiving MX without rate-limit." Gmail, Outlook, Yahoo will rate-limit.

### 4.3 Scaling the queue worker

`internal/gateway/queue/worker.go` is in-process with the SMTP gateway. If you separate them:
- Queue worker becomes a standalone process (own container, own scaling)
- The gateway just enqueues
- The worker pulls from queue, attempts delivery, re-enqueues with backoff

Multiple queue workers behind a postgres lock (FOR UPDATE SKIP LOCKED) is the standard pattern.

### 4.4 Read scaling

For high-read workloads (many IMAP clients on one mailbox), a read replica of postgres helps. The IMAP gateway gets a separate `DB_HOST_READ` and uses it for SELECTs. Not on the immediate roadmap; capacity-watch first.

## 5. Cost

### 5.1 What costs what

- **Postgres storage**: monotonic-growing. Old messages either go to "cold storage" (S3 or similar) or get deleted by retention policies.
- **Attachments**: bigger than message bodies; the attachment storage strategy needs a path off the hot DB.
- **Outbound SMTP**: most providers free; some networks meter egress.
- **Inbound storage**: same as message storage.
- **Logs**: surprisingly expensive at scale. Log retention policy + sampling.

### 5.2 The "free vs paid relay" cost question

Direct SMTP outbound: $0 + your reputation work
Mailgun / SendGrid: $1-10/1000 messages, no reputation work

For 100k msg/day: direct is $0; relay is $100-1000/month. For most small operators, direct + reputation work pays back.

### 5.3 Per-customer billing readiness

The activity_log + per-domain metrics give you the raw data. What's missing:
- Aggregation pipeline that computes "this domain sent N messages this billing period"
- Stripe integration (or equivalent) — that's its own product surface

## 6. Customer-facing operations

### 6.1 Self-service domain verification

When a customer adds a domain (acme.example), they need to prove ownership. Two options:

- **TXT record verification**: "add `restmail-verify=<token>` to DNS, then click verify"
- **DNS configuration wizard**: show the customer the records to add, link to common provider docs (Cloudflare, Route 53, GoDaddy)

Both are missing. For a self-service product, both are required.

### 6.2 Self-service mailbox creation

Once a domain is verified, the customer creates mailboxes via the admin UI. Limits:
- Max mailboxes per plan
- Max storage per mailbox
- Max storage per domain

`internal/db/models/quota.go` is partly this. Need the API + UI to expose it cleanly.

### 6.3 Status page

A public status page (status.acme.example or similar) that shows:
- API: operational / degraded / down
- SMTP / IMAP / POP3: operational / degraded / down
- DKIM signing: operational / degraded
- Queue depth (publicly visible? maybe just "normal / elevated / high")

Pre-built: Cachet, Statuspage, Atlassian Statuspage, Better Uptime, etc. Choose, don't build.

### 6.4 Documentation

For customers, you'll need:
- "Setting up your first domain"
- "Connecting your IMAP client"
- "Troubleshooting bouncing mail"
- "Setting up DKIM/SPF/DMARC"
- "Why is mail going to spam?"

The `docs/MANUAL.md` is technically detailed but reads like an internal doc. Customer-facing docs are different; they should be in a `docs/customer/` subtree or shipped on the website.

## 7. CI/CD gaps

### 7.1 What CI does today (after the fixes in this session)

- `go vet`
- `golangci-lint`
- `go test -race -count=1 ./internal/...` (unit, race-detected, with postgres service)
- `go build` for each cmd/ binary
- webmail: `npm run lint`, `tsc --noEmit`, `npm run build`
- Docker build & push for each gateway/api/webmail to ghcr.io

### 7.2 What CI should also do

- **e2e tests**: there are 13 stages of e2e tests, but they don't run in CI (they need the full testbed + reference-mailserver stack). Worth investing in a `task e2e:full` that brings the whole substrate up in a runner and runs them. Long-running CI job; mark it a separate workflow.
- **dependency vulnerability scan**: `govulncheck`, `npm audit` (with --omit=dev for prod deps), Trivy on Docker images
- **container image scanning**: `trivy image ghcr.io/rest-mail/api:latest`
- **SBOM generation**: useful for security review of customers
- **conformance tests**: send mail through the gateway, run instantmailcheck (the CLI you already have), assert no regressions in DKIM/SPF/DMARC pass rates

### 7.3 Release / deploy

`deploy.yml` exists in `.github/workflows/` (workflow_dispatch only). Verify:
- It deploys to a staging environment automatically before prod
- Prod deploys are gated on a successful e2e suite against staging
- Rollback is one button click

## 8. The "operational maturity" yardstick

Below is a rough 1-5 scale for each. Self-score honestly:

| Capability | Score |
|---|---|
| Can ship a feature in a day | 4 |
| Can debug a production issue | 2 |
| Can recover from a postgres-volume loss | 1 |
| Can detect a reputation issue early | 1 |
| Can respond to a customer "my mail is bouncing" within 15 min | 1 |
| Can scale to 10x load with one config change | 2 |
| Can onboard a new customer in <1 hour | 3 |
| Have a path from "this isn't working" to "fixed in prod" without writing code | 2 |

The four 1s are the operations gaps. Closing each is a 1-2 day investment of focused work. Total: ~2 weeks of operations work to get to a 4 across the board.

## 9. Things I'd build *before* shipping to first paying customer

The minimum viable operations:

1. **`task backup:create` + a daily cron** — postgres dump + attachments to S3
2. **`task backup:restore` + monthly drill** — verify backups work
3. **Reputation dashboard** in admin UI — pulls from DMARC reports + queue stats
4. **Spamhaus self-check + alert** — one Lambda or one cron
5. **Status page** — pick a hosted one
6. **Runbooks 2.1, 2.2, 2.3** as docs
7. **A "kill switch" for outbound from a specific domain** — for when a customer goes haywire

Below that, you're at risk. Above that, you can run a small mail product credibly.
