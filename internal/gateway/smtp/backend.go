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
}
