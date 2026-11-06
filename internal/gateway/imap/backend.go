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

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	"github.com/rest-mail/rest-mail-server/internal/gateway/rawmsg"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/metrics"
	"github.com/rest-mail/rest-mail-server/internal/requestid"
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
// ObserveCommand records restmail_imap_command_duration_seconds for one
// command (goimap.CommandObserver). Names outside metrics.IMAPCommandNames
// are labelled "other" (#1579).
func (b *Backend) ObserveCommand(name string, d time.Duration) {
	metrics.ObserveIMAPCommand(name, d)
}

var _ goimap.CommandObserver = (*Backend)(nil)

func (b *Backend) Authenticate(user, pass string) (goimap.Mailbox, error) {
	// One ID per session (#1580): on every line logged for it, and sent to
	// the API on each call the session makes.
	id := requestid.New()
	log := slog.Default().With(requestid.Attr, id)
	api := b.api.WithRequestID(id)

	// Per-account hard stop: reject a banned account WITHOUT calling the auth API,
	// so a rotating-IP brute-force is throttled and a ban is never lifted by a
	// lucky correct guess. The per-IP ban is enforced separately by the server.
	if b.limiter != nil && b.limiter.IsUserBanned(user) {
		log.Warn("imap: auth rejected: account temporarily locked",
			"user", maskEmail(user),
			"event", "imap_auth_banned",
		)
		return nil, errAccountLocked
	}

	// The connection gets its own session with the API (#1586): the address
	// and password go once, and every call that follows sends the session's
	// token. An app password signs in without the API's interactive login (#344).
	resp, err := api.OpenSession(user, pass, "imap")
	if err != nil {
		// A failed IMAP login is a security-relevant event (credential stuffing /
		// enumeration). Log it structured, with the attacker-controlled username
		// masked, so it is observable alongside the SMTP smtp_auth_failed events.
		log.Warn("imap: auth failed",
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
		api:       api,
		log:       log,
		email:     user,
		token:     resp.Token,
		accountID: resp.User.ID,
	}, nil
}

// logger is the session's logger, carrying its request ID when it has one.
func (m *mailbox) logger() *slog.Logger {
	if m.log != nil {
		return m.log
	}
	return slog.Default()
}

// Close ends the connection's session with the API, at LOGOUT or disconnect.
func (m *mailbox) Close() error {
	return m.api.CloseSession(m.token)
}

// mailbox is one authenticated account's view of its mail, for one session.
type mailbox struct {
	api *apiclient.Client
	// log carries the session's request ID (#1580); nil logs without it.
	log       *slog.Logger
	email     string
	token     string
	accountID uint
	// utf8 is set once the client has sent ENABLE UTF8=ACCEPT (RFC 6855 §3):
	// from then on it takes raw UTF-8 header fields, so FETCH stops
	// downgrading them.
	utf8 bool
	// clientID is what the client said it is with ID (RFC 2971), so an import
	// summary (#1498) can name the tool. It is never used to decide how to
	// serve the client (RFC 2971 §3).
	clientID goimap.ClientID
	// imports are this connection's import sessions by account (#1498): one
	// connection that APPENDs or COPYs is one import, audited as one summary.
	// Opened at the first import into an account, closed by EndSession.
	imports map[uint]string
}

var _ goimap.MailboxCloser = (*mailbox)(nil)

// The mailbox also satisfies the library's optional UIDPLUS (RFC 4315) and atomic
// MOVE (RFC 6851) interfaces, so the server advertises UIDPLUS and emits
// APPENDUID / COPYUID response codes and honours UID EXPUNGE for rest-mail. It
// also takes the client's APPEND date-time as the INTERNALDATE (DateAppender).
var (
	_ goimap.UIDPlusMailbox = (*mailbox)(nil)
	_ goimap.Mover          = (*mailbox)(nil)
	_ goimap.UTF8Mailbox    = (*mailbox)(nil)
	_ goimap.DateAppender   = (*mailbox)(nil)
	_ goimap.IDMailbox      = (*mailbox)(nil)
	_ goimap.SessionEnder   = (*mailbox)(nil)
	_ goimap.Subscriber     = (*mailbox)(nil)
)

// importMessage stores a message through the import operation, counted in this
// connection's import session for the account. A session the API closed for being
// idle is replaced; one that cannot be opened leaves the import to be audited on
// its own, so the API, not the gateway, decides whether the import may happen.
func (m *mailbox) importMessage(accountID uint, req *apiclient.ImportRequest) (*apiclient.ImportResponse, error) {
	resp, err := m.api.ImportMessage(m.token, accountID, m.importSession(accountID), req)
	if apiclient.IsImportSessionGone(err) {
		delete(m.imports, accountID)
		resp, err = m.api.ImportMessage(m.token, accountID, m.importSession(accountID), req)
	}
	return resp, err
}

func (m *mailbox) importSession(accountID uint) string {
	if session, ok := m.imports[accountID]; ok {
		return session
	}
	var body apitypes.OpenImportSessionBody
	body.ClientName, _ = m.clientID.Get("name")
	body.ClientVersion, _ = m.clientID.Get("version")
	session, err := m.api.OpenImportSession(m.token, accountID, body)
	if err != nil {
		m.logger().Warn("imap: could not open an import session; the import is audited on its own", "account_id", accountID, "error", err)
		return ""
	}
	if m.imports == nil {
		m.imports = map[uint]string{}
	}
	m.imports[accountID] = session
	return session
}

// EndSession closes this connection's import sessions, which writes their
// summaries. The library calls it once, at LOGOUT or disconnect.
func (m *mailbox) EndSession() {
	for accountID, session := range m.imports {
		if err := m.api.CloseImportSession(m.token, accountID, session); err != nil {
			m.logger().Warn("imap: closing the import session failed; the API closes it when idle", "account_id", accountID, "error", err)
		}
	}
	m.imports = nil
}

// SetClientID keeps the fields of the client's ID command (RFC 2971) for this
// session. The library calls it once the session is authenticated, whether the
// client sent ID before or after logging in.
func (m *mailbox) SetClientID(id goimap.ClientID) { m.clientID = id }

// ClientID returns what the client said it is with ID, or nil if it sent none.
func (m *mailbox) ClientID() goimap.ClientID { return m.clientID }

// Subscriptions is the account's subscription list, kept by the API (#1675), so
// LSUB and LIST (SUBSCRIBED) answer what SUBSCRIBE and UNSUBSCRIBE left, a
// deleted folder's name included (RFC 9051 §6.3.7).
func (m *mailbox) Subscriptions() ([]string, error) {
	return m.api.ListSubscriptions(m.token, m.accountID)
}

// Subscribe adds name to the account's subscription list (SUBSCRIBE).
func (m *mailbox) Subscribe(name string) error {
	return m.api.Subscribe(m.token, m.accountID, name)
}

// Unsubscribe removes name from the account's subscription list (UNSUBSCRIBE).
func (m *mailbox) Unsubscribe(name string) error {
	return m.api.Unsubscribe(m.token, m.accountID, name)
}

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
		out = append(out, goimap.Folder{Name: f.Folder, SpecialUse: rmail.RoleOf(f.Folder).SpecialUse()})
	}
	return out, nil
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
//
// It always asks the import for a new message. COPY adds a message to the end of
// dest (RFC 3501 §6.4.7), so a copy into the folder the source is in is a second
// message with a higher UID. Reporting the source's UID instead would tell a client
// that copies and then expunges the original to delete its only copy (#1553).
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
	resp, err := m.importMessage(src.MailboxID, &apiclient.ImportRequest{
		RawMessage:   []byte(m.rawMessage(src)),
		Folder:       dest,
		Flags:        flags,
		InternalDate: received,
		AlwaysStore:  true,
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
// judge it. APPEND adds a new message (RFC 3501 §6.3.11), so the same message again
// is another message with a higher UID, and it asks the import for that (#1553). The
// exception is a mail app's own copy of a message submission has just saved in Sent:
// it answers with that message's UID and stores nothing (#1707). An out-of-range UID
// is surfaced as an error rather than a bogus success carrying UID 0.
func (m *mailbox) AppendUID(dest string, f goimap.FlagUpdate, raw []byte) (uint32, error) {
	return m.appendAt(dest, f, time.Now(), raw)
}

// AppendWithDate is AppendUID for an APPEND that carries a date-time (DateAppender):
// the message keeps that instant as its INTERNALDATE, as RFC 3501 §6.3.11 and
// RFC 9051 §6.3.12 ask. Without a date-time the server calls AppendUID, which
// stores the current time.
func (m *mailbox) AppendWithDate(dest string, f goimap.FlagUpdate, internalDate time.Time, raw []byte) (uint32, error) {
	return m.appendAt(dest, f, internalDate, raw)
}

// appendAt stores raw in dest with internalDate as its INTERNALDATE and returns
// the UID the import assigned.
func (m *mailbox) appendAt(dest string, f goimap.FlagUpdate, internalDate time.Time, raw []byte) (uint32, error) {
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
	resp, err := m.importMessage(m.accountID, &apiclient.ImportRequest{
		RawMessage:   raw,
		Folder:       dest,
		Flags:        importFlags(f),
		InternalDate: internalDate,
		AlwaysStore:  true,
		// Only APPEND asks: COPY names a message the client can already see.
		MatchSubmission: true,
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
