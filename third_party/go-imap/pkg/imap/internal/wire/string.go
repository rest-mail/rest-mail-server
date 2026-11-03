package wire

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// QuotedStringEnd returns the index, within s, of the closing double quote of
// the IMAP quoted-string that s begins with (s[0] must be '"'). It skips
// backslash-escaped characters so an escaped \" does not close the string
// (RFC 3501 §4.3). It returns -1 when the string is unterminated.
func QuotedStringEnd(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			// Skip the escaped character; a trailing backslash escapes nothing.
			if i+1 < len(s) {
				i++
			}
		case '"':
			return i
		}
	}
	return -1
}

// unescapeQuoted decodes the backslash escapes of an IMAP quoted-string body
// (the text between the surrounding quotes). Per RFC 3501 §4.3 the only two
// escapes are \" -> " and \\ -> \; a backslash before any other character, or a
// trailing backslash, is preserved as a literal backslash.
func unescapeQuoted(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
			i++ // drop the backslash, emit the escaped character below
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Unquote removes the surrounding double quotes from an IMAP quoted-string and
// decodes its backslash escapes (\" -> ", \\ -> \; RFC 3501 §4.3). A value that
// is not a quoted-string (an atom) is returned unchanged — atoms carry no
// escapes. This is the counterpart to QuoteString, which escapes on output.
func Unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return unescapeQuoted(s[1 : len(s)-1])
	}
	return s
}

// QuoteString encodes an IMAP nstring (RFC 3501 §9): NIL for empty, otherwise
// the same quoted-or-literal framing as EncodeMailboxName. Envelope address
// components are decoded by net/mail, so an RFC 2047 personal name can carry
// 8-bit octets or even CR/LF; those must travel as a counted literal rather than
// break the quoted string or inject response lines.
func QuoteString(s string) string {
	if s == "" {
		return "NIL"
	}
	return EncodeMailboxName(s)
}

