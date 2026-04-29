# Critical Items Gap Fixes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix all remaining gaps across 9 critical features so they are fully functional.

**Architecture:** Each task is a focused change to one or two files. Tasks are ordered so that shared types (SMTPError) are defined before consumers. Most changes are in existing files — no new packages.

**Tech Stack:** Go 1.24+, GORM/PostgreSQL, chi router, Node.js 22 (sidecar), fsnotify

---

### Task 1: SMTPError type + parse SMTP codes in deliverToHost

**Files:**
- Modify: `internal/gateway/queue/worker.go:1-20` (add SMTPError type), `:394-457` (modify deliverToHost)

**Step 1: Define SMTPError type**

Add at top of `worker.go` after imports:

```go
// SMTPError represents a structured SMTP error with response code.
type SMTPError struct {
	Code     int    // 3-digit SMTP code (e.g. 550)
	Enhanced string // Enhanced status code (e.g. "5.1.1")
	Message  string // Human-readable message
}

func (e *SMTPError) Error() string {
	if e.Enhanced != "" {
		return fmt.Sprintf("SMTP %d %s: %s", e.Code, e.Enhanced, e.Message)
	}
	return fmt.Sprintf("SMTP %d: %s", e.Code, e.Message)
}

func (e *SMTPError) IsPermanent() bool {
	return e.Code >= 500 && e.Code < 600
}

// parseSMTPError extracts SMTP status code from a net/smtp error string.
// net/smtp errors look like: "550 5.1.1 User unknown" or just plain errors.
func parseSMTPError(err error, host string) *SMTPError {
	msg := err.Error()
	// Try to extract "NNN X.X.X ..." from the error string
	// net/smtp wraps the server response as the error message
	for _, prefix := range []string{
		fmt.Sprintf("MAIL FROM to %s: ", host),
		fmt.Sprintf("RCPT TO %s at %s: ", "", host), // variable, just try suffix
		fmt.Sprintf("DATA to %s: ", host),
		fmt.Sprintf("end DATA to %s: ", host),
	} {
		if idx := strings.Index(msg, prefix); idx >= 0 {
			msg = msg[idx+len(prefix):]
			break
		}
	}
	// Try to parse "NNN X.X.X rest" or "NNN rest"
	if len(msg) >= 3 && msg[0] >= '1' && msg[0] <= '5' {
		code := 0
		fmt.Sscanf(msg[:3], "%d", &code)
		if code >= 100 && code <= 599 {
			rest := strings.TrimSpace(msg[3:])
			enhanced := ""
			if len(rest) > 0 && rest[0] >= '1' && rest[0] <= '5' {
				// Try enhanced code like "5.1.1"
				parts := strings.SplitN(rest, " ", 2)
				if len(parts[0]) >= 5 && strings.Count(parts[0], ".") == 2 {
					enhanced = parts[0]
					if len(parts) > 1 {
						rest = parts[1]
					} else {
						rest = ""
					}
				}
			}
			return &SMTPError{Code: code, Enhanced: enhanced, Message: rest}
		}
	}
	return &SMTPError{Code: 0, Message: msg}
}
```

**Step 2: Modify deliverToHost to return \*SMTPError**

Change the signature of `deliverToHost` from returning `error` to returning `error` but wrapping SMTP-specific errors:

In `deliverToHost`, wrap each `client.Mail`, `client.Rcpt`, `wc.Close()` error with `parseSMTPError`:

```go
// Set sender
if err := client.Mail(item.Sender); err != nil {
	return parseSMTPError(err, host)
}

// Set recipient
if err := client.Rcpt(item.Recipient); err != nil {
	return parseSMTPError(err, host)
}

// Send data
wc, err := client.Data()
if err != nil {
	return parseSMTPError(err, host)
}
_, err = fmt.Fprint(wc, item.RawMessage)
if err != nil {
	wc.Close()
	return fmt.Errorf("write message to %s: %w", host, err)
}
if err := wc.Close(); err != nil {
	return parseSMTPError(err, host)
}
```

**Step 3: Build and verify**

Run: `go build ./internal/gateway/...`
Expected: compiles

**Step 4: Commit**

```bash
git add internal/gateway/queue/worker.go
git commit -m "Add SMTPError type and parse SMTP codes in deliverToHost"
```

---

### Task 2: Immediate bounce on 5xx + populate LastErrorCode

