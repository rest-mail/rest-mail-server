# Upstream Decomposition — rest-mail Org

**Date:** 2026-04-28
**Status:** Draft
**GitHub org:** https://github.com/rest-mail

## Goal

Decompose the current monorepo into focused upstream repositories under the `rest-mail` GitHub org, so that:

- The `rest-mail-server` repo holds **only product code** (Go services, gateways, js-filter, api) and its packaging (compose for dev, Helm for prod).
- Reference daemons (postfix, dovecot, rspamd, clamav, fail2ban, dnsmasq, certgen) are **standalone images** with their own repos, CI, and tagged releases — usable independently by anyone, not just restmail.
- A thin `reference-mailserver` composer assembles the daemons into a parameterized "traditional mail server" (one config dir = one server; deploy twice for the mail1/mail2 pair).
- A `testbed` repo provides the dev-only network/DNS/cert substrate (`mailnet`, dnsmasq, certgen, optional shared clamav). Two flavors: docker-compose and helm/kind.
- The `website` (currently in `website/`) moves to its own repo under the org.
- Integration is by **network name only** (`mailnet`). No shared code, no submodules, no awareness between repos beyond the documented convention.

## Non-goals

- Not changing protocol behavior, schema, or product features.
- Not introducing per-domain schema isolation (decided: single-schema + RLS + optional per-tenant encryption is the path).
- Not splitting restmail's own services (api, gateways, js-filter) into separate repos — they stay together as one product.

## Target repository layout

```
github.com/rest-mail/
├── conventions             # standards doc + lint script for reference-* images
├── reference-postfix       # one image: postfix + minimal entrypoint + config overlay
├── reference-dovecot       # one image
├── reference-rspamd        # one image
├── reference-clamav        # one image (or thin wrapper around upstream)
├── reference-fail2ban      # one image
├── reference-dnsmasq       # one image (used by testbed)
├── reference-certgen       # one image (used by testbed)
├── reference-postgres-mail # one image OR documented use of vanilla postgres + seed scripts
├── reference-mailserver    # composer: pulls images, mounts config dirs, joins mailnet
├── testbed                 # mailnet + dnsmasq + certgen + (optional) shared clamav
├── rest-mail-server        # the product (this repo, slimmed down)
└── website                 # marketing/docs site
```

## Cross-image conventions (the `conventions` repo)

Lock these in **before** extracting any image — they define the contract every reference image obeys.

- **Config overlay path:** `/etc/<daemon>-overlay` — entrypoint merges this on top of baked-in defaults.
- **Env var naming:** `<DAEMON>_HOSTNAME`, `<DAEMON>_DOMAIN`, `<DAEMON>_DB_HOST`, `<DAEMON>_DB_USER`, `<DAEMON>_TLS_CERT`, `<DAEMON>_TLS_KEY`, `<DAEMON>_LOG_LEVEL`. Predictable per-daemon prefix.
- **Logging:** stdout/stderr only. No syslog daemons inside the container. Structured where the daemon supports it.
- **PID 1:** `exec` the daemon directly. No supervisord wrapping a single process. Two-process containers (postfix needs rsyslog) use `tini` or `s6-overlay` minimally — supervisord only when genuinely multi-process.
- **Signals:** SIGTERM = graceful shutdown. SIGHUP = reload config / TLS certs without restart.
- **Healthcheck:** every image ships a `HEALTHCHECK` that protocol-tests the daemon, not just `pgrep`.
- **User/UID:** non-root where possible; documented UID for predictable volume permissions.
- **Image tags:** `:1.2.3`, `:1.2`, `:1`, `:latest`, plus immutable `:1.2.3-<git-sha>`.
- **Standalone usability:** every image has a README with a copy-pasteable `docker run` example that works without any other rest-mail repo.

The `conventions` repo also ships a small lint script (bash or Go) that any image repo can run in CI to verify compliance: env var prefix matches dir name, healthcheck exists, no supervisord-for-one-process, README has the expected sections.

## Phased plan

### Phase 0 — Prep

