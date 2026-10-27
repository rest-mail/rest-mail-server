# Delivery lookup failures

Issue #1161, incorporating #642 and #643. Checked against server main
477c9b67087b94948b287c1c81dbc989989856d3 on 2026-10-27.

## Findings and resulting behavior

| Boundary | Previous behavior | Result |
|---|---|---|
| RESTMAIL sender-domain lookup | Any SQL error made a potentially hosted sender appear external, allowing the non-strict authentication gate to skip alignment. | Only a genuine absent domain establishes an external sender. Other errors return 503 `deferred`. |
| RESTMAIL recipient and plus-address resolution | Operational errors became absent recipients, and resolution was repeated during report, quarantine, Sieve and storage handling. | Resolve all recipients before those actions. Preserve operational errors, return 503, and reuse the resolved identities. Exact and inactive identities and explicit alias reservations retain precedence over plus fallback. |
| RESTMAIL inbound domain/pipeline lookup | A failed pipeline lookup selected defaults; corrupt stored filter JSON skipped filtering. | Only a genuine absent pipeline selects defaults. Query and decoding errors return 503. |
| Gateway mailbox lookup by ID or address | Every error returned 404 `mailbox_not_found`. | Genuine absence keeps 404; an operational error returns a sanitized 500 `internal_error`. |
| Request cancellation | Several delivery queries used a background ORM context. | Sender, recipient, alias, pipeline, account-Sieve and gateway mailbox reads carry the HTTP context. |

`delivery_read_fault_test.go` uses the real registered delivery routes, GORM's
PostgreSQL dialect and a `database/sql` driver fixture. The fixture rejects
unexpected queries and every attempted write. It checks first and later
recipient failures before side effects, sanitized replies, SQL context/deadline
propagation and cancellation. Separate controls preserve genuinely missing
mailboxes, an absent pipeline's defaults, an explicitly empty configured pipeline,
exact identities, inactive accounts, alias reservations and plus addressing.
These are interaction proofs, not a substitute for PostgreSQL persistence tests.

The first baseline reproduced the gateway's false 404, loss of HTTP SQL context
and the non-strict sender lookup's fall-through to 422. Initial later-stage
fixtures were refused by the authentication gate before reaching their intended
faults; those fixtures now explicitly trust their synthetic test peer. Their
initial refusals are not claimed as additional production defects.

## Contract and remaining scope

Request/response types, status declarations and payload shapes are unchanged.
500 and 503 were already described. Generated and independent-oracle descriptions
now explain the corrected behavior. Shared named pairs and their findings remain
owned by #625/PR #1171; this change does not replace their work or claim complete
#1161 acceptance.

This is a read-failure fix. It does not establish transactionality for message,
quota, quarantine, redirect or TLS-report writes, including mixed report/mail
recipients. It does not change the existing first-recipient domain-pipeline
selection policy, introduce cross-domain pipeline grouping, or prove production
internal-listener mTLS. Those boundaries remain part of the wider delivery audit.
