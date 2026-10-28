package mail

import (
	"fmt"
	"net/mail"
	"strings"
)

// BareAddress validates a configured, bare email address and converts its
// domain to the DNS A-label form. Remote local parts keep their exact spelling.
// Display names and comments are refused, so it is safe in both headers and an
// SMTP envelope. The size limit matches outbound_queue's address columns.
func BareAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if err := ValidateAddress(address); err != nil {
		return "", err
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Name != "" {
		return "", fmt.Errorf("must be a bare email address")
	}
	// Address.String restores any required local-part quoting and wraps a
	// nameless address in angle brackets. Comparing that bare form refuses
	// comments and display syntax without losing quoted local parts.
	bare := strings.TrimSuffix(strings.TrimPrefix(parsed.String(), "<"), ">")
	if bare != address {
		return "", fmt.Errorf("must be a bare email address")
	}
	address, err = SMTPAddressASCII(address)
	if err != nil {
		return "", err
	}
	if len(address) > 255 {
		return "", fmt.Errorf("email address exceeds 255 bytes")
	}
	return address, nil
}
