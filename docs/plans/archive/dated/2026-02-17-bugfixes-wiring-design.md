# Bug Fixes and Integration Wiring Design

## Context

The core components (pipeline engine, filters, gateway protocols, API handlers) are individually implemented but not wired together. This document describes fixes for 7 integration gaps.

## Fix 1: Pipeline Execution on Inbound Delivery

**Problem:** `DeliverMessage` inserts messages directly to DB, bypassing all pipeline filters (spam checks, greylist, sieve rules, etc.).

**Solution:** Wire `pipelineEngine` into `MessageHandler`. Before inserting a message:

1. Convert the delivery request to `pipeline.EmailJSON`
2. Look up the domain's inbound pipeline config from DB; fall back to `DefaultInboundPipeline()`
3. Call `pipelineEngine.Execute(pipelineConfig, email)`
4. Handle the result:
   - **continue** — insert message, update quota, publish SSE (existing behavior)
   - **reject** — return HTTP 550 so SMTP gateway returns rejection to sender
   - **quarantine** — insert into `quarantine` table instead of `messages`
   - **discard** — return HTTP 200 silently (no insertion)
   - **defer** — return HTTP 451 for temporary failure

**Files changed:**
- `internal/api/handlers/messages.go` — add `engine` field, pipeline execution in `DeliverMessage`
- `internal/api/routes.go` — pass `pipelineEngine` to `NewMessageHandler`

## Fix 2: DB-Backed Filter Registration

**Problem:** 6 filters that require `*gorm.DB` are never registered: `greylist`, `vacation`, `domain_allowlist`, `contact_whitelist`, `recipient_check`, `sender_verify`.

**Solution:** In `routes.go`'s `NewRouter()`, explicitly register these after the blank import fires `init()` for stateless filters:

```go
pipeline.DefaultRegistry.Register("greylist", filters.NewGreylist(db))
pipeline.DefaultRegistry.Register("vacation", filters.NewVacation(db))
pipeline.DefaultRegistry.Register("domain_allowlist", filters.NewDomainAllowlist(db))
pipeline.DefaultRegistry.Register("contact_whitelist", filters.NewContactWhitelist(db))
pipeline.DefaultRegistry.Register("recipient_check", filters.NewRecipientCheck(db))
pipeline.DefaultRegistry.Register("sender_verify", filters.NewSenderVerify(db))
```

**Files changed:**
- `internal/api/routes.go`

## Fix 3: SMTP Outbound Queue Wiring

**Problem:** `handleDATA` in the SMTP gateway logs "queuing for outbound delivery" for non-local recipients but never inserts into `outbound_queue`.

**Solution:** The SMTP gateway already connects to the DB (for the queue worker). Pass `*gorm.DB` through the server to sessions. On non-local recipients in `handleDATA`, insert an `OutboundQueue` record:

```go
queueEntry := models.OutboundQueue{
    Sender:     s.mailFrom,
    Recipient:  rcpt,
    Domain:     recipientDomain(rcpt),
    RawMessage: string(data),
    Status:     "pending",
}
s.db.Create(&queueEntry)
```

**Files changed:**
- `internal/gateway/smtp/server.go` — add `db` field, pass to sessions
- `internal/gateway/smtp/session.go` — add `db` field, insert queue entry in `handleDATA`
- `cmd/smtp-gateway/main.go` — pass DB to SMTP server constructor

## Fix 4: SendMessage Outbound Queue Completion

**Problem:** When `SendMessage` enqueues outbound messages, `RawMessage` is empty and `MessageID` foreign key is nil.

**Solution:** After creating the sent message record, serialize it to RFC 2822 via `mime.Serialize()`, then populate `RawMessage` and `MessageID` on the queue entry.

**Files changed:**
- `internal/api/handlers/messages.go` — `SendMessage` handler

## Fix 5: IMAP EXPUNGE

**Problem:** EXPUNGE is a no-op that returns OK without deleting anything.

**Solution:** Track `\Deleted`-flagged message IDs in the session (via STORE). On EXPUNGE:
1. Call `api.DeleteMessage()` for each deleted message
2. Send `* N EXPUNGE` untagged responses in descending sequence order (per RFC 3501)
3. Remove deleted messages from the session's message list

**Files changed:**
- `internal/gateway/imap/session.go` — add `deleted` map, update STORE handler, implement EXPUNGE

## Fix 6: IMAP CREATE

**Problem:** CREATE returns OK without doing anything. Folder name is parsed but commented out.

**Solution:** Folders are implicit in this system (a string field on messages). CREATE validates the folder name (no special characters, reasonable length, not a reserved name) and returns OK. No API call needed.

**Files changed:**
- `internal/gateway/imap/session.go` — `handleCreate`

## Fix 7: IMAP SEARCH

**Problem:** SEARCH always returns all sequence numbers, ignoring all criteria.

**Solution:** Parse common IMAP SEARCH keys and filter the in-memory message list:
- `ALL`, `UNSEEN`/`SEEN`, `FLAGGED`/`UNFLAGGED`
- `FROM`/`TO`/`SUBJECT` (substring match)
- `SINCE`/`BEFORE`/`ON` (date comparison)
- `UID` (UID set matching)
- `NOT`, `OR` (combinators)

**Files changed:**
- `internal/gateway/imap/session.go` — `handleSearch`
