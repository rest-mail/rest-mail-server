# 05 — Security and Compliance

A deliberately specific list. Not a generic security checklist; only items I'd flag based on what I read in the codebase.

## Authentication

### A1 — Password storage

`internal/auth/auth.go` does password hashing. The visible test (`TestHashPassword`, `TestCheckPassword`) suggests it works. Two things to verify:

- The hash is bcrypt (cost ≥ 10) or argon2id (recommended for new code). Check what's implemented.
- There's a password upgrade path: when a user logs in successfully and their hash is bcrypt-cost-10, silently re-hash to cost-12 on the way out. Otherwise old accounts have weak hashes forever.

### A2 — JWT issuer/audience

The Subject prefix bug (`mailbox:42` vs `42`) we just fixed shows the JWT contract is evolving. Verify the JWT also has:

- `iss` (issuer) — required; should be the platform identifier
- `aud` (audience) — should be the API endpoint that's intended to consume the token
- `exp` — already verified by tests
- `nbf` (not-before) — defensive; rejects tokens that haven't started yet (clock skew tolerance)

If `aud` is missing, the access-token / refresh-token distinction (per `TokenType` field) is doing the work but isn't standards-canonical. Consider reading + verifying audience.

### A3 — Refresh token rotation

I see the API issues access + refresh tokens. The standard pattern: when a refresh is used, generate a new refresh and invalidate the old one. If the old one is used again, that's a leak signal — invalidate the entire session family.

I didn't trace this. Worth verifying.

### A4 — Token revocation

JWTs are stateless. To revoke, you need either:

- A short access-token TTL (5 minutes is common; the test uses 5 seconds)
- A blocklist (revoked-tokens table) consulted on each request
- Or both

Short TTL alone leaves a 5-minute window after a stolen token is detected. For a mail product, that's ok for users but not for admins. Consider: longer TTL for users, mandatory blocklist for admin tokens.

### A5 — Rate-limiting authentication

`internal/gateway/connlimiter/` handles per-IP gate; `internal/gateway/bancheck/` handles fail2ban-style banning. What's the auth-attempt rate limiter look like?

- IMAP: I see calls to `s.limiter.RecordAuthFail(ip)` in session.go. Good.
- SMTP submission: similarly check.
- API login: `internal/api/handlers/auth.go` should rate-limit failed POST /login attempts per IP and per username. Spammers will credential-stuff your API endpoint.

### A6 — Two-factor

Not implemented per [03-FEATURE-GAPS.md](03-FEATURE-GAPS.md) item L. Until it ships, the account is a single password's worth of security. App-specific passwords + TOTP is a reasonable bridge.

## Multi-tenancy boundary

### B1 — Domain admin doesn't exist as a tier

Roles I'd expect: Platform Admin, Domain Admin, Mailbox User, API Client.

Today: Platform Admin (manages everything) and Mailbox User (manages own mailbox). The "Domain Admin" tier (manages all mailboxes/aliases/pipelines for a single domain but no other domains) is missing.

Without it, multi-tenancy is unsafe. A customer who admins their own domain has to file a ticket with you to change their pipeline — or you give them platform admin and pray.

The model: `admin_user.go` exists; `mailbox.go` has accounts; `domain.go` has domains. The mapping is what's missing — the join table `(admin_user_id, domain_id, role)` that says "this admin user has the 'domain_admin' role on this specific domain."

### B2 — Authorization on every endpoint

Walk every API handler and ask: "given a logged-in user with mailbox_id=42, can they POST/PUT/DELETE on domain `mail3.test` if their mailbox is in `mail4.test`?"

The grep test: every handler that accepts a domain or mailbox parameter should call something like `requireDomainAccess(claims, domain_id)`. If a handler only checks `requireAuth(claims)` and operates on the parameterized domain, that's an authz hole.

I didn't audit every handler. This is the work. Allow ~1 day for a thorough audit.

### B3 — Database query scoping

GORM is great for productivity, prone to authz bugs. The pattern `db.Where("id = ?", id).First(&mailbox)` doesn't scope by domain. If user-A passes a mailbox-id from user-B's domain, they get user-B's mailbox.

Fix: standardize on a "scoped query" helper:

```go
func (q *Q) MailboxByID(claims *Claims, id uint) (*Mailbox, error)
```

