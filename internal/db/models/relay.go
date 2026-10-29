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

// The pieces of RelayHostPattern: a DNS label, an IPv6 group, an IPv4 octet as
// net.ParseIP reads one (no leading zero), and the last 32 bits of an IPv6
// address, two groups or an embedded IPv4 address.
const (
	relayHostLabel = `[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?`
	relayHostGroup = `[0-9A-Fa-f]{1,4}`
	relayHostOctet = `(25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])`
	relayHostLow32 = `(` + relayHostGroup + `:` + relayHostGroup + `|` + relayHostOctet + `(\.` + relayHostOctet + `){3})`
)

// RelayHostPattern is ValidRelayHost as the API describes it: a DNS name, or
// an IPv6 address in the RFC 3986 grammar net.ParseIP reads. An IPv4 address
// is a DNS name to it. With a maxLength of 253, which only a name can reach,
// it holds of exactly the hosts ValidRelayHost accepts.
const RelayHostPattern = `^(` + relayHostLabel + `(\.` + relayHostLabel + `)*` +
	`|(` + relayHostGroup + `:){6}` + relayHostLow32 +
	`|::(` + relayHostGroup + `:){5}` + relayHostLow32 +
	`|(` + relayHostGroup + `)?::(` + relayHostGroup + `:){4}` + relayHostLow32 +
	`|((` + relayHostGroup + `:){0,1}` + relayHostGroup + `)?::(` + relayHostGroup + `:){3}` + relayHostLow32 +
	`|((` + relayHostGroup + `:){0,2}` + relayHostGroup + `)?::(` + relayHostGroup + `:){2}` + relayHostLow32 +
	`|((` + relayHostGroup + `:){0,3}` + relayHostGroup + `)?::` + relayHostGroup + `:` + relayHostLow32 +
	`|((` + relayHostGroup + `:){0,4}` + relayHostGroup + `)?::` + relayHostLow32 +
	`|((` + relayHostGroup + `:){0,5}` + relayHostGroup + `)?::` + relayHostGroup +
	`|((` + relayHostGroup + `:){0,6}` + relayHostGroup + `)?::` +
	`)$`

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
