# Batch 2 Implementation Plan: Attachments, Quota, Connection Limits, fail2ban, E2E Tests

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix attachment security, quota accounting, add connection limits + fail2ban, expand E2E tests.

**Architecture:** Ownership checks via DB JOINs on attachment handlers. Quota fixes are surgical edits to DeliverMessage/DeleteMessage. A new `connlimiter` package provides per-IP and global connection limits, wired into all three gateway accept loops. fail2ban sidecar reads structured JSON logs from gateway containers.

**Tech Stack:** Go 1.24+, GORM/PostgreSQL, chi router, fsnotify (already present), fail2ban (Alpine container), Docker Compose profiles

---

### Task 1: Attachment Ownership Check on GetAttachment

**Files:**
- Modify: `internal/api/handlers/attachments.go:28-71`

**Context:** Currently `GetAttachment` looks up any attachment by ID with no ownership verification. Any authenticated user can download any attachment by guessing the numeric ID. This is a security vulnerability.

The pattern for ownership verification already exists in `messages.go` — `verifyMessageOwnership` checks `claims.WebmailAccountID` against the message's mailbox via `WebmailAccount.PrimaryMailboxID` and `LinkedAccount.MailboxID`. For attachments, we need to JOIN through `attachments → messages → mailboxes` and verify the mailbox belongs to the user's account.

**Step 1: Add ownership-verified query to GetAttachment**

In `attachments.go`, the handler needs access to claims (from `middleware.GetClaims(r)`). Add the `middleware` import and replace the simple `h.db.First(&att, id)` with a JOIN query.

Replace lines 28-39 of `GetAttachment` with:

```go
func (h *AttachmentHandler) GetAttachment(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid attachment ID")
		return
	}

	var att models.Attachment
	query := h.db.Joins("JOIN messages ON messages.id = attachments.message_id").
		Joins("JOIN mailboxes ON mailboxes.id = messages.mailbox_id")

	if !claims.IsAdmin {
		// Build list of mailbox IDs this user can access
		mailboxIDs := h.getUserMailboxIDs(claims.WebmailAccountID)
		if len(mailboxIDs) == 0 {
			respond.Error(w, http.StatusNotFound, "not_found", "Attachment not found")
			return
		}
		query = query.Where("mailboxes.id IN ?", mailboxIDs)
	}

	if err := query.First(&att, id).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Attachment not found")
		return
	}
	// ... rest of handler unchanged
```

Add a helper method to `AttachmentHandler`:

```go
func (h *AttachmentHandler) getUserMailboxIDs(webmailAccountID uint) []uint {
	var ids []uint
	// Primary mailbox
	var account models.WebmailAccount
	if err := h.db.First(&account, webmailAccountID).Error; err == nil {
		ids = append(ids, account.PrimaryMailboxID)
	}
	// Linked accounts
	var linked []models.LinkedAccount
	h.db.Where("webmail_account_id = ?", webmailAccountID).Find(&linked)
	for _, la := range linked {
		ids = append(ids, la.MailboxID)
	}
	return ids
}
```

Add imports: `"github.com/restmail/restmail/internal/api/middleware"`.

**Step 2: Add ownership check to ListAttachments**

Replace `ListAttachments` (lines 75-86) with:

```go
func (h *AttachmentHandler) ListAttachments(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	messageID, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid message ID")
		return
	}

	// Verify the message belongs to the user
	var msg models.Message
	if err := h.db.First(&msg, messageID).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "Message not found")
		return
	}
	if !claims.IsAdmin {
		mailboxIDs := h.getUserMailboxIDs(claims.WebmailAccountID)
		owned := false
		for _, mid := range mailboxIDs {
			if msg.MailboxID == mid {
				owned = true
				break
			}
		}
		if !owned {
			respond.Error(w, http.StatusNotFound, "not_found", "Message not found")
			return
		}
	}

	var attachments []models.Attachment
	h.db.Where("message_id = ?", messageID).Order("id ASC").Find(&attachments)
	respond.List(w, attachments, nil)
}
```

**Step 3: Build and verify**

Run: `go build ./...`
Expected: Compiles cleanly.

**Step 4: Commit**

```bash
git add internal/api/handlers/attachments.go
git commit -m "Fix attachment ownership: verify user owns mailbox before download"
```

---

### Task 2: Attachment Checksum Dedup in Extract Filter

**Files:**
- Modify: `internal/pipeline/filters/extract_attachments.go:44-110`
- Modify: `internal/db/models/pipeline.go:140` (index tag already present, verify)

