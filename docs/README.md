# rest-mail docs — index & map

This directory holds two very different kinds of document, and telling them apart
is the whole point of this file:

- **Live reference** describes how rest-mail works **today**. It is kept current;
  treat it as authoritative.
- **Dated snapshots** and most of **upstream** are **point-in-time history**.
  They were true when written and are never updated afterward.

> **Anti-rot contract.** Anything under `review/` and `security-review/` is a
> historical snapshot. **Do not read it as
> the current state of the code**, do not "fix" it to match today's code, and do
> not cite it as how the system behaves now. If a snapshot and the code disagree,
> the code wins and the snapshot is simply old. Only the files listed under **Live
> reference** below are maintained against the code.

## Live reference (authoritative — keep current)

Files in `docs/` root. These are verified against the code and updated when the
code changes.

| File | What it is |
|------|------------|
| [`MANUAL.md`](MANUAL.md) | Operator + developer manual: dev stack, env-var contract, endpoints, credentials, day-to-day tasks. |
| [`adapter-filters.md`](adapter-filters.md) | Pipeline filters — the built-in Go filters vs the external scanning adapters (rspamd, ClamAV), how they register, and how to write a custom adapter. |
| [`dns-providers.md`](dns-providers.md) | Configuring the DNS provider integration. |
| [`fail2ban-setup.md`](fail2ban-setup.md) | The two-layer IP ban system (in-memory connlimiter + DB `bans` table) and external fail2ban integration. |
| [`proxy-protocol.md`](proxy-protocol.md) | PROXY protocol support for running behind HAProxy/nginx. |

## Dated snapshots (point-in-time — not maintained)

Reviews that captured the state of the project on a specific date. Accurate as
of their date only.

| Path | What it is |
|------|------------|
| `review/2026-04-29-deep-review/**` | The 2026-04-29 deep review (architecture assessment, feature gaps, the "ugly side", security and compliance, operations gaps). |
| `security-review/**` | The Instant Mail Check security review. Point-in-time findings; check current code before acting on any item. |

## Upstream

Conventions and PRs relating to the wider `rest-mail` org (the extracted
libraries and reference images this repo composes).

| File | What it is | Status |
|------|------------|--------|
| [`upstream/CONVENTIONS.md`](upstream/CONVENTIONS.md) | The cross-repo conventions every `reference-*` image and extracted library obeys. | Reference |
| [`upstream/AUDIT.md`](upstream/AUDIT.md) | The 2026-04-28 audit that scoped what to extract from the monorepo. | Historical snapshot |
