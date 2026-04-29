package queue

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// mtaStsPolicy represents a parsed MTA-STS (RFC 8461) policy.
type mtaStsPolicy struct {
	Version string   // "STSv1"
	Mode    string   // "enforce", "testing", "none"
	MX      []string // MX host patterns (may include wildcards like "*.example.com")
	MaxAge  int      // seconds
}

// mxMatchesPolicy checks whether the given MX host matches any of the
// policy's mx patterns. Patterns may use a leading wildcard (e.g. "*.example.com").
func (p *mtaStsPolicy) mxMatchesPolicy(mxHost string) bool {
	mxHost = strings.TrimSuffix(strings.ToLower(mxHost), ".")
	for _, pattern := range p.MX {
		pattern = strings.TrimSuffix(strings.ToLower(pattern), ".")
		if pattern == mxHost {
			return true
		}
		// Wildcard match: "*.example.com" matches "mail.example.com"
		if strings.HasPrefix(pattern, "*.") {
			suffix := pattern[1:] // ".example.com"
			if strings.HasSuffix(mxHost, suffix) && strings.Count(mxHost, ".") == strings.Count(suffix, ".") {
				return true
			}
		}
	}
	return false
}

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
func parseSMTPError(err error) *SMTPError {
	msg := err.Error()
	// Try to parse "NNN X.X.X rest" or "NNN rest"
	if len(msg) >= 3 && msg[0] >= '1' && msg[0] <= '5' {
		code := 0
		_, _ = fmt.Sscanf(msg[:3], "%d", &code)
		if code >= 100 && code <= 599 {
			rest := strings.TrimSpace(msg[3:])
			enhanced := ""
			if len(rest) > 0 && rest[0] >= '1' && rest[0] <= '5' {
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

// Worker processes outbound mail queue entries.
type Worker struct {
	db            *gorm.DB
	hostname      string
	numWorkers    int
	pollInterval  time.Duration
	tlsInsecure   bool
	shutdown      chan struct{}
	wg            sync.WaitGroup
}

// NewWorker creates a new queue worker.
func NewWorker(db *gorm.DB, hostname string, numWorkers int, pollInterval time.Duration) *Worker {
	return &Worker{
		db:           db,
		hostname:     hostname,
		numWorkers:   numWorkers,
		pollInterval: pollInterval,
		tlsInsecure:  false,
		shutdown:     make(chan struct{}),
	}
}

// SetTLSInsecure sets whether to skip TLS certificate verification for outbound delivery.
func (w *Worker) SetTLSInsecure(insecure bool) {
	w.tlsInsecure = insecure
}

// Start begins processing the outbound queue.
func (w *Worker) Start() {
	slog.Info("queue: starting workers", "count", w.numWorkers, "poll_interval", w.pollInterval)

	for i := 0; i < w.numWorkers; i++ {
		w.wg.Add(1)
		go func(workerID int) {
			defer w.wg.Done()
			w.processLoop(workerID)
		}(i)
	}
}

// Shutdown stops the queue workers gracefully.
func (w *Worker) Shutdown() {
	close(w.shutdown)
	w.wg.Wait()
	slog.Info("queue: workers stopped")
}

func (w *Worker) processLoop(workerID int) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.shutdown:
			return
		case <-ticker.C:
			w.processOne(workerID)
		}
	}
}

func (w *Worker) processOne(workerID int) {
	// Claim a pending item using raw SQL with FOR UPDATE SKIP LOCKED
	var item models.OutboundQueue
	now := time.Now()
	err := w.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Raw(
			`SELECT * FROM outbound_queue
			 WHERE status IN ('pending', 'deferred') AND next_attempt <= ? AND expires_at > ?
			 ORDER BY next_attempt ASC
			 LIMIT 1
			 FOR UPDATE SKIP LOCKED`, now, now,
		).Scan(&item)

		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}

		// Mark as delivering
		return tx.Model(&item).Updates(map[string]interface{}{
			"status":       "delivering",
			"last_attempt": now,
			"attempts":     item.Attempts + 1,
		}).Error
	})

	if err != nil {
		// No items to process (or DB error)
		return
	}

	slog.Info("queue: processing",
		"worker", workerID,
		"id", item.ID,
		"sender", item.Sender,
		"recipient", item.Recipient,
		"domain", item.Domain,
		"attempt", item.Attempts+1,
	)

	// Attempt delivery
	deliveryErr := w.deliver(item)

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
}

