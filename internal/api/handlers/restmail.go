package handlers

import (
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
// There is no API key (like SMTP, any server may connect), but the delivery
// endpoint is authenticated per-message (OSI-3, see restmailDeliverAuth): a
// delivery must come from a trusted peer network or carry a DKIM signature that
// aligns with its From domain, so a spoofed-From injection into a local mailbox
// is refused rather than silently accepted.
type RestmailHandler struct {
	db          *gorm.DB
	engine      *pipeline.Engine
	recorder    traceRecorder
	tarpit      *negLookupTarpit
	deliverAuth *restmailDeliverAuth
}

func NewRestmailHandler(db *gorm.DB, engine *pipeline.Engine, recorder traceRecorder, tarpitCfg RestmailTarpitConfig, authCfg RestmailDeliverAuthConfig) *RestmailHandler {
	return &RestmailHandler{
		db:          db,
		engine:      engine,
		recorder:    recorder,
		tarpit:      newNegLookupTarpit(tarpitCfg),
		deliverAuth: newRestmailDeliverAuth(authCfg),
	}
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
		// Negative lookup: tarpit the source to throttle recipient enumeration
		// (OSI-1). The delay escalates per source and is capped; a positive
		// lookup (below) skips it and returns promptly. Keyed on the request
		// context so a client disconnect aborts the sleep.
		h.tarpit.delay(r.Context(), restmailClientIP(r))
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

	// When raw_message is provided, parse RFC 2822 into structured fields. The
	// parsed form is retained (not discarded) so it can seed the pipeline message
	// below with its real headers, including Headers.Raw.
	var parsed *pipeline.EmailJSON
	if req.RawMessage != "" {
		p, err := rmime.Parse([]byte(req.RawMessage))
		if err != nil {
			slog.Warn("restmail: failed to parse raw_message", "error", err)
			// Fall through to use whatever structured fields were provided
		} else {
			parsed = p
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

	// OSI-3: authenticate the delivery before accepting it. The RESTMAIL delivery
	// endpoint is unauthenticated by protocol (like SMTP, any server may connect),
	// so without this gate any host could POST a spoofed-From message straight
	// into a local mailbox (BEC/CEO-fraud). Trusted peers and DKIM-aligned mail
	// pass; an unauthenticated delivery claiming a locally-hosted From domain
	// (the internal-spoofing vector) is refused before any pipeline or storage.
	if h.deliverAuth != nil && h.deliverAuth.enabled {
		clientIP := restmailClientIP(r)
		fromDomain := rmail.DomainFromAddress(req.From)
		fromLocal := false
		if fromDomain != "" {
			var d models.Domain
			if h.db.Where("name = ?", fromDomain).First(&d).Error == nil {
				fromLocal = true
			}
		}
		// DKIM verification does DNS lookups, so only pay for it when the decision
		// depends on it: a trusted peer is already accepted, and a non-strict
		// external sender is accepted regardless of signature.
		aligned := false
		if !h.deliverAuth.trusted(clientIP) && (h.deliverAuth.strict || fromLocal) {
			aligned = dkimAlignedWith(r.Context(), req.RawMessage, fromDomain)
		}
		if ok, reason := h.deliverAuth.authorize(clientIP, fromDomain, fromLocal, aligned); !ok {
			slog.Warn("restmail: delivery refused by delivery-auth gate (OSI-3)",
				"reason", reason,
				"from_domain", fromDomain,
				"client_ip", clientIP,
				"dkim_aligned", aligned,
			)
			respond.Error(w, http.StatusForbidden, "unauthorized", "Delivery not authenticated")
			return
		}
	}

	var delivered []string
	var failed []string
	// Authentication-Results the inbound pipeline produced (dkim/spf/dmarc/arc),
	// prepended onto the stored raw message so the verdicts are visible — the
	// same surfacing the SMTP delivery path does.
	var authResults []string

	// Build the pipeline EmailJSON for inbound filtering. The parsed raw message
	// (with its real headers, including Headers.Raw) seeds the message so
	// header-dependent filters see the actual headers, mirroring the SMTP inbound
	// path; the structured request fields fill any gaps.
	emailJSON := buildInboundEmail(parsed, req.From, req.To, req.Subject, req.Date, req.MessageID, req.BodyText, req.BodyHTML, req.RawMessage, restmailClientIP(r))

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
	// suppressLocalKeep is set when a bare Sieve `redirect` forwarded the message
	// and cancelled the implicit keep (RFC 5228 §4.2): the message is forwarded but
	// NOT also stored to a local mailbox.
	suppressLocalKeep := false
	if pipelineCfg != nil && h.engine != nil {
		// Use the request context so a client disconnect / server shutdown cancels
		// the pipeline. Combined with the engine's per-filter timeout backstop, a
		// hung filter on this path is bounded even though there is no outer
		// delivery deadline here.
		result, err := h.engine.Execute(r.Context(), pipelineCfg, emailJSON)
		if err != nil {
			// Fail-CLOSED on a pipeline error (OSI-18): temp-fail so the peer
			// retries rather than delivering mail that skipped inbound filtering.
			slog.Error("restmail: pipeline error, deferring delivery", "error", err)
			respond.Error(w, http.StatusServiceUnavailable, "deferred", "Message deferred, retry later")
			return
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
			case pipeline.ActionDefer:
				// Fail-closed temp-fail (OSI-18): a deferred pipeline outcome (incl. a
				// filter that failed to instantiate/execute) must not be delivered —
				// the peer retries.
				rmTrace.Outcome = outcomeDeferred
				h.recordTrace(buildTrace(rmTrace))
				respond.Error(w, http.StatusServiceUnavailable, "deferred", "Message deferred, retry later")
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
					if mailbox, _, ok := h.resolveRecipientMailbox(rcpt); ok {
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

			// Sieve `redirect`: forward the ORIGINAL message onward to each recorded
			// target via the outbound queue (the same path SMTP submission and
			// vacation replies use). A bare redirect that cancelled the implicit keep
			// also suppresses the local copy below — but only when a forward was
			// actually enqueued, so a redirect that cannot be honoured never loses
			// the message.
			forwarded := 0
			for _, fwd := range buildRedirectForwards(result.FinalEmail, req.RawMessage, req.From, req.To) {
				row := fwd
				if err := h.db.Create(&row).Error; err != nil {
					slog.Error("restmail: failed to enqueue sieve redirect",
						"recipient", row.Recipient, "error", err)
					continue
				}
				forwarded++
				slog.Info("restmail: sieve redirect queued", "recipient", row.Recipient)
			}
			suppressLocalKeep = forwarded > 0 && redirectSuppressesKeep(result.FinalEmail)
		}
	}

	// A bare redirect forwarded the message with no local copy (RFC 5228 §4.2):
	// record the delivered trace and respond without storing it to any mailbox.
	if suppressLocalKeep {
		if deliveredTrace != nil {
			deliveredTrace.Outcome = outcomeDelivered
			h.recordTrace(buildTrace(*deliveredTrace))
		}
		respond.Data(w, http.StatusCreated, map[string]interface{}{
			"delivered": req.To,
			"failed":    []string{},
		})
		return
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
		mailbox, deliveredTo, ok := h.resolveRecipientMailbox(rcpt)
		if !ok {
			failed = append(failed, rcpt)
			continue
		}

		// Check quota
		if mailbox.QuotaBytes > 0 && mailbox.QuotaUsedBytes >= mailbox.QuotaBytes {
			failed = append(failed, rcpt)
			continue
		}

		// RFC 5233: when the recipient was resolved by stripping a "+detail" tag
		// down to the base mailbox, record the original recipient in a
		// Delivered-To header so downstream Sieve rules can still match the tag.
		rcptRaw := rawMessage
		if deliveredTo != "" && rcptRaw != "" {
			rcptRaw = "Delivered-To: " + deliveredTo + "\r\n" + rcptRaw
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
			RawMessage:   rcptRaw,
			SizeBytes:    sizeBytes,
			RawSize:      len(rcptRaw), // exact stored-raw octet count for IMAP/POP3 size reporting
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

// buildInboundEmail assembles the pipeline EmailJSON for a RESTMAIL inbound
// delivery, mirroring the SMTP inbound path (deliverToLocal).
//
// When the raw message parsed, that parsed form is the base — crucially it
// carries Headers.Raw, so header-dependent filters see the real headers rather
// than an empty set: arc_verify, header_validate, dmarc_check (which reads the
// local Authentication-Results the spf/dkim filters append), and vacation's RFC
// 3834 auto-reply suppression. Building the message from scratch (as this path
// previously did) left Headers.Raw nil and blinded those filters. The structured
// request fields fill any header the parse left empty, and are the sole source
// when there was no parseable raw message (parsed == nil).
//
// The SMTP envelope is not part of the message body: it is set from the request,
// with clientIP the connecting RESTMAIL peer so spf_check/dmarc_check have a
// genuine local verdict to work from rather than "none".
func buildInboundEmail(parsed *pipeline.EmailJSON, from string, to []string, subject, date, messageID, bodyText, bodyHTML, rawMessage, clientIP string) *pipeline.EmailJSON {
	toAddrs := make([]pipeline.Address, len(to))
	for i, rcpt := range to {
		toAddrs[i] = pipeline.Address{Address: rcpt}
	}

	emailJSON := parsed
	if emailJSON == nil {
		emailJSON = &pipeline.EmailJSON{
			Headers: pipeline.Headers{
				Subject:   subject,
				Date:      date,
				MessageID: messageID,
			},
			Body: pipeline.Body{
				ContentType: "text/plain",
				Content:     bodyText,
				Parts: []pipeline.Body{
					{ContentType: "text/plain", Content: bodyText},
					{ContentType: "text/html", Content: bodyHTML},
				},
			},
		}
	}

	emailJSON.Envelope = pipeline.Envelope{
		MailFrom:  from,
		RcptTo:    to,
		ClientIP:  clientIP,
		Direction: "inbound",
	}

	// Fill any header the parse didn't populate from the structured request fields.
	if len(emailJSON.Headers.From) == 0 {
		emailJSON.Headers.From = []pipeline.Address{{Address: from}}
	}
	if len(emailJSON.Headers.To) == 0 {
		emailJSON.Headers.To = toAddrs
	}
	if emailJSON.Headers.Subject == "" {
		emailJSON.Headers.Subject = subject
	}
	if emailJSON.Headers.Date == "" {
		emailJSON.Headers.Date = date
	}
	if emailJSON.Headers.MessageID == "" {
		emailJSON.Headers.MessageID = messageID
	}

	// Thread the raw source through for DKIM verification (dkim_verify must
	// canonicalize the exact signed bytes, not the reconstructed EmailJSON).
	if rawMessage != "" {
		if emailJSON.Metadata == nil {
			emailJSON.Metadata = make(map[string]string)
		}
		emailJSON.Metadata["raw_message"] = rawMessage
	}

	return emailJSON
}

// resolveRecipientMailbox resolves an envelope recipient to a deliverable local
// mailbox, applying RFC 5233 subaddressing ("plus addressing"). An exact
// address match always wins. When none exists and the local part carries a
// "+detail" tag, it retries against the base address — but only when no explicit
// alias claims the full "+detail" address (an explicit alias wins over the base
// fallback). deliveredTo is the original recipient when a "+detail" tag was
// stripped (so the caller can record a Delivered-To header preserving the tag
// for downstream Sieve filters), and "" otherwise.
func (h *RestmailHandler) resolveRecipientMailbox(rcpt string) (mailbox models.Mailbox, deliveredTo string, ok bool) {
	if err := h.db.Where("address = ? AND active = ?", rcpt, true).First(&mailbox).Error; err == nil {
		return mailbox, "", true
	}

	base, _, tagged := rmail.StripSubaddress(rcpt)
	if !tagged {
		return models.Mailbox{}, "", false
	}

	// An explicit alias for the full "+detail" address claims it — do not fall
	// back to the base mailbox (explicit alias wins over subaddressing).
	var aliasCount int64
	h.db.Model(&models.Alias{}).Where("source_address = ?", rcpt).Count(&aliasCount)
	if aliasCount > 0 {
		return models.Mailbox{}, "", false
	}

	if err := h.db.Where("address = ? AND active = ?", base, true).First(&mailbox).Error; err == nil {
		return mailbox, rcpt, true
	}
	return models.Mailbox{}, "", false
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
