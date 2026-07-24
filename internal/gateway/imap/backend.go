// Package imap adapts the rest-mail REST API to the github.com/rest-mail/go-imap
// server library: it implements the library's Backend/Mailbox interfaces by
// mapping apiclient responses onto the library's neutral types. The protocol
// engine itself lives in the library; this package only knows how rest-mail
// stores mail.
package imap

import (
	"fmt"
	"log/slog"

	imapsrv "github.com/rest-mail/go-imap"

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
		// A failed IMAP login is a security-relevant event (credential stuffing /
		// enumeration). Log it structured, with the attacker-controlled username
		// masked, so it is observable alongside the SMTP smtp_auth_failed events.
		slog.Warn("imap: auth failed",
			"user", maskEmail(user),
			"event", "imap_auth_failed",
		)
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

// The mailbox also satisfies the library's optional UIDPLUS (RFC 4315) and atomic
// MOVE (RFC 6851) interfaces, so the server advertises UIDPLUS and emits
// APPENDUID / COPYUID response codes and honours UID EXPUNGE for rest-mail.
var (
	_ imapsrv.UIDPlusMailbox = (*mailbox)(nil)
	_ imapsrv.Mover          = (*mailbox)(nil)
)

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
	if err := validateFolder(folder); err != nil {
		return nil, err
	}
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
//
// \Flagged is backed by BOTH is_flagged and is_starred so the mapping is
// symmetric with FETCH, which folds a webmail star into \Flagged
// (is_flagged || is_starred, see toMessage). Writing only is_flagged made
// STORE -FLAGS \Flagged leave a starred message still flagged on the next
// SELECT; mirroring the flag onto both columns means what you STORE is what
// you FETCH back.
func (m *mailbox) Store(uid uint32, f imapsrv.FlagUpdate) error {
	updates := map[string]interface{}{}
	if f.Seen != nil {
		updates["is_read"] = *f.Seen
	}
	if f.Flagged != nil {
		updates["is_flagged"] = *f.Flagged
		updates["is_starred"] = *f.Flagged
	}
	if f.Draft != nil {
		updates["is_draft"] = *f.Draft
	}
	if len(updates) == 0 {
		return nil
	}
	return m.api.UpdateMessage(m.token, uint(uid), updates)
}

// Move relocates a message to another folder (base Mailbox.Move). It delegates to
// MoveUID and discards the reported UID, so the two paths never diverge.
func (m *mailbox) Move(uid uint32, dest string) error {
	_, err := m.MoveUID(uid, dest)
	return err
}

// MoveUID relocates a message to dest and returns its UID there (Mover, RFC 6851).
//
// rest-mail assigns each message a UID from a monotonically increasing, never
// reused ID at delivery time, so a move cannot be a folder relabel that keeps the
// old ID: that would drop a message carrying a low source UID into a destination
// already holding higher UIDs, violating RFC 3501 §2.3.1.1 (each newly arrived
// message must be assigned a UID higher than all previously added) and breaking
// clients that sync incrementally with UID FETCH <lastuid+1>:*.
//
// Instead the move is a fresh delivery of the message into dest — which allocates
// a new, higher destination UID exactly like COPY — followed by deletion of the
// source. Copy-before-delete means a failure can at worst leave a duplicate, never
// lose the message. The new UID is what the atomic MOVE's COPYUID response reports.
func (m *mailbox) MoveUID(uid uint32, dest string) (uint32, error) {
	newUID, err := m.CopyUID(uid, dest)
	if err != nil {
		return 0, err
	}
	if err := m.api.DeleteMessage(m.token, uint(uid)); err != nil {
		return 0, err
	}
	return newUID, nil
}

// Delete permanently removes a message (EXPUNGE/CLOSE).
func (m *mailbox) Delete(uid uint32) error {
	return m.api.DeleteMessage(m.token, uint(uid))
}

// Copy duplicates a message into dest (base Mailbox.Copy). It delegates to CopyUID
// and discards the reported UID, so the two paths never diverge.
func (m *mailbox) Copy(uid uint32, dest string) error {
	_, err := m.CopyUID(uid, dest)
	return err
}

// CopyUID duplicates the message named by uid into dest by re-delivering its full
// detail (with the raw original preserved) directly into the destination folder.
// It returns the new message's UID (UIDPLUS, RFC 4315) — the ID the delivery
// assigned, which is rest-mail's UID for the copy — for the COPYUID response code.
//
// The copy carries the source message's flags and INTERNALDATE (RFC 3501 §6.4.7
// SHOULD preserve them) and is created directly in dest in a single delivery, so
// there is no transient INBOX state and no separate, error-swallowing move step. A
// delivery that produces no message (pipeline quarantine/discard, or an
// out-of-range UID) is surfaced as an error rather than reported as a bogus
// success carrying UID 0.
func (m *mailbox) CopyUID(uid uint32, dest string) (uint32, error) {
	if err := validateFolder(dest); err != nil {
		return 0, err
	}
	detail, err := m.api.GetMessage(m.token, uint(uid))
	if err != nil {
		return 0, err
	}
	src := detail.Data

	// Preserve the source flags and internal date on the copy. \Flagged is backed
	// by both is_flagged and is_starred, mirroring the gateway's symmetric mapping.
	seen, flagged, starred, draft := src.IsRead, src.IsFlagged, src.IsStarred, src.IsDraft
	received := src.ReceivedAt

	deliverReq := &apiclient.DeliverRequest{
		Address:      m.email,
		MailboxID:    src.MailboxID,
		Folder:       dest,
		Sender:       src.Sender,
		SenderName:   src.SenderName,
		RecipientsTo: src.RecipientsTo,
		Subject:      src.Subject,
		BodyText:     src.BodyText,
		BodyHTML:     src.BodyHTML,
		MessageID:    src.MessageID,
		InReplyTo:    src.InReplyTo,
		References:   src.References,
		RawMessage:   []byte(m.rawMessage(src)),
		IsRead:       &seen,
		IsFlagged:    &flagged,
		IsStarred:    &starred,
		IsDraft:      &draft,
		ReceivedAt:   &received,
	}

	resp, err := m.api.DeliverMessage(deliverReq)
	if err != nil {
		return 0, err
	}
	if resp == nil || resp.Data.ID == 0 {
		return 0, fmt.Errorf("imap: COPY to %q produced no stored message (quarantined or discarded)", dest)
	}
	newUID := toUID(resp.Data.ID)
	if newUID == 0 {
		return 0, fmt.Errorf("imap: COPY to %q assigned an out-of-range UID (%d)", dest, resp.Data.ID)
	}
	return newUID, nil
}

// Append delivers raw RFC 2822 bytes into dest (base Mailbox.Append). It delegates
// to AppendUID and discards the reported UID, so the two paths never diverge.
func (m *mailbox) Append(dest string, f imapsrv.FlagUpdate, raw []byte) error {
	_, err := m.AppendUID(dest, f, raw)
	return err
}

// AppendUID delivers raw RFC 2822 bytes into dest (APPEND), applying any flags the
// client supplied to the newly delivered message, and returns that message's UID
// (UIDPLUS, RFC 4315) — the ID the delivery assigned, which is rest-mail's UID —
// for the APPENDUID response code.
//
// The message is created directly in dest with its flags in a single delivery, so
// there is no transient INBOX state and no separate, error-swallowing move/flag
// step. A delivery that produces no message (pipeline quarantine/discard, or an
// out-of-range UID) is surfaced as an error rather than reported as a bogus
// success carrying UID 0.
func (m *mailbox) AppendUID(dest string, f imapsrv.FlagUpdate, raw []byte) (uint32, error) {
	if err := validateFolder(dest); err != nil {
		return 0, err
	}
	// Parse basic headers from raw message for the structured delivery fields.
	subject, bodyText, bodyHTML, messageID, senderName := parseBasicHeaders(raw)

	deliverReq := &apiclient.DeliverRequest{
		Address:    m.email,
		Folder:     dest,
		Sender:     m.email,
		SenderName: senderName,
		Subject:    subject,
		BodyText:   bodyText,
		BodyHTML:   bodyHTML,
		MessageID:  messageID,
		RawMessage: raw,
	}

	// Apply the client-supplied APPEND flags at creation time. \Flagged writes both
	// is_flagged and is_starred so it round-trips through FETCH's
	// is_flagged || is_starred fold, matching Store's symmetric mapping.
	if f.Seen != nil {
		deliverReq.IsRead = f.Seen
	}
	if f.Flagged != nil {
		deliverReq.IsFlagged = f.Flagged
		deliverReq.IsStarred = f.Flagged
	}
	if f.Draft != nil {
		deliverReq.IsDraft = f.Draft
	}

	resp, err := m.api.DeliverMessage(deliverReq)
	if err != nil {
		return 0, err
	}
	if resp == nil || resp.Data.ID == 0 {
		return 0, fmt.Errorf("imap: APPEND to %q produced no stored message (quarantined or discarded)", dest)
	}
	newUID := toUID(resp.Data.ID)
	if newUID == 0 {
		return 0, fmt.Errorf("imap: APPEND to %q assigned an out-of-range UID (%d)", dest, resp.Data.ID)
	}
	return newUID, nil
}

// UIDValidity reports the UIDVALIDITY for a folder (UIDPlusMailbox, RFC 4315).
// rest-mail uses a global message-ID-as-UID model: a message's ID is its IMAP UID,
// is assigned once and never reused, so a single constant UIDVALIDITY holds
// account-wide. Returning 1 matches the pre-UIDPLUS hardcoded SELECT
// [UIDVALIDITY 1] behaviour. Per-folder UIDVALIDITY is future work.
func (m *mailbox) UIDValidity(folder string) (uint32, error) {
	return 1, nil
}

// Quota returns storage use and limit in bytes.
func (m *mailbox) Quota() (used, limit int64, err error) {
	quota, err := m.api.GetQuota(m.token, m.accountID)
	if err != nil {
		return 0, 0, err
	}
	return quota.Data.QuotaUsedBytes, quota.Data.QuotaBytes, nil
}
