package smtp

import "github.com/restmail/restmail/internal/gateway/apiclient"

// Backend is the exact set of REST API operations the SMTP gateway depends on.
// It is deliberately narrow: only the methods this protocol actually calls.
//
// The concrete *apiclient.Client satisfies this interface structurally, so the
// production wiring keeps passing the real client unchanged.
type Backend interface {
	// Login authenticates a mailbox user (submission AUTH).
	Login(email, password string) (*apiclient.LoginResponse, error)
	// CheckMailbox verifies a recipient address exists locally.
	CheckMailbox(address string) (*apiclient.MailboxCheckResponse, error)
	// DeliverMessage delivers a message to a local mailbox.
	DeliverMessage(req *apiclient.DeliverRequest) (*apiclient.DeliverResponse, error)
}

// Store abstracts the two direct-database reaches the SMTP session makes, so the
// session depends on neither gorm nor the DB models:
//
//   - sender authorization on the submission path (linked_accounts -> mailboxes),
//   - outbound queueing of messages bound for remote recipients.
//
// The production implementation is dbStore (see dbstore.go), wired in
// cmd/smtp-gateway. Tests substitute an in-memory Store.
type Store interface {
	// SenderAuthorized reports whether the authenticated webmail account may send
	// as the given MAIL FROM address (i.e. it owns a linked mailbox with that
	// address). An error is treated by the caller as "not authorized".
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
