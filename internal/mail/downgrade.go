package mail

import (
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/rest-mail/go-imap/pkg/imap/wireform"
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
// for these clients. See DowngradeForPOP3 for the one thing POP3 adds.
//
//   - Subject, Comments and Keywords become RFC 2047 encoded-words.
//   - An address field changes only what holds an 8-bit octet, see
//     downgradeAddressField: names, group names and comments become encoded-words,
//     and an internationalized domain becomes its A-label. An address whose local
//     part is non-ASCII cannot be expressed in ASCII, so it becomes an empty group
//     whose encoded display name shows the original (RFC 6857 section 3.1.8): the
//     client still sees who it was, and a reply cannot silently go to a different
//     mailbox.
//   - Content-Type and Content-Disposition keep their meaning: non-ASCII
//     parameters become RFC 2231 parameters, and every other parameter stays as
//     it was sent, including one in a charset nothing here can read.
//   - Any other field, or one of the above that cannot be parsed, is renamed
//     Downgraded-<name> with its value as encoded-words (RFC 6857 section 3.1),
//     so nothing the client might interpret carries altered content.
//   - Text that is not valid UTF-8 uses charset UNKNOWN-8BIT instead of UTF-8.
func DowngradeHeaders(raw string) string {
	header, sep, body := splitHeader(raw)
	if !containsNonASCII(header) {
		return raw
	}
	var size headerSink
	writeDowngraded(&size, header, sep)
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
			// Not a header field at all. Keeping non-ASCII is not an option, and
			// neither is losing it, so it is encoded whole, as the IMAP engine does:
			// its octets survive, and a reader still finds no field in it.
			b.WriteString(wireform.EncodeWords(unfold(field)))
			b.WriteString("\r\n")
			continue
		}
		b.WriteString(downgradeField(name, value))
		b.WriteString("\r\n")
	}
	b.WriteString(sep)
	b.WriteString(body)
	return b.String()
}

// downgradeField converts one field; value is still folded.
func downgradeField(name, folded string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	if addressFields[key] {
		if value, ok := downgradeAddressField(len(name)+1, folded); ok {
			return name + ":" + value
		}
	}
	// White space only: a no-break space at either end is text.
	value := strings.Trim(unfold(folded), " \t")
	switch {
	case unstructuredFields[key]:
		return name + ": " + encodeText(value)
	case key == "content-type" || key == "content-disposition":
		if field, ok := wireform.ParameterField(name, value); ok {
			_, _ = w.WriteString(field)
			return
		}
	}
	_, _ = w.WriteString("Downgraded-")
	_, _ = w.WriteString(strings.TrimSpace(name))
	_, _ = w.WriteString(": ")
	writeEncodedText(w, value)
}

// encodeText renders text as encoded-words when it is not plain ASCII. Octets
// that are not UTF-8 go out as UNKNOWN-8BIT, so they survive and no charset is
// guessed.
func encodeText(s string) string {
	var b strings.Builder
	writeEncodedText(&b, s)
	return b.String()
}

func writeEncodedText(w wordSink, s string) {
	switch {
	case !containsNonASCII(s):
		_, _ = w.WriteString(s)
	case !utf8.ValidString(s):
		wireform.WriteEncodedWords(w, s)
	default:
		writeQWords(w, s)
	}
}

// writeQWords is mime.QEncoding.Encode("utf-8", s) for an s that needs encoding,
// holding nothing that grows with s (TestWriteQWordsMatchesTheLibrary).
func writeQWords(w wordSink, s string) {
	const (
		open       = "=?utf-8?q?"
		closing    = "?="
		maxContent = 75 - len(open) - len(closing)
		digits     = "0123456789ABCDEF"
	)
	_, _ = w.WriteString(open)
	length := 0
	for i := 0; i < len(s); {
		size, encoded := 1, 1
		if c := s[i]; c < ' ' || c > '~' || c == '=' || c == '?' || c == '_' {
			_, size = utf8.DecodeRuneInString(s[i:])
			encoded = 3 * size
		}
		if length+encoded > maxContent {
			_, _ = w.WriteString(closing + " " + open)
			length = 0
		}
		for k := i; k < i+size; k++ {
			switch c := s[k]; {
			case c == ' ':
				_, _ = w.WriteString("_")
			case c >= '!' && c <= '~' && c != '=' && c != '?' && c != '_':
				_, _ = w.WriteString(s[k : k+1])
			default:
				_, _ = w.WriteString("=")
				_, _ = w.WriteString(digits[c>>4 : c>>4+1])
				_, _ = w.WriteString(digits[c&0x0f : c&0x0f+1])
			}
		}
		length += encoded
		i += size
	}
	_, _ = w.WriteString(closing)
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

// eachHeaderField calls each with every field of a header block, still folded and
// without its final line break. bare says a line break in it is a lone LF.
func eachHeaderField(header string, each func(span string, bare bool)) {
	for at := 0; at <= len(header); {
		start, bare, ended := at, false, false
		var end int
		for {
			i := strings.IndexByte(header[at:], '\n')
			if i < 0 {
				end, at, ended = len(header), len(header)+1, false
				break
			}
			line := at + i
			bare = bare || line == at || header[line-1] != '\r'
			end, at, ended = line, line+1, true
			if at >= len(header) || (header[at] != ' ' && header[at] != '\t') {
				break
			}
		}
		span := header[start:end]
		if ended && end > start && header[end-1] == '\r' {
			span = span[:len(span)-1]
		}
		each(span, bare)
	}
}

var unfolder = strings.NewReplacer("\r\n", "", "\n", "")

// unfold joins a folded value onto one line (RFC 5322 section 2.2.3).
func unfold(value string) string {
	if strings.IndexByte(value, '\n') < 0 {
		return value
	}
	return unfolder.Replace(value)
}

func containsNonASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}

// DowngradeForPOP3 is what RETR and TOP send a client that has not enabled UTF-8:
// DowngradeHeaders, and a top-level 8-bit boundary in a spelling the client can match.
//
// POP3 sends the body as stored, so the delimiter lines hold the stored octets, and no
// 7-bit header can name those: RFC 2231 only moves the problem to a reader that decodes
// it, in a charset it knows. So the Content-Type and the delimiter lines take one 7-bit
// spelling, the one IMAP FETCH sends, and no other octet of the body changes. Every line
// stays a line, and DowngradedSize measures this form, so TOP and LIST stay right. IMAP
// doesn't come here, because its engine converts the whole message, boundary included.
func DowngradeForPOP3(raw string) string {
	if header, _, _ := splitHeader(raw); !containsNonASCII(header) {
		return raw
	}
	return DowngradeHeaders(wireform.SevenBitBoundary(raw))
}

// DowngradedSize is the octet count of DowngradeForPOP3(raw): what a POP3 client that
// has not enabled UTF-8 receives. It equals len(raw) when the header block is already
// ASCII.
func DowngradedSize(raw string) int {
	return len(DowngradeForPOP3(raw))
}
