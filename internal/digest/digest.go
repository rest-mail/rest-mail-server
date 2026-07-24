package digest

import (
	"bytes"
	"fmt"
	"html/template"
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

	if !mailbox.Active {
		slog.Debug("digest: skipping inactive mailbox", "mailbox_id", mailboxID)
		return
	}

	address := fmt.Sprintf("%s@%s", mailbox.LocalPart, mailbox.Domain.Name)
	subject := fmt.Sprintf("Quarantine Digest: %d message(s) held", len(items))

	bodyText, bodyHTML := renderDigest(address, time.Now(), items)

	msg := models.Message{
		MailboxID: mailboxID,
		Folder:    "INBOX",
		Sender:    fmt.Sprintf("quarantine-digest@%s", mailbox.Domain.Name),
		Subject:   subject,
		BodyText:  bodyText,
		BodyHTML:  bodyHTML,
		SizeBytes: len(bodyText),
	}

	if err := w.db.Create(&msg).Error; err != nil {
		slog.Warn("digest: failed to deliver digest", "mailbox_id", mailboxID, "error", err)
		return
	}

	// Update quota
	w.db.Model(&models.Mailbox{}).Where("id = ?", mailboxID).
		Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", msg.SizeBytes))
}

// lineSanitizer strips CR/LF from attacker-controlled fields before they are
// placed in the plain-text body, so injected newlines cannot forge additional
// digest entries.
var lineSanitizer = strings.NewReplacer("\r", " ", "\n", " ")

// sanitizeLine removes CR/LF from a single field value for the plain-text body.
func sanitizeLine(s string) string { return lineSanitizer.Replace(s) }

// digestItemView is the per-message row passed to the HTML template. Its string
// fields are attacker-controlled and rely on html/template's contextual
// auto-escaping for neutralization.
type digestItemView struct {
	Index    int
	Sender   string
	Subject  string
	Received string
	Reason   string
}

// digestView is the top-level data passed to digestHTMLTemplate.
type digestView struct {
	Address string
	Count   int
	Items   []digestItemView
}

// digestHTMLTemplate renders the quarantine digest HTML. Every interpolated
// value originates from quarantined (attacker-controlled) mail, so the template
// relies on html/template's contextual auto-escaping — never manual string
// concatenation — to prevent stored XSS.
var digestHTMLTemplate = template.Must(template.New("digest").Parse(
	`<html><body style="font-family: sans-serif;">` +
		`<h2>Quarantine Digest for {{.Address}}</h2>` +
		`<p>{{.Count}} message(s) are being held in quarantine.</p>` +
		`<table border="1" cellpadding="8" cellspacing="0" style="border-collapse: collapse;">` +
		`<tr><th>#</th><th>From</th><th>Subject</th><th>Received</th><th>Reason</th></tr>` +
		`{{range .Items}}<tr><td>{{.Index}}</td><td>{{.Sender}}</td><td>{{.Subject}}</td>` +
		`<td>{{.Received}}</td><td>{{.Reason}}</td></tr>{{end}}` +
		`</table>` +
		`<p>To review and release these messages, visit the <strong>Quarantine</strong> section in webmail.</p>` +
		`</body></html>`))

// renderDigest builds the plain-text and HTML bodies for a quarantine digest.
// All message-derived fields (sender, subject, reason, preview) are attacker-
// controlled: the HTML body relies on html/template contextual auto-escaping,
// and the plain-text body strips CR/LF so injected newlines cannot forge entries.
func renderDigest(address string, now time.Time, items []models.Quarantine) (bodyText, bodyHTML string) {
	// Build plain text body
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Quarantine Digest for %s\n", address))
	sb.WriteString(fmt.Sprintf("Period: %s\n", now.Format("2006-01-02 15:04 MST")))
	sb.WriteString(fmt.Sprintf("Messages held: %d\n\n", len(items)))
	sb.WriteString(strings.Repeat("-", 60) + "\n\n")

	view := digestView{Address: address, Count: len(items)}
	for i, item := range items {
		received := item.ReceivedAt.Format("2006-01-02 15:04")
		sb.WriteString(fmt.Sprintf("%d. From: %s\n", i+1, sanitizeLine(item.Sender)))
		sb.WriteString(fmt.Sprintf("   Subject: %s\n", sanitizeLine(item.Subject)))
		sb.WriteString(fmt.Sprintf("   Received: %s\n", received))
		sb.WriteString(fmt.Sprintf("   Reason: %s\n", sanitizeLine(item.QuarantineReason)))
		if item.BodyPreview != "" {
			preview := item.BodyPreview
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			sb.WriteString(fmt.Sprintf("   Preview: %s\n", sanitizeLine(preview)))
		}
		sb.WriteString("\n")

		view.Items = append(view.Items, digestItemView{
			Index:    i + 1,
			Sender:   item.Sender,
			Subject:  item.Subject,
			Received: received,
			Reason:   item.QuarantineReason,
		})
	}

	sb.WriteString(strings.Repeat("-", 60) + "\n")
	sb.WriteString("To review and release these messages, visit the Quarantine section in webmail.\n")

	// Build HTML body via html/template (contextual auto-escaping).
	var hb bytes.Buffer
	if err := digestHTMLTemplate.Execute(&hb, view); err != nil {
		// Never fall back to hand-built markup; deliver text-only rather than
		// risk emitting unescaped attacker content.
		slog.Error("digest: failed to render HTML body", "error", err)
		return sb.String(), ""
	}

	return sb.String(), hb.String()
}
