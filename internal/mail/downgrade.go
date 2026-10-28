package mail

import (
	"mime"
	netmail "net/mail"
	"strings"
)

// addressFields hold an address-list (RFC 5322 section 3.6.2 and 3.6.3, plus
// their Resent- forms). Their addresses are downgraded one by one.
var addressFields = map[string]bool{
	"from": true, "sender": true, "reply-to": true,
	"to": true, "cc": true, "bcc": true,
	"resent-from": true, "resent-sender": true,
	"resent-to": true, "resent-cc": true, "resent-bcc": true,
}

// unstructuredFields carry free text, so an encoded-word may replace the value
// in place (RFC 2047 section 5).
var unstructuredFields = map[string]bool{
	"subject": true, "comments": true, "keywords": true,
}

// DowngradeHeaders rewrites the top-level header block of a stored message so it
// carries only ASCII, for a POP3 or IMAP client that has not enabled UTF-8
// (RFC 6856 section 3, RFC 6855 section 3, downgrading per RFC 6857 and
// RFC 6858). A message whose header block is already ASCII is returned
// unchanged, byte for byte. The body is never touched: 8-bit bodies are legal
// for these clients.
//
//   - Subject, Comments and Keywords become RFC 2047 encoded-words.
//   - In address fields an internationalized domain becomes its A-label. An
//     address whose local part is non-ASCII cannot be expressed in ASCII, so it
//     becomes an empty group whose encoded display name shows the original
//     (RFC 6857 section 3.2.1): the client still sees who it was, and a reply
//     cannot silently go to a different mailbox.
//   - Content-Type and Content-Disposition keep their meaning: non-ASCII
//     parameters become RFC 2231 parameters.
//   - Any other field, or one of the above that cannot be parsed, is renamed
//     Downgraded-<name> with its value as encoded-words (RFC 6857 section 3.1),
//     so nothing the client might interpret carries altered content.
func DowngradeHeaders(raw string) string {
	header, sep, body := splitHeader(raw)
	if !containsNonASCII(header) {
		return raw
	}
	var b strings.Builder
	b.Grow(len(raw) + len(raw)/4)
	for _, field := range headerFields(header) {
		if !containsNonASCII(field) {
			b.WriteString(field)
			b.WriteString("\r\n")
			continue
		}
		name, value, ok := strings.Cut(field, ":")
		if !ok {
			// Not a header field at all. Keeping non-ASCII is not an option,
			// so drop the line rather than invent a name for it.
			continue
		}
		b.WriteString(downgradeField(name, unfold(value)))
		b.WriteString("\r\n")
	}
	b.WriteString(sep)
	b.WriteString(body)
	return b.String()
}

func downgradeField(name, value string) string {
	value = strings.TrimSpace(value)
	key := strings.ToLower(strings.TrimSpace(name))
	switch {
	case unstructuredFields[key]:
		return name + ": " + encodeText(value)
	case addressFields[key]:
		if list, ok := downgradeAddressList(value); ok {
			return name + ": " + list
		}
	case key == "content-type" || key == "content-disposition":
		if mediaType, params, err := mime.ParseMediaType(value); err == nil && !containsNonASCII(mediaType) {
			if formatted := mime.FormatMediaType(mediaType, params); formatted != "" && !containsNonASCII(formatted) {
				return name + ": " + formatted
			}
		}
	}
	return "Downgraded-" + strings.TrimSpace(name) + ": " + encodeText(value)
}

// downgradeAddressList renders each address with only ASCII. ok is false when
// the value does not parse as an address list.
func downgradeAddressList(value string) (string, bool) {
	addrs, err := netmail.ParseAddressList(value)
	if err != nil || len(addrs) == 0 {
		return "", false
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, downgradeAddress(a))
	}
	return strings.Join(out, ", "), true
}

func downgradeAddress(a *netmail.Address) string {
	at := strings.LastIndexByte(a.Address, '@')
	local, domain := a.Address, ""
	if at >= 0 {
		local, domain = a.Address[:at], a.Address[at+1:]
	}
	if !containsNonASCII(local) {
		if ascii, err := DomainASCII(domain); err == nil {
			// String encodes a non-ASCII display name as an encoded-word.
			return (&netmail.Address{Name: a.Name, Address: local + "@" + ascii}).String()
		}
	}
	original := a.Address
	if a.Name != "" {
		original = a.Name + " <" + a.Address + ">"
	}
	return encodeText(original) + " :;"
}

// encodeText renders text as encoded-words when it is not plain ASCII.
func encodeText(s string) string {
	if !containsNonASCII(s) {
		return s
	}
	return mime.QEncoding.Encode("utf-8", s)
}

// splitHeader returns the header block (without its final line break), the
// separator line and the body. A message with no body yields the whole input as
// its header.
func splitHeader(raw string) (header, sep, body string) {
	if i := strings.Index(raw, "\r\n\r\n"); i >= 0 {
		return raw[:i], "\r\n", raw[i+4:]
	}
	if i := strings.Index(raw, "\n\n"); i >= 0 {
		return raw[:i], "\r\n", raw[i+2:]
	}
	return strings.TrimRight(raw, "\r\n"), "\r\n", ""
}

// headerFields splits a header block into fields, each still folded.
func headerFields(header string) []string {
	lines := strings.Split(strings.ReplaceAll(header, "\r\n", "\n"), "\n")
	var fields []string
	for _, line := range lines {
		if len(fields) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			fields[len(fields)-1] += "\r\n" + line
			continue
		}
		fields = append(fields, line)
	}
	return fields
}

// unfold joins a folded value onto one line (RFC 5322 section 2.2.3).
func unfold(value string) string {
	return strings.NewReplacer("\r\n", "", "\n", "").Replace(value)
}

func containsNonASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}

// DowngradedSize is the octet count of DowngradeHeaders(raw): what a POP3 or
// IMAP client that has not enabled UTF-8 receives. It equals len(raw) when the
// header block is already ASCII.
func DowngradedSize(raw string) int {
	return len(DowngradeHeaders(raw))
}
