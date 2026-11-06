package smtp

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/trace"

	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
)

// Backend is the exact set of REST API operations the SMTP gateway depends on.
// It is deliberately narrow: only the methods this protocol actually calls.
//
// The concrete *apiclient.Client satisfies this interface structurally, so the
// production wiring keeps passing the real client unchanged.
type Backend interface {
	// OpenSession signs a mail client in at submission AUTH, with one of the
	// account's passwords or an app password, and opens the connection's
	// session with the API (#1586), with the submission service's certificate.
	OpenSession(email, password, protocol string) (*apitypes.GatewaySessionOpened, error)
	// CloseSession ends the connection's session, at QUIT or disconnect.
	CloseSession(token string) error
	// CheckMailbox verifies a recipient address exists locally.
	CheckMailbox(address string) (*apiclient.MailboxCheckResponse, error)
	// DeliverMessageFrom delivers a message to a local mailbox, reading its large
	// fields from content as it sends them.
	DeliverMessageFrom(req *apiclient.DeliverRequest, content apiclient.DeliverContent) (*apiclient.DeliverResponse, error)
}

// Clients is the SMTP gateway's Backend: two services, each calling the API
// with its own client certificate (#1586). Inbound delivery acts for no
// account, with the rest-mail-smtp-delivery certificate; submission opens and
// closes a signed-in mail client's session with rest-mail-smtp-submission.
type Clients struct {
	Delivery   *apiclient.Client
	Submission *apiclient.Client
}

var _ Backend = Clients{}

// WithRequestID returns the clients for one connection, sending its ID on every
// call to the API (#1580).
func (c Clients) WithRequestID(id string) Backend {
	out := c
	if c.Delivery != nil {
		out.Delivery = c.Delivery.WithRequestID(id)
	}
	if c.Submission != nil {
		out.Submission = c.Submission.WithRequestID(id)
	}
	return out
}

// withRequestID is b for one connection with ID id, when b can carry one.
// WithSpan returns the clients for one connection, tracing each call to the
// API as a child of span, the connection's span (#1580).
func (c Clients) WithSpan(span trace.Span) Backend {
	out := c
	if c.Delivery != nil {
		out.Delivery = c.Delivery.WithSpan(span)
	}
	if c.Submission != nil {
		out.Submission = c.Submission.WithSpan(span)
	}
	return out
}

// withSpan is withRequestID for the connection's span.
func withSpan(b Backend, span trace.Span) Backend {
	if t, ok := b.(interface{ WithSpan(trace.Span) Backend }); ok {
		return t.WithSpan(span)
	}
	return b
}

func withRequestID(b Backend, id string) Backend {
	if t, ok := b.(interface{ WithRequestID(string) Backend }); ok {
		return t.WithRequestID(id)
	}
	return b
}

func (c Clients) OpenSession(email, password, protocol string) (*apitypes.GatewaySessionOpened, error) {
	return c.Submission.OpenSession(email, password, protocol)
}

func (c Clients) CloseSession(token string) error { return c.Submission.CloseSession(token) }

func (c Clients) CheckMailbox(address string) (*apiclient.MailboxCheckResponse, error) {
	return c.Delivery.CheckMailbox(address)
}

func (c Clients) DeliverMessageFrom(req *apiclient.DeliverRequest, content apiclient.DeliverContent) (*apiclient.DeliverResponse, error) {
	return c.Delivery.DeliverMessageFrom(req, content)
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
	// AccountMaySend reports whether the authenticated account may send as
	// its own mailbox now: whether it holds mail:send there, within the
	// server's capability ceiling. Checked for every message, so removing the
	// capability stops sessions already signed in.
	AccountMaySend(accountID uint) (bool, error)
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
	// HostedDomain reports whether this server hosts the domain.
	HostedDomain(domain string) (bool, error)
	// RelayBounce queues a bounce that came back to an SRS address for the sender
	// that address carries, with a null reverse path (#775). Past the limit it
	// queues nothing and reports no error, as a DSN over the limit is dropped.
	RelayBounce(msg BounceRelay) error
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
	PrepareSubmitted(ctx context.Context, sender, raw string) (string, error)
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

// BounceRelay is a bounce to pass on to the sender of mail this server forwarded.
type BounceRelay struct {
	Recipient     string // the original sender, from the SRS address
	SendingDomain string // the hosted domain the SRS address is under
	RawMessage    string
	BodyType      string
	// MaxPerRecipient and Window are the per-recipient DSN limit (#715). A
	// non-positive MaxPerRecipient means no limit.
	MaxPerRecipient int
	Window          time.Duration
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
