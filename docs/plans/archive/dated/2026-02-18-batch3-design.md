# Batch 3 Design: Contacts Auto-Populate, Vacation Wiring, Queue Bulk Ops, SSE Refinements

**Date:** 2026-02-18
**Scope:** Wire contact auto-collection from sent mail, fix vacation filter disconnection + security, add queue bulk operations, improve SSE reliability

## Context

Exploration revealed that most features are substantially implemented but have critical wiring gaps. ARC verify/seal is **deferred** (requires building real DKIM crypto verification from a stub — too large for this batch).

---

## 1. Contact Auto-Populate from Sent Mail

### Current State
- Full CRUD API exists (`internal/api/handlers/contacts.go`): List, Create, Update, Delete, Block, Suggest, Import
- `models.Contact` has `Source` field with `"sent"` as a documented value, but nothing creates contacts with that source
- Unique index `idx_contacts_mailbox_email` already exists on `(mailbox_id, email)`

### Changes

**File:** `internal/api/handlers/messages.go` — `SendMessage` function

After the SSE publish block (line ~617), before the final `respond.Data`:

```go
// Auto-collect recipient contacts
for _, rcptAddr := range allRecipients {
    h.db.Where(models.Contact{MailboxID: senderMailbox.ID, Email: rcptAddr}).
        Attrs(models.Contact{
            Name:       "",
            TrustLevel: "auto",
            Source:     "sent",
        }).FirstOrCreate(&models.Contact{})
}
```

This uses GORM's `FirstOrCreate` which translates to `INSERT ... ON CONFLICT DO NOTHING` semantics with the unique index. Existing contacts (manual, imported, or previously auto-collected) are never overwritten.

---

## 2. Vacation Wiring + Security Fix

### Current State — Three Disconnected Problems

1. **Security bug:** `VacationHandler` uses `chi.URLParam(r, "id")` as mailbox_id with NO ownership check. Any authenticated user can read/modify any mailbox's vacation config.

2. **Architecture:** `VacationConfig` struct is defined locally in `handlers` package (not in `models`), and `AutoMigrate` is called in the handler constructor instead of the central `db.AutoMigrate`.

3. **Filter disconnection:** The `vacation` pipeline filter reads its Subject/Body/Days from the pipeline step's JSON config (stored in `pipelines.filters` column), NOT from the `vacation_configs` table. The API writes to `vacation_configs`; the filter never reads it. The filter also only sets metadata (`vacation_reply=true`) — nothing downstream actually sends the reply.

### Fix Strategy

**2a. Move model to `internal/db/models/`**

Create `VacationConfig` in `internal/db/models/pipeline.go` (alongside `VacationResponse` which is already there). Remove the local struct from `vacation.go`. Add to central `AutoMigrate` in `db.go`. Remove `db.AutoMigrate` call from `NewVacationHandler`.

**2b. Fix vacation handler auth**

Add `resolveMailboxID` to `VacationHandler` (same pattern as `ContactHandler`). All three methods (`GetVacation`, `SetVacation`, `DisableVacation`) must use it instead of raw `chi.URLParam`.

The handler needs to accept `*auth.JWTService` or use `middleware.GetClaims(r)` — since it's inside the JWT middleware group, `middleware.GetClaims(r)` is available.

**2c. Rewrite vacation filter to read from `vacation_configs` table**

Instead of using per-step JSON config for Subject/Body, the filter reads from `vacation_configs` via `pipeline.DBFromContext(ctx)`:

```go
func (f *vacationFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
    db := pipeline.DBFromContext(ctx)
    // For each recipient, look up vacation_configs WHERE mailbox_id = ? AND enabled = true
    // Check start_date/end_date window
    // Use existing RFC 3834 suppression + dedup logic (unchanged)
    // On match: queue reply via OutboundQueue
}
```

**2d. Actually send the vacation reply**

Instead of just setting metadata, the filter directly queues the reply into `outbound_queue` with RFC 3834 headers:

```go
reply := models.OutboundQueue{
    Sender:     rcpt,           // recipient becomes the sender of the reply
    Recipient:  sender,         // original sender receives the reply
    Domain:     senderDomain,
    RawMessage: buildVacationReply(rcpt, sender, vacCfg.Subject, vacCfg.Body, email),
    Status:     "pending",
}
db.Create(&reply)
```

