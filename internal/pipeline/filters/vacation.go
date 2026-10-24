package filters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
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
	// Only the envelope sender (RFC 5230 §4.2). A null one is a bounce or
	// another notice, and gets no reply (RFC 3834 §2): the From: header is
	// never a fallback, as it was when a bounce from postmaster@ got one.
	sender := strings.TrimSpace(strings.ToLower(email.Envelope.MailFrom))

	// ── 2. RFC 3834 suppression checks ─────────────────────────────
	if reason := f.shouldSuppress(ctx, sender, email); reason != "" {
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

		// Sent with a null reverse path (RFC 5230 §5.1, RFC 3834 §3.1.4), so a
		// reply that bounces or reaches another responder makes no more mail.
		// From: is still the mailbox, and it is signed as, and routed for, the
		// mailbox's domain.
		db.Create(&models.OutboundQueue{
			Sender:        "",
			SendingDomain: parts[1],
			Recipient:     sender,
			Domain:        senderDomain,
			RawMessage:    rawReply,
			Status:        "pending",
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
	// The incoming Message-ID reaches In-Reply-To/References; on the RESTMAIL path
	// it originates from the caller-supplied req.MessageID and is attacker-
	// influenced. Strip control characters so a CRLF cannot inject extra headers
	// (e.g. a Bcc:) into this outbound reply (issue #201). Subject also flows from
	// the mailbox config, so sanitize it for the same reason.
	inReplyTo = sanitizeHeaderValue(inReplyTo)
	subject = sanitizeHeaderValue(subject)

	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + rmail.GenerateMessageID(rmail.DomainFromAddress(from)) + "\r\n")
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
func (f *vacationFilter) shouldSuppress(ctx context.Context, sender string, email *pipeline.EmailJSON) string {
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

	// 3. Auto-Submitted header (RFC 3834 §2 / RFC 5230 §4.6). Any value other than
	//    "no" marks the message as auto-generated and MUST NOT be auto-replied to.
	//    The value is a keyword optionally followed by ";"-separated parameters
	//    (e.g. "auto-generated (from a filter)"), so compare only the keyword —
	//    this now also catches auto-notified and any future/parameterized keyword,
	//    not just the two exact strings matched before.
	if vals := f.rawHeader(email, "Auto-Submitted"); len(vals) > 0 {
		for _, v := range vals {
			keyword := autoSubmittedKeyword(v)
			if keyword != "" && keyword != "no" {
				return fmt.Sprintf("Auto-Submitted: %s", keyword)
			}
		}
	}

	// 3b. Backscatter guard: do not auto-reply to a sender whose SPF/DMARC failed.
	//     The reply would go to the (likely forged) envelope sender — classic
	//     backscatter. The signal is the verdicts the spf_check / dmarc_check
	//     filters recorded earlier in the pipeline.
	if reason := authFailureReason(ctx); reason != "" {
		return reason
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

// autoSubmittedKeyword returns the lower-cased leading keyword of an
// Auto-Submitted header value, dropping any ";"-separated parameters and
// trailing "(comment)" text (RFC 3834 §5). Returns "" for an empty value.
func autoSubmittedKeyword(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if i := strings.IndexAny(v, "; ("); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// authFailureReason returns a non-empty reason when spf_check or dmarc_check
// recorded an SPF or DMARC failure for the message, so the vacation responder
// can suppress a backscatter reply. It is deliberately conservative: an SPF
// fail or softfail of either identity, or a DMARC fail, is enough to hold the
// reply (fail-safe — a false positive only means "no auto-reply", never a lost
// message).
func authFailureReason(ctx context.Context) string {
	for _, r := range pipeline.AuthResultsFromContext(ctx).List() {
		switch {
		case r.Method == pipeline.AuthSPF && r.Result == "fail":
			return "sender failed SPF (backscatter guard)"
		case r.Method == pipeline.AuthSPF && r.Result == "softfail":
			return "sender soft-failed SPF (backscatter guard)"
		case r.Method == pipeline.AuthDMARC && r.Result == "fail":
			return "sender failed DMARC (backscatter guard)"
		}
	}
	return ""
}

// sanitizeHeaderValue strips CR and LF (and other C0 control characters except
// tab) from a value destined for a single header line, preventing header
// injection when the value is attacker-influenced (issue #201).
func sanitizeHeaderValue(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return -1
		}
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, v)
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
