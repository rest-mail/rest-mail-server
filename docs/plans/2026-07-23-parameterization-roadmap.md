# Parameterization Roadmap — rest-mail (re-grounded 2026-07-23)

Supersedes `docs/plans/2026-07-21-parameterization.md`. That doc's model (one YAML
manifest per instance, envelope/binding split, catalog executor, no `type:`
discriminator) has largely **shipped** — `cmd/instance`, `internal/instance`,
`instance:scaffold|render|check|new`, `dns-env`, `dkim-provision` all exist at
`main` (a896cbd). This revision re-measures the gap against that shipped tooling
plus the newer surface: go-smtp engine, 7 extracted `rest-mail/*` libs, RBAC, and
the `SMTP_MAX_MESSAGE_SIZE` / `SMTP_MIN_TRANSFER_RATE` / grace / stall knobs. The
in-flight `feat/internal-mtls` branch is accounted for structurally, not depended on.

## 1. Current-state assessment

### Already parameterized (env contract in `internal/config/config.go`)
Flat env vars via `getEnv*`, all overridable today with no code edit:
- DB pool: `DB_HOST/PORT/NAME/USER/PASS/MAX_OPEN_CONNS/MAX_IDLE_CONNS/CONN_MAX_LIFETIME`
- API: `API_PORT/API_HOST/LOG_LEVEL/API_BASE_URL`
- Gateway identity: `GATEWAY_HOSTNAME` — **default already `localhost`, not `mail3.test`** (old "PR4" is effectively done; `config.go` has zero `mail3.test`/`restmail.test` literals)
- Ports: `SMTP_PORT_INBOUND/SUBMISSION/SUBMISSION_TLS`, `IMAP_PORT/IMAP_TLS_PORT`, `POP3_PORT/POP3_TLS_PORT`
- New SMTP policy knobs (strict-validated): `SMTP_MAX_MESSAGE_SIZE`, `SMTP_MIN_TRANSFER_RATE`, `SMTP_TRANSFER_GRACE_PERIOD`, `SMTP_TRANSFER_STALL_TIMEOUT`
- Queue/policy: `QUEUE_WORKERS`, `QUEUE_POLL_INTERVAL`, `MTASTS_ENFORCE`
- TLS: `TLS_CERT_PATH/TLS_KEY_PATH/TLS_CERT_DIR`
- Secrets: `JWT_SECRET`, `MASTER_KEY` (hard-required in production)
- `DNS_PROVIDER`, `CORS_ALLOWED_ORIGINS`, `PROXY_PROTOCOL_TRUSTED_CIDRS`, `ACME_*`, `ENVIRONMENT`

Domains, aliases, mailboxes, DKIM keys are **fully DB-driven** (admin API,
per-domain `server_type ∈ {traditional, restmail}`). No domain identity is compiled in.

### Already parameterized in the manifest (`internal/instance/render.go` `Manifest`)
`domain`, `hostname`, `proxy_host`, `project`, `network`, `certs_volume`,
`testbed_dns_ip`, `dns_provider`, `cert_provider`, `registry`, `image_tag`,
`log_level`, `environment`, `mailnet_only`, `db.{name,user}`, `components[]`
(per-component IP + ports). Secrets render to gitignored `secrets.env`. `Parse` is
strict (`UnmarshalStrict`).

### Gap list — what a fresh deployer still cannot express declaratively

| # | Gap | Evidence |
|---|-----|----------|
| G1 | New SMTP policy knobs not in manifest, not threaded through Taskfile | `render.go:20-51`, `Taskfile.yml:128-149`, `tasks/smtp-gateway.yml:44-50` |
| G2 | `QUEUE_WORKERS`/`QUEUE_POLL_INTERVAL`/`MTASTS_ENFORCE` not manifest-reachable | `tasks/smtp-gateway.yml:54` |
| G3 | Instance-layout split: `.gitignore:35` expects `instances/*.test/`, but primary lives at `.workspace/testbed/configs/restmail/` via `INSTANCE_DIR` special-case | `.gitignore:35`, `Taskfile.yml:44`, `cmd/instance/main.go:113` |
| G4 | Scaffold hardwires testbed substrate (`10.99.0.`, `testbed_*`, `ghcr.io/rest-mail`) into every manifest | `scaffold.go:23,209-257` |
| G5 | TLS/cert story testbed-shaped: `instance:certs:issue` hardcodes `reference-certgen`, one wildcard; no manifest `tls:` block / ACME path | `Taskfile.yml:946-959`, `tlsutil/sni.go` |
| G6 | DKIM selector fixed to `"default"`; no manifest `dkim:` section | `cmd/instance/main.go:201,240`, `Taskfile.yml:1003-1028` |
| G7 | Internal-CA path hardcoded `/certs/ca.crt`; gateways call API over plain http; the `feat/internal-mtls` seam | `cmd/api/main.go:23-38`, `Taskfile.yml:138` |
| G8 | Litter/drift (dev-only): `.env.example:39,51`, `cmd/certgen/main.go:36`, `cmd/seed/main.go:22`, comments in `render.go`/`scaffold.go`/`tlsutil/sni.go` | as cited |

