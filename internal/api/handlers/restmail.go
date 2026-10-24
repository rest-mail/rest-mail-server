package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
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

// RestmailCapabilities is what a RESTMAIL server speaks: the protocol, its version and the
// features it offers. Its fields are in the order the map it replaced was written in.
type RestmailCapabilities struct {
	Features []string `json:"features"`
	Protocol string   `json:"protocol"`
	Version  string   `json:"version"`
}

func (h *RestmailHandler) capabilities(*http.Request, struct{}) (RestmailCapabilities, error) {
	return RestmailCapabilities{
		Features: []string{"delivery", "recipient-check"},
		Protocol: "RESTMAIL",
		Version:  "1.0",
	}, nil
}

// RestmailMailboxCheck answers whether a recipient exists, and if it does, as which address.
type RestmailMailboxCheck struct {
	Address string `json:"address,omitempty"`
	Exists  bool   `json:"exists"`
}

type restmailMailboxQuery struct {
	Address string `query:"address,required" minLength:"1" message:"address query parameter required" missing:"address query parameter required"`
}

// checkMailbox verifies a recipient mailbox exists.
func (h *RestmailHandler) checkMailbox(r *http.Request, in restmailMailboxQuery) (RestmailMailboxCheck, error) {
	var mailbox models.Mailbox
	if err := repositories.FindActiveMailboxByAddress(h.db, in.Address, &mailbox); err != nil {
		// Negative lookup: tarpit the source to throttle recipient enumeration
		// (OSI-1). The delay escalates per source and is capped; a positive
		// lookup (below) skips it and returns promptly. Keyed on the request
		// context so a client disconnect aborts the sleep.
		h.tarpit.delay(r.Context(), restmailClientIP(r))
		return RestmailMailboxCheck{Exists: false}, nil
	}
	return RestmailMailboxCheck{Exists: true, Address: mailbox.Address}, nil
}

type restmailDeliverInput struct {
	Body restmailDeliverBody `body:""`
}

// restmailDeliverBody is a message another RESTMAIL server delivers. Its recipients come from
// to, else recipients, else the To header of raw_message, so none of the three is required
// on its own; sender and recipients are what the RESTMAIL queue worker sends for from and to.
type restmailDeliverBody struct {
	From       string                     `json:"from,omitempty"`
	To         *[]string                  `json:"to,omitempty"`
	Subject    string                     `json:"subject,omitempty"`
	Date       string                     `json:"date,omitempty"`
	BodyText   string                     `json:"body_text,omitempty"`
	BodyHTML   string                     `json:"body_html,omitempty"`
	MessageID  string                     `json:"message_id,omitempty"`
	InReplyTo  string                     `json:"in_reply_to,omitempty"`
	References string                     `json:"references,omitempty"`
	Headers    map[string]json.RawMessage `json:"headers,omitempty"`
	RawMessage string                     `json:"raw_message,omitempty"`
	Sender     string                     `json:"sender,omitempty"`
	Recipients *[]string                  `json:"recipients,omitempty"`
}

// RestmailDelivery is the recipients a delivery was stored for, and those it was not.
type RestmailDelivery struct {
	Delivered []string `json:"delivered"`
	Failed    []string `json:"failed"`
}

