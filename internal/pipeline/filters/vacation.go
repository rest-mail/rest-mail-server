package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// vacationConfig holds the per-filter JSON configuration for the vacation
// auto-reply filter. It is stored in the pipeline's FilterConfig.Config field.
type vacationConfig struct {
	// Days is the deduplication window: do not re-send a vacation reply to
	// the same sender within this many days.  Defaults to 7 (RFC 5230).
	Days int `json:"days"`

	// Subject is the Subject header of the auto-reply message.
	Subject string `json:"subject"`

	// Body is the plain-text body of the auto-reply message.
	Body string `json:"body"`
}

// vacationFilter implements pipeline.Filter for vacation / out-of-office
// auto-replies.  It is an *action* filter that always returns ActionContinue
// (vacation processing must never block delivery).  When an auto-reply is
// warranted, it sets metadata keys on the email so that a downstream delivery
// agent can queue the actual reply.
type vacationFilter struct {
	db  *gorm.DB
	cfg vacationConfig
}

// NewVacation returns a FilterFactory that closes over the provided *gorm.DB.
// The factory itself parses the per-step JSON config each time a filter
// instance is created.
func NewVacation(db *gorm.DB) pipeline.FilterFactory {
	return func(config []byte) (pipeline.Filter, error) {
		cfg := vacationConfig{
			Days:    7,
			Subject: "Out of Office",
			Body:    "I am currently out of the office and will respond when I return.",
		}
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

func (f *vacationFilter) Name() string               { return "vacation" }
func (f *vacationFilter) Type() pipeline.FilterType   { return pipeline.FilterTypeAction }

func (f *vacationFilter) Execute(_ context.Context, email *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
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

	// ── 3. Process each recipient — look up their mailbox and check
	//       vacation state. ──────────────────────────────────────────
	replied := false
	for _, rcpt := range email.Envelope.RcptTo {
		parts := strings.SplitN(rcpt, "@", 2)
		if len(parts) != 2 {
			continue
		}

		// Look up the mailbox ID for this recipient.
		var mailboxID uint
		err := f.db.Table("mailboxes").
			Joins("JOIN domains ON domains.id = mailboxes.domain_id").
			Where("mailboxes.local_part = ? AND domains.name = ? AND mailboxes.active = ?",
				parts[0], parts[1], true).
			Pluck("mailboxes.id", &mailboxID).Error
		if err != nil || mailboxID == 0 {
			continue // mailbox not found or inactive — skip
		}

		// ── 3a. Deduplication: have we already replied to this sender
		//        for this mailbox within the configured window? ──────
		cutoff := time.Now().Add(-time.Duration(f.cfg.Days) * 24 * time.Hour)
		var recentCount int64
		f.db.Table("vacation_responses").
			Where("mailbox_id = ? AND sender = ? AND responded_at > ?",
				mailboxID, sender, cutoff).
			Count(&recentCount)

		if recentCount > 0 {
			continue // already responded within the window
		}

		// ── 3b. Record the response so we do not send again. ───────
		now := time.Now()
		f.db.Table("vacation_responses").Create(map[string]interface{}{
			"mailbox_id":   mailboxID,
			"sender":       sender,
			"responded_at": now,
		})

		replied = true
	}

	if !replied {
		return f.skip("no eligible recipients or already responded"), nil
	}

	// ── 4. Mark the email metadata so downstream can queue the reply.
	if email.Metadata == nil {
		email.Metadata = make(map[string]string)
	}
	email.Metadata["vacation_reply"] = "true"
	email.Metadata["vacation_subject"] = f.cfg.Subject
	email.Metadata["vacation_body"] = f.cfg.Body

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
