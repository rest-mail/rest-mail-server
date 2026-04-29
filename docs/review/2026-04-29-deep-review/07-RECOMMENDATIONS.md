# 07 — Recommendations / Roadmap

Prioritized. Time estimates are honest, not optimistic. Each item references the document where the rationale lives.

## Phase 0 — finish what's started (≈ 1 week)

The Phase 0 items are about the work-in-flight that needs to land cleanly before anything new. None of them is exciting; all of them prevent compounding pain.

### 0.1 Land the docker-compose → task migration in production

Status: code is done as of `main`. CI green for both this repo and the sibling repos (testbed, reference-mailserver) after the fixes in this session.

What's left:
- Verify the Helm chart (`helm/restmail/`) still works post-rename. The chart was last touched in Phase 5.
- Update the Helm chart to use the same `RESTMAIL_REGISTRY` + `<SVC>_REGISTRY` pattern so production gets the same FQDN images.

### 0.2 Reputation-disregarded items

The user explicitly mentioned not breaking anything. Spot-check at this point that:
- `task restmail:mail3:up` brings up the full stack from scratch
- `task restmail:mail3:down` cleanly tears it down
- `task restmail:mail3:restart` works
- e2e tests pass against the fresh stack (`task test:e2e`)

### 0.3 Health-wait for every service in `restmail:mail3:up`

Currently the umbrella waits for postgres then api. The other services (gateways, webmail, admin, website) come up sequentially without health checks. That's mostly fine but worth tightening — when a gateway is in "container running, not yet listening," the orchestration should wait. ~half day.

## Phase 1 — Parameterization (≈ 1-2 weeks)

The user's headline ask. Everything in [02-PARAMETERIZATION.md](02-PARAMETERIZATION.md). Five small PRs:

1. **PR 1** (1 day): move `MAIL3_*` to `instances/mail3.test/manifest.yml` + `dotenv:` loading.
2. **PR 2** (2 days): `task instance:scaffold DOMAIN=...`.
3. **PR 3** (2 days): `task instance:dns:register/unregister` building on the testbed's fragment volume.
4. **PR 4** (2 days): de-hardcode `mail3.test` from `internal/api/handlers/autoconfig.go` + `mtasts.go` + `cmd/seed`.
5. **PR 5** (1-2 days): `task instance:new` umbrella wiring it all together + README update.

Total ≈ 9 dev-days. After this lands you can spin up a customer instance in 90 seconds.

## Phase 2 — Operational visibility (≈ 1-2 weeks)

You can't run a mail product without these. From [04-UGLY-SIDE.md](04-UGLY-SIDE.md) and [06-OPERATIONS-GAPS.md](06-OPERATIONS-GAPS.md):

### 2.1 DMARC report ingestion (3 days)

Receivers send aggregate reports as XML/JSON gzipped attachments to your `rua=mailto:dmarc@...` address. Build:
- A dedicated mailbox that catches `dmarc@` for each domain
- A daily worker that parses incoming reports
- Storage: per-domain, per-source-IP pass/fail counts
- Admin UI: "your DMARC last 7 days" chart