- [ ] Confirm `github.com/rest-mail` org has the access needed to create N new repos.
- [ ] Confirm container registry choice (GHCR `ghcr.io/rest-mail/*` is the natural default).
- [ ] Audit current `docker/` for any rest-mail-specific code that snuck into "not ours" daemons (e.g. custom postfix milters wired to restmail). These are decision points: rewrite as generic, or keep in restmail.
- [ ] Write `conventions/CONVENTIONS.md` v0.1 (the bullet list above, expanded). This is the contract; no image work starts until this is written.

### Phase 1 — Pilot one image end-to-end

Pick the simplest daemon (likely **dnsmasq** or **certgen**) and take it all the way through:

- [ ] Create `rest-mail/reference-dnsmasq` repo
- [ ] Move `docker/dnsmasq/` contents in
- [ ] Refactor entrypoint to match conventions (overlay path, env vars, signal handling)
- [ ] Add `Dockerfile`, `README.md`, `LICENSE`, `.github/workflows/build.yml` (build + push to GHCR on tag)
- [ ] Tag `v0.1.0`, verify image pulls
- [ ] Update `testbed` (still in this repo at this stage) to consume the published image
- [ ] Validate end-to-end that mail1/mail2/mail3 still resolve `*.test` correctly

This phase exists to **shake out the convention doc and the CI template** before doing it eight more times.

### Phase 2 — Extract remaining reference images (parallelizable)

Repeat the Phase 1 process for each:

- [ ] `reference-certgen`
- [ ] `reference-postfix`
- [ ] `reference-dovecot`
- [ ] `reference-rspamd`
- [ ] `reference-clamav`
- [ ] `reference-fail2ban`
- [ ] `reference-postgres-mail` (or document use of vanilla `postgres:16`)

Each repo:
- Self-contained Dockerfile + entrypoint + minimal default config
- README with standalone `docker run` example
- CI builds + pushes tagged image to GHCR
- Lint passes against `conventions` repo's checker

### Phase 3 — Build the composers

#### `rest-mail/testbed`
- [ ] `docker-compose.yml` — creates `mailnet` network, runs `reference-dnsmasq` + `reference-certgen` + (optional, behind flag) shared `reference-clamav`
- [ ] `helm/` chart for kind/k3s — same services, same network contract
- [ ] README documents: how restmail and reference-mailserver join `mailnet`, how to fetch the testbed CA cert
- [ ] `Taskfile.yml` with `up`, `down`, `up:full` (latter brings shared clamav)

#### `rest-mail/reference-mailserver`
- [ ] `docker-compose.yml` — pulls `reference-postfix` + `reference-dovecot` + postgres + per-instance `reference-rspamd` + `reference-fail2ban`. Joins `mailnet` as external.
- [ ] `helm/` chart — same shape for kind/k3s.
- [ ] `configs/mail1/` and `configs/mail2/` example config dirs (hostname, domain, TLS paths, postgres seed users, DKIM keys).
- [ ] README: "launch with `--config configs/mail1` for one server, run twice with different configs for a pair, point at `mailnet` from the testbed."
- [ ] No restmail-specific anything. Pure traditional Postfix+Dovecot stack.

### Phase 4 — Slim down `rest-mail-server` (this repo)

After Phase 3 ships and is verified working:

- [ ] Delete from this repo: `docker/postfix/`, `docker/dovecot/`, `docker/rspamd/`, `docker/clamav/`, `docker/fail2ban/`, `docker/dnsmasq/`, `docker/certs/`, `docker/postgres/seed-mail{1,2}.sql`, `reference/`.
- [ ] Strip from `docker-compose.yml`: `postgres-mail1`, `postgres-mail2`, `postfix-mail1`, `postfix-mail2`, `dovecot-mail1`, `dovecot-mail2`, `mail1-maildir`, `mail2-maildir`, `postgres-mail1-data`, `postgres-mail2-data`, `dnsmasq`, `certgen`, `rspamd`, `clamav`, `clamav-rest`, `fail2ban`. Mark `mailnet` as `external: true`.
- [ ] What stays: `api`, `smtp-gateway`, `imap-gateway`, `pop3-gateway`, `js-filter`, `postgres-mail3`, plus restmail's own Dockerfiles in `docker/smtp-gateway/`, `docker/imap-gateway/`, `docker/pop3-gateway/`, `docker/js-filter-sidecar/`, `docker/api-entrypoint.sh`.
- [ ] Update `Taskfile.yml`: `task up` now requires testbed running first; document the dependency.
- [ ] Optionally provide a `task all:up` convenience target that brings testbed → restmail → reference-mailserver in order.
- [ ] Update `docs/` to point at the upstream repos.

