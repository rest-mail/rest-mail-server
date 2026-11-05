package message

import (
	"cmp"
	"hash/maphash"
	"mime"
	"slices"
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
// entityHeader reads from a header block, with what it would hold of every other field
// left unbuilt. It stops at a blank line or a line the reader loses, and keeps what it read
// before that, but a line with no colon anywhere makes the whole block read as empty, so
// the block is read to its end whatever has been found (TestContentFieldsMatchTheLibrary).
func contentFields(header string) (contentType, encoding string) {
	if header != "" && (header[0] == ' ' || header[0] == '\t') {
		return "", ""
	}
	rest := header
	haveType, haveEncoding := false, false
	for rest != "" {
		first, after, ended := cutHeaderLine(rest)
		if first == "" || (!ended && lostHeaderLine(first)) {
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
			if !ended && lostHeaderLine(next) {
				return contentType, encoding
			}
			folded = true
		}
		continuation := after[:len(after)-len(field)]
		line := trimHeaderSpace(first)
		name, value, named := strings.Cut(line, ":")
		switch {
		case !named && !strings.Contains(continuation, ":"):
			return "", ""
		case named:
			// A name folded over lines holds a space, so it never names these two.
			isType := len(name) == len("Content-Type") && strings.EqualFold(name, "Content-Type")
			isEncoding := len(name) == len("Content-Transfer-Encoding") && strings.EqualFold(name, "Content-Transfer-Encoding")
			if isType && !haveType || isEncoding && !haveEncoding {
				if folded {
					value = unfoldedField(value, continuation)
				}
				value = strings.TrimLeft(value, " \t")
				if isType {
					contentType, haveType = value, true
				} else {
					encoding, haveEncoding = value, true
				}
			}
		}
		rest = field
	}
	return contentType, encoding
}

// entityHeaderBuffer is the size of the buffers entityHeader reads with.
const entityHeaderBuffer = 4096

// lostHeaderLine reports whether the reader loses a line that ends its input with no line
// break. It takes a line a full buffer at a time, leaving a CR at a buffer's end for the
// next, and when the line ends exactly there the next read finds only the end of input.
func lostHeaderLine(line string) bool {
	for len(line) >= entityHeaderBuffer {
		n := entityHeaderBuffer
		if line[n-1] == '\r' {
			n--
		}
		line = line[n:]
	}
	return line == ""
}

// unfoldedField joins a value's first line and its continuation lines as entityHeader's
// reader does: each one trimmed, with one space before it.
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
		return parseKept(readableKept(value))
	}
	uncommented, err := uncommentMIME(value)
	if err != nil {
		return "", nil, err
	}
	return parseKept(uncommented)
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

// lowerNameHash hashes a parameter name without regard to case.
func lowerNameHash(name string) uint64 {
	var h maphash.Hash
	h.SetSeed(nameSeed)
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		_ = h.WriteByte(c)
	}
	return h.Sum64()
}

// firstRepeat is the first left-out name repeated with another value, as the two parameters that
// say so. names holds the hash of every such name, sorted.
func firstRepeat(parameters string, names []uint64) string {
	repeats := false
	for i := 1; i < len(names) && !repeats; i++ {
		repeats = names[i] == names[i-1]
	}
	if !repeats {
		return ""
	}
	earlier := map[uint64]string{}
	for rest := parameters; len(rest) > 0; {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		if rest == "" {
			break
		}
		key, value, next, ok := scanMediaParameter(rest)
		if !ok {
			break
		}
		if !isKeptName(key) {
			name := lowerNameHash(key)
			at, _ := slices.BinarySearch(names, name)
			if at+1 < len(names) && names[at+1] == name {
				first, seen := earlier[name]
				if seen && first != value && mediaValueText(first) != mediaValueText(value) {
					return ";" + key + "=" + first + rest[:len(rest)-len(next)]
				}
				if !seen {
					earlier[name] = value
				}
			}
		}
		rest = next
	}
	return ""
}