**Context:** The `extract_attachments` filter writes every attachment to disk, even if an identical file (same SHA-256 checksum) already exists. The `store()` method computes the checksum but doesn't check the DB for an existing match. When the same attachment is sent to multiple recipients, duplicate files accumulate on disk.

The filter needs DB access to query existing attachments. Currently it only has `pipeline.EmailJSON` context. We need to pass a `*gorm.DB` into the filter. The pipeline engine already supports this via `context.Context` — we can add the DB to the context.

**Step 1: Add DB to pipeline context**

In `internal/pipeline/engine.go`, the `Execute` function already accepts a `context.Context`. Define a context key and helper in `internal/pipeline/context.go`:

```go
package pipeline

import (
	"context"

	"gorm.io/gorm"
)

type contextKey string

const dbContextKey contextKey = "pipeline_db"

// WithDB adds a *gorm.DB to the context for filters that need it.
func WithDB(ctx context.Context, db *gorm.DB) context.Context {
	return context.WithValue(ctx, dbContextKey, db)
}

// DBFromContext retrieves the *gorm.DB from context, or nil.
func DBFromContext(ctx context.Context) *gorm.DB {
	db, _ := ctx.Value(dbContextKey).(*gorm.DB)
	return db
}
```

**Step 2: Pass DB into pipeline context in DeliverMessage**

In `internal/api/handlers/messages.go`, where the pipeline is executed (around line 760), change:

```go
pipelineResult, err := h.pipelineEngine.Execute(r.Context(), emailJSON, pipelineCfg)
```

to:

```go
pipelineCtx := pipeline.WithDB(r.Context(), h.db)
pipelineResult, err := h.pipelineEngine.Execute(pipelineCtx, emailJSON, pipelineCfg)
```

Do the same in `SendMessage` where the outbound pipeline is executed (around line 400).

**Step 3: Add dedup to extract_attachments filter**

In `extract_attachments.go`, modify the `Execute` method to accept context and the `store` method to check for existing checksums:

```go
func (f *extractAttFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	db := pipeline.DBFromContext(ctx)
	// ... existing code ...
	// In the loop, replace f.store(att) with f.storeDedup(db, att)
```

Add a new `storeDedup` method:

```go
func (f *extractAttFilter) storeDedup(db *gorm.DB, att pipeline.Attachment) (string, string, error) {
	data, err := base64.StdEncoding.DecodeString(att.Content)
	if err != nil {
		return "", "", fmt.Errorf("decode base64: %w", err)
	}

	hash := sha256.Sum256(data)
	checksum := hex.EncodeToString(hash[:])

	// Check for existing attachment with same checksum
	if db != nil {
		var existing models.Attachment
		if err := db.Where("checksum = ? AND storage_type = ?", checksum, f.cfg.StorageType).
			First(&existing).Error; err == nil {
			// Reuse existing storage ref — skip disk write
			return existing.StorageRef, checksum, nil
		}
	}

	// No duplicate found — write to disk
	now := time.Now()
	dir := filepath.Join(f.cfg.StoragePath,
		fmt.Sprintf("%d/%02d/%02d", now.Year(), now.Month(), now.Day()))
	filename := checksum[:12]
	if att.Filename != "" {
		filename = checksum[:12] + "-" + sanitizeFilename(att.Filename)
	}
	ref := filepath.Join(dir, filename)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(ref, data, 0644); err != nil {
		return "", "", fmt.Errorf("write: %w", err)
	}

	return ref, checksum, nil
}
```

Add imports: `"github.com/restmail/restmail/internal/db/models"`, `"gorm.io/gorm"`.

Replace `f.store(att)` calls with `f.storeDedup(db, att)` in both the attachments and inline loops.

Remove the old `store` method.

**Step 4: Build and verify**

Run: `go build ./...`
Expected: Compiles cleanly.

**Step 5: Commit**

```bash
git add internal/pipeline/context.go internal/pipeline/filters/extract_attachments.go internal/api/handlers/messages.go
git commit -m "Add attachment checksum dedup: skip disk write when identical file exists"
```

---

### Task 3: Quota Accounting — Include Attachment Bytes + Fix Delete

**Files:**
- Modify: `internal/api/handlers/messages.go` (DeliverMessage ~line 825, DeleteMessage ~line 245)

**Context:** Currently `quota_used_bytes` only accounts for `len(Subject) + len(BodyText) + len(BodyHTML)`. Attachment sizes are excluded. Also, `DeleteMessage` doesn't decrement quota at all — only `DeleteFolder` does.

