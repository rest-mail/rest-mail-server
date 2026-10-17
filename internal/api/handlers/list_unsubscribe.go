package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/mail"
	"net/url"
	"strings"
)

// The header fields an unsubscribe target is published in (RFC 2369 §3.2,
// RFC 8058 §3.1), and the one value RFC 8058 lets List-Unsubscribe-Post carry.
const (
	headerListUnsubscribe     = "List-Unsubscribe"
	headerListUnsubscribePost = "List-Unsubscribe-Post"
	listUnsubscribeOneClick   = "List-Unsubscribe=One-Click"
)

// maxUnsubscribeURLLen bounds the HTTPS target. It is generous for a URL that
// carries an opaque recipient token, and keeps one header from growing without
// limit.
const maxUnsubscribeURLLen = 2048

// listUnsubscribe is the send API's list_unsubscribe object (#144): where a
// recipient goes to stop receiving this mail. The sender runs the endpoint;
// the server only publishes it in the message and never fetches or posts to it.
//
// It is two named fields rather than a free-form header so a caller cannot
// write the header itself: at most one address and one HTTPS URL, each checked
// before it reaches the message.
type listUnsubscribe struct {
	// Mailto is a bare address (no display name, no mailto: prefix) that
	// unsubscribes the recipient when it receives mail.
	Mailto string `json:"mailto,omitempty"`
	// HTTPS is an https URL that unsubscribes the recipient when it receives
	// the RFC 8058 one-click POST.
	HTTPS string `json:"https,omitempty"`
}

// UnmarshalJSON refuses a key the object does not have, so a target under
// another name ("http", "post", a header name) is an error rather than
// silently dropped.
func (l *listUnsubscribe) UnmarshalJSON(b []byte) error {
	type fields listUnsubscribe
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var f fields
	if err := dec.Decode(&f); err != nil {
		return err
	}
	*l = listUnsubscribe(f)
	return nil
}

// validate checks each target. It is the only gate between the request and
// the header, so it refuses anything that could end the header line or add a
// target the field names do not say.
func (l *listUnsubscribe) validate() error {
	if l.Mailto == "" && l.HTTPS == "" {
		return errors.New("list_unsubscribe needs a mailto address, an https URL or both")
	}
	if l.Mailto != "" {
		if err := validateUnsubscribeMailto(l.Mailto); err != nil {
			return err
		}
	}
	if l.HTTPS != "" {
		if err := validateUnsubscribeHTTPS(l.HTTPS); err != nil {
			return err
		}
	}
	return nil
}

// validateUnsubscribeMailto accepts a bare ASCII address and nothing else: no
// display name, no URI form, no query, since mailto: and the angle brackets
// are added when the header is written.
func validateUnsubscribeMailto(addr string) error {
	if !headerSafeASCII(addr) {
		return errors.New("list_unsubscribe.mailto must be a plain ASCII address")
	}
	parsed, err := mail.ParseAddress(addr)
	if err != nil || parsed.Name != "" || parsed.Address != addr {
		return errors.New("list_unsubscribe.mailto must be a bare address, like unsubscribe@example.com")
	}
	// net/mail takes '?' and '%' as ordinary address characters, but in a
	// mailto: URI they start a query or an escape (RFC 6068 §2), so an address
	// holding one would publish a different target than it names. The domain
	// must be a plain host name.
	local, domain, _ := strings.Cut(addr, "@")
	if strings.ContainsAny(local, "?#%/&=[]\\") || !hostName(domain) {
		return errors.New("list_unsubscribe.mailto must be a bare address, like unsubscribe@example.com")
	}
	return nil
}

// hostName reports whether s is a dotted host name of letters, digits and
// hyphens.
func hostName(s string) bool {
	if s == "" || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// validateUnsubscribeHTTPS accepts one absolute https URL with a host and no
// credentials. RFC 8058 §3.1 needs an HTTPS URI for one-click; any other
// scheme is refused rather than published.
func validateUnsubscribeHTTPS(raw string) error {
	if len(raw) > maxUnsubscribeURLLen {
		return errors.New("list_unsubscribe.https is too long")
	}
	if !headerSafeASCII(raw) {
		return errors.New("list_unsubscribe.https must be a URL of printable ASCII, with no spaces, commas or angle brackets")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("list_unsubscribe.https is not a valid URL")
	}
	if u.Scheme != "https" {
		return errors.New("list_unsubscribe.https must be an https:// URL")
	}
	if u.Host == "" || u.Hostname() == "" || u.Opaque != "" {
		return errors.New("list_unsubscribe.https must name a host")
	}
	if u.User != nil {
		return errors.New("list_unsubscribe.https must not carry credentials")
	}
	return nil
}

// headerSafeASCII reports whether s is printable ASCII without the characters
// that delimit List-Unsubscribe targets (RFC 2369 §2: each in angle brackets,
// separated by commas) or could fold or end the header line.
func headerSafeASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x20 || c >= 0x7f {
			return false
		}
		switch c {
		case '<', '>', ',', '"':
			return false
		}
	}
	return true
}

// headers renders the targets as header fields. List-Unsubscribe lists the
// mailto target first, then the URL. List-Unsubscribe-Post claims one-click
// only when there is an HTTPS target to POST to (RFC 8058 §3.1); a mailto-only
// list is plain RFC 2369 and makes no such claim.
//
// The caller must DKIM-sign the message when this claims one-click: RFC 8058
// §4 requires a valid signature covering both fields.
func (l *listUnsubscribe) headers() map[string]string {
	var targets []string
	if l.Mailto != "" {
		targets = append(targets, "<mailto:"+l.Mailto+">")
	}
	if l.HTTPS != "" {
		targets = append(targets, "<"+l.HTTPS+">")
	}
	out := map[string]string{headerListUnsubscribe: strings.Join(targets, ", ")}
	if l.HTTPS != "" {
		out[headerListUnsubscribePost] = listUnsubscribeOneClick
	}
	return out
}

// withListUnsubscribe returns the transform headers with the request's
// unsubscribe fields in place of any a transform set, so the message carries
// exactly the targets the caller asked for and validated.
func withListUnsubscribe(extra map[string]string, l *listUnsubscribe) map[string]string {
	if l == nil {
		return extra
	}
	out := make(map[string]string, len(extra)+2)
	for name, value := range extra {
		if strings.EqualFold(name, headerListUnsubscribe) || strings.EqualFold(name, headerListUnsubscribePost) {
			continue
		}
		out[name] = value
	}
	for name, value := range l.headers() {
		out[name] = value
	}
	return out
}