Where it always joins on `domain_id = claims.DomainID OR claims.IsPlatformAdmin`. Make this the only way to look up a mailbox; raw `db.First` becomes a code review red flag.

### B4 — Cross-domain reference leakage

The model `message.go` has a `MailboxID`. When user-A's webmail loads messages, the API filters by their mailbox. Good. But:

- Search across "all messages I can see" — does it scope correctly?
- When a domain admin lists messages in the domain, are they seeing only their domain?

This is the kind of thing that breaks once you go multi-tenant.

## Secrets management

### S1 — Default secrets are in the Taskfile

Look at top-level vars:

```yaml
MAIL3_JWT_SECRET: dev-secret-change-in-production
MAIL3_MASTER_KEY: dev-master-key-change-in-production
MAIL3_DB_PASS: restmail
```

These are dev defaults. For prod, they need to be generated per-instance and stored externally (HashiCorp Vault, AWS Secrets Manager, sops-encrypted file, etc.). The parameterization document ([02-PARAMETERIZATION.md](02-PARAMETERIZATION.md)) proposes per-instance `secrets.env` files; that's the right shape.

### S2 — Master key rotation

`cmd/rotate-key/main.go` exists. The runbook for rotating the key isn't documented as far as I can see. The model `crypto/` likely envelopes encrypted fields with the master key.

What needs documenting:

- How long does rotation take? (Big mailboxes = lots of attachments to re-encrypt.)
- Can rotation be paused / resumed?
- What happens if rotation fails halfway through?

### S3 — DKIM private key storage

`internal/db/models/dkim.go` has the model. The private key is presumably encrypted at rest with the master key. Verify:

- Is the encrypted blob actually using the master key, or is the private key plaintext in a column?
- Is the key material zeroized in memory after use?

A leaked DKIM private key lets an attacker sign on your behalf forever (until you rotate). High-value protection target.

### S4 — TLS cert private keys

The certs volume holds them. In production, that volume is a docker volume on the host. Disk encryption matters; "prod ops will use encrypted disks" is the assumption. Document that as a deploy requirement.

## Input validation

### I1 — MIME parsing on inbound mail

`internal/mime/parser.go` parses incoming mail. MIME parsers are notorious for being attack surfaces:

- Decompression bombs (boundary recursion, deeply nested multipart)
- Memory exhaustion (giant base64-encoded attachments)
- Header injection (`Subject: foo\r\nBcc: attacker@evil.com`)

Verify:

- There's a max-message-size enforced before parsing
- There's a max-multipart-depth (recursion limit)
- Per-part max attachment size
- Header key names are validated (no CR/LF)

I see `internal/pipeline/filters/size_check.go` which probably handles message size. Verify the parser fails-safe before allocating buffers.

### I2 — User-supplied JS filter

`internal/pipeline/filters/javascript.go` runs user-supplied JS. The `internal/api/handlers/restmail.go` integration point. Sandbox: `vm.runInNewContext` in Node.js.

Risks:

- `vm.runInNewContext` is **not** a security boundary. The Node docs explicitly say so.
- Prototype pollution — a user's script can mutate `Array.prototype` and affect the sidecar's behavior subtly.
- Filesystem access via `process` — should be impossible in the runInNewContext sandbox, but a clever script (using import shenanigans) might escape.
- CPU exhaustion — `while(true){}` will pin a CPU until something kills it.

The fact that the JS sidecar is a separate Node process is the main mitigation; the host can `kill` it if it's pegged, and a sandbox escape only gets the sandbox's process, not the API's.

Recommend:

1. Document explicitly: the JS filter sandbox is **not** a security boundary. Treat user-supplied JS as semi-trusted.
2. Per-script timeout (5s default, configurable). Currently I see references to `--max-old-space-size=64` in the Dockerfile (memory cap) but I'm not sure about CPU / wall-time caps.
3. Only platform admins can write JS filters, not domain admins. Domain admins use Sieve.

### I3 — SMTP command parsing

The gateways parse SMTP commands. SMTP injection (CRLF in commands) is a classic attack:

```
RCPT TO:<bob@example.com>\r\nMAIL FROM:<eve@evil.com>
```

Some clients/middleboxes will treat the embedded CRLF as a new command. Verify the SMTP parser rejects CR/LF embedded in addresses.

### I4 — IMAP literal parsing

