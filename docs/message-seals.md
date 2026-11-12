# Message receipt seals

Part of #1415. A receipt seal detects changes to the message this server stored
at acceptance. It does not identify its author, authenticate a sender, assess
content, or make a message safe. It is not a signature: someone who can replace
both the message and its stored seal can replace the evidence too.

## Storage and verification

The existing additive GORM `db.AutoMigrate` migration adds nullable
`messages.sealed_hash` (`varchar(64)`, default NULL). There is no backfill. A
message with NULL or an empty seal remains unsealed when read, moved, flagged,
or encountered again by an idempotent import. A receipt seal is written by
`Message.BeforeCreate`, in the same transaction as the new message. Verification
does not update the seal.

Message responses include `sealed_hash` when one exists. With the same
`mail:read` authorization as `GET /api/v1/messages/{id}`, clients can call
`GET /api/v1/messages/{id}/seal`. Its `data` contains:

- `algorithm`: `restmail-seal-v1`.
- `state`: `intact`, `mismatch`, or `not_sealed`.
- `sealed_hash` and `computed_hash`: lowercase SHA-256 hex, omitted when unsealed.

`intact` means unchanged since this server accepted the stored message.
`mismatch` means the stored content no longer matches its receipt seal.
`not_sealed` means no receipt seal exists, not that a check failed. Missing and
inaccessible messages both return 404; anonymous callers return 401. A lookup,
authorization dependency, or hashing failure is an error, not an integrity
verdict. The webmail separately shows checking and unavailable states.

## Exact algorithm and bytes

The digest protects **both the original source and the independently stored
display content**. Hashing only the original source would miss an edit to
`body_text`, `body_html`, or a displayed header while that source stayed intact.
No MIME decoding, line-ending conversion, whitespace cleanup, Unicode
normalization, header reordering, or gateway downgrade is applied to the source.

1. Stream the entire stored RFC 5322 source through SHA-256: headers, their
   separators, the blank line, body, MIME boundaries, attachments, repeated
   headers, and trailing bytes, exactly as stored. `raw_octets` takes precedence
   when present, otherwise use the UTF-8 bytes of `raw_message`. When no source
   was stored, use the SHA-256 of the empty byte sequence. This is the stored
   recipient copy, including any server-added Authentication-Results and
   Delivered-To headers, not a reconstructed message or the pre-transform wire.
2. Initialize a second SHA-256 with the ASCII bytes `restmail-seal-v1`, one NUL
   byte, and the **32 binary bytes** of the source digest (not its hex string).
3. In this fixed order, append each field's byte length as an unsigned 64-bit
   big-endian integer, then its UTF-8 bytes:
   `message_id`, `in_reply_to`, `references`, `sender`, `sender_name`, `subject`,
   `body_text`, `body_html`, canonical `headers`, canonical `recipients_to`,
   canonical `recipients_cc`.
4. Encode the final SHA-256 as 64 lowercase hexadecimal characters. That is
   `sealed_hash`.

The string fields are hashed exactly as stored, including CRLF and whitespace.
PostgreSQL JSONB does not preserve JSON serialization, so the three JSON fields
use Go `encoding/json`'s compact encoding with sorted object keys and its default
string escaping, after decoding with `UseNumber`. Header values and recipients
are strings or arrays of strings, as in the message schema. Missing JSON fields
are the empty array `[]`, matching the model's JSONB storage default. JSONB's key
order and formatting therefore cannot invalidate a receipt. The exact original
header octets remain independently covered by the source digest.

IDs, mailbox ownership, thread grouping, folder, flags, rule class, timestamps,
quotas, transport metrics, and attachment storage references are not in the
transcript.
A flag update, folder rename, or move therefore leaves the receipt valid. The
original MIME attachment bytes remain covered wherever a source exists; this
is not a separate integrity check of extracted attachment objects or calendar
projections.

`internal/messageseal.Digest` consumes an `io.Reader` with a fixed 32 KiB buffer,
including when the reader implements `WriterTo`; it does not materialize a
source or a body. A read failure produces no digest. Its memory test hashes a
64 MiB stream and a 64 MiB display body with less than 1 MiB allocated.

