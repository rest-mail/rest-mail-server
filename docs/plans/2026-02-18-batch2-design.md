# Batch 2 Design: Attachments, Quota, Connection Limits, E2E Tests

**Date:** 2026-02-18
**Scope:** Fix attachment security + dedup, quota accounting, connection limits + fail2ban, E2E test expansion

## Context

Exploration revealed that attachment download streaming and IMAP GETQUOTA/GETQUOTAROOT are already implemented. The real gaps are: a security bug in attachment ownership, quota accounting holes, zero connection-level protection in gateways, and missing E2E test coverage.

---

## 1. Attachment Security + Dedup

### 1a. Ownership check on GetAttachment

**File:** `internal/api/handlers/attachments.go`

JOIN `attachments` → `messages` → `mailboxes` → verify the authenticated user's account ID owns the mailbox. Return 404 (not 403) if ownership fails, to avoid leaking attachment existence.

```sql
SELECT a.* FROM attachments a
JOIN messages m ON a.message_id = m.id
JOIN mailboxes mb ON m.mailbox_id = mb.id
WHERE a.id = ? AND mb.account_id = ?
```

Same ownership check on `ListAttachments` — verify the message belongs to the user's mailbox.

### 1b. Checksum-based dedup

**File:** `internal/pipeline/filters/extract_attachments.go`

Before writing a file to disk, query `SELECT storage_ref FROM attachments WHERE checksum = ? AND storage_type = ? LIMIT 1`. If found, reuse that `storage_ref` and skip the file write. The DB row still gets a new `Attachment` record pointing to the same file.

Add index: `idx_attachments_checksum` on `attachments(checksum)` via GORM tag.

**File:** `internal/db/models/pipeline.go`

Update `Checksum` field tag to `gorm:"size:64;index:idx_attachments_checksum"`.

### 1c. Include attachment bytes in quota

**File:** `internal/api/handlers/messages.go`

In `DeliverMessage`, after persisting attachments, add their total `SizeBytes` to `quota_used_bytes`:
```go
h.db.Model(&models.Mailbox{}).Where("id = ?", mailbox.ID).
    Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", totalAttachmentBytes))
```

---

## 2. Quota Accounting Fixes

**File:** `internal/api/handlers/messages.go`

### 2a. Decrement quota on message delete

In `DeleteMessage`, subtract `msg.SizeBytes` from `mailbox.quota_used_bytes`. If the message `has_attachments`, also subtract the sum of attachment `SizeBytes`.

### 2b. Quota check in API delivery path

In `DeliverMessage`, before creating the message, check `mailbox.QuotaUsedBytes + sizeBytes > mailbox.QuotaBytes`. If over quota, return `422` with `"mailbox_full"` error. This catches API-direct delivery that bypasses the SMTP pipeline's `recipient_check` filter.

### 2c. Quota check in SendMessage

In `SendMessage`, check sender's mailbox quota before storing the sent copy. If over quota, still deliver to recipients but skip storing in Sent folder and warn in response.

---

## 3. Connection Limits — `internal/gateway/connlimiter/`

**New package** with a single `Limiter` struct:

```go
type Config struct {
    MaxPerIP        int           // default 20
    MaxGlobal       int           // default 1000
    AuthMaxFails    int           // default 5
    AuthBanWindow   time.Duration // default 10m
    AuthBanDuration time.Duration // default 30m
}

type Limiter struct {
    cfg       Config
    global    atomic.Int32
    perIP     sync.Map // IP → *atomic.Int32
    authFails sync.Map // IP → *failRecord
}

func (l *Limiter) Accept(ip string) bool     // check per-IP + global, increment counters
func (l *Limiter) Release(ip string)          // decrement counters on disconnect
func (l *Limiter) RecordAuthFail(ip string)   // increment fail counter for IP
func (l *Limiter) IsBanned(ip string) bool    // check if IP is temporarily banned
func (l *Limiter) ResetAuth(ip string)        // clear fail counter on successful auth
```

### Wiring into gateways

Each gateway's `acceptLoop`:
```go
if !limiter.Accept(ip) {
    conn.Close()
    slog.Warn("conn rejected", "event", "conn_rejected", "ip", ip, "reason", "limit")
    continue
}
defer limiter.Release(ip)
```