**Bottom line:** orchestration skeleton is done. Remaining work is (a) thread new
knobs into the manifest (G1/G2), (b) reconcile instance layout (G3), (c) build the
non-testbed substrate + cert/mTLS story (G4/G5/G7). No deep app surgery.

## 2. Proposed manifest schema (additive, optional, strict-parse-safe)

New blocks map 1:1 to `config.go` env vars; omitted fields fall back to code
defaults (no behavior change for existing manifests):

- `smtp:` → `max_message_size`, `min_transfer_rate`, `transfer_grace_period`,
  `transfer_stall_timeout`, `queue_workers`, `queue_poll_interval`, `mtasts_enforce`
- `dkim:` → `selector`, `bits` (key material stays a runtime secret)
- `tls:` → `extra_hostnames[]`, `acme:{enabled,email,staging,directory}`,
  `internal:{mode: off|verify|require, ca_source}` (mTLS shape provisional)

Render mapping: each field → a `MAIL3_*` line → Taskfile per-component include →
container flat env var. Struct fields ship in the same PR that documents them
(strict parse).

## 3. Revised PR sequence

`config.go` is the serialization chokepoint (merged large-message work + in-flight
mTLS touch it). PRs that edit it are marked; the rest are manifest/Taskfile/scaffold.

- **PR1 — Reconcile instance layout (G3)** *[no config.go]* — one home for manifests (recommend top-level `instances/<domain>/`); drop or align the `INSTANCE_DIR` special-case. Risk: medium (dotenv load path). Test: `instance:render`/`check` + e2e stage1.
- **PR2 — Thread SMTP/queue policy into manifest (G1/G2)** *[no config.go]* — add `smtp:` block + Taskfile SMTP include lines. Risk: low, additive. Test: golden render + e2e stage3/4.
- **PR3 — DKIM selector in manifest (G6)** *[no config.go]* — `dkim.selector/bits` → `instance:dkim`. Risk: low.
- **PR4 — Litter/drift cleanup (G8)** — docs/dev tools; sequence after PR2/mTLS if it touches config.go at all. Risk: very low.
- **PR5 — Public TLS / cert-provider seam (G5)** *[no config.go]* — `tls:` block; generalize `instance:certs:issue` behind `cert_provider` (testbed-certgen | manual, letsencrypt-* later). Risk: medium.
- **PR6 — mTLS provisioning (G7)** *[touches config.go]* — **after `feat/internal-mtls` merges.** Replace hardcoded `/certs/ca.crt`; `tls.internal` → config; provision internal CA + client certs. Risk: high; gate behind `mode: off`.
- **PR7 — Real-host substrate profile (G4)** *[no config.go]* — `scaffold --profile testbed|host`. Risk: medium.

Order: **PR1 → PR2 → {PR3, PR4, PR5} → PR6 (after mTLS) → PR7**. Only PR6 (+ maybe a trivial PR4 line) touch `config.go`.

## 4. Migration / compatibility
- Env vars stay the contract; manifest only *renders* them — binaries never learn about YAML.
- Additive + strict-parse-safe; omitted blocks → code defaults.
- Primary `restmail.test` keeps working through PR1's single relocation; `instance:check` in CI guards drift.
- Testbed stays the default profile; real-host is opt-in (PR7).
- Secrets/DKIM/mTLS private material provisioned at runtime, never committed.

## 5. Open questions (need a decision)
1. **Instance home (PR1):** top-level `instances/<domain>/` (drop `INSTANCE_DIR` special-case) vs keep under `.workspace/testbed`?
2. **Manifest cert scope (PR5):** manifest owns public TLS/ACME + `extra_hostnames`, or cert provisioning stays out-of-band (deployer drops files, manifest names the volume)?
3. **mTLS coupling (PR6):** wait for `feat/internal-mtls` to define config fields then add the manifest block, or design `tls.internal` now?
4. **DKIM selector (PR3):** purely a rendered input, or also asserted in a DB/health-check?
5. **certgen (G8):** keep `cmd/certgen` as a dev tool or deprecate in favour of `instance:certs:issue`?
6. **Multi-domain instances:** manifest declares several served domains, or one primary domain + others via admin API post-up?

### Critical files
`internal/instance/render.go`, `internal/instance/scaffold.go`,
`internal/config/config.go`, `Taskfile.yml`, `tasks/smtp-gateway.yml`
(secondary: `cmd/instance/main.go`, `cmd/api/main.go` for the mTLS/internal-CA seam).
