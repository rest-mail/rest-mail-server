package models

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	restcrypto "github.com/restmail/restmail/internal/crypto"
)

// Outbound transports for a sending domain (#143). Direct resolves the
// recipient's MX; relay hands every message to the operator's smarthost.
const (
	OutboundTransportDirect = "direct"
	OutboundTransportRelay  = "relay"
)

// Relay TLS modes. TLS is never optional on a relay: credentials are only sent
// after a verified handshake.
const (
	RelayTLSStartTLS = "starttls"
	RelayTLSImplicit = "implicit"
)

// DomainRelay is a sending domain's outbound transport (#143). No row, or a row
// with transport direct, means the queue worker resolves MX as it always has.
// With transport relay, every message whose sender is in the domain goes to the
// configured host instead, over TLS, authenticating when a username is set.
//
// PasswordEnc is the SMTP AUTH password encrypted at rest with the
// MASTER_KEY-derived key, the same AES-256-GCM scheme as the DKIM private keys,
// tagged with RelayPasswordCipherPrefix. It is never serialized.
type DomainRelay struct {
	ID          uint      `gorm:"primaryKey" json:"-"`
	DomainID    uint      `gorm:"uniqueIndex;not null" json:"domain_id"`
	Transport   string    `gorm:"size:10;not null;default:direct" json:"transport"`
	Host        string    `gorm:"size:253" json:"host"`
	Port        int       `json:"port"`
	TLSMode     string    `gorm:"size:10" json:"tls_mode"`
	Username    string    `gorm:"size:255" json:"username"`
	PasswordEnc string    `gorm:"type:text" json:"-"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// RelayPasswordCipherPrefix marks an encrypted relay password, as
// DKIMKeyCipherPrefix does for DKIM keys.
const RelayPasswordCipherPrefix = "relay:v1:"

// ErrRelayPasswordUndecryptable is returned when a stored relay password cannot
// be read with the configured MASTER_KEY. The worker fails closed: the message
// is retried, never sent unauthenticated or direct.
var ErrRelayPasswordUndecryptable = errors.New("relay: stored password cannot be decrypted with MASTER_KEY")

// EncryptRelayPassword returns the at-rest form of a relay password. Unlike a
// DKIM key there is no plaintext dev path: without a MASTER_KEY a password is
// refused, so a credential for a third-party service never sits in the clear.
func EncryptRelayPassword(password, masterKey string) (string, error) {
	if masterKey == "" {
		return "", errors.New("relay: MASTER_KEY is required to store a relay password")
	}
	enc, err := restcrypto.EncryptString(password, masterKey)
	if err != nil {
		return "", err
	}
	return RelayPasswordCipherPrefix + enc, nil
}

// LoadRelayPassword is the inverse of EncryptRelayPassword. An empty stored
// value is an empty password (no AUTH).
func LoadRelayPassword(stored, masterKey string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if masterKey == "" || !strings.HasPrefix(stored, RelayPasswordCipherPrefix) {
		return "", ErrRelayPasswordUndecryptable
	}
	pw, err := restcrypto.DecryptString(strings.TrimPrefix(stored, RelayPasswordCipherPrefix), masterKey)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRelayPasswordUndecryptable, err)
	}
	return pw, nil
}

// ValidRelayHost reports whether host is a bare DNS name or IP literal: no
// scheme, port, path or brackets, so it can only ever name one host.
func ValidRelayHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
