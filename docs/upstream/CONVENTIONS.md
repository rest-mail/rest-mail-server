# Reference Image Conventions

**Status:** v0.1 (draft)
**Scope:** every `reference-*` image published under `ghcr.io/rest-mail/`

This document is the contract that every reference image obeys. Composer repos (`testbed`, `reference-mailserver`, `rest-mail-server`) consume images by tag and rely on this contract. Anyone outside the rest-mail org should be able to use a single image standalone with nothing more than this document.

---

## 1. One image, one daemon

- One major daemon per image (postfix, dovecot, rspamd, clamav, fail2ban, dnsmasq, certgen).
- No bundled "everything-in-one" images. If you need two daemons together, run two containers and connect them over a network.
- Co-process exception: a daemon that genuinely needs a syslog forwarder (e.g. postfix → rsyslog → stdout) may include the forwarder in the same image, but only as a transparent log relay — not as a second feature.

## 2. PID 1 and signals

- Default entrypoint is `exec <daemon>` — the daemon is PID 1.
- `supervisord` is forbidden when the container runs only one process. Use `tini` if you need proper SIGCHLD reaping for true multi-process containers (postfix + rsyslog).
- **SIGTERM** → graceful shutdown.
- **SIGHUP** → reload configuration *and TLS certificates* without restart. This is non-negotiable: cert-manager / Let's Encrypt rotate certs on disk, and the daemon must pick them up without a restart.

## 3. Configuration

- **Bake-in defaults:** the image ships sensible production-grade defaults that work standalone with zero config. `docker run ghcr.io/rest-mail/reference-<name>:latest` must start and serve.
- **Overlay path:** `/etc/<daemon>-overlay/` is the documented mount point for user config. The entrypoint merges overlay files on top of baked-in defaults at startup. Files in the overlay always win.
- **Templating:** if any baked-in default needs to reference an env var (hostname, domain, db host, cert paths), the image's entrypoint renders templates from `/etc/<daemon>-defaults.tmpl/` into `/etc/<daemon>/` at startup using simple `envsubst` or equivalent. No Jinja, no Helm, no extra runtime tools.
- **No restmail-specific defaults.** A daemon image must not assume restmail's database name, hostname, or CA filename. If a default is needed, use a neutral one (`mail`, `local-ca`, etc.).

## 4. Environment variables

Every image declares its env vars in its README with this exact shape:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|

Variable naming: `<DAEMON>_<PURPOSE>` — uppercase, daemon prefix, predictable.

