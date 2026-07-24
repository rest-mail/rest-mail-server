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
- **PR5 — Public TLS / cert-provider seam (G5)** *[no config.go]* — **SHIPPED.** Added the
  optional `tls:` block (`extra_hostnames[]` + `acme:{enabled,email,staging,directory}`),
  a `CertSANHostnames()` derivation (served hostnames ∪ extra_hostnames → rendered
  `MAIL3_TLS_CERT_SANS`, resolving #99's deferred multi-name SAN item), and generalized
  `instance:certs:issue` behind `cert_provider`: **testbed-certgen** (default, unchanged —
  e2e stays green), **manual** (validate operator-dropped cert/key, no issuance), and
  **acme/letsencrypt** stubbed to a not-yet-implemented error (fields wired through; the
  ACME client itself is a later PR). `tls.internal` (mTLS) stays with PR6. Risk: medium.
- **PR6 — mTLS provisioning (G7)** *[touches config.go]* — **SHIPPED.** Internal mTLS
  was already built (#65) and is on for the testbed (the scaffold/testbed manifests
  carry `internal_mtls: true`); this PR makes its provisioning DECLARATIVE without
  touching the working handshake. Added the optional `tls.internal:` block
  (`{ mode: off|verify|require (default require, secure-by-default ON), ca_source:
  testbed-certgen|manual }`) as the richer counterpart of the legacy top-level
  `internal_mtls` bool (still supported; the block wins when present, an omitted
  block falls back to the bool → existing manifests render byte-for-byte as before,
  pinned by golden). `mode` renders `MAIL3_INTERNAL_MTLS=true` (verify/require both
  ENABLE the enforced `RequireAndVerifyClientCert` handshake — the implementation
  has no soft mode; off suppresses the line). De-hardcoded cmd/api's literal
  `/certs/ca.crt` into `config.TrustedCACertPath` (`TRUSTED_CA_CERT`, default exactly
  `/certs/ca.crt` → testbed unchanged; empty skips the extra outbound trust).
  Generalized `instance:mtls:issue` behind `ca_source` (RESTMAIL_INTERNAL_CA_SOURCE,
  rendered only when non-default): **testbed-certgen** (default, the in-repo
  `certgen --internal-mtls` command byte-identical to before — e2e stays green) and
  **manual** (validate deployer-provisioned `/certs/internal-*` material, no issuance).
  The **NOTE the earlier "gate behind `mode: off`" line was STALE** — it predated the
  default-on decision; the real posture is mTLS ON and this PR does NOT change it.
  Risk: high (working handshake) — mitigated: every existing golden (config.env,
  scaffold_testbed.config.env, config_tls.env) is byte-unchanged and the testbed
  renders no new line, so its rendered config + provisioned cert paths are identical.
- **PR7 — Real-host substrate profile (G4)** *[no config.go]* — **SHIPPED.** Added a
  `--profile testbed|host` flag to `instance scaffold` (default `testbed`). The
  **testbed** profile is byte-for-byte today's output (pinned by the
  `scaffold_testbed.*` golden, so the testbed + e2e stay green). The **host**
  profile emits a real-host substrate with the testbed hardwires stripped: NO
  `10.99.0.x` mailnet IPs (component IPs left unset for the deployer to assign),
  NO `testbed_dns_ip` / `testbed-dnsmasq` / `testbed-certgen` / `testbed_*`
  volumes (per-instance `<slug>_net` / `<slug>_certs`, `dns_provider: manual`),
  a blank `registry` placeholder (not `ghcr.io/rest-mail`), `mailnet_only: false`
  (publish on real interfaces), a production runtime posture, and
  `cert_provider: manual` (a real host has no reference-certgen — the deployer
  drops the cert, or switches to acme once that client lands). `internal_mtls`
  stays on (secure-by-default; the internal CA is provisioned on any substrate).
  Profiles change SCAFFOLD DEFAULTS only — `render`/`check` and the env contract
  are identical, and a host manifest strict-parses + passes `instance:check`
  from birth (pinned by the `scaffold_host.*` golden). Risk: medium.

