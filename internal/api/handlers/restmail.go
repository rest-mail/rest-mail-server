package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/restmail/restmail/internal/api/respond"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// RestmailHandler implements the RESTMAIL server-to-server protocol endpoints.
// These are unauthenticated (like SMTP — any server can deliver to you).
// Authentication is via DKIM/SPF/DMARC verification, not API keys.
type RestmailHandler struct {
	db       *gorm.DB
	engine   *pipeline.Engine
	recorder traceRecorder
}

func NewRestmailHandler(db *gorm.DB, engine *pipeline.Engine, recorder traceRecorder) *RestmailHandler {
	return &RestmailHandler{db: db, engine: engine, recorder: recorder}
}

// recordTrace hands a MessageTrace to the async recorder when configured. Never
// blocks or errors — trace persistence must not delay or fail delivery.
func (h *RestmailHandler) recordTrace(t models.MessageTrace) {
	if h.recorder != nil {
		h.recorder.Record(t)
	}
}

// Capabilities returns the RESTMAIL server capabilities.
// GET /restmail/capabilities
func (h *RestmailHandler) Capabilities(w http.ResponseWriter, r *http.Request) {
	respond.Data(w, http.StatusOK, map[string]interface{}{
		"protocol": "RESTMAIL",
		"version":  "1.0",
		"features": []string{
			"delivery",
			"recipient-check",
		},
	})
}

// CheckMailbox verifies a recipient mailbox exists.
// GET /restmail/mailboxes?address=...
func (h *RestmailHandler) CheckMailbox(w http.ResponseWriter, r *http.Request) {
	address := r.URL.Query().Get("address")
	if address == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request", "address query parameter required")
		return
	}

	var mailbox models.Mailbox
	if err := h.db.Where("address = ? AND active = ?", address, true).First(&mailbox).Error; err != nil {
		respond.Data(w, http.StatusOK, map[string]interface{}{
			"exists": false,
		})
		return
	}

	respond.Data(w, http.StatusOK, map[string]interface{}{
		"exists":  true,
		"address": mailbox.Address,
	})
}

