# Known Gaps & Open Follow-ups — 2026-07-24

Consolidated register of findings surfaced during the 2026-07-23/24 hardening + library
sprint that are **not yet fixed**, so nothing lives only in chat or a PR body. Bugs that
were fixed are in git history + the OSI register's resolution table; this doc is only the
*open* tail. Update or delete entries as they're addressed.

## Library completeness

- **go-arc: verify-only, no public `Seal` API.** `Verify` is the entire exported surface,
  yet ARC is fundamentally about a forwarder **sealing** a message. rest-mail seals
  internally (`internal/pipeline/filters/arc_seal.go`) using go-dkim primitives — so the
  sealing logic exists but isn't exported from the public `go-arc` library. **Follow-up:**
  extract/export a `Seal` API so `go-arc` is a complete public ARC library (feature PR).
  Surfaced by the go-arc docs pass (rest-mail/go-arc#3).

## Security residuals (partial fixes — see OSI register)

- **OSI-20 residual — capability-cache MX binding.** #96 implemented MX-**host** identity
  binding + TTL (a cached capability can't be misapplied across hosts). **Not** implemented:
  `mx_cert_fingerprint` binding + a peer allowlist — the capability probe is still plaintext
  SMTP/25, so the entry is bound to the MX hostname but not its TLS identity. Do when the
  probe gains TLS.
- **OSI-5 residual — DSN sender-auth on the submission path.** #96 added bounce/DSN
  sender-auth for queued outbound that has a linked `MessageID`. The SMTP-submission enqueue
  (`internal/gateway/smtp/dbstore.go`) doesn't persist a `MessageID`, so SMTP-submitted DSNs
  fall to the "no linked message → allow (rate-limited)" branch. That path is authenticated
  at submit time, but carrying provenance for full DSN sender-auth there is deferred.

## Dependencies

- **webmail: 3 npm advisories deferred** (#114). The `shadcn → @modelcontextprotocol/sdk →
  @hono/node-server <2.0.5` chain (GHSA-frvp-7c67-39w9, moderate). No forward fix exists
  (latest MCP SDK still pins hono v1); npm's only offered fix is downgrading shadcn. shadcn
  is a dev-only scaffolding CLI (never bundled/exercised at runtime) → exposure ~nil.
  Revisit when the MCP SDK bumps `@hono/node-server` to v2.

## Infra deferrals (from #98 container hardening; mirrored in OSI-17)

- read-only rootfs on the dev-target service images
- non-root prod gateway images + prod nginx (blocked on external cert-key file perms)
- js-filter dedicated internal (no-egress) network — needs testbed dnsmasq coordination
- helm chart `securityContext` / `NetworkPolicy`

## Parameterization deferrals (roadmap)

- **ACME client not implemented.** `cert_provider: acme` is wired + stubbed (returns
  not-implemented); the manifest `tls.acme` fields render. Real Let's Encrypt issuance
  (challenge type — HTTP-01 vs DNS-01, account, storage, auto-renewal, staging/rate-limits)
  is a feature. Same item as OSI-26 **M-22/M-23**.

## Features awaiting product shape

- **M-26 — self-service password reset.** No end-user password-reset flow today (admin
  resets manually). Needs product/security design: identity proof (recovery address? the
  primary mailbox is *on this server* — chicken-and-egg), token lifetime/single-use/rate
  limiting, delivery channel. Feature, not a mechanical fix.

## Operator action (cannot be done from code)

- **#91 — `DEPLOY_HOST_KEY` secret.** The deploy workflow now pins the deploy host's SSH
  host key from this GitHub Actions secret and `StrictHostKeyChecking=yes`, failing closed
  if unset. The operator must add it (repo Settings → Secrets → Actions) with the output of
  `ssh-keyscan <deploy-host>`. Until then, deploy fails closed (safe).

## Already tracked elsewhere (pointers, not duplicated here)

- `docs/security-review/2026-07-23-open-security-items.md` — the OSI register: OSI-1
  (`/restmail` enumeration, accepted-by-design / product decision), OSI-4 (done), OSI-26
  residual medium/low (M-6 mass-assignment, M-9 DKIM header coverage, M-14 test-endpoint
  rate-limit, L-4/L-5/L-7/L-8/L-11), and the deferred sub-items in the OSI-6/17/19 rows.
- `docs/plans/2026-07-23-parameterization-roadmap.md` — parameterization is 100% (PR1/2/3/5/6/7
  + multi-domain + host profile); ACME issuance above is the one carried-forward stub.
