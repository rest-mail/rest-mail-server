package mime

import "strings"

// StripAuthResults removes from the header section of raw every
// Authentication-Results field that claims authservID (RFC 8601 §5): a
// receiver must not let mail arrive already carrying the verdicts it writes
// itself. A field whose authserv-id cannot be read is removed too, since it
// cannot be told apart from one of ours. The authserv-id is compared
// case-insensitively. Other fields, the body, and Authentication-Results from
// other servers are left byte for byte; raw is returned unchanged when there is
// nothing to remove.
//
// The delivery handlers call it on the raw message before anything parses,
// verifies or stores it, so a stored message carries only the
// Authentication-Results this server wrote.
func StripAuthResults(raw, authservID string) string {
	var out strings.Builder
	rest := raw
	removed := false
	// Walk the header section one field (a line and its continuation lines) at
	// a time, up to the empty line that ends it.
	for rest != "" && rest[0] != '\n' && !strings.HasPrefix(rest, "\r\n") {
		end := fieldEnd(rest)
		field := rest[:end]
		rest = rest[end:]
		if claimsAuthServID(field, authservID) {
			removed = true
			continue
		}
		out.WriteString(field)
	}
	if !removed {
		return raw
	}
	out.WriteString(rest)
	return out.String()
}

// fieldEnd returns the length of the header field that starts s: its first
// line and every continuation line (one starting with a space or tab), each
// with its line ending.
func fieldEnd(s string) int {
	i := 0
	for {
		nl := strings.IndexByte(s[i:], '\n')
		if nl < 0 {
			return len(s)
		}
		i += nl + 1
		if i >= len(s) || (s[i] != ' ' && s[i] != '\t') {
			return i
		}
	}
}

// claimsAuthServID reports whether field is an Authentication-Results field
// whose authserv-id is authservID or cannot be read.
func claimsAuthServID(field, authservID string) bool {
	colon := strings.IndexByte(field, ':')
	if colon < 0 || !strings.EqualFold(strings.TrimRight(field[:colon], " \t"), "Authentication-Results") {
		return false
	}
	id, ok := readAuthServID(field[colon+1:])
	return !ok || strings.EqualFold(id, authservID)
}

// readAuthServID reads the authserv-id that starts an Authentication-Results
// value: a token or a quoted-string after optional comments and folding
// whitespace (RFC 8601 §2.2).
func readAuthServID(v string) (string, bool) {
	v = skipCFWS(v)
	if v == "" {
		return "", false
	}
	if v[0] == '"' {
		var b strings.Builder
		for i := 1; i < len(v); i++ {
			switch c := v[i]; c {
			case '\\':
				i++
				if i < len(v) {
					b.WriteByte(v[i])
				}
			case '"':
				return b.String(), b.Len() > 0
			case '\r', '\n':
				// A fold inside the quoted-string.
			default:
				b.WriteByte(c)
			}
		}
		return "", false
	}
	end := 0
	for end < len(v) && isTokenChar(v[end]) {
		end++
	}
	return v[:end], end > 0
}

// skipCFWS returns s after any leading whitespace, line folds and (nested)
// comments, or "" when a comment is never closed.
func skipCFWS(s string) string {
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case depth > 0 && c == '\\':
			i++
		case c == '(':
			depth++
		case depth > 0 && c == ')':
			depth--
		case depth > 0, c == ' ', c == '\t', c == '\r', c == '\n':
		default:
			return s[i:]
		}
	}
	return ""
}

// isTokenChar reports whether c may appear in an RFC 2045 token.
func isTokenChar(c byte) bool {
	return c > ' ' && c < 0x7f && !strings.ContainsRune(`()<>@,;:\"/[]?=`, rune(c))
}
