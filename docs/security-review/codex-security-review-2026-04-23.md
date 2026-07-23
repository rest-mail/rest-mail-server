# Codex Security Review — 2026-04-23

> **Re-evaluated 2026-07-23 against `main` @ `0a6e69f`.** The codebase changed
> substantially since this review (go-smtp adoption, RBAC #58, internal mTLS #65,
> the SSE access-token fix #46, and the removal of all Docker Compose files during
> the repo decomposition), so the cited line numbers are stale — every finding was
> re-verified against current `file:line` evidence.
>
> **Outcome:** all 8 findings are resolved except one deliberately-accepted,
> protocol-inherent behavior (flagged below).
>
> | # | Finding | Verdict (2026-07-23) |
> |---|---------|----------------------|
> | 1 | Sieve IDOR | **STILL-VALID → FIXED** (ownership check added; tests) |
> | 2 | Quarantine object-level authz | **STILL-VALID → FIXED** (ownership check added; tests) |
> | 3 | `/messages/deliver` unauth | **ALREADY-FIXED via mTLS #65** (compose evidence OBSOLETE) |
> | 4 | Recipient enumeration | `/api/mailboxes` **FIXED via mTLS #65**; `/restmail/mailboxes` **WONT-FIX-BY-DESIGN** (SMTP-RCPT-equivalent — flagged) |
> | 5 | Grafana/Prometheus default creds | **OBSOLETE** (compose files removed) |
> | 6 | SSE accepts refresh tokens | **ALREADY-FIXED (#46)** (`ValidateAccessToken`) |
> | 7 | No auth-path rate limiting | **STILL-VALID → FIXED** (per-IP limiter added; tests) |
> | 8 | Insecure compose defaults | **OBSOLETE** (compose files removed; app-config prod guards remain) |

## Scope

This review covered the monorepo’s externally reachable attack surface and security-relevant paths, including:

- API/public HTTP routes and auth/authorization behavior
- Mail gateway exposure and delivery endpoints
- Object-level access controls in handlers
- Docker Compose exposure, default credentials, and config hardening
- Session/JWT handling and token usage patterns

This was a **static code/config review** (no live penetration testing or runtime fuzzing in this pass).

## Findings

### ~~1) Critical — IDOR on Sieve endpoints allows cross-account rule takeover~~

**[RESOLVED 2026-07-23 — FIXED]** Confirmed a real IDOR: `internal/api/handlers/sieve.go`
`GetScript`/`PutScript`/`DeleteScript` parsed the URL `{id}` straight into
`mailbox_id` with no ownership check, so any authenticated mailbox user could
read/overwrite/delete another mailbox's Sieve script. Fixed by adding
`SieveHandler.resolveMailboxID`, which resolves the webmail-account `{id}` to the
caller-owned mailbox via the shared `resolveAccountMailbox`
(`internal/api/handlers/account_resolve.go`) using the JWT `WebmailAccountID`
claim — the exact pattern `VacationHandler` uses — and returns **403** for a
non-owner. Regression test: `TestSieveIDOR_OwnershipEnforced`
(`internal/api/handlers/authz_idor_test.go`) proves owner-ALLOW + non-owner-403 on
GET/PUT/DELETE and that the owner's script is neither mutated nor deleted by an
attacker.

**Issue**
`GET/PUT/DELETE /api/v1/accounts/{id}/sieve` treats `{id}` as a mailbox ID directly and does not verify that the authenticated caller owns that mailbox.

**Evidence**
- `internal/api/handlers/sieve.go:28`
- `internal/api/handlers/sieve.go:35`
- `internal/api/handlers/sieve.go:51`
- `internal/api/handlers/sieve.go:83`
- `internal/api/handlers/sieve.go:113`
- `internal/api/handlers/sieve.go:119`

**Impact**
Any authenticated user can read/overwrite/delete another user’s sieve script by guessing mailbox/account IDs, enabling unauthorized message filtering behavior and potential data loss.

**Recommendation**
Use the same account-to-mailbox ownership resolution pattern already used in safer handlers (for example `VacationHandler.resolveMailboxID` / `MessageHandler.resolveAccountMailbox`) before any query/update/delete.

---

### ~~2) Critical — Quarantine endpoints missing object-level authorization~~

**[RESOLVED 2026-07-23 — FIXED]** Confirmed a real IDOR:
`internal/api/handlers/pipeline.go` `ListQuarantine` used the URL `{id}` directly
as `mailbox_id` with no ownership check, and `ReleaseQuarantine`/`DeleteQuarantine`
loaded/mutated a quarantine row purely by its row id `{mid}` — so any
authenticated user could view, release, or delete any mailbox's quarantined mail.
Fixed by adding `PipelineHandler.resolveMailboxID` (same `resolveAccountMailbox`
ownership pattern as #1): `ListQuarantine` now filters by the caller-owned mailbox;
`ReleaseQuarantine`/`DeleteQuarantine` resolve the caller's mailbox first and scope
the row lookup with `WHERE id = ? AND mailbox_id = ?`, so a row belonging to
another mailbox is indistinguishable from a missing one (**404**, no leak, no
mutation), and a foreign account `{id}` yields **403**. Regression test:
`TestQuarantineIDOR_OwnershipEnforced` (`internal/api/handlers/authz_idor_test.go`)
covers owner-ALLOW, foreign-account-403, and the cross-mailbox-by-row-id vector
(own account + foreign row id → 404 with the row left un-released/undeleted).

**Issue**
Quarantine endpoints are in an authenticated route group but do not verify ownership of requested resources:

- `ListQuarantine` uses URL `{id}` directly as mailbox ID
- `ReleaseQuarantine` and `DeleteQuarantine` operate by quarantine row ID without ownership checks

**Evidence**
- `internal/api/handlers/pipeline.go:480`
- `internal/api/handlers/pipeline.go:487`
- `internal/api/handlers/pipeline.go:503`
- `internal/api/handlers/pipeline.go:508`
- `internal/api/handlers/pipeline.go:548`
- `internal/api/handlers/pipeline.go:554`

**Impact**
An authenticated attacker can enumerate/view/release/delete other users’ quarantined messages (confidentiality + integrity break).

**Recommendation**
Resolve and verify caller mailbox ownership from JWT claims; enforce `mailbox_id IN caller_owned_mailbox_ids` on list/read/mutate actions.

---

### ~~3) High — Inbound delivery endpoint appears externally reachable without service authentication~~

**[RESOLVED 2026-07-23 — ALREADY-FIXED via internal mTLS #65; compose evidence OBSOLETE]**
`/api/v1/messages/deliver` and `/api/mailboxes` are now registered through
`registerInternal` in `internal/api/routes.go`. When `INTERNAL_MTLS_ENABLED=true`
they are served **only** on the dedicated internal mTLS listener
(`internal/mtls`, `middleware.RequireClientCert`) and withheld from the public
router, so an unauthenticated public caller cannot reach them. The
`docker-compose.yml:135` evidence is obsolete — no compose files exist in the repo
(removed in the decomposition). Residual (flagged): mTLS is **default-off**, so in
the default network-trust deployment these machine-to-machine routes remain
tokenless on the public listener by design (documented in `routes.go`). The
"secure-by-construction" direction is to make the internal listener mandatory; see
`docs/plans`.

**Issue**
`/api/v1/messages/deliver` is unauthenticated and the handler has no service-auth checks.

**Evidence**
- Route exposed: `internal/api/routes.go:154`
- Handler no auth: `internal/api/handlers/messages.go:698`, `internal/api/handlers/messages.go:717`
- API published to host: `docker-compose.yml:135`

**Impact**
Potential unauthorized message injection/spam flood into local mailboxes and storage abuse if exposed beyond trusted internal network.

**Recommendation**
Require service-to-service auth for this route (mTLS, signed HMAC header, or internal-only network/bind + strict ACL).

---

### 4) High — Public recipient enumeration endpoints

**[PARTIALLY RESOLVED 2026-07-23 — one path FIXED, one WONT-FIX-BY-DESIGN (flagged)]**
- `/api/mailboxes` (`MailboxHandler.CheckAddress`) is now the internal
  machine-to-machine route gated by mTLS #65 (`registerInternal`) — when
  `INTERNAL_MTLS_ENABLED=true` it is off the public listener entirely. Same
  default-off caveat as #3.
- `/restmail/mailboxes` (`RestmailHandler.CheckMailbox`,
  `internal/api/handlers/restmail.go`) is the RESTMAIL **server-to-server protocol**
  endpoint — the semantic equivalent of SMTP `RCPT TO` recipient verification.
  Revealing whether a mailbox is deliverable is inherent to accepting mail (every
  MTA does this at RCPT/callout time), so it is a **deliberately accepted risk**,
  not a bug. It is intentionally left un-struck below and flagged. Note it is
  **not** rate-limited: per-IP throttling here would disrupt legitimate bulk
  server-to-server delivery. If enumeration hardening is later desired, the option
  is tarpit/greylist-style delays rather than hard limits — a product decision.

**Issue**
Mailbox existence can be queried without auth:

- `/api/mailboxes?address=...`
- `/restmail/mailboxes?address=...`

**Evidence**
- `internal/api/routes.go:153`
- `internal/api/handlers/mailboxes.go:210`
- `internal/api/handlers/mailboxes.go:224`
- `internal/api/routes.go:160`
- `internal/api/handlers/restmail.go:45`

**Impact**
Enables user discovery and targeted attacks (credential stuffing/phishing/social engineering).

**Recommendation**
Require trusted auth context for this functionality, or aggressively rate-limit and return non-enumerable responses.

---

### ~~5) High — Monitoring interfaces exposed with static/default credentials~~

**[RESOLVED 2026-07-23 — OBSOLETE]** `monitoring/docker-compose.yml` no longer
exists — all Docker Compose files were removed in the repo decomposition
(`git ls-files | grep -i compose` returns no compose YAML). This repo no longer
binds Grafana or Prometheus, so there are no default monitoring credentials to
expose. Composition/observability wiring now lives in the separate decomposed
stack, outside this repo's attack surface.

**Issue**
- Grafana is host-exposed with static admin credentials in compose.
- Prometheus is also host-exposed.

**Evidence**
- `monitoring/docker-compose.yml:25`
- `monitoring/docker-compose.yml:27`
- `monitoring/docker-compose.yml:28`
- `monitoring/docker-compose.yml:6`

**Impact**
Telemetry and operational metadata disclosure; dashboard compromise if internet-accessible.

**Recommendation**
Do not bind monitoring UIs publicly by default. Put behind auth proxy/VPN, rotate credentials, and use secrets/env injection instead of hardcoded values.

---

### ~~6) Medium — SSE endpoint accepts refresh tokens (not only access tokens)~~

**[RESOLVED 2026-07-23 — ALREADY-FIXED (#46)]** `EventHandler.Events`
(`internal/api/handlers/events.go`) now validates the bearer token with
`jwtService.ValidateAccessToken(token)` (not `ValidateToken`) and additionally
resolves mailbox ownership via `resolveAccountMailbox` before streaming — so a
refresh token is rejected and the caller must own the account. Matches the
access-token-only middleware behavior (`middleware/auth.go` also uses
`ValidateAccessToken`).

**Issue**
SSE auth path uses `ValidateToken()` instead of `ValidateAccessToken()`.

**Evidence**
- SSE handler: `internal/api/handlers/events.go:191`
- Access middleware behavior: `internal/api/middleware/auth.go:31`

**Impact**
Refresh tokens can be used where access tokens should be required, expanding token abuse surface.

**Recommendation**
Use access-token-only validation for SSE and other interactive data endpoints.

---

### ~~7) Medium — No visible API-level request throttling for auth-sensitive paths~~

**[RESOLVED 2026-07-23 — FIXED]** Confirmed genuinely missing: the global
middleware stack (`RequestID`/`RealIP`/`Logger`/`Recoverer`/CORS/metrics) had no
HTTP throttle, and the mail-gateway connlimiter/bancheck are connection-level, not
HTTP-login. Fixed by adding a dependency-free per-client-IP token-bucket limiter
`middleware.RateLimit` (`internal/api/middleware/ratelimit.go`; lazy refill,
opportunistic idle-bucket sweep, no background goroutine) applied to
`/api/v1/auth/login` and `/api/v1/auth/refresh` in `routes.go`. Tunable via config
(`AUTH_RATE_LIMIT_ENABLED` default on, `AUTH_RATE_LIMIT_RPS` default 1/s,
`AUTH_RATE_LIMIT_BURST` default 15) — bounded so ordinary interactive use is
unaffected while brute-force/credential-stuffing is throttled to a trickle
(429 + `Retry-After`). Tests: `internal/api/middleware/ratelimit_test.go` (burst,
refill, per-IP isolation, idle sweep, middleware 429/Retry-After).

**Issue**
No router-level rate limiting middleware was found for login/auth/public endpoints.

**Evidence**
- Auth routes: `internal/api/routes.go:146`
- Middleware stack (no limiter visible): `internal/api/routes.go:31`

**Impact**
Higher brute-force and credential-stuffing risk.

**Recommendation**
Add centralized rate limiting for auth endpoints and sensitive public routes.

---

### ~~8) Medium — Insecure defaults in development compose can become production risk if misused~~

**[RESOLVED 2026-07-23 — OBSOLETE; app-config prod guards remain]** All
`docker-compose.yml` / `monitoring/docker-compose.yml` files were removed in the
decomposition, so the cited compose default-credential lines no longer exist. The
application-level production guards the review noted as mitigating are still
present and are the durable control: `internal/config/config.go` fails startup in
`production` when `JWT_SECRET` is left at the dev default
(`config.go:265`) or `MASTER_KEY` is empty (`config.go:269`).

**Issue**
Dev/default credentials and secrets are set in compose files (DB creds, JWT default, Grafana creds).

**Evidence**
- `docker-compose.yml:129`
- `docker-compose.yml:131`
- `docker-compose.yml:132`
- `docker-compose.yml:43`
- `docker-compose.yml:85`
- `monitoring/docker-compose.yml:28`

**Mitigating note**
Production guard exists for JWT/Master key in app config:
- `internal/config/config.go:123`
- `internal/config/config.go:127`

**Recommendation**
Maintain strict separation of dev/prod compose profiles and fail fast when insecure defaults are present in production-like deployments.

## Prioritized Remediation Plan

_Status as of 2026-07-23 re-evaluation (`main` @ `0a6e69f`)._

1. ~~Fix object-level auth in `sieve` handlers (Critical).~~ **DONE** — ownership
   resolution + tests (#1).
2. ~~Fix object-level auth in quarantine handlers (Critical).~~ **DONE** —
   ownership resolution on list/release/delete + tests (#2).
3. ~~Lock down `/api/v1/messages/deliver` to trusted service auth only (High).~~
   **DONE via internal mTLS #65** — mechanism exists; make the internal listener
   mandatory to close the default-off gap (tracked, see `docs/plans`).
4. Protect or redesign recipient existence endpoints (High). **`/api/mailboxes`
   DONE (mTLS #65); `/restmail/mailboxes` accepted-by-design** (SMTP-RCPT
   equivalent — open product decision if enumeration hardening is later wanted).
5. ~~Lock down monitoring exposure and credentials (High).~~ **OBSOLETE** — compose
   files removed from the repo.
6. ~~Enforce access-token-only on SSE endpoint (Medium).~~ **DONE (#46)**.
7. ~~Add centralized request throttling for auth/public sensitive endpoints
   (Medium).~~ **DONE** — per-IP token-bucket limiter on login/refresh + tests (#7).

### Still open (flagged for a product decision)

- **#4 `/restmail/mailboxes` recipient enumeration** — deliberately accepted as a
  protocol-inherent behavior (equivalent to SMTP `RCPT TO`). No code change made;
  hardening, if ever desired, should be tarpit/greylist-style rather than hard
  per-IP limits that would break legitimate server-to-server delivery.
- **#3/#4 internal mTLS is default-off** — the machine-to-machine routes are only
  fully closed when `INTERNAL_MTLS_ENABLED=true`. Making it mandatory is the
  "secure-by-construction" direction, deferred to that effort.

## Assumptions and Limits

- This is a static review; no live exploit testing was performed.
- Severity is based on likely deployment exposure. If this stack is strictly local-only, network-exposure findings may downgrade.
- Third-party dependency CVE audit was not included in this pass.
