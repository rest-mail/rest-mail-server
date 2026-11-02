package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"sort"
	"strings"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
)

type contactReplyTimeSetting struct {
	Enabled bool `json:"enabled"`
}

type setContactReplyTimeRequest struct {
	ID   uint32                  `path:"id" minimum:"1" example:"1"`
	Body contactReplyTimeSetting `body:""`
}

func (h *ContactHandler) getReplyTimeSetting(r *http.Request, in contactsPath) (contactReplyTimeSetting, error) {
	mailbox, err := h.resolveContactAccount(r, in.ID, models.CapMailRead)
	if err != nil {
		return contactReplyTimeSetting{}, err
	}
	return contactReplyTimeSetting{Enabled: mailbox.ContactReplyTimeEnabled}, nil
}

func (h *ContactHandler) setReplyTimeSetting(r *http.Request, in setContactReplyTimeRequest) (contactReplyTimeSetting, error) {
	mailbox, err := h.resolveContactAccount(r, in.ID, models.CapMailWrite)
	if err != nil {
		return contactReplyTimeSetting{}, err
	}
	result := h.db.WithContext(r.Context()).Model(mailbox).Update("contact_reply_time_enabled", in.Body.Enabled)
	if result.Error != nil {
		return contactReplyTimeSetting{}, fmt.Errorf("save contact reply-time setting: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return contactReplyTimeSetting{}, errContactAccess
	}
	return in.Body, nil
}

func replyTimeAddress(value string) string {
	address, err := mail.ParseAddress(value)
	if err != nil {
		return ""
	}
	return strings.ToLower(address.Address)
}

func replyTimeParent(message models.Message) string {
	parent := strings.Fields(message.InReplyTo)
	if len(parent) == 0 {
		parent = strings.Fields(message.References)
	}
	if len(parent) == 0 {
		return ""
	}
	return rmail.CanonicalID(parent[len(parent)-1])
}

func (h *ContactHandler) contactsWithReplyTime(r *http.Request, mailbox *models.Mailbox, rows []models.Contact) ([]Contact, error) {
	out := contactsOf(rows)
	if !mailbox.ContactReplyTimeEnabled || len(out) == 0 {
		return out, nil
	}
	addresses := make([]string, 0, len(out))
	wanted := make(map[string]bool, len(out))
	for _, contact := range out {
		address := replyTimeAddress(contact.Email)
		if address == "" {
			continue
		}
		addresses = append(addresses, address)
		wanted[address] = true
	}
	if len(addresses) == 0 {
		return out, nil
	}
	var messages []models.Message
	if err := h.db.WithContext(r.Context()).
		Select("id", "folder", "message_id", "in_reply_to", "references", "sender", "recipients_to", "recipients_cc", "received_at").
		Where("mailbox_id = ? AND is_deleted = false AND is_draft = false AND is_dsn = false", mailbox.ID).
		Where("folder = ? OR LOWER(sender) IN ?", "Sent", addresses).
		Find(&messages).Error; err != nil {
		return nil, fmt.Errorf("load contact reply history: %w", err)
	}
	sent := make(map[string]models.Message)
	ambiguous := make(map[string]bool)
	for _, message := range messages {
		if message.Folder != "Sent" || message.ReceivedAt.IsZero() {
			continue
		}
		id := rmail.CanonicalID(message.MsgID)
		if id == "" {
			continue
		}
		if _, exists := sent[id]; exists {
			ambiguous[id] = true
		}
		sent[id] = message
	}
	first := make(map[string]map[string]int64)
	for _, reply := range messages {
		address := replyTimeAddress(reply.Sender)
		if reply.Folder == "Sent" || !wanted[address] || reply.ReceivedAt.IsZero() {
			continue
		}
		parent := replyTimeParent(reply)
		original, found := sent[parent]
		if !found || ambiguous[parent] {
			continue
		}
		var recipients, cc []string
		if json.Unmarshal(original.RecipientsTo, &recipients) != nil || json.Unmarshal(original.RecipientsCc, &cc) != nil {
			continue
		}
		addressed := false
		for _, recipient := range append(recipients, cc...) {
			if replyTimeAddress(recipient) == address {
				addressed = true
				break
			}
		}
		if !addressed {
			continue
		}
		elapsed := reply.ReceivedAt.Sub(original.ReceivedAt)
		if elapsed <= 0 {
			continue
		}
		delay := max(int64(elapsed.Seconds()), 1)
		if first[address] == nil {
			first[address] = make(map[string]int64)
		}
		previous, exists := first[address][parent]
		if !exists || delay < previous {
			first[address][parent] = delay
		}
	}
	for index := range out {
		matches := first[replyTimeAddress(out[index].Email)]
		if len(matches) < 3 {
			continue
		}
		delays := make([]int64, 0, len(matches))
		for _, delay := range matches {
			delays = append(delays, delay)
		}
		sort.Slice(delays, func(left, right int) bool { return delays[left] < delays[right] })
		middle := len(delays) / 2
		median := delays[middle]
		if len(delays)%2 == 0 {
			median = delays[middle-1] + (median-delays[middle-1])/2
		}
		out[index].UsualReplySeconds = &median
	}
	return out, nil
}
