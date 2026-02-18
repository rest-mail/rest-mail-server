# Critical Items Gap Fixes Design

**Date:** 2026-02-18
**Scope:** Fix all remaining gaps across 9 critical features

## Context

Exploration revealed that all 9 "critical items" from PLAN.md are 70-90% implemented. This design covers the specific gaps, bugs, and missing wiring in each.

---

## 1. RESTMAIL Protocol Upgrade Fixes

**Files:** `internal/gateway/queue/worker.go`, `internal/gateway/smtp/session.go`

### 1a. Atomic capability cache upsert
`cacheCapability()` does a read-then-write without a transaction. Replace with GORM `Clauses(clause.OnConflict{...})` upsert to prevent race conditions between concurrent workers probing the same domain.

### 1b. Set ExpiresAt on SMTP-path queue entries
In `session.go` `handleDATA()`, when inserting `OutboundQueue` for remote recipients, set `ExpiresAt: time.Now().Add(72*time.Hour)` and `MaxRetries: 30`. Currently SMTP-path entries have zero `ExpiresAt`, causing the worker's `WHERE expires_at > ?` clause to filter them out — a delivery bug.

### 1c. Parse SMTP response codes in deliverToHost()
Extract the 3-digit SMTP code from errors returned by `net/smtp`. Return a structured error type (`SMTPError{Code int, Enhanced string, Message string}`) so the caller can distinguish 5xx (permanent) from 4xx (temporary) and bounce immediately on 5xx without exhausting retries.

---

## 2. SMTP Gateway Pipeline Response Codes

**Files:** `internal/gateway/apiclient/client.go`, `internal/gateway/smtp/session.go`, `internal/gateway/smtp/util.go`

### 2a. Structured error from apiclient
Add `StatusCode int` field to `APIError`. In `DeliverMessage()`, populate it from the HTTP response so callers can distinguish 403 (reject) from 503 (defer) from 200 (ok/discard/quarantine).

### 2b. SMTP reply code mapping
In `handleDATA()`, after `s.api.DeliverMessage()`, check the error's status code:
- 403 → `s.reply(550, "Rejected by policy")`
- 503 → `s.reply(451, "Try again later")`
- Other errors → `s.reply(451, "Temporary failure")`
- No error → `s.reply(250, "OK")`

### 2c. Pass missing fields in DeliverRequest
Extract `To`, `Cc`, `In-Reply-To`, `References` from `parseRawMessage()` and include them in the `DeliverRequest`. Pipeline filters that inspect recipients/threading currently see empty lists.

---

## 3. Bounce DSN Generation (RFC 3464)

**File:** `internal/gateway/queue/worker.go`

### 3a. Structured SMTP error type
Define `SMTPError` with `Code`, `EnhancedCode`, `Message` fields. Parse SMTP error strings from `net/smtp` to extract these. Used by both bounce generation and the immediate-bounce-on-5xx logic.

### 3b. Immediate bounce on 5xx
In `processOne()`, check `LastErrorCode` after delivery failure. If 5xx, bounce immediately instead of retrying to `MaxRetries`. Populate `LastErrorCode` from the structured SMTP error.

### 3c. RFC 3464 multipart/report bounce message
Rewrite `generateBounce()` to build:
- `Content-Type: multipart/report; report-type=delivery-status`
- Part 1 (`text/plain`): Human-readable explanation
- Part 2 (`message/delivery-status`): `Reporting-MTA`, `Arrival-Date`, `Final-Recipient`, `Action: failed`, `Status: 5.x.x`, `Diagnostic-Code: smtp; ...`
- Part 3 (`text/rfc822-headers`): Original message headers extracted from `RawMessage`

Generate proper RFC 2822 headers on the bounce message itself (From, To, Date, Message-ID, Subject, MIME-Version).

---

## 4. SNI Cache Invalidation

**Files:** `internal/gateway/tlsutil/sni.go`, `cmd/{smtp,imap,pop3}-gateway/main.go`

Add `fsnotify`-based file watcher to `SNICertLoader`:
- `StartWatching() error` — spawns goroutine watching `certDir` for CREATE/WRITE events on `.crt`/`.key` files.
- On change, extract domain from filename pattern `{domain}.crt`, invalidate that domain's entry from the in-memory cache. Next TLS handshake reloads from disk.
- `Stop()` — clean shutdown of watcher goroutine.
- Call `loader.StartWatching()` + `defer loader.Stop()` in each gateway's `main.go`.

