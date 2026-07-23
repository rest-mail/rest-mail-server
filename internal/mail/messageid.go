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

// CanonicalID normalizes a Message-ID-like value to the canonical "<id>" form
// used for thread matching. Surrounding whitespace and any angle brackets are
// stripped, then a single bracket pair is applied — so "<id>", "id", and " id "
// all normalize to "<id>". Empty (or bracket-only) input returns "".
//
// Thread IDs must be byte-identical to match: a Message-ID stored bracketed but
// a References-derived thread ID stored bare would split a reply into its own
// thread. Canonicalizing every thread-ID derivation through this keeps them
// consistent with the bracketed Message-IDs GenerateMessageID produces.
func CanonicalID(id string) string {
	id = strings.Trim(strings.TrimSpace(id), "<>")
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	return "<" + id + ">"
}

// DomainFromAddress returns the domain part of an email address (after the last @).
// If the address contains no @, the full string is returned.
func DomainFromAddress(email string) string {
	if idx := strings.LastIndex(email, "@"); idx >= 0 {
		return email[idx+1:]
	}
	return email
}