**Files:**
- Modify: `internal/gateway/queue/worker.go:85-168` (processOne)

**Step 1: Update processOne to check for permanent failures**

After `deliveryErr := w.deliver(item)` and the nil check, add 5xx detection before the retry logic:

```go
if deliveryErr == nil {
	// Success
	w.db.Model(&item).Updates(map[string]interface{}{
		"status": "delivered",
	})
	slog.Info("queue: delivered", "id", item.ID, "recipient", item.Recipient)
	return
}

// Extract SMTP error code if available
var smtpErr *SMTPError
var errorCode int
if se, ok := deliveryErr.(*SMTPError); ok {
	smtpErr = se
	errorCode = se.Code
}

slog.Warn("queue: delivery failed",
	"id", item.ID,
	"recipient", item.Recipient,
	"attempt", item.Attempts+1,
	"error", deliveryErr,
	"smtp_code", errorCode,
)

// Permanent failure (5xx) — bounce immediately, don't retry
if smtpErr != nil && smtpErr.IsPermanent() {
	w.db.Model(&item).Updates(map[string]interface{}{
		"status":          "bounced",
		"last_error":      deliveryErr.Error(),
		"last_error_code": errorCode,
	})
	slog.Warn("queue: permanent failure, bouncing", "id", item.ID, "smtp_code", errorCode)
	w.generateBounce(item, smtpErr)
	return
}

// Check if we should retry or bounce (max retries exhausted)
if item.Attempts+1 >= item.MaxRetries {
	w.db.Model(&item).Updates(map[string]interface{}{
		"status":          "bounced",
		"last_error":      deliveryErr.Error(),
		"last_error_code": errorCode,
	})
	slog.Warn("queue: message bounced (max retries)", "id", item.ID, "recipient", item.Recipient)
	w.generateBounce(item, &SMTPError{Code: 0, Message: deliveryErr.Error()})
	return
}

// Exponential backoff: 1min, 2min, 4min, 8min, ... up to 4 hours
backoff := time.Duration(math.Pow(2, float64(item.Attempts))) * time.Minute
if backoff > 4*time.Hour {
	backoff = 4 * time.Hour
}

w.db.Model(&item).Updates(map[string]interface{}{
	"status":          "deferred",
	"next_attempt":    time.Now().Add(backoff),
	"last_error":      deliveryErr.Error(),
	"last_error_code": errorCode,
})
```

Note: `generateBounce` signature changes to accept `*SMTPError` — we'll update it in Task 3.

**Step 2: Build and verify**

Run: `go build ./internal/gateway/...`
Expected: compile error (generateBounce signature mismatch — fixed in next task)

---

### Task 3: RFC 3464 bounce DSN generation

**Files:**
- Modify: `internal/gateway/queue/worker.go:340-391` (rewrite generateBounce)

**Step 1: Rewrite generateBounce**

Replace the entire `generateBounce` function:

```go
// generateBounce creates an RFC 3464 DSN (Delivery Status Notification)
// and delivers it to the original sender's mailbox if the sender is local.
func (w *Worker) generateBounce(item models.OutboundQueue, smtpErr *SMTPError) {
	// Check if the sender has a local mailbox
	var senderMailbox struct {
		ID     uint
		Active bool
	}
	result := w.db.Raw("SELECT id, active FROM mailboxes WHERE address = ? AND active = true LIMIT 1", item.Sender).Scan(&senderMailbox)
	if result.Error != nil || result.RowsAffected == 0 {
		slog.Debug("queue: bounce sender not local, discarding DSN", "sender", item.Sender)
		return
	}

	now := time.Now()
	boundary := fmt.Sprintf("=_restmail_dsn_%d", now.UnixNano())
	msgID := fmt.Sprintf("<dsn-%d-%d@%s>", item.ID, now.UnixNano(), w.hostname)

	statusCode := "5.0.0"
	diagnosticCode := "smtp; delivery failed"
	if smtpErr != nil {
		if smtpErr.Enhanced != "" {
			statusCode = smtpErr.Enhanced
		} else if smtpErr.Code >= 500 {
			statusCode = fmt.Sprintf("%d.0.0", smtpErr.Code/100)
		}
		if smtpErr.Code > 0 {
			diagnosticCode = fmt.Sprintf("smtp; %d %s", smtpErr.Code, smtpErr.Message)
		} else {
			diagnosticCode = fmt.Sprintf("smtp; %s", smtpErr.Message)
		}
	}

	// Extract original headers from RawMessage for Part 3
	originalHeaders := item.RawMessage
	if idx := strings.Index(originalHeaders, "\r\n\r\n"); idx >= 0 {
		originalHeaders = originalHeaders[:idx]
	} else if idx := strings.Index(originalHeaders, "\n\n"); idx >= 0 {
		originalHeaders = originalHeaders[:idx]
	}

	// Part 1: Human-readable
	humanPart := fmt.Sprintf(
		"This is the mail delivery system at %s.\r\n\r\n"+
			"Your message could not be delivered to the following recipient:\r\n\r\n"+
			"    %s\r\n\r\n"+
			"The delivery has been attempted %d time(s).\r\n\r\n"+
			"Error: %s\r\n",
		w.hostname, item.Recipient, item.Attempts, diagnosticCode,
	)

	// Part 2: Machine-readable DSN (RFC 3464)
	dsnPart := fmt.Sprintf(
		"Reporting-MTA: dns; %s\r\n"+
			"Arrival-Date: %s\r\n\r\n"+
			"Final-Recipient: rfc822; %s\r\n"+
			"Action: failed\r\n"+
			"Status: %s\r\n"+
			"Diagnostic-Code: %s\r\n",
		w.hostname,
		item.CreatedAt.Format(time.RFC1123Z),
		item.Recipient,
		statusCode,
		diagnosticCode,
	)

	// Build full multipart/report message
	bounceSubject := fmt.Sprintf("Undelivered Mail Returned to Sender <%s>", item.Recipient)

	var b strings.Builder
	b.WriteString("From: mailer-daemon@" + w.hostname + "\r\n")
	b.WriteString("To: " + item.Sender + "\r\n")
	b.WriteString("Subject: " + bounceSubject + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + msgID + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/report; report-type=delivery-status; boundary=\"" + boundary + "\"\r\n")
	b.WriteString("\r\n")
	// Part 1
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(humanPart + "\r\n")
	// Part 2
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: message/delivery-status\r\n\r\n")
	b.WriteString(dsnPart + "\r\n")
	// Part 3
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/rfc822-headers\r\n\r\n")
	b.WriteString(originalHeaders + "\r\n")
	b.WriteString("--" + boundary + "--\r\n")

	rawBounce := b.String()

	// Insert bounce message into sender's INBOX
	bounceMsg := map[string]interface{}{
		"mailbox_id":    senderMailbox.ID,
		"folder":        "INBOX",
		"sender":        "mailer-daemon@" + w.hostname,
		"sender_name":   "Mail Delivery System",
		"recipients_to": fmt.Sprintf(`["%s"]`, item.Sender),
		"recipients_cc": "[]",
		"subject":       bounceSubject,
		"body_text":     humanPart,
		"raw_message":   rawBounce,
		"is_read":       false,
		"size_bytes":    len(rawBounce),
		"received_at":   now,
		"created_at":    now,
		"updated_at":    now,
	}

	if err := w.db.Table("messages").Create(bounceMsg).Error; err != nil {
		slog.Error("queue: failed to insert bounce DSN", "sender", item.Sender, "error", err)
		return
	}

	slog.Info("queue: RFC 3464 bounce DSN delivered", "sender", item.Sender, "failed_recipient", item.Recipient)
}
```

**Step 2: Build and verify**

Run: `go build ./internal/gateway/...`
Expected: compiles

**Step 3: Commit**

```bash
git add internal/gateway/queue/worker.go
git commit -m "Add RFC 3464 bounce DSN generation and immediate 5xx bounce"
```

---

### Task 4: Atomic capability cache upsert

**Files:**
- Modify: `internal/gateway/queue/worker.go:232-259` (cacheCapability)

**Step 1: Replace cacheCapability with atomic upsert**

Add `"gorm.io/gorm/clause"` to imports, then replace the function:

```go
func (w *Worker) cacheCapability(domain string, supported bool, endpointURL string) {
	now := time.Now()
	ttl := 15 * time.Minute
	if supported {
		ttl = 1 * time.Hour
	}

	cap := models.RESTMAILCapability{
		Domain:      domain,
		Supported:   supported,
		EndpointURL: endpointURL,
		LastProbed:  now,
		ExpiresAt:   now.Add(ttl),
	}

	w.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "domain"}},
		DoUpdates: clause.AssignmentColumns([]string{"supported", "endpoint_url", "last_probed", "expires_at"}),
	}).Create(&cap)
}
```

