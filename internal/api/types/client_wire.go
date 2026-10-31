// Wire declarations are shared by the endpoint registrations and their Go clients.
// Lowercase declaration names retain the generated schema identities; exported aliases
// let clients construct the same values without keeping their own copies.
package apitypes

import (
	"encoding/json"
	"time"

	"github.com/antimatter-studios/go-oapifly"

	"github.com/restmail/restmail/internal/grants"
	"github.com/restmail/restmail/internal/pipeline"
)

type loginRequest struct {
	// Email is the account's address.
	Email    string `json:"email" minLength:"1" pattern:"\\S" message:"required: the account's address" missing:"required: the account's address"`
	Password string `json:"password" minLength:"1" message:"required" missing:"required"`
	// TOTPCode / RecoveryCode carry the second factor for accounts with 2FA
	// active (OSI-19). Both are optional and ignored for accounts without 2FA,
	// so non-2FA logins are byte-for-byte unchanged. A single-step login that
	// omits them for a 2FA account is answered with a totp_required challenge.
	TOTPCode     string `json:"totp_code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
	// Client is "native" for an app that is not a browser (the mobile client):
	// it gets the tokens in the response body instead of as cookies, and keeps
	// them in its own secure storage. Empty means a browser. Anything else is
	// refused, so a typo cannot quietly fall back to cookies.
	Client string `json:"client,omitempty" enums:"native" message:"must be \"native\" or omitted"`
}

type loginResponse struct {
	ExpiresIn          int            `json:"expires_in"`
	User               userInfo       `json:"user"`
	Capabilities       []string       `json:"capabilities,omitempty"`
	ScopedCapabilities []grants.Entry `json:"scoped_capabilities,omitempty"`
	// SetupRequired lists what the account must still do — "password" (POST
	// /api/v1/auth/password), "two_factor" (POST /api/v1/auth/2fa/enroll, then
	// /confirm) — before the session can reach anything else, which answers
	// 403 setup_required until then. Absent when the account is set up.
	SetupRequired []string `json:"setup_required,omitempty"`

	AccessToken      string `json:"access_token,omitempty"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	RefreshExpiresIn int    `json:"refresh_expires_in,omitempty"`
}