Order: **PR1 → PR2 → {PR3, PR4, PR5} → PR6 (after mTLS) → PR7**. Only PR6 (+ maybe a trivial PR4 line) touch `config.go`.

## 4. Migration / compatibility
- Env vars stay the contract; manifest only *renders* them — binaries never learn about YAML.
- Additive + strict-parse-safe; omitted blocks → code defaults.
- Primary `restmail.test` keeps working through PR1's single relocation; `instance:check` in CI guards drift.
- Testbed stays the default profile; real-host is opt-in (PR7).
- Secrets/DKIM/mTLS private material provisioned at runtime, never committed.

## 5. Open questions (need a decision)
1. **Instance home (PR1):** top-level `instances/<domain>/` (drop `INSTANCE_DIR` special-case) vs keep under `.workspace/testbed`?
2. ~~**Manifest cert scope (PR5):** manifest owns public TLS/ACME + `extra_hostnames`, or cert provisioning stays out-of-band (deployer drops files, manifest names the volume)?~~
   **RESOLVED (PR5):** both, selected by `cert_provider`. The manifest owns the SAN set
   (`domains:` served hostnames ∪ `tls.extra_hostnames`) and declares ACME inputs
   (`tls.acme:`), while provisioning stays a task: `testbed-certgen` issues via the testbed
   CA, `manual` validates deployer-dropped files in the named certs volume without issuing,
   and `acme` is stubbed pending the ACME client.
3. ~~**mTLS coupling (PR6):** wait for `feat/internal-mtls` to define config fields then add the manifest block, or design `tls.internal` now?~~
   **RESOLVED (PR6):** the mTLS implementation landed first (#65, on for the testbed via
   `internal_mtls: true`); PR6 then added `tls.internal:` as the richer declarative
   form over the existing `INTERNAL_MTLS_*` env contract. `mode` maps to the existing
   `MAIL3_INTERNAL_MTLS` switch (the legacy bool stays valid, block wins when present);
   `ca_source` steers `instance:mtls:issue` (testbed-certgen default | manual). No
   change to the working handshake — only its configuration/provisioning became
   manifest-driven, with the testbed's rendered env + cert paths byte-identical.
4. **DKIM selector (PR3):** purely a rendered input, or also asserted in a DB/health-check?
5. **certgen (G8):** keep `cmd/certgen` as a dev tool or deprecate in favour of `instance:certs:issue`?
6. ~~**Multi-domain instances:** manifest declares several served domains, or one primary domain + others via admin API post-up?~~
   **RESOLVED (multi-domain schema):** the manifest declares several served
   domains. Top-level `domain` stays the PRIMARY (instance identity/hostname +
   default cert CN); an optional `domains:` list declares ADDITIONAL served
   domains, each `{ name, server_type?, hostname?, dkim:{selector?,bits?}, dns? }`.
   The primary is never repeated in the list (validated). Omitting `domains:` →
   exactly today's single-domain behavior (byte-identical render, pinned by
   golden test). `ServedDomains()` resolves the primary+additional set; render
   emits `MAIL3_SERVED_HOSTNAMES` (cert SAN set) + `MAIL3_SEED_SERVED_DOMAINS`
   (name:server_type) only when additional domains exist. Provisioning iterates
   each served domain: `cmd/seed` creates a DB row per domain (with its
   server_type), `instance:dkim` provisions a DKIM selector/key per domain,
   `instance:dns:register` writes a dnsmasq fragment per domain over the shared
   instance gateways. Cert SAN plumbing is rendered/passed here; the multi-name
   SAN derivation (served hostnames ∪ `tls.extra_hostnames` →
   `MAIL3_TLS_CERT_SANS`) and reference-certgen multi-name issuance are RESOLVED
   in PR5 (TLS/cert seam). No
   DB/model changes — domains remain fully DB-driven, the manifest only DECLARES
   which to provision at instance-up.

### Critical files
`internal/instance/render.go`, `internal/instance/scaffold.go`,
`internal/config/config.go`, `Taskfile.yml`, `tasks/smtp-gateway.yml`
(secondary: `cmd/instance/main.go`, `cmd/api/main.go` for the mTLS/internal-CA seam).
