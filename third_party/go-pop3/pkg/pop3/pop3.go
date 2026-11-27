package pop3

import (
	"github.com/rest-mail/go-pop3/pkg/pop3/internal/wire"
)

// Message is one message in a POP3 maildrop, presented oldest-first. Its slice
// position (+1) is the POP3 message number the client uses for the session.
type Message struct {
	// UID is the persistent unique identifier a client sees via UIDL (RFC 1939
	// §7). The server also passes it back to [Mailbox.Retrieve], [Mailbox.MarkSeen]
	// and [Mailbox.Delete] to name this message.
	//
	// The backend must supply a UID that satisfies the RFC 1939 §7 unique-id
	// contract:
	//
	//   - Grammar: 1 to 70 characters, each a printable ASCII byte in the range
	//     0x21 ('!') to 0x7E ('~') inclusive — no spaces, control characters
	//     (including CR and LF), DEL, or non-ASCII bytes.
	//   - Unique: no two messages in the same maildrop may share a UID.
	//   - Persistent across sessions: a leave-on-server client de-duplicates by
	//     UID, so the same message must present the same UID on every future
	//     connection — even after a session ends without reaching the UPDATE
	//     state. Do not hand out sequence numbers or per-session identifiers.
	//
	// When the engine builds a UIDL response it validates each UID against the
	// grammar and checks the listing for duplicates; a malformed UID, or a
	// maildrop containing a duplicate UID, is answered with -ERR rather than
	// emitting a reply that would corrupt the protocol framing or mislead the
	// client. Persistence across sessions cannot be checked by the engine and
	// remains the backend's responsibility. UIDs passed to Retrieve, MarkSeen and
	// Delete are treated as opaque handles and are not re-validated.
	UID string
	// Size is the message's exact octet count as reported by STAT and LIST — the
	// "exact size of the message in octets" of RFC 1939 §5's scan listing. It MUST
	// be the number of octets RETR transmits for this message, measured on the
	// canonical CRLF wire form: RFC 1939 §11 defines the count by normalizing the
	// stored end-of-line convention to CRLF (a lone LF counts as the two octets
	// CR LF), and the §3 byte-stuffing of leading-dot lines and the CRLF.CRLF
	// terminator are transport framing that is NOT counted. This is exactly the
	// value RETR advertises in its "+OK <n> octets" reply, so a client that
	// pre-allocates or verifies against the LIST/STAT size matches what it
	// receives. Compute it with [OctetCount] on the same bytes [Mailbox.Retrieve]
	// returns; a size derived any other way (e.g. the raw on-disk length of
	// bare-LF content) will disagree with RETR.
	Size int
	// Seen reports whether the message is already marked read. RETR issues a
	// MarkSeen only for a message that was previously unseen; TOP never does.
	Seen bool
}

// OctetCount returns the RFC 1939 §11 octet count of a stored message: the number
// of octets RETR transmits for it, measured on the canonical CRLF wire form. Line
// endings are normalized to CRLF exactly as RETR does before transmission (a lone
// LF or a lone CR-then-LF becomes CR LF, so an end-of-line stored as a single
// character counts as two octets, per §11), while a bare CR that is not a line
// terminator is left unchanged. A message whose last line has no end-of-line
// counts the CRLF RETR ends that line with (every line of a §3 multi-line
// response ends in CRLF); an empty message counts 0. The §3 byte-stuffing of
// leading-dot lines and the CRLF.CRLF terminator are transport framing and are
// deliberately excluded, so the result matches the "+OK <n> octets" count RETR
// advertises and the length of the message a client decodes.
//
// A [Backend] computes [Message.Size] with OctetCount over the same bytes
// [Mailbox.Retrieve] returns so that STAT and LIST agree with RETR to the octet.
func OctetCount(raw []byte) int {
	return len(wire.WireMessage(string(raw)))
}

// Backend authenticates POP3 users. A server.Session calls Authenticate for each
// complete USER/PASS attempt until it enters TRANSACTION.
type Backend interface {
	// Authenticate validates the USER/PASS credentials. Returning a non-nil error
	// rejects the login (the client sees "-ERR ...") and counts as an auth
	// failure against the [Limiter], except for [ErrInUse], which reports valid
	// credentials but an already-open maildrop and receives [IN-USE] instead of
	// [AUTH], and [ErrLoginDelay], which reports valid credentials used too soon
	// after the last login and receives [LOGIN-DELAY]. Wrapped errors are
	// recognized with errors.Is. [ErrSystemTemporary] and [ErrSystemPermanent]
	// instead report system failures with [SYS/TEMP] or [SYS/PERM], without
	// recording or clearing credential-failure history or checking for a ban;
	// these may be returned even when credentials could not be verified. On
	// success it returns the [Mailbox] the session operates on.
	Authenticate(user, pass string) (Mailbox, error)
}

// LoginDelayBackend is an optional [Backend] extension that announces the
// RFC 2449 §6.5 LOGIN-DELAY capability. A Backend without it never has the tag
// in CAPA. Announcing a delay does not enforce one: the Backend still tracks
// login times and refuses early logins with [ErrLoginDelay].
type LoginDelayBackend interface {
	Backend
	// LoginDelay returns the minimum number of seconds between logins that CAPA
	// announces in AUTHORIZATION state. perUser reports that some users have a
	// longer delay; CAPA then appends USER, telling the client to re-issue CAPA
	// after login to learn its own value. A negative seconds value omits the tag.
	LoginDelay() (seconds int, perUser bool)
}

