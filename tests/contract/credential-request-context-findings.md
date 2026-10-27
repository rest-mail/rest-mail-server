# Credential request cancellation and dependency faults

Partial evidence for #1162, incorporating the credential operations in #635 and
protocol sign-in in #632. This batch preserves existing status, identity,
validation, second-factor and permission policy. Shared named examples remain
owned once by #625 and #1171.

## Finding and correction

On fetched main `26878b750a53dd28fb9e95c9682849eeb490b06e`, the six
app-password/API-key routes and protocol sign-in used the handler's background
GORM connection. The registered-route baseline reproduced lost HTTP deadlines
and request values on all seven operations. SQL could therefore outlive client
cancellation, including credential changes and their audit transaction.

Each operation now uses a request-local handler copy. Account access, live
grants, password rechecks, production two-factor reads, credential SQL and
API-key audit writes share the HTTP context. The shared handler, background
connection and injected in-memory two-factor stores are preserved. Transactions
retain their existing boundaries; cancellation before commit is an error.

## Local evidence

Six focused test functions use the registered routes and a scripted
`database/sql` driver, with real GORM, repositories, permission resolution,
password verification and audit append code:

- Every route preserves sanitized internal-error responses for operational
  lookup failures while carrying the deadline and request value into SQL.
- Cancellation interrupts each access/read/revocation stage, password lookup,
  two-factor check, API-key creator/owner grant read, capability lookup and
  credential insertion. A finite driver backstop exposes lost cancellation.
- Successful app-password and API-key creation/revocation and protocol sign-in
  retain their response statuses and transaction commits. API-key advisory
  locking, audit-chain reads and audit insertion also carry the HTTP context;
  injected faults and cancellation at each audit stage never commit the change.
- Metadata lists omit secret hashes; empty lists remain successful. Missing and
  inaccessible accounts keep the same not-found response. Machine credentials
  remain refused before account SQL. Missing credentials retain not-found.
- An already-canceled request performs no driver SQL and cannot poison the next
  request through the same registered handler.

The fixture routes inject claims and no-op throttle/outer audit middleware;
these tests do not establish production JWT, cookie, CSRF, rate-limit or external
protocol-gateway integration. API-key transactional audit code executes normally.
No request/response type, schema, documented status, oracle, generated client or
named example is changed by this context-only correction.

Run the six `TestCredential...` functions in `credential_context_test.go` and the
three mandatory API description/example guards. The latter can still report
existing missing named pairs; passing them does not fulfill #625/#1171.

## Remaining acceptance

No local PostgreSQL, full suite, race, timed fuzz, E2E or real credential-gateway
run is claimed. Live database constraints, transaction commit races, concurrent
credential revocation, session/browser lifecycles, cross-account tier principals,
configuration matrices and strict-area acceptance remain required. Cancellation
after a database commit cannot undo that commit. The owner decisions in #672
and #625 remain open, and this is not completion of the parent or either source
area.