// EncodeMailboxName encodes a mailbox name for output in a response as an IMAP
// astring (RFC 3501 §4.3, §9 — a mailbox name is an astring, and §7.2.2/§7.2.3
// render it that way in LIST and STATUS). A name a quoted-string can carry is
// emitted quoted, with the quoted-specials '"' and '\' backslash-escaped so an
// embedded quote or backslash cannot terminate the string early or corrupt the
// line. A name containing CR, LF, NUL, or an 8-bit octet — none of which a
// quoted-string may hold (its content is 7-bit TEXT-CHAR excluding CR/LF) — is
// emitted as a synchronizing literal, so those octets travel verbatim and,
// critically, a CR/LF in a backend-supplied folder name cannot inject additional
// response lines. An empty name encodes as "" (never NIL): a mailbox name is
// never absent. This is the encoder EVERY mailbox name emitted in a response must
// pass through; interpolating a raw name would break client parsing and, on
// CR/LF, allow response injection.
func EncodeMailboxName(name string) string {
	if mailboxNeedsLiteral(name) {
		return fmt.Sprintf("{%d}\r\n%s", len(name), name)
	}
	name = strings.ReplaceAll(name, `\`, `\\`)
	name = strings.ReplaceAll(name, `"`, `\"`)
	return `"` + name + `"`
}

// mailboxNeedsLiteral reports whether name contains an octet a quoted-string
// cannot represent — CR, LF, NUL, or any 8-bit octet — and so must be sent as a
// literal instead (RFC 3501 §4.3).
func mailboxNeedsLiteral(name string) bool {
	for i := 0; i < len(name); i++ {
		if c := name[i]; c == '\r' || c == '\n' || c == 0x00 || c >= 0x80 {
			return true
		}
	}
	return false
}

// ValidAstring reports whether s is exactly one RFC 3501 §9 astring: an atom of
// ASTRING-CHARs or a quoted-string whose only escapes are \" and \\. NUL is
// refused in both forms. Octets above 0x7F are let through a quoted-string,
// because a spliced literal carries them there.
func ValidAstring(s string) bool {
	if s == "" {
		return false
	}
	if s[0] != '"' {
		for i := 0; i < len(s); i++ {
			if c := s[i]; c <= 0x20 || c >= 0x7f || strings.IndexByte(`(){%*"\`, c) >= 0 {
				return false
			}
		}
		return true
	}
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case 0x00:
			return false
		case '\\':
			if i+1 >= len(s) || (s[i+1] != '"' && s[i+1] != '\\') {
				return false
			}
			i++
		case '"':
			return i == len(s)-1
		}
	}
	return false
}

// ParseAstring consumes a leading IMAP astring — a quoted-string or an atom
// (RFC 3501 §4.3 / §9) — from s, returning the decoded value and the remainder
// of s after the token. ok is false when s does not begin with a well-formed
// astring (e.g. an unterminated quoted-string). Leading spaces are skipped.
//
// Literal astrings ({n}...) are intentionally not handled here: APPEND's target
// mailbox is taken from the command line, where clients send it either quoted or
// as a bare atom. An atom runs up to the first space, '(' or '{' — the grammar's
// separators between the mailbox and an optional flag-list or the message
// literal — so it is decoded even without surrounding quotes.
func ParseAstring(s string) (val, rest string, ok bool) {
	s = strings.TrimLeft(s, " ")
	if s == "" {
		return "", "", false
	}
	if s[0] == '"' {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			switch c := s[i]; c {
			case '\\': // quoted-specials are backslash-escaped inside a quoted-string
				if i+1 >= len(s) {
					return "", "", false
				}
				i++
				b.WriteByte(s[i])
			case '"':
				return b.String(), s[i+1:], true
			default:
				b.WriteByte(c)
			}
		}
		return "", "", false // unterminated quoted-string
	}
	if end := strings.IndexAny(s, " ({"); end >= 0 {
		return s[:end], s[end:], true
	}
	return s, "", true
}

// ValidAppendQuotedMailbox checks RFC 3501 §9's quoted grammar before APPEND
// decodes the mailbox with ParseAstring. Only DQUOTE and backslash may be
// escaped; CHAR excludes NUL and 8-bit octets, and TEXT-CHAR excludes CR/LF.
// With utf8 set, RFC 6855 §3's uQUOTED-CHAR also allows 8-bit octets, which
// the caller has already checked are well-formed UTF-8.
func ValidAppendQuotedMailbox(token string, utf8 bool) bool {
	if len(token) < 2 || token[0] != '"' || token[len(token)-1] != '"' {
		return false
	}
	for i := 1; i < len(token)-1; i++ {
		c := token[i]
		if c == 0 || c == '\r' || c == '\n' || (c >= 0x80 && !utf8) {
			return false
		}
		switch c {
		case '\\':
			i++
			if i >= len(token)-1 || (token[i] != '\\' && token[i] != '"') {
				return false
			}
		case '"':
			return false
		}
	}
	return true
}

// QuoteLiteral re-encodes literal octets as an IMAP quoted-string so they can be
// spliced back into the command line in place of the "{n}" marker and consumed by
// the existing astring parsers (Unquote / ParseArgs). Any embedded '"' or
// '\' is backslash-escaped (RFC 3501 §4.3) so it cannot terminate the string
// early. Unlike QuoteString it emits "" (never NIL) for empty input, so a {0}
// literal becomes an explicit empty-string argument rather than a NIL token.
func QuoteLiteral(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b) + 2)
	sb.WriteByte('"')
	for _, c := range b {
		if c == '"' || c == '\\' {
			sb.WriteByte('\\')
		}
		sb.WriteByte(c)
	}
	sb.WriteByte('"')
	return sb.String()
}

// QuoteUpper returns s uppercased and IMAP-quoted, or fallback (already a bare
// word) quoted when s is empty.
func QuoteUpper(s, fallback string) string {
	if s == "" {
		s = fallback
	}
	return EncodeMailboxName(strings.ToUpper(s))
}

// NilOrQuote encodes a MIME nstring, using a counted literal for octets that
// cannot appear in a quoted string, or NIL when the header value is empty.
func NilOrQuote(s string) string {
	if s == "" {
		return "NIL"
	}
	return EncodeMailboxName(s)
}

func IsASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// ValidCommandUTF8 reports whether the command text a client typed, never a
// literal's octets, may carry its 8-bit octets. RFC 6855 §3 lets a UTF8=ACCEPT
// server's quoted-strings hold UTF8-2/3/4 and requires BAD for any octet
// sequence with the high bit set that is not well-formed UTF-8. Outside a
// quoted-string no 8-bit octet is legal at all, so the whole line is checked.
func ValidCommandUTF8(text string) bool {
	if !utf8.ValidString(text) {
		return false
	}
	quoted, escaped := false, false
	for i := range len(text) {
		c := text[i]
		if escaped {
			escaped = false
			continue
		}
		switch c {
		case '"':
			quoted = !quoted
		case '\\':
			escaped = quoted
		default:
			if c >= 0x80 && !quoted {
				return false
			}
		}
	}
	// Individual command parsers still validate quote balance and escapes.
	return true
}

// ValidUTF8MailboxName reports whether name is a mailbox name RFC 6855 §3
// allows: well-formed UTF-8 (RFC 5198 §2) with no C0 control (U+0000-U+001F),
// DELETE (U+007F), C1 control (U+0080-U+009F), LINE SEPARATOR (U+2028) or
// PARAGRAPH SEPARATOR (U+2029). Net-Unicode also asks for NFC (a SHOULD); this
// package has no normalisation tables, so that is left to the backend.
func ValidUTF8MailboxName(name string) bool {
	if !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		switch {
		case r <= 0x1f, r == 0x7f, r >= 0x80 && r <= 0x9f, r == 0x2028, r == 0x2029:
			return false
		}
	}
	return true
}