**Step 2: Build and verify**

Run: `go build ./internal/gateway/...`
Expected: compiles

**Step 3: Commit**

```bash
git add internal/gateway/queue/worker.go
git commit -m "Fix capability cache race with atomic upsert"
```

---

### Task 5: Set ExpiresAt/MaxRetries on SMTP-path queue entries

**Files:**
- Modify: `internal/gateway/smtp/session.go:445-451` (handleDATA queue entry)

**Step 1: Add ExpiresAt and MaxRetries to the OutboundQueue entry**

In `handleDATA()`, find the `queueEntry` creation and add the missing fields:

```go
queueEntry := models.OutboundQueue{
	Sender:     s.mailFrom,
	Recipient:  rcpt,
	Domain:     recipientDomain,
	RawMessage: string(data),
	Status:     "pending",
	MaxRetries: 30,
	ExpiresAt:  time.Now().Add(72 * time.Hour),
}
```

Note: `time` is already imported.

**Step 2: Build and verify**

Run: `go build ./internal/gateway/...`
Expected: compiles

**Step 3: Commit**

```bash
git add internal/gateway/smtp/session.go
git commit -m "Fix SMTP-path outbound queue: set ExpiresAt and MaxRetries"
```

---

### Task 6: SMTP gateway pipeline response code mapping

**Files:**
- Modify: `internal/gateway/smtp/session.go:475-480` (handleDATA delivery error handling)

**Step 1: Map API error status codes to SMTP reply codes**

Replace the error handling block after `s.api.DeliverMessage(deliverReq)`:

```go
_, err = s.api.DeliverMessage(deliverReq)
if err != nil {
	slog.Error("smtp: delivery failed", "from", s.mailFrom, "to", rcpt, "error", err)
	// Map API error codes to SMTP reply codes
	if apiErr, ok := err.(*apiclient.APIError); ok {
		switch {
		case apiErr.StatusCode == 403 || apiErr.StatusCode == 550:
			s.reply(550, "Rejected by policy")
		case apiErr.StatusCode == 503 || apiErr.StatusCode == 451:
			s.reply(451, "Try again later")
		default:
			s.reply(451, "Temporary delivery failure")
		}
	} else {
		s.reply(451, "Temporary delivery failure")
	}
	return
}
```

**Step 2: Build and verify**

Run: `go build ./internal/gateway/...`
Expected: compiles

**Step 3: Commit**

```bash
git add internal/gateway/smtp/session.go
git commit -m "Map API error codes to correct SMTP reply codes"
```

---

### Task 7: Pass missing fields (To/Cc/InReplyTo/References) from SMTP gateway

**Files:**
- Modify: `internal/gateway/smtp/util.go:87-148` (parseRawMessage — add return values)
- Modify: `internal/gateway/smtp/session.go:434,462-473` (use new fields)

**Step 1: Extend parseRawMessage to return additional fields**

Change the signature and add parsing for To, Cc, In-Reply-To, References:

```go
func parseRawMessage(data []byte) (subject, bodyText, bodyHTML, messageID, senderName, inReplyTo, references string, toList, ccList []string) {
```

In the header parsing loop, add:

```go
} else if strings.HasPrefix(lower, "in-reply-to:") {
	inReplyTo = strings.TrimSpace(line[12:])
	inReplyTo = strings.Trim(inReplyTo, "<>")
} else if strings.HasPrefix(lower, "references:") {
	references = strings.TrimSpace(line[11:])
} else if strings.HasPrefix(lower, "to:") {
	toRaw := strings.TrimSpace(line[3:])
	for _, addr := range strings.Split(toRaw, ",") {
		addr = strings.TrimSpace(addr)
		if a := extractEmailFromHeader(addr); a != "" {
			toList = append(toList, a)
		}
	}
} else if strings.HasPrefix(lower, "cc:") {
	ccRaw := strings.TrimSpace(line[3:])
	for _, addr := range strings.Split(ccRaw, ",") {
		addr = strings.TrimSpace(addr)
		if a := extractEmailFromHeader(addr); a != "" {
			ccList = append(ccList, a)
		}
	}
}
```

Add helper function:

```go
// extractEmailFromHeader extracts the email address from a header value like
// "Name <addr>" or bare "addr".
func extractEmailFromHeader(s string) string {
	if idx := strings.Index(s, "<"); idx >= 0 {
		end := strings.Index(s, ">")
		if end > idx {
			return s[idx+1 : end]
		}
	}
	s = strings.TrimSpace(s)
	if strings.Contains(s, "@") {
		return s
	}
	return ""
}
```

**Step 2: Update session.go to use the new return values**

In `handleDATA()`, update the call site:

```go
subject, bodyText, bodyHTML, messageID, senderName, inReplyTo, references, toList, ccList := parseRawMessage(data)
```

And update the `DeliverRequest` construction:

```go
deliverReq := &apiclient.DeliverRequest{
	Address:    rcpt,
	Sender:     s.mailFrom,
	SenderName: senderName,
	Subject:    subject,
	BodyText:   bodyText,
	BodyHTML:   bodyHTML,
	MessageID:  messageID,
	InReplyTo:  inReplyTo,
	References: references,
	RawMessage: string(data),
	ClientIP:   extractIP(s.remoteAddr),
	HeloName:   s.heloName,
}
if len(toList) > 0 {
	toJSON, _ := json.Marshal(toList)
	deliverReq.RecipientsTo = toJSON
}
if len(ccList) > 0 {
	ccJSON, _ := json.Marshal(ccList)
	deliverReq.RecipientsCc = ccJSON
}
```

Note: add `"encoding/json"` to session.go imports if not already present.

**Step 3: Build and verify**

Run: `go build ./internal/gateway/...`
Expected: compiles

**Step 4: Commit**

```bash
git add internal/gateway/smtp/util.go internal/gateway/smtp/session.go
git commit -m "Pass To/Cc/InReplyTo/References from SMTP gateway to API"
```

---

### Task 8: SNI cache invalidation with fsnotify

**Files:**
- Modify: `internal/gateway/tlsutil/sni.go` (add watcher)
- Modify: `cmd/smtp-gateway/main.go` (wire watcher)
- Modify: `cmd/imap-gateway/main.go` (wire watcher)
- Modify: `cmd/pop3-gateway/main.go` (wire watcher)

**Step 1: Add fsnotify dependency**

Run: `go get github.com/fsnotify/fsnotify`

**Step 2: Add watcher to SNICertLoader**

Add to `sni.go`:

```go
import (
	// ... existing imports ...
	"github.com/fsnotify/fsnotify"
)
```

Add fields to the struct:

```go
type SNICertLoader struct {
	certDir     string
	fallback    *tls.Certificate
	mu          sync.RWMutex
	cache       map[string]*tls.Certificate
	watcher     *fsnotify.Watcher
	stopCh      chan struct{}
}
```

Add methods:

```go
// Invalidate removes a domain's cached certificate so it reloads from disk
// on the next TLS handshake.
func (l *SNICertLoader) Invalidate(domain string) {
	l.mu.Lock()
	delete(l.cache, domain)
	l.mu.Unlock()
	slog.Info("sni: invalidated cached cert", "domain", domain)
}

// StartWatching begins monitoring the cert directory for file changes.
// When a .crt or .key file is created or modified, the corresponding
// domain's cached cert is invalidated.
func (l *SNICertLoader) StartWatching() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("sni: create watcher: %w", err)
	}

	if err := watcher.Add(l.certDir); err != nil {
		watcher.Close()
		return fmt.Errorf("sni: watch %s: %w", l.certDir, err)
	}

	l.watcher = watcher
	l.stopCh = make(chan struct{})

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
					continue
				}
				base := filepath.Base(event.Name)
				ext := filepath.Ext(base)
				if ext != ".crt" && ext != ".key" {
					continue
				}
				domain := strings.TrimSuffix(base, ext)
				domain = strings.ToLower(domain)
				l.Invalidate(domain)

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				slog.Error("sni: watcher error", "error", err)

			case <-l.stopCh:
				return
			}
		}
	}()

	slog.Info("sni: watching cert directory", "dir", l.certDir)
	return nil
}

// Stop shuts down the file watcher.
func (l *SNICertLoader) Stop() {
	if l.stopCh != nil {
		close(l.stopCh)
	}
	if l.watcher != nil {
		l.watcher.Close()
	}
}
```

**Step 3: Wire into SMTP gateway main.go**

After `loader := tlsutil.NewSNICertLoader(...)`:

```go
if err := loader.StartWatching(); err != nil {
	slog.Warn("SNI file watcher failed to start", "error", err)
} else {
	defer loader.Stop()
}
```

Apply the same pattern to `cmd/imap-gateway/main.go` and `cmd/pop3-gateway/main.go`.

Note: The IMAP and POP3 gateways currently don't store the `loader` variable. You'll need to capture it:

```go
if cfg.TLSCertDir != "" {
	loader := tlsutil.NewSNICertLoader(cfg.TLSCertDir, &cert)
	tlsConfig.GetCertificate = loader.GetCertificate
	if err := loader.StartWatching(); err != nil {
		slog.Warn("SNI file watcher failed to start", "error", err)
	} else {
		defer loader.Stop()
	}
	slog.Info("TLS configured with SNI", "cert", cfg.TLSCertPath, "cert_dir", cfg.TLSCertDir)
}
```

**Step 4: Build and verify**

Run: `go build ./...`
Expected: compiles

**Step 5: Commit**

```bash
git add go.mod go.sum internal/gateway/tlsutil/sni.go cmd/smtp-gateway/main.go cmd/imap-gateway/main.go cmd/pop3-gateway/main.go
git commit -m "Add fsnotify-based cert hot-reload to SNI loader"
```

---

### Task 9: SSE missing events (SendMessage, SendDraft, folder_update)

**Files:**
- Modify: `internal/api/handlers/messages.go` (multiple locations)

**Step 1: Add SSE event in SendMessage after successful send**

After `respond.Data(w, http.StatusCreated, sentMsg)` at the end of `SendMessage()` (around line 573), add just before the respond call:

```go
// Publish SSE event for sent message
if h.broker != nil {
	h.broker.Publish(senderMailbox.ID, SSEEvent{
		Type: "message_sent",
		Data: map[string]interface{}{
			"message_id": sentMsg.ID,
			"folder":     "Sent",
			"subject":    sentMsg.Subject,
		},
	})
}

respond.Data(w, http.StatusCreated, sentMsg)
```

**Step 2: Add folder_update SSE event in DeliverMessage**

After the existing `new_message` SSE publish in `DeliverMessage()` (around line 790-801), add:

```go
// Also publish folder_update with unread count
var unreadCount int64
h.db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ? AND is_read = ? AND is_deleted = ?",
	mailbox.ID, "INBOX", false, false).Count(&unreadCount)
h.broker.Publish(mailbox.ID, SSEEvent{
	Type: "folder_update",
	Data: map[string]interface{}{
		"folder":       "INBOX",
		"unread_count": unreadCount,
	},
})
```

**Step 3: Add folder_update SSE events in UpdateMessage for folder moves**

In `UpdateMessage()`, after the existing `message_updated` SSE publish (around line 193-204), add folder_update when the folder was changed:

```go
if h.broker != nil {
	h.broker.Publish(msg.MailboxID, SSEEvent{
		Type: "message_updated",
		Data: map[string]interface{}{
			"message_id": msg.ID,
			"folder":     msg.Folder,
			"is_read":    msg.IsRead,
			"is_flagged": msg.IsFlagged,
			"is_starred": msg.IsStarred,
		},
	})

	// Emit folder_update if the message was moved
	if req.Folder != nil {
		h.broker.Publish(msg.MailboxID, SSEEvent{
			Type: "folder_update",
			Data: map[string]interface{}{
				"folder": msg.Folder,
			},
		})
	}
}
```

**Step 4: Build and verify**

Run: `go build ./...`
Expected: compiles

**Step 5: Commit**

```bash
git add internal/api/handlers/messages.go
git commit -m "Add missing SSE events: message_sent, folder_update"
```

---

### Task 10: Draft endpoint gaps (InReplyTo, References)

**Files:**
- Modify: `internal/api/handlers/messages.go:1067-1127` (SaveDraft)
- Modify: `internal/api/handlers/messages.go:1234-1241` (SendDraft sendBody)

**Step 1: Add InReplyTo and References to SaveDraft request struct**

In `SaveDraft`, add to the request struct:

```go
var req struct {
	From       string   `json:"from"`
	To         []string `json:"to"`
	Cc         []string `json:"cc"`
	Subject    string   `json:"subject"`
	BodyText   string   `json:"body_text"`
	BodyHTML   string   `json:"body_html"`
	InReplyTo  string   `json:"in_reply_to"`
	References string   `json:"references"`
}
```

