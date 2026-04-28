# Batch 3 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Wire contact auto-collection, fix vacation filter disconnection + security, add queue bulk ops, improve SSE reliability

**Architecture:** Contact auto-populate uses GORM `FirstOrCreate` on send. Vacation filter rewired to read `vacation_configs` table and queue actual replies via `outbound_queue`. Queue gets bulk operations. SSE gets event IDs, ring buffer replay, and reconnect backoff.

**Tech Stack:** Go 1.24+, GORM/PostgreSQL, chi router, React/TypeScript

---

### Task 1: Contact Auto-Populate from Sent Mail

**Files:**
- Modify: `internal/api/handlers/messages.go:607-619` (SendMessage, after SSE publish)

**Step 1: Add contact auto-collection after successful send**

In `SendMessage`, after the SSE publish block and before the final `respond.Data`, add:

```go
// Auto-collect recipient contacts (silent — errors don't affect the send)
for _, rcptAddr := range allRecipients {
    contact := models.Contact{MailboxID: senderMailbox.ID, Email: rcptAddr}
    h.db.Where(contact).Attrs(models.Contact{
        Name:       "",
        TrustLevel: "auto",
        Source:     "sent",
    }).FirstOrCreate(&models.Contact{})
}
```

This goes right after the `h.broker.Publish(...)` block (around line 617) and before `respond.Data(w, http.StatusCreated, sentMsg)` (line 619).

`FirstOrCreate` + the existing unique index `(mailbox_id, email)` means: if a contact already exists for this mailbox+email, do nothing. Otherwise create with `source="sent"`, `trust_level="auto"`.

**Step 2: Verify compilation**

Run: `go build ./cmd/api/...`

**Step 3: Commit**

```bash
git add internal/api/handlers/messages.go
git commit -m "feat: auto-collect recipient contacts on send"
```

---

### Task 2: Move VacationConfig Model to Models Package

**Files:**
- Modify: `internal/db/models/pipeline.go` (add VacationConfig struct after VacationResponse)
- Modify: `internal/db/db.go` (add VacationConfig to AutoMigrate)
- Modify: `internal/api/handlers/vacation.go` (remove local VacationConfig, use models.VacationConfig, remove handler AutoMigrate)

**Step 1: Add VacationConfig to models package**

In `internal/db/models/pipeline.go`, after the `VacationResponse` struct (around line 129), add:

```go
// VacationConfig stores out-of-office auto-reply settings for a mailbox.
type VacationConfig struct {
    ID        uint       `gorm:"primaryKey" json:"id"`
    MailboxID uint       `gorm:"uniqueIndex;not null" json:"mailbox_id"`
    Enabled   bool       `gorm:"default:false" json:"enabled"`
    Subject   string     `gorm:"size:500" json:"subject"`
    Body      string     `gorm:"type:text" json:"body"`
    StartDate *time.Time `json:"start_date,omitempty"`
    EndDate   *time.Time `json:"end_date,omitempty"`
    CreatedAt time.Time  `json:"created_at"`
    UpdatedAt time.Time  `json:"updated_at"`

    Mailbox Mailbox `gorm:"foreignKey:MailboxID" json:"-"`
}

func (VacationConfig) TableName() string { return "vacation_configs" }
```

**Step 2: Add to central AutoMigrate**

In `internal/db/db.go`, add `&models.VacationConfig{}` to the `AutoMigrate` call, after `&models.SieveScript{}`:

```go
&models.SieveScript{},
&models.VacationConfig{},
// RESTMAIL protocol upgrade cache
&models.RESTMAILCapability{},
```

**Step 3: Update vacation handler to use models.VacationConfig**

In `internal/api/handlers/vacation.go`:

1. Remove the local `VacationConfig` struct (lines 15-27) and `TableName` func (line 27)
2. Add import: `"github.com/restmail/restmail/internal/db/models"`
3. Replace all `VacationConfig` references with `models.VacationConfig`
4. Remove `db.AutoMigrate(&VacationConfig{})` from `NewVacationHandler` (line 37)

The handler constructor becomes:
```go
func NewVacationHandler(db *gorm.DB) *VacationHandler {
    return &VacationHandler{db: db}
}
```

**Step 4: Verify compilation**

Run: `go build ./...`

**Step 5: Commit**

```bash
git add internal/db/models/pipeline.go internal/db/db.go internal/api/handlers/vacation.go
git commit -m "refactor: move VacationConfig to models package and central AutoMigrate"
```

