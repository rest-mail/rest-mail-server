# Codex Security Review — Board Risk Register (2026-04-23)

## Security Risk Register

| Risk ID | Risk | Likelihood | Impact | Severity | Owner | Target Date | Status |
|---|---|---|---|---|---|---|---|
| SR-001 | Cross-account access via missing object-level authorization in Sieve endpoints | High | High | **Critical** | API Lead | 2026-04-30 | Open |
| SR-002 | Cross-account quarantine read/release/delete via missing ownership checks | High | High | **Critical** | API Lead | 2026-04-30 | Open |
| SR-003 | Inbound delivery endpoint potentially reachable without strong service authentication | Medium-High | High | **High** | Platform/SRE Lead | 2026-05-07 | Open |
| SR-004 | Unauthenticated mailbox enumeration endpoints aid account discovery | High | Medium | **High** | API Lead | 2026-05-07 | Open |
| SR-005 | Monitoring interfaces exposed with static/default credentials | Medium | High | **High** | SRE Lead | 2026-05-07 | Open |
| SR-006 | SSE endpoint accepts refresh tokens (token-type boundary weakness) | Medium | Medium | **Medium** | API Lead | 2026-05-14 | Open |
| SR-007 | Limited API-level request throttling for auth-sensitive routes | High | Medium | **Medium** | API + Platform | 2026-05-14 | Open |
| SR-008 | Dev defaults/secrets can become production risk if deployment controls fail | Medium | Medium | **Medium** | Platform/SRE Lead | 2026-05-21 | Open |

## Executive View

- **Overall posture:** High risk until Critical authorization flaws are remediated.
- **Immediate focus:** Close SR-001 and SR-002 within 7 days.
- **Next priority:** Reduce internet-facing abuse paths (SR-003, SR-004, SR-005).

## Key Decisions Requested

1. Approve a **security hardening sprint** as top engineering priority for the next 2 weeks.
2. Require **go-live gate**: no external exposure until SR-001 to SR-005 are closed.
3. Assign named owners and weekly reporting against this register.

## Exit Criteria (for this review cycle)

- All Critical risks closed and regression-tested.
- High risks mitigated or accepted with documented compensating controls.
- Deployment checklist updated to block insecure defaults in production.

## References

- Technical deep-dive: `docs/codex-security-review-2026-04-23.md`
- Executive summary: `docs/codex-security-review-summary-2026-04-23.md`