And set them on the draft model:

```go
draft := models.Message{
	MailboxID:    mailboxID,
	Folder:       "Drafts",
	Sender:       req.From,
	RecipientsTo: models.JSONB(toJSON),
	RecipientsCc: models.JSONB(ccJSON),
	Subject:      req.Subject,
	BodyText:     req.BodyText,
	BodyHTML:     req.BodyHTML,
	InReplyTo:    req.InReplyTo,
	References:   req.References,
	IsDraft:      true,
	IsRead:       true,
	SizeBytes:    len(req.Subject) + len(req.BodyText) + len(req.BodyHTML),
	ReceivedAt:   time.Now(),
}
```

**Step 2: Forward InReplyTo in SendDraft**

In `SendDraft`, add `in_reply_to` to the `sendBody` map:

```go
sendBody := map[string]interface{}{
	"from":       draft.Sender,
	"to":         toList,
	"cc":         ccList,
	"subject":    draft.Subject,
	"body_text":  draft.BodyText,
	"body_html":  draft.BodyHTML,
	"in_reply_to": draft.InReplyTo,
}
```

**Step 3: Build and verify**

Run: `go build ./...`
Expected: compiles

**Step 4: Commit**

```bash
git add internal/api/handlers/messages.go
git commit -m "Add InReplyTo and References to draft save and send"
```

---

### Task 11: Thread grouping fixes

**Files:**
- Modify: `internal/api/handlers/messages.go` (SendMessage ~line 460, DeliverMessage ~line 754)

**Step 1: Add thread ID to sent messages in SendMessage**

In `SendMessage()`, before the `sentMsg := models.Message{...}` creation (around line 460), add thread ID computation:

```go
// Compute thread ID for sent message
threadID := messageID
if req.InReplyTo != "" {
	threadID = req.InReplyTo
}
```

And add `ThreadID` to the sentMsg struct:

```go
sentMsg := models.Message{
	MailboxID:    senderMailbox.ID,
	Folder:       "Sent",
	MsgID:        messageID,
	InReplyTo:    req.InReplyTo,
	ThreadID:     threadID,
	Sender:       req.From,
	// ... rest unchanged
}
```

**Step 2: Improve thread root resolution in DeliverMessage**

In `DeliverMessage()`, replace the thread ID logic (around line 754-757):

```go
// Compute thread ID — use first message-id from References chain (thread root),
// fall back to In-Reply-To, fall back to this message's own ID.
threadID := req.MessageID
if req.References != "" {
	// References is space-separated list of message-ids, first is thread root
	refs := strings.Fields(req.References)
	if len(refs) > 0 {
		threadID = strings.Trim(refs[0], "<>")
	}
} else if req.InReplyTo != "" {
	threadID = req.InReplyTo
}
```

**Step 3: Build and verify**

Run: `go build ./...`
Expected: compiles

**Step 4: Commit**

```bash
git add internal/api/handlers/messages.go
git commit -m "Fix thread grouping: sent messages join threads, use References root"
```

---

### Task 12: Attachment DB persistence from pipeline

**Files:**
- Modify: `internal/api/handlers/messages.go` (DeliverMessage, after pipeline ActionContinue)

**Step 1: Persist attachments after pipeline execution**

In `DeliverMessage()`, after the pipeline `ActionContinue` case where `emailJSON = pipelineResult.FinalEmail` (around line 746), and after the message is created (after `h.db.Create(&msg)` around line 777), add:

```go
if err := h.db.Create(&msg).Error; err != nil {
	respond.Error(w, http.StatusInternalServerError, "internal_error", "Failed to deliver message")
	return
}

// Persist attachments from pipeline extraction to DB
if emailJSON != nil {
	var hasAttachments bool
	allAttachments := append(emailJSON.Attachments, emailJSON.Inline...)
	for _, att := range allAttachments {
		if att.Ref == "" {
			continue // Not yet extracted to storage
		}
		dbAtt := models.Attachment{
			MessageID:   msg.ID,
			Filename:    att.Filename,
			ContentType: att.ContentType,
			SizeBytes:   att.Size,
			StorageType: att.Storage,
			StorageRef:  att.Ref,
			Checksum:    att.Checksum,
		}
		if err := h.db.Create(&dbAtt).Error; err != nil {
			slog.Error("deliver: failed to persist attachment", "message_id", msg.ID, "filename", att.Filename, "error", err)
			continue
		}
		hasAttachments = true
	}
	if hasAttachments {
		h.db.Model(&msg).Update("has_attachments", true)
	}
}
```

