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
	db     *gorm.DB
	engine *pipeline.Engine
}

func NewRestmailHandler(db *gorm.DB, engine *pipeline.Engine) *RestmailHandler {
	return &RestmailHandler{db: db, engine: engine}
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

	// Build pipeline EmailJSON from the request for inbound filtering
	emailJSON := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:      []pipeline.Address{{Address: req.From}},
			To:        func() []pipeline.Address { a := make([]pipeline.Address, len(req.To)); for i, r := range req.To { a[i] = pipeline.Address{Address: r} }; return a }(),
			Subject:   req.Subject,
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

	// Run the inbound pipeline
	if pipelineCfg != nil && h.engine != nil {
		result, err := h.engine.Execute(context.Background(), pipelineCfg, emailJSON)
		if err != nil {
			slog.Error("restmail: pipeline error", "error", err)
			// Continue delivery on pipeline error (fail-open)
		} else {
			// Persist pipeline execution log
			stepsJSON, _ := json.Marshal(result.Steps)
			h.db.Create(&models.PipelineLog{
				PipelineID: pipelineCfg.ID,
				Direction:  "inbound",
				Action:     string(result.FinalAction),
				Steps:      stepsJSON,
				DurationMS: result.Duration.Milliseconds(),
			})

			switch result.FinalAction {
			case pipeline.ActionReject:
				respond.Error(w, http.StatusForbidden, "rejected", "Message rejected by policy")
				return
			case pipeline.ActionDiscard:
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
				respond.Data(w, http.StatusCreated, map[string]interface{}{
					"delivered": []string{},
					"failed":    req.To,
				})
				return
			}
			if result.FinalEmail != nil {
				emailJSON = result.FinalEmail
			}
		}
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
			SizeBytes:    sizeBytes,
		}

		if err := h.db.Create(&msg).Error; err != nil {
			failed = append(failed, rcpt)
			continue
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

	status := http.StatusCreated
	if len(delivered) == 0 {
		status = http.StatusUnprocessableEntity
	}

	respond.Data(w, status, map[string]interface{}{
		"delivered": delivered,
		"failed":    failed,
	})
}

// extractBodyParts walks a potentially nested Body structure and returns the
// first text/plain and text/html content found.
func extractBodyParts(body pipeline.Body) (text, html string) {
	if body.ContentType == "text/plain" && body.Content != "" {
		text = body.Content
	}
	if body.ContentType == "text/html" && body.Content != "" {
		html = body.Content
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