// parseKept is parseUncommented of a field with no comments, when only its type, whether it can be
// read, and its boundary and charset matter. It takes a parameter one at a time as
// mime.ParseMediaType does (consumeMediaParam), and fails where that fails: on one it can't read.
// A parameter left out is only hashed, and an RFC 2231 form of a kept one is only noted, since
// mime.ParseMediaType holds several maps and strings for each (TestParseKeptMatchesParseUncommented).
// The first left-out name repeated with another value stays, with what's plainly kept: what a repeat
// means is parseMediaType's to say, and one is enough for it to say it.
func parseKept(kept string) (string, map[string]string, error) {
	base, _, _ := strings.Cut(kept, ";")
	var plain strings.Builder
	plain.WriteString(base)
	var pieces []keptPiece
	var names []uint64
	offset := func(of string) uint32 { return uint32(len(kept) - len(of)) }
	for rest := kept[len(base):]; len(rest) > 0; {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		if rest == "" {
			break
		}
		key, value, next, ok := scanMediaParameter(rest)
		if !ok {
			if strings.TrimSpace(rest) == ";" {
				break
			}
			return "", nil, mime.ErrInvalidMediaParameter
		}
		switch {
		case !isKeptName(key):
			if names == nil {
				names = make([]uint64, 0, strings.Count(rest, ";"))
			}
			names = append(names, lowerNameHash(key))
		case strings.IndexByte(key, '*') < 0:
			plain.WriteString(rest[:len(rest)-len(next)])
		default:
			if pieces == nil {
				pieces = make([]keptPiece, 0, strings.Count(rest, "*"))
			}
			keyAt := offset(rest[strings.Index(rest, key):])
			pieces = append(pieces, keptPiece{
				keyAt: keyAt, keyEnd: keyAt + uint32(len(key)),
				at: offset(next) - uint32(len(value)), end: offset(next),
				number: pieceNumber(key),
			})
		}
		rest = next
	}
	slices.Sort(names)
	plain.WriteString(firstRepeat(kept[len(base):], names))
	key := func(piece keptPiece) string { return kept[piece.keyAt:piece.keyEnd] }
	family := func(piece keptPiece) string { return pieceFamily(key(piece)) }
	// The family, then its name*N in order with name*N before name*N*, after everything else.
	slices.SortFunc(pieces, func(a, b keptPiece) int {
		return cmp.Or(cmp.Compare(family(a), family(b)), cmp.Compare(a.number, b.number), compareFoldASCII(key(a), key(b)))
	})
	for i := 1; i < len(pieces); i++ {
		a, b := pieces[i-1], pieces[i]
		if compareFoldASCII(key(a), key(b)) == 0 && kept[a.at:a.end] != kept[b.at:b.end] && mediaValueText(kept[a.at:a.end]) != mediaValueText(kept[b.at:b.end]) {
			// A repeat is for parseMediaType to judge, and these two are enough for it to say so.
			return parseUncommented(plain.String() + ";" + key(a) + "=" + kept[a.at:a.end] + ";" + key(b) + "=" + kept[b.at:b.end])
		}
	}
	mt, params, err := parseUncommented(plain.String())
	if err != nil {
		return mt, params, err
	}
	for from := 0; from < len(pieces); {
		to := from + 1
		name := family(pieces[from])
		for to < len(pieces) && family(pieces[to]) == name {
			to++
		}
		members := pieces[from:to]
		from = to
		if value, ok := keptExtendedValue(kept, name, members); ok {
			params[name] = value
			continue
		}
		_, set := params[name]
		if set || (name == "boundary" && strings.HasPrefix(mt, "multipart/")) {
			continue
		}
		if value, ok := keptLaterSegments(kept, name, members); ok {
			params[name] = value
		}
	}
	return mt, params, nil
}

// keptPiece is one RFC 2231 form of a kept parameter, as offsets into its field.
type keptPiece struct {
	keyAt, keyEnd, at, end uint32
	// number is the N of name*N or name*N*, spelled as extendedValue looks for it, or else -1.
	number int32
}

