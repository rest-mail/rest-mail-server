package message

import (
	"mime"
	"strconv"
	"strings"
)

// parseMediaType is mime.ParseMediaType with RFC 2231 parameters read here, because
// it drops one in any charset but UTF-8 and US-ASCII, and keeps only the later
// segments of a continued one, and with an unquoted 8-bit value read as if it were
// quoted, because it refuses the whole field for one. v holds no comments. See
// extendedValue for what a parameter becomes.
func parseMediaType(v string) (string, map[string]string, error) {
	v = quoteBareValues(v)
	mt, params, err := mime.ParseMediaType(v)
	if err != nil || !strings.Contains(v, "*") {
		return mt, params, err
	}
	segments, ok := fieldSegments(v)
	if !ok {
		return mt, params, nil
	}
	families := map[string]map[string]string{}
	for _, segment := range segments[1:] {
		key, value, ok := strings.Cut(segment, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		base, _, extended := strings.Cut(key, "*")
		if !extended {
			continue
		}
		if families[base] == nil {
			families[base] = map[string]string{}
		}
		families[base][key] = unquoteParameter(strings.TrimSpace(value))
	}
	for base, pieces := range families {
		if value, ok := extendedValue(base, pieces); ok {
			params[base] = value
		}
	}
	return mt, params, nil
}

// quoteBareValues quotes each parameter value that holds an 8-bit octet and isn't a
// quoted-string. A token may not hold one (RFC 2045 §5.1), but mailers do send
// name=Jörg.pdf, and refusing it would cost the real type and every other parameter.
// The value runs to the next ';', white space included. v holds no comments.
func quoteBareValues(v string) string {
	if !has8bit(v) {
		return v
	}
	segments, ok := fieldSegments(v)
	if !ok {
		return v
	}
	for i, segment := range segments {
		key, value, ok := strings.Cut(segment, "=")
		value = trimWSP(value)
		if i == 0 || !ok || !has8bit(value) || isQuotedString(value) {
			continue
		}
		segments[i] = key + "=" + quoteString(value)
	}
	return strings.Join(segments, ";")
}

// isQuotedString reports whether value is one whole quoted-string.
func isQuotedString(value string) bool {
	if len(value) < 2 || value[0] != '"' {
		return false
	}
	for i := 1; i < len(value); i++ {
		switch value[i] {
		case '\\':
			i++
		case '"':
			return i == len(value)-1
		}
	}
	return false
}

func quoteString(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

// isToken reports whether value may stand as a MIME token (RFC 2045 §5.1).
func isToken(value string) bool {
	for i := 0; i < len(value); i++ {
		if c := value[i]; c <= 0x20 || c >= 0x7f || strings.IndexByte(`()<>@,;:\"/[]?=`, c) >= 0 {
			return false
		}
	}
	return value != ""
}

// trimWSP trims white space only. strings.TrimSpace would also take a no-break space
// or NEL off the end of a value, and those are its text.
func trimWSP(s string) string {
	return strings.Trim(s, " \t\r\n")
}

// extendedValue puts an RFC 2231 parameter back together, in the order
// mime.ParseMediaType reads it. pieces holds its segments by lower-case name, each
// unquoted. A value in a charset go-imap decodes, or naming none, is its text; one
// in any other charset goes on as a word that names the charset (see keptValue).
func extendedValue(base string, pieces map[string]string) (string, bool) {
	if v, ok := pieces[base+"*"]; ok {
		charset, octets := splitExtendedValue(v)
		return keptValue(base, charset, octets), true
	}
	var octets strings.Builder
	charset := ""
	found := false
	for n := 0; ; n++ {
		key := base + "*" + strconv.Itoa(n)
		if v, ok := pieces[key]; ok {
			octets.WriteString(v)
			found = true
			continue
		}
		v, ok := pieces[key+"*"]
		if !ok {
			break
		}
		found = true
		if n == 0 {
			charset, v = splitExtendedValue(v)
		} else {
			v = percentDecode(v)
		}
		octets.WriteString(v)
	}
	if !found {
		return "", false
	}
	return keptValue(base, charset, octets.String()), true
}

// splitExtendedValue reads charset'language'text. A value without both quotes is
// malformed, and stays the text it was sent as, naming no charset.
func splitExtendedValue(v string) (charset, octets string) {
	parts := strings.SplitN(v, "'", 3)
	if len(parts) != 3 {
		return "", v
	}
	return parts[0], percentDecode(parts[2])
}

// percentDecode reads the %XX escapes of an extended value. An escape that isn't
// one stays as it was sent.
func percentDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if c, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(c))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// keptValue is octets as a parameter value. Text in a charset go-imap decodes is
// that text, and follows metadataText like any other. Any other charset stays named:
// the value becomes one encoded word in it, in either session, so no byte is guessed.
// It stays one word however long, because go-imap can't tell where a character ends
// in that charset and a word may not split one (RFC 2047 §5); that outranks the
// length limit (§2), which no reader needs in order to decode the word.
//
// A boundary and a charset name are ASCII by their own grammar, whatever their
// RFC 2231 form names (RFC 2046 §5.1.1, RFC 2045 §5.1), and the structure of the
// message depends on them, so they stay plain text when they are ASCII.
func keptValue(base, charset, octets string) string {
	switch {
	case !validWordCharset(charset), strings.EqualFold(charset, "UTF-8"), strings.EqualFold(charset, "US-ASCII"):
		return octets
	case isLatin1(charset):
		return latin1ToUTF8([]byte(octets))
	case (base == "boundary" || base == "charset") && !has8bit(octets):
		return octets
	}
	return "=?" + charset + "?Q?" + qEncode(octets) + "?="
}

// validWordCharset reports whether charset can stand in an encoded word (RFC 2047
// §2): a token with none of the especials, not empty. An extended value can name
// anything in a quoted-string, and what isn't a charset names none.
func validWordCharset(charset string) bool {
	if charset == "" {
		return false
	}
	for i := 0; i < len(charset); i++ {
		if c := charset[i]; c <= 0x20 || c >= 0x7f || strings.IndexByte(`()<>@,;:"/[]?.=\`, c) >= 0 {
			return false
		}
	}
	return true
}

// fieldSegments splits a Content-Type or Content-Disposition value at each ';'
// outside a quoted-string or comment. ok is false when one of those never closes.
func fieldSegments(value string) (segments []string, ok bool) {
	start, comments := 0, 0
	quoted := false
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c == '\\' && (quoted || comments > 0):
			i++
		case quoted:
			quoted = c != '"'
		case c == '"' && comments == 0:
			quoted = true
		case c == '(':
			comments++
		case c == ')' && comments > 0:
			comments--
		case c == ';' && comments == 0:
			segments = append(segments, value[start:i])
			start = i + 1
		}
	}
	if quoted || comments > 0 {
		return nil, false
	}
	return append(segments, value[start:]), true
}

// unquoteParameter reads a parameter value as mime.ParseMediaType does: a backslash
// escapes only an especial, because some mailers send "C:\dev\file" unescaped. A
// value that isn't a quoted-string comes back as it is.
func unquoteParameter(value string) string {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return value
	}
	value = value[1 : len(value)-1]
	if !strings.Contains(value, `\`) {
		return value
	}
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) && strings.IndexByte(`()<>@,;:\"/[]?=`, value[i+1]) >= 0 {
			i++
		}
		b.WriteByte(value[i])
	}
	return b.String()
}

// ParameterField makes a Content-Type or Content-Disposition field 7-bit by changing
// only what holds an 8-bit octet. Every other parameter stays as it was sent, in its
// order, because the text of one go-imap can't read is still the only copy of it. ok
// is false when the value does not parse, or the type itself is 8-bit.
func ParameterField(name, value string) (string, bool) {
	value = trimWSP(value)
	mt, _, err := parseContentType(value)
	if err != nil || has8bit(mt) {
		return "", false
	}
	segments, ok := fieldSegments(value)
	if !ok {
		return "", false
	}
	extended := extendedFamilies(segments[1:])
	var b strings.Builder
	b.WriteString(name + ": " + sevenBitComments(trimWSP(segments[0])))
	for _, segment := range segments[1:] {
		segment = trimWSP(segment)
		if segment == "" {
			continue
		}
		parameter, ok := sevenBitParameter(segment, extended)
		if !ok {
			return "", false
		}
		b.WriteString(";\r\n " + parameter)
	}
	return b.String(), true
}

// extendedFamilies names each parameter sent in RFC 2231 form, in lower case.
func extendedFamilies(segments []string) map[string]bool {
	families := map[string]bool{}
	for _, segment := range segments {
		text, _, err := splitComments(segment)
		if err != nil {
			continue
		}
		key, _, _ := strings.Cut(text, "=")
		if base, _, ok := strings.Cut(strings.TrimSpace(key), "*"); ok {
			families[strings.ToLower(base)] = true
		}
	}
	return families
}

// sevenBitParameter rewrites one parameter that holds an 8-bit octet, in its value or
// its comments. The comments end up after the value, since their place means nothing
// to a reader, and they keep their text.
func sevenBitParameter(segment string, extended map[string]bool) (string, bool) {
	if !has8bit(segment) {
		return segment, true
	}
	text, comments, err := splitComments(segment)
	if err != nil {
		return "", false
	}
	key, value, ok := strings.Cut(text, "=")
	key, value = trimWSP(key), trimWSP(value)
	if !ok || has8bit(key) {
		return "", false
	}
	out, ok := parameterValue(key, value, extended)
	if !ok {
		return "", false
	}
	for _, comment := range comments {
		out += " " + sevenBitComments(comment)
	}
	return out, true
}

// parameterValue spells key=value in 7 bits. An 8-bit value that isn't a quoted-string
// reads as if it were, see quoteBareValues.
//
//   - A segment already extended keeps its charset: its 8-bit octets are escaped.
//   - Otherwise it becomes extended (RFC 2231 §4), and a first or only segment names
//     what the octets are, as an encoded word would. A later segment can't name one
//     (§4.1), and reads in the charset of the first, as it did as stored.
//   - A plain value beside an extended form of the same parameter becomes encoded
//     words in its quoted-string, which readers take the extended form over, since
//     two extended forms that differ make the field unreadable. RFC 2047 §5 forbids
//     that spelling, but it is the only one that keeps both.
func parameterValue(key, value string, extended map[string]bool) (string, bool) {
	if !has8bit(value) {
		return key + "=" + value, true
	}
	text := value
	quoted := isQuotedString(value)
	if quoted {
		text = unquoteParameter(value)
	}
	base, segment, isExtended := strings.Cut(key, "*")
	switch {
	case !isExtended && extended[strings.ToLower(base)]:
		return key + `="` + strings.Join(encodedWords(text), " ") + `"`, true
	case !isExtended:
		return key + "*=" + wordCharset(text) + "''" + percentEncode(text), true
	case segment == "" || strings.HasSuffix(segment, "*"):
		escaped := escapeOctets(value)
		if !quoted && !isToken(escaped) {
			escaped = quoteString(escaped)
		}
		return key + "=" + escaped, true
	case segment == "0":
		return key + "*=" + wordCharset(text) + "''" + percentEncode(text), true
	}
	return key + "*=" + percentEncode(text), true
}

// percentEncode spells value as an RFC 2231 extended value: every octet that may not
// stand there is %XX.
func percentEncode(value string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if isAttributeChar(c) {
			b.WriteByte(c)
		} else {
			b.Write([]byte{'%', hex[c>>4], hex[c&0x0f]})
		}
	}
	return b.String()
}

// escapeOctets spells only the 8-bit octets of an extended value %XX.
func escapeOctets(value string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		if c := value[i]; c >= 0x80 {
			b.Write([]byte{'%', hex[c>>4], hex[c&0x0f]})
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// sevenBitComments turns the text of any 8-bit comment in s into encoded words, which
// RFC 2047 §5(2) allows in a comment. What is outside a comment stays. The comment's
// own delimiters stay too, and any nested inside it go into the word.
func sevenBitComments(s string) string {
	if !has8bit(s) {
		return s
	}
	var b strings.Builder
	start, comments := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && comments > 0:
			i++
		case c == '(':
			if comments == 0 {
				b.WriteString(s[start : i+1])
				start = i + 1
			}
			comments++
		case c == ')' && comments > 0:
			comments--
			if comments == 0 {
				if text := s[start:i]; has8bit(text) {
					b.WriteString(strings.Join(encodedWords(text), " "))
				} else {
					b.WriteString(text)
				}
				start = i
			}
		}
	}
	b.WriteString(s[start:])
	return b.String()
}
