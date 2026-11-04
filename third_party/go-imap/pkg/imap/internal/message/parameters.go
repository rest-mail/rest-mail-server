package message

import "strings"

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
	value = strings.TrimSpace(value)
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
	b.WriteString(name + ": " + sevenBitComments(strings.TrimSpace(segments[0])))
	for _, segment := range segments[1:] {
		segment = strings.TrimSpace(segment)
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
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	if !ok || has8bit(key) {
		return "", false
	}
	out, ok := parameterValue(key, value, extended)
	if !ok {
		return "", false
	}
	var b strings.Builder
	b.WriteString(out)
	for _, comment := range comments {
		b.WriteString(" " + sevenBitComments(comment))
	}
	return b.String(), true
}

// parameterValue spells key=value in 7 bits. mime.ParseMediaType refuses an 8-bit
// token, so an 8-bit value here is a quoted-string.
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
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", false
	}
	text := unquoteParameter(value)
	base, segment, isExtended := strings.Cut(key, "*")
	switch {
	case !isExtended && extended[strings.ToLower(base)]:
		return key + `="` + strings.Join(encodedWords(text), " ") + `"`, true
	case !isExtended:
		return key + "*=" + wordCharset(text) + "''" + percentEncode(text), true
	case segment == "" || strings.HasSuffix(segment, "*"):
		return key + "=" + escapeOctets(value), true
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