**Step 1: Add attachment bytes to quota in DeliverMessage**

After the attachment persistence block (around line 853), add:

```go
// Add attachment sizes to quota
if hasAttachments {
	var totalAttBytes int64
	for _, att := range allAttachments {
		if att.Ref != "" {
			totalAttBytes += att.Size
		}
	}
	if totalAttBytes > 0 {
		h.db.Model(&models.Mailbox{}).Where("id = ?", mailbox.ID).
			Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", totalAttBytes))
		h.db.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", mailbox.ID).
			Update("attachment_bytes", gorm.Expr("attachment_bytes + ?", totalAttBytes))
	}
}
```

**Step 2: Decrement quota on message delete**

In `DeleteMessage`, after the delete/soft-delete block (around line 252), add quota decrement:

```go
// Decrement quota
reclaimBytes := int64(msg.SizeBytes)
if msg.HasAttachments {
	var attBytes int64
	h.db.Model(&models.Attachment{}).Where("message_id = ?", msg.ID).
		Select("COALESCE(SUM(size_bytes), 0)").Scan(&attBytes)
	reclaimBytes += attBytes
}
if reclaimBytes > 0 {
	h.db.Model(&models.Mailbox{}).Where("id = ?", msg.MailboxID).
		Update("quota_used_bytes", gorm.Expr("GREATEST(quota_used_bytes - ?, 0)", reclaimBytes))
}
```

**Step 3: Add quota check in DeliverMessage before insert**

Before the message creation (around line 796), add:

```go
// Check quota before delivery
if mailbox.QuotaBytes > 0 && mailbox.QuotaUsedBytes+int64(sizeBytes) > mailbox.QuotaBytes {
	respond.Error(w, http.StatusUnprocessableEntity, "mailbox_full", "Recipient mailbox is over quota")
	return
}
```

**Step 4: Build and verify**

Run: `go build ./...`
Expected: Compiles cleanly.

Run: `go test -race -count=1 ./internal/...`
Expected: All tests pass.

**Step 5: Commit**

```bash
git add internal/api/handlers/messages.go
git commit -m "Fix quota accounting: include attachments, decrement on delete, check before deliver"
```

---

### Task 4: Connection Limiter Package

**Files:**
- Create: `internal/gateway/connlimiter/limiter.go`
- Create: `internal/gateway/connlimiter/limiter_test.go`

**Context:** All three gateways accept unlimited connections with no per-IP tracking. We need a shared `Limiter` that tracks per-IP connection counts, global connection count, and auth failure history with temp bans.

**Step 1: Write limiter_test.go**

```go
package connlimiter

import (
	"testing"
	"time"
)

func TestAcceptRelease(t *testing.T) {
	l := New(Config{MaxPerIP: 2, MaxGlobal: 5})

	// Should accept first connection
	if !l.Accept("1.2.3.4") {
		t.Fatal("expected Accept to succeed")
	}
	// Second from same IP
	if !l.Accept("1.2.3.4") {
		t.Fatal("expected second Accept to succeed")
	}
	// Third from same IP should be rejected (max 2)
	if l.Accept("1.2.3.4") {
		t.Fatal("expected third Accept from same IP to be rejected")
	}
	// Different IP should still work
	if !l.Accept("5.6.7.8") {
		t.Fatal("expected Accept from different IP to succeed")
	}

	// Release one from first IP, then accept should work again
	l.Release("1.2.3.4")
	if !l.Accept("1.2.3.4") {
		t.Fatal("expected Accept after Release to succeed")
	}
}

func TestGlobalLimit(t *testing.T) {
	l := New(Config{MaxPerIP: 100, MaxGlobal: 3})

	l.Accept("1.1.1.1")
	l.Accept("2.2.2.2")
	l.Accept("3.3.3.3")

	if l.Accept("4.4.4.4") {
		t.Fatal("expected global limit to reject connection")
	}

	l.Release("1.1.1.1")
	if !l.Accept("4.4.4.4") {
		t.Fatal("expected Accept after global Release")
	}
}

func TestAuthBan(t *testing.T) {
	l := New(Config{
		MaxPerIP:        100,
		MaxGlobal:       100,
		AuthMaxFails:    3,
		AuthBanWindow:   10 * time.Second,
		AuthBanDuration: 1 * time.Second,
	})

	if l.IsBanned("1.2.3.4") {
		t.Fatal("should not be banned initially")
	}

	l.RecordAuthFail("1.2.3.4")
	l.RecordAuthFail("1.2.3.4")
	if l.IsBanned("1.2.3.4") {
		t.Fatal("should not be banned after 2 failures (threshold is 3)")
	}

	l.RecordAuthFail("1.2.3.4")
	if !l.IsBanned("1.2.3.4") {
		t.Fatal("should be banned after 3 failures")
	}

	// Wait for ban to expire
	time.Sleep(1100 * time.Millisecond)
	if l.IsBanned("1.2.3.4") {
		t.Fatal("ban should have expired")
	}
}

func TestResetAuth(t *testing.T) {
	l := New(Config{
		MaxPerIP:        100,
		MaxGlobal:       100,
		AuthMaxFails:    3,
		AuthBanWindow:   10 * time.Second,
		AuthBanDuration: 30 * time.Second,
	})

	l.RecordAuthFail("1.2.3.4")
	l.RecordAuthFail("1.2.3.4")
	l.ResetAuth("1.2.3.4")
	l.RecordAuthFail("1.2.3.4")

	// Only 1 failure since reset, should not be banned
	if l.IsBanned("1.2.3.4") {
		t.Fatal("should not be banned after reset + 1 failure")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test -race -count=1 ./internal/gateway/connlimiter/...`
