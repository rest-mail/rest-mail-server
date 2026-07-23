# Codex Security Review — Board Risk Register (2026-04-23)

> **Re-evaluated 2026-07-23 against `main` (post-#77).** Criticals SR-001/SR-002 **closed and regression-tested**. All 8 risks are resolved or obsolete **except** SR-004's `/restmail` sub-path (protocol-inherent, accepted by design — awaiting a product decision). Per-finding detail in the deep-dive doc.

## Security Risk Register

| Risk ID | Risk | Likelihood | Impact | Severity | Owner | Target Date | Status |
|---|---|---|---|---|---|---|---|
| SR-001 | ~~Cross-account access via missing object-level authorization in Sieve endpoints~~ | High | High | **Critical** | API Lead | 2026-04-30 | ✅ **Fixed** 2026-07-23 (#77 — ownership check via `resolveAccountMailbox`; regression-tested) |
| SR-002 | ~~Cross-account quarantine read/release/delete via missing ownership checks~~ | High | High | **Critical** | API Lead | 2026-04-30 | ✅ **Fixed** (#77 — list scoped to owned mailbox; release/delete `WHERE id=? AND mailbox_id=?`) |
| SR-003 | ~~Inbound delivery endpoint potentially reachable without strong service authentication~~ | Medium-High | High | **High** | Platform/SRE Lead | 2026-05-07 | ✅ **Fixed** via internal mTLS (#65) ¹ |
| SR-004 | Unauthenticated mailbox enumeration endpoints aid account discovery | High | Medium | **High** | API Lead | 2026-05-07 | ◑ **Partly fixed / accepted** — `/api/mailboxes` closed by mTLS (#65); `/restmail` protocol-inherent, accepted by design ² |
| SR-005 | ~~Monitoring interfaces exposed with static/default credentials~~ | Medium | High | **High** | SRE Lead | 2026-05-07 | ⊘ **Obsolete** — monitoring compose removed in the decomposition |
| SR-006 | ~~SSE endpoint accepts refresh tokens (token-type boundary weakness)~~ | Medium | Medium | **Medium** | API Lead | 2026-05-14 | ✅ **Fixed** (#46 — `ValidateAccessToken`) |
| SR-007 | ~~Limited API-level request throttling for auth-sensitive routes~~ | High | Medium | **Medium** | API + Platform | 2026-05-14 | ✅ **Fixed** (#77 — per-IP token bucket on login/refresh) |
| SR-008 | ~~Dev defaults/secrets can become production risk if deployment controls fail~~ | Medium | Medium | **Medium** | Platform/SRE Lead | 2026-05-21 | ⊘ **Obsolete** — compose removed; config fail-closes on default JWT / empty MASTER_KEY |

¹ Residual: internal mTLS is default-off; making it mandatory is the secure-by-construction epic.
² Hardening lever, if desired, is greylist/tarpit on that path — not per-IP caps (which would break legitimate server-to-server delivery).

## Executive View (updated 2026-07-23)

- **Overall posture:** Criticals **closed** — both authorization flaws (SR-001/SR-002) fixed and regression-tested against Postgres.
- **Immediate focus:** ~~Close SR-001 and SR-002 within 7 days.~~ ✅ Done (#77).
- **Next priority:** ~~Reduce internet-facing abuse paths (SR-003, SR-004, SR-005).~~ SR-003 closed (mTLS), SR-005 obsolete, SR-004 `/api/mailboxes` closed (mTLS).
- **Only open item:** SR-004 `/restmail` enumeration — a product decision (accept as protocol-inherent vs greylist/tarpit), not a code gap.

## Key Decisions Requested — RESOLVED

1. ~~Approve a **security hardening sprint** as top engineering priority for the next 2 weeks.~~ — moot; the fixes landed (#46, #65, #77).
2. ~~Require **go-live gate**: no external exposure until SR-001 to SR-005 are closed.~~ — SR-001/002/003 closed, SR-005 obsolete; only SR-004 `/restmail` remains (accepted by design).
3. ~~Assign named owners and weekly reporting against this register.~~ — closed out in this pass.

## Exit Criteria (for this review cycle)

- ✅ All Critical risks closed and regression-tested (SR-001/SR-002, #77).
- ✅ High risks mitigated or accepted with documented compensating controls (SR-003 mTLS; SR-004 documented accept; SR-005 obsolete).
- ✅ Insecure-default risk addressed — compose removed; config fail-closes on default JWT / empty MASTER_KEY.

## References

- Technical deep-dive: `docs/security-review/codex-security-review-2026-04-23.md` (per-finding detail, struck through)
- Executive summary: `docs/security-review/codex-security-review-summary-2026-04-23.md`
