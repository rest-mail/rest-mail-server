// Package imap adapts the rest-mail REST API to the github.com/rest-mail/go-imap
// server library: it implements the library's Backend/Mailbox interfaces by
// mapping apiclient responses onto the library's neutral types. The protocol
// engine itself lives in the library; this package only knows how rest-mail
// stores mail.
package imap

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"

	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	"github.com/rest-mail/rest-mail-server/internal/gateway/rawmsg"
)

// errAccountLocked is returned when an account is temporarily banned for too
// many recent auth failures. The go-imap engine maps any Authenticate error to a
// tagged NO, so the message is not surfaced verbatim to the client; it exists so
// the hard-stop path is distinguishable in code and logs.
var errAccountLocked = errors.New("too many authentication failures")

// Backend authenticates IMAP users against the rest-mail REST API.
type Backend struct {
	api *apiclient.Client
	// limiter throttles brute-force per ACCOUNT, complementing the per-IP guard
	// the go-imap server already applies. It is separate because the library's
	// per-IP tracker cannot see a distributed attack that rotates source IPs
	// against one account. A nil limiter disables per-account throttling.
	limiter *connlimiter.Limiter
}

// NewBackend creates a Backend over the given API client. limiter enables
// per-account brute-force throttling (pass nil to disable it).
func NewBackend(api *apiclient.Client, limiter *connlimiter.Limiter) *Backend {
	return &Backend{api: api, limiter: limiter}
}

var _ goimap.Backend = (*Backend)(nil)

// Authenticate signs the user in via the API, with the mailbox's password or
// an app password, and returns a Mailbox scoped to the resulting token and
// account.
func (b *Backend) Authenticate(user, pass string) (goimap.Mailbox, error) {
	// Per-account hard stop: reject a banned account WITHOUT calling the auth API,
	// so a rotating-IP brute-force is throttled and a ban is never lifted by a
	// lucky correct guess. The per-IP ban is enforced separately by the server.
	if b.limiter != nil && b.limiter.IsUserBanned(user) {
		slog.Warn("imap: auth rejected: account temporarily locked",
			"user", maskEmail(user),
			"event", "imap_auth_banned",
		)
		return nil, errAccountLocked
	}

	// An app password signs in without the API's interactive login (#344).
	resp, err := b.api.SignIn(user, pass, "imap")
	if err != nil {
		// A failed IMAP login is a security-relevant event (credential stuffing /
		// enumeration). Log it structured, with the attacker-controlled username
		// masked, so it is observable alongside the SMTP smtp_auth_failed events.
		slog.Warn("imap: auth failed",
			"user", maskEmail(user),
			"event", "imap_auth_failed",
		)
		// Only a definitive credential rejection (401/403) counts against the
		// per-account ban; a transient API/network error leaves the password
		// unverified and must not lock the account out.
		if b.limiter != nil && apiclient.IsAuthRejection(err) {
			b.limiter.RecordAuthFailUser(user)
		}
		return nil, err
	}
	if b.limiter != nil {
		b.limiter.ResetAuthUser(user)
	}
	return &mailbox{
		api:       b.api,
		email:     user,
		token:     resp.AccessToken,
		accountID: resp.Data.User.ID,
	}, nil
}

// mailbox is one authenticated account's view of its mail, for one session.
type mailbox struct {
	api       *apiclient.Client
	email     string
	token     string
	accountID uint
	// utf8 is set once the client has sent ENABLE UTF8=ACCEPT (RFC 6855 §3):
	// from then on it takes raw UTF-8 header fields, so FETCH stops
	// downgrading them.
	utf8 bool
}

var _ goimap.Mailbox = (*mailbox)(nil)

// The mailbox also satisfies the library's optional UIDPLUS (RFC 4315) and atomic
// MOVE (RFC 6851) interfaces, so the server advertises UIDPLUS and emits
// APPENDUID / COPYUID response codes and honours UID EXPUNGE for rest-mail.
var (
	_ goimap.UIDPlusMailbox = (*mailbox)(nil)
	_ goimap.Mover          = (*mailbox)(nil)
	_ goimap.UTF8Mailbox    = (*mailbox)(nil)
)

