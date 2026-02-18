package digest

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// Worker sends periodic quarantine digest emails to mailbox owners.
type Worker struct {
	db       *gorm.DB
	interval time.Duration
	stop     chan struct{}
}

// NewWorker creates a new quarantine digest worker.
func NewWorker(db *gorm.DB, interval time.Duration) *Worker {
	if interval == 0 {
		interval = 24 * time.Hour
	}
	return &Worker{
		db:       db,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

// Start begins the periodic digest loop in a background goroutine.
func (w *Worker) Start() {
	go w.run()
	slog.Info("quarantine digest worker started", "interval", w.interval)
}

// Shutdown stops the digest worker.
func (w *Worker) Shutdown() {
	close(w.stop)
}

func (w *Worker) run() {
	// Run once at startup after a short delay, then on the interval
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-timer.C:
			w.sendDigests()
			timer.Reset(w.interval)
		}
	}
}

func (w *Worker) sendDigests() {
	// Find mailboxes that have unreleased quarantine items from the last digest interval
	cutoff := time.Now().Add(-w.interval)

	var items []models.Quarantine
	err := w.db.Where("released = false AND received_at > ?", cutoff).
		Order("mailbox_id ASC, received_at DESC").
		Find(&items).Error
	if err != nil {
		slog.Error("digest: failed to query quarantine", "error", err)
		return
	}

	if len(items) == 0 {
		return
	}

	// Group by mailbox
	byMailbox := make(map[uint][]models.Quarantine)
	for _, item := range items {
		byMailbox[item.MailboxID] = append(byMailbox[item.MailboxID], item)
	}

	for mailboxID, quarantined := range byMailbox {
		w.deliverDigest(mailboxID, quarantined)
	}

	slog.Info("digest: sent quarantine digests", "mailboxes", len(byMailbox), "total_items", len(items))
}

func (w *Worker) deliverDigest(mailboxID uint, items []models.Quarantine) {
	var mailbox models.Mailbox
	if err := w.db.Preload("Domain").First(&mailbox, mailboxID).Error; err != nil {
		slog.Warn("digest: mailbox not found", "mailbox_id", mailboxID, "error", err)
		return
	}

	address := fmt.Sprintf("%s@%s", mailbox.LocalPart, mailbox.Domain.Name)
	subject := fmt.Sprintf("Quarantine Digest: %d message(s) held", len(items))

	// Build plain text body
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Quarantine Digest for %s\n", address))
	sb.WriteString(fmt.Sprintf("Period: %s\n", time.Now().Format("2006-01-02 15:04 MST")))
	sb.WriteString(fmt.Sprintf("Messages held: %d\n\n", len(items)))
	sb.WriteString(strings.Repeat("-", 60) + "\n\n")

	for i, item := range items {
		sb.WriteString(fmt.Sprintf("%d. From: %s\n", i+1, item.Sender))
		sb.WriteString(fmt.Sprintf("   Subject: %s\n", item.Subject))
		sb.WriteString(fmt.Sprintf("   Received: %s\n", item.ReceivedAt.Format("2006-01-02 15:04")))
		sb.WriteString(fmt.Sprintf("   Reason: %s\n", item.QuarantineReason))
		if item.BodyPreview != "" {
			preview := item.BodyPreview
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			sb.WriteString(fmt.Sprintf("   Preview: %s\n", preview))
		}
		sb.WriteString("\n")
	}

	sb.WriteString(strings.Repeat("-", 60) + "\n")
	sb.WriteString("To review and release these messages, visit the Quarantine section in webmail.\n")

	// Build HTML body
	var html strings.Builder
	html.WriteString("<html><body style=\"font-family: sans-serif;\">")
	html.WriteString(fmt.Sprintf("<h2>Quarantine Digest for %s</h2>", address))
	html.WriteString(fmt.Sprintf("<p>%d message(s) are being held in quarantine.</p>", len(items)))
	html.WriteString("<table border=\"1\" cellpadding=\"8\" cellspacing=\"0\" style=\"border-collapse: collapse;\">")
	html.WriteString("<tr><th>#</th><th>From</th><th>Subject</th><th>Received</th><th>Reason</th></tr>")

	for i, item := range items {
		html.WriteString(fmt.Sprintf(
			"<tr><td>%d</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>",
			i+1, item.Sender, item.Subject,
			item.ReceivedAt.Format("2006-01-02 15:04"),
			item.QuarantineReason,
		))
	}

	html.WriteString("</table>")
	html.WriteString("<p>To review and release these messages, visit the <strong>Quarantine</strong> section in webmail.</p>")
	html.WriteString("</body></html>")

	msg := models.Message{
		MailboxID: mailboxID,
		Folder:    "INBOX",
		Sender:    fmt.Sprintf("quarantine-digest@%s", mailbox.Domain.Name),
		Subject:   subject,
		BodyText:  sb.String(),
		BodyHTML:  html.String(),
		SizeBytes: len(sb.String()),
	}

	if err := w.db.Create(&msg).Error; err != nil {
		slog.Warn("digest: failed to deliver digest", "mailbox_id", mailboxID, "error", err)
		return
	}

	// Update quota
	w.db.Model(&models.Mailbox{}).Where("id = ?", mailboxID).
		Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", msg.SizeBytes))
}