### Phase 5 — Helm chart for restmail

- [ ] Create `helm/` chart in this repo for production deployment of mail3 services.
- [ ] **Forcing function:** chart must deploy cleanly into the kind/k3s testbed without any testbed-aware values. If it does, separation is real.
- [ ] CI: deploy to ephemeral kind cluster + testbed chart, run smoke tests (SMTP HELO, IMAP CAPABILITY, REST API /health).

### Phase 6 — Website split

- [ ] Move `website/` contents to `rest-mail/website` repo.
- [ ] Update any cross-references (links from this repo's README, deploy pipelines).
- [ ] Delete `website/` from this repo.

## Decisions (resolved 2026-04-28)

1. **Container registry:** GHCR (`ghcr.io/rest-mail/*`).
2. **License:** MIT for all repos under the org.
3. **Versioning:** Calver everywhere — `YYYY.MM.DD` (with `.N` suffix if multiple releases in one day, e.g. `2026.04.28.2`).
   - Pros: zero ambiguity about "which is newer," no semver bikeshedding ("is this a breaking change?"), tag = release date is self-documenting.
   - Daemon wrappers also publish a parallel tag exposing the upstream version for discoverability: `reference-postfix:3.8.4` (mutable, points at latest wrapper for postfix 3.8.4) alongside the immutable `reference-postfix:2026.04.28`. Pin calver in production, use upstream-version tag for "give me the latest 3.8.x."
   - `:latest` tracks newest calver tag in each repo.
   - For composer repos pinning daemon images, pin to calver — it's the only fully reproducible identifier.
4. **Convention enforcement:** defer the lint script. Capture as a TODO in the `conventions` repo. Revisit if drift across reference-* repos becomes an issue.
5. **`reference-postgres-mail`:** **not a separate image.** Use vanilla `postgres:16`. The `reference-mailserver` composer mounts schema/seed SQL into `/docker-entrypoint-initdb.d/` from `configs/<server>/`. No custom postgres image until there's real value to bake in.
6. **Shared services in the testbed:** clamav and (when shared) rspamd are run **at the testbed level** as singletons on `mailnet`. The `reference-rspamd` and `reference-clamav` images are not aware of this — sharing is purely a composer-level decision. The testbed brings them up; reference-mailserver consumes them via DNS name.
7. **Branch strategy:** commit/PR current in-flight work on `main` first. Then branch `decompose/phase-0` off clean `main`. Each phase lands as its own PR/branch.

## Risks / things that will hurt

- **Network contract drift.** `mailnet` CIDR, dnsmasq config schema, cert paths — if any of these change in `testbed` without coordination, every consuming repo breaks. Mitigation: pin testbed version in restmail's docs; document the contract explicitly in `conventions/`.
- **CI/release coordination.** N repos × tags × image registry pushes. Worth investing in a small release script or reusable workflow in `conventions` that all image repos call.
- **Local dev friction during transition.** Until Phase 4 lands, devs have a hybrid setup (some images from upstream, some still embedded). Keep transition phases short to minimize this.
- **Discoverability.** N repos are harder for new contributors to find than one. Mitigation: a top-level README in `rest-mail-server` and on the `rest-mail` org profile page that maps the territory.

## Success criteria

- `helm install rest-mail-server` against a real cluster works without any testbed artifact present.
- `docker run rest-mail/reference-postfix:latest` works standalone with a mounted config — no rest-mail context required.
- A new contributor can `git clone rest-mail/testbed && task up` and `git clone rest-mail/rest-mail-server && task up` and have a working dev environment, with optional `git clone rest-mail/reference-mailserver && task up` for cross-stack peers.
- Adding a third reference peer (mail3-traditional?) takes adding a config dir, no code changes.
