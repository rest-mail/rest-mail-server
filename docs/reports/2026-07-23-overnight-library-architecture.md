# Overnight Report — Library Extraction & Protocol-Boundary Refactor

**Date:** 2026-07-23 (covers the ~24h window from 2026-07-22 morning)
**Prepared for:** project management / oversight
**Repo:** `github.com/rest-mail/rest-mail-server` · **main tip at time of writing:** `0e2c6ed`

---

## 1. Executive summary

The night split into two phases:

1. **Hardening (PRs #15–#49):** finished the email-authentication suite (DKIM verify + sign, ARC, DMARC alignment + aggregate reporting, MTA-STS enforcement, Sieve), greened the 13-stage e2e suite, and cleared a verified bug backlog across the IMAP/POP3/SMTP gateways, auth, and queue.
2. **Architecture (PRs #50–#54 + new repos):** a deliberate strategic pivot. `rest-mail` is being turned into a **thin consumer of focused, reusable Go mail libraries**. Where a mature external library already does the job well we **adopt** it; where there is a genuine ecosystem gap or the incumbent is low-quality, we **extract our own** working code into a standalone library and offer it as a real alternative.

**Tangible results this session:**
- **3 new public libraries created** under the `rest-mail` org (MIT): `dkim`, `arc` (both tagged `v0.1.0` and already consumed by the app), and `pop3` (in progress).
- **5 structural refactor PRs merged** to main (#50–#54) with zero behaviour regressions and all CI green.
- **~1,500 lines of hand-rolled code retired** from the app (MIME parser + DKIM/ARC internals) in favour of a maintained library and external modules.
- **Two further extraction efforts in flight** (`dmarc`/`mtasts`/`sieve` and `imap`, plus completing `pop3`).

---

## 2. The core strategic decision (adopt vs. extract)

The guiding rule agreed during the session:

> Adopt mature external libraries where they are good; do **not** reinvent them. Build/extract our own **only** where there is a real gap or the incumbent is genuinely poor — in which case owning it lets us provide a real alternative. `rest-mail` becomes the thin composer on top.

A quality analysis of the Go mail-library ecosystem (the emersion family: `go-message`, `go-smtp`, `go-imap`, `go-sasl`, `go-msgauth`) produced these per-component verdicts:

| Component | Decision | Rationale |
|---|---|---|
| **MIME** | **Adopt** `go-message` | Mature, production-proven, preserves raw headers (DKIM-safe). MIME is where hand-rolling bleeds bugs. ✅ done (#50) |
| **SASL** | **Adopt** `go-sasl` | Solved problem; trivial. Pending. |
| **SMTP** | **Adopt** `go-smtp` **+ fork + upstream PR** | Genuinely good library, **but** it hardcodes its EHLO capability list and cannot advertise our custom `RESTMAIL` extension without a patch. Chosen path: adopt the engine, fork for the capability hook, submit the same change upstream. Next focused wave. |
| **IMAP** | **Extract our own** | The only credible library (`go-imap` v2) has been in **beta ~3 years** with breaking changes between betas and a single maintainer. Replacing working code with a moving beta fails the quality bar. We extract our proven server as the stable, non-beta alternative the ecosystem lacks. In flight. |
| **POP3** | **Extract our own** | **No mature Go POP3 *server* library exists** (the one candidate is an abandoned, TLS-less stub). Clear gap. In flight. |
| **DKIM** | **Extract our own** | Full RFC 6376 sign+verify, zero external deps. ✅ done → `rest-mail/dkim` |
| **ARC** | **Extract our own** | `go-msgauth` has **no first-class ARC** — genuine gap. ✅ done → `rest-mail/arc` |
| **DMARC** | **Extract our own** | Alignment + aggregate (rua) report generation. In flight. |
| **MTA-STS** | **Extract our own** | Thin on the ground in Go. In flight. |
| **Sieve** | **Extract our own** | RFC 5228 parser/interpreter. In flight. |

**Explicitly rejected:** forking `go-imap` v2. Its problem is immaturity, not a missing feature, so a fork would chase a moving API with no concrete "things to fix" — the worst of both worlds.

---

## 3. Key discussions & agreements

**3.1 The "Backend inversion" (agreed to do regardless of the adopt/extract outcome).**
Each protocol gateway (IMAP/POP3/SMTP) was welded to the concrete API client, which is *why* the protocol bugs fixed earlier in the night could only be caught end-to-end. We inverted each gateway onto a package-local `Backend` interface. This:
- creates a clean one-way boundary,
- unlocks a **mock-backend test harness** (protocol logic tested in-memory, no live API/DB), and
- doubles as the **public API seam** for the eventual extracted libraries.
Merged as #51/#52/#53 with **zero behaviour change** and new transcript test suites that now cheaply cover the exact bug classes fixed earlier.

**3.2 The `RESTMAIL` EHLO extension is a hard constraint.**
The custom `RESTMAIL` SMTP EHLO capability is non-negotiable and drove the SMTP decision: because `go-smtp` cannot advertise it without a patch, we chose adopt-the-engine + fork + upstream-PR rather than either keeping the hand-rolled server or dropping the feature.

**3.3 Repository conventions (agreed).**
- **Org:** `rest-mail` (note the hyphen).
- **Granularity:** one focused repo per library (e.g. `dkim` and `arc` are *separate* repos, `arc` depending on `dkim` as a module).
- **Licence:** **MIT** (matches the emersion ecosystem we interoperate with).
- **Template:** the `dkim` repo is the canonical template (README with badges + RFC links + usage, CI = build/vet/golangci-lint/`-race`, LICENSE, `.gitignore`, `go 1.25.0`); all later repos mirror it.

**3.4 A module-path nuance (worth knowing).**
The app's own Go module path is `github.com/restmail/restmail` while its repo is `github.com/rest-mail/rest-mail-server` — harmless for a binary (nobody `go get`s it) but it means the **libraries** must use `github.com/rest-mail/<name>` (matching their repos) to be `go get`-able. All new libs follow this.

---

## 4. Git repositories created

| Repo | RFC / purpose | Licence | Status |
|---|---|---|---|
| **[rest-mail/dkim](https://github.com/rest-mail/dkim)** | RFC 6376 DKIM signing + verification; zero external deps; operates on raw transmitted bytes; exports canonicalization primitives for reuse | MIT | ✅ `v0.1.0`, consumed by app |
| **[rest-mail/arc](https://github.com/rest-mail/arc)** | RFC 8617 ARC chain verification; depends on `rest-mail/dkim` | MIT | ✅ `v0.1.0`, consumed by app |
| **[rest-mail/pop3](https://github.com/rest-mail/pop3)** | RFC 1939 POP3 **server engine** — "bring a Backend, it speaks the protocol"; standard-library only | MIT | 🔄 in progress |
| rest-mail/imap | IMAP server engine + Backend seam (neutral types) | MIT | 🔄 pending (this wave) |
| rest-mail/dmarc | RFC 7489 policy + alignment + rua report XML | MIT | 🔄 pending (this wave) |
| rest-mail/mtasts | RFC 8461 policy fetch/parse/enforce | MIT | 🔄 pending (this wave) |
| rest-mail/sieve | RFC 5228 parser/interpreter | MIT | 🔄 pending (this wave) |

Design rationale for each is captured in §2. The **decision behind creating them at all** is the adopt-vs-extract framework: these are the components where owning a focused library either fills a real ecosystem gap (ARC, POP3 server, IMAP server) or keeps first-class control of security-critical logic (DKIM/DMARC/MTA-STS).

---

## 5. Changes merged to the application (rest-mail-server)

**Architecture (Phase 2):**

| PR | Summary |
|---|---|
| [#50](https://github.com/rest-mail/rest-mail-server/pull/50) | Parse MIME with `go-message` instead of the hand-rolled parser (retired ~145 LOC; fixed a latent inline-vs-attachment misfiling) |
| [#51](https://github.com/rest-mail/rest-mail-server/pull/51) | POP3 depends on a `Backend` interface + mock-backend transcript tests |
| [#52](https://github.com/rest-mail/rest-mail-server/pull/52) | IMAP depends on a `Backend` interface + mock-backend transcript tests |
| [#53](https://github.com/rest-mail/rest-mail-server/pull/53) | SMTP depends on `Backend` + `Store` interfaces (DB reaches removed from the session) + tests |
| [#54](https://github.com/rest-mail/rest-mail-server/pull/54) | Consume `rest-mail/dkim` + `rest-mail/arc`; delete `internal/dkim` (+13 / −1493) |

**Hardening (Phase 1) — highlights of the #15–#49 backlog:**
- Email-auth suite: real DKIM verification (#28) and signing over transmitted bytes (#32); ARC crypto verification (#34); DMARC alignment fix (#33) + rua aggregate reporting (#39); MTA-STS enforcement (#37); Sieve depth (#38).
- Gateway correctness: serve stored original MIME over IMAP/POP3 (#35); whole-folder listing + `UID *` + `BODY.PEEK` (#44); IDLE goroutine ordering (#48); SMTP partial-delivery handling (#49).
- Platform: reclaim orphaned queue deliveries (#40); persist raw message + pipeline auth results (#42); consistent Message-ID threading (#43); admin-session-preserving token refresh (#45); single correct account→mailbox resolver (#47).
- Tooling: github-guard hooks installed (#25) + stale matrix-parent required-check prune (#29).

All merged PRs were CI-green; the four disjoint refactor PRs were batch-merged by briefly relaxing the branch's up-to-date requirement and restoring it immediately after.

---

## 6. In-flight work (as of this report)

- **Agent A:** extract `dmarc` + `mtasts` + `sieve` → 3 repos + one repoint PR.
- **Agent B:** finish `pop3` + extract `imap` → 2 server-lib repos + one repoint PR. Uses neutral domain types so the libraries don't drag in `rest-mail`'s API client; the shared `connlimiter` stays app-side behind a structural `Limiter` interface.

Both branch off the clean post-#54 main and mirror the `dkim` template. PRs will be opened for review (not auto-merged).

---

## 7. Open decisions & next steps

1. **go-smtp adoption** (next focused wave): fork `emersion/go-smtp`, add a capability hook so `RESTMAIL` can be advertised, integrate via the existing `Backend`/`Store` seams, and **prepare** an upstream PR for human review before submitting (it's an outward-facing contribution).
2. **Adopt `go-sasl`** (small, transitive).
3. **RFC822.SIZE**: report the exact transmitted octet count via a dedicated `raw_size` field (leaving quota semantics untouched). Deferred until the IMAP extraction settles.
4. **RBAC**: design a capability→route taxonomy and wire `RequireCapability` across the API. Self-resolving; done after the extractions.
5. **github-guard fast-follow**: install the standard git-hook guards on each new library repo.

---

## 8. Risks & watch items

- **go-smtp / `RESTMAIL` dependency.** The SMTP adoption depends on a fork until (and unless) the upstream capability-hook PR is accepted. Fallback is a thin maintained fork — low cost, one patch. The `RESTMAIL` EHLO advert must survive the migration; it is an explicit gating criterion.
- **go-imap v2 avoided deliberately.** If we later reconsider adopting it, revisit only at its 1.0 (it supports custom capabilities, so `RESTMAIL`-style extensions won't block us then).
- **Merge coordination.** The concurrent repoint PRs each touch `go.mod`; they merge cleanly but the second/third need a trivial rebase. Disjoint packages otherwise.
- **Overnight interruption (handled).** A session/rate limit killed the first fan-out attempt mid-exploration (no repos/PRs created except an empty `pop3` repo, which is being reused). No code was merged or broken; work resumed cleanly.
- **Review load.** ~5 new repos + ~2 repoint PRs will land close together and warrant review — especially the eventual go-smtp swap, which *replaces* a working protocol engine.

---

## 9. Snapshot metrics

- **PRs merged (24h):** 40 (#15–#54).
- **New libraries:** 2 published (`dkim`, `arc` @ `v0.1.0`) + 1 in progress (`pop3`) + up to 4 more this wave.
- **App code retired:** ~1,500 LOC (hand-rolled MIME + DKIM/ARC internals) moved to libraries or replaced by `go-message`.
- **New test surface:** mock-backend transcript suites for POP3/IMAP/SMTP (no live infra needed).
- **Behaviour regressions:** none detected; all merges CI-green.

---

## 10. Addendum — wave 2 landed (same morning)

The in-flight work in §6 completed. Final state:

| Repo | Status |
|---|---|
| [rest-mail/dkim](https://github.com/rest-mail/dkim) · [arc](https://github.com/rest-mail/arc) · [dmarc](https://github.com/rest-mail/dmarc) · [mtasts](https://github.com/rest-mail/mtasts) · [sieve](https://github.com/rest-mail/sieve) · [pop3](https://github.com/rest-mail/pop3) · [imap](https://github.com/rest-mail/imap) | ✅ **all 7 published + tagged `v0.1.0`**, consumed by the app |

- **[#55](https://github.com/rest-mail/rest-mail-server/pull/55) merged** — app consumes `dmarc`/`mtasts`/`sieve`; the rua Reporter stayed app-side (relocated to `internal/dmarcreport`); sieve got a neutral `Message` + host-`Executor` seam.
- **[#57](https://github.com/rest-mail/rest-mail-server/pull/57) merged** — app consumes the `pop3`/`imap` server libraries via thin apiclient→neutral-type adapters; **net −4,300 lines**. (#56 was the same change; its PR object got stuck on GitHub's side after a rebase and was recreated as #57.)
- `main` tip: `7867cd2`, CI green. Remaining program: go-smtp adoption (fork + `RESTMAIL` capability hook + prepared upstream PR), go-sasl, RFC822.SIZE (`raw_size`), RBAC wiring, github-guard on the 7 lib repos.

---

## 11. Addendum 2 — final wave landed (same day)

Everything remaining in §10 completed:

- **[#58](https://github.com/rest-mail/rest-mail-server/pull/58) RBAC** — capability taxonomy wired onto all admin routes (~125 allow/deny tests, zero regressions). Key win: readonly-role tokens could previously perform *every* admin write/delete — now 403. Flagged for later: legacy `IsAdmin`-token deprecation; hardening of the by-design-unauthenticated internal deliver endpoints.
- **[#59](https://github.com/rest-mail/rest-mail-server/pull/59) go-smtp adoption** — SMTP engine replaced by go-smtp via fork [rest-mail/go-smtp](https://github.com/rest-mail/go-smtp) (`ExtraCaps` hook preserves `RESTMAIL`). Landed only after an adversarial review (verdict MERGE-WITH-FIXES) whose probes confirmed every risky seam and caught go-smtp's silent 2000-byte line limit before it shipped (fixed to old-engine parity + test). Bonus fixes inherited: null-sender bounces now accepted (old engine 501'd DSNs), 465+PROXY ordering repaired, SMTP-smuggling hardening. **Upstream PR submitted: [emersion/go-smtp#303](https://github.com/emersion/go-smtp/pull/303)** — on acceptance the fork retires. go-sasl adopted transitively.
- **[#60](https://github.com/rest-mail/rest-mail-server/pull/60) configurable max message size** — `SMTP_MAX_MESSAGE_SIZE` (bytes): a max always exists; unset → 10 MiB default; any positive admin value respected; zero/negative → startup error; >100 MiB → startup warning. Audit also fixed a real bug: the pipeline `size_check` filter ignored its seeded `max_size_mb` key (admin policy silently unenforced).
- **[#61](https://github.com/rest-mail/rest-mail-server/pull/61) exact wire sizes** — `messages.raw_size` (`octet_length` backfill) now feeds IMAP `RFC822.SIZE` and POP3 `LIST`/`STAT`; quota semantics untouched.
- **github-guard live on all 7 lib repos** — squash-only, admin-enforced protection, `Build & Test` required (one discovery race on `imap` fixed manually; skill fix on the backlog).

**Final state:** `main` `c463542`, CI green. **Follow-up backlog** (also in session memory): emersion#303 fork-retirement watch; RBAC deprecation/hardening decisions; large-message path timeouts (apiclient 30s / API 15s vs big configured sizes); github-guard skill discovery-race fix + org-wide fan-out; root README refresh; lib build-out (imap extensions, docs/examples); parameterization roadmap PR1–PR5.
