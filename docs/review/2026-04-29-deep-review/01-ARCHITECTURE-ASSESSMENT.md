# 01 — Architecture Assessment

## What's been built

The current stack, by the numbers:

```
Substrate                     1 dnsmasq + 1 certs volume + 1 network
Reference daemons             postfix, dovecot, rspamd, clamav, fail2ban,
                              dnsmasq, certgen   (all upstream-extracted)
Reference mail server         postgres + postfix + dovecot + rspamd +
                              fail2ban   (per instance, e.g. mail1, mail2)
RESTMAIL product (mail3)      postgres + js-filter + api + 3 gateways +
                              webmail + admin + website
Pipeline filters              25 (DKIM, ARC, SPF, DMARC, rspamd, clamav,
                              sieve, vacation, JS sandbox, headers, size,
                              dup, sender-verify, allowlist, ratelimit, ...)
API handler files             25 (auth, mailboxes, messages, attachments,
                              aliases, contacts, domains, dkim, mtasts,
                              sender-rules, queue, sieve, stats, ...)
e2e test stages               13 (infra → traditional mail → gateways →
                              indistinguishability → restmail upgrade →
                              webmail → console → consistency → verify
                              → queue retry → bounce DSN → IMAP IDLE)
```

For a project this far along, that's a respectable feature surface. Most "build your own mail server" projects stall at "SMTP receive + IMAP fetch works, sometimes."

## The architectural decisions worth keeping

### 1. The API is the source of truth

Every protocol gateway is a thin shell over the REST API: SMTP receive → API call → DB; IMAP fetch → API call → DB. The gateways own zero state of their own (modulo TLS sessions). This means:

- Adding a new client-facing protocol is a couple-thousand-line gateway, not a re-architecture.
- The webmail and the SMTP gateway see *the same* messages by *the same* schema; you can't drift between them.
- Tests run against the API even when the goal is to validate IMAP behavior; one test surface.

The cost: the gateways can't function offline from the API. If the API is down, mail bounces. For a small product that's the right trade. For a 99.99%-uptime product you'd add a local cache or queue — but you don't get there for years.

### 2. The decomposition into upstream repos

Pulled out of the rest-mail-server repo into their own:

- `rest-mail/testbed` — substrate (network + DNS + certs)
- `rest-mail/reference-mailserver` — composer for postfix/dovecot/rspamd/fail2ban
- `rest-mail/reference-postfix`, `reference-dovecot`, `reference-rspamd`, `reference-clamav`, `reference-fail2ban`, `reference-dnsmasq`, `reference-certgen` — wrappers around upstream daemons with the rest-mail conventions (overlay configs, env-var templating, multi-arch, calver)
- `rest-mail/website` — public marketing site
- `rest-mail/instantmailcheck` — diagnostic CLI
- `rest-mail/conventions` — the contract every reference image follows

That separation lets `rest-mail-server` be just the product and not a distribution. **Most projects never make this cut.**

### 3. The pipeline-as-DAG mental model

`internal/pipeline/` provides a generic engine; filters in `internal/pipeline/filters/` plug in. A pipeline config is JSON, stored per-domain in postgres. That means:

- A domain admin can change spam thresholds without a deploy.
- New filters drop in without touching the engine.
- Inbound and outbound have the same machinery — there's no "spam check" in one and "outbound DKIM sign" hard-wired in another.

The 25 filters break down into roughly: 6 auth (DKIM/ARC sign+verify, SPF, DMARC), 4 anti-abuse (rspamd, clamav, sender-verify, ratelimit), 5 routing/policy (allowlist, contact-whitelist, recipient-check, domain-allowlist, header-validate), 3 user-controlled (sieve, vacation, javascript), 3 utility (size-check, duplicate, extract-attachments), 4 misc.

### 4. Task as the orchestrator, not docker-compose

The recent migration off docker-compose into pure task-driven `docker run` calls. Reasons it's better:

