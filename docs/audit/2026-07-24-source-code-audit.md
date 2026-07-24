# Source Code Audit — 2026-07-24 (migrated)

The 2026-07-24 source-code audit of the Go backend, frontends, infrastructure,
and protocol-conformance surface has been fully triaged and migrated into the
issue tracker. Every actionable finding now lives as a GitHub issue; the raw
findings text has been removed from this file. This index records where each
finding went.

## Critical / High

| Issue | Finding |
|---|---|
| #160 | JavaScript filter sidecar: VM sandbox is escapable and the execution endpoint is unauthenticated |
| #161 | `X-Forwarded-For` trusted without a proxy allowlist, defeating IP-based rate limiting and delivery auth |
| #162 | Inbound `Authentication-Results` headers are trusted, allowing DMARC/SPF enforcement bypass |
| #163 | Default config (internal mTLS off) exposes unauthenticated mail-injection and enumeration routes with caller-supplied `client_ip` |
| #164 | Sessions survive password change and account disable; admin capabilities frozen in the refresh token |
| #165 | `rotate-key` re-encrypts the wrong tables, silently breaking DKIM signing and 2FA |
| #166 | CRLF injection via unvalidated recipients enables SMTP command injection and header forgery |
| #167 | Outbound delivery has no SSRF protection on MX dial or the RESTMAIL endpoint URL |
| #168 | MTA-STS enforce mode bypassed by the plaintext RESTMAIL upgrade path |
| #169 | RESTMAIL capability probe has no I/O deadline; a silent MX can wedge queue workers |
| #170 | Permanent SMTP failures never detected (type assertion instead of `errors.As`); mail retried for days |
| #171 | SMTP-submitted outbound mail bypasses the outbound pipeline: unsigned, Bcc relayed, no rate limit |
| #172 | No `Received:` trace header is prepended on any path |
| #173 | Raw message bytes corrupted for non-UTF-8 content (JSON string transport) |
| #174 | IMAP MOVE keeps the same UID in the destination folder, breaking incremental sync |

## Medium

| Issue | Finding |
|---|---|
| #176 | `rate_limit` filter can never trip; config key mismatch |
| #177 | Allow/blocklist and contact-whitelist match the unauthenticated sender and skip spam/greylist scanning |
| #178 | ARC override applied with no trusted-sealer allowlist, enabling a DMARC bypass |
| #179 | Unthrottled password-verification oracles: `test-connection` and `LinkAccount` |
| #180 | Gateway AUTH brute-force protection is per-IP only and the ban is not a hard stop |
| #181 | Authenticated users can set an arbitrary `From:` header (header-level spoofing) |
| #182 | DATA-time API error on inbound mail misroutes local mail into the outbound queue |
| #183 | Unauthenticated TLS-RPT ingestion allows storage write amplification |
| #184 | No request-body size limit on authenticated endpoints; no send rate limit or recipient cap |
| #185 | Hand-rolled SPF evaluator: no `include:`/`redirect=`, no DNS timeouts, no lookup limits |
| #186 | Pipeline engine has no per-filter timeout and no panic recovery |
| #187 | RESTMAIL inbound path builds the pipeline message without raw headers or request context |
| #188 | Greylist: unbounded table growth, dead config fields, permanent whitelist, swallowed DB errors |
| #189 | Quarantine digest interpolates attacker-controlled strings into HTML unescaped |
| #190 | IMAP COPY loses flags/INTERNALDATE; COPY/APPEND non-atomic with swallowed errors; can return UID 0 |
| #197 | IMAP Draft flag silently discarded by the REST API; Flagged/starred mapping is asymmetric |
| #191 | Gateway EXPUNGE/POP3 delete is a soft delete: quota not reclaimed and the row is unreachable |
| #192 | Helm chart is undeployable in its declared production mode and ships no security contexts or NetworkPolicy |
| #193 | Gateways run as root and the shared certs volume exposes CA private keys to every container |
| #194 | Admin JWT persisted in localStorage; admin logout never revokes server-side |
| #195 | Secrets passed as `-e` env flags (visible via `docker inspect`); scaffolded `secrets.env` written 0644 |
| #196 | Production enforcement keys on exact-case `ENVIRONMENT`; destructive auto-migrate runs on every boot |

## Low / informational backlog

Low-severity items are grouped into subsystem backlog issues:

| Issue | Backlog |
|---|---|
| #199 | Outbound queue and DSN generation hardening (8BITMIME relay, DSN headers/escaping, DSN counter spoofing, retry jitter) |
| #200 | SMTP/IMAP/POP3 gateway conformance and robustness (quota code, line bound, bancheck, folder truncation, APPEND framing, ENVELOPE fields) |
| #201 | Pipeline filter correctness (vacation, sieve/duplicate actions, size estimate, shallow-copy aliasing, JS filter fail-open, sidecar async timeout) |
| #202 | API-surface input-validation and output-encoding (mass-assignment, XML/Content-Disposition encoding, pagination, domain validation, LIKE wildcards) |
| #203 | Infrastructure, deployment, and frontend hardening (monitoring, cert key perms, seed binary, health disclosure, frontend headers, CI pinning) |
| #204 | Defense-in-depth and go-imap upstream-limitation notes |
