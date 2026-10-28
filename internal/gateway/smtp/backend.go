package smtp

import "github.com/restmail/restmail/internal/gateway/apiclient"

// Backend is the exact set of REST API operations the SMTP gateway depends on.
// It is deliberately narrow: only the methods this protocol actually calls.
//
// The concrete *apiclient.Client satisfies this interface structurally, so the
// production wiring keeps passing the real client unchanged.
type Backend interface {
	// SignIn authenticates an account (submission AUTH), with one of its
	// passwords or an app password (apiclient.Client.SignIn).
	SignIn(email, password, protocol string) (*apiclient.SignInResponse, error)
	// CheckMailbox verifies a recipient address exists locally.
	CheckMailbox(address string) (*apiclient.MailboxCheckResponse, error)
	// DeliverMessage delivers a message to a local mailbox.
	DeliverMessage(req *apiclient.DeliverRequest) (*apiclient.DeliverResponse, error)
}

// Store abstracts the two direct-database reaches the SMTP session makes, so the
// session depends on neither gorm nor the DB models:
//
//   - sender authorization on the submission path (the account's grants),
//   - outbound queueing of messages bound for remote recipients.
//
// The production implementation is dbStore (see dbstore.go), wired in
// cmd/smtp-gateway. Tests substitute an in-memory Store.
type Store interface {
	// SenderMatchesAccount reports whether from resolves to the authenticated
	// mailbox ID. It preserves exact legacy identities when NFC-equivalent
	// accounts coexist; string normalization alone cannot establish ownership.
	SenderMatchesAccount(accountID uint, from string) (bool, error)
	// SenderAuthorized reports whether the authenticated account may send as
	// the given MAIL FROM address, another account's: whether it holds
	// mail:send on that account (it owns or has linked it). An error is
	// treated by the caller as "not authorized".
	SenderAuthorized(accountID uint, from string) (bool, error)
	// PersistSubmittedMessage records an authenticated submission as a message
	// row owned by the sender's mailbox and returns its id. The returned id is
	// threaded onto the outbound queue rows for the message's remote recipients
	// so a later bounce/DSN can be authenticated back to the submitting mailbox
	// (DSN sender-auth provenance). It returns (nil, nil) when the sender has no
	// local mailbox to attribute the message to; a returned error must not fail
	// the submission (delivery proceeds without a linked reference).
	PersistSubmittedMessage(msg SubmittedMessage) (*uint, error)
	// EnqueueOutbound appends a message to the outbound delivery queue for the
	// queue worker to deliver to a remote MX.
	EnqueueOutbound(msg OutboundMessage) error
	// SendingPolicy is the sending policy of the domain an address is on
	// (models.SendingPolicyBulk, models.SendingPolicyTransactional), or "" when
	// it has none or is not a domain this server hosts (#144).
	SendingPolicy(address string) (string, error)
	// CredentialUsable reports whether the app password a session signed in
	// with can still be used: not revoked, not expired, its mailbox active.
	// Checked before each message, so revoking one stops it sending at once.
	CredentialUsable(credentialID uint) (bool, error)
	// PrepareSubmitted applies the outbound transforms (outbound.Prepare) to
	// an authenticated submission before a local mailbox receives it: the Bcc
	// header stripped, and the message DKIM-signed with the sender domain's
	// key. An error means a configured key could not sign; the caller
	// defers rather than deliver the message unsigned.
	PrepareSubmitted(sender, raw string) (string, error)
}

// OutboundMessage is a neutral description of a queued outbound message, keeping
// the DB model out of the SMTP session.
type OutboundMessage struct {
	Sender     string
	Recipient  string
	Domain     string // destination domain for MX lookup
	RawMessage string // RFC 2822 formatted message
	// BodyType is the SMTP BODY= parameter the client declared on MAIL FROM
	// (7BIT/8BITMIME/BINARYMIME, or empty). It is persisted on the queue row so
	// the outbound worker never relays 8-bit content to a next hop that does not
	// advertise 8BITMIME (RFC 6152).
	BodyType string
	// MessageID links the queue row to the stored submission reference (a
	// messages row owned by the sender), so a bounce/DSN for this recipient can
	// verify its sender against the real submission. Nil when no reference was
	// persisted (e.g. the sender has no local mailbox).
	MessageID *uint
	// RequireTLS is the REQUIRETLS tag of the MAIL FROM it came with (RFC 8689 §4.1).
	RequireTLS bool
}

// SubmittedMessage is a neutral description of an authenticated submission the
// Store persists as a sender-owned message reference, keeping the DB model out
// of the SMTP session. RecipientsTo/RecipientsCc are pre-marshaled JSON (nil
// when empty).
type SubmittedMessage struct {
	Sender       string
	MessageID    string // RFC 5322 Message-ID header
	SenderName   string
	Subject      string
	BodyText     string
	BodyHTML     string
	InReplyTo    string
	References   string
	RawMessage   string
	RecipientsTo []byte
	RecipientsCc []byte
}