// Deliver receives a message from another RESTMAIL server.
// POST /restmail/messages
func (h *RestmailHandler) Deliver(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From       string          `json:"from"`
		To         []string        `json:"to"`
		Subject    string          `json:"subject"`
		Date       string          `json:"date"`
		BodyText   string          `json:"body_text"`
		BodyHTML   string          `json:"body_html"`
		MessageID  string          `json:"message_id"`
		InReplyTo  string          `json:"in_reply_to"`
		References string          `json:"references"`
		Headers    json.RawMessage `json:"headers"`
		// Fields sent by the RESTMAIL queue worker
		RawMessage string   `json:"raw_message"`
		Sender     string   `json:"sender"`
		Recipients []string `json:"recipients"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	// Accept sender/recipients as aliases for from/to
	if req.From == "" && req.Sender != "" {
		req.From = req.Sender
	}
	if len(req.To) == 0 && len(req.Recipients) > 0 {
		req.To = req.Recipients
	}

	// When raw_message is provided, parse RFC 2822 into structured fields
	if req.RawMessage != "" {
		parsed, err := rmime.Parse([]byte(req.RawMessage))
		if err != nil {
			slog.Warn("restmail: failed to parse raw_message", "error", err)
			// Fall through to use whatever structured fields were provided
		} else {
			if len(parsed.Headers.From) > 0 && req.From == "" {
				req.From = parsed.Headers.From[0].Address
			}
			if len(parsed.Headers.To) > 0 && len(req.To) == 0 {
				addrs := make([]string, len(parsed.Headers.To))
				for i, a := range parsed.Headers.To {
					addrs[i] = a.Address
				}
				req.To = addrs
			}
			if parsed.Headers.Subject != "" && req.Subject == "" {
				req.Subject = parsed.Headers.Subject
			}
			if parsed.Headers.Date != "" && req.Date == "" {
				req.Date = parsed.Headers.Date
			}
			if parsed.Headers.MessageID != "" && req.MessageID == "" {
				req.MessageID = parsed.Headers.MessageID
			}
			if parsed.Headers.InReplyTo != "" && req.InReplyTo == "" {
				req.InReplyTo = parsed.Headers.InReplyTo
			}
			if len(parsed.Headers.References) > 0 && req.References == "" {
				req.References = strings.Join(parsed.Headers.References, " ")
			}
			// Extract body text and HTML from parsed parts
			if req.BodyText == "" || req.BodyHTML == "" {
				text, html := extractBodyParts(parsed.Body)
				if req.BodyText == "" {
					req.BodyText = text
				}
				if req.BodyHTML == "" {
					req.BodyHTML = html
				}
			}
			// Preserve raw headers as JSON
			if req.Headers == nil && parsed.Headers.Raw != nil {
				req.Headers, _ = json.Marshal(parsed.Headers.Raw)
			}
		}
	}

	if req.MessageID == "" {
		req.MessageID = rmail.GenerateMessageID(rmail.DomainFromAddress(req.From))
	}

	if len(req.To) == 0 {
		respond.ValidationError(w, map[string]string{"to": "at least one recipient required"})
		return
	}

	var delivered []string
	var failed []string
	// Authentication-Results the inbound pipeline produced (dkim/spf/dmarc/arc),
	// prepended onto the stored raw message so the verdicts are visible — the
	// same surfacing the SMTP delivery path does.
	var authResults []string

	// Build pipeline EmailJSON from the request for inbound filtering
	emailJSON := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:      []pipeline.Address{{Address: req.From}},
			To:        func() []pipeline.Address { a := make([]pipeline.Address, len(req.To)); for i, r := range req.To { a[i] = pipeline.Address{Address: r} }; return a }(),
			Subject:   req.Subject,
			Date:      req.Date,
			MessageID: req.MessageID,
		},
		Body: pipeline.Body{
			ContentType: "text/plain",
			Content:     req.BodyText,
			Parts: []pipeline.Body{
				{ContentType: "text/plain", Content: req.BodyText},
				{ContentType: "text/html", Content: req.BodyHTML},
			},
		},
		Envelope: pipeline.Envelope{
			MailFrom:  req.From,
			RcptTo:    req.To,
			Direction: "inbound",
		},
	}

	// Thread the raw source through for DKIM verification (dkim_verify must
	// canonicalize the exact signed bytes, not the reconstructed EmailJSON).
	if req.RawMessage != "" {
		emailJSON.Metadata = map[string]string{"raw_message": req.RawMessage}
	}

	// Look up inbound pipeline for the recipient domain
	var domainName string
	if len(req.To) > 0 {
		if idx := strings.LastIndex(req.To[0], "@"); idx >= 0 {
			domainName = req.To[0][idx+1:]
		}
	}

	var pipelineCfg *pipeline.PipelineConfig
	if domainName != "" {
		var domain models.Domain
		if err := h.db.Where("name = ?", domainName).First(&domain).Error; err == nil {
			var dbPipeline models.Pipeline
			if err := h.db.Where("domain_id = ? AND direction = ? AND active = ?", domain.ID, "inbound", true).
				First(&dbPipeline).Error; err == nil {
				var filterConfigs []pipeline.FilterConfig
				if jsonErr := json.Unmarshal(dbPipeline.Filters, &filterConfigs); jsonErr == nil {
					pipelineCfg = &pipeline.PipelineConfig{
						ID:        dbPipeline.ID,
						DomainID:  dbPipeline.DomainID,
						Direction: dbPipeline.Direction,
						Filters:   filterConfigs,
						Active:    dbPipeline.Active,
					}
				}
			} else {
				pipelineCfg = pipeline.DefaultInboundPipeline(domain.ID)
			}
		}
	}

	// Run the inbound pipeline.
	// deliveredTrace, set when the pipeline permits delivery, is recorded after
	// the delivery loop so its message_id points at the first delivered row. A
	// RESTMAIL delivery has no TLS/IP envelope, so transport stays "".
	var deliveredTrace *traceInputs
	if pipelineCfg != nil && h.engine != nil {
		result, err := h.engine.Execute(context.Background(), pipelineCfg, emailJSON)
		if err != nil {
			slog.Error("restmail: pipeline error", "error", err)
			// Continue delivery on pipeline error (fail-open)
		} else {
			rmTrace := traceInputs{
				PipelineID:   pipelineCfg.ID,
				Direction:    "inbound",
				Result:       result,
				Envelope:     emailJSON.Envelope,
				RFCMessageID: req.MessageID,
			}

			switch result.FinalAction {
			case pipeline.ActionReject:
				rmTrace.Outcome = outcomeRejected
				h.recordTrace(buildTrace(rmTrace))
				respond.Error(w, http.StatusForbidden, "rejected", "Message rejected by policy")
				return
			case pipeline.ActionDiscard:
				rmTrace.Outcome = outcomeDiscarded
				h.recordTrace(buildTrace(rmTrace))
				respond.Data(w, http.StatusCreated, map[string]interface{}{
					"delivered": req.To,
					"failed":    []string{},
				})
				return
			case pipeline.ActionQuarantine:
				for _, rcpt := range req.To {
					var mailbox models.Mailbox
					if h.db.Where("address = ? AND active = ?", rcpt, true).First(&mailbox).Error == nil {
						preview := req.BodyText
						if len(preview) > 200 {
							preview = preview[:200]
						}
						h.db.Create(&models.Quarantine{
							MailboxID:        mailbox.ID,
							Sender:           req.From,
							Subject:          req.Subject,
							BodyPreview:      preview,
							QuarantineReason: "pipeline",
						})
					}
				}
				rmTrace.Outcome = outcomeQuarantined
				h.recordTrace(buildTrace(rmTrace))
				respond.Data(w, http.StatusCreated, map[string]interface{}{
					"delivered": []string{},
					"failed":    req.To,
				})
				return
			}
			// Fall-through (continue / any non-terminal action): the message will
			// be delivered — stash the trace to record post-loop with message_id.
			base := rmTrace
			deliveredTrace = &base
			// Carry the pipeline's Authentication-Results forward so the message
			// stored below records the dkim/spf/dmarc/arc verdicts (they are
			// otherwise computed and thrown away).
			if result.FinalEmail != nil {
				authResults = result.FinalEmail.Headers.Raw["Authentication-Results"]
			}
		}
	}

	// Store the original raw message (with the pipeline's Authentication-Results
	// prepended). Previously the stored message had no raw form at all, so
	// RESTMAIL-delivered mail lost its full MIME (attachments, headers) over
	// IMAP/POP3 and couldn't be re-verified.
	rawMessage := req.RawMessage
	if rawMessage != "" && len(authResults) > 0 {
		var b strings.Builder
		for _, ar := range authResults {
			b.WriteString("Authentication-Results: ")
			b.WriteString(ar)
			b.WriteString("\r\n")
		}
		rawMessage = b.String() + rawMessage
	}

	for _, rcpt := range req.To {
		var mailbox models.Mailbox
		if err := h.db.Where("address = ? AND active = ?", rcpt, true).First(&mailbox).Error; err != nil {
			failed = append(failed, rcpt)
			continue
		}

		// Check quota
		if mailbox.QuotaBytes > 0 && mailbox.QuotaUsedBytes >= mailbox.QuotaBytes {
			failed = append(failed, rcpt)
			continue
		}

		sizeBytes := len(req.Subject) + len(req.BodyText) + len(req.BodyHTML)

		threadID := req.MessageID
		if req.InReplyTo != "" {
			threadID = req.InReplyTo
		}

		recipientsTo, _ := json.Marshal([]map[string]string{{"address": rcpt}})

		msg := models.Message{
			MailboxID:    mailbox.ID,
			Folder:       "INBOX",
			MsgID:        req.MessageID,
			InReplyTo:    req.InReplyTo,
			References:   req.References,
			ThreadID:     threadID,
			Sender:       req.From,
			RecipientsTo: models.JSONB(recipientsTo),
			Subject:      req.Subject,
			BodyText:     req.BodyText,
			BodyHTML:     req.BodyHTML,
			Headers:      models.JSONB(req.Headers),
			RawMessage:   rawMessage,
			SizeBytes:    sizeBytes,
			RawSize:      len(rawMessage), // exact stored-raw octet count for IMAP/POP3 size reporting
		}

		if err := h.db.Create(&msg).Error; err != nil {
			failed = append(failed, rcpt)
			continue
		}

		// The trace links to the first delivered recipient row (one trace per
		// pipeline run; a RESTMAIL delivery may fan out to several mailboxes).
		if deliveredTrace != nil && deliveredTrace.MessageID == nil {
			mid := msg.ID
			deliveredTrace.MessageID = &mid
		}

		// Update quota
		h.db.Model(&mailbox).Update("quota_used_bytes", gorm.Expr("quota_used_bytes + ?", sizeBytes))
		h.db.Model(&models.QuotaUsage{}).Where("mailbox_id = ?", mailbox.ID).Updates(map[string]interface{}{
			"subject_bytes": gorm.Expr("subject_bytes + ?", len(req.Subject)),
			"body_bytes":    gorm.Expr("body_bytes + ?", len(req.BodyText)+len(req.BodyHTML)),
			"message_count": gorm.Expr("message_count + 1"),
		})

		delivered = append(delivered, rcpt)
	}

	// Record the delivered trace once, after the fan-out loop. message_id points
	// at the first delivered row (nil if every recipient failed); the outcome is
	// delivered because the pipeline permitted delivery.
	if deliveredTrace != nil {
		deliveredTrace.Outcome = outcomeDelivered
		h.recordTrace(buildTrace(*deliveredTrace))
	}

	status := http.StatusCreated
	if len(delivered) == 0 {
		status = http.StatusUnprocessableEntity
	}

	respond.Data(w, status, map[string]interface{}{
		"delivered": delivered,
		"failed":    failed,
	})
}

// mediaType returns the lower-cased media type of a Content-Type value with any
// parameters (charset, boundary, ...) stripped: "text/plain; charset=utf-8" ->
// "text/plain".
func mediaType(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}

// extractBodyParts walks a potentially nested Body structure and returns the
// first text/plain and text/html content found.
//
// The comparison is on the media type alone: a part's ContentType frequently
// carries parameters (e.g. "text/plain; charset=utf-8", as the outbound
// pipeline emits), and an exact-string match against "text/plain" would miss
// those and silently drop the body a transform produced.
func extractBodyParts(body pipeline.Body) (text, html string) {
	switch mediaType(body.ContentType) {
	case "text/plain":
		if body.Content != "" {
			text = body.Content
		}
	case "text/html":
		if body.Content != "" {
			html = body.Content
		}
	}
	for _, part := range body.Parts {
		t, h := extractBodyParts(part)
		if text == "" {
			text = t
		}
		if html == "" {
			html = h
		}
	}
	return
}