// EnableUTF8 switches this session to UTF-8 (RFC 6855 UTF8=ACCEPT). The
// library advertises the capability because the mailbox implements this, and
// calls it once when the client enables the mode.
func (m *mailbox) EnableUTF8() error {
	m.utf8 = true
	return nil
}

// Folders lists the account's folders.
func (m *mailbox) Folders() ([]goimap.Folder, error) {
	resp, err := m.api.ListFolders(m.token, m.accountID)
	if err != nil {
		return nil, err
	}
	out := make([]goimap.Folder, 0, len(resp.Data))
	for _, f := range resp.Data {
		out = append(out, goimap.Folder{Name: f.Folder, SpecialUse: specialUse[f.Folder]})
	}
	return out, nil
}

// specialUse is the role (RFC 6154) of each of REST Mail's standard folders,
// the ones every mailbox has and none can delete (the API's standardFolders):
// a mail app then files junk into Spam and archives into Archive rather than
// making folders of its own beside them. Any other folder has no role.
var specialUse = map[string]string{
	"Sent":    goimap.SpecialUseSent,
	"Drafts":  goimap.SpecialUseDrafts,
	"Trash":   goimap.SpecialUseTrash,
	"Spam":    goimap.SpecialUseJunk,
	"Archive": goimap.SpecialUseArchive,
}

// Messages returns a folder's messages oldest-first, mapped onto the library's
// neutral Message type. The message ID doubles as the IMAP UID.
func (m *mailbox) Messages(folder string) ([]goimap.Message, error) {
	if err := validateFolder(folder); err != nil {
		return nil, err
	}
	resp, err := m.api.ListIMAPMessages(m.token, m.accountID, folder)
	if err != nil {
		return nil, err
	}
	out := make([]goimap.Message, 0, len(resp.Data))
	for _, msg := range resp.Data {
		item := toMessage(msg)
		// FETCH sends the downgraded form to a session that has not enabled
		// UTF-8, so RFC822.SIZE counts that form's octets.
		item.Size = msg.WireSizeFor(m.utf8)
		out = append(out, item)
	}
	return out, nil
}

