package models

import "time"

// Gateway services: the protocol gateways that call the API, each identified
// by its own mTLS client certificate on the internal listener (#1586).
const (
	GatewayServiceIMAP           = "imap"
	GatewayServicePOP3           = "pop3"
	GatewayServiceSMTPSubmission = "smtp-submission"
	GatewayServiceSMTPDelivery   = "smtp-delivery"
)

// GatewaySession is one mail client's connection to a protocol gateway, as the
// API sees it (#1586): the gateway opened it when the client signed in, with
// the account's address and a password or app password, and it lasts until
// the gateway closes it at logout or disconnect. The token the gateway holds
// only names this row (its SHA-256 is stored, never the token), and the API
// checks the row and its credential on every request, so revoking the
// credential, or the account's sessions, ends the connection's access at its
// next command. It has no expiry while the connection is in use; one left
// open is closed once it has been idle for GatewaySessionIdle.
type GatewaySession struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// TokenHash is the hex SHA-256 of the session token.
	TokenHash string `gorm:"size:64;uniqueIndex;not null" json:"-"`
	// AccountID is the account the client signed in to.
	AccountID uint `gorm:"not null;index" json:"account_id"`
	// CredentialID is the credential it signed in with (a password or an app
	// password): the session lasts only while that credential is usable.
	CredentialID   uint   `gorm:"not null;index" json:"credential_id"`
	CredentialKind string `gorm:"size:32;not null" json:"credential_kind"`
	// Service is the gateway that opened it (GatewayService*), from its client
	// certificate. Only that service may use the session.
	Service string `gorm:"size:32;not null" json:"service"`
	// Protocol is what the client signed in over: imap, pop3 or smtp.
	Protocol   string     `gorm:"size:16;not null" json:"protocol"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt time.Time  `gorm:"not null" json:"last_used_at"`
	ClosedAt   *time.Time `gorm:"index" json:"closed_at"`
}

func (GatewaySession) TableName() string { return "gateway_sessions" }

// GatewaySessionIdle is how long a gateway session may go unused before it is
// closed: a connection the gateway never closed (a crash, a lost disconnect).
// An IMAP connection in IDLE keeps its session alive with the calls it makes
// while it waits, so this is far longer than any of them apart.
const GatewaySessionIdle = 24 * time.Hour
