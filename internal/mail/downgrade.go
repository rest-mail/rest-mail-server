package mail

import (
	"io"
	netmail "net/mail"
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
//   - Text that is not valid UTF-8 uses charset UNKNOWN-8BIT instead of UTF-8.
func DowngradeHeaders(raw string) string {
	header, sep, body := splitHeader(raw)
	if !containsNonASCII(header) {
		return raw
	}
	var size headerSink
	writeDowngraded(&size, header, sep)
	var b strings.Builder
	b.Grow(int(size) + len(body))
	writeDowngraded(&b, header, sep)
	b.WriteString(body)
	return b.String()
}

// downgradedBlock is what DowngradeHeaders puts before the body of a message whose
// header holds an 8-bit octet.
func downgradedBlock(header, sep string) string {
	var size headerSink
	writeDowngraded(&size, header, sep)
	var b strings.Builder
	b.Grow(int(size))
	writeDowngraded(&b, header, sep)
	return b.String()
}

// wordSink takes a downgraded header block as it is made: a strings.Builder to send it,
// a headerSink to measure it.
type wordSink interface {
	io.Writer
	io.StringWriter
}

// headerSink is a wordSink that keeps only the count.
type headerSink int

func (c *headerSink) Write(p []byte) (int, error) {
	*c += headerSink(len(p))
	return len(p), nil
}

func (c *headerSink) WriteString(s string) (int, error) {
	*c += headerSink(len(s))
	return len(s), nil
}

// writeDowngraded writes the header block of DowngradeHeaders, field by field.
func writeDowngraded(w wordSink, header, sep string) {
	eachHeaderField(header, func(span string, bare bool) {
		writeDowngradedField(w, span, bare)
	})
	_, _ = w.WriteString(sep)
}

func writeDowngradedField(w wordSink, span string, bare bool) {
	if !containsNonASCII(span) {
		writeFolded(w, span, bare)
		_, _ = w.WriteString("\r\n")
		return
	}
	field := span
	if bare {
		var b strings.Builder
		b.Grow(len(span) + len(span)/4)
		writeFolded(&b, span, true)
		field = b.String()
	}
	name, value, ok := strings.Cut(field, ":")
	if !ok {
		// Not a header field at all. Keeping non-ASCII is not an option, and
		// neither is losing it, so it is encoded whole, as the IMAP engine does:
		// its octets survive, and a reader still finds no field in it.
		wireform.WriteEncodedWords(w, unfold(field))
		_, _ = w.WriteString("\r\n")
		return
	}
	downgradeField(w, name, unfold(value))
	_, _ = w.WriteString("\r\n")
}

// measuredBlock stands in for a downgraded header block when measuring: it reads the
// same to the conversion that follows, so the conversion's count plus dropped is that of
// the real block. A field rewritten to carry no 8-bit octet keeps only its name, since
// its text only ever counts. ok is false when a blank line in the block ends the header
// early, so that text is the body, and counts for what it says.
func measuredBlock(header, sep string) (block string, dropped int, ok bool) {
	var size headerSink
	writeMeasuredBlock(&size, header, sep, &dropped)
	var b strings.Builder
	b.Grow(int(size))
	dropped = 0
	writeMeasuredBlock(&b, header, sep, &dropped)
	block = b.String()
	if strings.HasPrefix(block, "\r\n") || strings.Index(block, "\r\n\r\n") != len(block)-4 {
		return "", 0, false
	}
	return block, dropped, true
}

func writeMeasuredBlock(w wordSink, header, sep string, dropped *int) {
	names := &nameOnly{w: w}
	eachHeaderField(header, func(span string, bare bool) {
		if !containsNonASCII(span) || keptWhenMeasured(span) {
			writeDowngradedField(w, span, bare)
			return
		}
		names.named, names.dropped = false, 0
		writeDowngradedField(names, span, bare)
		_, _ = w.WriteString("\r\n")
		*dropped += names.dropped - len("\r\n")
	})
	_, _ = w.WriteString(sep)
}

// keptWhenMeasured reports whether the conversion reads more of a downgraded 8-bit field
// than its name: a Content-Type, or a name still 8-bit, which encodes the whole field.
// A line with no name is encoded whole, so it stays too.
func keptWhenMeasured(span string) bool {
	name, _, ok := strings.Cut(span, ":")
	return !ok || containsNonASCII(name) || strings.EqualFold(strings.TrimSpace(name), "Content-Type")
}

// nameOnly passes on what a field says up to and including its colon.
type nameOnly struct {
	w       wordSink
	named   bool
	dropped int
}

func (n *nameOnly) Write(p []byte) (int, error) {
	if n.named {
		n.dropped += len(p)
		return len(p), nil
	}
	return n.WriteString(string(p))
}

func (n *nameOnly) WriteString(s string) (int, error) {
	if !n.named {
		at := strings.IndexByte(s, ':')
		if at < 0 {
			return n.w.WriteString(s)
		}
		n.named = true
		_, _ = n.w.WriteString(s[:at+1])
		n.dropped += len(s) - at - 1
		return len(s), nil
	}
	n.dropped += len(s)
	return len(s), nil
}

// writeFolded writes a field's lines, each break a CRLF. bare says some aren't yet.
func writeFolded(w wordSink, span string, bare bool) {
	if !bare {
		_, _ = w.WriteString(span)
		return
	}
	for {
		i := strings.IndexByte(span, '\n')
		if i < 0 {
			_, _ = w.WriteString(span)
			return
		}
		_, _ = w.WriteString(strings.TrimSuffix(span[:i], "\r"))
		_, _ = w.WriteString("\r\n")
		span = span[i+1:]
	}
}

func downgradeField(w wordSink, name, value string) {
	value = strings.TrimSpace(value)
	key := strings.ToLower(strings.TrimSpace(name))
	switch {
	case unstructuredFields[key]:
		_, _ = w.WriteString(name)
		_, _ = w.WriteString(": ")
		writeEncodedText(w, value)
		return
	case addressFields[key]:
		if list, ok := downgradeAddressList(value); ok {
			_, _ = w.WriteString(name)
			_, _ = w.WriteString(": ")
			_, _ = w.WriteString(list)
			return
		}
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

// DowngradedSize is the octet count of DowngradeHeaders(raw): what a POP3 or
// IMAP client that has not enabled UTF-8 receives. It equals len(raw) when the
// header block is already ASCII. Nothing is built to measure it.
func DowngradedSize(raw string) int {
	header, sep, body := splitHeader(raw)
	if !containsNonASCII(header) {
		return len(raw)
	}
	var size headerSink
	writeDowngraded(&size, header, sep)
	return int(size) + len(body)
}