Note: the `pipeline.Attachment` type has fields `Storage`, `Ref`, `Checksum` that map directly to the `models.Attachment` fields `StorageType`, `StorageRef`, `Checksum`.

**Step 2: Build and verify**

Run: `go build ./...`
Expected: compiles

**Step 3: Run tests**

Run: `go test -race -count=1 ./internal/...`
Expected: all pass

**Step 4: Commit**

```bash
git add internal/api/handlers/messages.go
git commit -m "Persist pipeline-extracted attachments to DB on delivery"
```

---

### Task 13: JS filter — replace vm2 with Node.js built-in vm

**Files:**
- Modify: `projects/js-filter-sidecar/server.js` (rewrite sandbox)
- Modify: `projects/js-filter-sidecar/package.json` (remove vm2)
- Modify: `projects/js-filter-sidecar/Dockerfile` (add memory limit)

**Step 1: Rewrite server.js**

```js
const http = require('http');
const vm = require('vm');

const PORT = process.env.PORT || 3100;

const server = http.createServer((req, res) => {
  if (req.method !== 'POST' || req.url !== '/execute') {
    res.writeHead(404, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: 'Not found' }));
    return;
  }

  let body = '';
  req.on('data', chunk => { body += chunk; });
  req.on('end', () => {
    try {
      const { script, email, timeout_ms } = JSON.parse(body);

      if (!script || !email) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: 'script and email are required' }));
        return;
      }

      const timeout = timeout_ms || 500;

      // Build a restricted sandbox — no require, process, fs, net, etc.
      const logs = [];
      const sandbox = {
        JSON,
        Math,
        Date,
        String,
        Array,
        Object,
        RegExp,
        parseInt,
        parseFloat,
        isNaN,
        isFinite,
        encodeURIComponent,
        decodeURIComponent,
        console: {
          log: (...args) => { logs.push(args.map(String).join(' ')); },
          warn: (...args) => { logs.push(args.map(String).join(' ')); },
          error: (...args) => { logs.push(args.map(String).join(' ')); },
        },
      };

      const context = vm.createContext(sandbox);

      const wrappedScript = `
        ${script}
        if (typeof filter !== 'function') {
          throw new Error('script must define a filter(email) function');
        }
        filter(${JSON.stringify(email)});
      `;

      const result = vm.runInContext(wrappedScript, context, { timeout });

      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ result, logs }));
    } catch (err) {
      if (err.code === 'ERR_SCRIPT_EXECUTION_TIMEOUT') {
        res.writeHead(408, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: 'execution timeout' }));
        return;
      }
      res.writeHead(500, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: err.message || 'script execution failed' }));
    }
  });
});

server.listen(PORT, () => {
  console.log(`js-filter-sidecar listening on port ${PORT}`);
});
```

**Step 2: Update package.json — remove vm2**

```json
{
  "name": "restmail-js-filter",
  "version": "1.0.0",
  "private": true,
  "description": "Sandboxed JavaScript filter execution for restmail pipeline",
  "main": "server.js",
  "dependencies": {}
}
```

**Step 3: Update Dockerfile — add memory limit**

```dockerfile
FROM node:22-alpine

RUN addgroup -S jsfilter && adduser -S jsfilter -G jsfilter

WORKDIR /app

COPY server.js .
COPY package.json .

USER jsfilter

EXPOSE 3100

CMD ["node", "--max-old-space-size=64", "server.js"]
```

Note: no `npm install` needed since there are zero dependencies.

**Step 4: Commit**

```bash
git add projects/js-filter-sidecar/server.js projects/js-filter-sidecar/package.json projects/js-filter-sidecar/Dockerfile
git commit -m "Replace abandoned vm2 with Node.js built-in vm module"
```

---

### Task 14: Final build and test verification

**Step 1: Full build**

Run: `go build ./...`
Expected: compiles with no errors

**Step 2: Full test suite**

Run: `go test -race -count=1 ./internal/...`
Expected: all tests pass

**Step 3: Vet**

Run: `go vet ./...`
Expected: no issues

**Step 4: Final commit (if any straggling changes)**

```bash
git status
```