// toMessage maps an apiclient summary onto the library's neutral Message.
// Starred folds into \Flagged, matching the previous gateway's flag mapping.
// Size uses WireSize: the exact stored-raw octet count when the server has
// one (RFC 3501 requires RFC822.SIZE to be the transmitted size exactly),
// falling back to size_bytes for messages without a stored raw. The UID uses
// toUID, the same 32-bit clamp APPENDUID/COPYUID apply, so an ID past the UID
// space clamps to 0 rather than wrapping to a small, wrong UID.
func toMessage(msg apiclient.MessageSummary) goimap.Message {
	return goimap.Message{
		UID:      toUID(msg.ID),
		Size:     msg.WireSize(),
		Answered: msg.IsAnswered,
		Seen:     msg.IsRead,
		Flagged:  msg.IsFlagged || msg.IsStarred,
		Draft:    msg.IsDraft,
		Deleted:  msg.IMAPDeleted,
		Keywords: msg.Keywords,
		Subject:  msg.Subject,
		From:     goimap.Address{Name: msg.SenderName, Email: msg.Sender},
		To:       string(msg.RecipientsTo),
		Date:     msg.ReceivedAt,
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
	raw := m.rawMessage(detail.Data)
	if m.utf8 {
		return []byte(raw), nil
	}
	// A client that has not enabled UTF-8 gets headers downgraded to ASCII
	// (RFC 6855 §3, RFC 6857).
	return []byte(rawmsg.Downgrade(raw)), nil
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
func (m *mailbox) Store(uid uint32, f goimap.FlagUpdate) error {
	updates := apiclient.MessageUpdate{IsAnswered: f.Answered, IsRead: f.Seen, IsFlagged: f.Flagged, IsStarred: f.Flagged, IsDraft: f.Draft, IMAPDeleted: f.Deleted}
	if f.KeywordsSet {
		updates.Keywords = &f.Keywords
	}
	if !f.KeywordsSet && f.Seen == nil && f.Answered == nil && f.Flagged == nil && f.Draft == nil && f.Deleted == nil {
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
	seen, flagged, draft, answered := src.IsRead, src.IsFlagged || src.IsStarred, src.IsDraft, src.IsAnswered
	received := src.ReceivedAt

	flags := importFlags(goimap.FlagUpdate{Answered: &answered, Seen: &seen, Flagged: &flagged, Draft: &draft, Deleted: &src.IMAPDeleted, Keywords: src.Keywords})
	resp, err := m.api.ImportMessage(m.token, src.MailboxID, &apiclient.ImportRequest{
		RawMessage:   []byte(m.rawMessage(src)),
		Folder:       dest,
		Flags:        flags,
		InternalDate: received,
	})
	if err != nil {
		return 0, err
	}
	if resp == nil || resp.Data.ID == 0 {
		return 0, fmt.Errorf("imap: COPY to %q produced no stored message", dest)
	}
	newUID := toUID(resp.Data.ID)
	if newUID == 0 {
		return 0, fmt.Errorf("imap: COPY to %q assigned an out-of-range UID (%d)", dest, resp.Data.ID)
	}
	return newUID, nil
}

// Append stores raw RFC 2822 bytes in dest (base Mailbox.Append). It delegates
// to AppendUID and discards the reported UID, so the two paths never diverge.
func (m *mailbox) Append(dest string, f goimap.FlagUpdate, raw []byte) error {
	_, err := m.AppendUID(dest, f, raw)
	return err
}

// AppendUID stores raw RFC 2822 bytes in dest (APPEND), with any flags the client
// supplied, and returns that message's UID (UIDPLUS, RFC 4315) — the ID the import
// assigned, which is rest-mail's UID — for the APPENDUID response code.
//
// This is the account's own client storing its own mail, not mail arriving, so it
// goes through the import operation as the signed-in account and no delivery rules
// judge it. The same message again answers with the copy already there. An
// out-of-range UID is surfaced as an error rather than a bogus success carrying
// UID 0.
func (m *mailbox) AppendUID(dest string, f goimap.FlagUpdate, raw []byte) (uint32, error) {
	if err := validateFolder(dest); err != nil {
		return 0, err
	}
	// Normalize line endings to CRLF at ingest. An APPEND'd LF-only message would
	// otherwise be stored with bare LFs, breaking POP3 RETR/TOP framing (go-pop3
	// splits on CRLF and does not dot-stuff bare-LF lines, risking premature
	// termination) and diverging from the CRLF wire form of every other stored
	// message. Done before parsing/storing so both the parsed fields and the
	// persisted raw are consistent.
	raw = normalizeToCRLF(raw)
	resp, err := m.api.ImportMessage(m.token, m.accountID, &apiclient.ImportRequest{
		RawMessage:   raw,
		Folder:       dest,
		Flags:        importFlags(f),
		InternalDate: time.Now(),
	})
	if err != nil {
		return 0, err
	}
	if resp == nil || resp.Data.ID == 0 {
		return 0, fmt.Errorf("imap: APPEND to %q produced no stored message", dest)
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

// importFlags is the IMAP system flags and keywords of f that are set. A flag that is unset or
// false is left out, since the import starts every flag off.
func importFlags(f goimap.FlagUpdate) []string {
	var flags []string
	for _, flag := range []struct {
		set  *bool
		name string
	}{{f.Seen, `\Seen`}, {f.Answered, `\Answered`}, {f.Flagged, `\Flagged`}, {f.Draft, `\Draft`}, {f.Deleted, `\Deleted`}} {
		if flag.set != nil && *flag.set {
			flags = append(flags, flag.name)
		}
	}
	return append(flags, f.Keywords...)
}
