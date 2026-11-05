package message

import (
	"hash/maphash"
	"mime"
	"strings"
	"sync"
	"unicode"
)

// wireType is what a conversion needs to know of an entity's type: mediaType's answer
// for an entity, without the parameters nothing here reads. A store sizes every message
// it takes, and a sender chooses how many fields and parameters that reads
// (TestWireTypeMatchesMediaType).
type wireType struct{ mt, boundary, charset string }

// wireType is n.mediaType() for a conversion.
func (n mimeNode) wireType() wireType {
	var contentType, encoding string
	if namesContentField(n.header) {
		contentType, encoding = contentFields(n.header)
	}
	if !knownTransferEncoding(encoding) {
		return wireType{mt: "application/octet-stream"}
	}
	var t wireType
	usable := false
	if contentType != "" {
		mt, params, err := parseKeptParameters(contentType)
		t = wireType{mt, params["boundary"], params["charset"]}
		usable = err == nil && strings.Contains(mt, "/") && (!strings.HasPrefix(mt, "multipart/") || t.boundary != "")
	}
	if !usable {
		t = wireType{mt: n.defaultType}
	}
	if opaqueType(t.mt, t.boundary, encoding, n.depth) {
		return wireType{mt: "application/octet-stream"}
	}
	return t
}

// contentFields is the first Content-Type and the first Content-Transfer-Encoding that
// textproto.Reader.ReadMIMEHeader reads from a header block, with what it would hold of
// every other field left unbuilt: it stops at the first line it can't read, and keeps
// what it read before that (TestContentFieldsMatchTheLibrary).
func contentFields(header string) (contentType, encoding string) {
	if header != "" && (header[0] == ' ' || header[0] == '\t') {
		return "", ""
	}
	buffer := max(min(len(header), 4096), 16)
	// A last line with no line break, in a whole number of mediaType's buffers, reads as the
	// end of input straight after a full buffer, and the reader loses it.
	lost := func(line string, ended bool) bool {
		return !ended && line != "" && len(line)%buffer == 0
	}
	rest := header
	haveType, haveEncoding := false, false
	for rest != "" && !(haveType && haveEncoding) {
		first, after, ended := cutHeaderLine(rest)
		if first == "" || lost(first, ended) {
			return contentType, encoding
		}
		colon := strings.IndexByte(first, ':')
		if colon < 0 {
			return contentType, encoding
		}
		line := trimHeaderSpace(first)
		colon = strings.IndexByte(line, ':')
		if !readableFieldName(line[:colon]) || !readableFieldValue(line[colon+1:]) {
			return contentType, encoding
		}
		field := after
		folded := false
		for ended {
			spaces := 0
			for spaces < len(field) && (field[spaces] == ' ' || field[spaces] == '\t') {
				spaces++
			}
			if spaces == 0 {
				break
			}
			if spaces == len(field) {
				// A continuation that never comes loses the field it continues.
				return contentType, encoding
			}
			var next string
			next, field, ended = cutHeaderLine(field[spaces:])
			if lost(next, ended) || !readableFieldValue(trimHeaderSpace(next)) {
				return contentType, encoding
			}
			folded = true
		}
		name := line[:colon]
		isType, isEncoding := strings.EqualFold(name, "Content-Type"), strings.EqualFold(name, "Content-Transfer-Encoding")
		if isType && !haveType || isEncoding && !haveEncoding {
			value := line[colon+1:]
			if folded {
				value = unfoldedField(value, after[:len(after)-len(field)])
			}
			value = strings.TrimLeft(value, " \t")
			if isType {
				contentType, haveType = value, true
			} else {
				encoding, haveEncoding = value, true
			}
		}
		rest = field
	}
	return contentType, encoding
}

// unfoldedField joins a value's first line and its continuation lines as ReadMIMEHeader
// does: each one trimmed, with one space before it.
func unfoldedField(value, lines string) string {
	var b strings.Builder
	b.Grow(len(value) + len(lines))
	b.WriteString(value)
	for lines != "" {
		var line string
		line, lines, _ = cutHeaderLine(strings.TrimLeft(lines, " \t"))
		b.WriteByte(' ')
		b.WriteString(trimHeaderSpace(line))
	}
	return b.String()
}

