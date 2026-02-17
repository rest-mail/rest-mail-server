package queue

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"math"
	"net"
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
		shutdown:     make(chan struct{}),
	}
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
		// TODO: Generate bounce notification (DSN) to sender
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

	// Try each MX in priority order
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
			InsecureSkipVerify: true, // TODO: Proper cert validation in production
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