The `APPEND` command can include arbitrary message bodies as IMAP literals (`{1234}` followed by 1234 bytes of content). Bound the literal size; reject literals over the max-message-size limit.

## Audit logging

### L1 — Who did what, when

`internal/db/models/activity_log.go` exists. Verify it's actually written to from:

- Login successes / failures
- Admin: domain create/delete
- Admin: pipeline change
- Admin: mailbox create/delete
- Mailbox: password change
- Sending a message

Without these breadcrumbs, a security incident response is "guess from logs."

### L2 — Tamper-evident log

The activity log is a regular DB table. An attacker who pwns the DB can edit it. For a security-conscious deployment, the log should be:

- Append-only (DB triggers prevent UPDATE/DELETE)
- Or shipped to an external log sink (syslog, Splunk, etc.) in real time

This is overkill for v1 but worth knowing the gap exists.

### L3 — Sensitive-data redaction

Activity logs should NOT contain passwords (even hashed), JWTs, master keys, DKIM keys, message bodies, or recipient lists.

Audit the log entries for what's actually being recorded. It's easy to accidentally `log.Printf("login: %+v", request)` and capture the password.

## TLS

### T1 — TLS minimum version

The gateways use Go's stdlib TLS. Default min version in Go 1.22+ is TLS 1.2. Explicitly set to TLS 1.2 minimum (or 1.3 if you want to be aggressive — but old clients will break).

### T2 — Cipher suite ordering

Less of a problem in TLS 1.3 (no negotiation). For TLS 1.2 connections, prefer ECDHE > AES-GCM > ChaCha20.

### T3 — STS/MTA-STS enforcement

Outbound: when the recipient publishes MTA-STS with `mode: enforce`, the queue worker should fail-closed (refuse plain SMTP), not fail-open (downgrade to plain). Verify in `internal/gateway/queue/worker.go`.

Inbound: your own MTA-STS handler exists. Verify the policy is `mode: enforce` for production.

### T4 — Certificate pinning for known senders

For domains you talk to a lot, you could pin the receiving cert to known issuers. Probably overkill for v1.

## DNS / external dependencies

### D1 — DNS resolver hardening

Outbound mail relies on DNS. The codebase uses `net.LookupMX` (stdlib). That's vulnerable to:

- DNS rebinding attacks if you're trusting the resolved address for ACLs
- Cached poisoned responses

For a paranoid setup, run your own resolver (unbound, dnsmasq) and refuse to use the host's resolver.

### D2 — DNSSEC validation

Most stdlib resolvers do not validate DNSSEC. Depending on your threat model, this matters. For mail server delivery, it's relevant if you accept DANE-only routing (DNSSEC + TLSA records) — and that's a niche feature.

## Compliance

### C1 — GDPR data subject rights

A GDPR-covered user has rights to:

- **Access**: download all their personal data → API endpoint that emits a JSON dump of mailbox + messages + contacts + activity log entries
- **Erasure**: delete all their data → DELETE /accounts/me with cascading deletes
- **Portability**: export in a portable format → mbox + iCal + vCard

If your customers serve EU users, you need this. Today: not visible in the API surface.

### C2 — Data residency

If a customer says "my data must stay in the EU," the deployment needs to:

- Run all infrastructure (DB, attachments, backups) in EU regions
- Not call out to non-EU services (e.g., a US-based webhook target counts)
- Audit log this constraint

Today: data residency is what you tell the customer; not enforced.

### C3 — Email retention policy

Some industries (finance, healthcare) require N years of email retention; others (HR records) require deletion after N days. Per-domain retention rules with safety against accidental admin deletion.

### C4 — Subprocessor list

For B2B customers, they need to know all third parties that touch their data. This is a doc, not code, but the doc has to be maintained as you add integrations (e.g., if you add a Sentry, that's a new subprocessor).

## What I'd do first (security-wise)

1. **Authz audit**: walk every API endpoint, verify it enforces domain scope. ~1 day.
2. **Domain Admin tier**: implement the 3-tier model (platform / domain / mailbox). ~3 days.
3. **Audit log verification**: confirm what's captured, fix gaps. ~1 day.
4. **MIME parser bounds**: max-size, max-depth, max-attachments. ~half day.
5. **Password hash upgrade path**: silent re-hash on login. ~half day.

The rest is roadmap, not emergency.
