# Contact mutation atomicity and request cancellation

Partial evidence for #1163 and its incorporated contact area #639. Shared named
examples remain #625/#1171. Existing response types, permission/refusal policy,
validation, import semantics and limits decisions are preserved.

## Findings on fetched main

Baseline `26878b750a53dd28fb9e95c9682849eeb490b06e` returned a sanitized 500
when reloading an updated or blocked contact failed, but had already committed
the mutation. The SQL-driver regression reproduced a committed changed name and
a committed blocked trust level despite both failed requests. Account access,
list/count and delete SQL also lacked the HTTP context. Mutation predicates did
not consistently guard both the contact and mailbox or check affected rows.

Update and block now lock the selected row and perform mutation and response
reload in one request-scoped transaction. Failure or cancellation before commit
rolls back; the returned row comes from that transaction. Delete similarly locks
and deletes with both contact and mailbox predicates. A missing affected row
retains update/delete's 404; block keeps its existing internal-error contract if
a selected contact disappears. New blocks still return 201, existing blocks 200,
and deletion 204. Contact access/list/count/delete now use the HTTP context;
creation, import, suggestion and their existing scoped work are preserved.

## Evidence and limits

Six registered-route/database-sql-driver functions cover rollback on response
reload failure, cancellation on all seven routes' account access, contact read
and mutation failures, affected-row and mailbox guards, successful responses,
new/existing block branches, true absence and cross-account denial. The driver
models pending and committed name/trust state rather than checking only response
codes. A separate PostgreSQL proof covers actual write/reload rollback and
successful update, block and deletion. It skips locally without DB_HOST; no
local database is started. The first focused run after implementation exposed two
fixture assumptions: raw SQL predicates are not quoted, and a zero-row delete
needs its affected-row refusal inside the transaction. Both were corrected. Cancellation tests also wait on the driver
rollback completion signal because database/sql may finish cancellation cleanup
asynchronously after the handler returns; the pending/committed-state assertions
remain in place. No case was removed.

Run the seven new `TestContact...` functions in the two contact fault/transaction
files, plus the mandatory three API description/example guards. Passing those
guards does not fulfill existing missing named pairs. No schema, oracle,
generated client or example is changed.

These routes inject a fixture principal and exercise real endpoint registration,
GORM and SQL calls. Production authentication, middleware, live PostgreSQL locks
and races, full/race/timed-fuzz/E2E, tier/configuration/strict-area and complete
source-body acceptance remain separate. Cancellation after a completed commit
cannot undo it. No full contact-topic or parent completion is claimed.