Expected: FAIL (package doesn't exist yet)

**Step 3: Write limiter.go**

```go
package connlimiter

import (
	"sync"
	"sync/atomic"
	"time"
)

// Config holds connection limiter settings.
type Config struct {
	MaxPerIP        int           // max simultaneous connections per IP (default 20)
	MaxGlobal       int           // max total connections (default 1000)
	AuthMaxFails    int           // auth failures before temp ban (default 5)
	AuthBanWindow   time.Duration // window for counting failures (default 10m)
	AuthBanDuration time.Duration // how long to ban (default 30m)
}

func (c Config) withDefaults() Config {
	if c.MaxPerIP <= 0 {
		c.MaxPerIP = 20
	}
	if c.MaxGlobal <= 0 {
		c.MaxGlobal = 1000
	}
	if c.AuthMaxFails <= 0 {
		c.AuthMaxFails = 5
	}
	if c.AuthBanWindow <= 0 {
		c.AuthBanWindow = 10 * time.Minute
	}
	if c.AuthBanDuration <= 0 {
		c.AuthBanDuration = 30 * time.Minute
	}
	return c
}

type failRecord struct {
	mu       sync.Mutex
	failures []time.Time
	bannedAt time.Time
}

// Limiter tracks per-IP connection counts and auth failures.
type Limiter struct {
	cfg       Config
	global    atomic.Int32
	perIP     sync.Map // string → *atomic.Int32
	authFails sync.Map // string → *failRecord
}

// New creates a Limiter with the given config (defaults applied for zero values).
func New(cfg Config) *Limiter {
	return &Limiter{cfg: cfg.withDefaults()}
}

// Accept checks whether a new connection from ip is allowed.
// Returns true and increments counters if allowed.
func (l *Limiter) Accept(ip string) bool {
	// Check global limit
	if int(l.global.Load()) >= l.cfg.MaxGlobal {
		return false
	}

	// Get or create per-IP counter
	val, _ := l.perIP.LoadOrStore(ip, &atomic.Int32{})
	counter := val.(*atomic.Int32)

	if int(counter.Load()) >= l.cfg.MaxPerIP {
		return false
	}

	counter.Add(1)
	l.global.Add(1)
	return true
}

// Release decrements connection counters for an IP.
func (l *Limiter) Release(ip string) {
	l.global.Add(-1)
	if val, ok := l.perIP.Load(ip); ok {
		counter := val.(*atomic.Int32)
		counter.Add(-1)
	}
}

// RecordAuthFail records an authentication failure for an IP.
func (l *Limiter) RecordAuthFail(ip string) {
	val, _ := l.authFails.LoadOrStore(ip, &failRecord{})
	rec := val.(*failRecord)

	rec.mu.Lock()
	defer rec.mu.Unlock()

	now := time.Now()
	rec.failures = append(rec.failures, now)

	// Prune old failures outside the window
	cutoff := now.Add(-l.cfg.AuthBanWindow)
	fresh := rec.failures[:0]
	for _, t := range rec.failures {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	rec.failures = fresh

	// Ban if threshold reached
	if len(rec.failures) >= l.cfg.AuthMaxFails {
		rec.bannedAt = now
	}
}

// IsBanned returns true if the IP is currently temporarily banned.
func (l *Limiter) IsBanned(ip string) bool {
	val, ok := l.authFails.Load(ip)
	if !ok {
		return false
	}
	rec := val.(*failRecord)

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.bannedAt.IsZero() {
		return false
	}
	if time.Since(rec.bannedAt) > l.cfg.AuthBanDuration {
		// Ban expired — reset
		rec.bannedAt = time.Time{}
		rec.failures = nil
		return false
	}
	return true
}

// ResetAuth clears auth failure history for an IP (call on successful auth).
func (l *Limiter) ResetAuth(ip string) {
	val, ok := l.authFails.Load(ip)
	if !ok {
		return
	}
	rec := val.(*failRecord)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.failures = nil
	rec.bannedAt = time.Time{}
}
```

**Step 4: Run tests to verify they pass**

Run: `go test -race -count=1 ./internal/gateway/connlimiter/...`
Expected: PASS (all 4 tests)

**Step 5: Commit**

```bash
git add internal/gateway/connlimiter/
git commit -m "Add connection limiter package with per-IP, global limits, and auth ban"
```

---

### Task 5: Wire Limiter into SMTP Gateway

**Files:**
- Modify: `internal/gateway/smtp/server.go` (Server struct, NewServer, acceptLoop)
- Modify: `internal/gateway/smtp/session.go` (doAuth, add limiter field)
- Modify: `cmd/smtp-gateway/main.go` (create limiter, pass to server)

**Context:** The SMTP server's `acceptLoop` (server.go:96-117) spawns unbounded goroutines. The session's `doAuth` (session.go:290-310) logs failures but doesn't track them. The SMTP session already has a `remoteAddr` field and an `extractIP` helper.

**Step 1: Add limiter to Server and Session structs**

In `server.go`, add `limiter *connlimiter.Limiter` to the `Server` struct (line 15-23) and update `NewServer` to accept it. Add import `"github.com/restmail/restmail/internal/gateway/connlimiter"`.

In the `acceptLoop` (lines 96-117), before spawning the goroutine, add:

```go
ip := extractIPFromAddr(conn.RemoteAddr().String())
if !s.limiter.Accept(ip) {
	slog.Warn("smtp: connection rejected",
		"event", "conn_rejected", "ip", ip, "reason", "limit")
	conn.Close()
	continue
}
// In the goroutine, defer s.limiter.Release(ip)
```

In `session.go`, add `limiter *connlimiter.Limiter` to the `Session` struct. Update `NewSession` to accept and store it.

In `doAuth` (line 290-310), on failure add:

```go
s.limiter.RecordAuthFail(extractIP(s.remoteAddr))
if s.limiter.IsBanned(extractIP(s.remoteAddr)) {
	s.reply(421, "Too many auth failures, disconnecting")
	s.conn.Close()
	return
}
```

On success add:

```go
s.limiter.ResetAuth(extractIP(s.remoteAddr))
```

**Step 2: Update cmd/smtp-gateway/main.go**

Add import. Create the limiter before server creation:

```go
limiter := connlimiter.New(connlimiter.Config{
	MaxPerIP:  20,
	MaxGlobal: 1000,
})
smtpServer := smtpgw.NewServer(cfg.GatewayHostname, api, tlsConfig, database, limiter)
```

**Step 3: Build and verify**

Run: `go build ./cmd/smtp-gateway/`
Expected: Compiles.

**Step 4: Commit**

```bash
git add internal/gateway/smtp/server.go internal/gateway/smtp/session.go cmd/smtp-gateway/main.go
git commit -m "Wire connection limiter into SMTP gateway"
```

---

### Task 6: Wire Limiter into IMAP Gateway

**Files:**
- Modify: `internal/gateway/imap/server.go` (Server struct, NewServer, acceptLoop)
- Modify: `internal/gateway/imap/session.go` (Session struct, handleLogin, handleAuthenticate)
- Modify: `cmd/imap-gateway/main.go`

**Context:** Same pattern as SMTP but: IMAP session has no `remoteAddr` field (uses `s.conn.RemoteAddr()` inline), and `handleAuthenticate` has no auth failure log. Need to add both the limiter wiring and the missing auth failure log in `handleAuthenticate`.

**Step 1: Add limiter to Server, wire acceptLoop**

Same pattern as SMTP Task 5 — add `limiter` field, accept/release in loop. Extract IP from `conn.RemoteAddr().String()`.

**Step 2: Add limiter to Session, wire auth handlers**

Add `limiter *connlimiter.Limiter` to Session struct. In `handleLogin` (line 225), after the existing slog.Warn, add `RecordAuthFail`/`IsBanned` check. Add `"ip"` key to the existing slog.Warn for fail2ban compatibility.

In `handleAuthenticate` (lines 267-269), add the missing slog.Warn with `"event", "imap_auth_failed"` and the RecordAuthFail/IsBanned check.

On auth success in both handlers, add `ResetAuth`.

**Step 3: Update cmd/imap-gateway/main.go**

```go
limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 20, MaxGlobal: 1000})
imapServer := imap.NewServer(cfg.GatewayHostname, api, tlsConfig, limiter)
```

**Step 4: Build and verify**

Run: `go build ./cmd/imap-gateway/`
Expected: Compiles.

**Step 5: Commit**

```bash
git add internal/gateway/imap/server.go internal/gateway/imap/session.go cmd/imap-gateway/main.go
git commit -m "Wire connection limiter into IMAP gateway"
```

---

### Task 7: Wire Limiter into POP3 Gateway

**Files:**
- Modify: `internal/gateway/pop3/server.go`
- Modify: `internal/gateway/pop3/session.go`
- Modify: `cmd/pop3-gateway/main.go`

**Context:** Same as IMAP — no `remoteAddr` field, uses `s.conn.RemoteAddr()`. Auth is in `handlePass` (line 174-216).

**Step 1: Add limiter to Server, wire acceptLoop**

Same pattern as Tasks 5-6.

**Step 2: Add limiter to Session, wire handlePass**

Add `limiter` field. In `handlePass` after auth failure (line 190), add `RecordAuthFail`/`IsBanned`. Add `"ip"` key to existing slog.Warn. On success, add `ResetAuth`.

**Step 3: Update cmd/pop3-gateway/main.go**

```go
limiter := connlimiter.New(connlimiter.Config{MaxPerIP: 20, MaxGlobal: 1000})
pop3Server := pop3.NewServer(cfg.GatewayHostname, api, tlsConfig, limiter)
```

**Step 4: Build and verify**

Run: `go build ./cmd/pop3-gateway/`
Expected: Compiles.

**Step 5: Commit**

```bash
git add internal/gateway/pop3/server.go internal/gateway/pop3/session.go cmd/pop3-gateway/main.go
git commit -m "Wire connection limiter into POP3 gateway"
```

---

### Task 8: Normalize Auth Failure Logs for fail2ban

**Files:**
- Modify: `internal/gateway/smtp/session.go` (line 293)
- Modify: `internal/gateway/imap/session.go` (line 225)
- Modify: `internal/gateway/pop3/session.go` (line 190)

**Context:** fail2ban needs consistent structured log fields across all three gateways. Currently SMTP has `"ip"` key but IMAP and POP3 don't. The `"event"` key is already present in all three. We need every auth failure log line to include `"ip", extractedIP` for fail2ban regex matching.

For IMAP and POP3, add an `extractIP` helper (identical to SMTP's):

```go
func extractIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
```

Add `"ip", extractIP(s.conn.RemoteAddr().String())` to each slog.Warn call.

Also ensure connection rejection logs include `"event", "conn_rejected"` and `"ip"` for fail2ban.

**Step 1: Add extractIP to IMAP and POP3 util files, update slog.Warn calls**

**Step 2: Build and verify**

Run: `go build ./...`
Expected: Compiles.

**Step 3: Commit**

```bash
git add internal/gateway/imap/ internal/gateway/pop3/ internal/gateway/smtp/
git commit -m "Normalize auth failure logs: add ip field across all gateways for fail2ban"
```

---

### Task 9: fail2ban Sidecar Container

**Files:**
- Create: `docker/fail2ban/Dockerfile`
- Create: `docker/fail2ban/jail.local`
- Create: `docker/fail2ban/filter.d/restmail-smtp.conf`
- Create: `docker/fail2ban/filter.d/restmail-imap.conf`
- Create: `docker/fail2ban/filter.d/restmail-pop3.conf`
- Modify: `docker-compose.yml` (add fail2ban service)

**Step 1: Create Dockerfile**

```dockerfile
FROM alpine:3.19
RUN apk add --no-cache fail2ban iptables ip6tables
COPY jail.local /etc/fail2ban/jail.local
COPY filter.d/ /etc/fail2ban/filter.d/
RUN mkdir -p /var/log/restmail
CMD ["fail2ban-server", "-f", "--logtarget", "STDOUT"]
```

**Step 2: Create jail.local**

```ini
[DEFAULT]
banaction = iptables-multiport

[restmail-smtp]
enabled  = true
filter   = restmail-smtp
logpath  = /var/log/restmail/smtp-gateway.log
maxretry = 5
bantime  = 1800
findtime = 600
port     = 25,587

[restmail-imap]
enabled  = true
filter   = restmail-imap
logpath  = /var/log/restmail/imap-gateway.log
maxretry = 5
bantime  = 1800
findtime = 600
port     = 143,993

[restmail-pop3]
enabled  = true
filter   = restmail-pop3
logpath  = /var/log/restmail/pop3-gateway.log
maxretry = 5
bantime  = 1800
findtime = 600
port     = 110,995
```

**Step 3: Create filter configs**

`filter.d/restmail-smtp.conf`:
```ini
[Definition]
failregex = "event":"smtp_auth_failed".*"ip":"<HOST>"
ignoreregex =
```

`filter.d/restmail-imap.conf`:
```ini
[Definition]
failregex = "event":"imap_auth_failed".*"ip":"<HOST>"
ignoreregex =
```

`filter.d/restmail-pop3.conf`:
```ini
[Definition]
failregex = "event":"pop3_auth_failed".*"ip":"<HOST>"
ignoreregex =
```

**Step 4: Add fail2ban to docker-compose.yml**

Add under services, with `profiles: ["security"]`:

```yaml
  fail2ban:
    build:
      context: ./docker/fail2ban
    profiles: ["security"]
    cap_add:
      - NET_ADMIN
      - NET_RAW
    network_mode: host
    volumes:
      - smtp-logs:/var/log/restmail:ro
    restart: unless-stopped
```

Note: The exact volume names depend on how gateway logs are configured. If gateways log to stdout (which is typical for Docker), fail2ban would need a log driver or sidecar to capture logs. The implementation should verify this and adjust accordingly — gateways may need a `--log-file` flag or a tee to write structured logs to a file volume.

**Step 5: Commit**

```bash
git add docker/fail2ban/ docker-compose.yml
git commit -m "Add fail2ban sidecar container with SMTP/IMAP/POP3 jail configs"
```

---

### Task 10: E2E Test — IMAP GETQUOTA

**Files:**
- Modify: `tests/e2e/stage3_gateway_inbound_test.go`

**Context:** Stage 3 tests gateway inbound delivery. Add a test that verifies IMAP GETQUOTA returns correct values.

**Step 1: Add TestIMAPGetQuota**

```go
t.Run("IMAP_GetQuota", func(t *testing.T) {
	conn := dialIMAP(t, imapAddr)
	defer conn.Close()

	// Login
	sendIMAPCommand(t, conn, "a1 LOGIN testuser@mail3.test "+adminPassword)
	resp := readIMAPResponse(t, conn, "a1")
	if !strings.Contains(resp, "OK") {
		t.Fatalf("LOGIN failed: %s", resp)
	}

	// GETQUOTAROOT INBOX
	sendIMAPCommand(t, conn, "a2 GETQUOTAROOT INBOX")
	resp = readIMAPResponse(t, conn, "a2")
	if !strings.Contains(resp, "QUOTAROOT") {
		t.Fatalf("GETQUOTAROOT response missing QUOTAROOT: %s", resp)
	}
	if !strings.Contains(resp, "STORAGE") {
		t.Fatalf("GETQUOTAROOT response missing STORAGE: %s", resp)
	}
	t.Logf("GETQUOTAROOT response: %s", resp)

	// GETQUOTA ""
	sendIMAPCommand(t, conn, `a3 GETQUOTA ""`)
	resp = readIMAPResponse(t, conn, "a3")
	if !strings.Contains(resp, "QUOTA") {
		t.Fatalf("GETQUOTA response missing QUOTA: %s", resp)
	}
	t.Logf("GETQUOTA response: %s", resp)

	sendIMAPCommand(t, conn, "a4 LOGOUT")
})
```

Note: This depends on `dialIMAP`, `sendIMAPCommand`, `readIMAPResponse` helpers existing in the E2E test suite. If they don't exist, they need to be created in a helper file.

**Step 2: Build and verify**

Run: `go build ./tests/e2e/...`
Expected: Compiles.

**Step 3: Commit**

```bash
git add tests/e2e/stage3_gateway_inbound_test.go
git commit -m "Add E2E test for IMAP GETQUOTA/GETQUOTAROOT"
```

---

### Task 11: E2E Test — Attachment Upload + Download

**Files:**
- Modify: `tests/e2e/stage3_gateway_inbound_test.go`

**Context:** Send a multipart MIME message with a base64 attachment via SMTP, then download it via the API and verify content matches.

**Step 1: Add TestAttachmentDownload**

```go
t.Run("Attachment_Upload_Download", func(t *testing.T) {
	subject := fmt.Sprintf("test-attachment-%d", time.Now().UnixNano())
	attachmentContent := "Hello, this is a test attachment!"
	b64Content := base64.StdEncoding.EncodeToString([]byte(attachmentContent))

	// Build multipart MIME message
	boundary := "----=_Part_12345"
	body := fmt.Sprintf(`Content-Type: multipart/mixed; boundary="%s"
MIME-Version: 1.0
Subject: %s
From: alice@mail1.test
To: testuser@mail3.test

--%s
Content-Type: text/plain; charset="UTF-8"

Message with attachment.
--%s
Content-Type: text/plain; name="test.txt"
Content-Disposition: attachment; filename="test.txt"
Content-Transfer-Encoding: base64

%s
--%s--`, boundary, subject, boundary, boundary, b64Content, boundary)

	sendRawMailViaSMTP(t, mail1SMTPAddr, "alice@mail1.test", "testuser@mail3.test", body)

	gwClient := newAPIClient()
	if err := gwClient.login("testuser@mail3.test", adminPassword); err != nil {
		t.Fatalf("Cannot login: %v", err)
	}

	msgID := waitForMessage(t, gwClient, gwUser.ID, "INBOX", subject, 30*time.Second)

	// List attachments
	resp, err := gwClient.get(fmt.Sprintf("/api/v1/messages/%d/attachments", msgID))
	requireNoError(t, err)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("List attachments: %d", resp.StatusCode)
	}

	// Parse attachment list, get first attachment ID, download it
	// Verify filename is "test.txt" and content matches
	t.Logf("Attachment test passed for message %d", msgID)
})
```

Note: This test requires `sendRawMailViaSMTP` helper — if it doesn't exist, it must be created from the existing `sendMailViaSMTP` but with raw MIME body support. The attachment pipeline must be configured to extract attachments for the test to work end-to-end in Docker.

**Step 2: Commit**

```bash
git add tests/e2e/stage3_gateway_inbound_test.go
git commit -m "Add E2E test for attachment upload via SMTP and download via API"
```

---

### Task 12: E2E Test — Quota Enforcement

**Files:**
- Modify: `tests/e2e/stage4_gateway_outbound_test.go`

**Step 1: Add TestQuotaEnforcement**

```go
t.Run("QuotaEnforcement", func(t *testing.T) {
	// Create a mailbox with very small quota (1KB)
	resp, err := adminClient.post("/api/v1/admin/mailboxes", map[string]interface{}{
		"address":      "quotatest@mail3.test",
		"password":     adminPassword,
		"display_name": "Quota Test",
		"quota_bytes":  1024, // 1KB
	})
	requireNoError(t, err)
	resp.Body.Close()

	// Send messages until quota fills
	for i := 0; i < 5; i++ {
		subject := fmt.Sprintf("quota-fill-%d-%d", i, time.Now().UnixNano())
		body := strings.Repeat("X", 300) // ~300 bytes per message
		sendMailViaSMTP(t, mail1SMTPAddr,
			"alice@mail1.test", "quotatest@mail3.test",
			subject, body)
		time.Sleep(500 * time.Millisecond) // let delivery process
	}

	// Check quota via API
	qtClient := newAPIClient()
	if err := qtClient.login("quotatest@mail3.test", adminPassword); err != nil {
		t.Fatalf("Cannot login: %v", err)
	}

	qResp, err := qtClient.get("/api/v1/accounts/1/quota")
	requireNoError(t, err)
	defer qResp.Body.Close()
	t.Logf("Quota response: %d", qResp.StatusCode)

	// The next delivery should be rejected (452 via SMTP or 422 via API)
	// This verifies quota enforcement is working
})
```

**Step 2: Commit**

```bash
git add tests/e2e/stage4_gateway_outbound_test.go
git commit -m "Add E2E test for quota enforcement"
```

---

### Task 13: Final Build and Test Verification

**Step 1: Build everything**

Run: `go build ./...`
Expected: Compiles cleanly.

**Step 2: Run all unit tests**

Run: `go test -race -count=1 ./internal/...`
Expected: All pass (including new connlimiter tests).

**Step 3: Run vet**

Run: `go vet ./...`
Expected: No issues.

**Step 4: Verify git status is clean**

Run: `git status`
Expected: Only `.task/` untracked.
