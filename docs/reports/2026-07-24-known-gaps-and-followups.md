# Known Gaps & Open Follow-ups — 2026-07-24

Open findings from the 2026-07-23/24 hardening + library sprint are tracked as **GitHub
issues** (the source of truth — a fix PR closes each with `Closes #N`). This file is only a
discoverable in-repo index.

## Tracked as issues

| Issue | Summary |
|---|---|
| [#119](https://github.com/rest-mail/rest-mail-server/issues/119) | OSI-5 residual — SMTP-submission DSN sender-auth lacks provenance (`dbstore.go` has no MessageID) |
| [#120](https://github.com/rest-mail/rest-mail-server/issues/120) | OSI-20 residual — capability-cache lacks `mx_cert_fingerprint` + peer-allowlist (probe is plaintext SMTP/25) |
| [#121](https://github.com/rest-mail/rest-mail-server/issues/121) | webmail — 3 deferred npm advisories (shadcn → MCP SDK → `@hono/node-server` chain, dev-only) |
| [#122](https://github.com/rest-mail/rest-mail-server/issues/122) | Container hardening deferrals (read-only rootfs, non-root prod gateways, js-filter internal net, helm securityContext) |
| [#123](https://github.com/rest-mail/rest-mail-server/issues/123) | ACME client not implemented (`cert_provider: acme` stubbed) — M-22/M-23 |
| [#124](https://github.com/rest-mail/rest-mail-server/issues/124) | Feature: self-service password reset (M-26) |
| [go-arc#4](https://github.com/rest-mail/go-arc/issues/4) | Missing public `Seal` API — go-arc is verify-only |

## Not issues (tracked elsewhere)

- `docs/security-review/2026-07-23-open-security-items.md` — the OSI register: OSI-1
  (`/restmail` enumeration, accepted-by-design / product decision), OSI-4 (done), and the
  OSI-26 residual medium/low backlog (M-6, M-9, M-14, L-4/L-5/L-7/L-8/L-11). Migrate these to
  issues if/when they're picked up.
- **Operator action** — the deploy workflow needs a `DEPLOY_HOST_KEY` GitHub Actions secret
  (`ssh-keyscan <deploy-host>`); until set, deploy fails closed (safe). Not a code task.
