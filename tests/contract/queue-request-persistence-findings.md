# Queue request cancellation and mutation findings

This is a partial implementation record for #1164 and its queue source #650,
against main `7a4c49ee666c3bea59922cd0ff98b470a2f1bac4` on 2026-10-27.
It does not complete the broader admin-contract acceptance criteria.

## Findings and resulting behavior

All nine queue HTTP operations previously started SQL with a background context.
The request deadline and cancellation now reach count/list/detail/statistics
reads, bulk selection and mutation SQL, and transaction creation. Operational
failures retain the existing sanitized 500 envelope; they do not become missing
entry or successful mutation responses.

Single retry and bounce previously read the entry before their update transaction.
A competing mutation could change or remove the row between those operations,
and a zero-row update was answered as success. They now lock the selected row
with PostgreSQL `FOR UPDATE`, check retry eligibility and write within the same
request-scoped transaction. Failed lookup, write or commit cannot return success;
a missing entry or zero affected rows returns the existing 404 refusal. Retry
still accepts only pending/deferred entries and refuses other states with the
existing 400 response. Single bounce still accepts any found row; defining a new
lifecycle rule is outside this change.

List envelopes, literal sender/recipient filtering, pagination, IDs taking
precedence over bulk filters, bulk status guards, empty-filter deletion, affected
counts, delete's existing 404, and capability middleware are preserved. No DSN
side effect, worker lease, delivery attempt or background queue policy is added.
The short admin transaction does not establish coordination with a worker's
external SMTP work; the wider concurrency and worker-lifecycle acceptance remains
open.

## Evidence

`queue_request_persistence_test.go` exercises all nine registered routes using
`database/sql` and the production PostgreSQL GORM dialect with a local driver.
It covers normal responses, actual deadline cancellation during SQL, cancellation
after the locked read, BEGIN/read/list/write/COMMIT failures, sanitized errors,
locked reads inside a transaction, rollback, affected-row refusal, missing rows,
and unchanged retry/bounce state rules. The baseline failed on all nine context
checks and every locked-mutation scenario before the production fix.

`queue_mutation_postgres_test.go` adds real PostgreSQL proofs of rollback after a
successful SQL update followed by an injected failure, successful retry/bounce
persistence, and request deadlines while a second connection holds the row lock.
These proofs require `DB_HOST` in CI and are explicitly skipped locally. No local
PostgreSQL instance is started. The driver fixture is not a substitute for those
PostgreSQL proofs or for full queue-worker concurrency acceptance.

The registered operation descriptions and `openapi.yaml` describe the same
cancellation and transaction behavior. Request/response JSON types and named
examples do not change. Mandatory generated-description, example-shape and
canonical-example guards must pass without new exceptions. The existing 135
missing named example pairs remain owned by #625 / PR #1171; this work neither
fills nor suppresses that separate inventory. Full middleware authorization and
owner decisions in #646/#672 remain outside this batch.
