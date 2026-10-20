package mail

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// Non-transitional lookup mapping preserves IDNA2008 distinctions such as ß
// versus ss. Validate labels, bidi rules and DNS lengths before using a name
// at the DNS or SMTP boundary.
var domainLookup = idna.New(idna.MapForLookup(), idna.Transitional(false), idna.BidiRule(), idna.VerifyDNSLength(true))

// DomainASCII converts a Unicode domain to its ASCII DNS representation. It
// must be applied at protocol boundaries, not to the stored/displayed domain.
func DomainASCII(domain string) (string, error) {
	if domain == "" || !utf8.ValidString(domain) || ContainsControlChar(domain) {
		return "", errors.New("invalid domain")
	}
	// DNS callers may supply an absolute name. VerifyDNSLength validates the
	// labels without the root dot; preserve the absolute form for the lookup.
	absolute := strings.HasSuffix(domain, ".")
	ascii, err := domainLookup.ToASCII(strings.TrimSuffix(domain, "."))
	if err != nil {
		return "", fmt.Errorf("invalid domain: %w", err)
	}
	if absolute {
		ascii += "."
	}
	return ascii, nil
}

// SMTPAddressASCII converts only the domain of an already validated, bare
// envelope address to Punycode. Local parts are preserved byte-for-byte: their
// normalization is a mailbox provisioning concern. Empty reverse paths and
// valid IP address literals are preserved as well. This does not downgrade a
// UTF-8 local part or any message headers.
func SMTPAddressASCII(address string) (string, error) {
	if !utf8.ValidString(address) || ContainsControlChar(address) {
		return "", errors.New("invalid envelope address encoding")
	}
	if address == "" {
		return "", nil
	}
	at := strings.LastIndexByte(address, '@')
	if at <= 0 {
		return "", errors.New("envelope address requires a local part and domain")
	}
	domain := address[at+1:]
	if strings.HasPrefix(domain, "[") && strings.HasSuffix(domain, "]") {
		literal := strings.TrimSuffix(strings.TrimPrefix(domain, "["), "]")
		if net.ParseIP(strings.TrimPrefix(literal, "IPv6:")) == nil {
			return "", errors.New("invalid envelope address literal")
		}
		return address, nil
	}
	ascii, err := DomainASCII(domain)
	if err != nil {
		return "", err
	}
	return address[:at+1] + ascii, nil
}
