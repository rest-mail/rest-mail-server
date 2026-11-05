package message

import (
	"cmp"
	"mime"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// parseMediaType is mime.ParseMediaType with RFC 2231 parameters read here, because
// it drops one in any charset but UTF-8 and US-ASCII, and keeps only the later
// segments of a continued one, and none of one with no first segment. v holds no
// comments, and a field holding an 8-bit octet has been through readableField. See
// extendedValue for what a parameter becomes.
func parseMediaType(v string) (string, map[string]string, error) {
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
			continue
		}
		// Delimiter lines match a boundary's whole value, and this is only part of one.
		_, plain := params[base]
		if plain || (base == "boundary" && strings.HasPrefix(mt, "multipart/")) {
			continue
		}
		if value, ok := laterSegments(base, pieces); ok {
			params[base] = value
		}
	}
	return mt, params, nil
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

// cleanValue reports whether value is one a reader of the field can take as it is.
func cleanValue(value string) bool {
	return (isToken(value) || isQuotedString(value)) && !strings.ContainsAny(value, "\r\n")
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

// laterSegments is the text of a continued parameter whose first segment never came,
// in segment order. Nothing stands in for what is missing: only a first segment can
// name a charset (RFC 2231 §4.1), so the octets name none, and a gap closes up.
func laterSegments(base string, pieces map[string]string) (string, bool) {
	type segment struct{ number, key string }
	var segments []segment
	for key := range pieces {
		suffix := strings.TrimPrefix(key, base+"*")
		if suffix != "" && validExtendedSuffix(suffix) {
			number := strings.TrimLeft(strings.TrimSuffix(suffix, "*"), "0")
			segments = append(segments, segment{number, key})
		}
	}
	if len(segments) == 0 {
		return "", false
	}
	// A number can be too long to parse, and without leading zeros the longer is larger.
	// The name settles a tie, so the same field always reads the same.
	slices.SortFunc(segments, func(a, b segment) int {
		return cmp.Or(cmp.Compare(len(a.number), len(b.number)), cmp.Compare(a.number, b.number), cmp.Compare(a.key, b.key))
	})
	var octets strings.Builder
	for _, s := range segments {
		if v := pieces[s.key]; strings.HasSuffix(s.key, "*") {
			octets.WriteString(percentDecode(v))
		} else {
			octets.WriteString(v)
		}
	}
	return keptValue(base, "", octets.String()), true
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
// message depends on them, so they stay plain text when they are ASCII. A boundary
// that isn't stays its octets, whatever charset it names, because delimiter lines
// match octets and nothing decodes those.
func keptValue(base, charset, octets string) string {
	switch {
	case base == "boundary", !validWordCharset(charset), strings.EqualFold(charset, "UTF-8"), strings.EqualFold(charset, "US-ASCII"):
		return octets
	case isLatin1(charset):
		return latin1ToUTF8([]byte(octets))
	case base == "charset" && !has8bit(octets):
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

// fieldPiece is one ';'-separated piece of a field holding an 8-bit octet, as
// readField reads it.
type fieldPiece struct {
	raw      string   // as sent, trimmed
	key      string   // text before the first '=', comments cut out, trimmed
	value    string   // text after it, from the first character that isn't a comment to the last
	hasValue bool     // there was an '='
	comments []string // comments around the key and value, with their delimiters
	broken   string   // first piece only: what follows the type from an unbalanced delimiter on
}

// readField reads a field holding an 8-bit octet without refusing it. RFC 2045 §5.1
// has no reading for most of what this accepts, but mailers do send name=Jörg (1).pdf
// and name=Jö"rg.pdf, and refusing one would cost the real type and every other
// parameter.
//
//   - A quoted-string or comment that closes is one, and a ';' inside it separates
//     nothing. A '"' or '(' that never closes, or a stray ')', is text.
//   - A value runs from the first character that isn't white space or a comment to
//     the last. A comment with value text on both sides is that text, because a
//     value can't be split, and a comment around the value is a comment.
//
// It takes time linear in value, however many delimiters never close.
func readField(value string) []fieldPiece {
	closers := commentClosers(value)
	quotes := true // false once a '"' has found nothing to close it
	var pieces []fieldPiece
	start, eq, broken := 0, -1, -1
	var comments [][2]int
	flush := func(end int) {
		pieces = append(pieces, newFieldPiece(value, start, end, eq, broken, comments, len(pieces) == 0))
		start, eq, broken, comments = end+1, -1, -1, nil
	}
	for i := 0; i < len(value); i++ {
		switch c := value[i]; c {
		case '"':
			end := -1
			if quotes {
				if end = quoteEnd(value, i); end < 0 {
					quotes = false
				}
			}
			if end >= 0 {
				i = end
			} else if broken < 0 {
				broken = i
			}
		case '(':
			if end, ok := closers[i]; ok {
				comments = append(comments, [2]int{i, end + 1})
				i = end
			} else if broken < 0 {
				broken = i
			}
		case ')':
			if broken < 0 {
				broken = i
			}
		case '=':
			if eq < 0 {
				eq = i
			}
		case ';':
			flush(i)
		}
	}
	flush(len(value))
	return pieces
}

// newFieldPiece reads value[start:end]: see readField. eq is the first '=' and broken
// the first unbalanced delimiter in it, or -1, and comments are where its comments are.
func newFieldPiece(value string, start, end, eq, broken int, comments [][2]int, first bool) fieldPiece {
	piece := fieldPiece{raw: trimWSP(value[start:end])}
	if first {
		if broken >= 0 {
			piece.broken = trimWSP(value[broken:end])
			piece.raw = trimWSP(value[start:broken])
			end = broken
		}
		piece.key, _ = cutComments(value, start, end, comments)
		return piece
	}
	keyEnd := end
	if eq >= 0 {
		keyEnd = eq
	}
	piece.key, piece.comments = cutComments(value, start, keyEnd, comments)
	if eq < 0 {
		return piece
	}
	from, to := eq+1, end
	// comments is in order, so each end of the value walks it once.
	lo, hi := 0, len(comments)-1
	for lo <= hi && comments[lo][0] < from {
		lo++
	}
	for {
		for from < to && isHeaderSpace(value[from]) {
			from++
		}
		if lo > hi || comments[lo][0] != from {
			break
		}
		piece.comments = append(piece.comments, value[comments[lo][0]:comments[lo][1]])
		from = comments[lo][1]
		lo++
	}
	var trailing []string
	for {
		for to > from && isHeaderSpace(value[to-1]) {
			to--
		}
		if lo > hi || comments[hi][1] != to {
			break
		}
		trailing = append(trailing, value[comments[hi][0]:comments[hi][1]])
		to = comments[hi][0]
		hi--
	}
	for i := len(trailing) - 1; i >= 0; i-- {
		piece.comments = append(piece.comments, trailing[i])
	}
	piece.value, piece.hasValue = value[from:to], true
	if piece.key == "" {
		// No name. The whole of it is one, as what has no value is.
		piece.key, piece.value, piece.hasValue = "="+piece.value, "", false
	}
	return piece
}

// cutComments is value[start:end] with each comment in it replaced by a space, as
// uncommentMIME does, and those comments. A comment after end is left for later.
func cutComments(value string, start, end int, comments [][2]int) (string, []string) {
	var b strings.Builder
	var cut []string
	at := start
	for _, comment := range comments {
		if comment[0] >= end {
			break
		}
		b.WriteString(value[at:comment[0]])
		b.WriteByte(' ')
		cut = append(cut, value[comment[0]:comment[1]])
		at = comment[1]
	}
	if at < end {
		b.WriteString(value[at:end])
	}
	return trimWSP(b.String()), cut
}

// commentClosers pairs each '(' with the ')' that closes it, in one pass. A quoted-pair
// is skipped wherever it stands. A '(' inside a quoted-string pairs here too, but only
// one outside a quoted-string is ever looked up.
func commentClosers(value string) map[int]int {
	if strings.IndexByte(value, '(') < 0 {
		return nil
	}
	closers := map[int]int{}
	var open []int
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\':
			i++
		case '(':
			open = append(open, i)
		case ')':
			if len(open) > 0 {
				closers[open[len(open)-1]] = i
				open = open[:len(open)-1]
			}
		}
	}
	return closers
}

// quoteEnd is where the quoted-string opening at value[open] closes, or -1.
func quoteEnd(value string, open int) int {
	for i := open + 1; i < len(value); i++ {
		switch value[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// parameterName spells a parameter's name so it may stand as a token (RFC 2045 §5.1).
// An 8-bit octet has no other spelling: neither RFC 2231 nor an encoded word (RFC 2047
// §5) reaches a name. '%' is a token character, so what's spelled %XX reads as a name
// like any other, and the text survives. What makes a name RFC 2231 stays outside.
func parameterName(key string) string {
	base, suffix, extended := strings.Cut(key, "*")
	switch {
	case !extended && isToken(base):
		return key
	case !extended || !validExtendedSuffix(suffix):
		return percentEncode(key)
	case isToken(base):
		return key
	}
	return percentEncode(base) + "*" + suffix
}

// validExtendedSuffix reports whether suffix is what follows the first '*' of an RFC
// 2231 name: nothing, a segment number, or one and its own '*'. mime.ParseMediaType
// would drop a parameter with anything else after one.
func validExtendedSuffix(suffix string) bool {
	number := strings.TrimSuffix(suffix, "*")
	if suffix != "" && number == "" {
		return false
	}
	for i := 0; i < len(number); i++ {
		if number[i] < '0' || number[i] > '9' {
			return false
		}
	}
	return true
}

var lineBreaksAsSpaces = strings.NewReplacer("\r", " ", "\n", " ")

// readableField rewrites a field holding an 8-bit octet into what mime.ParseMediaType
// accepts: no comments, every name a token (see parameterName), every value a token or
// a quoted-string, and each name once. See readField for how it reads, and for a
// repeated name the first stands, which is also what ParameterField keeps as one.
func readableField(value string) string {
	pieces := readField(value)
	var b strings.Builder
	b.WriteString(pieces[0].key)
	seen := map[string]bool{}
	for _, piece := range pieces[1:] {
		if piece.key == "" {
			continue
		}
		name := parameterName(piece.key)
		if seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		text := piece.value
		if !cleanValue(text) {
			text = quoteString(lineBreaksAsSpaces.Replace(text))
		}
		b.WriteString("; " + name + "=" + text)
	}
	return b.String()
}

// restoreNames puts the names parameterName spelled back, once the field is read,
// because a session that took the field as stored agreed to read UTF-8 (RFC 6855).
// stored is the field as sent, and a field that was 7-bit needed no spelling.
func restoreNames(stored string, params map[string]string) map[string]string {
	if !has8bit(stored) {
		return params
	}
	names := map[string]string{}
	for key := range params {
		if name := percentDecode(key); strings.Contains(key, "%") && !isToken(name) && utf8.ValidString(name) {
			names[key] = name
		}
	}
	for key, name := range names {
		params[name] = params[key]
		delete(params, key)
	}
	return params
}

// ParameterField makes a Content-Type or Content-Disposition field 7-bit by changing
// only what holds an 8-bit octet, or what a reader of the 7-bit result couldn't read.
// Every other parameter stays as it was sent, in its order, because the text of one
// go-imap can't read is still the only copy of it. ok is false when the value holds
// no readable type, or the type itself is 8-bit.
func ParameterField(name, value string) (string, bool) {
	return parameterField(name, value, "")
}

// parameterField is ParameterField with the boundary parameter, whichever way it was
// spelled, replaced by boundary when that isn't empty.
func parameterField(name, value, boundary string) (string, bool) {
	value = trimWSP(value)
	mt, _, err := parseContentType(value)
	if err != nil || has8bit(mt) {
		return "", false
	}
	pieces := readField(value)
	head := sevenBitComments(pieces[0].raw)
	if text := pieces[0].broken; text != "" {
		head += " (" + strings.Join(encodedWords(text), " ") + ")"
	}
	// Each element is a parameter and any comments that went on after it.
	out := [][]string{{head}}
	extended := extendedFamilies(pieces[1:])
	seen := map[string]bool{}
	boundaryAt := -1
	for _, piece := range pieces[1:] {
		at := len(out) - 1
		switch key := parameterName(piece.key); {
		case piece.key == "":
		case boundary != "" && isBoundaryName(key):
			if boundaryAt >= 0 {
				at = boundaryAt
				break
			}
			base, _, _ := strings.Cut(key, "*")
			out = append(out, []string{base + `="` + boundary + `"`})
			at, boundaryAt = len(out)-1, len(out)-1
		case seen[strings.ToLower(key)]:
			out[at] = append(out[at], "("+strings.Join(encodedWords(repeatedText(piece)), " ")+")")
		default:
			seen[strings.ToLower(key)] = true
			if !has8bit(piece.raw) && piece.hasValue && key == piece.key && cleanValue(piece.value) {
				out = append(out, []string{piece.raw})
				continue
			}
			out = append(out, []string{parameterValue(key, piece.value, extended)})
			at = len(out) - 1
		}
		for _, comment := range piece.comments {
			out[at] = append(out[at], sevenBitComments(comment))
		}
	}
	var b strings.Builder
	b.WriteString(name + ": ")
	for i, parts := range out {
		if i > 0 {
			b.WriteString(";\r\n ")
		}
		b.WriteString(strings.Join(parts, " "))
	}
	return b.String(), true
}

func isBoundaryName(key string) bool {
	base, _, _ := strings.Cut(key, "*")
	return strings.EqualFold(base, "boundary")
}

// repeatedText is the text of a parameter whose name has already stood. It goes in a
// comment, because two values for one name make a field unreadable.
func repeatedText(piece fieldPiece) string {
	if !piece.hasValue {
		return piece.key
	}
	return piece.key + "=" + piece.value
}

// extendedFamilies names each parameter sent in RFC 2231 form, in lower case.
func extendedFamilies(pieces []fieldPiece) map[string]bool {
	families := map[string]bool{}
	for _, piece := range pieces {
		if base, _, ok := strings.Cut(parameterName(piece.key), "*"); ok {
			families[strings.ToLower(base)] = true
		}
	}
	return families
}

// parameterValue spells key=value in 7 bits. key is already a token. A value that
// isn't a token or a quoted-string reads as if it were quoted, see readField.
//
//   - 7-bit text is a quoted-string when it can't stand as it is.
//   - A segment already extended keeps its charset: its 8-bit octets are escaped.
//   - Otherwise it becomes extended (RFC 2231 §4), and a first or only segment names
//     what the octets are, as an encoded word would. A later segment can't name one
//     (§4.1), and reads in the charset of the first, as it did as stored.
//   - A plain value beside an extended form of the same parameter becomes encoded
//     words in its quoted-string, which readers take the extended form over, since
//     two extended forms that differ make the field unreadable. RFC 2047 §5 forbids
//     that spelling, but it is the only one that keeps both.
func parameterValue(key, value string, extended map[string]bool) string {
	sevenBit := !has8bit(value) && !strings.ContainsAny(value, "\r\n")
	if sevenBit && cleanValue(value) {
		return key + "=" + value
	}
	if sevenBit {
		return key + "=" + quoteString(value)
	}
	text := value
	quoted := isQuotedString(value)
	if quoted {
		text = unquoteParameter(value)
	}
	base, segment, isExtended := strings.Cut(key, "*")
	switch {
	case !isExtended && extended[strings.ToLower(base)]:
		return key + `="` + strings.Join(encodedWords(text), " ") + `"`
	case !isExtended:
		return key + "*=" + wordCharset(text) + "''" + percentEncode(text)
	case segment == "" || strings.HasSuffix(segment, "*"):
		escaped := escapeOctets(value)
		if !quoted && !isToken(escaped) {
			escaped = quoteString(escaped)
		}
		return key + "=" + escaped
	case segment == "0":
		return key + "*=" + wordCharset(text) + "''" + percentEncode(text)
	}
	return key + "*=" + percentEncode(text)
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

// escapeOctets spells only the 8-bit octets of an extended value %XX, and a CR or LF,
// which a quoted-string may not hold either.
func escapeOctets(value string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		if c := value[i]; c >= 0x80 || c == '\r' || c == '\n' {
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