This unblocks Phase 3 (you can move a domain from `p=none` to `p=reject` once the data shows it's safe).

### 2.2 TLS-RPT report ingestion (2 days)

The model already exists (`internal/db/models/tlsrpt.go`). Wire up the inbound parser. JSON gzipped, simpler than DMARC.

### 2.3 Spamhaus self-check + alerting (1 day)

A scheduled task: query each of your sending IPs against the major blocklists every 15 minutes. If any return "listed", trigger an alert and (optionally) auto-suspend outbound from that IP.

### 2.4 Per-domain metrics (2 days)

Add `domain` as a label to every Prometheus metric that's currently global. Update the Grafana dashboard to drill in. This is the data feed for the reputation dashboard.

### 2.5 Reputation dashboard (3 days)

Admin UI page that pulls together:
- DMARC pass rate (last 7d)
- TLS-RPT failure rate (last 7d)
- Bounce rate (last 7d)
- Queue depth
- Blocklist status
- Per-receiver delivery stats (Gmail, Outlook, Yahoo)

This is the page an admin opens every Monday morning.

## Phase 3 — Multi-tenancy hardening (≈ 1 week)

From [05-SECURITY-AND-COMPLIANCE.md](05-SECURITY-AND-COMPLIANCE.md):

### 3.1 Domain Admin tier (3 days)

Add the missing role tier. Database migration: `domain_admin` join table. API change: every endpoint that accepts a `domain_id` checks `requireDomainAccess(claims, domain_id)`. Webmail: a "I'm a domain admin for X" UI surface.

### 3.2 Authz audit (2 days)

Walk every API endpoint, verify it enforces domain scope. Fix the holes. Write tests. This is grunt work but irreplaceable; nobody else will catch the holes.

### 3.3 Activity log completeness (2 days)

Verify every admin action and every login event writes to the activity log with sane fields. Add what's missing.

## Phase 4 — Queue maturity (≈ 1 week)

From [03-FEATURE-GAPS.md](03-FEATURE-GAPS.md) Tier 1 A, B, D:

### 4.1 Per-recipient queue state (3 days)

`queue_recipient` table, per-recipient delivery attempts, per-recipient state in API responses, webmail surface.

### 4.2 Suppression list (2 days)

`suppression` table, automatic insertion on hard bounce, periodic expiry, admin UI to clear.

### 4.3 Bounce categorization (2 days)

DSN parser → category enum. Queue retry strategy branches on category. Webmail UI shows human-readable reason.

## Phase 5 — Customer-experience features (≈ 2-3 weeks)

The features that make the product feel polished. Pick from [03-FEATURE-GAPS.md](03-FEATURE-GAPS.md):

- Plus-addressing (Tier 1.E) — half day
- ARC trust list (Tier 1.F) — 2 days
- DKIM key rotation automation (Tier 1.C) — 2 days
- MIME composition fixes (Tier 2.N) — 2 days
- Send-later (Tier 3.O) — 2 days
- Snooze (Tier 3.P) — 2 days
- Undo send (Tier 3.Q) — 2 days
- List-Unsubscribe outbound (Tier 1, surfaced in [04-UGLY-SIDE.md](04-UGLY-SIDE.md)) — 1 day

## Phase 6 — DR / scale (≈ 2 weeks)

From [06-OPERATIONS-GAPS.md](06-OPERATIONS-GAPS.md):

- `task backup:create` + cron + S3 (2 days)
- `task backup:restore` + monthly restore drill scheduling (2 days)
- Standalone queue worker container (2 days)
- Per-domain rate limit (2 days)
- Status page integration (1 day)

## Phase 7 — Polish (≈ 1-2 weeks)

The quality-of-life stuff:

- 2FA / TOTP (3 days)
- WebAuthn / passkeys (3 days; can lean on a library)
- Sieve script editor in webmail (3 days)
- Calendar invite outbound (3 days)
- Attachment preview generation (3 days)

## Cross-cutting investments

These don't fit a phase but should happen continuously:

### Tracing

OpenTelemetry SDK in each Go binary, OTLP exporter to your tracing backend. Adds ~10% overhead, gives you the ability to debug issues in 5 minutes that currently take 50.

### Load testing

Build a `task loadtest:smtp` that drives smtp-source against the gateway. Run it before each release. Catches regressions in the queue, the api client, the gateway accept loop.

### e2e in CI

The 13 stages of e2e tests are gold but only run locally. Containerize the testbed + reference-mailserver and run them in a slow CI lane (1-2 hours, scheduled nightly).

### Documentation

Customer-facing docs as a separate effort from the internal `docs/`. A `docs/customer/` tree with the runbooks-but-for-end-users is a different writing style.

## What I'd cut from "things to add"

- **Calendar full implementation (CalDAV)** — heavy, low value vs. just-iCal-invitations
- **PGP/S/MIME encryption** — niche; the audience for "I want PGP through webmail" is approximately zero
- **Mailing list management** — Mailman exists, don't reinvent
- **Custom HTML signatures with embedded images** — image-hosting story is a rabbit hole
- **Multi-region** — premature for the user count

## The single most important one

If you do nothing else from this list: **Phase 1, PR 1**. Move the `MAIL3_*` vars into `instances/mail3.test/manifest.yml` and load them via `dotenv:`.

Everything else is roadmap; this is plumbing. Without it, every subsequent feature has to keep working with the hardcoded mail3.test, and the temptation to "just add another constant" will compound. With it, every subsequent feature lands in a place that already understands "this is one of N instances."

It's the smallest single change with the largest downstream multiplier.

## A note on velocity

The codebase is in a place where small focused PRs land cleanly. The infrastructure is solid (CI, lint, tests, image build, docker-run substrate). The temptation will be to go big — "let's add 2FA AND multi-tenancy AND DMARC reports in one quarter."

Resist. Mail products fail by trying to do too much at once and shipping none of it. The phases above are intentionally conservative. Three months of phases 0-2 ships a more mature product than six months of "everything at once" usually does.

Have a good evening. The architecture is in good shape; the road ahead is paved.
