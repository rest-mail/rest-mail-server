package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	rmail "github.com/restmail/restmail/internal/mail"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// productionLocked reports whether the destructive/abusable test endpoints
// (reset, seed, snapshot/restore, send, probe, verify) must be refused.
//
// The gate fails CLOSED on any production-like ENVIRONMENT value via the shared
// Config.IsProduction predicate: trimmed, case-insensitive, matching the whole
// "prod…" family ("prod", "PROD", "Production", "production-eu", …). Keying on the
// exact string "production" would let a rebranded or mistyped value (e.g.
// ENVIRONMENT=prod) silently leave these endpoints ENABLED on a production
// deployment (CWE-489). Non-production environments (development/test/staging) are
// unaffected.
func (h *TestHandler) productionLocked() bool {
	return h.cfg.IsProduction()
}

// sendTestEmail sends a test email from one mailbox to another.
// POST /api/v1/admin/test/send
func (h *TestHandler) sendTestEmail(_ *http.Request, req sendTestEmailRequest) (TestEmailSent, error) {
	if h.productionLocked() {
		return TestEmailSent{}, endpoint.Refuse(http.StatusForbidden, "forbidden", "test endpoints disabled in production")
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
		return TestEmailSent{}, endpoint.Refuse(http.StatusNotFound, "not_found", "sender mailbox not found")
	}

	// Look up recipient mailbox
	var recipientMb models.Mailbox
	if err := h.db.Where("address = ?", req.To).First(&recipientMb).Error; err != nil {
		return TestEmailSent{}, endpoint.Refuse(http.StatusNotFound, "not_found", "recipient mailbox not found")
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
		RecipientsTo: models.JSONB(mustJSON([]string{req.To})),
		Subject:      req.Subject,
		BodyText:     req.Body,
		SizeBytes:    len(req.Body),
		ReceivedAt:   time.Now(),
	}

	if err := h.db.Create(&msg).Error; err != nil {
		return TestEmailSent{}, endpoint.Refuse(http.StatusInternalServerError, "internal", "failed to create test message")
	}

	// Also save a copy in the sender's Sent folder
	sent := models.Message{
		MailboxID:    senderMb.ID,
		Folder:       "Sent",
		MsgID:        messageID,
		ThreadID:     messageID,
		Sender:       req.From,
		SenderName:   senderMb.DisplayName,
		RecipientsTo: models.JSONB(mustJSON([]string{req.To})),
		Subject:      req.Subject,
		BodyText:     req.Body,
		SizeBytes:    len(req.Body),
		IsRead:       true,
		ReceivedAt:   time.Now(),
	}
	h.db.Create(&sent)

	return TestEmailSent{MessageID: msg.ID, MsgID: messageID, From: req.From, To: req.To, Subject: req.Subject}, nil
}

