package smtp

import (
	"bytes"
	"strings"

	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
)

// maskEmail redacts the local-part of an email address for logging (OSI-5):
// "alice@example.com" -> "a***@example.com". The domain is preserved for
// operational triage while the individual identity is not written in the clear —
// important on the failed-auth / not-authorized paths, which log attacker-
// supplied, high-volume, often non-customer addresses (credential-stuffing and
// enumeration probes). An empty value maps to "" and a value without "@" is
// masked whole.
func maskEmail(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	at := strings.LastIndex(addr, "@")
	if at <= 0 {
		// No usable local-part/domain split: reveal only the first rune.
		return firstRune(addr) + "***"
	}
	return firstRune(addr[:at]) + "***" + addr[at:]
}

// firstRune returns the first rune of s (or "" if empty), used to keep a minimal
// non-identifying prefix in masked log values.
func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// extractIP extracts the IP address from a remote address string (host:port).
func extractIP(addr string) string {
	host, _, err := splitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// splitHostPort is a simple host:port splitter.
func splitHostPort(addr string) (string, string, error) {
	// Handle IPv6
	if strings.HasPrefix(addr, "[") {
		end := strings.Index(addr, "]")
		if end == -1 {
			return addr, "", nil
		}
		host := addr[1:end]
		if end+1 < len(addr) && addr[end+1] == ':' {
			return host, addr[end+2:], nil
		}
		return host, "", nil
	}

	last := strings.LastIndex(addr, ":")
	if last == -1 {
		return addr, "", nil
	}
	return addr[:last], addr[last+1:], nil
}

// parseRawMessage extracts the fields a DeliverRequest needs from a raw RFC 5322
// message. fromAddr is the bare address of the From header, used on the
// authenticated submission path to bind the header From to an identity the
// account is authorized to send as.
//
// The parsing itself is delegated to internal/mime, which wraps go-message:
// RFC 2047 encoded-words, charset conversion, transfer-encoding decoding and
// nested multipart structures all come from there. The REST ingest path already
// parses with that package, so the same message now yields the same fields
// whether it arrives over SMTP or over REST (issue #298).
func parseRawMessage(data []byte) (subject, bodyText, bodyHTML, messageID, senderName, inReplyTo, references string, toList, ccList []string, fromAddr string) {
	// An empty or whitespace-only message has nothing to parse. Handing it over
	// would come back as a default text/plain body holding that whitespace.
	if len(bytes.TrimSpace(data)) == 0 {
		return "", "", "", "", "", "", "", nil, nil, ""
	}

	email, err := rmime.Parse(terminateHeaders(data))
	if err != nil || email == nil {
		// Nothing parseable: keep whatever body text can be recovered rather
		// than delivering an empty message. Headers stay empty, which is what
		// an unparseable message can honestly report.
		return "", recoverBody(data), "", "", "", "", "", nil, nil, ""
	}

	h := email.Headers
	subject = h.Subject
	// The stored form is the bare id; the angle brackets are re-added wherever
	// the message is rendered back into a header.
	messageID = strings.Trim(h.MessageID, "<>")
	inReplyTo = strings.Trim(h.InReplyTo, "<>")
	// References stays the whitespace-separated list with brackets intact,
	// matching the historical output contract.
	references = strings.Join(h.References, " ")

	if len(h.From) > 0 {
		senderName = h.From[0].Name
		fromAddr = h.From[0].Address
	}
	toList = bareAddresses(h.To)
	ccList = bareAddresses(h.Cc)

	bodyText, bodyHTML = rmime.TextAndHTML(email.Body)
	return
}

// terminateHeaders ensures the header block ends with a blank line. A message
// that is nothing but headers has no terminator, and the parser needs one to
// treat the whole input as a header block rather than a truncated message.
func terminateHeaders(data []byte) []byte {
	if bytes.Contains(data, []byte("\r\n\r\n")) || bytes.Contains(data, []byte("\n\n")) {
		return data
	}
	out := make([]byte, 0, len(data)+4)
	out = append(out, data...)
	return append(out, "\r\n\r\n"...)
}

// recoverBody returns the body of a message the parser could not read: whatever
// follows the first blank line, or nothing when there is no body at all.
func recoverBody(data []byte) string {
	for _, sep := range []string{"\r\n\r\n", "\n\n"} {
		if _, body, found := strings.Cut(string(data), sep); found {
			return body
		}
	}
	return ""
}

// bareAddresses reduces parsed addresses to their address strings, dropping any
// entry that carries no address.
func bareAddresses(addrs []pipeline.Address) []string {
	var out []string
	for _, a := range addrs {
		if a.Address != "" {
			out = append(out, a.Address)
		}
	}
	return out
}

// The body-tree walk lives in internal/mime (TextAndHTML): the IMAP gateway
// needs the same two fields on APPEND, and one copy is enough.