The streaming message-store work in #1463 is already on main. A future #1437
consumer can pass its opened message object to the same digest function rather
than load it into memory; the algorithm does not depend on PostgreSQL text or a
filesystem/S3 location. This change does not modify the store, inbound-queue,
pipeline-split, or streaming PRs.

Reference vector: source `Subject: Receipt\r\n\r\noriginal body\r\n`,
`message_id=<seal@example.test>`, `sender=sender@example.test`,
`subject=Receipt`, `body_text=original body`, the other string fields empty, and
all three JSON fields `[]`, produces
`c8df0e809a88268049a3d046bf4c17819b16cbf2e5d3845e15368a251710bfb2`.
The named OpenAPI intact and mismatch example pairs pin this vector independently
of the real PostgreSQL delivery tests.

## Acceptance paths

All new `models.Message` inserts share the creation hook:

| Path | Receipt behavior |
| --- | --- |
| SMTP inbound and local delivery (`deliverToLocal`) | Seals the final stored recipient copy, after delivery transformations. |
| Authenticated SMTP submission (`smtp.dbStore.PersistSubmittedMessage`) | Seals the sender's new Sent copy. A nonlocal sender has no local message row to seal. |
| RESTMAIL inbound (`RestmailHandler.deliver`) | Seals every stored recipient copy. |
| API/webmail send, draft-send, forward, and calendar reply | Seals new Sent copies, including structured messages with no original source. A recipient on this server gets its copy through the outbound queue and SMTP inbound, which seals it. Draft-send creates a new sent message; it does not retrofit a seal onto its editable draft. |
| Forward with attachments (`commitForward`) | Seals the new message through the same hook; storage references are excluded. |
| Historical import | Seals a newly accepted import, including non-UTF-8/NUL `raw_octets`; an existing Message-ID with identical source in that folder is skipped without adding a seal. An existing source-less row is also skipped, following import's duplicate policy. The receipt means acceptance here, not integrity at the historical sender. |
| IMAP APPEND and COPY | Both use the gateway's import endpoint after #1475, with the signed-in account's `mail:import` permission. Every new stored copy is sealed, even with `\Draft` set. COPY is a new acceptance, not evidence about the earlier copy. A source message without a seal stays unsealed. |
| Quarantine release | Seals the newly stored message, not the earlier quarantine record. |
| Generated quarantine digests and send-limit notices | Seals their stored structured headers and bodies, even without a raw source. |
| Administrative test-email endpoint | Seals both newly created test INBOX and Sent copies through the same hook, even without a raw source. |
| Locally generated delivery-status notifications | Uses a typed `models.Message` insert, replacing the former map insert that bypassed model hooks. Seals the DSN source and display body. |
| Editable API/webmail draft | No receipt yet: explicitly marked `PendingDraft` only for that create operation. It remains unsealed during editing. A delivered message carrying a draft flag is still sealed. |
| Empty folder marker | Not a message acceptance; remains unsealed. |
| Queue entries, redirects, discarded mail, and rejected mail | No newly stored mailbox message exists to seal. If a later delivery creates one, the hook seals that new row. |

## Webmail and remaining mobile work

The message viewer checks the authorized seal endpoint and renders the result
beside SPF, DKIM, ARC, and DMARC in the trust receipt. It uses the API's stored
authentication verdicts, not authentication claims supplied in arbitrary message
headers. Merely having a digest is never displayed as a successful verification.
Errors and pending checks never become mismatches. Unsealed messages still show
“Not sealed” even when no sender-authentication verdict exists.

The mobile app is in another repository and is not changed here. It still needs
to consume `sealed_hash`, call the verification endpoint, and render the same
three outcomes beside its authentication results, with separate pending/error
states and the same limited integrity wording. This server/webmail change is
**Part of #1415**, not a complete implementation of that issue.

Sent messages are now inserted only after their final raw source has been built
and signed. The former insert-then-update sequence would have sealed an empty
source and immediately invalidated the receipt. Snapshot DTOs are regenerated
with the repository's `go generate` command, preserving the seal field without
adding a model-parity exclusion. Example tests verify the real persisted digest
against the final source and display fields before normalizing hashes whose
inputs contain generated Message-IDs, timestamps, or MIME boundaries.
