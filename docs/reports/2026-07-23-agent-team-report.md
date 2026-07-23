# Agent Team Report — Parallel Agent Fleet, 2026-07-23

**Companion to:** [2026-07-23-overnight-library-architecture.md](2026-07-23-overnight-library-architecture.md)
**Scope:** every parallel background agent deployed in the ~24h window — missions, outcomes, incidents, and how interrupted work was recovered.

---

## 1. Executive summary

**16 agent deployments across 3 waves.** 11 completed with all artifacts merged; 1 was deliberately stopped on a strategy realignment; 4 were interrupted by infrastructure events (2 by a session rate limit, 2 by a Claude Code process restart) — **no merged work was ever lost or broken**, and the 2 interrupted-in-progress agents have been resumed from their saved transcripts and are active now.

`main` stayed green through every incident. All agent work landed exclusively via CI-gated PRs; no agent ever pushed to `main` directly.

---

## 2. How the team was organized

- **Isolation:** every code-writing agent ran in its own git worktree on its own branch — never in the main checkout.
- **Landing rule:** PR + full CI only. Merges were performed centrally (batch-merged when PRs were disjoint), never by the agents.
- **Sequencing gates:** the flagship (`dkim`/`arc`) validated the library template *before* the fan-out wave copied it; the Backend inversion merged *before* the protocol extractions that depend on it; the riskiest change (go-smtp engine swap) was deliberately **excluded** from parallel waves and reserved for a focused, closely-reviewed deployment.
- **Concurrency control:** simultaneous agents were assigned disjoint packages; the only shared file is `go.mod` (trivial rebase on merge).

---

## 3. Roster & outcomes

### Wave 0 — hardening (bug backlog, protocol fidelity, features)