---

### Task 3: Fix Vacation Handler Auth

**Files:**
- Modify: `internal/api/handlers/vacation.go`

**Step 1: Add auth resolution to VacationHandler**

The handler needs to use `middleware.GetClaims(r)` to verify ownership, same pattern as `ContactHandler.resolveMailboxID`. Since the vacation routes are inside the JWT middleware group, claims are available.

Add import for `middleware`:
```go
"github.com/restmail/restmail/internal/api/middleware"
```

Add a `resolveMailboxID` method to `VacationHandler`:
```go
func (h *VacationHandler) resolveMailboxID(r *http.Request, accountIDStr string) (uint, error) {
    claims := middleware.GetClaims(r)
    if claims == nil {
        return 0, fmt.Errorf("no claims")
    }

    accountID, err := strconv.ParseUint(accountIDStr, 10, 32)
    if err != nil {
        return 0, err
    }

    // Check if this is the user's primary account
    var account models.WebmailAccount
    if err := h.db.First(&account, accountID).Error; err == nil {
        if account.ID == claims.WebmailAccountID {
            return account.PrimaryMailboxID, nil
        }
    }

    // Check linked accounts
    var linked models.LinkedAccount
    if err := h.db.Where("webmail_account_id = ? AND id = ?", claims.WebmailAccountID, accountID).First(&linked).Error; err == nil {
        return linked.MailboxID, nil
    }

    return 0, fmt.Errorf("access denied")
}
```

Add imports: `"fmt"`, models package.

**Step 2: Update all three handler methods**

Replace the raw `chi.URLParam` + `strconv.ParseUint` pattern in `GetVacation`, `SetVacation`, `DisableVacation` with:

```go
mailboxID, err := h.resolveMailboxID(r, chi.URLParam(r, "id"))
if err != nil {
    respond.Error(w, http.StatusForbidden, "forbidden", "Access denied")
    return
}
```

Remove the old `strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)` lines.

**Step 3: Verify compilation**

Run: `go build ./cmd/api/...`

**Step 4: Commit**

```bash
git add internal/api/handlers/vacation.go
git commit -m "fix: add ownership check to vacation handler endpoints"
```

---

### Task 4: Rewrite Vacation Filter to Read from DB + Queue Replies

**Files:**
- Modify: `internal/pipeline/filters/vacation.go`

**Step 1: Rewrite Execute to read from vacation_configs table**

The filter should:
1. Get `*gorm.DB` from context via `pipeline.DBFromContext(ctx)`
2. For each recipient, look up `vacation_configs WHERE mailbox_id = ? AND enabled = true`
3. Check date window (`start_date`/`end_date`)
4. Keep existing RFC 3834 suppression + dedup logic
5. Instead of setting metadata, directly queue the reply into `outbound_queue`

The `vacationConfig` per-step struct should be simplified to just `Days` (dedup window):

```go
type vacationConfig struct {
    Days int `json:"days"` // dedup window in days, default 7
}
```

Add import for `models`:
```go
"github.com/restmail/restmail/internal/db/models"
```

The rewritten `Execute`:

```go
func (f *vacationFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
    db := pipeline.DBFromContext(ctx)
    if db == nil {
        db = f.db // fallback
    }

    sender := email.Envelope.MailFrom
    if sender == "" && len(email.Headers.From) > 0 {
        sender = email.Headers.From[0].Address
    }
    sender = strings.TrimSpace(strings.ToLower(sender))

    if reason := f.shouldSuppress(sender, email); reason != "" {
        return f.skip(reason), nil
    }

    replied := false
    now := time.Now()

    for _, rcpt := range email.Envelope.RcptTo {
        parts := strings.SplitN(rcpt, "@", 2)
        if len(parts) != 2 {
            continue
        }

        // Look up mailbox
        var mailboxID uint
        err := db.Table("mailboxes").
            Joins("JOIN domains ON domains.id = mailboxes.domain_id").
            Where("mailboxes.local_part = ? AND domains.name = ? AND mailboxes.active = ?",
                parts[0], parts[1], true).
            Pluck("mailboxes.id", &mailboxID).Error
        if err != nil || mailboxID == 0 {
            continue
        }

        // Look up vacation config from DB
        var vacCfg models.VacationConfig
        if err := db.Where("mailbox_id = ? AND enabled = ?", mailboxID, true).First(&vacCfg).Error; err != nil {
            continue // no vacation config or not enabled
        }

        // Check date window
        if vacCfg.StartDate != nil && now.Before(*vacCfg.StartDate) {
            continue
        }
        if vacCfg.EndDate != nil && now.After(*vacCfg.EndDate) {
            continue
        }

        // Dedup check
        cutoff := now.Add(-time.Duration(f.cfg.Days) * 24 * time.Hour)
        var recentCount int64
        db.Table("vacation_responses").
            Where("mailbox_id = ? AND sender = ? AND responded_at > ?",
                mailboxID, sender, cutoff).
            Count(&recentCount)
        if recentCount > 0 {
            continue
        }

        // Record response for dedup
        db.Table("vacation_responses").Create(map[string]interface{}{
            "mailbox_id":   mailboxID,
            "sender":       sender,
            "responded_at": now,
        })

        // Queue the actual reply
        senderDomain := parts[1]
        if idx := strings.LastIndex(sender, "@"); idx >= 0 {
            senderDomain = sender[idx+1:]
        }

        originalMsgID := email.Headers.MessageID
        rawReply := buildVacationReply(rcpt, sender, vacCfg.Subject, vacCfg.Body, originalMsgID)

        db.Create(&models.OutboundQueue{
            Sender:     rcpt,
            Recipient:  sender,
            Domain:     senderDomain,
            RawMessage: rawReply,
            Status:     "pending",
        })

        replied = true
    }

    if !replied {
        return f.skip("no eligible recipients or already responded"), nil
    }

    return &pipeline.FilterResult{
        Type:   pipeline.FilterTypeAction,
        Action: pipeline.ActionContinue,
        Log: pipeline.FilterLog{
            Filter: "vacation",
            Result: "queued",
            Detail: fmt.Sprintf("vacation auto-reply queued for sender %s", sender),
        },
    }, nil
}
```

**Step 2: Add the buildVacationReply helper**

```go
// buildVacationReply constructs an RFC 2822 vacation auto-reply with RFC 3834 headers.
func buildVacationReply(from, to, subject, body, inReplyTo string) string {
    var b strings.Builder
    b.WriteString("From: " + from + "\r\n")
    b.WriteString("To: " + to + "\r\n")
    b.WriteString("Subject: " + subject + "\r\n")
    b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
    if inReplyTo != "" {
        b.WriteString("In-Reply-To: " + inReplyTo + "\r\n")
        b.WriteString("References: " + inReplyTo + "\r\n")
    }
    b.WriteString("Auto-Submitted: auto-replied\r\n")
    b.WriteString("Precedence: bulk\r\n")
    b.WriteString("X-Auto-Response-Suppress: All\r\n")
    b.WriteString("MIME-Version: 1.0\r\n")
    b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
    b.WriteString("\r\n")
    b.WriteString(body + "\r\n")
    return b.String()
}
```

**Step 3: Verify compilation**

Run: `go build ./...`

**Step 4: Run existing pipeline tests**

Run: `go test -race -count=1 ./internal/pipeline/...`

**Step 5: Commit**

```bash
git add internal/pipeline/filters/vacation.go
git commit -m "fix: wire vacation filter to vacation_configs DB table and queue actual replies"
```

---

### Task 5: Queue Bulk Operations

**Files:**
- Modify: `internal/api/handlers/queue.go`
- Modify: `internal/api/routes.go`

**Step 1: Add sender/recipient filters to ListQueue**

In `ListQueue`, after the existing `domain` filter (around line 31):

```go
if sender := r.URL.Query().Get("sender"); sender != "" {
    query = query.Where("sender ILIKE ?", "%"+sender+"%")
}
if recipient := r.URL.Query().Get("recipient"); recipient != "" {
    query = query.Where("recipient ILIKE ?", "%"+recipient+"%")
}
```

**Step 2: Add bulk request type and helper**

```go
type bulkQueueRequest struct {
    IDs    []uint `json:"ids"`
    Filter *struct {
        Status string `json:"status"`
        Domain string `json:"domain"`
    } `json:"filter"`
}

// buildBulkQuery returns a GORM query scoped to the requested IDs or filter.
func (h *QueueHandler) buildBulkQuery(req bulkQueueRequest) *gorm.DB {
    query := h.db.Model(&models.OutboundQueue{})
    if len(req.IDs) > 0 {
        return query.Where("id IN ?", req.IDs)
    }
    if req.Filter != nil {
        if req.Filter.Status != "" {
            query = query.Where("status = ?", req.Filter.Status)
        }
        if req.Filter.Domain != "" {
            query = query.Where("domain = ?", req.Filter.Domain)
        }
    }
    return query
}
```