Each gateway's auth handler, after failed auth:
```go
limiter.RecordAuthFail(ip)
if limiter.IsBanned(ip) {
    // close connection with protocol-appropriate error
}
```

On successful auth: `limiter.ResetAuth(ip)`.

**Files modified:**
- `internal/gateway/smtp/server.go` — wire limiter into acceptLoop
- `internal/gateway/smtp/session.go` — wire auth fail/ban checks
- `internal/gateway/imap/server.go` + `session.go` — wire limiter
- `internal/gateway/pop3/server.go` + `session.go` — wire limiter
- `cmd/{smtp,imap,pop3}-gateway/main.go` — create limiter from env config, pass to server

---

## 4. fail2ban Sidecar

### `docker/fail2ban/jail.local`
```ini
[restmail-smtp]
enabled  = true
filter   = restmail-smtp
logpath  = /var/log/restmail/smtp-gateway.log
maxretry = 5
bantime  = 1800
findtime = 600

[restmail-imap]
enabled  = true
filter   = restmail-imap
logpath  = /var/log/restmail/imap-gateway.log
maxretry = 5
bantime  = 1800
findtime = 600

[restmail-pop3]
enabled  = true
filter   = restmail-pop3
logpath  = /var/log/restmail/pop3-gateway.log
maxretry = 5
bantime  = 1800
findtime = 600
```

### `docker/fail2ban/filter.d/restmail-smtp.conf`
```ini
[Definition]
failregex = "event":"smtp_auth_failed".*"ip":"<HOST>"
```

Similar filters for IMAP and POP3 using their respective `event` field values.

### `docker/fail2ban/Dockerfile`
Alpine + fail2ban package.

### `docker-compose.yml`
Add `fail2ban` service under `profiles: [security]`, mounting gateway log volumes read-only and jail/filter configs.

---

## 5. E2E Test Expansion

### Stage 3 additions (`tests/e2e/stage3_gateway_inbound_test.go`)
- `TestIMAPGetQuota`: LOGIN → GETQUOTA "" → parse STORAGE response, verify values match API quota endpoint
- `TestAttachmentDownload`: Send multipart MIME with base64 attachment via SMTP → wait for delivery → list attachments via API → GET attachment by ID → verify content and content-type match original

### Stage 4 additions (`tests/e2e/stage4_gateway_outbound_test.go`)
- `TestQuotaEnforcement`: Create mailbox with 1KB quota → send messages until quota fills → verify next SMTP delivery gets 452 rejection and API delivery gets 422

---

## File Change Summary

| File | Changes |
|------|---------|
| `internal/api/handlers/attachments.go` | Ownership JOIN on GetAttachment + ListAttachments |
| `internal/api/handlers/messages.go` | Quota decrement on delete, quota check in DeliverMessage + SendMessage, attachment bytes in quota |
| `internal/pipeline/filters/extract_attachments.go` | Checksum dedup lookup before write |
| `internal/db/models/pipeline.go` | Add `idx_attachments_checksum` index tag |
| `internal/gateway/connlimiter/limiter.go` | New — Limiter struct |
| `internal/gateway/connlimiter/limiter_test.go` | New — unit tests |
| `internal/gateway/smtp/server.go` | Wire limiter into acceptLoop |
| `internal/gateway/smtp/session.go` | Wire auth fail/ban checks |
| `internal/gateway/imap/server.go` + `session.go` | Wire limiter |
| `internal/gateway/pop3/server.go` + `session.go` | Wire limiter |
| `cmd/{smtp,imap,pop3}-gateway/main.go` | Create limiter from env config, pass to server |
| `docker/fail2ban/Dockerfile` | New — Alpine + fail2ban |
| `docker/fail2ban/jail.local` | New — 3 jails |
| `docker/fail2ban/filter.d/*.conf` | New — 3 filter configs |
| `docker-compose.yml` | fail2ban service (security profile) |
| `tests/e2e/stage3_gateway_inbound_test.go` | IMAP GETQUOTA + attachment download tests |
| `tests/e2e/stage4_gateway_outbound_test.go` | Quota enforcement test |

## Verification

1. `go build ./...` — compiles
2. `go test -race -count=1 ./internal/...` — all tests pass (including new connlimiter tests)
3. `go vet ./...` — no issues
4. Manual inspection of ownership checks, quota math, limiter wiring
