# rest-mail docs — index & map

This directory holds two very different kinds of document, and telling them apart
is the whole point of this file:

- **Live reference** and the **active plan** describe how rest-mail works (or is
  about to work) **today**. They are kept current; treat them as authoritative.
- **Archive**, **dated snapshots**, and most of **upstream** are **point-in-time
  history**. They were true when written and are never updated afterward.

> **Anti-rot contract.** Anything under `plans/archive/`, `reports/`,
> `review/`, and `security-review/` is a historical snapshot. **Do not read it as
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

## Active plan (forward-looking — not yet all shipped)

| File | What it is |
|------|------------|
| [`plans/2026-07-23-parameterization-roadmap.md`](plans/2026-07-23-parameterization-roadmap.md) | The single live roadmap: what is already parameterized, the remaining gaps, and the proposed PR sequence. Describes **planned** work (e.g. manifest `smtp:`/`tls:` blocks, internal mTLS) — these are proposals, not current behavior. |

## Archive (historical — not maintained)

Superseded plans and completed stage/design/implementation docs, kept for
provenance. Do not read as current state.

| Path | What it is |
|------|------------|
| `plans/archive/dated/**` | Dated design + implementation notes (Feb–Jul 2026), including the superseded `2026-07-21-parameterization.md` and the completed `2026-04-28-upstream-decomposition.md`. |
| `plans/archive/stages/**` | The original `STAGE_2`…`STAGE_8` build plans. |
| `plans/archive/top-level/**` | The founding `PLAN.md` and the `ADMIN`/`BACKUP`/`EAI` plans. |

## Dated snapshots (point-in-time — not maintained)

Reports and reviews that captured the state of the project on a specific date.
Accurate as of their date only.

| Path | What it is |
|------|------------|
| `reports/**` | Dated agent/architecture reports (e.g. the 2026-07-23 library-architecture and agent-team reports). |
| `review/2026-04-29-deep-review/**` | The 2026-04-29 deep review (architecture, parameterization, feature gaps, security, operations, recommendations). Its parameterization findings are superseded by the active roadmap. |
| `security-review/**` | The 2026-04-23 security reviews (codex/kimi/opus) and the instant-mail-check review. Point-in-time findings; check current code before acting on any item. |

## Upstream

Conventions and PRs relating to the wider `rest-mail` org (the extracted
libraries and reference images this repo composes).

| File | What it is | Status |
|------|------------|--------|
| [`upstream/CONVENTIONS.md`](upstream/CONVENTIONS.md) | The cross-repo conventions every `reference-*` image and extracted library obeys. | Reference |
| [`upstream/AUDIT.md`](upstream/AUDIT.md) | The 2026-04-28 audit that scoped what to extract from the monorepo. | Historical snapshot |
| [`upstream/POST-DECOMPOSE.md`](upstream/POST-DECOMPOSE.md) | Follow-up questions to resolve after the upstream split. | Historical snapshot |
| [`upstream/go-smtp-ehlo-capability-pr.md`](upstream/go-smtp-ehlo-capability-pr.md) | The `ExtraCaps` EHLO patch, submitted upstream as [emersion/go-smtp#303](https://github.com/emersion/go-smtp/pull/303). rest-mail stays on the `rest-mail/go-smtp` fork (via `replace` in `go.mod`) until/unless it merges. | Live (tracks an open PR) |
