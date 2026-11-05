package mime

import (
	"cmp"
	"errors"
	"hash/maphash"
	stdmime "mime"
	"slices"
	"strings"
	"unicode"
	"unsafe"
)

// manyParameters is how many semicolons a field holds before it's read one parameter at a time.
// Mail with fewer goes straight to mime.ParseMediaType.
const manyParameters = 16

// parseContentType is parseMedia for a Content-Type, which is text/plain when it's empty.
func parseContentType(value string) (mediaType string, params map[string]string, failed bool) {
	if value == "" {
		return "text/plain", nil, false
	}
	return parseMedia(value)
}

// parseMedia reads a Content-Type or Content-Disposition value as go-message does: the type and its
// parameters, with encoded-words decoded, or when that fails, the value as its type and failed set.
// It keeps only the parameters this package looks at. value may be a view of a buffer, and nothing
// it answers is.
func parseMedia(value string) (mediaType string, params map[string]string, failed bool) {
	var kept string
	var continued map[string]string
	if strings.Count(value, ";") > manyParameters {
		var ok bool
		if kept, continued, ok = keptMediaParams(value); !ok {
			return strings.Clone(value), nil, true
		}
	} else {
		kept = strings.Clone(value)
	}
	mediaType, all, err := stdmime.ParseMediaType(kept)
	if err != nil {
		return strings.Clone(value), nil, true
	}
	for name, parameter := range all {
		if keptMediaName(name) {
			if params == nil {
				params = make(map[string]string, 2)
			}
			params[name] = decodeWords(parameter)
		}
	}
	for name, parameter := range continued {
		if params == nil {
			params = make(map[string]string, 2)
		}
		params[name] = decodeWords(parameter)
	}
	return mediaType, params, false
}

// keptMediaName says whether a parameter name, in any of its RFC 2231 forms, is one this package
// reads.
func keptMediaName(name string) bool {
	base, _, _ := strings.Cut(name, "*")
	return strings.EqualFold(base, "boundary") || strings.EqualFold(base, "charset") ||
		strings.EqualFold(base, "name") || strings.EqualFold(base, "filename")
}

// keptMediaParams is a value less every parameter keptMediaName leaves out, which
// mime.ParseMediaType reads the same, or false when it would fail. It takes a parameter at a time as
// that does (consumeMediaParam), and fails where that does: on one it can't read, or on a name
// repeated with another value. The RFC 2231 forms of what it keeps are read here rather than left in
// value, because mime.ParseMediaType holds several maps and strings for each one, and continued says
// what they come to.
func keptMediaParams(value string) (kept string, continued map[string]string, ok bool) {
	base, _, _ := strings.Cut(value, ";")
	var b strings.Builder
	b.WriteString(base)
	seed := maphash.MakeSeed()
	stars := strings.Count(value, "*")
	names := make([]uint64, 0, max(strings.Count(value, ";")-stars, 0))
	var pieces []mediaPiece
	eachParam := func(visit func(key, spelled, text string)) bool {
		for rest := value[len(base):]; len(rest) > 0; {
			rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
			if rest == "" {
				break
			}
			key, spelled, next, ok := scanMediaParam(rest)
			if !ok {
				return strings.TrimSpace(rest) == ";"
			}
			visit(key, rest[:len(rest)-len(next)], spelled)
			rest = next
		}
		return true
	}
	offset := func(of string) uint32 {
		return uint32(uintptr(unsafe.Pointer(unsafe.StringData(of))) - uintptr(unsafe.Pointer(unsafe.StringData(value))))
	}
	ok = eachParam(func(key, spelled, text string) {
		if keptMediaName(key) {
			if piece, isPiece := newMediaPiece(key); isPiece {
				if pieces == nil {
					pieces = make([]mediaPiece, 0, stars)
				}
				piece.at = offset(text)
				pieces = append(pieces, piece)
				return
			}
			if !strings.Contains(key, "*") {
				b.WriteString(spelled)
				return
			}
		}
		names = append(names, lowerHash(seed, key))
	})
	if !ok {
		return "", nil, false
	}
	slices.Sort(names)
	if repeatedHash(names) {
		// A name came twice, which is only an error when its values differ.
		values := map[uint64]uint64{}
		same := true
		eachParam(func(key, _, text string) {
			if keptMediaName(key) {
				if _, isPiece := newMediaPiece(key); isPiece || !strings.Contains(key, "*") {
					return
				}
			}
			name := lowerHash(seed, key)
			if _, repeated := slices.BinarySearch(names, name); !repeated || !hashRepeats(names, name) {
				return
			}
			hashed := valueHash(seed, text)
			if earlier, seen := values[name]; seen && earlier != hashed {
				same = false
			}
			values[name] = hashed
		})
		if !same {
			return "", nil, false
		}
	}
	continued, ok = stitchMediaPieces(value, pieces, seed)
	if !ok {
		return "", nil, false
	}
	return b.String(), continued, true
}

