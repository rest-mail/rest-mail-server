package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"gorm.io/gorm"
)

// Recipient delivery states, the small set a sender is told (#1568). The queue's own
// statuses map onto them: pending and delivering are queued (retrying once an attempt
// has failed), deferred is retrying, delivered is delivered, and bounced and expired are
// failed. unknown is a recipient this server kept no delivery record for.
const (
	recipientQueued    = "queued"
	recipientRetrying  = "retrying"
	recipientDelivered = "delivered"
	recipientFailed    = "failed"
	recipientUnknown   = "unknown"
)

// MessageRecipients is what happened to each copy of a message the account sent.
type MessageRecipients struct {
	MessageID  uint              `json:"message_id" description:"The sent message these recipients belong to"`
	Recipients []RecipientStatus `json:"recipients" description:"One entry per recipient: To and Cc in the message's order, then any Bcc recipient that was queued. Empty for a message this account did not send."`
}

// RecipientStatus is one recipient's copy. The remote server's own reply (the queue's
// last_error and last_error_code) is never here: it can carry details meant for the
// sending server, not the person, so only the reason derived from its category is.
type RecipientStatus struct {
	Address       string     `json:"address" example:"bob@example.com"`
	Delivery      string     `json:"delivery" enums:"remote,local" description:"remote: a recipient on another server; local: a mailbox on this server. Either goes through the outbound queue"`
	State         string     `json:"state" enums:"queued,retrying,delivered,failed,unknown" description:"queued: waiting for its first attempt; retrying: an attempt failed and it will be tried again; delivered: the recipient's server (or this one) accepted it; failed: it bounced or expired and will not be tried again; unknown: no delivery record was kept"`
	Reason        string     `json:"reason,omitempty" description:"Why it is retrying, failed or unknown, in a sentence derived from the failure category, never the remote server's own reply"`
	Category      string     `json:"category,omitempty" enums:"bad_mailbox,bad_domain,policy,other_permanent,mailbox_full,greylisted,rate_limited,connection,tls,dns,other_transient" description:"The kind of failure of the last attempt, when one failed"`
	Attempts      int        `json:"attempts" description:"Delivery attempts made so far; 0 for a recipient with no outbound queue entry"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty" description:"When the last attempt was made; absent before the first one"`
}

// listMessageRecipients answers, for a message the account sent, what happened to each
// recipient: from its outbound queue row, which every send makes for every recipient
// (#1564), and otherwise from the copy delivered into the mailbox.
func (h *MessageHandler) listMessageRecipients(r *http.Request, in messagePath) (MessageRecipients, error) {
	msg, err := h.ownedMessage(r, in.ID, models.CapMailRead, "id", "mailbox_id", "folder", "message_id", "sender", "recipients_to", "recipients_cc", "received_at")
	if err != nil {
		return MessageRecipients{}, err
	}
	db := h.db.WithContext(r.Context())
	result := MessageRecipients{MessageID: msg.ID, Recipients: []RecipientStatus{}}

	// Every queue row with this message's id was made by sending it: the Sent copy is the
	// only message a queue row names.
	var queued []models.OutboundQueue
	if err := db.Select("id", "recipient", "status", "attempts", "last_attempt", "failure_category").
		Where("message_id = ?", msg.ID).Order("id").Find(&queued).Error; err != nil {
		return MessageRecipients{}, fmt.Errorf("load message recipients: %w", err)
	}
	if len(queued) == 0 && msg.Folder != "Sent" {
		return result, nil
	}
	rows := map[string]models.OutboundQueue{}
	for _, row := range queued {
		rows[strings.ToLower(row.Recipient)] = row // the newest row for an address wins
	}

	var owner models.Mailbox
	if err := db.Select("id", "address").First(&owner, msg.MailboxID).Error; err != nil {
		return MessageRecipients{}, fmt.Errorf("load sender mailbox: %w", err)
	}

	seen := map[string]bool{}
	for _, address := range append(addressList(msg.RecipientsTo), addressList(msg.RecipientsCc)...) {
		key := strings.ToLower(address)
		if seen[key] {
			continue
		}
		seen[key] = true
		if row, ok := rows[key]; ok {
			status, err := h.queuedRecipient(r, row)
			if err != nil {
				return MessageRecipients{}, err
			}
			result.Recipients = append(result.Recipients, status)
			continue
		}
		status, err := h.localRecipient(r, msg, owner, address)
		if err != nil {
			return MessageRecipients{}, err
		}
		result.Recipients = append(result.Recipients, status)
	}
	// A Bcc recipient is not in the Sent copy's headers; its queue row names it.
	for _, row := range queued {
		key := strings.ToLower(row.Recipient)
		if seen[key] {
			continue
		}
		seen[key] = true
		status, err := h.queuedRecipient(r, rows[key])
		if err != nil {
			return MessageRecipients{}, err
		}
		result.Recipients = append(result.Recipients, status)
	}
	return result, nil
}