- Variables live in one place (`Taskfile.yml`'s `vars:` block), not split between an `.env` file, a compose file, and a Helm chart.
- The orchestration logic can branch on `MODE=dev|prod`, run `docker pull && docker tag` to retag stock images under `ghcr.io/rest-mail/*`, and otherwise do things compose can't express.
- Dev experience: `task <svc>:up|down|logs|restart` for any of the ~10 services, identical shape across all of them.

Worth noting: *this is unusual*. Most teams stop at docker-compose forever. The trade is more code but more control. As of today, the Taskfile (~700 lines including 10 `tasks/*.yml` includes) does what compose did plus things compose couldn't.

### 5. Air for hot-reload of Go services

Each gateway and the API have a `dev` Dockerfile target that uses `air` to rebuild and restart on file change. That's a meaningful productivity multiplier. The tradeoff is that dev mode bind-mounts the project root (`-v $(pwd):/app`), which means the dev container's filesystem is your laptop's filesystem — fine, but it's why the dev image isn't suitable for prod.

## Where the cracks are

### Crack #1: hardcoded `mail3.test` everywhere

`grep -r mail3.test internal/` returns hits in:

- TLS cert paths (`/certs/mail3.test.crt`, `/certs/mail3.test.key`)
- Hostnames (`GATEWAY_HOSTNAME: mail3.test`)
- Default DB host derivations (`{{.PROJECT}}-postgres`)
- Banner messages
- Docs
- Pipeline-filter test fixtures

Spinning up a mail4.test or a customer-specific instance today is a search-and-replace exercise. [02-PARAMETERIZATION.md](02-PARAMETERIZATION.md) has the fix.

### Crack #2: the queue's failure model is binary

Look at `internal/gateway/queue/`. A queued outbound message has the states "pending", "delivered", "failed". When delivery fails, "failed" is terminal. There's no:

- Bounced-with-soft-fail-retry-N-times state
- Per-recipient state when one address bounces and the others go through
- Bounce category (4xx temp vs 5xx permanent vs reputation block vs rate limit)
- DSN parsing back to the original sender — `tests/e2e/stage12_bounce_dsn_test.go` exists but the prod path is shallow

For a product you'd hand to customers, you need at least:

- Per-recipient queue state (one message → many recipients → many independent delivery attempts)
- Bounce category extracted from the DSN and stored
- Suppression list keyed on (recipient, reason) so a hard-bounced address doesn't get retried for 90 days

### Crack #3: multi-tenancy is implicit

Reading `internal/api/middleware/`, the AuthZ check is essentially "is this token valid; what mailbox does it belong to". There's no:

- Domain-scoped admin (a customer with mail4.test should be able to manage mail4.test mailboxes but not see mail5.test)
- Platform admin / domain admin / mailbox user / API client tier system
- Audit log of admin actions

If two real organizations share the same RESTMAIL deployment, the boundary that prevents one from seeing the other's mail is "we trust the developer wrote the right WHERE clause." That's a vulnerability waiting to happen. Detail in [05-SECURITY-AND-COMPLIANCE.md](05-SECURITY-AND-COMPLIANCE.md).

### Crack #4: SMTP outbound retries are time-based, not reputation-based

`internal/gateway/queue/worker.go` retries on a fixed schedule. Good upstream MTAs (postfix's "delay backoff", e.g.) instead retry differently based on:

- Was the bounce a 421/451 (temp) vs 5xx (perm)? Don't retry the perm.
- Has the receiving MTA started rate-limiting us? Back off harder than the default schedule.
- Is the recipient on a known-flaky mailbox provider (Yahoo, AOL)? Different schedule than Gmail.

The current implementation is the textbook implementation, and it'll work fine until you're sending real volume and start tripping rate limits.

### Crack #5: cert lifecycle is half-built

`internal/acme/` is implemented and there's an `internal/api/handlers/certificates.go`. But:

- There's no scheduled job that runs `task: renew-near-expiry` — renewal is on-demand only as far as I can tell from the code.
- DNS-01 vs HTTP-01 selection is implicit; for self-hosted deployments you usually need DNS-01 (because port 80 might not be reachable from public internet).
- The ACME account key rotates via `cmd/rotate-key` — but the runbook for "we rotated the key, now what" isn't documented.

### Crack #6: the "indistinguishability" claim is fragile

`tests/e2e/stage5_indistinguishability_test.go` asserts that the RESTMAIL gateway behaves like a traditional postfix+dovecot pair (same CAPABILITY responses, same STARTTLS dance, etc.). That's a great claim and a great test. But:

- It only tests the *protocol* surface. Headers added by RESTMAIL but not by postfix (e.g. our X-Pipeline-Verdict ones) are still observable to a sender comparing.
- Header order differs. Most receivers don't care, but some spam classifiers do.
- The test is brittle: every CAPABILITY string change is a test break.

Indistinguishability is a moving target as long as you're inventing new headers.

## What's load-bearing

These are the parts where if you change them sideways, three other things stop working:

1. **`internal/api/middleware/`** — every API request goes through it. Adding a new auth tier here is a careful diff.
2. **`internal/db/models/`** — schema changes need a migration. The `cmd/migrate` tool is fine but I don't see a "downgrade" path. Plan accordingly.
3. **`internal/pipeline/engine.go`** — every inbound and outbound message goes through it. Changes here need full e2e coverage.
4. **`internal/gateway/apiclient/`** — the gateways' contract with the API. Breaking it breaks SMTP+IMAP+POP3 simultaneously.

## Things that smell like they're going to bite you

1. **`internal/gateway/queue/worker.go` runs in-process with the gateway**, not as a separate worker. If the SMTP gateway's process restarts, in-flight retries reset. For dev that's fine; for prod you want the queue worker to be its own process with its own DB-backed lock so you can scale gateway and worker independently.
2. **The `js-filter` sidecar uses `vm.runInNewContext`**, which is sandbox-ish but not security-grade. A user with permission to write a JS filter who's clever about prototype pollution could read process env. The sidecar process being separate from the API is the only thing saving you. Detail in [05-SECURITY-AND-COMPLIANCE.md](05-SECURITY-AND-COMPLIANCE.md).
3. **`internal/api/handlers/events.go` (SSE)** opens a persistent connection per browser. A few hundred concurrent webmail users => a few hundred hanging file descriptors. Test it under load before you discover it.
4. **The `digest` package** suggests a daily-summary feature but I didn't see a scheduler. If it's there, it's not visible from `cmd/`. If it's *not* there, the digest will never fire.

## What's missing relative to "a real mail server"

Not a feature gap (those are in [03-FEATURE-GAPS.md](03-FEATURE-GAPS.md)) — this is architectural:

- **No tenancy boundary.** Domain ownership exists but it's not enforced as a hard wall.
- **No multi-region.** Not a "we should add this tomorrow" item, but it's the kind of thing that influences DB layout and you want to plan for.
- **No idempotency keys on outbound send.** A retried API call that says "send this message" might double-send. Idempotency keys on `POST /messages/send` would fix it.
- **No audit log of admin actions.** Who changed the pipeline filter for mail3.test from `dmarc:reject` to `dmarc:none`? Currently: nobody knows.
- **No backup/restore tooling.** `pg_dump` works, but there's no `task backup:create / backup:restore` flow for the mail filesystem (attachments + maildir + signing keys).

## Verdict

The architecture is *good*. Better than most "we built our own mail server" projects of comparable age. The remaining work is mostly filling in operational gaps (queue surgery, reputation visibility, tenancy enforcement) rather than redesigning anything fundamental.

The one thing that needs doing *first* before more features land: **parameterization** ([02-PARAMETERIZATION.md](02-PARAMETERIZATION.md)). Right now the cost of "spin up a customer instance" is high enough that you'll resist doing it, and that resistance will compound.