type userInfo struct {
	ID          uint   `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

type protocolSignInRequest struct {
	Protocol string `json:"protocol" enums:"imap,pop3,smtp" message:"must be \"imap\", \"pop3\" or \"smtp\"" missing:"must be \"imap\", \"pop3\" or \"smtp\""`
}

type sendMessageRequest struct {
	// OnBehalf uses From as the represented mailbox and the authenticated
	// account's address as RFC 5322 Sender. Requires mail:send_on_behalf.
	OnBehalf bool `json:"on_behalf,omitempty"`
	// Sender is resolved by the server, never accepted from a request body.
	Sender        string                  `json:"-"`
	From          string                  `json:"from,omitempty"`
	To            []string                `json:"to,omitempty"`
	Cc            []string                `json:"cc,omitempty"`
	Bcc           []string                `json:"bcc,omitempty"`
	Subject       string                  `json:"subject,omitempty"`
	BodyText      string                  `json:"body_text,omitempty"`
	BodyHTML      string                  `json:"body_html,omitempty"`
	InReplyTo     string                  `json:"in_reply_to,omitempty"`
	CalendarEvent *pipeline.CalendarEvent `json:"calendar_event,omitempty"`
	// ListUnsubscribe, when set, publishes where a recipient unsubscribes
	// (#144). Absent, the message carries no List-Unsubscribe fields.
	ListUnsubscribe *listUnsubscribe `json:"list_unsubscribe,omitempty"`
	// RequireTLS asks that every hop carry the message over TLS, as SMTP's
	// REQUIRETLS does (RFC 8689): it goes only to an MTA-STS host over verified
	// TLS that offers REQUIRETLS, and fails back to the sender otherwise (#724).
	RequireTLS bool `json:"require_tls,omitempty"`
	// ScheduledAt asks the server to send the message at that time instead of
	// now. It is not part of the replayed request; the stored copy omits it.
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
}

type deliverMessageBody struct {
	MailboxID    uint            `json:"mailbox_id,omitempty"`
	Address      string          `json:"address,omitempty"`
	Sender       string          `json:"sender,omitempty"`
	SenderName   string          `json:"sender_name,omitempty"`
	RecipientsTo json.RawMessage `json:"recipients_to,omitempty"`
	RecipientsCc json.RawMessage `json:"recipients_cc,omitempty"`
	Subject      string          `json:"subject,omitempty"`
	BodyText     string          `json:"body_text,omitempty"`
	BodyHTML     string          `json:"body_html,omitempty"`
	MessageID    string          `json:"message_id,omitempty"`
	InReplyTo    string          `json:"in_reply_to,omitempty"`
	References   string          `json:"references,omitempty"`
	// RawMessage is base64-decoded from the wire ([]byte, not string) so the
	// pristine RFC 2822 octets survive JSON transport unchanged — a JSON
	// string field would mangle every non-UTF-8 byte to U+FFFD (see #173).
	RawMessage []byte `json:"raw_message,omitempty"`
	ClientIP   string `json:"client_ip,omitempty"`
	HeloName   string `json:"helo_name,omitempty"`
	// Folder is the destination folder the message must be created in (empty =
	// INBOX). Set by the IMAP gateway's COPY/APPEND so the message is created
	// directly in its destination in one atomic delivery, rather than delivered
	// to INBOX and then moved. Flags and ReceivedAt (INTERNALDATE) let COPY
	// preserve the source message's flags/internal date and APPEND apply the
	// client-supplied flags at creation time. A nil flag pointer / nil (or zero)
	// ReceivedAt keeps the delivery default, so ordinary inbound mail is
	// unaffected.
	Folder     string     `json:"folder,omitempty"`
	IsAnswered *bool      `json:"is_answered,omitempty"`
	IsRead     *bool      `json:"is_read,omitempty"`
	IsFlagged  *bool      `json:"is_flagged,omitempty"`
	IsStarred  *bool      `json:"is_starred,omitempty"`
	IsDraft    *bool      `json:"is_draft,omitempty"`
	ReceivedAt *time.Time `json:"received_at,omitempty"`
	// Inbound transport-security metrics (always-on, inbound-MX only). A nil
	// ReceivedTLS means the caller is not an inbound-MX delivery, persisted as
	// NULL. TLSCipher is accepted for wire completeness but only the version is
	// persisted on the message.
	ReceivedTLS *bool  `json:"received_tls,omitempty"`
	TLSVersion  string `json:"tls_version,omitempty"`
	TLSCipher   string `json:"tls_cipher,omitempty"`
	// RequireTLS is the REQUIRETLS tag the message arrived with (RFC 8689 §4.1): a copy
	// REST Mail sends on, a Sieve redirect, goes with TLS required too.
	RequireTLS bool `json:"require_tls,omitempty"`
}

type listUnsubscribe struct {
	// Mailto is a bare address (no display name, no mailto: prefix) that
	// unsubscribes the recipient when it receives mail.
	Mailto string `json:"mailto,omitempty"`
	// HTTPS is an https URL that unsubscribes the recipient when it receives
	// the RFC 8058 one-click POST.
	HTTPS string `json:"https,omitempty"`
}

type createDomainRequest struct {
	Name string `json:"name" minLength:"1" message:"required"`
	// ServerType is restmail (the server keeps the mail) or traditional, the default when it
	// is left out, empty or null.
	ServerType        *string `json:"server_type,omitempty" enums:",traditional,restmail" message:"must be 'traditional' or 'restmail'"`
	DefaultQuotaBytes *int64  `json:"default_quota_bytes,omitempty"`
	SendingPolicy     string  `json:"sending_policy,omitempty" enums:",transactional,bulk" message:"must be 'transactional', 'bulk' or empty"`
	DMARCPolicy       *string `json:"dmarc_policy,omitempty" enums:"none,quarantine,reject" message:"must be 'none', 'quarantine' or 'reject'"`
}

type createAccountRequest struct {
	Address     string `json:"address" format:"email" formatMessage:"must be a mail address: local@domain"`
	DisplayName string `json:"display_name,omitempty"`
	// Password, when given, becomes the account's password credential, so it
	// can be signed in to. Without one the account has no credentials, and is
	// reached only through its owners.
	Password string `json:"password,omitempty" minLength:"12" minLengthMessage:"must be at least 12 characters"`
	// PasswordHash imports an existing crypt(3) or Argon2 password without resetting it.
	// Mutually exclusive with Password; never returned or written to audit.
	// Owned makes an owned account, an agent's or a sub account, owned by the
	// creator and never holding more than it does. It needs agents:create on
	// the domain and a session that signed in with a second factor.
	Owned        bool   `json:"owned,omitempty"`
	Receive      *bool  `json:"receive,omitempty" description:"Accept inbound mail through a mail:receive grant; defaults to true. False creates the account without that grant."`
	QuotaBytes   *int64 `json:"quota_bytes,omitempty" minimum:"0" description:"Storage cap in bytes; owned accounts inherit live when omitted or zero and a positive cap cannot exceed the owner's ceiling"`
	PasswordHash string `json:"password_hash,omitempty" maxLength:"256" pattern:"^((\\{BLF-CRYPT\\})?\\$2[aby]\\$(0[4-9]|1[0-4])\\$[./A-Za-z0-9]{53}|(\\{SHA256-CRYPT\\})?\\$5\\$(rounds=([1-9][0-9]{3,5}|1000000)\\$)?[./A-Za-z0-9]{1,16}\\$[./A-Za-z0-9]{43}|(\\{SHA512-CRYPT\\})?\\$6\\$(rounds=([1-9][0-9]{3,5}|1000000)\\$)?[./A-Za-z0-9]{1,16}\\$[./A-Za-z0-9]{86}|(\\{ARGON2ID\\})?\\$argon2id\\$v=19\\$m=([89]|[1-9][0-9]{1,4}|1[0-9]{5}|2[0-5][0-9]{4}|26[01][0-9]{3}|2620[0-9]{2}|2621[0-3][0-9]|26214[0-4]),t=([1-9]|10),p=[1-8]\\$[A-Za-z0-9+/]{11,86}\\$[A-Za-z0-9+/]{22,86}|(\\{ARGON2I\\})?\\$argon2i\\$v=19\\$m=([89]|[1-9][0-9]{1,4}|1[0-9]{5}|2[0-5][0-9]{4}|26[01][0-9]{3}|2620[0-9]{2}|2621[0-3][0-9]|26214[0-4]),t=([1-9]|10),p=[1-8]\\$[A-Za-z0-9+/]{11,86}\\$[A-Za-z0-9+/]{22,86}|)$"`
}

type accountResponse struct {
	ID          uint   `json:"id"`
	Address     string `json:"address"`
	DisplayName string `json:"display_name"`
	IsPrimary   bool   `json:"is_primary"`
	// OwnerID is the owning account of an owned account; absent otherwise.
	OwnerID *uint `json:"owner_id,omitempty"`
}

type updateMailboxRequest struct {
	// The handler counts bytes; minLength counts code points. Twelve code points
	// are always at least twelve bytes, so the schema never admits a password the
	// handler refuses.
	Password    *string `json:"password,omitempty" format:"password" minLength:"12" description:"At least 12 bytes when supplied; null leaves the password unchanged"`
	DisplayName *string `json:"display_name,omitempty"`
	QuotaBytes  *int64  `json:"quota_bytes,omitempty" description:"Storage cap in bytes; non-positive values inherit for an owned account, otherwise unlimited; a positive cap cannot exceed the owner's ceiling"`
	Active      *bool   `json:"active,omitempty"`
}

type updateMessageBody struct {
	IsAnswered *bool   `json:"is_answered,omitempty"`
	IsRead     *bool   `json:"is_read,omitempty"`
	IsFlagged  *bool   `json:"is_flagged,omitempty"`
	IsStarred  *bool   `json:"is_starred,omitempty"`
	IsDraft    *bool   `json:"is_draft,omitempty"`
	Folder     *string `json:"folder,omitempty" minLength:"1" minLengthMessage:"required"`
}

type updatePipelineRequestBody struct {
	Filters json.RawMessage `json:"filters,omitempty"`
	Active  *bool           `json:"active,omitempty"`
	// Webmail sends these on PATCH; historically they are accepted and ignored.
	DomainID  *uint   `json:"domain_id,omitempty"`
	Direction *string `json:"direction,omitempty"`
}

type ownerTokenRequest struct {
	Challenge       string `json:"challenge,omitempty"`
	PublicKey       string `json:"public_key,omitempty"`
	Signature       string `json:"signature,omitempty"` // base64 of the ssh wire-format signature
	Purpose         string `json:"purpose,omitempty"`   // reset-admin, the only one
	Address         string `json:"address,omitempty"`
	LifetimeSeconds int    `json:"lifetime_seconds,omitempty"`
}

type resetRequest struct {
	Token    string `json:"token,omitempty"`
	Password string `json:"password,omitempty"`
	Client   string `json:"client,omitempty"`
}

type dkimEntry struct {
	DomainID   uint       `json:"domain_id"`
	Domain     string     `json:"domain"`
	Selector   string     `json:"selector"`
	HasKey     bool       `json:"has_key"`
	CreatedAt  *time.Time `json:"created_at"`
	AgeSeconds *float64   `json:"age_seconds" minimum:"0"`
}

type setDKIMKeyBody struct {
	Selector   string `json:"selector" minLength:"1" maxLength:"63" pattern:"^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$" message:"Must be a DNS label (letters, digits, hyphens), at most 63 characters"`
	PrivateKey string `json:"private_key" minLength:"1" message:"required"`
}

type ProtocolSignedIn struct {
	CredentialID   uint     `json:"credential_id"`
	CredentialKind string   `json:"credential_kind" enums:"password,app_password"`
	User           userInfo `json:"user"`
}

type Message struct {
	ID                uint            `json:"id"`
	MailboxID         uint            `json:"mailbox_id"`
	Folder            string          `json:"folder"`
	MsgID             string          `json:"message_id" description:"The RFC 5322 Message-ID"`
	InReplyTo         string          `json:"in_reply_to"`
	References        string          `json:"references"`
	ThreadID          string          `json:"thread_id"`
	Sender            string          `json:"sender"`
	SenderName        string          `json:"sender_name"`
	RecipientsTo      json.RawMessage `json:"recipients_to"`
	RecipientsCc      json.RawMessage `json:"recipients_cc"`
	Subject           string          `json:"subject"`
	BodyText          string          `json:"body_text,omitempty"`
	BodyHTML          string          `json:"body_html,omitempty"`
	Headers           json.RawMessage `json:"headers,omitempty"`
	CalendarEventsRaw json.RawMessage `json:"calendar_events,omitempty"`
	SizeBytes         int             `json:"size_bytes"`
	RawSize           int             `json:"raw_size" description:"The exact octet count of the stored raw message; 0 when none is stored"`
	DowngradedSize    int             `json:"downgraded_size" description:"The octet count of the stored raw message with its headers downgraded to ASCII (RFC 6857), as sent to a POP3 or IMAP client that has not enabled UTF-8; 0 when not measured"`
	ReceivedTLS       *bool           `json:"received_tls,omitempty" description:"For an inbound-MX delivery, whether it arrived encrypted"`
	TLSVersion        string          `json:"tls_version,omitempty"`
	HasAttachments    bool            `json:"has_attachments"`
	IsAnswered        bool            `json:"is_answered,omitempty"`
	IsRead            bool            `json:"is_read"`
	IsDSN             bool            `json:"is_dsn" description:"A delivery status notification this server generated"`
	IsFlagged         bool            `json:"is_flagged"`
	IsStarred         bool            `json:"is_starred"`
	IsDraft           bool            `json:"is_draft"`
	IsDeleted         bool            `json:"is_deleted"`
	ReceivedAt        time.Time       `json:"received_at"`
	DateHeader        *time.Time      `json:"date_header"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	Mailbox           *Mailbox        `json:"mailbox,omitempty" description:"Present only when the mailbox association is loaded"`
}