**Step 3: Add BulkRetry**

```go
// POST /api/v1/admin/queue/bulk-retry
func (h *QueueHandler) BulkRetry(w http.ResponseWriter, r *http.Request) {
    var req bulkQueueRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
        return
    }
    if len(req.IDs) == 0 && req.Filter == nil {
        respond.Error(w, http.StatusBadRequest, "bad_request", "ids or filter required")
        return
    }
    if len(req.IDs) > 1000 {
        respond.Error(w, http.StatusBadRequest, "bad_request", "max 1000 IDs per request")
        return
    }

    result := h.buildBulkQuery(req).
        Where("status IN ?", []string{"deferred", "pending"}).
        Updates(map[string]interface{}{
            "status":       "pending",
            "next_attempt": time.Now(),
        })

    respond.Data(w, http.StatusOK, map[string]int64{"affected": result.RowsAffected})
}
```

**Step 4: Add BulkBounce**

```go
// POST /api/v1/admin/queue/bulk-bounce
func (h *QueueHandler) BulkBounce(w http.ResponseWriter, r *http.Request) {
    var req bulkQueueRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
        return
    }
    if len(req.IDs) == 0 && req.Filter == nil {
        respond.Error(w, http.StatusBadRequest, "bad_request", "ids or filter required")
        return
    }
    if len(req.IDs) > 1000 {
        respond.Error(w, http.StatusBadRequest, "bad_request", "max 1000 IDs per request")
        return
    }

    result := h.buildBulkQuery(req).Updates(map[string]interface{}{
        "status":     "bounced",
        "last_error": "manually bounced by admin (bulk)",
    })

    respond.Data(w, http.StatusOK, map[string]int64{"affected": result.RowsAffected})
}
```

**Step 5: Add BulkDelete**

```go
// DELETE /api/v1/admin/queue/bulk-delete
func (h *QueueHandler) BulkDelete(w http.ResponseWriter, r *http.Request) {
    var req bulkQueueRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
        return
    }
    if len(req.IDs) == 0 && req.Filter == nil {
        respond.Error(w, http.StatusBadRequest, "bad_request", "ids or filter required")
        return
    }
    if len(req.IDs) > 1000 {
        respond.Error(w, http.StatusBadRequest, "bad_request", "max 1000 IDs per request")
        return
    }

    result := h.buildBulkQuery(req).Delete(&models.OutboundQueue{})

    respond.Data(w, http.StatusOK, map[string]int64{"affected": result.RowsAffected})
}
```

**Step 6: Add import for `encoding/json`**

Add to the import block in `queue.go`:
```go
"encoding/json"
```

**Step 7: Register routes**

In `internal/api/routes.go`, in the admin group, after the existing queue routes (around line 227):

```go
// Queue bulk operations
r.Post("/api/v1/admin/queue/bulk-retry", queueH.BulkRetry)
r.Post("/api/v1/admin/queue/bulk-bounce", queueH.BulkBounce)
r.Delete("/api/v1/admin/queue/bulk-delete", queueH.BulkDelete)
```

**IMPORTANT:** These must be registered BEFORE `r.Get("/api/v1/admin/queue/{id}", ...)` to avoid `bulk-retry` being captured by `{id}`. Alternatively, since they are POST/DELETE (not GET), they won't conflict with the GET `{id}` route. But the safest approach is to place them after `/stats` and before `/{id}`:

```go
r.Get("/api/v1/admin/queue", queueH.ListQueue)
r.Get("/api/v1/admin/queue/stats", queueH.QueueStats)
r.Post("/api/v1/admin/queue/bulk-retry", queueH.BulkRetry)
r.Post("/api/v1/admin/queue/bulk-bounce", queueH.BulkBounce)
r.Delete("/api/v1/admin/queue/bulk-delete", queueH.BulkDelete)
r.Get("/api/v1/admin/queue/{id}", queueH.GetQueueEntry)
r.Post("/api/v1/admin/queue/{id}/retry", queueH.RetryQueueEntry)
r.Post("/api/v1/admin/queue/{id}/bounce", queueH.BounceQueueEntry)
r.Delete("/api/v1/admin/queue/{id}", queueH.DeleteQueueEntry)
```