// deliver attempts to send a message via SMTP to the destination MX.
// It first checks if the primary MX supports the RESTMAIL protocol for
// direct HTTPS delivery, falling back to standard SMTP if not.
func (w *Worker) deliver(item models.OutboundQueue) error {
	// Look up MX records
	mxRecords, err := net.LookupMX(item.Domain)
	if err != nil {
		return fmt.Errorf("MX lookup failed for %s: %w", item.Domain, err)
	}

	if len(mxRecords) == 0 {
		// Fall back to A record
		mxRecords = []*net.MX{{Host: item.Domain, Pref: 0}}
	}

	firstHost := strings.TrimSuffix(mxRecords[0].Host, ".")

	// Check capability cache before EHLO probe
	var cap models.RESTMAILCapability
	cacheHit := w.db.Where("domain = ? AND expires_at > ?", item.Domain, time.Now()).
		First(&cap).Error == nil

	if cacheHit {
		if cap.Supported {
			slog.Info("queue: using cached RESTMAIL capability", "domain", item.Domain, "url", cap.EndpointURL)
			err := w.deliverRESTMAILHTTPS(cap.EndpointURL, item)
			if err == nil {
				return nil
			}
			slog.Warn("queue: cached RESTMAIL delivery failed, invalidating cache",
				"domain", item.Domain, "error", err)
			w.db.Where("domain = ?", item.Domain).Delete(&models.RESTMAILCapability{})
			// Fall through to SMTP
		}
		// cap.Supported == false: skip RESTMAIL, go straight to SMTP
	} else {
		// No cache or expired — do EHLO probe
		upgraded, err := w.tryRESTMAIL(firstHost, item)
		if upgraded && err == nil {
			return nil // RESTMAIL delivery succeeded
		}
		if upgraded && err != nil {
			slog.Warn("queue: RESTMAIL delivery failed, falling back to SMTP",
				"host", firstHost, "error", err)
		}
	}

	// Check MTA-STS policy for the target domain (RFC 8461)
	var stsPolicy *mtaStsPolicy
	policy, err := w.checkMTASTS(item.Domain)
	if err != nil {
		slog.Debug("queue: MTA-STS not available, proceeding normally",
			"domain", item.Domain, "error", err)
	} else {
		stsPolicy = policy
		slog.Info("queue: MTA-STS policy found",
			"domain", item.Domain, "mode", policy.Mode, "mx_count", len(policy.MX))
	}

	// Fall back to SMTP delivery
	var lastErr error
	for _, mx := range mxRecords {
		host := strings.TrimSuffix(mx.Host, ".")

		// MTA-STS MX validation
		if stsPolicy != nil && len(stsPolicy.MX) > 0 {
			if !stsPolicy.mxMatchesPolicy(host) {
				if stsPolicy.Mode == "enforce" {
					slog.Warn("queue: MTA-STS enforce — MX host not in policy, skipping",
						"host", host, "domain", item.Domain)
					lastErr = fmt.Errorf("MTA-STS enforce: MX host %s not in policy for %s", host, item.Domain)
					continue
				}
				if stsPolicy.Mode == "testing" {
					slog.Warn("queue: MTA-STS testing — MX host not in policy, delivering anyway",
						"host", host, "domain", item.Domain)
				}
			}
		}

		lastErr = w.deliverToHost(host, item, stsPolicy)
		if lastErr == nil {
			return nil
		}
		slog.Debug("queue: MX attempt failed", "host", host, "error", lastErr)
	}

	return fmt.Errorf("all MX hosts failed: %w", lastErr)
}

// cacheCapability stores a RESTMAIL capability probe result in the database.
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

