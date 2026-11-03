package message

import "strings"

// knownTransferEncoding reports whether we can interpret this entity's body.
// RFC 2045 §6.4 requires an unrecognized encoding to override Content-Type
// with application/octet-stream, including for composite entities.
func knownTransferEncoding(value string) bool {
	switch transferEncoding(value) {
	case "7BIT", "8BIT", "BINARY", "BASE64", "QUOTED-PRINTABLE":
		return true
	}
	return false
}

// transferEncoding removes lexical whitespace/comments around the single MIME
// encoding token (RFC 2045 §§1, 6.1; RFC 822 §3.4). It describes the stored
// encoding without decoding the body. Malformed fields retain the historical
// opaque-value behavior; in particular, comments cannot join two token pieces.
func transferEncoding(value string) string {
	if value == "" {
		return "7BIT"
	}
	original := strings.ToUpper(value)
	token := ""
	for i := 0; i < len(value); {
		switch value[i] {
		case ' ', '\t':
			i++
		case '(':
			depth := 1
			i++
			for i < len(value) && depth > 0 {
				switch value[i] {
				case '\\':
					i++ // quoted-pair, including an escaped parenthesis
				case '(':
					depth++
				case ')':
					depth--
				}
				i++
			}
			if depth != 0 {
				return original
			}
		default:
			if token != "" {
				return original // a second token is not one encoding mechanism
			}
			start := i
			for i < len(value) && value[i] > 32 && value[i] < 127 && !strings.ContainsRune("()<>@,;:\\\"/[]?=", rune(value[i])) {
				i++
			}
			if start == i {
				return original
			}
			token = value[start:i]
		}
	}
	if token == "" {
		return original
	}
	return strings.ToUpper(token)
}