---

## 5. SSE Missing Events

**File:** `internal/api/handlers/messages.go`

### Events to add:
- `SendMessage()`: after successful send, publish `message_sent` to sender's mailbox with `{message_id, folder: "Sent", subject}`.
- `SendDraft()`: after send + draft deletion, publish `message_sent` and `draft_deleted`.
- `DeliverMessage()`: after folder count changes, publish `folder_update` with `{folder, unread_count}`.
- `UpdateMessage()` when folder changes (move): publish `folder_update` for both old and new folders.

---

## 6. Draft Endpoint Gaps

**File:** `internal/api/handlers/messages.go`

- Add `InReplyTo string` and `References string` fields to the `SaveDraft` request struct.
- Persist both fields in the draft `models.Message`.
- In `SendDraft`, ensure `in_reply_to` is included in the send payload forwarded to `SendMessage`.

---

## 7. Thread Grouping Fixes

**File:** `internal/api/handlers/messages.go`

### 7a. Outbound messages join threads
In `SendMessage()`, compute `threadID` using the same logic as `DeliverMessage` — if `req.InReplyTo != ""`, set `threadID = req.InReplyTo`. Set it on the `sentMsg` before DB insert.

### 7b. Improved thread root resolution
In `DeliverMessage()`, improve thread ID resolution: parse `References` header (space-separated message-ids), use the first entry as the thread root. Fall back to `In-Reply-To` if `References` is empty. Fall back to the message's own `Message-ID` if neither is present.

---

## 8. Attachment DB Persistence

**File:** `internal/api/handlers/messages.go`

In `DeliverMessage()`, after pipeline returns `ActionContinue` with `FinalEmail`:
1. Iterate `pipelineResult.FinalEmail.Attachments` and `.Inline`.
2. For each entry where `Storage != ""` (extraction filter ran), create a `models.Attachment` row: `MessageID`, `Filename`, `ContentType`, `SizeBytes`, `StorageType: Storage`, `StorageRef: Ref`, `Checksum`.
3. Set `msg.HasAttachments = true` on the message if any attachments were persisted.

This bridges the pipeline filter (writes files to disk) and the download handler (reads from DB + serves files).

---

## 9. JS Filter vm2 Replacement

**Files:** `docker/js-filter-sidecar/server.js`, `docker/js-filter-sidecar/package.json`, `docker/js-filter-sidecar/Dockerfile`

- Replace `vm2` with Node.js built-in `vm.runInNewContext()`.
- Sandbox object: `JSON`, `Math`, `Date`, `console.log` (captured), `String`, `Array`, `Object`, `RegExp`. No `require`, `process`, `fs`, `net`.
- `timeout` option on `vm.runInNewContext()` for per-script time limits.
- Remove `vm2` from `package.json` (zero external dependencies).
- Add `--max-old-space-size=64` to Dockerfile CMD for memory limits.

---

## File Change Summary

| File | Changes |
|------|---------|
| `internal/gateway/queue/worker.go` | SMTPError type, atomic cache upsert, 5xx immediate bounce, RFC 3464 bounce rewrite |
| `internal/gateway/smtp/session.go` | Set ExpiresAt/MaxRetries on outbound queue entries, SMTP reply code mapping from API response |
| `internal/gateway/smtp/util.go` | Extract To, Cc, In-Reply-To, References from raw message |
| `internal/gateway/apiclient/client.go` | Add StatusCode to APIError, structured error returns |
| `internal/gateway/tlsutil/sni.go` | fsnotify watcher for cert hot-reload |
| `cmd/{smtp,imap,pop3}-gateway/main.go` | Wire StartWatching/Stop on SNICertLoader |
| `internal/api/handlers/messages.go` | SSE events, draft InReplyTo/References, thread ID on sent msgs, thread root from References, attachment DB rows from pipeline |
| `docker/js-filter-sidecar/server.js` | Replace vm2 with vm.runInNewContext() |
| `docker/js-filter-sidecar/package.json` | Remove vm2 dependency |
| `docker/js-filter-sidecar/Dockerfile` | Add --max-old-space-size=64 |

## Verification

1. `go build ./...` — compiles
2. `go test -race -count=1 ./internal/...` — all tests pass
3. `go vet ./...` — no issues
4. Manual inspection of each changed section