// deliverRESTMAILHTTPS sends the message via HTTPS POST to a known RESTMAIL endpoint.
func (w *Worker) deliverRESTMAILHTTPS(endpointURL string, item models.OutboundQueue) error {
	payload := map[string]interface{}{
		"from":        item.Sender,
		"to":          []string{item.Recipient},
		"raw_message": item.RawMessage,
	}
	payloadBytes, _ := json.Marshal(payload)

	messagesURL := endpointURL + "/messages"
	if !strings.HasPrefix(messagesURL, "http") {
		messagesURL = "https://" + messagesURL
	}

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: w.tlsInsecure},
		},
	}
	resp, err := httpClient.Post(messagesURL, "application/json", bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("RESTMAIL POST to %s: %w", messagesURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Info("queue: RESTMAIL delivery succeeded", "url", messagesURL, "recipient", item.Recipient)
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("RESTMAIL delivery got %d: %s", resp.StatusCode, string(body))
}

// tryRESTMAIL probes a host for the RESTMAIL EHLO extension. If found,
// it delivers the message via HTTPS POST instead of SMTP.
// Returns (true, nil) on successful RESTMAIL delivery,
// (true, err) if RESTMAIL was detected but delivery failed,
// (false, nil) if the host does not support RESTMAIL.
func (w *Worker) tryRESTMAIL(host string, item models.OutboundQueue) (upgraded bool, err error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.Dial("tcp", host+":25")
	if err != nil {
		return false, nil // Can't connect, let SMTP path handle it
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return false, nil
	}

	if err := client.Hello(w.hostname); err != nil {
		client.Close()
		return false, nil
	}

	ok, restmailURL := client.Extension("RESTMAIL")
	_ = client.Quit()
	client.Close()

	if !ok || restmailURL == "" {
		w.cacheCapability(item.Domain, false, "")
		return false, nil // No RESTMAIL support
	}

	slog.Info("queue: RESTMAIL capability detected", "host", host, "url", restmailURL)
	w.cacheCapability(item.Domain, true, restmailURL)

	err = w.deliverRESTMAILHTTPS(restmailURL, item)
	if err != nil {
		return true, err
	}
	return true, nil
}

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

// checkMTASTS fetches and parses the MTA-STS policy for a domain.
// It returns the parsed policy or nil if no policy is available.
// The fetch uses a short timeout (5s) so it never blocks delivery.
func (w *Worker) checkMTASTS(domain string) (*mtaStsPolicy, error) {
	url := fmt.Sprintf("https://mta-sts.%s/.well-known/mta-sts.txt", domain)

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: w.tlsInsecure},
		},
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("MTA-STS fetch failed for %s: %w", domain, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("MTA-STS returned HTTP %d for %s", resp.StatusCode, domain)
	}

	// Limit body to 64KB to prevent abuse
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("MTA-STS read failed for %s: %w", domain, err)
	}

	policy := &mtaStsPolicy{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "version":
			policy.Version = value
		case "mode":
			policy.Mode = value
		case "mx":
			policy.MX = append(policy.MX, value)
		case "max_age":
			_, _ = fmt.Sscanf(value, "%d", &policy.MaxAge)
		}
	}

	if policy.Version != "STSv1" {
		return nil, fmt.Errorf("MTA-STS unsupported version %q for %s", policy.Version, domain)
	}

	return policy, nil
}

// deliverToHost attempts SMTP delivery to a specific host.
// If an MTA-STS policy is provided, TLS requirements are enforced accordingly.
func (w *Worker) deliverToHost(host string, item models.OutboundQueue, stsPolicy *mtaStsPolicy) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	addr := host + ":25"

	// Determine if MTA-STS requires TLS
	requireTLS := stsPolicy != nil && stsPolicy.Mode == "enforce"
	testingTLS := stsPolicy != nil && stsPolicy.Mode == "testing"

	// Dial with timeout
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP client for %s: %w", addr, err)
	}
	defer client.Close()

	// Say hello
	if err := client.Hello(w.hostname); err != nil {
		return fmt.Errorf("EHLO to %s: %w", host, err)
	}

	// Try STARTTLS if available
	tlsEstablished := false
	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: w.tlsInsecure,
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			if requireTLS {
				return fmt.Errorf("MTA-STS enforce: STARTTLS failed for %s: %w", host, err)
			}
			if testingTLS {
				slog.Warn("queue: MTA-STS testing — STARTTLS failed, delivering anyway",
					"host", host, "error", err)
			} else {
				slog.Debug("queue: STARTTLS failed, continuing without TLS", "host", host, "error", err)
			}
		} else {
			tlsEstablished = true
		}
	} else {
		// No STARTTLS support at all
		if requireTLS {
			return fmt.Errorf("MTA-STS enforce: host %s does not support STARTTLS", host)
		}
		if testingTLS {
			slog.Warn("queue: MTA-STS testing — host does not support STARTTLS, delivering anyway",
				"host", host)
		}
	}

	_ = tlsEstablished // available for future logging/metrics

	// Set sender
	if err := client.Mail(item.Sender); err != nil {
		return parseSMTPError(err)
	}

	// Set recipient
	if err := client.Rcpt(item.Recipient); err != nil {
		return parseSMTPError(err)
	}

	// Send data
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA to %s: %w", host, err)
	}
	_, err = fmt.Fprint(wc, item.RawMessage)
	if err != nil {
		wc.Close()
		return fmt.Errorf("write message to %s: %w", host, err)
	}
	if err := wc.Close(); err != nil {
		return parseSMTPError(err)
	}

	// Quit
	_ = client.Quit()
	return nil
}