// LoginDelayMailbox is an optional [Mailbox] extension for a
// [LoginDelayBackend]. In TRANSACTION state CAPA announces the value it returns,
// the authenticated user's own delay in seconds, never followed by USER. A
// Mailbox without it announces the Backend's minimum. A negative value omits
// the tag.
type LoginDelayMailbox interface {
	Mailbox
	LoginDelay() int
}

// ImplementationBackend is an optional [Backend] extension that announces the
// RFC 2449 §6.9 IMPLEMENTATION capability, identifying the server software. A
// Backend without it never has the tag in CAPA. Clients must not change their
// behavior on it, so the text is informational only.
type ImplementationBackend interface {
	Backend
	// Implementation returns the text CAPA announces after IMPLEMENTATION, such
	// as "Example-POP3-Server-v1.2". transactionOnly hides the tag before login,
	// which RFC 2449 §6.9 permits so unauthenticated clients learn less. Text
	// that is empty, is not printable ASCII words separated by single spaces
	// (the RFC 2449 §9 param grammar) or would make the line longer than 512
	// octets omits the tag.
	Implementation() (text string, transactionOnly bool)
}

// UTF8Backend is an optional [Backend] extension for RFC 6856 internationalized
// mail. When the Backend implements it, CAPA advertises UTF8 and the engine
// accepts the UTF8 command in AUTHORIZATION state; a Backend without it never
// sees either, and its clients keep the plain RFC 1939 behavior.
type UTF8Backend interface {
	Backend
	// AuthenticateUTF8 replaces [Backend.Authenticate] for a UTF8Backend. utf8
	// reports whether the client issued UTF8 before logging in. When it is
	// false, RFC 6856 §3 forbids serving messages with raw UTF-8 headers, so the
	// returned Mailbox must downgrade them (RFC 6857), and [Message.Size] must
	// count the downgraded bytes that Retrieve returns. A message with no
	// downgraded form is refused with [ErrUTF8Required] instead.
	AuthenticateUTF8(user, pass string, utf8 bool) (Mailbox, error)
}

// SASLBackend is an optional [Backend] extension that enables the RFC 5034 AUTH
// command with the SASL PLAIN mechanism (RFC 4616). CAPA lists SASL PLAIN only
// while AUTH PLAIN is accepted: like USER/PASS, it is refused on a plaintext
// connection when a TLS config is present, until STLS succeeds. A Backend
// without it never sees AUTH, which stays an unknown command.
type SASLBackend interface {
	Backend
	// AuthenticatePlain validates SASL PLAIN credentials: authcid and pass log
	// in, and authzid is the identity the client asks to act as, empty when it
	// names none. A Backend that does not support acting as another user rejects
	// a non-empty authzid that differs from authcid. Errors mean what they mean
	// for [Backend.Authenticate], so a credential failure gets [AUTH] and counts
	// against the [Limiter]. utf8 is as for [UTF8Backend.AuthenticateUTF8], and
	// always false unless the Backend is also a UTF8Backend.
	AuthenticatePlain(authzid, authcid, pass string, utf8 bool) (Mailbox, error)
}

// Mailbox is a single authenticated POP3 maildrop. Every method is scoped to the
// user that [Backend.Authenticate] accepted; a Mailbox is used by one session.
type Mailbox interface {
	// Messages returns the maildrop contents oldest-first. It is called once,
	// immediately after each successful Authenticate call. Returning [ErrInUse]
	// (possibly wrapped) refuses transaction access with [IN-USE] and permits a
	// fresh USER/PASS attempt; it does not count as a credential failure.
	// [ErrSystemPermanent] refuses the login with [SYS/PERM]. Any other non-nil
	// error, including [ErrSystemTemporary], uses "-ERR [SYS/TEMP] ..." (RFC 3206
	// §4) and leaves the session in AUTHORIZATION; the error text is logged,
	// never sent to the client.
	Messages() ([]Message, error)
	// Retrieve returns the full RFC 5322 bytes of the message with the given UID.
	// RETR and TOP serve these bytes with line endings canonicalized to CRLF and
	// RFC 1939 dot-stuffing applied; a bare LF in the returned bytes is treated
	// as a line boundary and normalized to CRLF on the wire. [ErrUTF8Required]
	// refuses a message that needs UTF-8 mode with [UTF8]; any other error
	// replies "-ERR Failed to retrieve message".
	Retrieve(uid string) ([]byte, error)
	// MarkSeen flags a message read after a successful RETR. POP3 has no read
	// state of its own; implementations without one may return nil.
	MarkSeen(uid string) error
	// Delete permanently removes a message. The server calls it on QUIT, once for
	// each message the client DELEted during the session.
	Delete(uid string) error
}

// Limiter is the per-IP connection and authentication guard the server.Server
// consults. It is a structural interface: any type with these methods satisfies
// it. Pass [NopLimiter] (or nil) to impose no limits.
type Limiter interface {
	// Accept reports whether a new connection from ip may proceed, incrementing
	// the in-use count when it returns true.
	Accept(ip string) bool
	// Release decrements the in-use count for ip when a connection ends.
	Release(ip string)
	// RecordAuthFail records an authentication failure from ip.
	RecordAuthFail(ip string)
	// IsBanned reports whether ip is currently banned.
	IsBanned(ip string) bool
	// ResetAuth clears the recorded auth-failure history for ip after a success.
	ResetAuth(ip string)
}

// NopLimiter is a [Limiter] that imposes no limits: it accepts every connection
// and never bans. It is the default when a nil Limiter is passed to server.New
// or server.NewSession.
type NopLimiter struct{}

func (NopLimiter) Accept(string) bool    { return true }
func (NopLimiter) Release(string)        {}
func (NopLimiter) RecordAuthFail(string) {}
func (NopLimiter) IsBanned(string) bool  { return false }
func (NopLimiter) ResetAuth(string)      {}
