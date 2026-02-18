package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// vacationConfig holds the per-step JSON configuration for the vacation filter.
// Only the dedup window is configurable per-step; Subject and Body come from
// the vacation_configs DB table.
type vacationConfig struct {
	// Days is the deduplication window: do not re-send a vacation reply to
	// the same sender within this many days.  Defaults to 7 (RFC 5230).
	Days int `json:"days"`
}

// vacationFilter implements pipeline.Filter for vacation / out-of-office
// auto-replies. It reads vacation configuration from the vacation_configs DB
// table and queues actual replies via outbound_queue.
type vacationFilter struct {
	db  *gorm.DB
	cfg vacationConfig
}

// NewVacation returns a FilterFactory that closes over the provided *gorm.DB.
func NewVacation(db *gorm.DB) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		cfg := vacationConfig{Days: 7}
		if len(config) > 0 {
			if err := json.Unmarshal(config, &cfg); err != nil {
				return nil, fmt.Errorf("vacation: bad config: %w", err)
			}
		}
		if cfg.Days < 1 {
			cfg.Days = 1
		}
		return &vacationFilter{db: db, cfg: cfg}, nil
	}
}

func (f *vacationFilter) Name() string             { return "vacation" }
func (f *vacationFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }

func (f *vacationFilter) Execute(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	db := pipeline.DBFromContext(ctx)
	if db == nil {
		db = f.db // fallback
	}

	// ── 1. Determine the sender address to reply to ────────────────
	sender := email.Envelope.MailFrom
	if sender == "" && len(email.Headers.From) > 0 {
		sender = email.Headers.From[0].Address
	}
	sender = strings.TrimSpace(strings.ToLower(sender))

	// ── 2. RFC 3834 suppression checks ─────────────────────────────
	if reason := f.shouldSuppress(sender, email); reason != "" {
		return f.skip(reason), nil
	}

	// ── 3. Process each recipient — look up their mailbox, vacation
	//       config, and queue a reply if warranted. ─────────────────
	replied := false
	now := time.Now()

	for _, rcpt := range email.Envelope.RcptTo {
		parts := strings.SplitN(rcpt, "@", 2)
		if len(parts) != 2 {
			continue
		}

		// Look up the mailbox ID for this recipient.
		var mailboxID uint
		err := db.Table("mailboxes").
			Joins("JOIN domains ON domains.id = mailboxes.domain_id").
			Where("mailboxes.local_part = ? AND domains.name = ? AND mailboxes.active = ?",
				parts[0], parts[1], true).
			Pluck("mailboxes.id", &mailboxID).Error
		if err != nil || mailboxID == 0 {
			continue // mailbox not found or inactive — skip
		}

		// Look up vacation config from DB.
		var vacCfg models.VacationConfig
		if err := db.Where("mailbox_id = ? AND enabled = ?", mailboxID, true).First(&vacCfg).Error; err != nil {
			continue // no vacation config or not enabled
		}

		// Check date window.
		if vacCfg.StartDate != nil && now.Before(*vacCfg.StartDate) {
			continue
		}
		if vacCfg.EndDate != nil && now.After(*vacCfg.EndDate) {
			continue
		}

		// ── 3a. Deduplication ──────────────────────────────────────
		cutoff := now.Add(-time.Duration(f.cfg.Days) * 24 * time.Hour)
		var recentCount int64
		db.Table("vacation_responses").
			Where("mailbox_id = ? AND sender = ? AND responded_at > ?",
				mailboxID, sender, cutoff).
			Count(&recentCount)

		if recentCount > 0 {
			continue // already responded within the window
		}

		// ── 3b. Record the response for dedup ─────────────────────
		db.Table("vacation_responses").Create(map[string]interface{}{
			"mailbox_id":   mailboxID,
			"sender":       sender,
			"responded_at": now,
		})

		// ── 3c. Queue the actual reply via outbound_queue ─────────
		senderDomain := parts[1]
		if idx := strings.LastIndex(sender, "@"); idx >= 0 {
			senderDomain = sender[idx+1:]
		}

		rawReply := buildVacationReply(rcpt, sender, vacCfg.Subject, vacCfg.Body, email.Headers.MessageID)

		db.Create(&models.OutboundQueue{
			Sender:     rcpt,
			Recipient:  sender,
			Domain:     senderDomain,
			RawMessage: rawReply,
			Status:     "pending",
		})

		replied = true
	}

	if !replied {
		return f.skip("no eligible recipients or already responded"), nil
	}

	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "vacation",
			Result: "queued",
			Detail: fmt.Sprintf("vacation auto-reply queued for sender %s", sender),
		},
	}, nil
}

