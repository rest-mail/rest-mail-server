package message

import (
	"bytes"
	"cmp"
	"hash/maphash"
	"mime"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unsafe"
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
		contentType, encoding = readContentFields(n.header, true)
	}
	if !knownTransferEncoding(encoding) {
		return wireType{mt: "application/octet-stream"}
	}
	var t wireType
	usable := false
	if contentType != "" {
		mt, params, err := readWireField(contentType)
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
	return readContentFields(header, false)
}

// readContentFields is contentFields. With keepFolded, a Content-Type stays as it's spelled, line
// breaks and all, for readWireField.
func readContentFields(header string, keepFolded bool) (contentType, encoding string) {
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
					// The value and its lines lie side by side in the block.
					spelled := rest[strings.IndexByte(rest, ':')+1 : len(rest)-len(field)]
					if keepFolded && isType {
						value = strings.TrimRight(spelled, " \t\r\n")
					} else {
						value = unfoldedField(value, continuation)
					}
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

// readsFolded says parseKept reads a value just the same with its line breaks in it, so nothing need
// be built to take them out. It holds many parameters, and with nothing quoted or commented a line
// break can only stand where white space is read as nothing. mime's reader takes CR and LF as space.
func readsFolded(spelled string) bool {
	return strings.Count(spelled, ";") > manyParameters && !strings.ContainsAny(spelled, `"()`) && !has8bit(spelled)
}

// readWireField is parseKeptParameters of a Content-Type as readContentFields spells it. Most of what
// a field holds doesn't change by its line breaks: see readsFolded for a 7-bit one, and an 8-bit one
// reads a piece at a time, where one matters only inside a piece it keeps. So it's only built again
// without them when it has to be, as entityHeader unfolds it, and then what's answered is copied out.
func readWireField(value string) (string, map[string]string, error) {
	if strings.IndexByte(value, '\n') < 0 || readsFolded(value) {
		return parseKeptParameters(value)
	}
	if has8bit(value) && strings.Count(value, ";") > manyParameters {
		if mt, params, err, read := readKeptSpelled(value, conversionFamilies, true); read {
			return mt, params, err
		}
	}
	scratch := TakeFieldScratch(0)
	defer scratch.Release()
	first, lines, _ := cutHeaderLine(value)
	mt, params, err := parseKeptParameters(strings.TrimLeft(scratch.unfoldedField(trimHeaderSpace(first), lines), " \t"))
	for name, text := range params {
		if scratch.holds(text) {
			params[name] = strings.Clone(text)
		}
	}
	if scratch.holds(mt) {
		mt = strings.Clone(mt)
	}
	return mt, params, err
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
	writeUnfoldedField(&b, value, lines)
	return b.String()
}

func (s *FieldScratch) unfoldedField(value, lines string) string {
	s.room(len(value) + len(lines))
	writeUnfoldedField(s, value, lines)
	return s.String()
}

func writeUnfoldedField(w interface{ WriteString(string) (int, error) }, value, lines string) {
	_, _ = w.WriteString(value)
	for lines != "" {
		var line string
		line, lines, _ = cutHeaderLine(strings.TrimLeft(lines, " \t"))
		_, _ = w.WriteString(" ")
		_, _ = w.WriteString(trimHeaderSpace(line))
	}
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
	return readKept(value, conversionFamilies)
}

// conversionFamilies are the parameters a conversion reads, and hostFamilies the ones a host reads
// of a part as well, to say what it's called.
var (
	conversionFamilies = []string{"boundary", "charset"}
	hostFamilies       = []string{"boundary", "charset", "filename", "name"}
)

// readKept is parseContentType of any field, keeping only families and holding nothing for the rest.
func readKept(value string, families []string) (string, map[string]string, error) {
	if has8bit(value) {
		mt, params, err, _ := readKeptSpelled(value, families, false)
		return mt, params, err
	}
	if kept, ok := uncommentedKept(value, families); ok {
		return parseKept(kept, families)
	}
	uncommented, err := uncommentMIME(value)
	if err != nil {
		return "", nil, err
	}
	return parseKept(uncommented, families)
}

// readKeptSpelled is readKept of a field holding an 8-bit octet. With noBreaks, read is false and
// nothing else is answered when a piece that's kept holds a line break.
func readKeptSpelled(value string, families []string, noBreaks bool) (mt string, params map[string]string, err error, read bool) {
	scratch := TakeFieldScratch(0)
	defer scratch.Release()
	kept, read := readableKept(scratch, value, families, noBreaks)
	if !read {
		return "", nil, nil, false
	}
	mt, params, err = parseKept(kept, families)
	// One value on its own is a view of what's kept, and that memory goes back here.
	for name, text := range params {
		if scratch.holds(text) {
			params[name] = strings.Clone(text)
		}
	}
	return mt, params, err, true
}

// uncommentedKept is uncommentMIME of a 7-bit field holding comments, less each parameter that reads
// and isn't one of families, so a field of many costs only what's kept of it. ok is false when
// there's nothing to save, or anything about the field is for parseKept to decide: a parameter that
// doesn't read, a blank, or a comment or quoted-string that doesn't close.
func uncommentedKept(value string, families []string) (string, bool) {
	if strings.IndexByte(value, '(') < 0 && strings.IndexByte(value, ')') < 0 {
		return "", false
	}
	var scratch bytes.Buffer
	var b strings.Builder
	// Twice: once to size it, and once to write it.
	for size, writing := 0, false; ; writing = true {
		rest, more := value, true
		for first := true; more; first = false {
			var segment string
			var closed bool
			segment, rest, more, closed = cutFieldSegment(rest)
			if !closed {
				return "", false
			}
			text := segment
			if strings.IndexByte(segment, '(') >= 0 || strings.IndexByte(segment, ')') >= 0 {
				scratch.Reset()
				if !uncommentInto(&scratch, segment) {
					return "", false
				}
				// Good only until the next segment: what's kept of it is copied to b below.
				text = unsafe.String(unsafe.SliceData(scratch.Bytes()), scratch.Len())
			}
			if !first {
				if strings.TrimSpace(text) == "" {
					return "", false
				}
				key, after, read := scanMediaParameter(text)
				if !read || strings.TrimSpace(after) != "" {
					return "", false
				}
				if _, kept := keptFamily(families, key); !kept {
					continue
				}
			}
			size += len(text) + 1
			if writing {
				if !first {
					b.WriteByte(';')
				}
				b.WriteString(text)
			}
		}
		if writing {
			return b.String(), true
		}
		b.Grow(size)
	}
}

// ReadContentType reads a Content-Type as BODYSTRUCTURE does, keeping hostFamilies. usable says it's
// the entity's type. When it isn't, RFC 2045 §5.2's default stands. Its answers may be views of value.
func ReadContentType(value string) (mediaType string, params map[string]string, usable bool) {
	mt, params, err := readKept(value, hostFamilies)
	return mt, params, usableContentType(mt, params, err)
}

// ReadDisposition reads a Content-Disposition as BODYSTRUCTURE does, keeping hostFamilies. usable is
// false where dispositionField answers NIL. Its answers may be views of value.
func ReadDisposition(value string) (disposition string, params map[string]string, usable bool) {
	disposition, params, err := readKept(value, hostFamilies)
	return disposition, params, err == nil && disposition != "" && !strings.Contains(disposition, "/")
}

// keptFamily is which of families a parameter name is a form of, in any of its RFC 2231 forms. A '*'
// in any other form is part of another name (see parseMediaType).
func keptFamily(families []string, name string) (string, bool) {
	base, suffix, _ := strings.Cut(name, "*")
	if !validExtendedSuffix(suffix) {
		return "", false
	}
	family := pieceFamily(families, base)
	return family, family != ""
}

// readableKept is readableField keeping only families, with their repeats, which parseKept reads as
// parseMediaType does. What readableField writes always parses, so leaving the others out can't change
// anything else. What it answers is in scratch.
func readableKept(scratch *FieldScratch, value string, families []string, noBreaks bool) (string, bool) {
	closers := commentClosers(value)
	// A long field has one buffer, of what's kept of it, which is usually far less than the field.
	size := 0
	first := true
	breaks := false
	eachFieldPiece(value, closers, func(piece fieldPiece) {
		switch {
		case first:
			first = false
			size += len(piece.key)
			breaks = breaks || pieceHasBreak(piece)
		case piece.key != "":
			name := parameterName(piece.key)
			if _, kept := keptFamily(families, name); kept {
				size += len(name) + len(piece.value) + len("; =\"\"")
				breaks = breaks || pieceHasBreak(piece)
			}
		}
	})
	if noBreaks && breaks {
		return "", false
	}
	scratch.room(size)
	first = true
	eachFieldPiece(value, closers, func(piece fieldPiece) {
		if first {
			_, _ = scratch.WriteString(piece.key)
			first = false
			return
		}
		if piece.key == "" {
			return
		}
		name := parameterName(piece.key)
		if _, kept := keptFamily(families, name); kept {
			writeReadableParameter(scratch, name, piece)
		}
	})
	return scratch.String(), true
}

// pieceHasBreak says a line break stands inside what a piece reads as, not only around it.
func pieceHasBreak(piece fieldPiece) bool {
	// A piece's key and value are parts of raw.
	return strings.IndexByte(piece.raw, '\n') >= 0 || strings.IndexByte(piece.broken, '\n') >= 0
}

// parseKept is parseUncommented of a field with no comments, when only its type, whether it can be
// read, and families matter (TestParseKeptMatchesParseUncommented). mime.ParseMediaType
// holds several maps and strings for each parameter, so this reads them one at a time, and hands it
// only the type and the plain kept ones.
//
// It reads a repeated name as parseMediaType does, with withoutRepeats: the first of a name stands,
// and a later one has to read but is otherwise gone. So every segment has to read, wherever it stands
// and whatever it's named, and nothing is held for a name that's left out.
func parseKept(kept string, families []string) (string, map[string]string, error) {
	base, _, _ := strings.Cut(kept, ";")
	var plain strings.Builder
	plain.WriteString(base)
	var plainSeen uint8 // a bit for each of families
	var held *keptPieces
	var pieces []keptPiece
	_, rest, more, closed := cutFieldSegment(kept)
	for closed && more {
		at := len(kept) - len(rest)
		var segment string
		segment, rest, more, closed = cutFieldSegment(rest)
		if !closed {
			break
		}
		if strings.TrimSpace(segment) == "" {
			if more && !onlyRepeatsFollow(kept[:at-1], rest) {
				return "", nil, mime.ErrInvalidMediaParameter
			}
			break
		}
		key, after, ok := scanMediaParameter(segment)
		if !ok || strings.TrimSpace(after) != "" {
			return "", nil, mime.ErrInvalidMediaParameter
		}
		family, isKept := keptFamily(families, key)
		switch {
		case !isKept:
		case strings.IndexByte(key, '*') < 0:
			if bit := uint8(1) << slices.Index(families, family); plainSeen&bit == 0 {
				plainSeen |= bit
				plain.WriteByte(';')
				plain.WriteString(segment)
			}
		default:
			if pieces == nil {
				held = takeKeptPieces(strings.Count(rest, "*") + 1)
				defer held.release()
				pieces = held.pieces
			}
			spaces := len(segment) - len(strings.TrimLeftFunc(segment, unicode.IsSpace))
			pieces = append(pieces, keptPiece{keyAt: uint32(at + spaces)})
		}
	}
	if !closed {
		return "", nil, mime.ErrInvalidMediaParameter
	}
	mt, params, err := parseUncommented(plain.String())
	if err != nil {
		return mt, params, err
	}
	family := func(piece keptPiece) string { return pieceFamily(families, piece.key(kept)) }
	suffix := func(piece keptPiece) string {
		key := piece.key(kept)
		return key[strings.IndexByte(key, '*')+1:]
	}
	text := func(piece keptPiece) string { return mediaValueText(piece.value(kept)) }
	// Only digits and a '*' are left of a name to compare, so this is the first of each name.
	slices.SortFunc(pieces, func(a, b keptPiece) int {
		return cmp.Or(cmp.Compare(family(a), family(b)), cmp.Compare(suffix(a), suffix(b)), cmp.Compare(a.keyAt, b.keyAt))
	})
	pieces = slices.CompactFunc(pieces, func(a, b keptPiece) bool {
		return family(a) == family(b) && suffix(a) == suffix(b)
	})
	for from := 0; from < len(pieces); {
		to := from + 1
		name := family(pieces[from])
		size := len(pieces[from].value(kept))
		for to < len(pieces) && family(pieces[to]) == name {
			size += len(pieces[to].value(kept))
			to++
		}
		value, first, whole := joinSegments(name, pieces[from:to], size, suffix, text)
		setExtended(params, mt, name, value, first, whole)
		from = to
	}
	return mt, params, nil
}

// onlyRepeatsFollow says what follows a blank segment is all parameters that read, each named before
// it, so withoutRepeats leaves that blank last, where mime.ParseMediaType reads it as the ';' that
// ends a field. Anywhere else it reads as nothing, and a parameter can't follow it. A blank before
// more of a field is rare, so only this holds the names, for the one field that has to ask.
func onlyRepeatsFollow(before, after string) bool {
	names := takeNameSet(strings.Count(before, ";") + 1)
	defer names.release()
	_, rest, more, _ := cutFieldSegment(before)
	for more {
		var segment string
		segment, rest, more, _ = cutFieldSegment(rest)
		key, _, _ := scanMediaParameter(segment)
		names.add(key)
	}
	for more = true; more; {
		var segment string
		var closed bool
		segment, after, more, closed = cutFieldSegment(after)
		if !closed {
			return false
		}
		key, tail, ok := scanMediaParameter(segment)
		if !ok || strings.TrimSpace(tail) != "" || !names.has(key) {
			return false
		}
	}
	return true
}

// keptPieces is memory for the pieces of one reading, used again by the next, as a field of many of
// them holds them only until it's read, and it's read more than once.
type keptPieces struct{ pieces []keptPiece }

var keptPieceLists = sync.Pool{New: func() any { return new(keptPieces) }}

func takeKeptPieces(count int) *keptPieces {
	list := keptPieceLists.Get().(*keptPieces)
	if cap(list.pieces) < count {
		list.pieces = make([]keptPiece, 0, count)
	}
	list.pieces = list.pieces[:0]
	return list
}

func (list *keptPieces) release() { keptPieceLists.Put(list) }

// keptPiece is one RFC 2231 form of a kept parameter, by where its name starts in its field. A field
// is mostly these, so what's held for each is as small as can be, and the rest is read again when
// it's asked for.
type keptPiece struct{ keyAt uint32 }

func (piece keptPiece) key(kept string) string {
	key, _ := cutMediaToken(kept[piece.keyAt:])
	return key
}

// value is the piece's value as it's spelled. Its field has read as parameters already.
func (piece keptPiece) value(kept string) string {
	_, rest := cutMediaToken(kept[piece.keyAt:])
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	rest = strings.TrimLeftFunc(rest[1:], unicode.IsSpace)
	return rest[:mediaValueEnd(rest)]
}

// pieceFamily is which of families a name or its base is a form of, or "" when it's none of them.
func pieceFamily(families []string, key string) string {
	base, _, _ := strings.Cut(key, "*")
	for _, family := range families {
		if strings.EqualFold(base, family) {
			return family
		}
	}
	return ""
}

// scanMediaParameter is mime's consumeMediaParam of what follows a ';'.
func scanMediaParameter(v string) (key, rest string, ok bool) {
	rest = strings.TrimLeftFunc(v, unicode.IsSpace)
	key, rest = cutMediaToken(rest)
	if key == "" {
		return "", v, false
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	rest, ok = strings.CutPrefix(rest, "=")
	if !ok {
		return "", v, false
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	end := mediaValueEnd(rest)
	if end <= 0 {
		return "", v, false
	}
	return key, rest[end:], true
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
	slots  []nameSlot
	arena  []byte
	filled int
	// source is a field its names are mostly views of. A name that is one stays there, spelled as it
	// was, and holds nothing in the arena.
	source string
}

// nameSlot is one name: off is one past where it starts in the arena, or that as a negative number
// when it's in source.
type nameSlot struct {
	off, length int32
}

// text is the slot's name as it's held. It's in lower case only if it's in the arena.
func (s *nameSet) text(slot nameSlot) []byte {
	if slot.off < 0 {
		name := s.source[-slot.off-1 : -slot.off-1+slot.length]
		return unsafe.Slice(unsafe.StringData(name), len(name))
	}
	return s.arena[slot.off-1 : slot.off-1+slot.length]
}

// matches says the slot holds lowered, which is a name in lower case.
func (s *nameSet) matches(slot nameSlot, lowered []byte) bool {
	if int(slot.length) != len(lowered) {
		return false
	}
	held := s.text(slot)
	if slot.off > 0 {
		return string(held) == string(lowered)
	}
	for i, c := range held {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lowered[i] {
			return false
		}
	}
	return true
}

var (
	nameSets = sync.Pool{New: func() any { return new(nameSet) }}
	nameSeed = maphash.MakeSeed()
)

// takeNameSet is an empty set with room for count names without growing.
func takeNameSet(count int) *nameSet {
	size := 16
	for 3*size < 4*count+8 {
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
	s.filled = 0
	s.source = ""
	return s
}

func (s *nameSet) release() {
	s.source = ""
	nameSets.Put(s)
}

// add finds name, or adds it, and says whether it was there already. A set taken with room for its
// names never grows. Its names double the arena when they fill it, as appending to it would grow it by
// a quarter at a time, which allocates five times what it ends up holding.
func (s *nameSet) add(name string) bool {
	off := len(s.arena)
	if need := off + len(name); need > cap(s.arena) {
		grown := make([]byte, off, max(2*cap(s.arena), need, 256))
		copy(grown, s.arena)
		s.arena = grown
	}
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
			if at, in := viewOffset(s.source, name); in {
				slot.off = -int32(at) - 1
				s.arena = s.arena[:off]
			}
			if s.filled++; 4*s.filled >= 3*len(s.slots) {
				s.grow()
			}
			return false
		}
		if s.matches(*slot, lowered) {
			s.arena = s.arena[:off]
			return true
		}
	}
}

// grow doubles the table, with the names it holds, which are all in the arena.
func (s *nameSet) grow() {
	slots := make([]nameSlot, 2*len(s.slots))
	mask := uint64(len(slots) - 1)
	for _, old := range s.slots {
		if old.length == 0 {
			continue
		}
		at := maphash.Bytes(nameSeed, s.loweredText(old)) & mask
		for slots[at].length != 0 {
			at = (at + 1) & mask
		}
		slots[at] = old
	}
	s.slots = slots
}

// loweredText is the slot's name in lower case, past the end of the arena when it isn't in it.
func (s *nameSet) loweredText(slot nameSlot) []byte {
	if slot.off > 0 {
		return s.text(slot)
	}
	off := len(s.arena)
	for _, c := range s.text(slot) {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		s.arena = append(s.arena, c)
	}
	lowered := s.arena[off:]
	s.arena = s.arena[:off]
	return lowered
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
		if s.matches(*slot, lowered) {
			return true
		}
	}
}