// deliver receives a message from another RESTMAIL server. When no recipient takes it, the
// answer is 422 undeliverable: every recipient failed.
func (h *RestmailHandler) deliver(r *http.Request, in restmailDeliverInput) (RestmailDelivery, error) {
	req := in.Body
	var to []string
	if req.To != nil {
		to = *req.To
	}
	// Accept sender/recipients as aliases for from/to
	if req.From == "" && req.Sender != "" {
		req.From = req.Sender
	}
	// The envelope sender is what the peer sent as from (or sender), and only
	// that: empty is the null reverse path of a bounce or another notice. The
	// From: header below may fill req.From for display and checks, but never the
	// envelope, or a bounce would look like personal mail and get a vacation
	// reply (RFC 3834 §2, #716).
	envelopeFrom := req.From
	if len(to) == 0 && req.Recipients != nil && len(*req.Recipients) > 0 {
		to = *req.Recipients
	}
	var headers json.RawMessage
	if req.Headers != nil {
		headers, _ = json.Marshal(req.Headers)
	}

	// When raw_message is provided, parse RFC 2822 into structured fields. The
	// parsed form is retained (not discarded) so it can seed the pipeline message
	// below with its real headers, including Headers.Raw.
	//
	// Authentication-Results claiming this server's authserv-id did not come
	// from this server: they are removed first, before anything parses,
	// verifies or stores the message (RFC 8601 §5).
	req.RawMessage = rmime.StripAuthResults(req.RawMessage, pipeline.AuthServID)
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
			if len(parsed.Headers.To) > 0 && len(to) == 0 {
				addrs := make([]string, len(parsed.Headers.To))
				for i, a := range parsed.Headers.To {
					addrs[i] = a.Address
				}
				to = addrs
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
			if headers == nil && parsed.Headers.Raw != nil {
				headers, _ = json.Marshal(parsed.Headers.Raw)
			}
		}
	}

	if req.MessageID == "" {
		req.MessageID = rmail.GenerateMessageID(rmail.DomainFromAddress(req.From))
	}

	if len(to) == 0 {
		return RestmailDelivery{}, endpoint.Invalid(map[string]string{"to": "at least one recipient required"})
	}

	// OSI-3: authenticate the delivery before accepting it. The RESTMAIL delivery
	// endpoint is unauthenticated by protocol (like SMTP, any server may connect),
	// so without this gate any host could POST a spoofed-From message straight
	// into a local mailbox (BEC/CEO-fraud). Trusted peers and DKIM-aligned mail
	// pass; an unauthenticated delivery claiming a locally-hosted From domain
	// (the internal-spoofing vector) is refused before any pipeline or storage.
	clientIP := restmailClientIP(r)
	fromDomain := rmail.DomainFromAddress(req.From)
	fromLocal := false
	if fromDomain != "" {
		// Hosted domains are stored as lowercase A-labels. Use the same IDNA
		// form for the spoofing gate and DKIM alignment so a Unicode spelling
		// cannot make a hosted From domain look external.
		if ascii, err := rmail.DomainASCII(fromDomain); err == nil {
			fromDomain = strings.TrimSuffix(ascii, ".")
			var d models.Domain
			fromLocal = h.db.Where("name = ?", fromDomain).First(&d).Error == nil
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
		return RestmailDelivery{}, endpoint.Refuse(http.StatusForbidden, "unauthorized", "Delivery not authenticated")
	}

	delivered := []string{}
	failed := []string{}
	// The inbound pipeline's verdicts (spf/dkim/arc/dmarc), prepended onto the
	// stored raw message as Authentication-Results so they are visible — the
	// same surfacing the SMTP delivery path does.
	var authResults []pipeline.AuthResult

	// Build the pipeline EmailJSON for inbound filtering. The parsed raw message
	// (with its real headers, including Headers.Raw) seeds the message so
	// header-dependent filters see the actual headers, mirroring the SMTP inbound
	// path; the structured request fields fill any gaps.
	emailJSON := buildInboundEmail(parsed, envelopeFrom, req.From, to, req.Subject, req.Date, req.MessageID, req.BodyText, req.BodyHTML, req.RawMessage, restmailClientIP(r))

	// Look up inbound pipeline for the recipient domain
	var domainName string
	if len(to) > 0 {
		// Use the same identity policy as delivery, including whitespace, so an
		// equivalent spelling cannot bypass the hosted inbound pipeline.
		if canonical, err := rmail.CanonicalMailboxAddress(to[0]); err == nil {
			_, domainName, _ = strings.Cut(canonical, "@")
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
	// sharedEmail is the message as the domain's pipeline left it, which each recipient's own
	// Sieve script starts from (#787).
	sharedEmail := emailJSON
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
			return RestmailDelivery{}, endpoint.Refuse(http.StatusServiceUnavailable, "deferred", "Message deferred, retry later")
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
				return RestmailDelivery{}, endpoint.Refuse(http.StatusForbidden, "rejected", "Message rejected by policy")
			case pipeline.ActionDefer:
				// Fail-closed temp-fail (OSI-18): a deferred pipeline outcome (incl. a
				// filter that failed to instantiate/execute) must not be delivered —
				// the peer retries.
				rmTrace.Outcome = outcomeDeferred
				h.recordTrace(buildTrace(rmTrace))
				return RestmailDelivery{}, endpoint.Refuse(http.StatusServiceUnavailable, "deferred", "Message deferred, retry later")
			case pipeline.ActionDiscard:
				rmTrace.Outcome = outcomeDiscarded
				h.recordTrace(buildTrace(rmTrace))
				return RestmailDelivery{Delivered: to, Failed: []string{}}, nil
			case pipeline.ActionQuarantine:
				for _, rcpt := range to {
					if mailbox, _, ok := h.resolveRecipientMailbox(rcpt); ok {
						h.quarantineFor(mailbox.ID, req.From, req.Subject, req.BodyText, result)
					}
				}
				rmTrace.Outcome = outcomeQuarantined
				h.recordTrace(buildTrace(rmTrace))
				return RestmailDelivery{Delivered: []string{}, Failed: to}, nil
			}
			// Fall-through (continue / any non-terminal action): the message will
			// be delivered — stash the trace to record post-loop with message_id.
			base := rmTrace
			deliveredTrace = &base
			// Carry the pipeline's verdicts forward so the message stored below
			// records them (they are otherwise computed and thrown away).
			authResults = result.Auth
			if result.FinalEmail != nil {
				sharedEmail = result.FinalEmail
			}

			// Sieve `redirect`: forward the ORIGINAL message onward to each recorded
			// target via the outbound queue (the same path SMTP submission and
			// vacation replies use). A bare redirect that cancelled the implicit keep
			// also suppresses the local copy below — but only when a forward was
			// actually enqueued, so a redirect that cannot be honoured never loses
			// the message.
			forwarded := 0
			for _, fwd := range buildRedirectForwards(result.FinalEmail, req.RawMessage, req.From, to, domainName, authResults) {
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
		return RestmailDelivery{Delivered: to, Failed: []string{}}, nil
	}

	// Store the original raw message (with the pipeline's Authentication-Results
	// prepended). Previously the stored message had no raw form at all, so
	// RESTMAIL-delivered mail lost its full MIME (attachments, headers) over
	// IMAP/POP3 and couldn't be re-verified.
	rawMessage := req.RawMessage
	if rawMessage != "" {
		rawMessage = pipeline.AuthResultsHeader(authResults) + rawMessage
	}

	// Each recipient's own Sieve script runs on its own copy of the message, after the domain's
	// pipeline ran once for all of them (#787). Every script runs before anything is stored,
	// so one that cannot run defers the whole message, as a failed domain pipeline does, and
	// no recipient gets the message twice when the peer retries.
	sieved, err := h.runRecipientScripts(r.Context(), pipelineCfg, sharedEmail, to)
	if err != nil {
		slog.Error("restmail: a recipient's Sieve script could not run, deferring delivery", "error", err)
		return RestmailDelivery{}, endpoint.Refuse(http.StatusServiceUnavailable, "deferred", "Message deferred, retry later")
	}

	for _, rcpt := range to {
		mailbox, deliveredTo, ok := h.resolveRecipientMailbox(rcpt)
		if !ok {
			failed = append(failed, rcpt)
			continue
		}

		// The recipient's own script decides for this recipient alone: refused, dropped,
		// held, forwarded instead, or kept, with its folder and flags. Without one, the
		// domain pipeline's folder and flags apply.
		final := sharedEmail
		if own, ran := sieved[rcpt]; ran {
			final = own.FinalEmail
			switch own.FinalAction {
			case pipeline.ActionReject:
				failed = append(failed, rcpt)
				continue
			case pipeline.ActionDiscard:
				delivered = append(delivered, rcpt)
				continue
			case pipeline.ActionQuarantine:
				h.quarantineFor(mailbox.ID, req.From, req.Subject, req.BodyText, own)
				failed = append(failed, rcpt)
				continue
			}
			forwarded := 0
			for _, fwd := range buildRedirectForwards(final, req.RawMessage, req.From, []string{rcpt}, domainName, authResults) {
				row := fwd
				if err := h.db.Create(&row).Error; err != nil {
					slog.Error("restmail: failed to enqueue sieve redirect", "recipient", row.Recipient, "error", err)
					continue
				}
				forwarded++
			}
			if forwarded > 0 && redirectSuppressesKeep(final) {
				delivered = append(delivered, rcpt)
				continue
			}
		}
		flags := sieveFlagsOf(final)

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

		recipientsTo, _ := json.Marshal([]string{rcpt})

		msg := models.Message{
			MailboxID:    mailbox.ID,
			Folder:       sieveFolder(final, mailbox.Address),
			IsRead:       flags.seen,
			IsFlagged:    flags.flagged,
			IsDraft:      flags.draft,
			MsgID:        req.MessageID,
			InReplyTo:    req.InReplyTo,
			References:   req.References,
			ThreadID:     threadID,
			Sender:       req.From,
			RecipientsTo: models.JSONB(recipientsTo),
			Subject:      req.Subject,
			BodyText:     req.BodyText,
			BodyHTML:     req.BodyHTML,
			Headers:      models.JSONB(headers),
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

	if len(delivered) == 0 {
		return RestmailDelivery{}, endpoint.Refuse(http.StatusUnprocessableEntity, "undeliverable", "No recipient accepted the message")
	}
	return RestmailDelivery{Delivered: delivered, Failed: failed}, nil
}

// runRecipientScripts runs each recipient's own Sieve script, the ones that have one, on its
// copy of email, and returns their results by recipient. An error, or a script that defers,
// stops them all: the caller defers the message.
func (h *RestmailHandler) runRecipientScripts(ctx context.Context, cfg *pipeline.PipelineConfig, email *pipeline.EmailJSON, to []string) (map[string]*pipeline.ExecutionResult, error) {
	sieved := map[string]*pipeline.ExecutionResult{}
	if cfg == nil || h.engine == nil {
		return sieved, nil
	}
	for _, rcpt := range to {
		mailbox, _, ok := h.resolveRecipientMailbox(rcpt)
		if !ok {
			continue
		}
		own, err := accountSieveOnly(h.db, cfg, mailbox.ID)
		if err != nil {
			return nil, err
		}
		if own == nil {
			continue
		}
		copied, err := recipientCopy(email, rcpt)
		if err != nil {
			return nil, err
		}
		result, err := h.engine.Execute(ctx, own, copied)
		if err != nil {
			return nil, fmt.Errorf("%s's Sieve script: %w", rcpt, err)
		}
		if result.FinalAction == pipeline.ActionDefer {
			return nil, fmt.Errorf("%s's Sieve script deferred the message", rcpt)
		}
		if result.FinalEmail == nil {
			result.FinalEmail = copied
		}
		sieved[rcpt] = result
	}
	return sieved, nil
}

// quarantineFor holds a RESTMAIL message for mailboxID, with the pipeline's reason.
func (h *RestmailHandler) quarantineFor(mailboxID uint, from, subject, bodyText string, result *pipeline.ExecutionResult) {
	preview := bodyText
	if len(preview) > 200 {
		preview = preview[:200]
	}
	h.db.Create(&models.Quarantine{
		MailboxID:        mailboxID,
		Sender:           from,
		Subject:          subject,
		BodyPreview:      preview,
		QuarantineReason: quarantineReason(result),
		ReceivedAt:       time.Now(),
		ExpiresAt:        time.Now().Add(30 * 24 * time.Hour),
	})
}

// buildInboundEmail assembles the pipeline EmailJSON for a RESTMAIL inbound
// delivery, mirroring the SMTP inbound path (deliverToLocal).
//
// When the raw message parsed, that parsed form is the base — crucially it
// carries Headers.Raw, so header-dependent filters see the real headers rather
// than an empty set: arc_verify, header_validate, and vacation's RFC 3834
// auto-reply suppression. Building the message from scratch (as this path
// previously did) left Headers.Raw nil and blinded those filters. The structured
// request fields fill any header the parse left empty, and are the sole source
// when there was no parseable raw message (parsed == nil).
//
// The SMTP envelope is not part of the message body: it is set from the request.
// envelopeFrom is the peer's from, empty for a null reverse path, and never the
// From: header; clientIP is the connecting RESTMAIL peer, so spf_check and
// dmarc_check have a genuine local verdict to work from rather than "none".
func buildInboundEmail(parsed *pipeline.EmailJSON, envelopeFrom, from string, to []string, subject, date, messageID, bodyText, bodyHTML, rawMessage, clientIP string) *pipeline.EmailJSON {
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
		MailFrom:  envelopeFrom,
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
// stored spelling always wins; otherwise a unique NFC/IDNA identity is used.
// When none exists and the local part carries a "+detail" tag, it retries
// against the base address — but only when no explicit
// alias claims the full "+detail" address (an explicit alias wins over the base
// fallback). deliveredTo is the original recipient when a "+detail" tag was
// stripped (so the caller can record a Delivered-To header preserving the tag
// for downstream Sieve filters), and "" otherwise.
func (h *RestmailHandler) resolveRecipientMailbox(rcpt string) (mailbox models.Mailbox, deliveredTo string, ok bool) {
	_, err := rmail.CanonicalMailboxAddress(rcpt)
	if err != nil || rmail.ContainsControlChar(rcpt) {
		return models.Mailbox{}, "", false
	}
	if err := repositories.FindActiveMailboxByAddress(h.db, rcpt, &mailbox); err == nil {
		return mailbox, "", true
	} else if !errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, repositories.ErrMailboxUnavailable) {
		return models.Mailbox{}, "", false
	}

	base, _, tagged := rmail.StripSubaddress(rcpt)
	if !tagged {
		return models.Mailbox{}, "", false
	}

	// An explicit alias for the full "+detail" address claims it — do not fall
	// back to the base mailbox (explicit alias wins over subaddressing).
	aliasClaims, err := repositories.AliasClaimsAddress(h.db, rcpt, false)
	if err != nil || aliasClaims {
		return models.Mailbox{}, "", false
	}

	if err := repositories.FindActiveMailboxByAddress(h.db, base, &mailbox); err == nil {
		return mailbox, rcpt, true
	}
	return models.Mailbox{}, "", false
}

// mediaType returns the lower-cased media type of a Content-Type value with any
// parameters (charset, boundary, ...) stripped: "text/plain; charset=utf-8" ->
// "text/plain".
func mediaType(contentType string) string {
	return rmime.MediaType(contentType)
}

// extractBodyParts walks a potentially nested Body structure and returns the
// first text/plain and text/html content found.
//
// The comparison is on the media type alone: a part's ContentType frequently
// carries parameters (e.g. "text/plain; charset=utf-8", as the outbound
// pipeline emits), and an exact-string match against "text/plain" would miss
// those and silently drop the body a transform produced.
func extractBodyParts(body pipeline.Body) (text, html string) {
	return rmime.TextAndHTML(body)
}
