package mail

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// GenerateMessageID creates an RFC 5322 compliant Message-ID using 16 random
// bytes formatted as a UUID-like hex string. The result includes angle brackets,
// e.g. "<0192e4a1-7b3c-7def-8abc-0123456789ab@example.com>".
func GenerateMessageID(domain string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("mail: failed to read crypto/rand: " + err.Error())
	}
	return fmt.Sprintf("<%x-%x-%x-%x-%x@%s>",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16], domain)
}

// DomainFromAddress returns the domain part of an email address (after the last @).
// If the address contains no @, the full string is returned.
func DomainFromAddress(email string) string {
	if idx := strings.LastIndex(email, "@"); idx >= 0 {
		return email[idx+1:]
	}
	return email
}