// mediaFamilies are the parameters this package reads, as a mediaPiece names them.
var mediaFamilies = [...]string{"boundary", "charset", "filename", "name"}

// mediaPiece is one RFC 2231 form of a kept parameter, by where its value starts in the field it came
// from: name*, name*N or name*N*. Anything else with a star in its name means nothing to
// mime.ParseMediaType beyond being one more name. A field is mostly these, so what's held for each is
// as small as can be. Its family, N and whether it's name*N* make one number that sorts as they do.
type mediaPiece struct {
	order uint32
	at    uint32
}

const (
	mediaPieceBits   = 27
	mediaPieceSingle = 1<<mediaPieceBits - 1
)

func makeMediaOrder(family uint8, number int32, encoded bool) uint32 {
	order := uint32(family) << (mediaPieceBits + 1)
	if number < 0 {
		// name*, which comes before everything else of its family.
		return order
	}
	order |= (uint32(number) + 1) << 1
	if encoded {
		order |= 1
	}
	return order
}

func (p mediaPiece) family() uint8 { return uint8(p.order >> (mediaPieceBits + 1)) }

// number is the N, or -1 for name*.
func (p mediaPiece) number() int32 { return int32(p.order>>1&mediaPieceSingle) - 1 }

func (p mediaPiece) encoded() bool { return p.order&1 == 1 }

// spelled is the piece's value as it's spelled.
func (p mediaPiece) spelled(value string) string {
	rest := value[p.at:]
	return rest[:mediaValueEnd(rest)]
}

// newMediaPiece reads a kept parameter's name, whose number has to be spelled as mime.ParseMediaType
// looks for it.
func newMediaPiece(key string) (mediaPiece, bool) {
	base, suffix, starred := strings.Cut(key, "*")
	if !starred {
		return mediaPiece{}, false
	}
	var family uint8
	for i, name := range mediaFamilies {
		if strings.EqualFold(base, name) {
			family = uint8(i)
		}
	}
	if suffix == "" {
		return mediaPiece{order: makeMediaOrder(family, -1, false)}, true
	}
	digits, encoded := strings.CutSuffix(suffix, "*")
	if digits == "" || len(digits) > 8 || (digits[0] == '0' && len(digits) > 1) {
		return mediaPiece{}, false
	}
	number := int32(0)
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return mediaPiece{}, false
		}
		number = number*10 + int32(digits[i]-'0')
	}
	return mediaPiece{order: makeMediaOrder(family, number, encoded)}, true
}

// stitchMediaPieces puts each family back together as mime.ParseMediaType does, or answers false for
// a piece repeated with another value, where that fails.
func stitchMediaPieces(value string, pieces []mediaPiece, seed maphash.Seed) (map[string]string, bool) {
	if len(pieces) == 0 {
		return nil, true
	}
	slices.SortFunc(pieces, func(a, b mediaPiece) int { return cmp.Compare(a.order, b.order) })
	for i := 1; i < len(pieces); i++ {
		if pieces[i-1].order == pieces[i].order && valueHash(seed, pieces[i-1].spelled(value)) != valueHash(seed, pieces[i].spelled(value)) {
			return nil, false
		}
	}
	stitched := make(map[string]string, 2)
	for from := 0; from < len(pieces); {
		to := from + 1
		for to < len(pieces) && pieces[to].family() == pieces[from].family() {
			to++
		}
		family := pieces[from:to]
		from = to
		name := mediaFamilies[family[0].family()]
		if family[0].number() < 0 {
			if decoded, ok := decodeExtended(mediaText(value, family[0])); ok {
				stitched[name] = decoded
			}
			continue
		}
		size := 0
		for _, piece := range family {
			size += len(piece.spelled(value))
		}
		var b strings.Builder
		b.Grow(size)
		next, valid := int32(0), false
		for _, piece := range family {
			number := piece.number()
			if number > next {
				break
			}
			if number < next {
				continue
			}
			switch {
			case !piece.encoded():
				writeMediaText(&b, piece.spelled(value))
			case next == 0:
				if decoded, ok := decodeExtended(mediaText(value, piece)); ok {
					b.WriteString(decoded)
				}
			default:
				decoded, _ := percentUnescape(mediaText(value, piece))
				b.WriteString(decoded)
			}
			valid = true
			next++
		}
		if valid {
			stitched[name] = b.String()
		}
	}
	return stitched, true
}