type Mailbox struct {
	ID                     uint        `json:"id"`
	DomainID               uint        `json:"domain_id"`
	LocalPart              string      `json:"local_part"`
	Address                string      `json:"address" format:"email"`
	DisplayName            string      `json:"display_name"`
	QuotaBytes             int64       `json:"quota_bytes"`
	QuotaUsedBytes         int64       `json:"quota_used_bytes"`
	Active                 bool        `json:"active"`
	LastLoginAt            *time.Time  `json:"last_login_at"`
	PasswordChangeRequired bool        `json:"password_change_required"`
	TwoFactorRequired      bool        `json:"two_factor_required"`
	LastPasswordChange     *time.Time  `json:"last_password_change,omitempty"`
	CreatedAt              time.Time   `json:"created_at"`
	UpdatedAt              time.Time   `json:"updated_at"`
	Domain                 *Domain     `json:"domain,omitempty" description:"Present only when loaded and the session may read the parent domain"`
	QuotaUsage             *QuotaUsage `json:"quota_usage,omitempty"`
}

type Domain struct {
	ID                uint      `json:"id"`
	Name              string    `json:"name"`
	ServerType        string    `json:"server_type" enums:"traditional,restmail"`
	Active            bool      `json:"active"`
	DefaultQuotaBytes int64     `json:"default_quota_bytes"`
	DKIMSelector      string    `json:"dkim_selector"`
	SendingPolicy     string    `json:"sending_policy" enums:",transactional,bulk"`
	DMARCPolicy       string    `json:"dmarc_policy" enums:"none,quarantine,reject"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type QuotaUsage struct {
	MailboxID       uint      `json:"mailbox_id"`
	SubjectBytes    int64     `json:"subject_bytes"`
	BodyBytes       int64     `json:"body_bytes"`
	AttachmentBytes int64     `json:"attachment_bytes"`
	MessageCount    int       `json:"message_count"`
	UpdatedAt       time.Time `json:"updated_at"`
	Mailbox         *Mailbox  `json:"mailbox,omitempty" description:"Present only when the mailbox association is loaded"`
}

type AddressCheck struct {
	Address   string `json:"address"`
	Exists    bool   `json:"exists"`
	MailboxID uint   `json:"mailbox_id,omitempty"`
}

type FolderSummary struct {
	Folder string `json:"name"`
	Total  int64  `json:"total"`
	Unread int64  `json:"unread"`
}

type Quota struct {
	MessageCount   int64   `json:"message_count"`
	PercentUsed    float64 `json:"percent_used"`
	QuotaBytes     int64   `json:"quota_bytes"`
	QuotaUsedBytes int64   `json:"quota_used_bytes"`
}

type QueueStatistics struct {
	Pending    int64 `json:"pending"`
	Deferred   int64 `json:"deferred"`
	Delivered  int64 `json:"delivered"`
	Bounced    int64 `json:"bounced"`
	Total      int64 `json:"total"`
	Delivering int64 `json:"delivering,omitempty"`
	Expired    int64 `json:"expired,omitempty"`
}

type Pipeline struct {
	ID        uint            `json:"id"`
	DomainID  uint            `json:"domain_id"`
	Direction string          `json:"direction" enums:"inbound,outbound"`
	Filters   json.RawMessage `json:"filters"`
	Active    bool            `json:"active"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`

	Domain PipelineDomain `json:"domain,omitempty"`
}

type PipelineDomain struct {
	ID                uint      `json:"id"`
	Name              string    `json:"name"`
	ServerType        string    `json:"server_type" enums:",traditional,restmail"`
	Active            bool      `json:"active"`
	DefaultQuotaBytes int64     `json:"default_quota_bytes"`
	DKIMSelector      string    `json:"dkim_selector"`
	SendingPolicy     string    `json:"sending_policy" enums:",transactional,bulk"`
	DMARCPolicy       string    `json:"dmarc_policy" enums:",none,quarantine,reject"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type Ban struct {
	ID        uint       `json:"id"`
	IP        string     `json:"ip"`
	Reason    string     `json:"reason"`
	Protocol  string     `json:"protocol" enums:"smtp,imap,pop3,all"`
	CreatedBy string     `json:"created_by"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type OwnerToken struct {
	ExpiresAt time.Time `json:"expires_at"`
	Purpose   string    `json:"purpose"`
	Token     string    `json:"token"`
}

// LoginRequest is the loginRequest wire type used by the endpoint registration.
type LoginRequest = loginRequest

// LoginResponse is the loginResponse wire type used by the endpoint registration.
type LoginResponse = loginResponse

// UserInfo is the userInfo wire type used by the endpoint registration.
type UserInfo = userInfo

// ProtocolSignInRequest is the protocolSignInRequest wire type used by the endpoint registration.
type ProtocolSignInRequest = protocolSignInRequest

// SendMessageRequest is the sendMessageRequest wire type used by the endpoint registration.
type SendMessageRequest = sendMessageRequest

// AnyOf states what no one field can: a delivery names its mailbox by a nonzero
// mailbox_id or a nonempty address. The binder refuses a body naming neither
// with the handler's own words, as its description says.
func (deliverMessageBody) AnyOf() oapifly.AnyOfRule {
	return oapifly.AnyOfRule{
		Alternatives: []oapifly.Alternative{
			{Required: []string{"mailbox_id"}, Fields: map[string]string{"mailbox_id": `minimum:"1"`}},
			{Required: []string{"address"}, Fields: map[string]string{"address": `minLength:"1"`}},
		},
		Field:   "address",
		Message: "mailbox_id or address required",
	}
}

// DeliverMessageBody is the deliverMessageBody wire type used by the endpoint registration.
type DeliverMessageBody = deliverMessageBody

// ListUnsubscribe is the listUnsubscribe wire type used by the endpoint registration.
type ListUnsubscribe = listUnsubscribe

// CreateDomainRequest is the createDomainRequest wire type used by the endpoint registration.
type CreateDomainRequest = createDomainRequest

// CreateAccountRequest is the createAccountRequest wire type used by the endpoint registration.
type CreateAccountRequest = createAccountRequest

// AccountResponse is the accountResponse wire type used by the endpoint registration.
type AccountResponse = accountResponse

// UpdateMailboxRequest is the updateMailboxRequest wire type used by the endpoint registration.
type UpdateMailboxRequest = updateMailboxRequest

// UpdateMessageBody is the updateMessageBody wire type used by the endpoint registration.
type UpdateMessageBody = updateMessageBody

// UpdatePipelineRequestBody is the updatePipelineRequestBody wire type used by the endpoint registration.
type UpdatePipelineRequestBody = updatePipelineRequestBody

// OwnerTokenRequest is the ownerTokenRequest wire type used by the endpoint registration.
type OwnerTokenRequest = ownerTokenRequest

// ResetRequest is the resetRequest wire type used by the endpoint registration.
type ResetRequest = resetRequest

// DKIMEntry is the dkimEntry wire type used by the endpoint registration.
type DKIMEntry = dkimEntry

// SetDKIMKeyBody is the setDKIMKeyBody wire type used by the endpoint registration.
type SetDKIMKeyBody = setDKIMKeyBody

// WireSize is the stored raw message size, or the rebuilt size when there is no raw form.
func (m Message) WireSize() int {
	if m.RawSize > 0 {
		return m.RawSize
	}
	return m.SizeBytes
}

// WireSizeFor is the size a POP3 or IMAP session is sent: WireSize for a
// session that enabled UTF-8, and the downgraded size for one that did not.
// A message whose downgraded size is not measured yet reports WireSize.
func (m Message) WireSizeFor(utf8 bool) int {
	if !utf8 && m.RawSize > 0 && m.DowngradedSize > 0 {
		return m.DowngradedSize
	}
	return m.WireSize()
}

// RestmailDiscovery is the unwrapped RESTMAIL protocol discovery document.
type RestmailDiscovery struct {
	Endpoint string `json:"endpoint" format:"uri" pattern:"^https://"`
}

// DeliveryProcessed is an accepted message quarantined or discarded without a mailbox row.
type DeliveryProcessed struct {
	Status string `json:"status" enums:"processed"`
}
