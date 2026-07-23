// Package pop3 adapts the rest-mail REST API to the github.com/rest-mail/pop3
// server library: it implements the library's Backend/Mailbox interfaces by
// mapping apiclient responses onto the library's neutral types. The protocol
// engine itself lives in the library; this package only knows how rest-mail
// stores mail.
package pop3

import (
	"strconv"

	pop3srv "github.com/rest-mail/pop3"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// Backend authenticates POP3 users against the rest-mail REST API.
type Backend struct {
	api *apiclient.Client
}

// NewBackend creates a Backend over the given API client.
func NewBackend(api *apiclient.Client) *Backend {
	return &Backend{api: api}
}

var _ pop3srv.Backend = (*Backend)(nil)

// Authenticate logs the user in via the API and returns a Mailbox scoped to the
// resulting access token and account.
func (b *Backend) Authenticate(user, pass string) (pop3srv.Mailbox, error) {
	resp, err := b.api.Login(user, pass)
	if err != nil {
		return nil, err
	}
	return &mailbox{
		api:       b.api,
		token:     resp.Data.AccessToken,
		accountID: resp.Data.User.ID,
	}, nil
}

// mailbox is one authenticated POP3 maildrop: the account's INBOX.
type mailbox struct {
	api       *apiclient.Client
	token     string
	accountID uint
}

var _ pop3srv.Mailbox = (*mailbox)(nil)

// Messages returns the INBOX contents oldest-first, mapped onto the library's
// neutral Message type. The message ID doubles as the POP3 UIDL value.
func (m *mailbox) Messages() ([]pop3srv.Message, error) {
	resp, err := m.api.ListMessages(m.token, m.accountID, "INBOX")
	if err != nil {
		return nil, err
	}
	out := make([]pop3srv.Message, 0, len(resp.Data))
	for _, msg := range resp.Data {
		out = append(out, pop3srv.Message{
			UID: strconv.FormatUint(uint64(msg.ID), 10),
			// WireSize: exact stored-raw octet count when the server has one
			// (POP3 STAT/LIST must report the transmitted size exactly),
			// falling back to size_bytes for messages without a stored raw.
			Size: msg.WireSize(),
			Seen: msg.IsRead,
		})
	}
	return out, nil
}

// Retrieve returns the full-fidelity RFC 2822 form of a message: the pristine
// stored original when the server has one, otherwise a best-effort
// reconstruction from the structured detail (for locally-composed items that
// have no stored raw). Serving the stored original preserves attachments, To/Cc,
// custom headers and any DKIM-Signature verbatim.
func (m *mailbox) Retrieve(uid string) ([]byte, error) {
	id, err := parseUID(uid)
	if err != nil {
		return nil, err
	}
	detail, err := m.api.GetMessage(m.token, id)
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

// MarkSeen flags a message read after a successful RETR.
func (m *mailbox) MarkSeen(uid string) error {
	id, err := parseUID(uid)
	if err != nil {
		return err
	}
	return m.api.UpdateMessage(m.token, id, map[string]interface{}{"is_read": true})
}

// Delete permanently removes a message (called by the library on QUIT).
func (m *mailbox) Delete(uid string) error {
	id, err := parseUID(uid)
	if err != nil {
		return err
	}
	return m.api.DeleteMessage(m.token, id)
}

// parseUID converts the library's string UID back to the API's numeric ID.
func parseUID(uid string) (uint, error) {
	v, err := strconv.ParseUint(uid, 10, 64)
	return uint(v), err
}
