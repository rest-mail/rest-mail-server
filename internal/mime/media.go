package mime

import (
	"hash/maphash"
	stdmime "mime"
	"slices"
	"strings"
	"unicode"
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
	if strings.Count(value, ";") > manyParameters {
		var ok bool
		if kept, ok = keptMediaParams(value); !ok {
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
// repeated with another value.
func keptMediaParams(value string) (string, bool) {
	base, _, _ := strings.Cut(value, ";")
	var kept strings.Builder
	kept.WriteString(base)
	seed := maphash.MakeSeed()
	names := make([]uint64, 0, strings.Count(value, ";"))
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
	ok := eachParam(func(key, spelled, _ string) {
		if keptMediaName(key) {
			kept.WriteString(spelled)
			return
		}
		names = append(names, lowerHash(seed, key))
	})
	if !ok {
		return "", false
	}
	slices.Sort(names)
	if repeatedHash(names) {
		// A name came twice, which is only an error when its values differ.
		values := map[uint64]uint64{}
		same := true
		eachParam(func(key, _, text string) {
			if keptMediaName(key) {
				return
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
			return "", false
		}
	}
	return kept.String(), true
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
