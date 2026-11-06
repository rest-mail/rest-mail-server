# 01 — Architecture Assessment

> A dated snapshot from 2026-04-29, kept for the decisions below and what rests on them. The open problems it listed moved to the issue tracker; [#1563](https://github.com/rest-mail/rest-mail-server/issues/1563) leads to where each one went.

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

## What's load-bearing

These are the parts where if you change them sideways, three other things stop working:

1. **`internal/api/middleware/`** — every API request goes through it. Adding a new auth tier here is a careful diff.
2. **`internal/db/models/`** — schema changes need a migration. The `cmd/migrate` tool is fine but I don't see a "downgrade" path. Plan accordingly.
3. **`internal/pipeline/engine.go`** — every inbound and outbound message goes through it. Changes here need full e2e coverage.
4. **`internal/gateway/apiclient/`** — the gateways' contract with the API. Breaking it breaks SMTP+IMAP+POP3 simultaneously.

## Verdict

The architecture is *good*. Better than most "we built our own mail server" projects of comparable age. What it left to do was mostly operational, not a redesign of anything fundamental.
