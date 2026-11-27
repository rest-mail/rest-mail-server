package wire

import "strings"

// ParseCommand splits a POP3 command line into command and argument. The command
// is upper-cased; the argument is the remainder of the line, if any.
//
// RFC 1939 §3 keywords are printable ASCII and case-insensitive, so only a-z are
// upper-cased and every other byte is kept as sent. strings.ToUpper would apply
// Unicode case mapping instead: "paſs" (U+017F) and "lıst" (U+0131) would be
// dispatched as PASS and LIST, and invalid UTF-8 would be rewritten to U+FFFD.
func ParseCommand(line string) (string, string) {
	parts := strings.SplitN(line, " ", 2)
	cmd := UpperASCII(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = parts[1]
	}
	return cmd, arg
}

// UpperASCII upper-cases the ASCII letters of s and leaves every other byte alone.
func UpperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'a' <= c && c <= 'z' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}

// credentialCommands are POP3 verbs whose arguments carry secret material that
// must never be written to logs: PASS (the cleartext password), APOP (a digest
// derived from the shared secret, replayable/attackable if leaked), and AUTH
// (SASL initial responses, which base64-encode credentials). Keys are the
// upper-cased command keyword as returned by ParseCommand.
var credentialCommands = map[string]bool{
	"PASS": true,
	"APOP": true,
	"AUTH": true,
}

// verbatimCommands are the keywords server.Session.Handle dispatches whose arguments
// are not secret, so their lines may be logged as sent. Keys are the upper-cased
// keyword as returned by ParseCommand. A keyword missing from this set is masked
// in full by RedactCommand, so a command added to the dispatcher without being
// listed here fails safe: it is hidden from the debug log, not leaked.
var verbatimCommands = map[string]bool{
	"USER": true, "QUIT": true, "STAT": true, "LIST": true, "RETR": true,
	"DELE": true, "NOOP": true, "RSET": true, "TOP": true, "UIDL": true,
	"CAPA": true, "STLS": true,
}

// unknownCommandMarker is the whole log rendering of a line whose keyword is not
// a known command.
const unknownCommandMarker = "<unknown command redacted>"

// RedactCommand returns a log-safe rendering of a raw client command line. The
// diagnostic redaction policy is:
//
//   - A credential-bearing verb (see credentialCommands) keeps its keyword and
//     has its entire argument replaced with "<redacted>", so no password, APOP
//     digest or SASL response reaches the logs. With no argument the line is
//     logged as sent.
//   - A known verb whose argument is not secret (see verbatimCommands) is logged
//     verbatim, so ordinary sessions stay debuggable.
//   - Every other line is replaced in full with unknownCommandMarker: nothing of
//     it, not even its length, is logged.
//
// The last rule exists because an unknown line is exactly where a credential
// ends up when a client gets the syntax wrong. Keywords fold ASCII case only, so
// " PASS x", "PASS\tx", "paſs x" (U+017F) and "PАSS x" (Cyrillic А) are all
// rejected as unknown commands, as are a password typed on a line by itself and
// a SASL response sent after an AUTH the server does not offer. Any of them may
// carry a secret, and the classifier cannot tell which, so none is logged. The
// classification only decides what is logged; it never changes which commands
// the session accepts.
//
// This is the redaction boundary for issue #10, which found the per-command debug
// log recording "PASS <password>" in cleartext.
func RedactCommand(line string) string {
	cmd, arg := ParseCommand(line)
	switch {
	case credentialCommands[cmd]:
		if arg == "" {
			return line
		}
		return cmd + " <redacted>"
	case verbatimCommands[cmd]:
		return line
	default:
		return unknownCommandMarker
	}
}

// CanonicalCRLF rewrites a stored message's line endings to canonical CRLF for
// POP3 transmission (RFC 1939 §3). Every LF becomes CRLF, an existing CR that
// already precedes an LF is kept (not doubled), and a bare CR not followed by
// LF is passed through verbatim (it is not a line terminator).
//
// This is a security boundary, not cosmetics: RETR/TOP frame the multi-line
// response by splitting on CRLF and dot-stuffing each line. A bare LF in
// attacker-controllable message content would otherwise survive inside a line,
// smuggling a raw line boundary onto the wire that evades dot-stuffing and can
// forge the "CRLF.CRLF" terminator (premature termination, session desync,
// response forgery). Canonicalizing first guarantees every wire line boundary
// is a real CRLF the stuffing and terminator logic can see.
func CanonicalCRLF(raw string) string {
	var b strings.Builder
	b.Grow(len(raw) + 16)
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '\n':
			// Bare LF (a preceding CR was already consumed by the '\r' case).
			b.WriteString("\r\n")
		case '\r':
			if i+1 < len(raw) && raw[i+1] == '\n' {
				b.WriteString("\r\n")
				i++ // consume the paired LF
			} else {
				b.WriteByte('\r') // bare CR: not a terminator, keep verbatim
			}
		default:
			b.WriteByte(raw[i])
		}
	}
	return b.String()
}