func mediaText(value string, piece mediaPiece) string {
	spelled := piece.spelled(value)
	if spelled == "" || spelled[0] != '"' {
		return spelled
	}
	var b strings.Builder
	b.Grow(len(spelled))
	writeMediaText(&b, spelled)
	return b.String()
}

// writeMediaText writes a value as mime's consumeValue reads it, unquoted.
func writeMediaText(b *strings.Builder, spelled string) {
	if spelled == "" || spelled[0] != '"' {
		b.WriteString(spelled)
		return
	}
	for i := 1; i < len(spelled)-1; i++ {
		if spelled[i] == '\\' && i+1 < len(spelled) && strings.IndexByte(mediaSpecials, spelled[i+1]) >= 0 {
			i++
		}
		b.WriteByte(spelled[i])
	}
}

// decodeExtended is mime's decode2231Enc.
func decodeExtended(v string) (string, bool) {
	parts := strings.SplitN(v, "'", 3)
	if len(parts) != 3 {
		return "", false
	}
	charset := strings.ToLower(parts[0])
	if charset != "us-ascii" && charset != "utf-8" {
		return "", false
	}
	decoded, err := percentUnescape(parts[2])
	if err != nil {
		return "", false
	}
	return decoded, true
}

// percentUnescape is mime's percentHexUnescape.
func percentUnescape(s string) (string, error) {
	if strings.IndexByte(s, '%') < 0 {
		return s, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) || !isHexDigit(s[i+1]) || !isHexDigit(s[i+2]) {
			return "", errBadPercent
		}
		b.WriteByte(hexValue(s[i+1])<<4 | hexValue(s[i+2]))
		i += 2
	}
	return b.String(), nil
}

var errBadPercent = errors.New("mime: bogus characters after %")

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexValue(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

func repeatedHash(sorted []uint64) bool {
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1] {
			return true
		}
	}
	return false
}

func hashRepeats(sorted []uint64, name uint64) bool {
	at, _ := slices.BinarySearch(sorted, name)
	return at+1 < len(sorted) && sorted[at+1] == name
}

// lowerHash hashes a parameter name without regard to case.
func lowerHash(seed maphash.Seed, name string) uint64 {
	var h maphash.Hash
	h.SetSeed(seed)
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		_ = h.WriteByte(c)
	}
	return h.Sum64()
}

// valueHash hashes a value as mime's consumeValue reads it, unquoted.
func valueHash(seed maphash.Seed, spelled string) uint64 {
	if spelled == "" || spelled[0] != '"' {
		return maphash.String(seed, spelled)
	}
	var h maphash.Hash
	h.SetSeed(seed)
	for i := 1; i < len(spelled)-1; i++ {
		if spelled[i] == '\\' && i+1 < len(spelled) && strings.IndexByte(mediaSpecials, spelled[i+1]) >= 0 {
			i++
		}
		_ = h.WriteByte(spelled[i])
	}
	return h.Sum64()
}

const mediaSpecials = `()<>@,;:\"/[]?=`

// scanMediaParam is mime's consumeMediaParam, handing back a value as it is spelled.
func scanMediaParam(v string) (key, value, rest string, ok bool) {
	rest = strings.TrimLeftFunc(v, unicode.IsSpace)
	if rest, ok = strings.CutPrefix(rest, ";"); !ok {
		return "", "", v, false
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	key, rest = cutMediaToken(rest)
	if key == "" {
		return "", "", v, false
	}
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	if rest, ok = strings.CutPrefix(rest, "="); !ok {
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
		if c := v[i]; c <= 0x20 || c >= 0x7f || strings.IndexByte(mediaSpecials, c) >= 0 {
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
		case c == '\\' && i+1 < len(v) && strings.IndexByte(mediaSpecials, v[i+1]) >= 0:
			i++
		case c == '\r' || c == '\n':
			return 0
		}
	}
	return 0
}
