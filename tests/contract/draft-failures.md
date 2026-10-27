# Draft persistence and scheduled request failures

Part of #1163/#638. Reviewed against fetched main
2bac2b0f06291effd4ba0c9cc036548f6b24a0bd on 2026-10-27.

## Findings and behavior

- Draft updates ignored both the write and reload error, returning 200 after a
  failed write or after a committed update whose reload failed. Update and reload
  now share a request-scoped transaction; either error returns sanitized 500 and
  rolls back. The update/reload require the same mailbox and an extant draft,
  so a vanished or no-longer-draft row receives 404 rather than false success.
- The already-declared `in_reply_to` and `references` draft-update fields were
  ignored. They now reach storage; explicit empty values clear them, while omitted
  or null values preserve the existing fields. Request/response types are unchanged.
- Draft save sender checks and draft update/send lookup/access checks hid SQL
  failures as forbidden or not found. Genuine absence/denial keeps its existing
  response; operational errors return 500 without private dependency text.
- Draft save/update/send preflight and scheduled-list/cancellation SQL now inherit
  the HTTP context, including sender authorization and the draft-update transaction.
  This does not claim context propagation through every downstream send operation.

The baseline reproduces the false-success updates, ignored thread fields,
false403/404 and lost SQL context. Positive/missing-resource controls that failed
only the context assertion are not claimed as additional status defects.

Six SQL-driver/registered-route functions exercise faults, cancellation before
and within updates/reloads, atomic rollback, successful reload, absent/denied
policy, a zero affected-row race, and thread-field set/clear/omit/null requests.
The fixture uses PostgreSQL GORM and database/sql with synchronized transaction
state; it is an interaction proof. A separate PostgreSQL CI function checks
actual rollback on update/reload failure and persisted response/thread fields.
No local PostgreSQL service is started; that function skips without DB_HOST.

## Boundaries and ownership

Shared named pairs remain with #625/PR #1171. Published #1172–#1176 work and open
#622/#625/#646/#661/#672 decisions are preserved. Generated operation text and the
independent oracle's operation descriptions record this behavior without changing
response shapes, status declarations, permission policy or exception lists.

The broader #1163 acceptance stays open. This fix does not make the complete
send-and-post-send-draft-deletion sequence atomic, change the scheduled worker's
claim/retry/finalization model, or establish end-to-end send/attachment/quota and
external side-effect rollback. Full PostgreSQL, race and E2E acceptance belongs
to GitHub, and no production send, release or deployment is claimed.