func pieceFamily(key string) string {
	base, _, _ := strings.Cut(key, "*")
	if strings.EqualFold(base, "boundary") {
		return "boundary"
	}
	return "charset"
}

func pieceNumber(key string) int32 {
	_, suffix, _ := strings.Cut(key, "*")
	digits := strings.TrimSuffix(suffix, "*")
	if digits == "" || len(digits) > 9 || (digits[0] == '0' && len(digits) > 1) {
		return -1
	}
	number := int32(0)
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return -1
		}
		number = number*10 + int32(digits[i]-'0')
	}
	return number
}

// compareFoldASCII orders two parameter names as their lower-case spellings.
func compareFoldASCII(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return cmp.Compare(x, y)
		}
	}
	return cmp.Compare(len(a), len(b))
}

// keptExtendedValue is extendedValue, of a family's pieces as parseKept sorts them.
func keptExtendedValue(kept, family string, members []keptPiece) (string, bool) {
	text := func(piece keptPiece) string { return mediaValueText(kept[piece.at:piece.end]) }
	first := 0
	for first < len(members) && members[first].number < 0 {
		piece := members[first]
		if int(piece.keyEnd-piece.keyAt) == len(family)+1 {
			charset, octets := splitExtendedValue(text(piece))
			return keptValue(family, charset, octets), true
		}
		first++
	}
	numbered := members[first:]
	size := 0
	for _, piece := range numbered {
		size += int(piece.end - piece.at)
	}
	var octets strings.Builder
	octets.Grow(size)
	charset := ""
	found := false
	next := int32(0)
	for _, piece := range numbered {
		if piece.number > next {
			break
		}
		if piece.number < next {
			continue
		}
		value := text(piece)
		if kept[piece.keyEnd-1] == '*' {
			if next == 0 {
				charset, value = splitExtendedValue(value)
			} else {
				value = percentDecode(value)
			}
		}
		octets.WriteString(value)
		found = true
		next++
	}
	if !found {
		return "", false
	}
	return keptValue(family, charset, octets.String()), true
}

// keptLaterSegments is laterSegments, of a family's pieces. It reorders them.
func keptLaterSegments(kept, family string, members []keptPiece) (string, bool) {
	number := func(piece keptPiece) string {
		key := kept[piece.keyAt+uint32(len(family))+1 : piece.keyEnd]
		return strings.TrimLeft(strings.TrimSuffix(key, "*"), "0")
	}
	valid := members[:0]
	size := 0
	for _, piece := range members {
		if suffix := kept[piece.keyAt+uint32(len(family))+1 : piece.keyEnd]; suffix != "" && validExtendedSuffix(suffix) {
			valid = append(valid, piece)
			size += int(piece.end - piece.at)
		}
	}
	if len(valid) == 0 {
		return "", false
	}
	slices.SortFunc(valid, func(a, b keptPiece) int {
		x, y := number(a), number(b)
		return cmp.Or(cmp.Compare(len(x), len(y)), cmp.Compare(x, y), compareFoldASCII(kept[a.keyAt:a.keyEnd], kept[b.keyAt:b.keyEnd]))
	})
	var octets strings.Builder
	octets.Grow(size)
	for i, piece := range valid {
		// A name repeated with the same value is one segment.
		if i > 0 && compareFoldASCII(kept[valid[i-1].keyAt:valid[i-1].keyEnd], kept[piece.keyAt:piece.keyEnd]) == 0 {
			continue
		}
		value := mediaValueText(kept[piece.at:piece.end])
		if kept[piece.keyEnd-1] == '*' {
			value = percentDecode(value)
		}
		octets.WriteString(value)
	}
	return keptValue(family, "", octets.String()), true
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

// nameSlot is one name in the arena.
type nameSlot struct {
	off, length int32
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
func (s *nameSet) add(name string) bool {
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
			return false
		}
		if int(slot.length) == len(lowered) && string(s.arena[slot.off-1:slot.off-1+slot.length]) == string(lowered) {
			s.arena = s.arena[:off]
			return true
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
