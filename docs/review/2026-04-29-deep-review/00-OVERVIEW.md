# RESTMAIL — Deep Review (2026-04-29)

A full-feature audit of the rest-mail-server codebase as it stands at the head of `main` (commit `72b3642`, post the docker-compose → task migration and the docker/→projects/ rename).

This review is opinionated. The author has built the codebase enough to know where the joints are; the goal here isn't to pat you on the back but to surface the things you'd otherwise discover the hard way at 3am on a Tuesday.

## Documents in this review

- **[01 — Architecture assessment](01-ARCHITECTURE-ASSESSMENT.md)** — what's been built, what's load-bearing, where the cracks are.
- **[02 — Parameterization (one-command instance bring-up)](02-PARAMETERIZATION.md)** — the headline ask: how to spin up a new domain or instance from a script, no hardcoded IPs/hostnames. Concrete proposal with an example end-to-end script.
- **[03 — Feature gaps](03-FEATURE-GAPS.md)** — what's missing that mature mail servers ship.
- **[04 — Ugly side of mail](04-UGLY-SIDE.md)** — deliverability, reputation, anti-abuse, DNS hygiene. The stuff that breaks once strangers start sending you mail.
- **[05 — Security and compliance](05-SECURITY-AND-COMPLIANCE.md)** — auth, secrets, multi-tenancy, audit logging.
- **[06 — Operations gaps](06-OPERATIONS-GAPS.md)** — observability, runbooks, DR, cert lifecycle, queue surgery.
- **[07 — Recommendations](07-RECOMMENDATIONS.md)** — prioritized roadmap, not a fantasy list.

## Executive summary

**What's strong.**

- The decomposition is real. Substrate (testbed: dnsmasq + certs volume + mailnet network) is its own repo, daemons are their own reference-* repos, and rest-mail-server is just the *product* — gateways, API, frontends. That's a clean architectural line and most projects never get there.
- The pipeline filter set is unusually deep for a homegrown stack: DKIM sign+verify, ARC sign+verify, SPF, DMARC, rspamd adapter, ClamAV adapter, Sieve, vacation autoresponder, JavaScript sandbox filter, header validation, size limits, duplicate detection, sender verify, contact whitelist, domain allowlist, rate limit. That's a respectable feature set.
- Three protocol gateways (SMTP submission/relay, IMAP, POP3) all backed by the same REST API as the source of truth. The "everything is the API" choice means the protocol layer is genuinely thin and the storage layer is consistent.
- ACME client is in-tree (not a sidecar). Means cert lifecycle is observable from the same metrics surface as the rest of the app.
- Move to plain `docker run` driven from Taskfiles eliminated docker-compose's worst quirks (sticky var leaks across includes, the COMPOSE_PROJECT_NAME footgun, the awkward depends_on/healthcheck dance).

**What's weak.**

- **Heavy hardcoding of `mail3.test` and `10.99.0.x` IPs.** This is the user's headline concern, and it's right on. Spinning up another product instance today requires hand-edits in 6+ files. See [02-PARAMETERIZATION.md](02-PARAMETERIZATION.md) for the fix.
- **No bounce/DSN handling on the receiving side beyond the basics.** When mail3 sends a message that fails, what comes back is best-effort. This is a deliverability deal-breaker once you start sending real volume. ([04-UGLY-SIDE.md](04-UGLY-SIDE.md))
- **Reputation is invisible.** No DMARC report ingestion, no TLS-RPT, no Google Postmaster integration, no Microsoft SNDS. You will not know your reputation is dying until your bounce rate spikes. ([04-UGLY-SIDE.md](04-UGLY-SIDE.md))
- **Multi-tenancy is implicit, not enforced.** A logged-in mailbox can mostly poke at things scoped by mailbox-id, but the boundary between "user", "domain admin", and "platform admin" needs an explicit RBAC model. ([05-SECURITY-AND-COMPLIANCE.md](05-SECURITY-AND-COMPLIANCE.md))
- **Queue lacks a graveyard.** Failed-permanently messages should be visible, replayable, deletable. Right now an op debugging a delivery failure has to `psql` and read raw rows. ([06-OPERATIONS-GAPS.md](06-OPERATIONS-GAPS.md))
- **No structured DR plan.** Postgres backups are implicit (the named volume), there's no runbook for "we lost the volume," there's no second-region replica concept yet.

**The thing nobody talks about until it's too late.**

If a customer sends a bulk campaign that gets your sender IP blocklisted on Spamhaus / Spamcop / SORBS / a major ISP's internal list, you have to a) detect it, b) suspend that customer, c) request delisting. None of those three has a path in the current product. This is more important than any feature on the roadmap. See [04-UGLY-SIDE.md](04-UGLY-SIDE.md) for "what mature mail products do here."

## How to read this

Each document stands alone. Start with [02-PARAMETERIZATION.md](02-PARAMETERIZATION.md) if you only have time for one — it's the most actionable and matches the user's specific ask. Then [04-UGLY-SIDE.md](04-UGLY-SIDE.md) for the things that will bite you when real users arrive. The rest is depth.

There are no fake metrics or "best practices say" hand-waving in here. Every claim is rooted in something I read in this codebase or a real-world mail-server failure mode.
