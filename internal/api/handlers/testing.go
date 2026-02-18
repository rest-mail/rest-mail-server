package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	"gorm.io/gorm"
)

// TestHandler provides endpoints for testing and debugging the mail system.
// These endpoints are only available in non-production environments.
type TestHandler struct {
	db  *gorm.DB
	cfg *config.Config
}

func NewTestHandler(db *gorm.DB, cfg *config.Config) *TestHandler {
	return &TestHandler{db: db, cfg: cfg}
}

// SendTestEmail sends a test email from one mailbox to another.
// POST /api/v1/admin/test/send
func (h *TestHandler) SendTestEmail(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Environment == "production" {
		respond.Error(w, http.StatusForbidden, "forbidden", "test endpoints disabled in production")
		return
	}

	var req struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid_json", "malformed request body")
		return
	}

	if req.From == "" || req.To == "" {
		respond.ValidationError(w, map[string]string{
			"from": "sender address required",
			"to":   "recipient address required",
		})
		return
	}

	if req.Subject == "" {
		req.Subject = "Test email from RestMail"
	}
	if req.Body == "" {
		req.Body = fmt.Sprintf("This is a test email sent at %s", time.Now().Format(time.RFC3339))
	}

	// Look up sender mailbox
	var senderMb models.Mailbox
	if err := h.db.Where("address = ?", req.From).First(&senderMb).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "sender mailbox not found")
		return
	}

	// Look up recipient mailbox
	var recipientMb models.Mailbox
	if err := h.db.Where("address = ?", req.To).First(&recipientMb).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "recipient mailbox not found")
		return
	}

	messageID := rmail.GenerateMessageID(rmail.DomainFromAddress(req.From))

	// Create the message directly in the recipient's INBOX
	msg := models.Message{
		MailboxID:    recipientMb.ID,
		Folder:       "INBOX",
		MsgID:        messageID,
		ThreadID:     messageID,
		Sender:       req.From,
		SenderName:   senderMb.DisplayName,
		RecipientsTo: models.JSONB(mustJSON([]map[string]string{{"address": req.To, "name": recipientMb.DisplayName}})),
		Subject:      req.Subject,
		BodyText:     req.Body,
		SizeBytes:    len(req.Body),
		ReceivedAt:   time.Now(),
	}

	if err := h.db.Create(&msg).Error; err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal", "failed to create test message")
		return
	}

	// Also save a copy in the sender's Sent folder
	sent := models.Message{
		MailboxID:    senderMb.ID,
		Folder:       "Sent",
		MsgID:        messageID,
		ThreadID:     messageID,
		Sender:       req.From,
		SenderName:   senderMb.DisplayName,
		RecipientsTo: models.JSONB(mustJSON([]map[string]string{{"address": req.To, "name": recipientMb.DisplayName}})),
		Subject:      req.Subject,
		BodyText:     req.Body,
		SizeBytes:    len(req.Body),
		IsRead:       true,
		ReceivedAt:   time.Now(),
	}
	h.db.Create(&sent)

	respond.Data(w, http.StatusCreated, map[string]any{
		"message_id": msg.ID,
		"msg_id":     messageID,
		"from":       req.From,
		"to":         req.To,
		"subject":    req.Subject,
	})
}

