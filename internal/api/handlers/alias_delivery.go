package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

func (h *RestmailHandler) deliverResolvedRecipients(ctx context.Context, req restmailDeliverBody, sender, clientIP string, accepted, refused []string, recipients map[string]repositories.Recipient) (RestmailDelivery, error) {
	result := RestmailDelivery{Delivered: []string{}, Failed: refused}
	var lastError error
	deferred := false
	to, _ := json.Marshal(accepted)
	for _, address := range accepted {
		recipient := recipients[address]
		_, err := h.localDelivery.deliverToRecipient(ctx, recipient, localDeliveryParams{
			Mailbox: recipient.Mailbox, Sender: sender, RecipientsTo: to, Subject: req.Subject,
			BodyText: req.BodyText, BodyHTML: req.BodyHTML, MessageID: req.MessageID,
			InReplyTo: req.InReplyTo, References: req.References, RawMessage: req.RawMessage, ClientIP: clientIP,
		})
		if err != nil {
			lastError = err
			deferred = deferred || (!strings.HasPrefix(err.Error(), "rejected:") && !strings.HasPrefix(err.Error(), "mailbox_full:"))
			result.Failed = append(result.Failed, address)
			continue
		}
		result.Delivered = append(result.Delivered, address)
	}
	if len(result.Delivered) == 0 {
		if deferred {
			return RestmailDelivery{}, endpoint.Refuse(http.StatusServiceUnavailable, "deferred", "Message deferred, retry later")
		}
		return RestmailDelivery{}, endpoint.Refuse(http.StatusUnprocessableEntity, "undeliverable", lastError.Error())
	}
	return result, nil
}

func (h *MessageHandler) deliverToRecipient(ctx context.Context, recipient repositories.Recipient, params localDeliveryParams) (*models.Message, error) {
	if recipient.DeliveredTo != "" && params.RawMessage != "" {
		if params.Stage != deliverContent {
			for _, previous := range deliveredToChain(params.RawMessage) {
				if normalizeRedirectAddr(previous) == normalizeRedirectAddr(recipient.DeliveredTo) {
					return nil, fmt.Errorf("rejected: alias forwarding loop at %s", recipient.DeliveredTo)
				}
			}
		}
		params.RawMessage = "Delivered-To: " + recipient.DeliveredTo + "\r\n" + params.RawMessage
	}
	if recipient.Mailbox.ID != 0 || !recipient.Alias {
		if recipient.Mailbox.ID != 0 {
			params.Mailbox = recipient.Mailbox
		}
		return h.deliverToLocal(ctx, params)
	}
	if strings.TrimSpace(params.RawMessage) == "" {
		return nil, fmt.Errorf("rejected: alias forwarding requires the original raw message")
	}
	params.RawMessage = rmime.StripAuthResults(params.RawMessage, pipeline.AuthServID)
	email, err := rmime.Parse([]byte(params.RawMessage))
	if err != nil {
		return nil, fmt.Errorf("rejected: invalid alias message: %w", err)
	}
	email.Envelope = pipeline.Envelope{MailFrom: params.Sender, RcptTo: []string{recipient.DeliveredTo}, ClientIP: params.ClientIP,
		Helo: params.HeloName, TLS: receivedTLSFlag(params.ReceivedTLS), Direction: "inbound"}
	if email.Metadata == nil {
		email.Metadata = make(map[string]string)
	}
	email.Metadata["raw_message"] = params.RawMessage
	var auth []pipeline.AuthResult
	var deliveredTrace *traceInputs
	if h.engine != nil {
		rest := &RestmailHandler{db: h.db}
		cfg, err := rest.inboundPipeline(ctx, recipient.ForwardingDomain)
		if err != nil {
			return nil, fmt.Errorf("deferred: alias inbound policy: %w", err)
		}
		if cfg == nil {
			return nil, fmt.Errorf("rejected: alias domain no longer exists")
		}
		stage, state := params.stageOf(cfg, email)
		result, err := h.engine.ExecuteStage(pipeline.WithDB(ctx, h.db), stage, email, state)
		if err != nil {
			return nil, fmt.Errorf("deferred: alias pipeline: %w", err)
		}
		deliveredTrace = &traceInputs{PipelineID: cfg.ID, Direction: "inbound", Result: result,
			Envelope: email.Envelope, Transport: transportLabel(params.ReceivedTLS), RFCMessageID: email.Headers.MessageID}
		switch result.FinalAction {
		case pipeline.ActionContinue:
		case pipeline.ActionDefer:
			deliveredTrace.Outcome = outcomeDeferred
			h.recordTrace(buildTrace(*deliveredTrace))
			return nil, fmt.Errorf("deferred: alias pipeline deferred delivery")
		default:
			deliveredTrace.Outcome = outcomeRejected
			h.recordTrace(buildTrace(*deliveredTrace))
			return nil, fmt.Errorf("rejected: alias pipeline: %s %s", result.FinalAction, result.RejectMsg)
		}
		auth = result.Auth
		if result.FinalEmail != nil {
			email = result.FinalEmail
		}
	}
	if params.Stage == deliverRequired {
		params.State.keep(email)
		return nil, errContentStageNext
	}
	targets, _ := json.Marshal([]string{recipient.Address})
	if email.Metadata == nil {
		email.Metadata = make(map[string]string)
	}
	email.Metadata[redirectMetaTargets] = string(targets)
	forwards := buildRedirectForwards(email, params.RawMessage, params.Sender, []string{recipient.DeliveredTo}, recipient.ForwardingDomain, auth)
	if len(forwards) != 1 {
		return nil, fmt.Errorf("rejected: alias forwarding loop or invalid destination")
	}
	row := forwards[0]
	row.RequireTLS = params.RequireTLS
	err = h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if params.InCommit != nil {
			if err := params.InCommit(tx); err != nil {
				return err
			}
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return nil, fmt.Errorf("deferred: queue alias forward: %w", err)
	}
	if deliveredTrace != nil {
		deliveredTrace.Outcome = outcomeDelivered
		h.recordTrace(buildTrace(*deliveredTrace))
	}
	return nil, nil
}