// verifyDelivery checks whether a message matching the given criteria was delivered.
// GET /api/v1/admin/test/verify?to=alice@mail1.test&subject=Test&timeout=5s
func (h *TestHandler) verifyDelivery(_ *http.Request, in verifyDeliveryRequest) (TestDelivery, error) {
	if h.productionLocked() {
		return TestDelivery{}, endpoint.Refuse(http.StatusForbidden, "forbidden", "test endpoints disabled in production")
	}

	to, subject, messageID, timeoutStr := in.To, in.Subject, in.MessageID, in.Timeout

	timeout := 5 * time.Second
	if timeoutStr != "" {
		if d, err := time.ParseDuration(timeoutStr); err == nil {
			timeout = d
		}
	}
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}

	// No mailbox at the address means nothing was delivered to it: that is an
	// answer (found: false), not a missing resource (#388).
	var mailbox models.Mailbox
	if err := h.db.Where("address = ?", to).First(&mailbox).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TestDelivery{Message: "no mailbox at that address"}, nil
		}
		return TestDelivery{}, endpoint.Refuse(http.StatusInternalServerError, "internal_error", "Failed to look up the recipient mailbox")
	}

	deadline := time.Now().Add(timeout)
	for {
		query := h.db.Where("mailbox_id = ? AND folder = ?", mailbox.ID, "INBOX")
		if subject != "" {
			query = query.Where("subject LIKE ? ESCAPE '\\'", "%"+escapeLike(subject)+"%")
		}
		if messageID != "" {
			query = query.Where("message_id = ?", messageID)
		}

		var msg models.Message
		if err := query.Order("received_at DESC").First(&msg).Error; err == nil {
			return TestDelivery{Found: true, MessageID: &msg.ID, MsgID: &msg.MsgID, Subject: &msg.Subject, Sender: &msg.Sender, ReceivedAt: &msg.ReceivedAt}, nil
		}

		if time.Now().After(deadline) {
			return TestDelivery{Message: "no matching message found within timeout"}, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// probeableServices are the services ProbeServices knows how to reach.
var probeableServices = map[string]bool{
	"smtp": true, "smtp_submission": true, "imap": true, "pop3": true, "dns": true, "db": true,
}

// probeServices checks connectivity to SMTP, IMAP, POP3, and DNS services.
// POST /api/v1/admin/test/probe
func (h *TestHandler) probeServices(_ *http.Request, req probeServicesRequest) (TestProbeResults, error) {
	if h.productionLocked() {
		return TestProbeResults{}, endpoint.Refuse(http.StatusForbidden, "forbidden", "test endpoints disabled in production")
	}

	for _, svc := range req.Services {
		if !probeableServices[strings.ToLower(svc)] {
			return TestProbeResults{}, endpoint.Invalid(map[string]string{
				"services": fmt.Sprintf("unknown service %q: want smtp, smtp_submission, imap, pop3, dns or db", svc),
			})
		}
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

	results := TestProbeResults{}

	for _, svc := range req.Services {
		switch strings.ToLower(svc) {
		// The probes follow the listeners. Client access is implicit TLS, so
		// smtp_submission, imap and pop3 mean 465, 993 and 995 — there is nothing on
		// 587, 143 or 110 to reach. `smtp` is inbound relay on 25, the one listener a
		// plain TCP probe still describes accurately.
		case "smtp":
			results.SMTP = probePtr(probeTCP(req.Host, h.cfg.SMTPPortInbound, timeout))
		case "smtp_submission":
			results.SMTPSubmission = probePtr(probeTCP(req.Host, h.cfg.SMTPPortSubmissionTLS, timeout))
		case "imap":
			results.IMAP = probePtr(probeTCP(req.Host, h.cfg.IMAPTLSPort, timeout))
		case "pop3":
			results.POP3 = probePtr(probeTCP(req.Host, h.cfg.POP3TLSPort, timeout))
		case "dns":
			results.DNS = probePtr(probeDNS("mail1.test", timeout))
		case "db":
			results.DB = probePtr(probeDB(h.db))
		}
	}

	return results, nil
}

// resetTestData wipes all data and re-seeds with default test data.
// POST /api/v1/admin/test/reset
func (h *TestHandler) resetTestData(_ *http.Request, _ endpoint.Empty) (TestDataStatus, error) {
	if h.productionLocked() {
		return TestDataStatus{}, endpoint.Refuse(http.StatusForbidden, "forbidden", "test endpoints disabled in production")
	}

	slog.Warn("test: resetting all data")

	err := h.db.Transaction(clearTestData)
	if err != nil {
		return TestDataStatus{}, endpoint.Refuse(http.StatusInternalServerError, "internal", "failed to reset data: "+err.Error())
	}

	// Re-seed
	if err := seedTestData(h.db); err != nil {
		return TestDataStatus{}, endpoint.Refuse(http.StatusInternalServerError, "internal", "failed to seed data: "+err.Error())
	}

	return TestDataStatus{Status: "reset_complete"}, nil
}

// seedTestData creates default test domains, mailboxes, aliases, and accounts.
// POST /api/v1/admin/test/seed
func (h *TestHandler) seedTestData(_ *http.Request, _ endpoint.Empty) (TestDataStatus, error) {
	if h.productionLocked() {
		return TestDataStatus{}, endpoint.Refuse(http.StatusForbidden, "forbidden", "test endpoints disabled in production")
	}

	if err := seedTestData(h.db); err != nil {
		return TestDataStatus{}, endpoint.Refuse(http.StatusInternalServerError, "internal", "failed to seed data: "+err.Error())
	}

	return TestDataStatus{Status: "seed_complete"}, nil
}

// snapshot captures the current database state as JSON.
// POST /api/v1/admin/test/snapshot
func (h *TestHandler) snapshot(_ *http.Request, _ endpoint.Empty) (TestSnapshot, error) {
	if h.productionLocked() {
		return TestSnapshot{}, endpoint.Refuse(http.StatusForbidden, "forbidden", "test endpoints disabled in production")
	}

	snap := testSnapshotModels{}

	var domains []models.Domain
	h.db.Find(&domains)
	snap.Domains = domains

	var mailboxes []models.Mailbox
	h.db.Find(&mailboxes)
	snap.Mailboxes = mailboxes

	var aliases []models.Alias
	h.db.Find(&aliases)
	snap.Aliases = aliases

	var messages []models.Message
	h.db.Find(&messages)
	snap.Messages = messages

	var queue []models.OutboundQueue
	h.db.Find(&queue)
	snap.Queue = queue

	var pipelines []models.Pipeline
	h.db.Find(&pipelines)
	snap.Pipelines = pipelines

	var bans []models.Ban
	h.db.Find(&bans)
	snap.Bans = bans

	snap.SnapshotAt = time.Now()

	return snapshotJSON[TestSnapshot](snap)
}

// restoreSnapshot restores database state from a previously captured snapshot.
// POST /api/v1/admin/test/snapshot/restore
func (h *TestHandler) restoreSnapshot(_ *http.Request, in RestoreTestSnapshot) (TestDataStatus, error) {
	if h.productionLocked() {
		return TestDataStatus{}, endpoint.Refuse(http.StatusForbidden, "forbidden", "test endpoints disabled in production")
	}

	snap, err := snapshotJSON[testSnapshotModels](in)
	if err != nil {
		return TestDataStatus{}, err
	}

	err = h.db.Transaction(func(tx *gorm.DB) error {
		// Clear existing data, sparing the admins.
		if err := clearTestData(tx); err != nil {
			return err
		}

		// Restore in FK-safe order
		for i := range snap.Domains {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&snap.Domains[i]).Error; err != nil {
				return fmt.Errorf("restore domains: %w", err)
			}
		}
		for i := range snap.Mailboxes {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&snap.Mailboxes[i]).Error; err != nil {
				return fmt.Errorf("restore mailboxes: %w", err)
			}
		}
		for i := range snap.Aliases {
			if err := tx.Create(&snap.Aliases[i]).Error; err != nil {
				return fmt.Errorf("restore aliases: %w", err)
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
		return TestDataStatus{}, endpoint.Refuse(http.StatusInternalServerError, "internal", "restore failed: "+err.Error())
	}

	return TestDataStatus{Status: "restore_complete"}, nil
}

// testDataAdmins are the accounts the test endpoints never clear: those
// holding a grant at "*", whoever runs the tests among them.
const testDataAdmins = `(SELECT holder_id FROM grants WHERE holder_kind = 'account' AND scope = '*')`

// clearTestData deletes the test data, in tx: every account but the admins
// (with their credentials and the grants on them), and everything else the
// fixtures make.
func clearTestData(tx *gorm.DB) error {
	stmts := []string{
		"DELETE FROM attachments", "DELETE FROM messages", "DELETE FROM outbound_queue",
		"DELETE FROM quarantines", "DELETE FROM vacation_configs", "DELETE FROM vacation_responses",
		"DELETE FROM contacts", "DELETE FROM sieve_scripts", "DELETE FROM quota_usages",
		"DELETE FROM credentials WHERE account_id NOT IN " + testDataAdmins,
		"DELETE FROM grants WHERE scope LIKE 'mailbox:%' OR holder_id NOT IN " + testDataAdmins,
		"DELETE FROM aliases", "DELETE FROM message_traces",
		"DELETE FROM custom_filters", "DELETE FROM bans",
		"DELETE FROM restmail_capabilities", "DELETE FROM certificates",
		"DELETE FROM sender_rules", "DELETE FROM greylist_entries",
		"DELETE FROM mailboxes WHERE id NOT IN " + testDataAdmins,
		"DELETE FROM pipelines WHERE domain_id NOT IN (SELECT domain_id FROM mailboxes)",
		"DELETE FROM domains WHERE id NOT IN (SELECT domain_id FROM mailboxes)",
	}
	for _, stmt := range stmts {
		if err := tx.Exec(stmt).Error; err != nil {
			// Ignore errors for tables that may not exist
			slog.Debug("test: clear table", "statement", stmt, "error", err)
		}
	}
	return nil
}

// --- helpers ---

func probeTCP(host string, port int, timeout time.Duration) TestProbeResult {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	latency := time.Since(start)
	if err != nil {
		return TestProbeResult{
			Status:  "down",
			Address: addr,
			Error:   err.Error(),
			Latency: latency.String(),
		}
	}
	conn.Close()
	return TestProbeResult{
		Status:  "up",
		Address: addr,
		Latency: latency.String(),
	}
}

func probeDNS(domain string, timeout time.Duration) TestProbeResult {
	resolver := &net.Resolver{}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	records, err := resolver.LookupMX(ctx, domain)
	latency := time.Since(start)
	if err != nil {
		return TestProbeResult{
			Status:  "error",
			Domain:  domain,
			Error:   err.Error(),
			Latency: latency.String(),
		}
	}
	mxs := make([]string, len(records))
	for i, mx := range records {
		mxs[i] = fmt.Sprintf("%s (pref %d)", mx.Host, mx.Pref)
	}
	return TestProbeResult{
		Status:    "ok",
		Domain:    domain,
		MXRecords: &mxs,
		Latency:   latency.String(),
	}
}

func probeDB(db *gorm.DB) TestProbeResult {
	start := time.Now()
	sqlDB, err := db.DB()
	if err != nil {
		return TestProbeResult{Status: "error", Error: err.Error()}
	}
	if err := sqlDB.Ping(); err != nil {
		return TestProbeResult{Status: "error", Error: err.Error(), Latency: time.Since(start).String()}
	}
	return TestProbeResult{Status: "ok", Latency: time.Since(start).String()}
}

// seedTestData creates the standard test domains, accounts (each with the
// password "password123") and aliases.
func seedTestData(database *gorm.DB) error {
	defaultPassword, err := hashPassword("password123")
	if err != nil {
		return err
	}

	domains := []models.Domain{
		{Name: "mail1.test", ServerType: "traditional", Active: true, DefaultQuotaBytes: 1073741824},
		{Name: "mail2.test", ServerType: "traditional", Active: true, DefaultQuotaBytes: 1073741824},
		{Name: "restmail.test", ServerType: "restmail", Active: true, DefaultQuotaBytes: 1073741824},
	}
	for i := range domains {
		database.Where("name = ?", domains[i].Name).FirstOrCreate(&domains[i])
	}

	mailboxes := []models.Mailbox{
		{DomainID: domains[0].ID, LocalPart: "alice", Address: "alice@mail1.test", DisplayName: "Alice Smith", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[0].ID, LocalPart: "bob", Address: "bob@mail1.test", DisplayName: "Bob Jones", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[0].ID, LocalPart: "postmaster", Address: "postmaster@mail1.test", DisplayName: "Postmaster", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[1].ID, LocalPart: "charlie", Address: "charlie@mail2.test", DisplayName: "Charlie Brown", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[1].ID, LocalPart: "diana", Address: "diana@mail2.test", DisplayName: "Diana Prince", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[1].ID, LocalPart: "postmaster", Address: "postmaster@mail2.test", DisplayName: "Postmaster", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[2].ID, LocalPart: "eve", Address: "eve@restmail.test", DisplayName: "Eve Wilson", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[2].ID, LocalPart: "frank", Address: "frank@restmail.test", DisplayName: "Frank Miller", QuotaBytes: 1073741824, Active: true},
		{DomainID: domains[2].ID, LocalPart: "postmaster", Address: "postmaster@restmail.test", DisplayName: "Postmaster", QuotaBytes: 1073741824, Active: true},
	}
	creds := repositories.NewCredentialRepository(database)
	for i := range mailboxes {
		res := database.Where("address = ?", mailboxes[i].Address).FirstOrCreate(&mailboxes[i])
		database.Where("mailbox_id = ?", mailboxes[i].ID).FirstOrCreate(&models.QuotaUsage{MailboxID: mailboxes[i].ID})
		if res.Error == nil && res.RowsAffected > 0 {
			if _, err := creds.SetPassword(mailboxes[i].ID, defaultPassword, nil, time.Now()); err != nil {
				return err
			}
		}
	}

	aliases := []models.Alias{
		{DomainID: domains[0].ID, SourceAddress: "info@mail1.test", DestinationAddress: "alice@mail1.test", Active: true},
		{DomainID: domains[1].ID, SourceAddress: "info@mail2.test", DestinationAddress: "charlie@mail2.test", Active: true},
		{DomainID: domains[2].ID, SourceAddress: "info@restmail.test", DestinationAddress: "eve@restmail.test", Active: true},
		{DomainID: domains[2].ID, SourceAddress: "admin@restmail.test", DestinationAddress: "eve@restmail.test", Active: true},
	}
	for i := range aliases {
		database.Where("source_address = ? AND destination_address = ?", aliases[i].SourceAddress, aliases[i].DestinationAddress).FirstOrCreate(&aliases[i])
	}

	return nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// testSnapshotModels is the database fixture format, used only for conversion;
// the generated endpoint types retain exactly its exported JSON fields.
type testSnapshotModels struct {
	Domains    []models.Domain        `json:"domains"`
	Mailboxes  []models.Mailbox       `json:"mailboxes"`
	Aliases    []models.Alias         `json:"aliases"`
	Messages   []models.Message       `json:"messages"`
	Queue      []models.OutboundQueue `json:"queue"`
	Pipelines  []models.Pipeline      `json:"pipelines"`
	Bans       []models.Ban           `json:"bans"`
	SnapshotAt time.Time              `json:"snapshot_at"`
}

func snapshotJSON[T any](in any) (T, error) {
	var out T
	data, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}