// queuedRecipient is a queue row as the sender sees it, named local when its address is
// a mailbox on this server: a queue row says how the copy travelled, not where it went.
func (h *MessageHandler) queuedRecipient(r *http.Request, row models.OutboundQueue) (RecipientStatus, error) {
	status := queueRowStatus(row)
	var mailbox models.Mailbox
	err := repositories.FindMailboxByAddress(h.db.WithContext(r.Context()), row.Recipient, &mailbox)
	switch {
	case err == nil && mailbox.ID != 0:
		status.Delivery = "local"
	case err == nil, errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, repositories.ErrMailboxUnavailable):
	default:
		return RecipientStatus{}, fmt.Errorf("look up a queued recipient: %w", err)
	}
	return status, nil
}

// queueRowStatus is a queue row's state and its category's reason, never last_error.
func queueRowStatus(row models.OutboundQueue) RecipientStatus {
	status := RecipientStatus{Address: row.Recipient, Delivery: "remote", Attempts: row.Attempts, LastAttemptAt: row.LastAttempt, Category: string(row.FailureCategory)}
	switch row.Status {
	case "pending", "delivering":
		status.State = recipientQueued
		if row.Attempts > 0 {
			status.State = recipientRetrying
		}
	case "deferred":
		status.State = recipientRetrying
	case "delivered":
		status.State = recipientDelivered
		status.Category = ""
	case "bounced", "expired":
		status.State = recipientFailed
	default:
		status.State = recipientUnknown
	}
	switch {
	case status.State == recipientDelivered:
	case row.FailureCategory.Description() != "":
		status.Reason = row.FailureCategory.Description()
	case row.Status == "expired":
		status.Reason = "The message could not be delivered before it expired."
	case row.Status == "bounced":
		status.Reason = models.FailureOtherPermanent.Description()
	case status.State == recipientRetrying:
		status.Reason = models.FailureOtherTransient.Description()
	case status.State == recipientUnknown:
		status.Reason = "No delivery record was kept for this recipient."
	}
	return status
}

// localRecipient is a To or Cc recipient with no queue row: delivered when the copy the
// send put in a mailbox on this server is there. Only a copy from the sender's own
// address counts, so a message made to look sent, such as an import, cannot ask whether
// someone else's mailbox holds a message.
func (h *MessageHandler) localRecipient(r *http.Request, msg models.Message, owner models.Mailbox, address string) (RecipientStatus, error) {
	status := RecipientStatus{Address: address, Delivery: "local", State: recipientUnknown, Reason: "No delivery record was kept for this recipient."}
	db := h.db.WithContext(r.Context())
	var mailbox models.Mailbox
	err := repositories.FindMailboxByAddress(db, address, &mailbox)
	switch {
	case err == nil && mailbox.ID != 0:
	case err == nil, errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, repositories.ErrMailboxUnavailable):
		// Not a mailbox here and never queued: nothing was recorded for it.
		status.Delivery = "remote"
		return status, nil
	default:
		return RecipientStatus{}, fmt.Errorf("look up a local recipient: %w", err)
	}
	if msg.MsgID == "" || !strings.EqualFold(msg.Sender, owner.Address) {
		return status, nil
	}
	var copies int64
	if err := db.Model(&models.Message{}).
		Where("mailbox_id = ? AND message_id = ? AND lower(sender) = lower(?) AND id <> ?", mailbox.ID, msg.MsgID, msg.Sender, msg.ID).
		Count(&copies).Error; err != nil {
		return RecipientStatus{}, fmt.Errorf("check local delivery: %w", err)
	}
	if copies > 0 {
		status.State, status.Reason = recipientDelivered, ""
	}
	return status, nil
}

// addressList reads a recipients column: a JSON array of addresses. Anything else is
// no addresses.
func addressList(raw models.JSONB) []string {
	var addresses []string
	if len(raw) == 0 || json.Unmarshal(raw, &addresses) != nil {
		return nil
	}
	out := addresses[:0]
	for _, address := range addresses {
		if address = strings.TrimSpace(address); address != "" {
			out = append(out, address)
		}
	}
	return out
}
