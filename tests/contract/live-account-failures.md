# Live account and device request failures

Issue #1162, including account links (#635) and live events/devices (#636).
Source reviewed at current server main
2bac2b0f06291effd4ba0c9cc036548f6b24a0bd on 2026-10-27.

| Boundary | Finding | Correction |
|---|---|---|
| Account event stream | Any account-access lookup error became 403. Its SQL context was unrelated to the HTTP request. | Real absence or denied access keeps 403. Other failures return sanitized 500 before stream headers, slots or subscriptions. SQL follows request cancellation. |
| Account unlink | Any initial mailbox lookup error became a missing-link 404. Both lookup and grant-removal transaction lost the HTTP context. | Genuine absence keeps 404; operational lookup failure is sanitized 500. The read and transaction inherit the request context. |
| Device list, registration and removal | HTTP cancellation did not reach the store's SQL work, including registration/removal transactions. | Use a request-scoped copy of the store; the shared/background store is unchanged. Existing errors remain sanitized 500. |

Five new test functions exercise the actual registered routes and GORM's
PostgreSQL dialect through a `database/sql` driver. Fault and cancellation cases
cover all five operations. Device-registration and account-unlink cases proceed
past their initial read and into a transaction before injecting a failure or
cancelling a blocked query/lock. They assert no commit, no leaked dependency
text, no SSE response/subscription/stream slot, and HTTP context propagation to
SQL operations and transaction start. Counters use atomics because database/sql
may roll back a cancelled transaction asynchronously.

The baseline reproduced the false 403 and 404 and lost cancellation/context on
all five operations, including transactional device registration. Genuine
missing-resource controls retain the current 403/404 policy and empty device
list. An initial fixture callback used uint64 where SessionCurrent takes int;
its type was corrected before the runtime baseline, without removing a case.

Request/response types, declared statuses, permissions and ownership checks are
unchanged. Generated and independent-oracle descriptions are synchronized.
Shared named request/response pairs remain owned by #625/PR #1171, and the
published #1172/#1178 behavior work is preserved. No authorization/validation
policy is inferred from the open #672 decisions.

These are interaction and error-path proofs. They do not establish PostgreSQL
commit constraints, concurrency/device-cap invariants, production browser/JWT
middleware behavior, delivery of an Apple notification, or permission revocation
during an already-open event stream. Those broader acceptance boundaries remain
in #1162 and its incorporated records. Full suites and race checks stay in GitHub.