Standard purposes (use these names where applicable, don't invent new ones):

| Variable suffix | Meaning |
|-----------------|---------|
| `_HOSTNAME` | Server's own hostname (e.g. `mail1.example.com`) |
| `_DOMAIN` | Primary domain served |
| `_DB_HOST`, `_DB_PORT`, `_DB_NAME`, `_DB_USER`, `_DB_PASSWORD` | Database connection |
| `_TLS_CERT`, `_TLS_KEY` | Paths to cert/key files inside the container |
| `_TLS_CA_PATH` | Path to a directory of trusted CA certs to add to the system trust store |
| `_LOG_LEVEL` | `debug` / `info` / `warn` / `error` |
| `_LISTEN_ADDR` | Bind address (default `0.0.0.0`) |

If a daemon uses something genuinely outside this list, prefix it the same way (`POSTFIX_RELAYHOST`, `DOVECOT_AUTH_MECHANISMS`, etc.).

## 5. Logging

- **stdout/stderr only.** No `syslog` daemon writing to files inside the container, no log file rotation. Container logs are the host's problem.
- **Format:** prefer structured (JSON or key=value) where the daemon supports it. Plain text is acceptable when the daemon doesn't.
- **Log level controlled** by `<DAEMON>_LOG_LEVEL` env var.

## 6. Healthcheck

- Every image ships a `HEALTHCHECK` instruction in the Dockerfile.
- Healthcheck **protocol-tests** the daemon — not `pgrep`, not `pidof`. Examples:
  - postfix → `postconf mail_version` (verifies postfix is configured + reachable)
  - dovecot → `doveadm who` or netcat to `localhost:143` reading `* OK`
  - rspamd → `rspamadm control stat` or HTTP `GET /ping`
  - dnsmasq → `dig @127.0.0.1 localhost`
- Reasonable defaults: `--interval=30s --timeout=5s --start-period=10s --retries=3`.

## 7. User and permissions

- Run as a **non-root user** when the daemon allows it. Document the UID:GID in the README.
- When the daemon must start as root (postfix master), drop privileges per its own model — do not run forever as root.
- Volume mount points have predictable permissions. Document any chown needs.

## 8. TLS

- Mount certs as files (not env-injected base64 blobs).
- Default mount paths: `/certs/<hostname>.crt`, `/certs/<hostname>.key`. Override via `<DAEMON>_TLS_CERT` / `<DAEMON>_TLS_KEY`.
- A CA bundle can be mounted at `<DAEMON>_TLS_CA_PATH` (default `/certs/ca.d/`); the entrypoint adds these to the system trust store at startup. CA files are added with neutral names (`local-ca.crt`), never product-specific (`restmail-ca.crt`).
- SIGHUP reloads certs without restart (see §2).

## 9. Image tags and architectures

- **Multi-arch.** Every image is published as a manifest list covering at least `linux/amd64` and `linux/arm64`. Apple Silicon, ARM cloud hosts, and most CI runners are arm64; missing arm64 silently breaks pulls on those platforms. Build via `docker/setup-qemu-action` + `docker/build-push-action` with `platforms: linux/amd64,linux/arm64`.
- **Calver:** `YYYY.MM.DD`, with `.N` suffix for multiple releases on the same day (`2026.04.28.2`).
- **Daemon wrappers** also publish a mutable upstream-version tag (e.g. `reference-postfix:3.8.4`) pointing at the latest wrapper for that upstream version. Composers pin calver; humans use upstream-version for "latest patch."
- **`:latest`** points at the newest calver tag.
- A breaking change to the overlay/env contract bumps the **month** segment to act as a visual cue (e.g. last release `2026.04.28`, breaking change ships as `2026.05.01` even if 28 days haven't passed).

## 10. Repository layout

Every `reference-<name>` repo follows this layout:

```
reference-<name>/
├── Dockerfile
├── entrypoint.sh
├── defaults/                 # baked-in default config (templates if needed)
├── README.md                 # standalone usage + env var table + healthcheck description
├── LICENSE                   # MIT
├── CHANGELOG.md              # human-readable, reverse-chronological by calver tag
├── .github/workflows/
│   ├── build.yml             # builds + pushes to ghcr.io on tag push
│   └── lint.yml              # (future) convention compliance check
└── tests/                    # smoke test: docker run + healthcheck passes
```

## 11. Standalone usability

- README opens with a copy-pasteable `docker run` example that works **with no other rest-mail repo present** and produces a useful running daemon.
- README never assumes the reader is using the `testbed`, `reference-mailserver`, or `rest-mail-server` repo. Those are downstream consumers, not preconditions.

## 12. License

MIT. Same across the org. Image attribution to upstream daemon authors goes in `LICENSE-THIRD-PARTY.md` if needed.

---

## Lessons learned from prior images

A growing list of footguns surfaced while building these images. Each entry is a thing the convention-lint script (deferred per [POST-DECOMPOSE.md](POST-DECOMPOSE.md) item 3) should eventually catch.

- **Don't over-specify "listen everywhere" defaults.** When a daemon already listens on all interfaces by default, *don't add a config line trying to make that explicit.* `listen-address=0.0.0.0` in dnsmasq doesn't mean "wildcard" — it tries to bind that as a literal address and silently fails to answer queries. Caught in `reference-dnsmasq` 2026.04.28; the fix was to delete the line entirely and rely on the upstream default. General rule: if the daemon's default behavior is what you want, don't write config for it.

- **Wrapping a non-root upstream image: fix dir ownership for runtime overlays.** When you `FROM` an upstream image that runs as a non-root user (rspamd's `_rspamd`, postgres's `postgres`, etc.) and your entrypoint writes config files at runtime, the directories need to be writable by that user. Use `USER root` + `COPY --chown=<user>:<group>` + `RUN chown -R <user>:<group> <dir>` + `USER <user>`, or your entrypoint will hit `Permission denied` the moment a user mounts an overlay. Caught in `reference-rspamd` 2026.04.28 (couldn't write `/etc/rspamd/local.d/logging.inc` at startup).

- **Multi-file template rendering needs explicit iteration.** A daemon's config often splits across multiple files (e.g. dovecot's `dovecot.conf` + `dovecot-sql.conf.ext`). A single `envsubst < tmpl > out` only renders one file and silently leaves siblings un-templated. Stage all templates under `/etc/<daemon>-defaults.tmpl/` and loop:

  ```sh
  for f in /etc/<daemon>-defaults.tmpl/*; do
    envsubst < "$f" > "/etc/<daemon>/$(basename "$f")"
  done
  ```

  Caught in `reference-dovecot` 2026.04.28.

- **`exec` and PID 1 are non-negotiable for signal handling.** Dropping supervisord (per §2) is only half the work — your entrypoint must `exec <daemon>` rather than calling it as a child of the shell. Without `exec`, signals reach the shell instead of the daemon, breaking SIGTERM graceful shutdown and SIGHUP cert reload (§8). Tini as PID 1 + `exec` in the entrypoint is the right combo.

- **Smoke tests must probe the protocol, not just the TCP port.** A daemon's listener can be open seconds before it's ready to greet clients (postfix under supervisord takes 5–8s after the socket binds). And on macOS, `nc` closes its connection on stdin EOF, which postfix's anti-pipelining check rejects with `improper command pipelining after CONNECT` — you'll never see the 220 banner. Always: poll for the actual protocol greeting, and where the daemon is sensitive to pipelining, insert `sleep` between client writes (`{ sleep 2; printf 'EHLO ...'; sleep 1; printf 'QUIT'; sleep 1; } | nc host port`). Caught in `reference-postfix` 2026.04.28.

- **Don't depend on amd64-only upstream images for multi-arch work.** Some upstream images on Docker Hub publish only `amd64` (notably `clamav/clamav` at the time of writing). Inheriting `FROM` such an image silently fails the multi-arch build — you can't reach `arm64` if the parent doesn't. Check `docker manifest inspect <upstream>` for `arm64` *before* `FROM`-ing it. If the upstream is amd64-only, build from a multi-arch base (`alpine`, `debian-slim`) and install the package via the OS package manager instead. Caught in `reference-clamav` 2026.04.28 (yanked initial release; rebuilt on alpine).

### Composer-side lessons

For composer repos (`testbed`, `reference-mailserver`, etc.) that wire reference images together — separate concerns from image-building.

- **Document the network/volume names explicitly; they're the contract.** Composers create resources with names like `<COMPOSE_PROJECT_NAME>_mailnet` and `<COMPOSE_PROJECT_NAME>_certs`. Downstream consumers join via `external: true` referencing those exact names. Without explicit docs, consumers either guess wrong (`networks: { mailnet: ... }` defaults to `<their_project>_mailnet` and silently fails to find the resource) or hard-code the testbed's project name. State the canonical names + the override knob (`COMPOSE_PROJECT_NAME`) in the README's first 100 lines. Caught in `rest-mail/testbed` and `rest-mail/reference-mailserver` 2026.04.28.

- **Bind-mounted overlay dirs must exist on disk before `up`.** Compose can't synthesize a missing source directory for a bind mount — it fails immediately with a cryptic error. Each per-instance config dir needs a `.gitkeep` (or actual content) so `git clone && task up` works on first run, even when the user hasn't customized any overlays yet. Caught in `rest-mail/reference-mailserver` 2026.04.28.

- **Parameterized composers need `COMPOSE_PROJECT_NAME` per parameter.** When the same compose stack is launched twice with different config dirs (e.g. `mail1` and `mail2`), they collide on container names unless each invocation gets its own project name. Bake this into the Taskfile (`COMPOSE_PROJECT_NAME=mailref-${CONFIG}`) — *and* warn in the README that direct `docker compose` invocations need to do the same.

- **Subnet collisions are silent until `up`.** Two composers both wanting `10.99.0.0/16` fail with "Pool overlaps with other one." Only one composer at a time owns `mailnet`; downstream consumers always join `external: true`, never recreate. Worth a one-liner in every composer's README.

- **Composers can't use a reference image to run client diagnostics.** Reference images run their daemon as PID 1 — running `dig`/`openssl`/`curl` against them via `docker exec` works, but spinning up a fresh container of the daemon image with a custom command (`docker run --rm reference-dnsmasq dig ...`) fails because the entrypoint expects the daemon's args, not arbitrary commands. For diagnostic shims, use a neutral tools image (`alpine:3.20 + apk add bind-tools openssl curl`). Caught in `rest-mail/testbed` 2026.04.28.

- **Helm: keep testbed knowledge in a separate values file, not inline conditionals.** When the same chart needs to deploy against real cloud infra and against a kind+testbed dev cluster, the temptation is to add `if .Values.testbed.enabled` blocks throughout the templates. Don't. Templates stay neutral and reference values like `tls.secretName`, `networking.dnsPolicy`, and `*.service.type` directly; a `values-dev.yaml` file overlays the testbed-specific values (dnsmasq IP `10.99.0.10` in `dnsConfig.nameservers`, NodePort instead of LoadBalancer, inline cleartext creds, the `mail3.test` hostname). Forcing function: `helm template <chart> | grep -E "<testbed-IPs|test-domains|dnsmasq>"` must return nothing for the production path. Caught while building `rest-mail-server/helm/restmail` 2026.04.28.

- **Helm: render-time `fail` on missing required values trades trivially-passing forcing-function greps for safety.** A required-with-`{{ fail }}` check refuses to render the chart when an operator forgets to set hostname/domain — but it also makes `helm template <chart> | grep ...` return nothing because the template errored. The leak check passes vacuously. Either provide neutral example defaults (`mail.example.com`) so the forcing-function grep is meaningful AND keep documentation that the values must be overridden, or invoke the chart with `--set` in the forcing-function command. Caught while wiring Phase 5 forcing-function checks 2026.04.28.

## Open items / future work

- **Lint script** to verify a repo follows this doc — deferred, will write when N>3 image repos exist and drift is observed.
- **Shared CI workflow** in the `conventions` repo (or a `.github` repo on the org) so each image's `build.yml` is `uses: rest-mail/conventions/.github/workflows/build-image.yml@v1` — DRY across repos.
- **SBOM and provenance** (cosign, SLSA) — defer until there's a real consumer asking for it.