// cutHeaderLine splits off one line as bufio.Reader.ReadLine reads it: without its LF, or
// its CRLF. ended says it had one.
func cutHeaderLine(s string) (line, rest string, ended bool) {
	i := strings.IndexByte(s, '\n')
	if i < 0 {
		return s, "", false
	}
	line, rest = s[:i], s[i+1:]
	line = strings.TrimSuffix(line, "\r")
	return line, rest, true
}

func trimHeaderSpace(s string) string { return strings.Trim(s, " \t") }

// readableFieldName is what ReadMIMEHeader takes as a field name: not empty, and
// token characters or a space, which it allows before the colon.
func readableFieldName(name string) bool {
	for i := 0; i < len(name); i++ {
		if c := name[i]; c != ' ' && !isHeaderNameByte(c) {
			return false
		}
	}
	return name != ""
}

func isHeaderNameByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// readableFieldValue is what ReadMIMEHeader takes as a value: no control octet but a tab.
func readableFieldValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if c := value[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// manyParameters is how many parameters a field holds before reading it reads only the
// ones it needs. Mail with fewer goes the way it always did.
const manyParameters = 16

// parseKeptParameters is parseContentType when only the type, whether it can be read, and
// its boundary and charset matter.
func parseKeptParameters(value string) (string, map[string]string, error) {
	if strings.Count(value, ";") <= manyParameters {
		return parseContentType(value)
	}
	if has8bit(value) {
		return parseUncommented(readableKept(value))
	}
	uncommented, err := uncommentMIME(value)
	if err != nil {
		return "", nil, err
	}
	kept, err := keptParameters(uncommented)
	if err != nil {
		return "", nil, err
	}
	return parseUncommented(kept)
}

// isKeptName reports whether a parameter name, in any of its RFC 2231 forms, is one a
// conversion reads.
func isKeptName(name string) bool {
	base, _, _ := strings.Cut(name, "*")
	return strings.EqualFold(base, "boundary") || strings.EqualFold(base, "charset")
}

// readableKept is readableField without the parameters isKeptName leaves out. What
// readableField writes always parses, so leaving them out can't change anything else.
func readableKept(value string) string {
	var b strings.Builder
	first := true
	seen := map[string]bool{}
	eachFieldPiece(value, func(piece fieldPiece) {
		if first {
			b.WriteString(piece.key)
			first = false
			return
		}
		if piece.key == "" {
			return
		}
		name := parameterName(piece.key)
		if !isKeptName(name) {
			return
		}
		if seen[strings.ToLower(name)] {
			return
		}
		seen[strings.ToLower(name)] = true
		writeReadableParameter(&b, name, piece)
	})
	return b.String()
}

// keptParameters is a field with no comments, less every parameter isKeptName leaves out,
// which parseMediaType reads the same. It takes a parameter one at a time as
// mime.ParseMediaType does (consumeMediaParam), and fails where that fails: on one it can't
// read. The parameters kept are left for parseMediaType to judge, and so is the first name
// repeated with another value, whose two parameters stay: what a repeat means is
// parseMediaType's to say, and one is enough for it to say it, since every name dropped
// here has already been read.
func keptParameters(v string) (string, error) {
	base, _, _ := strings.Cut(v, ";")
	rest := v[len(base):]
	names := takeNameSet(strings.Count(rest, ";"))
	defer names.release()
	var b strings.Builder
	b.WriteString(base)
	keptRepeat := false
	for len(rest) > 0 {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		if rest == "" {
			break
		}
		key, value, next, ok := scanMediaParameter(rest)
		if !ok {
			if strings.TrimSpace(rest) == ";" {
				break
			}
			return "", mime.ErrInvalidMediaParameter
		}
		if isKeptName(key) {
			b.WriteString(rest[:len(rest)-len(next)])
		} else {
			slot, repeated := names.add(key)
			if !repeated {
				slot.at, slot.size = int32(len(v)-len(value)-len(next)), int32(len(value))
			} else if earlier := v[slot.at : slot.at+slot.size]; !keptRepeat && earlier != value && mediaValueText(earlier) != mediaValueText(value) {
				keptRepeat = true
				b.WriteString(";" + key + "=" + earlier)
				b.WriteString(rest[:len(rest)-len(next)])
			}
		}
		rest = next
	}
	return b.String(), nil
}

// scanMediaParameter is mime's consumeMediaParam, handing back a value as it is spelled.
func scanMediaParameter(v string) (key, value, rest string, ok bool) {
	rest = strings.TrimLeftFunc(v, unicode.IsSpace)
	rest, ok = strings.CutPrefix(rest, ";")
	if !ok {
		return "", "", v, false
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	key, rest = cutMediaToken(rest)
	if key == "" {
		return "", "", v, false
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	rest, ok = strings.CutPrefix(rest, "=")
	if !ok {
		return "", "", v, false
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	end := mediaValueEnd(rest)
	if end <= 0 {
		return "", "", v, false
	}
	return key, rest[:end], rest[end:], true
}

// cutMediaToken is mime's consumeToken.
func cutMediaToken(v string) (token, rest string) {
	for i := 0; i < len(v); i++ {
		if c := v[i]; c <= 0x20 || c >= 0x7f || strings.IndexByte(`()<>@,;:\"/[]?=`, c) >= 0 {
			return v[:i], v[i:]
		}
	}
	return v, ""
}

// mediaValueEnd is where the value mime's consumeValue reads ends, or 0 when it reads none.
func mediaValueEnd(v string) int {
	if v == "" {
		return 0
	}
	if v[0] != '"' {
		token, _ := cutMediaToken(v)
		return len(token)
	}
	for i := 1; i < len(v); i++ {
		switch c := v[i]; {
		case c == '"':
			return i + 1
		case c == '\\' && i+1 < len(v) && strings.IndexByte(`()<>@,;:\"/[]?=`, v[i+1]) >= 0:
			i++
		case c == '\r' || c == '\n':
			return 0
		}
	}
	return 0
}

// mediaValueText is the text of a value mediaValueEnd found, as consumeValue reads it.
func mediaValueText(value string) string {
	if value[0] != '"' {
		return value
	}
	return unquoteParameter(value)
}

// nameSet is a set of ASCII parameter names, compared without regard to case. Its memory
// is used again by the next field, because a field of very many parameters would
// otherwise allocate many times its own size for each reading of it.
type nameSet struct {
	slots []nameSlot
	arena []byte
}

// nameSlot is one name in the arena. at and size are its user's to keep.
type nameSlot struct {
	off, length, at, size int32
}

var (
	nameSets = sync.Pool{New: func() any { return new(nameSet) }}
	nameSeed = maphash.MakeSeed()
)

// takeNameSet is an empty set with room for count names without growing.
func takeNameSet(count int) *nameSet {
	size := 16
	for size < 2*count+2 {
		size *= 2
	}
	s := nameSets.Get().(*nameSet)
	if cap(s.slots) < size {
		s.slots = make([]nameSlot, size)
	} else {
		s.slots = s.slots[:size]
		clear(s.slots)
	}
	s.arena = s.arena[:0]
	return s
}

func (s *nameSet) release() { nameSets.Put(s) }

// add finds name, or adds it, and says whether it was there already. The set must have
// been taken with room for it.
func (s *nameSet) add(name string) (*nameSlot, bool) {
	off := len(s.arena)
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		s.arena = append(s.arena, c)
	}
	lowered := s.arena[off:]
	mask := uint64(len(s.slots) - 1)
	for at := maphash.Bytes(nameSeed, lowered) & mask; ; at = (at + 1) & mask {
		slot := &s.slots[at]
		if slot.length == 0 {
			slot.off, slot.length = int32(off)+1, int32(len(lowered))
			return slot, false
		}
		if int(slot.length) == len(lowered) && string(s.arena[slot.off-1:slot.off-1+slot.length]) == string(lowered) {
			s.arena = s.arena[:off]
			return slot, true
		}
	}
}

// has reports whether name is in the set.
func (s *nameSet) has(name string) bool {
	off := len(s.arena)
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		s.arena = append(s.arena, c)
	}
	lowered := s.arena[off:]
	defer func() { s.arena = s.arena[:off] }()
	mask := uint64(len(s.slots) - 1)
	for at := maphash.Bytes(nameSeed, lowered) & mask; ; at = (at + 1) & mask {
		slot := &s.slots[at]
		if slot.length == 0 {
			return false
		}
		if int(slot.length) == len(lowered) && string(s.arena[slot.off-1:slot.off-1+slot.length]) == string(lowered) {
			return true
		}
	}
}
