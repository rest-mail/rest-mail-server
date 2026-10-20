package mail

import (
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// CanonicalMailboxAddress returns the identity used to provision and sign in to
// a hosted mailbox. Local parts follow the server's existing lowercase policy
// and are normalized to NFC; domains use the same A-label form as hosted domain
// records. Only bare, unquoted addresses are supported for hosted accounts.
// This must not be used to rewrite remote SMTP local parts or message headers.
func CanonicalMailboxAddress(address string) (string, error) {
	if !utf8.ValidString(address) {
		return "", errors.New("invalid mailbox address encoding")
	}
	address = strings.TrimSpace(address)
	local, domain, ok := strings.Cut(address, "@")
	if !ok || local == "" || ContainsControlChar(address) ||
		strings.ContainsAny(local, " \t\r\n<>()[],;:\\\"") || strings.HasSuffix(domain, ".") {
		return "", errors.New("mailbox address must be local@domain")
	}
	ascii, err := DomainASCII(domain)
	if err != nil {
		return "", err
	}
	canonical := norm.NFC.String(strings.ToLower(local)) + "@" + ascii
	if err := ValidateAddress(canonical); err != nil {
		return "", err
	}
	return canonical, nil
}
