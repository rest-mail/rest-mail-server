# Open Security Items — 2026-07-23

Live register of **unresolved** security items carried forward from the point-in-time
review docs in this folder. This is the maintained doc; the dated review files are
historical and, once fully processed + captured here, can be deleted.

## Open items

### OSI-1 — `/restmail/mailboxes` recipient enumeration
- **Status:** decision pending (accept-by-design vs harden).
- **Detail:** `/restmail/mailboxes?address=` reveals recipient existence. It is the
  server-to-server `RCPT TO` equivalent — protocol-inherent (refusing to answer breaks
  legitimate delivery). The sibling `/api/mailboxes` is closed by internal mTLS (#65).
- **Decision needed:** accept as protocol-inherent, **or** harden with greylist/tarpit
  on that path — **not** per-IP rate caps (which would break legitimate bulk
  server-to-server delivery).
- **Origin:** `codex-security-review-2026-04-23` (SR-004).

### OSI-2 — Internal mTLS is default-off
- **Status:** on roadmap (secure-by-construction epic).
- **Detail:** the gateway→API machine routes (`/api/v1/messages/deliver`,
  `/api/mailboxes`) only fully close when `INTERNAL_MTLS_ENABLED=true`. The mechanism
  exists (#65); the residual is that it is opt-in.
- **Action:** make mTLS mandatory — auto-provision the internal CA + certs in
  `instance:new`, default new instances on, and boot-refuse when enabled-but-uncertified.
  Pre-enable gate: a live `task e2e` run with `internal_mtls: true`.
- **Origin:** `codex-security-review-2026-04-23` (SR-003 / SR-004).

### OSI-3 — RESTMAIL `/restmail/messages` accepts unauthenticated mail
- **Status:** needs-fix (**deferred — collides with in-flight tarpit work in `restmail.go`**).
- **Detail:** the handler comment claims DKIM/SPF/DMARC verification but no such
  code exists; any host can POST a spoofed-`From` message into a local mailbox (BEC/CEO fraud).
- **Action:** verify DKIM (or mTLS/HMAC per-peer) before acceptance, or restrict to a
  known-peer allowlist. Do **not** edit `restmail.go` here — hand to the tarpit worker.
- **Origin:** kimi #7, opus C-1.

### OSI-4 — Config secret/TLS hardening
- **Status:** needs-fix (**deferred — collides with in-flight work in `config.go`**).
- **Detail:** `JWT_SECRET` is rejected only when equal to the legacy default in
  `production` — no min-length/entropy floor; DB `DSN()` uses `sslmode=disable`; API
  binds `0.0.0.0` by default.
- **Action:** in `config.go`: enforce ≥32-byte secret regardless of env; make
  `sslmode` configurable (default `require` in prod). Deferred (config.go collision).
- **Origin:** kimi #14, #34, opus C-11 (residual), M-16, M-27.

### OSI-5 — SMTP session hardening
- **Status:** needs-fix (**deferred — collides with in-flight tarpit work in `internal/gateway/smtp/**`**).
- **Detail:** confirm AUTH PLAIN is refused pre-STARTTLS on cleartext ports; enforce
  strict `\r\n` (SMTP-smuggling, CVE-2023-51766 class); cap command pipelining; banner
  recon; SMTP auth logs include plaintext emails.
- **Action:** fold into the tarpit/smtp work. Do not edit smtp here.
- **Origin:** kimi #31, opus H-7, L-6, M-1, M-4, M-24.

### OSI-6 — JS filter sidecar is not a real sandbox (admin RCE)
- **Status:** needs-decision (large effort; lives in `projects/js-filter-sidecar`).
- **Detail:** the sidecar runs admin-supplied scripts via Node `vm.runInNewContext`
  (documented as **not** a sandbox); a `pipelines:write` admin escapes to the Node
  runtime → env (DB creds, `MASTER_KEY`). URL is now configurable; unreachable sidecar
  still fails open.
- **Action:** replace with `isolated-vm`/WASM/microVM, or remove user-defined JS;
  harden the container (read-only FS, drop caps, no egress); default filter-error to
  `defer`. **Decision needed:** keep-and-sandbox vs remove the feature.
- **Origin:** kimi #4, #10, #11, opus C-7, H-9, H-10.

### OSI-7 — REST request-body size caps
- **Status:** needs-fix (low; open, non-colliding).
- **Detail:** several handlers `json.NewDecoder(r.Body)` without `http.MaxBytesReader`
  (body-size DoS). SMTP `SIZE` is already enforced (#46); this is the REST side.
- **Action:** wrap request bodies with `http.MaxBytesReader` (shared middleware).
- **Origin:** kimi #21 (residual), #46.

### OSI-8 — DKIM sign fails open to plaintext key
- **Status:** needs-decision (data migration + fail-closed).
- **Detail:** `dkim_sign` silently uses the stored key as plaintext when
  `DecryptString` fails; legacy plaintext-at-rest keys may exist.
- **Action:** migrate all keys to encrypted-at-rest; remove the silent fallback and
  refuse to sign on decrypt failure. **Decision needed** because it can stop signing
  for currently-plaintext keys in the running stack.
- **Origin:** kimi #28, opus C-15.

### OSI-9 — IMAP object-level authorization hardening
- **Status:** needs-fix (open, non-colliding — `internal/gateway/imap/**`).
- **Detail:** folder names aren't gateway-validated; DELETE/EXPUNGE trust the
  gateway-cached message list. Backend must re-verify the message/folder is linked to
  the JWT account.
- **Action:** gateway allowlist folder chars + length; backend `DeleteMessage`/select
  filter by authenticated account.
- **Origin:** kimi #30, opus H-4, H-16.

### OSI-10 — Refresh-token rotation + revocation
- **Status:** needs-decision (schema addition; touches `auth.go`/`internal/auth`).
- **Detail:** refresh tokens aren't rotated and can't be revoked; logout is
  client-side only; unlinked mailbox keeps access until the 15-min access TTL lapses.
- **Action:** rotate on every refresh; persist `revoked_tokens(jti, expires_at)`;
  logout + password-change revoke. Coordinate with #45 (admin-session refresh).
- **Origin:** kimi #32, opus C-6, M-25.

### OSI-11 — Security headers on API + frontends
- **Status:** needs-decision (best at reverse proxy).
- **Detail:** no HSTS/CSP/X-Frame-Options/X-Content-Type-Options/Referrer-Policy. A
  blanket API CSP breaks the bundled Swagger UI, so the safe subset belongs at the
  proxy / nginx (`admin/` is out of scope to edit here).
- **Action:** add headers at the reverse proxy; optionally a non-CSP subset middleware
  on the API.
- **Origin:** kimi #37, opus H-13.

### OSI-12 — `/metrics` unauthenticated
- **Status:** needs-decision (accept-by-design vs gate).
- **Detail:** API `/metrics` (and gateway `/metrics` from #83) are unauthenticated.
  Standard Prometheus practice is network-level scraping; JWT-gating breaks scrapers.
- **Action:** decide network-gate (reverse proxy / monitoring CIDR) vs auth; scrub any
  PII from labels.
- **Origin:** kimi #38, opus H-12.

### OSI-13 — Sieve `redirect` has no external-domain allowlist
- **Status:** needs-decision (product).
- **Detail:** a mailbox owner (or attacker via a future IDOR) can silently redirect all
  mail to an external address; the sieve IDOR itself is already fixed (codex #1).
- **Action:** default-restrict redirect targets to the user's domain unless
  admin-approved; log/alert on external redirect installs; cap per mailbox.
- **Origin:** opus C-3.

### OSI-14 — `IsAdmin` latent self-escalation
- **Status:** needs-fix (latent; low — not currently exploitable).
- **Detail:** the deprecated `WebmailAccount.IsAdmin` is copied into mailbox claims and
  `AdminOnly` honors it. No self-update path exists today, but the field is a foot-gun.
- **Action:** remove `IsAdmin` from `WebmailAccount`/claims; make `AdminOnly` key only
  on `UserType=="admin"`.
- **Origin:** opus C-5.

### OSI-15 — Scanner (rspamd/ClamAV) verdicts trusted unsigned + fail-open
- **Status:** needs-decision.
- **Detail:** verdicts are fetched over plain HTTP with no mTLS/HMAC/pinning;
  `fallback_action: continue` ships mail when the scanner is unreachable.
- **Action:** mTLS or HMAC-signed verdicts (or netns sidecars); change default
  fallback to `defer`.
- **Origin:** opus C-9.

### OSI-16 — Webmail HTML rendering (CSS exfiltration + no iframe)
- **Status:** needs-fix (frontend — `webmail/`).
- **Detail:** DOMPurify keeps `style` in `ALLOWED_ATTR` (CSS/`@font-face`
  exfiltration); email HTML renders in the main document (no sandboxed iframe).
- **Action:** drop `style`/`class`; render in a sandboxed iframe with a strict CSP;
  block remote images by default.
- **Origin:** opus C-16, L-2.

### OSI-17 — Container / infrastructure hardening
- **Status:** needs-decision (deployment; several items OBSOLETE post-decompose).
- **Detail:** protocol/frontend containers run as root; `/certs` keys readable by all
  mounting containers; Dovecot plaintext auth + unencrypted SASL on mailnet; Postfix
  submission TLS `may`; fail2ban regex/log-injection.
- **Action:** non-root `USER`; key perms / secrets manager; `disable_plaintext_auth`;
  SASL on unix socket; TLS `encrypt` on 587; harden fail2ban filter.
- **Origin:** opus C-17, H-17, H-18, H-19, M-11, M-18.

### OSI-18 — Pipeline unknown-filter fail-open
- **Status:** needs-decision (behavior change; blast radius).
- **Detail:** `engine.go` logs and `continue`s when a filter fails to instantiate, so a
  renamed/removed security filter is silently skipped.
- **Action:** validate filter names at pipeline-save; at runtime default to
  `defer`/`reject` (fail-closed) for unknown/failed filters.
- **Origin:** opus H-8.

### OSI-19 — Admin exposure + 2FA
- **Status:** needs-decision (deployment + feature).
- **Detail:** admin UI reachable from the internet with only a password; no
  IP-allowlist/VPN/2FA.
- **Action:** IP/VPN gate at the proxy; add TOTP/WebAuthn for admin (and user) logins.
- **Origin:** opus H-11, L-9.

### OSI-20 — RESTMAIL capability-cache MX binding
- **Status:** needs-fix (`internal/gateway/queue`).
- **Detail:** the outbound capability cache keys on recipient domain and trusts an
  EHLO-advertised RESTMAIL URL; a rogue MX could poison routing on shared/multi-tenant
  relays.
- **Action:** key by `(recipient_domain, mx_host, mx_cert_fingerprint)`; only trust
  allowlisted peers; TTL + invalidate on cert rotation.
- **Origin:** opus H-20.

### OSI-21 — Account-linking TOCTOU + single-link constraint
- **Status:** needs-fix (`accounts.go`, non-colliding).
- **Detail:** no transaction/`SELECT FOR UPDATE` between mailbox lookup and
  `linked_accounts` insert; the unique constraint allows two webmail accounts to link
  the same mailbox.
- **Action:** wrap link in a transaction; add a unique constraint on `mailbox_id` alone.
- **Origin:** opus H-21.

### OSI-22 — CI deploy `StrictHostKeyChecking=no`
- **Status:** needs-fix (out of scope to edit here — `.github/workflows`).
- **Detail:** SSH deploy accepts unknown host keys (MITM of the deploy channel).
- **Action:** pre-populate `known_hosts` from a `DEPLOY_HOST_KEY` secret.
- **Origin:** opus H-23.

### OSI-23 — MIME RFC 2047 header decode without CRLF rejection
- **Status:** needs-fix (`internal/mime`, non-colliding).
- **Detail:** decoded header values aren't CRLF-checked before any re-serialization
  (forward/reply/bounce/Sieve), enabling header injection (e.g. Bcc). Also autodiscover
  XXE (M-12) to verify.
- **Action:** strip/reject `\r`/`\n` in decoded header values on every header.
- **Origin:** opus H-5, M-12.

### OSI-24 — Auth timing / user enumeration (constant-time)
- **Status:** needs-fix (low; substantially mitigated by the auth rate limiter).
- **Detail:** login and POP3 `USER` return faster for unknown users (no bcrypt),
  leaking existence.
- **Action:** always run bcrypt against a dummy hash; constant-time identical error.
- **Origin:** opus H-3, H-14.

### OSI-25 — Bounce / DSN sender authentication
- **Status:** needs-fix (`internal/gateway/queue`; blast radius — do carefully).
- **Detail:** DSNs are inserted without verifying the bounce recipient matches the
  authenticated session; spoofed-sender mail-bombing.
- **Action:** verify bounce recipient vs session account; rate-limit DSNs per recipient.
- **Origin:** opus H-6.

### OSI-26 — Residual medium/low backlog
- **Status:** open (low / best-practice; batch later).
- **Detail:** catch-all for the remaining opus Medium/Low items not called out above —
  M-2 (connlimiter race), M-5 (attachment symlink), M-6 (mass-assignment fragility),
  M-9 (DKIM header coverage), M-14 (test-endpoint rate-limit), M-17 (test/send —
  now prod-locked), M-22/M-23 (ACME limits/path), M-26 (no password reset), L-4/L-5/L-7/L-8/L-11.
- **Action:** run `govulncheck ./...` + `pnpm audit`; address opportunistically.
- **Origin:** opus §5–§6.

## Processed reviews

- **`kimi-security-review.md`** — re-evaluated 2026-07-23 against `main` @ `3567df1`;
  all 50 findings classified and struck/annotated. Fixed here: #9 (webhook SSRF).
- **`opus-security-review-2026-04-23.md`** — re-evaluated 2026-07-23; all 17 Critical +
  24 High struck/annotated, Medium/Low dispositioned. Fixed here: C-8 (webhook/duplicate
  SSRF), C-10 (dnsmasq injection), C-13 (test-endpoint prod gate fail-closed).
- The codex trio (`codex-security-review-2026-04-23.md` + board + summary) remains
  **fully resolved** (struck in #77 / #80); OSI-1 and OSI-2 carry forward from it.

### Resolved in this pass (2026-07-23 — not carried as open items)

- **Webhook/duplicate SSRF** — `internal/pipeline/filters/ssrf.go` guarded dialer
  refuses loopback/link-local (incl. cloud metadata) at dial time (rebind-safe);
  RFC1918 stays allowed for legit internal calls. Tests: `ssrf_test.go`.
  (kimi #9/#49, opus C-8.)
- **dnsmasq config injection** — `DnsmasqProvider.EnsureRecords` rejects control
  characters/newlines in domain/record name/value before writing. Test:
  `dnsmasq_test.go`. (opus C-10.)
- **Test-endpoint prod gate** — `TestHandler.productionLocked()` fails closed on any
  `prod*` `ENVIRONMENT` (case-insensitive). Test: `testing_env_test.go`. (opus C-13.)

## Retirement

Once a source review is fully resolved (every finding struck through) and its open items
are captured above, its dated file can be deleted.
