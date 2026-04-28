# Codex Security Review — 2026-04-23

## Scope

This review covered the monorepo’s externally reachable attack surface and security-relevant paths, including:

- API/public HTTP routes and auth/authorization behavior
- Mail gateway exposure and delivery endpoints
- Object-level access controls in handlers
- Docker Compose exposure, default credentials, and config hardening
- Session/JWT handling and token usage patterns

This was a **static code/config review** (no live penetration testing or runtime fuzzing in this pass).

## Findings

### 1) Critical — IDOR on Sieve endpoints allows cross-account rule takeover

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

### 2) Critical — Quarantine endpoints missing object-level authorization

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

### 3) High — Inbound delivery endpoint appears externally reachable without service authentication

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

### 5) High — Monitoring interfaces exposed with static/default credentials

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

### 6) Medium — SSE endpoint accepts refresh tokens (not only access tokens)

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

### 7) Medium — No visible API-level request throttling for auth-sensitive paths

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

### 8) Medium — Insecure defaults in development compose can become production risk if misused

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

1. Fix object-level auth in `sieve` handlers (Critical).
2. Fix object-level auth in quarantine handlers (Critical).
3. Lock down `/api/v1/messages/deliver` to trusted service auth only (High).
4. Protect or redesign recipient existence endpoints (High).
5. Lock down monitoring exposure and credentials (High).
6. Enforce access-token-only on SSE endpoint (Medium).
7. Add centralized request throttling for auth/public sensitive endpoints (Medium).

## Assumptions and Limits

- This is a static review; no live exploit testing was performed.
- Severity is based on likely deployment exposure. If this stack is strictly local-only, network-exposure findings may downgrade.
- Third-party dependency CVE audit was not included in this pass.