// buildVacationReply constructs an RFC 2822 vacation auto-reply with RFC 3834 headers.
func buildVacationReply(from, to, subject, body, inReplyTo string) string {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	if inReplyTo != "" {
		b.WriteString("In-Reply-To: " + inReplyTo + "\r\n")
		b.WriteString("References: " + inReplyTo + "\r\n")
	}
	b.WriteString("Auto-Submitted: auto-replied\r\n")
	b.WriteString("Precedence: bulk\r\n")
	b.WriteString("X-Auto-Response-Suppress: All\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body + "\r\n")
	return b.String()
}

// ── RFC 3834 suppression logic ──────────────────────────────────────────

// shouldSuppress returns a non-empty reason string if we must NOT auto-reply.
func (f *vacationFilter) shouldSuppress(sender string, email *pipeline.EmailJSON) string {
	// 1. Empty / null sender (bounce messages).
	if sender == "" || sender == "<>" {
		return "empty or null sender"
	}

	// 2. MAILER-DAEMON, noreply, no-reply addresses.
	lc := strings.ToLower(sender)
	localPart := lc
	if idx := strings.Index(lc, "@"); idx >= 0 {
		localPart = lc[:idx]
	}
	switch {
	case strings.HasPrefix(localPart, "mailer-daemon"):
		return "sender is MAILER-DAEMON"
	case strings.Contains(lc, "noreply"):
		return "sender contains noreply"
	case strings.Contains(lc, "no-reply"):
		return "sender contains no-reply"
	}

	// 3. Auto-Submitted header (RFC 3834 section 2).
	if vals := f.rawHeader(email, "Auto-Submitted"); len(vals) > 0 {
		for _, v := range vals {
			v = strings.TrimSpace(strings.ToLower(v))
			if v == "auto-replied" || v == "auto-generated" {
				return fmt.Sprintf("Auto-Submitted: %s", v)
			}
		}
	}

	// 4. Precedence header.
	if vals := f.rawHeader(email, "Precedence"); len(vals) > 0 {
		for _, v := range vals {
			v = strings.TrimSpace(strings.ToLower(v))
			if v == "bulk" || v == "list" || v == "junk" {
				return fmt.Sprintf("Precedence: %s", v)
			}
		}
	}

	// 5. Mailing-list indicators.
	for _, hdr := range []string{"List-Id", "List-Unsubscribe", "X-Mailing-List"} {
		if vals := f.rawHeader(email, hdr); len(vals) > 0 {
			return fmt.Sprintf("mailing-list header present: %s", hdr)
		}
	}

	return ""
}

// rawHeader performs a case-insensitive lookup in the Raw headers map.
func (f *vacationFilter) rawHeader(email *pipeline.EmailJSON, name string) []string {
	if email.Headers.Raw == nil {
		return nil
	}
	target := strings.ToLower(name)
	for k, v := range email.Headers.Raw {
		if strings.ToLower(k) == target {
			return v
		}
	}
	return nil
}

// skip returns a continue result with the given detail.
func (f *vacationFilter) skip(detail string) *pipeline.FilterResult {
	return &pipeline.FilterResult{
		Type:   pipeline.FilterTypeAction,
		Action: pipeline.ActionContinue,
		Log: pipeline.FilterLog{
			Filter: "vacation",
			Result: "skip",
			Detail: detail,
		},
	}
}
