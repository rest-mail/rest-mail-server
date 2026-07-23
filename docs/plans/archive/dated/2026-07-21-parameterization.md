# Parameterization → installable system (2026-07-21)

Supersedes the 2026-04-29 deep-review recommendations where they conflict. That
review's headline ask (de-hardcode `mail3.test` / `10.99.0.x`) still holds; its
code-surgery estimate was overstated (see Audit below).

## Goal

One command spins up a mail instance from a single config file:

```
task instance:up -f instances/mail3.test.yml
```

Design **installable-first** (dev testbed and real-host install share one model),
**implement the testbed path first**.

## Corrected audit (fresh, at head of `main` + uncommitted work)

Parameterization here is ~80% orchestration/config, ~20% code cleanup — the app
is already domain-agnostic:

- `internal/api/handlers/autoconfig.go`, `mtasts.go` — already fully DB-driven
  (look up domain by name/id). The review's PR4 target is already clean.
- `internal/config/config.go` — the app's real config contract is flat env vars
  read via `getEnv` (`DB_*`, `JWT_SECRET`, `MASTER_KEY`, `GATEWAY_HOSTNAME`,
  `DNS_PROVIDER`, ports, `ACME_*`, …). `GATEWAY_HOSTNAME` just *defaults* to
  `mail3.test` — a soft default, env-overridable.
- Litter: `internal/console/status.go` hardcodes a `mail3.test` health entry;
  `cmd/seed`, `cmd/certgen` default flag, `internal/api/handlers/testing.go` are
  dev/test-only.
- Taskfile centralizes IPs/ports as `MAIL3_*` / `RESTMAIL_*` top-level vars;
  per-service `tasks/*.yml` keep `10.99.0.x` only as fallback defaults, mapped to
  flat container env at `docker run` (`-e DB_NAME={{.API_DB_NAME}}`).
- `.env` (repo root) is **orphaned** from the container flow — nothing loads it
  (no godotenv, no `dotenv:`, air doesn't source it). It only matters for native/
  IDE runs, is a stale subset of the contract, and drifts from the Taskfile.

## Config model (locked)

**One YAML file per instance.** YAML not JSON (human-authored, wants comments);
optional JSON Schema for validation only.

**No `type:` discriminator.** Type is an execution concern, not a config field.
An instance is a declared **composition of components** on the substrate;
"reference" vs "restmail" is emergent from the component set, never declared.

Two visibly separate layers in the file:

- **envelope** — logical, implementation-agnostic: `domain`, `hostname`,
  `network`, `dns_provider`, `cert_provider`, DNS *intent*, cert, mailbox/user set.
- **binding** — implementation-specific: `components:` (each `{name, ip, …}`),
  overlay dirs, ports.

```yaml
# instances/mail3.test.yml
domain: mail3.test
hostname: mail3.test
network: testbed_mailnet
dns_provider: testbed-dnsmasq     # future: cloudflare | route53 | manual
cert_provider: testbed-certgen    # future: letsencrypt-http01 | letsencrypt-dns01
components:
  - { name: postgres,     ip: 10.99.0.43 }
  - { name: api,          ip: 10.99.0.20, port: 8080 }
  - { name: smtp-gateway, ip: 10.99.0.13 }
  - { name: imap-gateway, ip: 10.99.0.15 }
  - { name: pop3-gateway, ip: 10.99.0.16 }
  - { name: js-filter,    ip: 10.99.0.22 }
  - { name: webmail,      ip: 10.99.0.21 }
  - { name: admin,        ip: 10.99.0.27 }
admin: { email: admin@mail3.test }
# secrets (DB_PASS/JWT_SECRET/MASTER_KEY) → instances/mail3.test.secrets.env (gitignored)
```

**Executor = component catalog** (`name → how to run it`, `name → source repo`).
The existing `tasks/*.yml` files already *are* this catalog (each wraps one
component's `docker run`); reference-mailserver's `tasks/*.yml` are its catalog.
Per-component **defaults live in the catalog** so manifests stay minimal — no
`preset:` shorthand (that just smuggles `type:` back in). Dispatcher stays THIN:
parse → route → pass vars; never absorbs daemon config. One invocation = one
instance (no pair/bundle aggregator).

reference-mailserver keeps owning its daemon config (overlays + init SQL, its own
repo). A small cross-repo change lets it take manifest-derived inputs
(hostname/IPs) instead of only `configs/<name>/dns.env` + `CONFIG=`.

## Endgame (build toward, not now)

Ultimately the **same config runs as reference OR restmail depending on how it's
executed** — the task layer picks the implementation. Realizes the "old vs new
against each other" testbed as an abstraction. Reaching it = lift the *binding*
out of the config into the executor; the *envelope* alone drives either
implementation. Guardrail now: keep envelope/binding as separate blocks so
level-3 is a move, not a rewrite. Consequence: DNS A/MX records point at the
concrete server IP (implementation-specific), so treat DNS as *intent* the
executor renders from the chosen implementation's IPs — never freeze concrete IPs
into the envelope.

## PR sequence

| PR | Scope |
|----|-------|
| **PR0** | Reconcile uncommitted work: website → `.workspace/website` clone-on-demand + `.env.local`/`project:toggle`/`WEBSITE_MODE` toggle layer. Commit or set aside before PR1 touches the Taskfile. Define precedence: `instances/<x>.yml` (rendered `.env`) → `secrets.env` → `.env.local` (per-machine user toggles) override last. |
| **PR1** | **Foundation.** Create `instances/mail3.test.yml` (envelope + components) + `instances/mail3.test.secrets.env` from today's `MAIL3_*`. Render manifest → flat `.env` (the config.go contract names). `INSTANCE`-based selection + `dotenv:` in Taskfile. Replace the orphaned root `.env` with a committed `.env.example` documenting the full contract. `task restmail:mail3:up` unchanged. No-regret. |
| **PR2** | `instance:scaffold DOMAIN=` — template a new manifest, allocate unused IPs **at scaffold time** (write to manifest, never runtime autodetect), gen secrets. Brings up nothing. |
| **PR3** | `instance:dns:register` / `:unregister` — `testbed-dnsmasq` provider writing unique `<domain>.conf` fragments to the testbed shared volume. Build the provider seam so `manual` (emit records to stdout) is a trivial 2nd impl — that seam is what the prod path reuses. DNS as intent (see Endgame). |
| **PR4** | Small code cleanup: drop `mail3.test` default in `config.go`; de-hardcode `console/status.go`. seed/certgen stay dev tools. |
| **PR5** | `instance:up -f <file>` + `instance:new` umbrella: parse manifest → catalog dispatch → certs → dkim → dns:register → up → bootstrap → smoke. Orchestration by now. |
| later | Prod providers (`letsencrypt-*`, `cloudflare`/`route53`/`manual`) slot into the existing schema; packaging bundle. Then the endgame binding-into-executor move. |