**Step 8: Verify compilation**

Run: `go build ./...`

**Step 9: Commit**

```bash
git add internal/api/handlers/queue.go internal/api/routes.go
git commit -m "feat: add queue bulk operations and sender/recipient filters"
```

---

### Task 6: SSE Event IDs and Ring Buffer Replay

**Files:**
- Modify: `internal/api/handlers/events.go`

**Step 1: Add event tracking types**

Add after `SSEBroker` struct:

```go
type numberedEvent struct {
    ID    uint64
    Event SSEEvent
}

type mailboxState struct {
    counter    atomic.Uint64
    ringBuffer [64]numberedEvent
    ringIdx    atomic.Uint64 // next write position
}
```

Update `SSEBroker`:
```go
type SSEBroker struct {
    mu          sync.RWMutex
    subscribers map[uint]map[chan numberedEvent]struct{}
    states      sync.Map // uint (mailboxID) -> *mailboxState
}
```

**Step 2: Update Subscribe/Unsubscribe/Publish**

`Subscribe` now returns `chan numberedEvent` (buffered 16).
`Publish` increments counter, stores in ring buffer, fans out `numberedEvent`.

Add `SubscribeWithReplay(mailboxID uint, lastEventID uint64) chan numberedEvent`:
- Creates the channel
- Replays any events from ring buffer where ID > lastEventID
- Registers the channel

**Step 3: Update Events handler**

Read `Last-Event-ID` header from request. If present, call `SubscribeWithReplay`. In the event loop, write `id:` field:

```go
fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Event.Type, data)
```

**Step 4: Verify compilation**

Run: `go build ./cmd/api/...`

**Step 5: Commit**

```bash
git add internal/api/handlers/events.go
git commit -m "feat: add SSE event IDs and ring buffer replay on reconnect"
```

---

### Task 7: SSE React Reconnect Backoff + message_sent

**Files:**
- Modify: `webmail/src/hooks/useSSE.ts`
- Modify: `webmail/src/App.tsx`

**Step 1: Add message_sent event type and reconnect backoff to useSSE.ts**

Rewrite `useSSE` to use manual reconnection with exponential backoff:

```typescript
export function useSSE(accountId: number | null, onEvent: (event: SSEEvent) => void) {
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  useEffect(() => {
    if (!accountId) return;
    const token = getToken();
    if (!token) return;

    let es: EventSource | null = null;
    let delay = 1000;
    const maxDelay = 30000;
    let closed = false;

    const eventTypes = ['new_message', 'folder_update', 'message_updated', 'message_deleted', 'message_sent'];

    function connect() {
      if (closed) return;
      const url = `/api/v1/accounts/${accountId}/events?token=${encodeURIComponent(token)}`;
      es = new EventSource(url);

      const handleEvent = (e: MessageEvent) => {
        try { onEventRef.current({ type: e.type, data: JSON.parse(e.data) }); }
        catch { /* ignore malformed */ }
      };

      eventTypes.forEach(type => es!.addEventListener(type, handleEvent));

      es.onopen = () => { delay = 1000; }; // reset backoff on success

      es.onerror = () => {
        es?.close();
        if (!closed) {
          setTimeout(connect, delay);
          delay = Math.min(delay * 2, maxDelay);
        }
      };
    }

    connect();

    return () => {
      closed = true;
      es?.close();
    };
  }, [accountId]);
}
```

Apply similar changes to `useMultiAccountSSE`.

**Step 2: Handle message_sent in App.tsx**

Find the SSE event handler in `App.tsx` and add a case for `message_sent`:

```typescript
case 'message_sent':
    refresh();
    break;
```

**Step 3: Verify frontend builds**

Run: `cd webmail && npm run build` (if npm is available, otherwise just verify TypeScript compiles)

**Step 4: Commit**

```bash
git add webmail/src/hooks/useSSE.ts webmail/src/App.tsx
git commit -m "feat: add SSE message_sent event, reconnect backoff, event ID support"
```

---

### Task 8: Final Verification

**Step 1: Full build check**

Run: `go build ./...`
Expected: Clean compilation

**Step 2: Run all tests**

Run: `go test -race -count=1 ./internal/...`
Expected: All tests pass

**Step 3: Vet check**

Run: `go vet ./...`
Expected: No issues