// VerifyDelivery checks whether a message matching the given criteria was delivered.
// GET /api/v1/admin/test/verify?to=alice@mail1.test&subject=Test&timeout=5s
func (h *TestHandler) VerifyDelivery(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Environment == "production" {
		respond.Error(w, http.StatusForbidden, "forbidden", "test endpoints disabled in production")
		return
	}

	to := r.URL.Query().Get("to")
	subject := r.URL.Query().Get("subject")
	messageID := r.URL.Query().Get("message_id")
	timeoutStr := r.URL.Query().Get("timeout")

	if to == "" {
		respond.ValidationError(w, map[string]string{"to": "recipient address required"})
		return
	}

	timeout := 5 * time.Second
	if timeoutStr != "" {
		if d, err := time.ParseDuration(timeoutStr); err == nil {
			timeout = d
		}
	}
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}

	var mailbox models.Mailbox
	if err := h.db.Where("address = ?", to).First(&mailbox).Error; err != nil {
		respond.Error(w, http.StatusNotFound, "not_found", "recipient mailbox not found")
		return
	}

	deadline := time.Now().Add(timeout)
	for {
		query := h.db.Where("mailbox_id = ? AND folder = ?", mailbox.ID, "INBOX")
		if subject != "" {
			query = query.Where("subject LIKE ?", "%"+subject+"%")
		}
		if messageID != "" {
			query = query.Where("message_id = ?", messageID)
		}

		var msg models.Message
		if err := query.Order("received_at DESC").First(&msg).Error; err == nil {
			respond.Data(w, http.StatusOK, map[string]any{
				"found":      true,
				"message_id": msg.ID,
				"msg_id":     msg.MsgID,
				"subject":    msg.Subject,
				"sender":     msg.Sender,
				"received_at": msg.ReceivedAt,
			})
			return
		}

		if time.Now().After(deadline) {
			respond.Data(w, http.StatusOK, map[string]any{
				"found":   false,
				"message": "no matching message found within timeout",
			})
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ProbeServices checks connectivity to SMTP, IMAP, POP3, and DNS services.
// POST /api/v1/admin/test/probe
func (h *TestHandler) ProbeServices(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Environment == "production" {
		respond.Error(w, http.StatusForbidden, "forbidden", "test endpoints disabled in production")
		return
	}

	var req struct {
		Services []string `json:"services"` // smtp, imap, pop3, dns, api, db
		Host     string   `json:"host"`
		Timeout  string   `json:"timeout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid_json", "malformed request body")
		return
	}

	if len(req.Services) == 0 {
		req.Services = []string{"smtp", "imap", "pop3", "dns", "db"}
	}
	if req.Host == "" {
		req.Host = "localhost"
	}

	timeout := 5 * time.Second
	if req.Timeout != "" {
		if d, err := time.ParseDuration(req.Timeout); err == nil {
			timeout = d
		}
	}

	results := make(map[string]any)

	for _, svc := range req.Services {
		switch strings.ToLower(svc) {
		case "smtp":
			results["smtp"] = probeTCP(req.Host, h.cfg.SMTPPortInbound, timeout)
		case "smtp_submission":
			results["smtp_submission"] = probeTCP(req.Host, h.cfg.SMTPPortSubmission, timeout)
		case "imap":
			results["imap"] = probeTCP(req.Host, h.cfg.IMAPPort, timeout)
		case "pop3":
			results["pop3"] = probeTCP(req.Host, h.cfg.POP3Port, timeout)
		case "dns":
			results["dns"] = probeDNS("mail1.test", timeout)
		case "db":
			results["db"] = probeDB(h.db)
		}
	}

	respond.Data(w, http.StatusOK, results)
}

// ResetTestData wipes all data and re-seeds with default test data.
// POST /api/v1/admin/test/reset
func (h *TestHandler) ResetTestData(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Environment == "production" {
		respond.Error(w, http.StatusForbidden, "forbidden", "test endpoints disabled in production")
		return
	}

	slog.Warn("test: resetting all data")

	err := h.db.Transaction(func(tx *gorm.DB) error {
		// Delete in correct order to respect FK constraints
		tables := []string{
			"attachments", "messages", "outbound_queue",
			"quarantines", "vacation_configs", "vacation_responses",
			"contacts", "sieve_scripts", "quota_usages",
			"linked_accounts", "webmail_accounts",
			"aliases", "pipeline_logs", "pipelines", "custom_filters",
			"bans", "activity_logs", "restmail_capabilities",
			"dkim_keys", "certificates",
			"sender_rules", "greylist_entries",
			"mailboxes", "domains",
		}
		for _, table := range tables {
			if err := tx.Exec(fmt.Sprintf("DELETE FROM %s", table)).Error; err != nil {
				slog.Debug("test: delete from table", "table", table, "error", err)
				// Ignore errors for tables that may not exist
			}
		}
		return nil
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal", "failed to reset data: "+err.Error())
		return
	}

	// Re-seed
	if err := seedTestData(h.db); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal", "failed to seed data: "+err.Error())
		return
	}

	respond.Data(w, http.StatusOK, map[string]string{"status": "reset_complete"})
}

// SeedTestData creates default test domains, mailboxes, aliases, and accounts.
// POST /api/v1/admin/test/seed
func (h *TestHandler) SeedTestData(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Environment == "production" {
		respond.Error(w, http.StatusForbidden, "forbidden", "test endpoints disabled in production")
		return
	}

	if err := seedTestData(h.db); err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal", "failed to seed data: "+err.Error())
		return
	}

	respond.Data(w, http.StatusOK, map[string]string{"status": "seed_complete"})
}

// Snapshot captures the current database state as JSON.
// POST /api/v1/admin/test/snapshot
func (h *TestHandler) Snapshot(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Environment == "production" {
		respond.Error(w, http.StatusForbidden, "forbidden", "test endpoints disabled in production")
		return
	}

	snap := make(map[string]any)

	var domains []models.Domain
	h.db.Find(&domains)
	snap["domains"] = domains

	var mailboxes []models.Mailbox
	h.db.Find(&mailboxes)
	snap["mailboxes"] = mailboxes

	var aliases []models.Alias
	h.db.Find(&aliases)
	snap["aliases"] = aliases

	var messages []models.Message
	h.db.Find(&messages)
	snap["messages"] = messages

	var queue []models.OutboundQueue
	h.db.Find(&queue)
	snap["queue"] = queue

	var webmailAccounts []models.WebmailAccount
	h.db.Find(&webmailAccounts)
	snap["webmail_accounts"] = webmailAccounts

	var pipelines []models.Pipeline
	h.db.Find(&pipelines)
	snap["pipelines"] = pipelines

	var bans []models.Ban
	h.db.Find(&bans)
	snap["bans"] = bans

	snap["snapshot_at"] = time.Now()

	respond.Data(w, http.StatusOK, snap)
}

// RestoreSnapshot restores database state from a previously captured snapshot.
// POST /api/v1/admin/test/snapshot/restore
func (h *TestHandler) RestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Environment == "production" {
		respond.Error(w, http.StatusForbidden, "forbidden", "test endpoints disabled in production")
		return
	}

	var snap struct {
		Domains         []models.Domain         `json:"domains"`
		Mailboxes       []models.Mailbox        `json:"mailboxes"`
		Aliases         []models.Alias          `json:"aliases"`
		Messages        []models.Message        `json:"messages"`
		Queue           []models.OutboundQueue  `json:"queue"`
		WebmailAccounts []models.WebmailAccount `json:"webmail_accounts"`
		Pipelines       []models.Pipeline       `json:"pipelines"`
		Bans            []models.Ban            `json:"bans"`
	}

	if err := json.NewDecoder(r.Body).Decode(&snap); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid_json", "malformed snapshot data")
		return
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		// Clear existing data
		tables := []string{
			"attachments", "messages", "outbound_queue",
			"quarantines", "vacation_configs", "vacation_responses",
			"contacts", "sieve_scripts", "quota_usages",
			"linked_accounts", "webmail_accounts",
			"aliases", "pipeline_logs", "pipelines", "custom_filters",
			"bans", "activity_logs", "restmail_capabilities",
			"dkim_keys", "certificates",
			"sender_rules", "greylist_entries",
			"mailboxes", "domains",
		}
		for _, table := range tables {
			tx.Exec(fmt.Sprintf("DELETE FROM %s", table))
		}

		// Restore in FK-safe order
		for i := range snap.Domains {
			if err := tx.Create(&snap.Domains[i]).Error; err != nil {
				return fmt.Errorf("restore domains: %w", err)
			}
		}
		for i := range snap.Mailboxes {
			if err := tx.Create(&snap.Mailboxes[i]).Error; err != nil {
				return fmt.Errorf("restore mailboxes: %w", err)
			}
		}
		for i := range snap.Aliases {
			if err := tx.Create(&snap.Aliases[i]).Error; err != nil {
				return fmt.Errorf("restore aliases: %w", err)
			}
		}
		for i := range snap.WebmailAccounts {
			if err := tx.Create(&snap.WebmailAccounts[i]).Error; err != nil {
				return fmt.Errorf("restore webmail accounts: %w", err)
			}
		}
		for i := range snap.Messages {
			if err := tx.Create(&snap.Messages[i]).Error; err != nil {
				return fmt.Errorf("restore messages: %w", err)
			}
		}
		for i := range snap.Queue {
			if err := tx.Create(&snap.Queue[i]).Error; err != nil {
				return fmt.Errorf("restore queue: %w", err)
			}
		}
		for i := range snap.Pipelines {
			if err := tx.Create(&snap.Pipelines[i]).Error; err != nil {
				return fmt.Errorf("restore pipelines: %w", err)
			}
		}
		for i := range snap.Bans {
			if err := tx.Create(&snap.Bans[i]).Error; err != nil {
				return fmt.Errorf("restore bans: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "internal", "restore failed: "+err.Error())
		return
	}

	respond.Data(w, http.StatusOK, map[string]string{"status": "restore_complete"})
}

// --- helpers ---

func probeTCP(host string, port int, timeout time.Duration) map[string]any {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	latency := time.Since(start)
	if err != nil {
		return map[string]any{
			"status":  "down",
			"address": addr,
			"error":   err.Error(),
			"latency": latency.String(),
		}
	}
	conn.Close()
	return map[string]any{
		"status":  "up",
		"address": addr,
		"latency": latency.String(),
	}
}

func probeDNS(domain string, timeout time.Duration) map[string]any {
	resolver := &net.Resolver{}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	records, err := resolver.LookupMX(ctx, domain)
	latency := time.Since(start)
	if err != nil {
		return map[string]any{
			"status":  "error",
			"domain":  domain,
			"error":   err.Error(),
			"latency": latency.String(),
		}
	}
	mxs := make([]string, len(records))
	for i, mx := range records {
		mxs[i] = fmt.Sprintf("%s (pref %d)", mx.Host, mx.Pref)
	}
	return map[string]any{
		"status":     "ok",
		"domain":     domain,
		"mx_records": mxs,
		"latency":    latency.String(),
	}
}

func probeDB(db *gorm.DB) map[string]any {
	start := time.Now()
	sqlDB, err := db.DB()
	if err != nil {
		return map[string]any{"status": "error", "error": err.Error()}
	}
	if err := sqlDB.Ping(); err != nil {
		return map[string]any{"status": "error", "error": err.Error(), "latency": time.Since(start).String()}
	}
	return map[string]any{"status": "ok", "latency": time.Since(start).String()}
}

// seedTestData creates the standard test domains, mailboxes, aliases, and accounts.
func seedTestData(database *gorm.DB) error {
	defaultPassword, err := auth.HashPassword("password123")
	if err != nil {
		return err
	}

	domains := []models.Domain{
		{Name: "mail1.test", ServerType: "traditional", Active: true, DefaultQuotaBytes: 1073741824},
		{Name: "mail2.test", ServerType: "traditional", Active: true, DefaultQuotaBytes: 1073741824},
		{Name: "mail3.test", ServerType: "restmail", Active: true, DefaultQuotaBytes: 1073741824},
	}
	for i := range domains {
		database.Where("name = ?", domains[i].Name).FirstOrCreate(&domains[i])
	}

	mailboxes := []models.Mailbox{
		{DomainID: domains[0].ID, LocalPart: "alice", Address: "alice@mail1.test", Password: defaultPassword, DisplayName: "Alice Smith", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[0].ID, LocalPart: "bob", Address: "bob@mail1.test", Password: defaultPassword, DisplayName: "Bob Jones", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[0].ID, LocalPart: "postmaster", Address: "postmaster@mail1.test", Password: defaultPassword, DisplayName: "Postmaster", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[1].ID, LocalPart: "charlie", Address: "charlie@mail2.test", Password: defaultPassword, DisplayName: "Charlie Brown", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[1].ID, LocalPart: "diana", Address: "diana@mail2.test", Password: defaultPassword, DisplayName: "Diana Prince", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[1].ID, LocalPart: "postmaster", Address: "postmaster@mail2.test", Password: defaultPassword, DisplayName: "Postmaster", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[2].ID, LocalPart: "eve", Address: "eve@mail3.test", Password: defaultPassword, DisplayName: "Eve Wilson", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[2].ID, LocalPart: "frank", Address: "frank@mail3.test", Password: defaultPassword, DisplayName: "Frank Miller", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[2].ID, LocalPart: "postmaster", Address: "postmaster@mail3.test", Password: defaultPassword, DisplayName: "Postmaster", QuotaBytes: 1073741824, Active: true},
	}
	for i := range mailboxes {
		database.Where("address = ?", mailboxes[i].Address).FirstOrCreate(&mailboxes[i])
		database.Where("mailbox_id = ?", mailboxes[i].ID).FirstOrCreate(&models.QuotaUsage{MailboxID: mailboxes[i].ID})
	}

	aliases := []models.Alias{
		{DomainID: domains[0].ID, SourceAddress: "info@mail1.test", DestinationAddress: "alice@mail1.test", Active: true},
		{DomainID: domains[1].ID, SourceAddress: "info@mail2.test", DestinationAddress: "charlie@mail2.test", Active: true},
		{DomainID: domains[2].ID, SourceAddress: "info@mail3.test", DestinationAddress: "eve@mail3.test", Active: true},
		{DomainID: domains[2].ID, SourceAddress: "admin@mail3.test", DestinationAddress: "eve@mail3.test", Active: true},
	}
	for i := range aliases {
		database.Where("source_address = ? AND destination_address = ?", aliases[i].SourceAddress, aliases[i].DestinationAddress).FirstOrCreate(&aliases[i])
	}

	for _, addr := range []string{"eve@mail3.test", "frank@mail3.test"} {
		var mailbox models.Mailbox
		database.Where("address = ?", addr).First(&mailbox)
		var account models.WebmailAccount
		database.Where("primary_mailbox_id = ?", mailbox.ID).FirstOrCreate(&account, models.WebmailAccount{PrimaryMailboxID: mailbox.ID})
	}

	return nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