| Agent | Mission | Outcome | Artifacts (merged) |
|---|---|---|---|
| Bug hunter | Sweep the whole codebase for defects; verify and feed them to the fixer | ✅ completed | The verified findings list that became the backlog below |
| Dev-loop fixer | Work the verified bug backlog autonomously (test → fix → test, baseline contract) | ✅ completed | [#40](https://github.com/rest-mail/rest-mail-server/pull/40), [#42](https://github.com/rest-mail/rest-mail-server/pull/42)–[#49](https://github.com/rest-mail/rest-mail-server/pull/49) |
| IMAP/POP3 fidelity | Serve stored original MIME instead of a lossy rebuild | ✅ completed | [#35](https://github.com/rest-mail/rest-mail-server/pull/35) |
| Stats/MIME correctness | Dashboard stats + MIME body-extraction bugs | ✅ completed | [#36](https://github.com/rest-mail/rest-mail-server/pull/36) |
| MTA-STS | Enforce recipient MTA-STS policy on outbound | ✅ completed | [#37](https://github.com/rest-mail/rest-mail-server/pull/37) |
| Sieve depth | Deepen RFC 5228 coverage | ✅ completed | [#38](https://github.com/rest-mail/rest-mail-server/pull/38) |
| Code quality | Readability pass over `internal/` + `cmd/` | ✅ completed | [#41](https://github.com/rest-mail/rest-mail-server/pull/41) |

*(The email-auth suite itself — #28–#34, #39 — was driven in the main session alongside this fleet.)*

### Wave 1 — architecture pivot

| Agent | Mission | Outcome | Artifacts |
|---|---|---|---|
| Library quality analyst | Evidence-based adopt-vs-build assessment of the Go mail ecosystem (emersion family) | ✅ completed (research-only) | The per-component verdicts in §2 of the architecture report |
| go-smtp migrator *(premature)* | Began migrating SMTP onto go-smtp | ⏹️ **deliberately stopped** — direction was realigned to "analyze library quality first"; its findings fed the later adopt+fork+upstream decision | none landed (dirty worktree retained for reference) |
| go-message adopter | Replace hand-rolled MIME parser with `go-message` | ✅ completed | [#50](https://github.com/rest-mail/rest-mail-server/pull/50) |
| Backend inverter | Invert pop3/imap/smtp onto `Backend` interfaces + mock-backend transcript tests | ✅ completed | [#51](https://github.com/rest-mail/rest-mail-server/pull/51) [#52](https://github.com/rest-mail/rest-mail-server/pull/52) [#53](https://github.com/rest-mail/rest-mail-server/pull/53) |
| Flagship extractor | Extract `dkim` + `arc` as standalone libraries; set the repo template | ✅ completed | [rest-mail/dkim](https://github.com/rest-mail/dkim) + [rest-mail/arc](https://github.com/rest-mail/arc) `v0.1.0`, [#54](https://github.com/rest-mail/rest-mail-server/pull/54) |

### Wave 2 — extraction fan-out (the eventful one)

| Agent | Mission | Attempt 1 | Attempt 2 | Now |
|---|---|---|---|---|
| Domain extractor | `dmarc` + `mtasts` + `sieve` → 3 repos + repoint PR | ❌ killed by **rate limit** mid-exploration (read-only; no artifacts) | ⚠️ interrupted by **process restart** — but all 3 libs already **published + tagged v0.1.0**; worktree survived | ▶️ **resumed**: finishing the repoint PR |
| Protocol extractor | `pop3` + `imap` server libs → 2 repos + repoint PR | ❌ killed by rate limit; left one empty `pop3` repo (reused) | ⚠️ interrupted by restart — `pop3` **published + tagged v0.1.0**; `imap` repo created but empty | ▶️ **resumed**: building `imap`, then the repoint PR |

---

## 4. Incident log

**Incident 1 — deliberate stop (strategy realignment).**
The first go-smtp migration agent was launched before the adopt-vs-build question was settled. When the direction crystallized ("analyze incumbent quality first; RESTMAIL EHLO is non-negotiable"), the agent was stopped mid-task rather than allowed to land a premature migration. Cost: one discarded worktree. Benefit: the subsequent quality analysis discovered go-smtp *cannot* advertise the RESTMAIL capability without a patch — which reshaped the entire SMTP plan.

**Incident 2 — session rate limit (08:30, reset 08:50 Berlin).**
Both wave-2 agents were killed in their read-only exploration phase. Damage: one empty GitHub repo (`pop3`, later reused). Response: audited remote state, armed an auto-resume timer for the reset window; the user unblocked earlier and both agents were relaunched with their partial findings (e.g. "`connlimiter` is shared → keep app-side behind a `Limiter` interface") baked into the fresh specs.

**Incident 3 — Claude Code process restart.**
Killed both wave-2 attempt-2 agents mid-publish. Because published+tagged libraries act as durable checkpoints, the recovery audit (repos, tags, PRs, worktrees) showed 4 of 5 libraries fully published and nothing half-merged. Both agents were **resumed from their saved transcripts** with explicit "verified done / remaining work" handoffs so nothing is redone or duplicated.

---

## 5. Why the interruptions stayed cheap

1. **Worktree isolation** — an interrupted agent cannot corrupt the main checkout or other agents' work.
2. **PR-only landings on a protected `main`** — an interrupted agent cannot leave `main` broken; it stayed at green `0e2c6ed` through all three incidents.
3. **Tags as checkpoints** — a `v0.1.0` tag is proof a library is complete; recovery audits trust the remote state, not memory.
4. **Saved transcripts** — stopped agents resume with full context instead of restarting from zero.
5. **Disjoint-package assignment** — concurrent agents can only ever collide on `go.mod`, which is a trivial rebase.

---

## 6. Current fleet status & planned deployments

**Active now (2):** the two resumed wave-2 extractors (repoint PRs pending — will be opened for review, not auto-merged).

**Planned next:**
- **go-smtp adoption wave** (single focused agent, closely reviewed): fork `emersion/go-smtp`, add the capability hook for `RESTMAIL`, integrate via the merged `Backend`/`Store` seams, *prepare* the upstream PR for human sign-off before submission.
- go-sasl adoption; RFC822.SIZE (`raw_size`) and RBAC fixes; github-guard hook install across the 7 new library repos.

**Housekeeping:** several stale wave-0 worktrees remain in `.claude/worktrees/` (their branches are merged) plus the dirty worktree from the stopped go-smtp attempt — cleanup pending, zero risk.

---

## 7. Fleet metrics

| Metric | Value |
|---|---|
| Agent deployments (24h) | 16 (across 3 waves) |
| Completed, artifacts merged | 11 |
| Deliberately stopped (direction change) | 1 |
| Interrupted by infrastructure | 4 (2 rate-limit, 2 process-restart) → 2 resumed & active |
| Merged PRs produced by agents | 19 (#35–#38, #40–#54) |
| Public libraries published by agents | 6 tagged `v0.1.0` (`dkim`, `arc`, `pop3`, `dmarc`, `mtasts`, `sieve`) + `imap` in progress |
| Damage to `main` from any incident | none — green throughout |

---

## 8. Addendum — wave 2 completed (same morning)

Both resumed extractors finished after this report was written:

- **Domain extractor** ✅ — `dmarc`/`mtasts`/`sieve` v0.1.0 + repoint [#55](https://github.com/rest-mail/rest-mail-server/pull/55) (merged).
- **Protocol extractor** ✅ — `pop3`/`imap` v0.1.0 + repoint [#57](https://github.com/rest-mail/rest-mail-server/pull/57) (merged; net −4,300 lines). One extra incident: after rebasing the branch onto the freshly-merged #55, the original PR #56's GitHub object never re-synced and couldn't be reopened — recreated as #57, whose CI then sat in a GitHub Actions backlog before going green.

**Updated fleet metrics:** 16 deployments → **13 completed** (all artifacts merged), 1 deliberately stopped, 2 rate-limit kills (attempt 1, superseded); **21 merged PRs** (#35–#38, #40–#55, #57); **7 libraries** published + tagged `v0.1.0`; `main` green throughout at `7867cd2`.

---

## 9. Addendum 3 — wave 3 (final wave, same day)

Six further deployments, all completed:

| Agent | Mission | Artifacts |
|---|---|---|
| RBAC wirer | Capability taxonomy + route enforcement | [#58](https://github.com/rest-mail/rest-mail-server/pull/58) merged |
| go-smtp adopter | Fork + `ExtraCaps` hook + engine swap + prepared upstream text; resumed once to apply review fixes | [#59](https://github.com/rest-mail/rest-mail-server/pull/59) merged; [rest-mail/go-smtp](https://github.com/rest-mail/go-smtp); upstream [emersion/go-smtp#303](https://github.com/emersion/go-smtp/pull/303) submitted |
| Adversarial reviewer | Refute #59's safety claims (15 live probes) | MERGE-WITH-FIXES verdict; caught the silent 2000-byte line limit pre-merge |
| github-guard deployer | Guard all 7 lib repos | 7 PRs merged; protection + squash-only live (imap discovery race fixed manually after) |
| Max-size configurer | `SMTP_MAX_MESSAGE_SIZE` + end-to-end honesty audit | [#60](https://github.com/rest-mail/rest-mail-server/pull/60) merged (+ fixed the ignored `max_size_mb` pipeline bug it found) |
| raw_size fixer | Exact RFC822.SIZE / POP3 sizes | [#61](https://github.com/rest-mail/rest-mail-server/pull/61) merged |

The adversarial-review pattern earned its keep: a fresh skeptic with instructions to *refute* found the one silent behavior change (line-length limit) that the implementing agent, the test suite, and CI all missed — before it reached `main`.

**Final fleet metrics:** **22 deployments, 19 completed**, 1 deliberately stopped, 2 rate-limit kills (superseded); **25 merged PRs** (#35–#38, #40–#55, #57–#61) + 7 lib-repo guard PRs + 1 upstream PR submitted; **7 libraries + 1 fork** published; `main` green throughout, finishing at `c463542`.
