// Package imap adapts the rest-mail REST API to the github.com/rest-mail/imap
// server library: it implements the library's Backend/Mailbox interfaces by
// mapping apiclient responses onto the library's neutral types. The protocol
// engine itself lives in the library; this package only knows how rest-mail
// stores mail.
package imap

import (
	imapsrv "github.com/rest-mail/imap"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// Backend authenticates IMAP users against the rest-mail REST API.
type Backend struct {
	api *apiclient.Client
}

// NewBackend creates a Backend over the given API client.
func NewBackend(api *apiclient.Client) *Backend {
	return &Backend{api: api}
}

var _ imapsrv.Backend = (*Backend)(nil)

// Authenticate logs the user in via the API and returns a Mailbox scoped to the
// resulting access token and account.
func (b *Backend) Authenticate(user, pass string) (imapsrv.Mailbox, error) {
	resp, err := b.api.Login(user, pass)
	if err != nil {
		return nil, err
	}
	return &mailbox{
		api:       b.api,
		email:     user,
		token:     resp.Data.AccessToken,
		accountID: resp.Data.User.ID,
	}, nil
}

// mailbox is one authenticated account's view of its mail.
type mailbox struct {
	api       *apiclient.Client
	email     string
	token     string
	accountID uint
}

var _ imapsrv.Mailbox = (*mailbox)(nil)

// Folders lists the account's folders.
func (m *mailbox) Folders() ([]imapsrv.Folder, error) {
	resp, err := m.api.ListFolders(m.token, m.accountID)
	if err != nil {
		return nil, err
	}
	out := make([]imapsrv.Folder, 0, len(resp.Data))
	for _, f := range resp.Data {
		out = append(out, imapsrv.Folder{Name: f.Name})
	}
	return out, nil
}

// Messages returns a folder's messages oldest-first, mapped onto the library's
// neutral Message type. The message ID doubles as the IMAP UID.
func (m *mailbox) Messages(folder string) ([]imapsrv.Message, error) {
	resp, err := m.api.ListMessages(m.token, m.accountID, folder)
	if err != nil {
		return nil, err
	}
	out := make([]imapsrv.Message, 0, len(resp.Data))
	for _, msg := range resp.Data {
		out = append(out, toMessage(msg))
	}
	return out, nil
}

// toMessage maps an apiclient summary onto the library's neutral Message.
// Starred folds into \Flagged, matching the previous gateway's flag mapping.
// Size uses WireSize: the exact stored-raw octet count when the server has
// one (RFC 3501 requires RFC822.SIZE to be the transmitted size exactly),
// falling back to size_bytes for messages without a stored raw.
func toMessage(msg apiclient.MessageSummary) imapsrv.Message {
	return imapsrv.Message{
		UID:     uint32(msg.ID),
		Size:    msg.WireSize(),
		Seen:    msg.IsRead,
		Flagged: msg.IsFlagged || msg.IsStarred,
		Draft:   msg.IsDraft,
		Subject: msg.Subject,
		From:    imapsrv.Address{Name: msg.SenderName, Email: msg.Sender},
		To:      string(msg.RecipientsTo),
		Date:    msg.ReceivedAt,
	}
}

// Fetch returns the full-fidelity RFC 2822 form of a message: the pristine
// stored original when the server has one, otherwise a best-effort
// reconstruction from the structured detail (for locally-composed items that
// have no stored raw). Serving the stored original preserves attachments, To/Cc,
// custom headers and any DKIM-Signature verbatim.
func (m *mailbox) Fetch(uid uint32) ([]byte, error) {
	detail, err := m.api.GetMessage(m.token, uint(uid))
	if err != nil {
		return nil, err
	}
	return []byte(m.rawMessage(detail.Data)), nil
}

// rawMessage returns the stored raw bytes when present, else the rebuild.
func (m *mailbox) rawMessage(detail apiclient.MessageDetail) string {
	if raw, err := m.api.GetRawMessage(m.token, detail.ID); err == nil && raw != "" {
		return raw
	}
	return buildRawMessage(detail)
}

// Store applies a persistent flag change (STORE, or the auto-\Seen of a
// non-peek BODY[] fetch).
func (m *mailbox) Store(uid uint32, f imapsrv.FlagUpdate) error {
	updates := map[string]interface{}{}
	if f.Seen != nil {
		updates["is_read"] = *f.Seen
	}
	if f.Flagged != nil {
		updates["is_flagged"] = *f.Flagged
	}
	if f.Draft != nil {
		updates["is_draft"] = *f.Draft
	}
	if len(updates) == 0 {
		return nil
	}
	return m.api.UpdateMessage(m.token, uint(uid), updates)
}

// Move relocates a message to another folder.
func (m *mailbox) Move(uid uint32, dest string) error {
	return m.api.UpdateMessage(m.token, uint(uid), map[string]interface{}{"folder": dest})
}

// Delete permanently removes a message (EXPUNGE/CLOSE).
func (m *mailbox) Delete(uid uint32) error {
	return m.api.DeleteMessage(m.token, uint(uid))
}

// Copy duplicates a message into dest by re-delivering its full detail (with the
// raw original preserved) and then moving the new copy out of INBOX if needed.
func (m *mailbox) Copy(uid uint32, dest string) error {
	detail, err := m.api.GetMessage(m.token, uint(uid))
	if err != nil {
		return err
	}

	deliverReq := &apiclient.DeliverRequest{
		Address:      m.email,
		MailboxID:    detail.Data.MailboxID,
		Sender:       detail.Data.Sender,
		SenderName:   detail.Data.SenderName,
		RecipientsTo: detail.Data.RecipientsTo,
		Subject:      detail.Data.Subject,
		BodyText:     detail.Data.BodyText,
		BodyHTML:     detail.Data.BodyHTML,
		MessageID:    detail.Data.MessageID,
		InReplyTo:    detail.Data.InReplyTo,
		References:   detail.Data.References,
		RawMessage:   m.rawMessage(detail.Data),
	}

	resp, err := m.api.DeliverMessage(deliverReq)
	if err != nil {
		return err
	}

	// Move the new message to the destination folder if not INBOX
	if dest != "INBOX" && resp != nil {
		_ = m.api.UpdateMessage(m.token, resp.Data.ID, map[string]interface{}{"folder": dest})
	}
	return nil
}

// Append delivers raw RFC 2822 bytes into dest (APPEND), applying any flags the
// client supplied to the newly delivered message.
func (m *mailbox) Append(dest string, f imapsrv.FlagUpdate, raw []byte) error {
	// Parse basic headers from raw message for the structured delivery fields.
	subject, bodyText, bodyHTML, messageID, senderName := parseBasicHeaders(raw)

	deliverReq := &apiclient.DeliverRequest{
		Address:    m.email,
		Sender:     m.email,
		SenderName: senderName,
		Subject:    subject,
		BodyText:   bodyText,
		BodyHTML:   bodyHTML,
		MessageID:  messageID,
		RawMessage: string(raw),
	}

	resp, err := m.api.DeliverMessage(deliverReq)
	if err != nil {
		return err
	}

	// Move to the target folder if not INBOX
	if dest != "INBOX" && resp != nil {
		_ = m.api.UpdateMessage(m.token, resp.Data.ID, map[string]interface{}{"folder": dest})
	}

	// Apply supplied flags to the delivered message
	updates := map[string]interface{}{}
	if f.Seen != nil && *f.Seen {
		updates["is_read"] = true
	}
	if f.Flagged != nil && *f.Flagged {
		updates["is_flagged"] = true
	}
	if f.Draft != nil && *f.Draft {
		updates["is_draft"] = true
	}
	if resp != nil && len(updates) > 0 {
		_ = m.api.UpdateMessage(m.token, resp.Data.ID, updates)
	}
	return nil
}

// Quota returns storage use and limit in bytes.
func (m *mailbox) Quota() (used, limit int64, err error) {
	quota, err := m.api.GetQuota(m.token, m.accountID)
	if err != nil {
		return 0, 0, err
	}
	return quota.Data.QuotaUsedBytes, quota.Data.QuotaBytes, nil
}
