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
)

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

	slog.Warn("queue: delivery failed",
		"id", item.ID,
		"recipient", item.Recipient,
		"attempt", item.Attempts+1,
		"error", deliveryErr,
	)

	// Check if we should retry or bounce
	if item.Attempts+1 >= item.MaxRetries {
		w.db.Model(&item).Updates(map[string]interface{}{
			"status":     "bounced",
			"last_error": deliveryErr.Error(),
		})
		slog.Warn("queue: message bounced (max retries)", "id", item.ID, "recipient", item.Recipient)
		w.generateBounce(item)
		return
	}

	// Exponential backoff: 1min, 2min, 4min, 8min, ... up to 4 hours
	backoff := time.Duration(math.Pow(2, float64(item.Attempts))) * time.Minute
	if backoff > 4*time.Hour {
		backoff = 4 * time.Hour
	}

	w.db.Model(&item).Updates(map[string]interface{}{
		"status":      "deferred",
		"next_attempt": time.Now().Add(backoff),
		"last_error":  deliveryErr.Error(),
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

	// Try RESTMAIL protocol upgrade on the first MX host
	firstHost := strings.TrimSuffix(mxRecords[0].Host, ".")
	upgraded, err := w.tryRESTMAIL(firstHost, item)
	if upgraded && err == nil {
		return nil // RESTMAIL delivery succeeded
	}
	if upgraded && err != nil {
		slog.Warn("queue: RESTMAIL delivery failed, falling back to SMTP",
			"host", firstHost, "error", err)
	}

	// Fall back to SMTP delivery
	var lastErr error
	for _, mx := range mxRecords {
		host := strings.TrimSuffix(mx.Host, ".")
		lastErr = w.deliverToHost(host, item)
		if lastErr == nil {
			return nil
		}
		slog.Debug("queue: MX attempt failed", "host", host, "error", lastErr)
	}

	return fmt.Errorf("all MX hosts failed: %w", lastErr)
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
	client.Quit()
	client.Close()

	if !ok || restmailURL == "" {
		return false, nil // No RESTMAIL support
	}

	slog.Info("queue: RESTMAIL capability detected", "host", host, "url", restmailURL)

	// Build the delivery payload
	payload := map[string]interface{}{
		"sender":      item.Sender,
		"recipients":  []string{item.Recipient},
		"raw_message": item.RawMessage,
	}
	payloadBytes, _ := json.Marshal(payload)

	messagesURL := restmailURL + "/messages"
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
		return true, fmt.Errorf("RESTMAIL POST to %s: %w", messagesURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Info("queue: RESTMAIL delivery succeeded", "host", host, "recipient", item.Recipient)
		return true, nil
	}

	body, _ := io.ReadAll(resp.Body)
	return true, fmt.Errorf("RESTMAIL delivery got %d: %s", resp.StatusCode, string(body))
}

// generateBounce creates a DSN (Delivery Status Notification) and delivers
// it to the original sender's mailbox if the sender is a local user.
func (w *Worker) generateBounce(item models.OutboundQueue) {
	bounceSubject := fmt.Sprintf("Undelivered Mail Returned to Sender <%s>", item.Recipient)
	bounceBody := fmt.Sprintf(
		"This is the mail delivery system at %s.\n\n"+
			"I'm sorry to inform you that your message could not be delivered to one or more\n"+
			"recipients. The delivery has been attempted %d times over the message lifetime.\n\n"+
			"For further assistance, please contact your mail administrator.\n\n"+
			"--- Delivery report ---\n"+
			"Reporting-MTA: dns; %s\n"+
			"Final-Recipient: rfc822; %s\n"+
			"Action: failed\n"+
			"Status: 5.0.0\n"+
			"Diagnostic-Code: smtp; %s\n",
		w.hostname, item.Attempts, w.hostname, item.Recipient, item.LastError,
	)

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

	// Insert bounce message directly into the sender's INBOX
	now := time.Now()
	bounceMsg := map[string]interface{}{
		"mailbox_id":    senderMailbox.ID,
		"folder":        "INBOX",
		"sender":        "mailer-daemon@" + w.hostname,
		"sender_name":   "Mail Delivery System",
		"recipients_to": fmt.Sprintf(`["%s"]`, item.Sender),
		"recipients_cc": "[]",
		"subject":       bounceSubject,
		"body_text":     bounceBody,
		"is_read":       false,
		"size_bytes":    len(bounceSubject) + len(bounceBody),
		"received_at":   now,
		"created_at":    now,
		"updated_at":    now,
	}

	if err := w.db.Table("messages").Create(bounceMsg).Error; err != nil {
		slog.Error("queue: failed to insert bounce DSN", "sender", item.Sender, "error", err)
		return
	}

	slog.Info("queue: bounce DSN delivered", "sender", item.Sender, "failed_recipient", item.Recipient)
}

// deliverToHost attempts SMTP delivery to a specific host.
func (w *Worker) deliverToHost(host string, item models.OutboundQueue) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	addr := host + ":25"

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
	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: w.tlsInsecure,
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			slog.Debug("queue: STARTTLS failed, continuing without TLS", "host", host, "error", err)
		}
	}

	// Set sender
	if err := client.Mail(item.Sender); err != nil {
		return fmt.Errorf("MAIL FROM to %s: %w", host, err)
	}

	// Set recipient
	if err := client.Rcpt(item.Recipient); err != nil {
		return fmt.Errorf("RCPT TO %s at %s: %w", item.Recipient, host, err)
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
		return fmt.Errorf("end DATA to %s: %w", host, err)
	}

	// Quit
	client.Quit()
	return nil
}

