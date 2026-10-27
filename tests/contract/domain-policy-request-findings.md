# Domain-policy request budgets and stale writes

Partial evidence for #1164 and the implementation area incorporated from #646.
Shared named examples remain #625/#1171. The TLS-report access-ceiling decision
in #646 and API policy decisions in #672/#625 are not changed.

## Reproduced on fetched main

At `26878b750a53dd28fb9e95c9682849eeb490b06e`, all thirteen sender-list,
suppression, admin MTA-STS and relay operations used background SQL contexts.
The positive registered-route baseline loses HTTP deadlines/request values on
every operation. The independent public MTA-STS policy-file correction in ready
#1222 is preserved; this batch changes only its administration methods.

The stale-write baseline also reproduces:

- Both sender-rule deletes answer 204 after their DELETE affects zero rows.
- Existing-policy MTA-STS and relay saves fall back to INSERT after a zero-row
  UPDATE, recreating vanished rows and answering 200. MTA-STS's fallback also
  substitutes defaults for explicit max_age zero and active false.

All thirteen operations now carry the HTTP context into their queries, writes
and default transactions. Sender deletes repeat rule ID,
domain ID and allow/block kind predicates and check affected rows, preserving
404 when that selected rule disappears or moves. Existing MTA-STS and relay
rows use explicit-field, ID/domain-guarded UPDATE without Save's insert fallback;
a vanished selected row returns the existing sanitized internal error. First
creation retains its former status and defaults. Intentional zero/false/empty
updates, including clearing a relay's password and transport fields, persist.

## Local regression evidence

Ten SQL-driver test groups exercise the actual registered routes, GORM calls
and write behavior:

- Successful responses and HTTP budget propagation for all thirteen operations.
- Fault injection and blocked-query/write cancellation at every scripted SQL
  stage, with a finite backstop for lost cancellation and sanitized errors.
- Zero-row sender deletion and MTA-STS/relay updates, with no fallback INSERT.
- Genuine missing rows, empty lists, direct transport when no relay exists,
  MTA-STS creation with explicit zero/false, and preserved success statuses.
- Bound SQL fields and ID/domain predicates, including empty relay fields.
- Missing-domain refusal codes remain intact, and an already-canceled request
  performs no driver SQL; the next use of the same handler still succeeds.
- Relay passwords retained when omitted, cleared when empty, encrypted when
  replaced, never returned as plaintext/ciphertext, and refused with the existing
  409 if a new password needs a missing master key. These are isolated operator
  SMTP-relay fixtures, not phone-account notification credentials.
- Capability middleware still executes before binding/SQL: domain checks remain
  scoped and setting relay transport retains installation-wide domains:write.

One additional PostgreSQL proof executes a deliberately zero-row UPDATE and
checks that MTA-STS/relay rows and values remain intact, without Save fallback.
It compiles but skips locally without DB_HOST. It is not a concurrent-delete or
row-lock proof. No local PostgreSQL is started.

Run the eleven new `TestDomainPolicy...` functions in the fault and stale-persistence
files, plus the three mandatory API description/example guards. No request or
response type, schema, oracle, generated client, named example or exception list
changes are part of this correction. Existing missing pairs remain separate.

## Remaining acceptance

Fixture routes use no-op authorization for SQL cases and refusal middleware for
boundary-order cases; this does not prove production JWT/CSRF/grant middleware.
No full suite, race, timed fuzz, E2E, live SMTP, live concurrency or complete tier,
configuration and strict-area acceptance is claimed. MTA-STS/relay read-modify-
write serialization, concurrent first creation, commit ambiguity and domain
lifecycle races remain. Cancellation after commit cannot undo the commit.
The parent and all source-body acceptance remain partial; no owner scope/status
or relay-default decision is inferred.