// WireMessage returns a stored message as RETR transmits it and an RFC 1939 §3
// client decodes it: line endings canonicalized to CRLF (see CanonicalCRLF), and
// a final CRLF added when the message does not already end in one. Every line of
// a multi-line response ends in CRLF, so a last line stored without an
// end-of-line still gets one on the wire; it is part of what the client
// receives, so it is part of the §11 octet count. An empty message stays empty.
// Byte-stuffing and the "." terminator are framing and are not included.
func WireMessage(raw string) string {
	msg := CanonicalCRLF(raw)
	if msg != "" && !strings.HasSuffix(msg, "\r\n") {
		msg += "\r\n"
	}
	return msg
}

// maxUIDLen is the RFC 1939 §7 upper bound on a UIDL unique-id: a unique-id is
// "1 to 70 characters in the range 0x21 to 0x7E".
const maxUIDLen = 70

// ValidUID reports whether uid conforms to the RFC 1939 §7 unique-id grammar:
// 1 to 70 characters, each a printable ASCII byte in the range 0x21 ('!') to
// 0x7E ('~') inclusive. This rejects the empty string, any id longer than 70
// bytes, the space (0x20), CR, LF, every other control character, DEL (0x7F),
// and all high-bit/non-ASCII bytes. Because every legal byte is single-byte
// ASCII, the byte length is also the character count.
//
// A UID that fails this test cannot be safely interpolated into a UIDL response
// line: a space would split the "n uid" pair the client parses, and a CR or LF
// would terminate the line early or inject an extra protocol line. The engine
// therefore validates each UID at UIDL-response time and answers -ERR rather
// than emitting a malformed reply (issue #15).
func ValidUID(uid string) bool {
	if len(uid) == 0 || len(uid) > maxUIDLen {
		return false
	}
	for i := 0; i < len(uid); i++ {
		if uid[i] < 0x21 || uid[i] > 0x7E {
			return false
		}
	}
	return true
}

// SplitMessageLines splits a canonical CRLF message block into the lines to
// transmit for RETR/TOP. A well-formed block ends in CRLF, so strings.Split on
// "\r\n" yields a trailing "" that is the terminator of the final line, not a
// separate empty line. SplitMessageLines drops that single trailing empty
// element so callers do not emit a spurious blank line before the "."
// terminator (RFC 1939 §5.1/§7); the transmitted octets then equal the
// advertised count (§11). An empty block yields no lines.
func SplitMessageLines(block string) []string {
	if block == "" {
		return nil
	}
	lines := strings.Split(block, "\r\n")
	if n := len(lines); lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// DotStuff applies RFC 1939 §3 byte-stuffing to one line of a multi-line
// response: a line beginning with the termination octet "." gets one more "."
// prepended, so no content line can be read as the "." terminator. The client
// removes exactly one leading "." from any line that begins with one.
func DotStuff(line string) string {
	if strings.HasPrefix(line, ".") {
		return "." + line
	}
	return line
}

// TopLines returns the lines TOP sends for a canonical CRLF message (RFC 1939
// §7): the header lines, the empty line separating them from the body, and at
// most n (n >= 0) body lines. The header section ends at the first empty line
// (RFC 5322 §2.1): a message with no empty line is all header, and a message
// that begins with an empty line has no header fields, so none of its body is
// sent as header.
func TopLines(raw string, n int) []string {
	header, body := raw, ""
	if strings.HasPrefix(raw, "\r\n") {
		header, body = "", raw[2:]
	} else if i := strings.Index(raw, "\r\n\r\n"); i >= 0 {
		header, body = raw[:i], raw[i+4:]
	}
	lines := append(SplitMessageLines(header), "")
	bodyLines := SplitMessageLines(body)
	if n > len(bodyLines) {
		n = len(bodyLines)
	}
	return append(lines, bodyLines[:n]...)
}