The `buildVacationReply` helper constructs an RFC 2822 message with:
- `Auto-Submitted: auto-replied` (RFC 3834)
- `Precedence: bulk`
- `In-Reply-To: <original-message-id>`
- `References: <original-message-id>`
- `X-Auto-Response-Suppress: All` (Microsoft compatibility)

The per-step JSON config (`days` only) is kept for the dedup window but Subject/Body now come from DB.

---

## 3. Queue Bulk Operations

### Current State
- Full single-item CRUD: List, Get, Retry, Bounce, Delete, Stats
- Missing: bulk operations, sender/recipient filters

### Changes

**File:** `internal/api/handlers/queue.go`

Add 3 new methods:

```go
// POST /api/v1/admin/queue/bulk-retry
type bulkRequest struct {
    IDs    []uint `json:"ids"`
    Filter *struct {
        Status string `json:"status"`
        Domain string `json:"domain"`
    } `json:"filter"`
}

func (h *QueueHandler) BulkRetry(...)   // Update matching rows: status="pending", next_attempt=now()
func (h *QueueHandler) BulkBounce(...)  // Update matching rows: status="bounced"
func (h *QueueHandler) BulkDelete(...)  // DELETE matching rows
```

Each accepts either `ids` (explicit list, max 1000) or `filter` (match by status+domain). Returns `{"affected": N}`.

Also add `sender` and `recipient` query params to `ListQueue`.

**File:** `internal/api/routes.go`

Add routes in admin group:
```
POST   /api/v1/admin/queue/bulk-retry
POST   /api/v1/admin/queue/bulk-bounce
DELETE /api/v1/admin/queue/bulk-delete
```

---

## 4. SSE Refinements

### Current State
- Server: `SSEBroker` with in-memory pub/sub, 30s keepalive, non-blocking publish
- Client: `useSSE`/`useMultiAccountSSE` hooks listening for 4 event types
- Missing: `message_sent` in React handler, reconnect backoff, event replay

### Changes

**4a. Add `message_sent` to React SSE**

**File:** `webmail/src/hooks/useSSE.ts`

Add `'message_sent'` to the event listener array (currently only 4 types). Also update `App.tsx` handler.

**4b. Reconnect with exponential backoff**

Replace `es.onerror = () => {}` with a manual reconnect strategy. `EventSource` auto-reconnects but with no backoff control, so switch to manual reconnection:

```typescript
// On error, close and reconnect with exponential backoff
let delay = 1000;
const maxDelay = 30000;

es.onerror = () => {
    es.close();
    setTimeout(() => {
        reconnect();
        delay = Math.min(delay * 2, maxDelay);
    }, delay);
};
```

Reset delay to 1000 on successful open (`es.onopen`).

**4c. Event ID + replay buffer (server-side)**

**File:** `internal/api/handlers/events.go`

Add to `SSEBroker`:
- `eventCounter` — per-mailbox `atomic.Uint64` for monotonic event IDs
- `ringBuffer` — per-mailbox ring buffer of last 64 events (type + data + ID)

On `Publish`: increment counter, store in ring buffer, include `id:` field.
On `Subscribe`: accept `lastEventID` param, replay missed events from ring buffer.

SSE handler reads `Last-Event-ID` header (set automatically by `EventSource` on reconnect) and calls `broker.SubscribeWithReplay(mailboxID, lastEventID)`.

---

## File Change Summary

| File | Changes |
|------|---------|
| `internal/api/handlers/messages.go` | Contact auto-populate in SendMessage |
| `internal/db/models/pipeline.go` | Add `VacationConfig` model |
| `internal/db/db.go` | Add `VacationConfig` to central AutoMigrate |
| `internal/api/handlers/vacation.go` | Remove local struct, fix auth with resolveMailboxID |
| `internal/pipeline/filters/vacation.go` | Read from vacation_configs DB, queue actual reply |
| `internal/api/handlers/queue.go` | BulkRetry, BulkBounce, BulkDelete + sender/recipient filters |
| `internal/api/routes.go` | Register bulk queue routes |
| `internal/api/handlers/events.go` | Event IDs, ring buffer, replay on reconnect |
| `webmail/src/hooks/useSSE.ts` | Add message_sent, reconnect backoff |
| `webmail/src/App.tsx` | Handle message_sent SSE event |

## Verification

1. `go build ./...` — compiles
2. `go test -race -count=1 ./internal/...` — all tests pass
3. `go vet ./...` — no issues
4. Manual inspection of vacation auth fix, contact auto-populate, bulk operations
